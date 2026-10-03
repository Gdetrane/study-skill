package checkpoint_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

func TestLogReadsHeadAndSubjectsNewestFirst(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if h, err := checkpoint.Log(ctx, dir, 10); err != nil || h.Head != "" || len(h.Commits) != 0 {
		t.Fatalf("a repository without commits = %+v, %v", h, err)
	}
	write(t, dir, "a.txt", "one")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "[agent] init study workspace")
	write(t, dir, "a.txt", "two")
	git(t, dir, "commit", "-q", "-am", "[agent] complete lesson 01")

	h, err := checkpoint.Log(ctx, dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if h.Head != git(t, dir, "rev-parse", "HEAD") || len(h.Commits) != 2 ||
		h.Commits[0].Subject != "[agent] complete lesson 01" || h.Commits[1].Subject != "[agent] init study workspace" {
		t.Errorf("Log = %+v", h)
	}
	if h, err := checkpoint.Log(ctx, dir, 1); err != nil || len(h.Commits) != 1 {
		t.Errorf("Log with a limit of 1 = %+v, %v", h, err)
	}
}

// Log never runs a program the repository's configuration names, such as
// the signature checker log.showSignature would call.
func TestLogRunsNothingTheConfigurationNames(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, dir, "a.txt", "one")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "first")
	marker := filepath.Join(t.TempDir(), "ran")
	trap := filepath.Join(t.TempDir(), "trap.sh")
	if err := os.WriteFile(trap, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A commit with a signature header, so checking signatures would call
	// gpg.program.
	tree := git(t, dir, "rev-parse", "HEAD^{tree}")
	parent := git(t, dir, "rev-parse", "HEAD")
	signed := "tree " + tree + "\nparent " + parent + "\n" +
		"author Ada Learner <ada@example.com> 1790000000 +0000\n" +
		"committer Ada Learner <ada@example.com> 1790000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\nsigned\n"
	obj := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(obj, []byte(signed), 0o644); err != nil {
		t.Fatal(err)
	}
	commit := git(t, dir, "hash-object", "-t", "commit", "-w", obj)
	git(t, dir, "update-ref", "HEAD", commit)
	git(t, dir, "config", "log.showSignature", "true")
	git(t, dir, "config", "gpg.program", trap)
	git(t, dir, "config", "core.pager", trap)
	git(t, dir, "config", "core.fsmonitor", trap)

	if _, err := checkpoint.Log(ctx, dir, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Log ran a program the repository's configuration names")
	}
	// The trap is real: plain git with this configuration runs it.
	gitMayFail(dir, "log", "-1")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the trap never fired with plain git, so this test proves nothing: %v", err)
	}
}

// Log reads subjects in UTF-8 whatever the repository's i18n settings, and
// cuts long ones.
func TestLogReadsUTF8AndCutsLongSubjects(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	write(t, dir, "a.txt", "one")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "[agent] complete lesson 01: café")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", strings.Repeat("é", 500))
	git(t, dir, "config", "i18n.logOutputEncoding", "ISO-8859-1")
	if out := gitMayFail(dir, "log", "-1", "--skip=1", "--format=%s"); utf8.ValidString(out) {
		t.Fatalf("plain git log printed UTF-8, so this test proves nothing: %q", out)
	}

	h, err := checkpoint.Log(ctx, dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Commits) != 2 || h.Commits[1].Subject != "[agent] complete lesson 01: café" {
		t.Fatalf("Log = %+v", h)
	}
	long := h.Commits[0].Subject
	if !utf8.ValidString(long) || utf8.RuneCountInString(long) > checkpoint.SubjectColumns ||
		!strings.HasPrefix(long, "éé") || !strings.HasSuffix(long, "..") {
		t.Errorf("a long subject = %q", long)
	}
}

// Log's output is bounded: past the bound, only whole records are kept and
// the History says it is truncated.
func TestLogBoundsItsOutput(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	for i := range 20 {
		git(t, dir, "commit", "-q", "--allow-empty", "-m", fmt.Sprintf("commit %02d %s", i, strings.Repeat("x", 100)))
	}
	h, err := checkpoint.LogWithin(ctx, dir, 20, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Truncated || len(h.Commits) == 0 || len(h.Commits) >= 20 {
		t.Fatalf("a bounded Log = %d commits, truncated %v", len(h.Commits), h.Truncated)
	}
	for i, c := range h.Commits {
		if want := fmt.Sprintf("commit %02d ", 19-i); !strings.HasPrefix(c.Subject, want) || len(c.Hash) != 40 {
			t.Errorf("commit %d = %+v", i, c)
		}
	}
	if h, err := checkpoint.Log(ctx, dir, 20); err != nil || h.Truncated || len(h.Commits) != 20 {
		t.Errorf("Log = %d commits, truncated %v, %v", len(h.Commits), h.Truncated, err)
	}
}

func TestTrackedSaysWhichPathsHEADHolds(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if got, err := checkpoint.Tracked(ctx, dir, []string{"node_modules"}); err != nil || got["node_modules"] {
		t.Fatalf("a repository without commits: %v, %v", got, err)
	}
	write(t, dir, "web/node_modules/left-pad/index.js", "x")
	write(t, dir, "notes/a.md", "x")
	write(t, dir, ".venv/pyvenv.cfg", "x")
	git(t, dir, "add", "web", "notes")
	git(t, dir, "commit", "-q", "-m", "first")
	got, err := checkpoint.Tracked(ctx, dir, []string{"web/node_modules", ".venv", "notes", "web/node"})
	if err != nil {
		t.Fatal(err)
	}
	if !got["web/node_modules"] || got[".venv"] || !got["notes"] || got["web/node"] {
		t.Errorf("Tracked = %v", got)
	}
	// Paths inside one another: git lists what is inside the outer one.
	if got, err := checkpoint.Tracked(ctx, dir, []string{"web", "web/node_modules/left-pad"}); err != nil ||
		!got["web"] || !got["web/node_modules/left-pad"] {
		t.Errorf("Tracked of nested paths = %v, %v", got, err)
	}
}
