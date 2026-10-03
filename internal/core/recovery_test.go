package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitIdentity gives git a fixed identity through a temporary HOME, as a
// learner's global configuration would, and hides the developer's own.
func gitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	config := "[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

// git runs git in dir and fails the test if it fails.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCommand(context.Background(), dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// pauseAt makes writes stop at point until the returned release is called;
// reached is closed when a write gets there.
func pauseAt(point string) (hook func(string) error, reached <-chan struct{}, release func()) {
	r, done := make(chan struct{}), make(chan struct{})
	var once bool
	hook = func(p string) error {
		if p == point && !once {
			once = true
			close(r)
			<-done
		}
		return nil
	}
	return hook, r, func() { close(done) }
}

func takeCheckpoint(t *testing.T, m *machine) CheckpointResult {
	t.Helper()
	res, err := m.Checkpoint(context.Background(), CheckpointSpec{Topic: "c", Role: "agent"})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	return res
}

// TestCheckpointWaitsForAWriteInProgress plays the MCP server taking a
// Checkpoint while the CLI is half way through a write: the Checkpoint waits
// for the lock, so it never commits an Event without its content.
func TestCheckpointWaitsForAWriteInProgress(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	m := newTopic(t)
	hook, reached, release := pauseAt(crashAfterEvent)
	m.crash = hook
	wrote := make(chan error)
	go func() {
		_, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("Systems programming in C")})
		wrote <- err
	}()
	<-reached

	other := newMachine(t, m.home, "o", t0)
	type result struct {
		res CheckpointResult
		err error
	}
	took := make(chan result)
	go func() {
		res, err := other.Checkpoint(ctx, CheckpointSpec{Topic: "c", Role: "agent"})
		took <- result{res, err}
	}()
	select {
	case r := <-took:
		t.Fatalf("the Checkpoint did not wait for the write in progress: %+v, %v", r.res, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	r := <-took
	if r.err != nil {
		t.Fatalf("Checkpoint: %v", r.err)
	}
	res := r.res
	dir := filepath.Join(m.home, "c")
	if toml := git(t, dir, "show", res.Commit+":"+topicFile); !strings.Contains(toml, "Systems programming in C") {
		t.Errorf("the Checkpoint committed topic.toml without the write's content:\n%s", toml)
	}
}

// TestCheckpointFinishesAnInterruptedWriteFirst: after a crash, a Checkpoint
// commits the finished write, not an Event without its content, and leaves
// the crash's temporary files out.
func TestCheckpointFinishesAnInterruptedWriteFirst(t *testing.T) {
	gitIdentity(t)
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("Interrupted")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	leftover := filepath.Join(dir, tempPrefix(topicFile)+"left-by-a-crash")
	if err := os.WriteFile(leftover, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	dry, err := m.Checkpoint(context.Background(), CheckpointSpec{Topic: "c", Role: "agent", DryRun: true})
	if CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("dry run with an interrupted write = %+v, %v; want failed_precondition", dry, err)
	}

	res := takeCheckpoint(t, m)
	if toml := git(t, dir, "show", res.Commit+":"+topicFile); !strings.Contains(toml, "Interrupted") {
		t.Errorf("committed topic.toml lacks the interrupted write's title:\n%s", toml)
	}
	if files := git(t, dir, "ls-tree", "--name-only", res.Commit); strings.Contains(files, "lamplight-tmp") {
		t.Errorf("the Checkpoint committed a temporary file:\n%s", files)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("recovery left the temporary file behind (err = %v)", err)
	}
	if hasIntentFile(t, m) {
		t.Error("the intent marker is still there after the Checkpoint")
	}
	checkChain(t, dir)
}

// TestCheckpointDropsACutOffEvent: a Checkpoint never commits a History
// whose last line is cut off, where a union merge would bury it.
func TestCheckpointDropsACutOffEvent(t *testing.T) {
	gitIdentity(t)
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterIntent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("Half written")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	appendToHistory(t, dir, `{"format":1,"id":"id002","ty`)

	res := takeCheckpoint(t, m)
	if hist := git(t, dir, "show", res.Commit+":"+historyFile); strings.Contains(hist, `"ty`+"\n") || !strings.HasSuffix(hist, "\n") ||
		strings.Count(hist, "\n") != 1 {
		t.Errorf("the Checkpoint committed a cut-off Event:\n%s", hist)
	}
	if !strings.Contains(m.logs.String(), "dropped the unfinished last line") {
		t.Errorf("logs:\n%s", m.logs)
	}
}

func hasIntentFile(t *testing.T, m *machine) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(m.home, intentPath("c")))
	return err == nil
}

