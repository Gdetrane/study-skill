package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

// lockedBuffer collects log output from concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// machine is one computer using a Study home: a Core with its own clock and
// its own Event IDs, so tests can play several machines against each other.
type machine struct {
	*Core
	home string
	logs *lockedBuffer
	mu   sync.Mutex
	at   time.Time
}

func newMachine(t *testing.T, home, idPrefix string, at time.Time) *machine {
	t.Helper()
	m := &machine{home: home, logs: &lockedBuffer{}, at: at}
	var n atomic.Int64
	c, err := Open(Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
		Now: func() time.Time {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.at
		},
		NewID:  func() string { return fmt.Sprintf("%s%03d", idPrefix, n.Add(1)) },
		Logger: slog.New(slog.NewTextHandler(m.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Core = c
	return m
}

func (m *machine) setClock(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.at = at
}

// newTopic returns a machine with one Topic, "c".
func newTopic(t *testing.T) *machine {
	t.Helper()
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	return m
}

func ptr(s string) *string { return &s }

func (m *machine) update(t *testing.T, changes TopicChanges) TopicUpdate {
	t.Helper()
	res, err := m.UpdateTopic(context.Background(), "c", changes)
	if err != nil {
		t.Fatalf("UpdateTopic(%+v): %v", changes, err)
	}
	return res
}

// replayed reads and replays a Topic's History.
func replayFolder(t *testing.T, dir string) *replayed {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	h, err := readHistory(root, filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	return replayHistory(h)
}

// checkChain asserts that the Events editing topic.toml form one unbroken
// chain of versions ending at the file on disk: no write was lost or
// interleaved with another.
func checkChain(t *testing.T, dir string) *replayed {
	t.Helper()
	s := replayFolder(t, dir)
	last := ""
	for _, ev := range s.applied {
		for _, it := range ev.Items {
			if it.Item != topicFile {
				continue
			}
			if it.Before != last {
				t.Fatalf("Event %s changed topic.toml from %.15s, but the previous Event left %.15s", ev.ID, it.Before, last)
			}
			last = it.After
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := contentHash(data, true); got != last {
		t.Fatalf("topic.toml is %.15s, but the History recorded %.15s", got, last)
	}
	if len(s.flags) != 0 {
		t.Fatalf("flags: %+v", s.flags)
	}
	return s
}

var errCrash = errors.New("simulated crash")

// crashOnce makes the next write stop at point, as a crash would.
func crashOnce(point string) func(string) error {
	var done atomic.Bool
	return func(p string) error {
		if p == point && done.CompareAndSwap(false, true) {
			return errCrash
		}
		return nil
	}
}

func readSettings(t *testing.T, dir string) topicSettings {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseTopicSettings(data, topicFile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func flagKinds(flags []Flag) []string {
	var kinds []string
	for _, f := range flags {
		kinds = append(kinds, f.Kind)
	}
	return kinds
}

func TestNextEventTime(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wall, latest time.Time
		want         time.Time
	}{
		{"empty History", t0.Add(123 * time.Nanosecond), time.Time{}, t0},
		{"wall clock ahead", t0.Add(time.Second), t0, t0.Add(time.Second)},
		{"same instant", t0, t0, t0.Add(clockTick)},
		{"another machine ahead", t0, t0.Add(time.Hour), t0.Add(time.Hour + clockTick)},
	} {
		if got := nextEventTime(tc.wall, tc.latest); !got.Equal(tc.want) {
			t.Errorf("%s: nextEventTime = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEventsRecordItemVersions(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	res := m.update(t, TopicChanges{Title: ptr("Systems programming in C")})
	if !res.Changed || res.Topic.Title != "Systems programming in C" {
		t.Fatalf("update = %+v", res)
	}
	s := checkChain(t, dir)
	if got := s.applied[len(s.applied)-1]; got.Type != eventTopicUpdated || got.Time.Equal(s.applied[0].Time) {
		t.Errorf("the update is %s at %v; it must sort after the creation at %v", got.Type, got.Time, s.applied[0].Time)
	}

	// Asking again for the same title changes nothing and records nothing.
	again := m.update(t, TopicChanges{Title: ptr("Systems programming in C")})
	if again.Changed {
		t.Error("an update to the current values reported a change")
	}
	if n := len(replayFolder(t, dir).applied); n != 2 {
		t.Errorf("History has %d Events after a no-op update, want 2", n)
	}
	if _, err := os.Stat(filepath.Join(m.home, intentPath("c"))); !os.IsNotExist(err) {
		t.Errorf("the intent marker was left behind (err = %v)", err)
	}
}

func TestRecoveryFinishesInterruptedWrites(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		point        string
		titleApplied bool // whether the interrupted Event reached the History
		log          string
	}{
		{crashAfterIntent, false, "never reached the History"},
		{crashAfterEvent, true, "finished an interrupted write"},
		{crashAfterItem, true, ""},
		{crashBeforeClear, true, ""},
	} {
		t.Run(tc.point, func(t *testing.T) {
			m := newTopic(t)
			dir := filepath.Join(m.home, "c")
			m.crash = crashOnce(tc.point)
			_, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("Systems programming in C")})
			if !errors.Is(err, errCrash) {
				t.Fatalf("err = %v, want the simulated crash", err)
			}
			m.crash = nil

			// Status reports the interrupted write without repairing it.
			topic, err := m.readTopic("c")
			if err != nil {
				t.Fatal(err)
			}
			if kinds := flagKinds(topic.Flags); !slices.Equal(kinds, []string{FlagInterruptedWrite}) {
				t.Fatalf("flags = %v, want an interrupted write", kinds)
			}

			// The next write finishes the interrupted one, then makes its own.
			m.update(t, TopicChanges{Goal: ptr("Write a shell")})
			settings := readSettings(t, dir)
			wantTitle := "C"
			if tc.titleApplied {
				wantTitle = "Systems programming in C"
			}
			if settings.Title != wantTitle || settings.Goal != "Write a shell" {
				t.Errorf("topic.toml = %+v, want title %q and the new goal", settings, wantTitle)
			}
			s := checkChain(t, dir)
			if want := map[bool]int{true: 3, false: 2}[tc.titleApplied]; len(s.applied) != want {
				t.Errorf("History has %d Events, want %d", len(s.applied), want)
			}
			if topic, _ := m.readTopic("c"); len(topic.Flags) != 0 {
				t.Errorf("flags after recovery: %+v", topic.Flags)
			}
			if !strings.Contains(m.logs.String(), tc.log) {
				t.Errorf("logs lack %q:\n%s", tc.log, m.logs)
			}
		})
	}
}

func TestRecoveryKeepsAHandEdit(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("From Lamplight")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	handEdit := "format = 1\ntitle = \"From the learner\"\n"
	if err := os.WriteFile(filepath.Join(dir, topicFile), []byte(handEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	m.update(t, TopicChanges{Goal: ptr("Write a shell")})
	if settings := readSettings(t, dir); settings.Title != "From the learner" || settings.Goal != "Write a shell" {
		t.Errorf("topic.toml = %+v: recovery must keep the hand edit", settings)
	}
	if !strings.Contains(m.logs.String(), "kept a hand edit") {
		t.Errorf("the kept hand edit was not logged:\n%s", m.logs)
	}
}

func TestRecoveryStopsOnADamagedItem(t *testing.T) {
	ctx := context.Background()
	for name, damage := range map[string]func(path string) error{
		"missing":    os.Remove,
		"unreadable": func(path string) error { return os.WriteFile(path, []byte("title = [\n"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			m := newTopic(t)
			dir := filepath.Join(m.home, "c")
			path := filepath.Join(dir, topicFile)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m.crash = crashOnce(crashAfterEvent)
			if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("Interrupted")}); !errors.Is(err, errCrash) {
				t.Fatal(err)
			}
			m.crash = nil
			if err := damage(path); err != nil {
				t.Fatal(err)
			}

			_, err = m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("Write a shell")})
			if CodeOf(err) != CodeCorrupt {
				t.Fatalf("err = %v, want corrupt", err)
			}
			if _, err := os.Stat(filepath.Join(m.home, intentPath("c"))); err != nil {
				t.Errorf("the intent marker must stay until the file is fixed: %v", err)
			}

			// Restoring the file lets the next write finish both changes.
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
			m.update(t, TopicChanges{Goal: ptr("Write a shell")})
			if settings := readSettings(t, dir); settings.Title != "Interrupted" || settings.Goal != "Write a shell" {
				t.Errorf("topic.toml = %+v", settings)
			}
			checkChain(t, dir)
		})
	}
}

func TestACutOffEventIsDroppedByTheNextWrite(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	history := filepath.Join(dir, historyFile)

	// A crash after the marker, while the Event was half written.
	m.crash = crashOnce(crashAfterIntent)
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("Half written")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	f, err := os.OpenFile(history, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"format":1,"id":"id002","time":"2026-10-01T09:30:00.000001Z","ty`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if topic, err := m.readTopic("c"); err != nil || topic.Title != "C" {
		t.Fatalf("reading with a cut-off Event: %+v, %v", topic, err)
	}
	m.update(t, TopicChanges{Goal: ptr("Write a shell")})

	data, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"ty{`)) || !bytes.HasSuffix(data, []byte("\n")) || bytes.Count(data, []byte("\n")) != 2 {
		t.Errorf("the fragment was not dropped:\n%s", data)
	}
	if settings := readSettings(t, dir); settings.Title != "C" || settings.Goal != "Write a shell" {
		t.Errorf("topic.toml = %+v", settings)
	}
	checkChain(t, dir)
	if logs := m.logs.String(); !strings.Contains(logs, "dropped the unfinished last line") || !strings.Contains(logs, "never reached the History") {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestAHandEditAfterAnEventSurvivesReload(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.update(t, TopicChanges{Title: ptr("From Lamplight")})
	if err := os.WriteFile(filepath.Join(dir, topicFile), []byte("format = 1\ntitle = \"From the learner\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if topic.Title != "From the learner" || len(topic.Flags) != 0 {
		t.Errorf("topic = %+v: content is authoritative for text, and a hand edit needs no acknowledgement", topic)
	}

	// Content that gates progress is compared with the version its last
	// Event recorded.
	m.gating = func(item string) bool { return item == topicFile }
	topic, _ = m.readTopic("c")
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagEditedOutside || topic.Flags[0].Item != topicFile {
		t.Fatalf("flags = %+v, want topic.toml edited outside Lamplight", topic.Flags)
	}
	m.update(t, TopicChanges{Goal: ptr("Write a shell")})
	if topic, _ = m.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags after Lamplight recorded the version: %+v", topic.Flags)
	}
}

func TestSymbolicLinksAreRefused(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	if err := os.WriteFile(outside, []byte("title = \"Elsewhere\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, topicFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, topicFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("Escaped")}); CodeOf(err) != CodeCorrupt {
		t.Errorf("update through a symlink: err = %v, want corrupt", err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "title = \"Elsewhere\"\n" {
		t.Errorf("the file outside the Topic changed: %q", data)
	}
}

// TestWritersInOneProcess plays the CLI and the MCP server: two Cores over
// one Study home writing to one Topic at once.
func TestWritersInOneProcess(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	setup := newMachine(t, home, "setup", t0)
	if _, err := setup.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, name := range []string{"cli", "mcp"} {
		c, err := Open(Options{Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home}[key] }, Dir: home})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				if _, err := c.UpdateTopic(ctx, "c", TopicChanges{Title: ptr(fmt.Sprintf("%s %d", name, i))}); err != nil {
					t.Errorf("%s update %d: %v", name, i, err)
				}
			}
		}()
	}
	wg.Wait()
	if s := checkChain(t, filepath.Join(home, "c")); len(s.applied) != 41 {
		t.Errorf("History has %d Events, want 41", len(s.applied))
	}
}

// TestWritersInSeparateProcesses runs writers as real processes, so the
// Topic lock is exercised across processes as the CLI and the MCP server
// would. Each process runs TestWriterProcess.
func TestWritersInSeparateProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts several processes")
	}
	ctx := context.Background()
	home := t.TempDir()
	setup := newMachine(t, home, "setup", t0)
	if _, err := setup.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	const processes, writes = 3, 10
	var wg sync.WaitGroup
	for p := range processes {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWriterProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "LAMPLIGHT_TEST_WRITER="+strconv.Itoa(p),
			"LAMPLIGHT_TEST_WRITES="+strconv.Itoa(writes), "STUDY_HOME="+home)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("writer process %d: %v\n%s", p, err, out)
			}
		}()
	}
	wg.Wait()
	if s := checkChain(t, filepath.Join(home, "c")); len(s.applied) != 1+processes*writes {
		t.Errorf("History has %d Events, want %d", len(s.applied), 1+processes*writes)
	}
}

// TestWriterProcess is the body of one writer process started by
// TestWritersInSeparateProcesses; on its own it does nothing.
func TestWriterProcess(t *testing.T) {
	name := os.Getenv("LAMPLIGHT_TEST_WRITER")
	if name == "" {
		t.Skip("run by TestWritersInSeparateProcesses")
	}
	writes, _ := strconv.Atoi(os.Getenv("LAMPLIGHT_TEST_WRITES"))
	c, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range writes {
		if _, err := c.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr(fmt.Sprintf("process %s write %d", name, i))}); err != nil {
			t.Fatal(err)
		}
	}
}

