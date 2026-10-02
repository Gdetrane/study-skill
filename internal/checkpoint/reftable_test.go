package checkpoint_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// newReftableRepo creates a repository that stores its refs in the reftable
// format, where .git/refs/heads is a stub file rather than a folder.
func newReftableRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := gitCmd(dir, "init", "-q", "-b", "main", "--ref-format=reftable").CombinedOutput(); err != nil {
		t.Skipf("this git cannot create reftable repositories: %v\n%s", err, out)
	}
	git(t, dir, "config", "user.name", "Ada Learner")
	git(t, dir, "config", "user.email", "ada@example.com")
	return dir
}

func TestTakeWorksWithReftable(t *testing.T) {
	dir := newReftableRepo(t)
	write(t, dir, "notes.md", "first")
	first := take(t, dir, checkpoint.Learner, "first")
	if !first.Committed || git(t, dir, "rev-parse", "HEAD") != first.Commit {
		t.Fatalf("first Checkpoint = %+v, HEAD %s", first, git(t, dir, "rev-parse", "HEAD"))
	}
	write(t, dir, "notes.md", "second")
	second := take(t, dir, checkpoint.Agent, "second")
	if !second.Committed || git(t, dir, "rev-parse", "HEAD~1") != first.Commit {
		t.Fatalf("second Checkpoint = %+v", second)
	}
	if got := snapshot(t, dir, ""); got != second.Tree {
		t.Errorf("Snapshot = %s, want the Checkpoint's tree %s", got, second.Tree)
	}
}

func TestTakeRefusesALinkInTheReftableFolder(t *testing.T) {
	dir := newReftableRepo(t)
	write(t, dir, "notes.md", "first")
	take(t, dir, checkpoint.Learner, "first")
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(dir, ".git", "reftable", "tables.list.link")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "notes.md", "second")
	_, err := checkpoint.Take(context.Background(), dir, checkpoint.Options{Role: checkpoint.Agent, Time: when})
	if !errors.Is(err, checkpoint.ErrRepositoryChanged) {
		t.Errorf("Take with a link in .git/reftable: %v, want ErrRepositoryChanged", err)
	}
}
