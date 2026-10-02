package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Removing a Topic moves its folder, whole, into .lamplight/removed, where
// moving it back restores it; the dry run reports the same place and moves
// nothing.
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

	// Moving the folder back restores the Topic.
	if err := os.Rename(got.MovedTo, filepath.Join(home, "rust")); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 1 || status.Topics[0].Goal != "Write a CLI" || len(status.Topics[0].Flags) != 0 {
		t.Fatalf("status after moving it back = %+v", status.Topics)
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

	// An interrupted write is finished first, by the next write, never
	// carried away half done.
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeIntent(root, intent{Format: FormatVersion, Topic: "rust", Event: "id999", Type: "topic.updated"}); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		if _, err := m.RemoveTopic(ctx, "rust", dryRun); CodeOf(err) != CodeFailedPrecondition {
			t.Errorf("an interrupted write (dry run %v): %v", dryRun, err)
		}
	}
	if !exists(filepath.Join(home, "rust", "topic.toml")) {
		t.Fatal("a refused removal moved the Topic")
	}
}