// historyLines returns the lines of a Topic's History.
func historyLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitAfter(string(data), "\n")[:bytes.Count(data, []byte("\n"))]
}

// replayLines replays History lines written to a scratch Topic folder.
func replayLines(t *testing.T, lines []string) *replayed {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "c")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return replayFolder(t, dir)
}

// sync copies a Topic's content and History from one machine to another, as
// git would when the learner moves between machines one at a time.
func syncTopic(t *testing.T, from, to *machine) {
	t.Helper()
	dst := filepath.Join(to.home, "c")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{topicFile, gitattributes, historyFile} {
		data, err := os.ReadFile(filepath.Join(from.home, "c", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// unionMerge merges two Histories the way git's union driver does: lines
// both sides share, then ours, then theirs.
func unionMerge(ours, theirs []string) []string {
	merged := slices.Clone(ours)
	for _, line := range theirs {
		if !slices.Contains(ours, line) {
			merged = append(merged, line)
		}
	}
	return merged
}

type replaySummary struct {
	Applied []string
	Flags   []Flag
	Latest  time.Time
	Created time.Time
}

func summarize(s *replayed) replaySummary {
	sum := replaySummary{Flags: s.flags, Latest: s.latest, Created: s.created}
	for _, ev := range s.applied {
		sum.Applied = append(sum.Applied, ev.ID)
	}
	return sum
}

func TestUnionMergedHistoriesReplayTheSameInBothDirections(t *testing.T) {
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Hour)) // B's clock is an hour ahead.
	syncTopic(t, a, b)

	// The learner moves to B, then back to A. A's clock is behind the
	// Event B wrote, yet A's next Event sorts after it.
	b.update(t, TopicChanges{Title: ptr("C on B")})
	syncTopic(t, b, a)
	a.setClock(t0.Add(time.Minute))
	a.update(t, TopicChanges{Goal: ptr("Write a shell")})
	s := checkChain(t, filepath.Join(a.home, "c"))
	if ids := summarize(s).Applied; !slices.Equal(ids, []string{"a001", "b001", "a002"}) {
		t.Fatalf("replay order = %v: the Event written on A after reading B's must sort after it", ids)
	}
	syncTopic(t, a, b)

	// Now both machines change the title without syncing first.
	a.update(t, TopicChanges{Title: ptr("C on A")})
	b.update(t, TopicChanges{Title: ptr("C on B again")})
	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))

	aIntoB := summarize(replayLines(t, unionMerge(theirs, ours)))
	bIntoA := summarize(replayLines(t, unionMerge(ours, theirs)))
	// A union merge can repeat a shared line; each Event still applies once.
	doubled := summarize(replayLines(t, append(slices.Clone(ours), theirs...)))
	if !reflect.DeepEqual(aIntoB, bIntoA) || !reflect.DeepEqual(aIntoB, doubled) {
		t.Fatalf("merges replay differently:\n A into B: %+v\n B into A: %+v\n doubled:  %+v", aIntoB, bIntoA, doubled)
	}
	if want := []string{"a001", "b001", "a002", "a003", "b002"}; !slices.Equal(aIntoB.Applied, want) {
		t.Errorf("applied = %v, want every Event from both machines: %v", aIntoB.Applied, want)
	}
	if len(aIntoB.Flags) != 1 || aIntoB.Flags[0].Kind != FlagConflict || aIntoB.Flags[0].Item != topicFile ||
		!slices.Equal(aIntoB.Flags[0].Events, []string{"a003", "b002"}) {
		t.Errorf("flags = %+v, want one conflict on topic.toml between a003 and b002", aIntoB.Flags)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	for i := range 5 {
		m.update(t, TopicChanges{Title: ptr(fmt.Sprintf("Title %d", i))})
	}
	lines := historyLines(t, dir)
	// A repeated line, an Event this version doesn't know, and an Event
	// that refers to nothing known.
	lines = append(lines, lines[2],
		`{"format":1,"id":"zz1","time":"2026-10-01T09:31:00Z","wall":"2026-10-01T09:31:00Z","type":"card.reviewed","data":{"card":"c1.x"}}`+"\n")

	want := summarize(replayLines(t, lines))
	if len(want.Applied) != 6 || !slices.Equal(flagKinds(want.Flags), []string{FlagHeldEvent}) {
		t.Fatalf("replay = %+v", want)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		shuffled := slices.Clone(lines)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := summarize(replayLines(t, shuffled)); !reflect.DeepEqual(got, want) {
			t.Fatalf("a shuffled History replays differently:\n got  %+v\n want %+v", got, want)
		}
	}
}

func TestReplayHoldsEventsUntilWhatTheyReferToIsKnown(t *testing.T) {
	created := `{"format":1,"id":"e1","time":"2026-10-01T09:30:00Z","wall":"2026-10-01T09:30:00Z","type":"topic.created","data":{"title":"C"}}` + "\n"
	// Sorts before the creation, as a skewed clock without the hybrid
	// logical clock could make it.
	early := `{"format":1,"id":"e0","time":"2026-10-01T09:00:00Z","wall":"2026-10-01T09:00:00Z","type":"topic.updated","data":{"goal":"G"}}` + "\n"

	resolved := summarize(replayLines(t, []string{early, created}))
	if !slices.Equal(resolved.Applied, []string{"e1", "e0"}) || len(resolved.Flags) != 0 {
		t.Errorf("an Event held until its Topic was created: %+v", resolved)
	}

	orphan := summarize(replayLines(t, []string{early}))
	if len(orphan.Applied) != 0 || !slices.Equal(flagKinds(orphan.Flags), []string{FlagHeldEvent}) {
		t.Errorf("an Event whose Topic never appears: %+v", orphan)
	}

	clash := strings.Replace(created, `"title":"C"`, `"title":"Not C"`, 1)
	dup := summarize(replayLines(t, []string{created, clash}))
	if len(dup.Applied) != 1 || !slices.Equal(flagKinds(dup.Flags), []string{FlagConflict}) {
		t.Errorf("two different Events with one ID: %+v", dup)
	}
}
