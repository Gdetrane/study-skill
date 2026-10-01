package checkpoint_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// These tests play a sandboxed agent that can write inside its Topic and
// tries to make the core, which runs outside the sandbox, commit into another
// of the learner's repositories by swapping folders while a Checkpoint runs.

// victim is another repository of the learner's, outside the Topic.
type victim struct {
	dir, head, index, status string
}

func newVictim(t *testing.T) victim {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "main.c", "int main(void) { return 0; }\n")
	git(t, dir, "add", "main.c")
	git(t, dir, "commit", "-qm", "private work")
	return snapshotVictim(t, dir)
}

func snapshotVictim(t *testing.T, dir string) victim {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return victim{
		dir:    dir,
		head:   git(t, dir, "rev-parse", "HEAD"),
		index:  string(index),
		status: git(t, dir, "status", "--porcelain"),
	}
}

func (v victim) assertUntouched(t *testing.T) {
	t.Helper()
	if now := snapshotVictim(t, v.dir); now != v {
		t.Fatalf("the other repository changed:\nbefore %+v\nafter  %+v", v, now)
	}
}

// topicWithWork returns a Topic with one Checkpoint and an unsaved change.
func topicWithWork(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "notes.md", "first\n")
	take(t, dir, checkpoint.Learner, "")
	write(t, dir, "notes.md", "second\n")
	return dir
}

