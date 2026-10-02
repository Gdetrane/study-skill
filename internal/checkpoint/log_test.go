package checkpoint_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
