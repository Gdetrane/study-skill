package core

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAVersionThatComesBackIsNotAConflict: setting a goal, clearing it, then
// changing something else returns topic.toml to an earlier version on one
// machine. That is a sequence, not two machines changing one version.
func TestAVersionThatComesBackIsNotAConflict(t *testing.T) {
	m := newTopic(t)
	m.update(t, TopicChanges{Goal: ptr("G")})
	m.update(t, TopicChanges{Goal: ptr("")})
	m.update(t, TopicChanges{Title: ptr("Other")})
	m.update(t, TopicChanges{Goal: ptr("G")})
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Flags) != 0 {
		t.Errorf("sequential writes on one machine flagged: %+v", topic.Flags)
	}
	checkChain(t, filepath.Join(m.home, "c"))
}

// TestACompleteEventWithoutItsNewlineIsKept: an editor or a hand-resolved
// merge can drop the final newline; the Event is still applied, and the
// next write adds the newline back instead of truncating the Event.
func TestACompleteEventWithoutItsNewlineIsKept(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.update(t, TopicChanges{Title: ptr("T1")})
	path := filepath.Join(dir, historyFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSuffix(string(data), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := replayFolder(t, dir); len(s.applied) != 2 {
		t.Errorf("replay applied %d Events, want the last one too", len(s.applied))
	}
	m.update(t, TopicChanges{Goal: ptr("G")})
	if got := historyLines(t, dir); len(got) != 3 || !strings.Contains(got[1], `"title":"T1"`) {
		t.Errorf("History:\n%s", strings.Join(got, ""))
	}
	checkChain(t, dir)
	if !strings.Contains(m.logs.String(), "added the missing newline") {
		t.Errorf("logs:\n%s", m.logs)
	}
}

// TestADamagedLineInTheMiddleIsSkippedAndFlagged runs real git: machine A
// commits a History whose last line was cut off (as an older binary or a
// manual commit could), machine B writes and checkpoints, and A merges B.
// git's union merge moves the cut-off line into the middle of the file. The
// Topic stays readable and writable; the line is flagged, never deleted.
func TestADamagedLineInTheMiddleIsSkippedAndFlagged(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	dirA := filepath.Join(a.home, "c")
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", dirA, "c")

	const fragment = `{"format":1,"id":"a002","ty`
	appendToHistory(t, dirA, fragment)
	git(t, dirA, "commit", "-qam", "a commit made outside Lamplight")
	b.update(t, TopicChanges{Goal: ptr("G")})
	takeCheckpoint(t, b)
	git(t, dirA, "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(b.home, "c"), "main")

	lines := historyLines(t, dirA)
	i := slices.Index(lines, fragment+"\n")
	if i < 0 || i == len(lines)-1 {
		t.Fatalf("the merge did not move the cut-off line into the middle:\n%s", strings.Join(lines, ""))
	}
	topic, err := a.readTopic("c")
	if err != nil {
		t.Fatalf("reading after the merge: %v", err)
	}
	if topic.Goal != "G" || len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagDamagedLine {
		t.Fatalf("topic = %+v", topic)
	}
	a.update(t, TopicChanges{Title: ptr("Still writable")})
	if !slices.Contains(historyLines(t, dirA), fragment+"\n") {
		t.Error("the damaged line was deleted")
	}

	res, err := a.DismissFlag(ctx, "c", topic.Flags[0].ID, false)
	if err != nil || !res.Changed {
		t.Fatalf("DismissFlag = %+v, %v", res, err)
	}
	if topic, _ = a.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags after dismissing: %+v", topic.Flags)
	}
}

// TestANewerEventIsHeldAndBlocksWrites: a History with an Event from a newer
// version of study can be read, with the Event flagged, but not written.
func TestANewerEventIsHeldAndBlocksWrites(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	appendToHistory(t, dir, `{"format":2,"id":"zz","time":"2026-10-01T10:00:00Z","kind":"from the future"}`+"\n")
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatalf("reading a Topic with a newer Event: %v", err)
	}
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagHeldEvent || !slices.Equal(topic.Flags[0].Events, []string{"zz"}) {
		t.Errorf("flags = %+v", topic.Flags)
	}
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")}); CodeOf(err) != CodeNewerFormat {
		t.Errorf("writing: err = %v, want newer_format", err)
	}
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X"), DryRun: true}); CodeOf(err) != CodeNewerFormat {
		t.Errorf("dry run: err = %v, want newer_format", err)
	}
}

