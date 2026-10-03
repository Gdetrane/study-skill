package checkpoint_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// gitSnapshot records every file and folder under .git, with its mode, size,
// modification time and, for files, a hash of its content.
func gitSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(dir, ".git")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out[rel] = fmt.Sprintf("dir %s %s", info.Mode(), info.ModTime())
			return nil
		}
		sum := ""
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h := sha256.Sum256(b)
			sum = hex.EncodeToString(h[:8])
		}
		out[rel] = fmt.Sprintf("%s %d %s %s", info.Mode(), info.Size(), info.ModTime(), sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ChangesSince runs none of the programs a repository's configuration can
// name (ADR-0009), writes nothing to .git, and never lists files outside the
// Topic, whatever the configuration says.
func TestChangesSinceRunsNothingAndWritesNothing(t *testing.T) {
	tr := newTraps(t)
	dir := newRepo(t)
	outside := t.TempDir()
	write(t, outside, "outside.txt", "not part of the Topic\n")
	write(t, dir, "notes.md", "plain\n")
	write(t, dir, "practice/l1/data.txt", "line one\r\n")
	take(t, dir, checkpoint.Learner, "")

	filter := tr.script("filter", "cat\n")
	hooks := filepath.Join(tr.dir, "hooks")
	for _, name := range hookNames {
		tr.scriptAt(filepath.Join(hooks, name), "exit 0\n")
		tr.scriptAt(filepath.Join(dir, ".git", "hooks", name), "exit 0\n")
	}
	for _, kv := range [][2]string{
		{"filter.evil.clean", filter + " clean %f"},
		{"filter.evil.smudge", filter + " smudge %f"},
		{"filter.evil.process", filter + " process"},
		{"filter.evil.required", "true"},
		{"diff.evil.textconv", tr.script("textconv", "cat \"$1\"\n")},
		{"diff.evil.command", tr.script("diff-driver", "exit 0\n")},
		{"diff.external", tr.script("external-diff", "exit 0\n")},
		{"core.fsmonitor", tr.script("fsmonitor", "exit 1\n")},
		{"core.hooksPath", hooks},
		{"hook.trap.command", tr.script("config-hook", "exit 0\n")},
		{"core.pager", tr.script("pager", "cat\n")},
		{"core.worktree", outside},
		{"core.untrackedCache", "true"},
		{"core.splitIndex", "true"},
		{"index.version", "4"},
	} {
		git(t, dir, "config", kv[0], kv[1])
	}
	for _, name := range hookNames {
		git(t, dir, "config", "--add", "hook.trap.event", name)
	}
	write(t, dir, ".gitattributes", "* filter=evil text eol=lf ident\n*.txt diff=evil\n")
	write(t, dir, "practice/l1/data.txt", "line one\r\nline two\r\n")
	write(t, dir, "new.md", "new\n")
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "big.bin", strings.Repeat("x", 1<<20))

	before := gitSnapshot(t, dir)
	ch, err := checkpoint.ChangesSince(context.Background(), dir)
	if err != nil {
		t.Fatalf("ChangesSince: %v", err)
	}
	tr.assertQuiet("ChangesSince")
	if after := gitSnapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("ChangesSince touched .git:\nbefore %v\nafter  %v", before, after)
	}
	for _, f := range ch.Files {
		if strings.Contains(f.Path, "outside") {
			t.Errorf("listed a file outside the Topic: %v", f)
		}
	}
	if !slices.Contains(ch.Files, checkpoint.Change{Path: "new.md", Kind: checkpoint.Added}) {
		t.Errorf("changes = %+v, want new.md added", ch.Files)
	}
}

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