func appendToHistory(t *testing.T, dir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, historyFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryLeavesASupersededEvent: machine A crashes after recording a
// title; on machine B the learner changes the title twice, ending where it
// started. Back on A, the interrupted Event matches the item's version but
// later Events changed it since, so recovery leaves it.
func TestRecoveryLeavesASupersededEvent(t *testing.T) {
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	a.crash = crashOnce(crashAfterEvent)
	if _, err := a.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	a.crash = nil
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	syncTopic(t, a, b)
	b.update(t, TopicChanges{Title: ptr("Y")})
	b.update(t, TopicChanges{Title: ptr("C")})
	syncTopic(t, b, a)

	a.setClock(t0.Add(time.Hour))
	a.update(t, TopicChanges{Goal: ptr("G")})
	if got := readSettings(t, filepath.Join(a.home, "c")); got.Title != "C" || got.Goal != "G" {
		t.Errorf("topic.toml = %+v: recovery re-applied a superseded Event", got)
	}
	if !strings.Contains(a.logs.String(), "superseded") {
		t.Errorf("logs:\n%s", a.logs)
	}
}

// TestRecoveryAfterAnUpgradeThatWritesItemsDifferently: the binary that
// finishes an interrupted write produces different bytes from the one that
// recorded the Event. Nobody edited the item, so the change still applies,
// with a warning, rather than blocking the Topic for good.
func TestRecoveryAfterAnUpgradeThatWritesItemsDifferently(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	// Stand in for the older binary's output: the recorded after hash no
	// longer matches what this binary writes.
	path := filepath.Join(dir, historyFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	last := len(lines) - 2
	s := replayFolder(t, dir)
	recorded := s.applied[len(s.applied)-1].Items[0].After
	lines[last] = bytes.Replace(lines[last], []byte(`"after":"`+recorded), []byte(`"after":"sha256:0000`), 1)
	if err := os.WriteFile(path, bytes.Join(lines, nil), 0o644); err != nil {
		t.Fatal(err)
	}

	m.update(t, TopicChanges{Goal: ptr("G")})
	if got := readSettings(t, dir); got.Title != "X" || got.Goal != "G" {
		t.Errorf("topic.toml = %+v", got)
	}
	if !strings.Contains(m.logs.String(), "writes differently") {
		t.Errorf("logs:\n%s", m.logs)
	}
}

// TestRecoveryCanItselfBeInterrupted: a crash while recovery is finishing
// a write leaves the marker, and the next write finishes the job.
func TestRecoveryCanItselfBeInterrupted(t *testing.T) {
	ctx := context.Background()
	for _, point := range []string{crashRecoveryAfterItem, crashRecoveryBeforeClear} {
		t.Run(point, func(t *testing.T) {
			m := newTopic(t)
			dir := filepath.Join(m.home, "c")
			m.crash = crashOnce(crashAfterEvent)
			if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, errCrash) {
				t.Fatal(err)
			}
			m.crash = crashOnce(point)
			if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("G")}); !errors.Is(err, errCrash) {
				t.Fatalf("err = %v, want the crash during recovery", err)
			}
			if !hasIntentFile(t, m) {
				t.Fatal("a crash during recovery cleared the marker")
			}
			m.crash = nil
			m.update(t, TopicChanges{Goal: ptr("G")})
			if got := readSettings(t, dir); got.Title != "X" || got.Goal != "G" {
				t.Errorf("topic.toml = %+v", got)
			}
			checkChain(t, dir)
		})
	}
}

// TestAWriteThatChangesNothingStillClearsAStaleMarker: the marker of a write
// that never reached the History is cleared even when the next write has
// nothing to record.
func TestAWriteThatChangesNothingStillClearsAStaleMarker(t *testing.T) {
	m := newTopic(t)
	m.crash = crashOnce(crashAfterIntent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("Never recorded")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	if res := m.update(t, TopicChanges{Title: ptr("C")}); res.Changed {
		t.Errorf("update = %+v, want no change", res)
	}
	if hasIntentFile(t, m) {
		t.Error("the marker of a write that never reached the History was left behind")
	}
}

// TestDryRunPlansAgainstTheRecoveredTopic: a dry run reports what the real
// run would do after finishing an interrupted write, and writes nothing.
func TestDryRunPlansAgainstTheRecoveredTopic(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	before, err := os.ReadFile(filepath.Join(dir, historyFile))
	if err != nil {
		t.Fatal(err)
	}

	same, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("X"), DryRun: true})
	if err != nil || same.Changed || same.Topic.Title != "X" {
		t.Errorf("dry run of the title the interrupted write sets = %+v, %v; want no change", same, err)
	}
	other, err := m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("G"), DryRun: true})
	if err != nil || !other.Changed || other.Topic.Title != "X" || other.Topic.Goal != "G" {
		t.Errorf("dry run = %+v, %v", other, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !hasIntentFile(t, m) || readSettings(t, dir).Title != "C" {
		t.Error("a dry run wrote to the Topic")
	}
	if real := m.update(t, TopicChanges{Title: ptr("X")}); real.Changed {
		t.Errorf("real run = %+v; the dry run said nothing would change", real)
	}
}

// TestAFailedWriteSaysTheChangeIsRecorded: when content cannot be written
// after the Event was recorded, the error says so, and the next write
// finishes the change once the problem is fixed.
func TestAFailedWriteSaysTheChangeIsRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not apply to root")
	}
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	_, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")})
	_ = os.Chmod(dir, 0o755)
	if err == nil || !strings.Contains(err.Error(), "the change is recorded") {
		t.Fatalf("err = %v, want it to say the change is recorded", err)
	}
	m.update(t, TopicChanges{Goal: ptr("G")})
	if got := readSettings(t, dir); got.Title != "X" || got.Goal != "G" {
		t.Errorf("topic.toml = %+v", got)
	}
}