// TestANewerEventWithChangedFieldTypesIsStillNewer: a newer format may change
// the type of any field but format, and such a line must still block writes
// and never be treated (or truncated) as a damaged line.
func TestANewerEventWithChangedFieldTypesIsStillNewer(t *testing.T) {
	for name, line := range map[string]string{
		"time as a number": `{"format":2,"id":"zz","time":1790000000,"type":"x"}`,
		"id as an object":  `{"format":2,"id":{"machine":"a","n":1},"time":"2026-10-01T10:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := newTopic(t)
			dir := filepath.Join(m.home, "c")
			appendToHistory(t, dir, line+"\n")
			topic, err := m.readTopic("c")
			if err != nil {
				t.Fatalf("reading: %v", err)
			}
			if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagHeldEvent {
				t.Errorf("flags = %+v, want one held Event", topic.Flags)
			}
			if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")}); CodeOf(err) != CodeNewerFormat {
				t.Errorf("writing: err = %v, want newer_format", err)
			}
		})
	}
	// Without its final newline, the same line is a complete newer Event,
	// never an interrupted append to truncate.
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	line := `{"format":2,"id":"zz","time":1790000000,"type":"x"}`
	appendToHistory(t, dir, line)
	_, _ = m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")})
	data, err := os.ReadFile(filepath.Join(dir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), line) {
		t.Errorf("the newer Event was truncated from the History:\n%s", data)
	}
}

// TestLinesThatAreNotEventsAreFlagged covers the format rules: every Event
// has a format, an id, a type, a time and a wall time.
func TestLinesThatAreNotEventsAreFlagged(t *testing.T) {
	created := `{"format":1,"id":"e1","time":"2026-10-01T09:30:00Z","wall":"2026-10-01T09:30:00Z","type":"topic.created","data":{"title":"C"}}`
	for name, line := range map[string]string{
		"no format":    strings.Replace(created, `"format":1,`, "", 1),
		"format zero":  strings.Replace(created, `"format":1`, `"format":0`, 1),
		"no wall time": strings.Replace(created, `"wall":"2026-10-01T09:30:00Z",`, "", 1),
		"not JSON":     `{"format":1,"id":"e2","ty`,
		"not an Event": `["format", 1]`,
	} {
		s := replayLines(t, []string{created + "\n", line + "\n"})
		if len(s.applied) != 1 || !slices.Equal(flagKinds(s.flags), []string{FlagDamagedLine}) {
			t.Errorf("%s: applied %d, flags %+v", name, len(s.applied), s.flags)
		}
	}
}

// TestEventsKeepTheirWallTime: the hybrid logical clock orders Events, and
// each Event also keeps its writer's real time, which a clock that ran ahead
// on another machine never moves.
func TestEventsKeepTheirWallTime(t *testing.T) {
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	ahead := t0.Add(365 * 24 * time.Hour)
	b := newMachine(t, t.TempDir(), "b", ahead)
	syncTopic(t, a, b)
	b.update(t, TopicChanges{Title: ptr("on B")})
	syncTopic(t, b, a)
	a.setClock(t0.Add(time.Hour))
	a.update(t, TopicChanges{Title: ptr("on A")})

	s := replayFolder(t, filepath.Join(a.home, "c"))
	last := s.applied[len(s.applied)-1]
	if !last.Time.After(ahead) || !last.Wall.Equal(t0.Add(time.Hour)) {
		t.Errorf("A's Event: time %v, wall %v; want time after B's and wall A's own clock", last.Time, last.Wall)
	}

	// On A, the History is dated a year ahead: status says so, once.
	topic, err := a.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagClockAhead {
		t.Fatalf("flags = %+v, want the clock that ran ahead", topic.Flags)
	}
	if _, err := a.DismissFlag(ctx, "c", topic.Flags[0].ID, false); err != nil {
		t.Fatal(err)
	}
	a.update(t, TopicChanges{Goal: ptr("G")})
	if topic, _ = a.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("the dismissed flag came back after later writes: %+v", topic.Flags)
	}
}

