package checkpoint_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

const practice = "practice/answer"

func work(t *testing.T, dir string) checkpoint.Work {
	t.Helper()
	w, err := checkpoint.SnapshotWork(context.Background(), dir, practice)
	if err != nil {
		t.Fatalf("SnapshotWork: %v", err)
	}
	return w
}

func refused(t *testing.T, dir string, want error) {
	t.Helper()
	if _, err := checkpoint.SnapshotWork(context.Background(), dir, practice); !errors.Is(err, want) {
		t.Errorf("SnapshotWork: err = %v, want %v", err, want)
	}
}

// gitState lists every path under .git with its size and modification
// time, to prove a call wrote nothing there.
func gitState(t *testing.T, dir string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, ".git"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		state[p] = fmt.Sprintf("%v %d", info.ModTime(), info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSnapshotWorkWritesNothingAndIgnoresBuildOutputs(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, practice+"/main.c", "int main(void) { return 0; }\n")
	write(t, dir, practice+"/.gitignore", "a.out\n")
	take(t, dir, checkpoint.Agent, "")
	before := gitState(t, dir)
	first := work(t, dir)
	if got := gitState(t, dir); !maps.Equal(got, before) {
		t.Fatal("SnapshotWork wrote to .git")
	}
	write(t, dir, practice+"/a.out", "a build output")
	if w := work(t, dir); w.Hash != first.Hash {
		t.Errorf("an ignored build output changed the snapshot: %v", w.Changed(first))
	}
	write(t, dir, practice+"/main.c", "int main(void) { return 1; }\n")
	second := work(t, dir)
	if second.Hash == first.Hash || !slices.Equal(second.Changed(first), []string{"main.c"}) {
		t.Errorf("an edit: changed = %v", second.Changed(first))
	}
	if !maps.Equal(gitState(t, dir), before) {
		t.Error("SnapshotWork wrote to .git")
	}
}

// The reviewer's bypasses: each would let a Check pass on work that differs
// from what the snapshot covers.
func TestSnapshotWorkHasNoBlindSpots(t *testing.T) {
	t.Run("ignoring a file after a pass", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, practice+"/main.c", "int main;\n")
		write(t, dir, practice+"/answer.txt", "42\n")
		pass := work(t, dir)
		write(t, dir, ".gitignore", "answer.txt\n")
		if w := work(t, dir); w.Hash == pass.Hash {
			t.Error("ignoring the answer did not change the snapshot")
		}
		write(t, dir, ".gitignore", "")
		write(t, dir, ".git/info/exclude", "answer.txt\n")
		if w := work(t, dir); w.Hash == pass.Hash {
			t.Error("excluding the answer in .git/info/exclude did not change the snapshot")
		}
		write(t, dir, ".git/info/exclude", "")
		global := filepath.Join(t.TempDir(), "ignore")
		write(t, filepath.Dir(global), "ignore", "answer.txt\n")
		git(t, dir, "config", "core.excludesFile", global)
		if w := work(t, dir); w.Hash == pass.Hash {
			t.Error("a global excludes file did not change the snapshot")
		}
	})
	t.Run("a file the index hides", func(t *testing.T) {
		for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
			dir := newRepo(t)
			write(t, dir, practice+"/answer.txt", "41\n")
			take(t, dir, checkpoint.Learner, "")
			git(t, dir, "update-index", flag, practice+"/answer.txt")
			refused(t, dir, checkpoint.ErrHiddenEntries)
		}
	})
	t.Run("a nested repository", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, practice+"/main.c", "int main;\n")
		write(t, dir, practice+"/vendor/lib.c", "int lib;\n")
		git(t, filepath.Join(dir, practice, "vendor"), "init", "-q")
		refused(t, dir, checkpoint.ErrNestedRepository)
	})
	t.Run("a link leading outside the folder", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "notes/answer.txt", "42\n")
		write(t, dir, practice+"/inside.txt", "42\n")
		if err := os.Symlink("inside.txt", filepath.Join(dir, practice, "same.txt")); err != nil {
			t.Fatal(err)
		}
		work(t, dir) // a link inside the folder is fine
		if err := os.Symlink("../../notes/answer.txt", filepath.Join(dir, practice, "answer.txt")); err != nil {
			t.Fatal(err)
		}
		refused(t, dir, checkpoint.ErrLinkOutside)
		if err := os.Remove(filepath.Join(dir, practice, "answer.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/hostname", filepath.Join(dir, practice, "answer.txt")); err != nil {
			t.Fatal(err)
		}
		refused(t, dir, checkpoint.ErrLinkOutside)
	})
	t.Run("every file ignored", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, practice+"/.gitignore", "*\n")
		write(t, dir, practice+"/answer.txt", "42\n")
		refused(t, dir, checkpoint.ErrAllIgnored)
		if err := os.RemoveAll(filepath.Join(dir, practice)); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, practice), 0o755); err != nil {
			t.Fatal(err)
		}
		work(t, dir) // an empty folder is fine
	})
	t.Run("a self-ignoring .gitignore inside the folder", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, practice+"/main.c", "int main;\n")
		write(t, dir, practice+"/src/answer.txt", "42\n")
		pass := work(t, dir)
		write(t, dir, practice+"/src/.gitignore", ".gitignore\nanswer.txt\n")
		if w := work(t, dir); w.Hash == pass.Hash {
			t.Error("a .gitignore that ignores itself did not change the snapshot")
		}
	})
}