// swapForLink renames path aside and puts a symbolic link to target in its
// place.
func swapForLink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Rename(path, path+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestTakeRefusesGitSwappedWhileWaitingForTheLock(t *testing.T) {
	other := newVictim(t)
	topic := topicWithWork(t)
	write(t, topic, ".git/index.lock", "")

	done := make(chan error, 1)
	go func() {
		_, err := checkpoint.Take(context.Background(), topic, checkpoint.Options{
			Role: checkpoint.Agent, Time: when, LockTimeout: 10 * time.Second,
		})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // Take is now waiting for the lock
	swapForLink(t, filepath.Join(topic, ".git"), filepath.Join(other.dir, ".git"))
	if err := os.Remove(filepath.Join(topic, ".git-real", "index.lock")); err != nil {
		t.Fatal(err)
	}

	err := <-done
	if !errors.Is(err, checkpoint.ErrRepositoryChanged) && !errors.Is(err, checkpoint.ErrNotRepository) {
		t.Errorf("Take = %v, want ErrRepositoryChanged", err)
	}
	other.assertUntouched(t)
}

func TestTakeRefusesSwapsAfterTheLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		swap func(t *testing.T, topic, other string)
	}{
		{".git for a link to another repository", func(t *testing.T, topic, other string) {
			swapForLink(t, filepath.Join(topic, ".git"), filepath.Join(other, ".git"))
		}},
		{"the Topic folder for a link to another repository", func(t *testing.T, topic, other string) {
			swapForLink(t, topic, other)
		}},
		{"refs/heads for a link into another repository", func(t *testing.T, topic, other string) {
			swapForLink(t, filepath.Join(topic, ".git", "refs", "heads"), filepath.Join(other, ".git", "refs", "heads"))
		}},
		{"a commondir naming another repository", func(t *testing.T, topic, other string) {
			write(t, topic, ".git/commondir", filepath.Join(other, ".git")+"\n")
		}},
		{"objects for a link into another repository", func(t *testing.T, topic, other string) {
			swapForLink(t, filepath.Join(topic, ".git", "objects"), filepath.Join(other, ".git", "objects"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := newVictim(t)
			topic := topicWithWork(t)
			index, err := os.ReadFile(filepath.Join(topic, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			checkpoint.SetTestHook(t, func(point string) {
				if point == "locked" {
					tc.swap(t, topic, other.dir)
				}
			})
			_, err = checkpoint.Take(context.Background(), topic, checkpoint.Options{Role: checkpoint.Agent, Time: when})
			if !errors.Is(err, checkpoint.ErrRepositoryChanged) {
				t.Errorf("Take = %v, want ErrRepositoryChanged", err)
			}
			other.assertUntouched(t)
			// Wherever the real .git now is, the learner's index is intact.
			real := filepath.Join(topic, ".git", "index")
			for _, moved := range []string{filepath.Join(topic+"-real", ".git"), filepath.Join(topic, ".git-real")} {
				if _, err := os.Lstat(moved); err == nil {
					real = filepath.Join(moved, "index")
				}
			}
			if now, err := os.ReadFile(real); err != nil || string(now) != string(index) {
				t.Errorf("the learner's index at %s changed (%v)", real, err)
			}
		})
	}
}

// TestGitStaysPinnedToTheOpenedRepository swaps .git after the last check,
// just before git moves the branch. On Linux git works on the folder that
// was opened, not on the name, so the swap cannot redirect the commit.
func TestGitStaysPinnedToTheOpenedRepository(t *testing.T) {
	if !checkpoint.CanPin() {
		t.Skip("git can only be pinned through /proc/self/fd on Linux")
	}
	other := newVictim(t)
	topic := topicWithWork(t)
	checkpoint.SetTestHook(t, func(point string) {
		if point == "update-ref" {
			swapForLink(t, filepath.Join(topic, ".git"), filepath.Join(other.dir, ".git"))
		}
	})
	res, err := checkpoint.Take(context.Background(), topic, checkpoint.Options{Role: checkpoint.Agent, Time: when})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	other.assertUntouched(t)
	real := filepath.Join(topic, ".git-real")
	if head := git(t, topic, "--git-dir="+real, "rev-parse", "HEAD"); head != res.Commit {
		t.Errorf("the Topic's branch is at %s, want the Checkpoint %s", head, res.Commit)
	}
}

func TestTakePutsTheLearnersIndexBackWhenTheBranchCannotMove(t *testing.T) {
	for _, tc := range []struct {
		name string
		// interfere runs just before Take moves the branch.
		interfere func(t *testing.T, dir string, cancel func())
		want      error
	}{
		{"another process is updating the branch", func(t *testing.T, dir string, _ func()) {
			write(t, dir, ".git/refs/heads/main.lock", "")
		}, checkpoint.ErrRefLocked},
		{"the branch moved", func(t *testing.T, dir string, _ func()) {
			c := git(t, dir, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "elsewhere")
			git(t, dir, "update-ref", "refs/heads/main", c)
		}, checkpoint.ErrHeadMoved},
		{"the Checkpoint is cancelled", func(t *testing.T, dir string, cancel func()) {
			cancel()
		}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			write(t, dir, "notes.md", "v1\n")
			take(t, dir, checkpoint.Learner, "")
			// The learner staged v2 by hand, then kept editing.
			write(t, dir, "notes.md", "v2\n")
			git(t, dir, "add", "notes.md")
			staged := git(t, dir, "ls-files", "-s", "notes.md")
			index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			write(t, dir, "notes.md", "v3\n")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checkpoint.SetTestHook(t, func(point string) {
				if point == "update-ref" {
					tc.interfere(t, dir, cancel)
				}
			})
			_, err = checkpoint.Take(ctx, dir, checkpoint.Options{Role: checkpoint.Agent, Time: when})
			if !errors.Is(err, tc.want) {
				t.Errorf("Take = %v, want %v", err, tc.want)
			}
			now, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
			if err != nil || string(now) != string(index) {
				t.Errorf("the learner's index changed (%v); notes.md is now %s, was %s",
					err, git(t, dir, "ls-files", "-s", "notes.md"), staged)
			}
		})
	}
}

func TestTakeCopesWithFilesChangingWhileItRuns(t *testing.T) {
	// replace saves path the way editors do: a new file renamed over it.
	replace := func(t *testing.T, dir, path, content string) {
		t.Helper()
		write(t, dir, path+".tmp", content)
		if err := os.Rename(filepath.Join(dir, path+".tmp"), filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a file vanishes before it is hashed", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "keep.md", "keep\n")
		write(t, dir, ".swap.tmp", "an editor's temporary file\n")
		checkpoint.SetTestHook(t, func(point string) {
			if point == "hash" {
				os.Remove(filepath.Join(dir, ".swap.tmp"))
			}
		})
		res := take(t, dir, checkpoint.Learner, "")
		if got := paths(files(t, dir, res.Commit)); !slices.Equal(got, []string{"keep.md"}) {
			t.Errorf("committed %v, want only keep.md", got)
		}
	})

	t.Run("a file is replaced once", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "notes.md", "old\n")
		replaced := false
		checkpoint.SetTestHook(t, func(point string) {
			if point == "hash" && !replaced {
				replaced = true
				replace(t, dir, "notes.md", "new\n")
			}
		})
		res := take(t, dir, checkpoint.Learner, "")
		if got := git(t, dir, "show", res.Commit+":notes.md"); got != "new" {
			t.Errorf("committed notes.md = %q, want the saved version", got)
		}
	})

	t.Run("a file keeps changing", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "notes.md", "0\n")
		n := 0
		checkpoint.SetTestHook(t, func(point string) {
			if point == "hash" {
				n++
				replace(t, dir, "notes.md", strconv.Itoa(n)+"\n")
			}
		})
		_, err := checkpoint.Take(context.Background(), dir, checkpoint.Options{Role: checkpoint.Learner, Time: when})
		if !errors.Is(err, checkpoint.ErrWorktreeChanged) {
			t.Errorf("Take = %v, want ErrWorktreeChanged", err)
		}
		if out := gitMayFail(dir, "rev-parse", "-q", "--verify", "HEAD"); out != "" {
			t.Errorf("a commit was made: %s", out)
		}
	})
}
