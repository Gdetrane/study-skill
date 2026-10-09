package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Removing a Topic moves its folder, whole, into .lamplight/removed, from
// where study topic restore brings it back (topic_restore_test.go); the dry
// run reports the same place and moves nothing.
func TestRemoveTopicMovesItOutOfTheStudyHome(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	home := t.TempDir()
	m := newMachine(t, home, "id", t0)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}

	dry, err := m.RemoveTopic(ctx, "rust", true)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".lamplight", "removed", "20261001-093000-rust")
	if !dry.DryRun || dry.MovedTo != want {
		t.Fatalf("dry run = %+v, want moved_to %s", dry, want)
	}
	if !exists(filepath.Join(home, "rust", "topic.toml")) || exists(want) {
		t.Fatal("the dry run moved the Topic")
	}

	got, err := m.RemoveTopic(ctx, "rust", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.DryRun || got.MovedTo != dry.MovedTo {
		t.Fatalf("removal = %+v, the dry run said %+v", got, dry)
	}
	for _, rel := range []string{"topic.toml", "history.jsonl", ".git"} {
		if !exists(filepath.Join(want, rel)) {
			t.Errorf("the removed Topic lacks %s", rel)
		}
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 0 || len(status.Problems) != 0 || status.ActiveTopic != nil {
		t.Fatalf("status after the removal = %+v", status)
	}

	// The id is free again, and a second removal in the same second does
	// not overwrite the first.
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a web server"}); err != nil {
		t.Fatal(err)
	}
	again, err := m.RemoveTopic(ctx, "rust", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.MovedTo == got.MovedTo || !exists(filepath.Join(got.MovedTo, "topic.toml")) ||
		!exists(filepath.Join(again.MovedTo, "topic.toml")) {
		t.Fatalf("two removals of one id: %s and %s", got.MovedTo, again.MovedTo)
	}

	// Each removal names the command that restores it, by its own folder,
	// and has a record beside that folder saying which Topic it was.
	if want := "study topic restore rust --from 20261001-093000-rust"; got.Restore != want || dry.Restore != want {
		t.Errorf("restore = %q, in the dry run %q, want %q", got.Restore, dry.Restore, want)
	}
	if want := "study topic restore rust --from " + filepath.Base(again.MovedTo); again.Restore != want {
		t.Errorf("restore of the second removal = %q, want %q", again.Restore, want)
	}
	for _, removal := range []TopicRemoval{got, again} {
		data, err := os.ReadFile(removal.MovedTo + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"format":1,"topic":"rust","removed":"2026-10-01T09:30:00Z"}` + "\n"; string(data) != want {
			t.Errorf("the record of %s = %s, want %s", removal.MovedTo, data, want)
		}
	}
}

// A removal interrupted between its two writes moved nothing: the record it
// left names no removed Topic, and the Topic is removed the next time.
func TestARemovalInterruptedAfterItsRecordRemovedNothing(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	home := t.TempDir()
	m := newMachine(t, home, "id", t0)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	m.crash = crashOnce(crashRemoveRecorded)
	if _, err := m.RemoveTopic(ctx, "rust", false); !errors.Is(err, errCrash) {
		t.Fatalf("RemoveTopic = %v, want the crash", err)
	}
	orphan := filepath.Join(home, ".lamplight", removedDir, "20261001-093000-rust.json")
	if !exists(orphan) || !exists(filepath.Join(home, "rust", "topic.toml")) {
		t.Fatal("the crash should leave the record and the Topic where they were")
	}

	if list, err := m.ListRemovedTopics(ctx); err != nil || len(list.Removed) != 0 {
		t.Errorf("removed Topics after the crash = %+v, %v; want none", list, err)
	}
	for _, spec := range []TopicRestoreSpec{{Topic: "rust"}, {Topic: "rust", From: "20261001-093000-rust"}} {
		if _, err := m.RestoreTopic(ctx, spec); CodeOf(err) != CodeNotFound {
			t.Errorf("RestoreTopic(%+v) = %v, want not_found", spec, err)
		}
	}
	if status, err := m.Status(ctx); err != nil || len(status.Topics) != 1 || len(status.Topics[0].Flags) != 0 {
		t.Fatalf("status after the crash = %+v, %v", status, err)
	}

	// The next removal does not take the name the record holds.
	removed, err := m.RemoveTopic(ctx, "rust", false)
	if err != nil {
		t.Fatal(err)
	}
	if removed.MovedTo+".json" == orphan || !exists(removed.MovedTo+".json") || !exists(filepath.Join(removed.MovedTo, "topic.toml")) {
		t.Errorf("the removal after the crash went to %s", removed.MovedTo)
	}
	if list, err := m.ListRemovedTopics(ctx); err != nil || len(list.Removed) != 1 || list.Removed[0].Path != removed.MovedTo {
		t.Errorf("removed Topics = %+v, %v; want the one removal", list, err)
	}
}

// The reason removal exists: an import whose proof marked a Lesson done
// that the learner did not finish is removed, then imported again with
// --not-done.
func TestRemoveTopicLetsAnImportBeRedone(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{commits: []string{"[agent] complete lesson 01"}})
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src, NotDone: []string{"lesson-01"}}); CodeOf(err) != CodeAlreadyExists {
		t.Fatalf("importing over the Topic: %v", err)
	}
	if _, err := m.RemoveTopic(ctx, "go-concurrency", false); err != nil {
		t.Fatal(err)
	}
	again, err := m.ImportV1(ctx, ImportSpec{Dir: src, NotDone: []string{"lesson-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if ids := lessonIDs(again.Completed); len(ids) != 1 || ids[0] != "lesson-02" {
		t.Errorf("completed after the second import = %v, want lesson-02 only", ids)
	}
}

func TestRemoveTopicRefusals(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	home := t.TempDir()
	m := newMachine(t, home, "id", t0)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.RemoveTopic(ctx, "go", false); CodeOf(err) != CodeNotFound {
		t.Errorf("an unknown Topic: %v", err)
	}
	if _, err := m.RemoveTopic(ctx, "../rust", false); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a path for an id: %v", err)
	}
	if err := os.Symlink(filepath.Join(home, "rust"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RemoveTopic(ctx, "alias", false); CodeOf(err) != CodeCorrupt {
		t.Errorf("a link to a Topic: %v", err)
	}

	// An interrupted write (a marker and no writer holding the lock) is
	// finished first, by the next write, never carried away half done.
	leaveIntent(t, home, "rust")
	for _, dryRun := range []bool{true, false} {
		if _, err := m.RemoveTopic(ctx, "rust", dryRun); CodeOf(err) != CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "interrupted") {
			t.Errorf("an interrupted write (dry run %v): %v", dryRun, err)
		}
	}
	if !exists(filepath.Join(home, "rust", "topic.toml")) {
		t.Fatal("a refused removal moved the Topic")
	}
}

// Moves the operating system refuses get advice, not an internal error.
func TestRemoveTopicExplainsARefusedMove(t *testing.T) {
	for _, tc := range []struct {
		errno syscall.Errno
		code  ErrorCode
		says  string
	}{
		{syscall.EXDEV, CodeFailedPrecondition, "different file systems"},
		{syscall.EBUSY, CodeBusy, "in use or is a mount point"},
	} {
		err := moveError("rust", "/study/.lamplight/removed/x", &os.LinkError{Op: "rename", Old: "rust", New: "x", Err: tc.errno})
		if CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.says) || !errors.Is(err, tc.errno) {
			t.Errorf("%v: %v (%s)", tc.errno, err, CodeOf(err))
		}
	}
}

// leaveIntent leaves the marker a write to the Topic leaves while it runs.
func leaveIntent(t *testing.T, home, topicID string) {
	t.Helper()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeIntent(root, intent{Format: FormatVersion, Topic: topicID, Event: "id999", Type: "topic.updated"}); err != nil {
		t.Fatal(err)
	}
}
