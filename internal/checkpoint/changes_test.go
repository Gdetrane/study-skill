package checkpoint_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

func changesSince(t *testing.T, dir string) checkpoint.Changes {
	t.Helper()
	ch, err := checkpoint.ChangesSince(context.Background(), dir)
	if err != nil {
		t.Fatalf("ChangesSince: %v", err)
	}
	return ch
}

func TestChangesSinceTheLastCheckpoint(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "notes.md", "first")
	if ch := changesSince(t, dir); ch.Since != "" ||
		!slices.Equal(ch.Files, []checkpoint.Change{{Path: "notes.md", Kind: checkpoint.Added}}) {
		t.Errorf("before the first Checkpoint = %+v, want notes.md added", ch)
	}

	write(t, dir, ".gitignore", "*.parquet\n")
	write(t, dir, "old.md", "to delete")
	first := take(t, dir, checkpoint.Learner, "")
	if ch := changesSince(t, dir); ch.Since != first.Commit || len(ch.Files) != 0 {
		t.Errorf("right after a Checkpoint = %+v, want nothing since %s", ch, first.Commit)
	}

	write(t, dir, "notes.md", "second")
	write(t, dir, "practice/answer.txt", "42")
	write(t, dir, "data.parquet", "ignored, so never a change")
	if err := os.Remove(filepath.Join(dir, "old.md")); err != nil {
		t.Fatal(err)
	}
	before := gitFolder(t, dir)
	want := []checkpoint.Change{
		{Path: "notes.md", Kind: checkpoint.Modified},
		{Path: "old.md", Kind: checkpoint.Deleted},
		{Path: "practice/answer.txt", Kind: checkpoint.Added},
	}
	if ch := changesSince(t, dir); ch.Since != first.Commit || !slices.Equal(ch.Files, want) {
		t.Errorf("changes = %+v, want %+v since %s", ch, want, first.Commit)
	}
	if after := gitFolder(t, dir); !maps.Equal(before, after) {
		t.Errorf("ChangesSince wrote to .git:\nbefore %v\nafter  %v", before, after)
	}

	// It agrees with the Checkpoint it describes.
	second := take(t, dir, checkpoint.Learner, "")
	if !second.Committed {
		t.Fatal("the changes were not committed")
	}
	if ch := changesSince(t, dir); ch.Since != second.Commit || len(ch.Files) != 0 {
		t.Errorf("after committing them = %+v", ch)
	}
}

func TestChangesSinceNeedsARepository(t *testing.T) {
	if _, err := checkpoint.ChangesSince(context.Background(), t.TempDir()); err == nil {
		t.Error("a folder without git reported changes")
	}
}