func TestACancelledContextWritesNothing(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if n := len(historyLines(t, dir)); n != 1 || readSettings(t, dir).Title != "C" {
		t.Errorf("a cancelled write changed the Topic: %d Events", n)
	}
}

// TestStatusTellsAWriteInProgressFromAnInterruptedOne: a marker whose
// writer still holds the lock is a write in progress, not a crash.
func TestStatusTellsAWriteInProgressFromAnInterruptedOne(t *testing.T) {
	m := newTopic(t)
	hook, reached, release := pauseAt(crashAfterEvent)
	m.crash = hook
	wrote := make(chan error)
	go func() {
		_, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")})
		wrote <- err
	}()
	<-reached
	topic, err := newMachine(t, m.home, "o", t0).readTopic("c")
	release()
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Flags) != 0 {
		t.Errorf("status during a write in progress: flags = %+v", topic.Flags)
	}
}

// TestATopicFolderReachedThroughALinkIsRefused: locks and markers are kept
// by name, so a second name for a Topic would bypass them.
func TestATopicFolderReachedThroughALinkIsRefused(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	m := newTopic(t)
	if err := os.Symlink("c", filepath.Join(m.home, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := m.UpdateTopic(ctx, "alias", TopicChanges{Title: ptr("Through a link")}); CodeOf(err) != CodeCorrupt {
		t.Errorf("update through a link: err = %v, want corrupt", err)
	}
	if _, err := m.Checkpoint(ctx, CheckpointSpec{Topic: "alias", Role: "agent"}); CodeOf(err) != CodeCorrupt {
		t.Errorf("Checkpoint through a link: err = %v, want corrupt", err)
	}
	if _, err := m.readTopic("alias"); CodeOf(err) != CodeCorrupt {
		t.Errorf("read through a link: err = %v, want corrupt", err)
	}
	if got := readSettings(t, filepath.Join(m.home, "c")).Title; got != "C" {
		t.Errorf("title = %q", got)
	}
}

// TestUpdateKeepsSettingsItDoesNotKnow: settings a newer version of study
// added within the same format, or the learner's own, survive an update and
// an interrupted one.
func TestUpdateKeepsSettingsItDoesNotKnow(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	path := filepath.Join(dir, topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, "level = \"expert\"\n\n[pace]\nhours = 10\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	m.update(t, TopicChanges{Goal: ptr("G")})
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("X")}); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	m.crash = nil
	m.update(t, TopicChanges{Goal: ptr("H")})

	settings := readSettings(t, dir)
	pace, _ := settings.extra["pace"].(map[string]any)
	if settings.Title != "X" || settings.Goal != "H" || settings.extra["level"] != "expert" || pace["hours"] != int64(10) {
		t.Errorf("settings = %+v", settings)
	}
}

func TestTopicSettingsNeedAFormat(t *testing.T) {
	if _, err := parseTopicSettings([]byte("title = \"C\"\n"), topicFile); CodeOf(err) != CodeCorrupt {
		t.Errorf("topic.toml without a format: err = %v, want corrupt", err)
	}
}

// TestWritesAreSynced checks that a write flushes the History and its items
// to disk before reporting success.
func TestWritesAreSynced(t *testing.T) {
	m := newTopic(t)
	var synced []string
	syncFile = func(f *os.File) error {
		synced = append(synced, filepath.Base(f.Name()))
		return f.Sync()
	}
	defer func() { syncFile = (*os.File).Sync }()
	m.update(t, TopicChanges{Title: ptr("X")})
	got := strings.Join(synced, " ")
	if !strings.Contains(got, historyFile) || !strings.Contains(got, tempPrefix(topicFile)) {
		t.Errorf("synced %q, want the History and topic.toml", got)
	}
}