func TestDismissFlag(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	appendToHistory(t, dir, `{"format":1,"id":"zz1","time":"2026-10-01T09:31:00Z","wall":"2026-10-01T09:31:00Z","type":"card.reviewed","data":{}}`+"\n")
	topic, err := m.readTopic("c")
	if err != nil || len(topic.Flags) != 1 {
		t.Fatalf("topic = %+v, %v", topic, err)
	}
	flag := topic.Flags[0]
	settings, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}

	dry, err := m.DismissFlag(ctx, "c", flag.ID, true)
	if err != nil || !dry.Changed || !dry.DryRun || dry.Flag.ID != flag.ID {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	if topic, _ = m.readTopic("c"); len(topic.Flags) != 1 {
		t.Error("a dry run dismissed the flag")
	}
	res, err := m.DismissFlag(ctx, "c", flag.ID, false)
	if err != nil || !res.Changed || res.Flag.Kind != FlagHeldEvent {
		t.Fatalf("DismissFlag = %+v, %v", res, err)
	}
	if topic, _ = m.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags after dismissing: %+v", topic.Flags)
	}
	again, err := m.DismissFlag(ctx, "c", flag.ID, false)
	if err != nil || again.Changed {
		t.Errorf("dismissing again = %+v, %v; want no change", again, err)
	}
	if now, _ := os.ReadFile(filepath.Join(dir, topicFile)); string(now) != string(settings) {
		t.Error("dismissing a flag changed content")
	}

	for name, tc := range map[string]struct {
		id   string
		want ErrorCode
	}{
		"not an id":  {"Z!", CodeInvalidArgument},
		"no such id": {"0123456789", CodeNotFound},
	} {
		if _, err := m.DismissFlag(ctx, "c", tc.id, false); CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}

	// A flag that goes away by itself cannot be dismissed.
	m.gating = func(item string) bool { return item == topicFile }
	if err := os.WriteFile(filepath.Join(dir, topicFile), []byte("format = 1\ntitle = \"Edited\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, _ = m.readTopic("c")
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagEditedOutside {
		t.Fatalf("flags = %+v", topic.Flags)
	}
	if _, err := m.DismissFlag(ctx, "c", topic.Flags[0].ID, false); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("dismissing edited_outside: err = %v, want failed_precondition", err)
	}
}

// TestFlagIDsAreStable: the same History gives the same flag IDs, so a
// dismissal recorded on one machine applies on another.
func TestFlagIDsAreStable(t *testing.T) {
	lines := []string{
		`{"format":1,"id":"e1","time":"2026-10-01T09:30:00Z","wall":"2026-10-01T09:30:00Z","type":"topic.created","data":{"title":"C"}}` + "\n",
		`{"format":1,"id":"e2","time":"2026-10-01T09:31:00Z","wall":"2026-10-01T09:31:00Z","type":"card.reviewed","data":{}}` + "\n",
		"damaged\n",
	}
	first := replayLines(t, lines).flags
	second := replayLines(t, []string{lines[2], lines[1], lines[0]}).flags
	ids := func(flags []Flag) []string {
		var out []string
		for _, f := range flags {
			out = append(out, f.ID)
		}
		slices.Sort(out)
		return out
	}
	if len(first) != 2 || !slices.Equal(ids(first), ids(second)) {
		t.Errorf("flag IDs differ between orders: %v and %v", ids(first), ids(second))
	}
}
