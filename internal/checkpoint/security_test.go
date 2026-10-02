package checkpoint_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// hookNames are the hook events git's own plumbing or a commit could fire.
var hookNames = []string{
	"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit",
	"reference-transaction", "post-index-change", "post-checkout",
	"post-merge", "post-rewrite", "pre-auto-gc",
}

// traps creates executable scripts that append "<name> <args>" to a marker
// file. Filters also copy their input through.
type traps struct {
	t      *testing.T
	dir    string
	marker string
}

func newTraps(t *testing.T) *traps {
	dir := t.TempDir()
	return &traps{t: t, dir: dir, marker: filepath.Join(dir, "marker")}
}

func (tr *traps) script(name, body string) string {
	tr.t.Helper()
	return tr.scriptAt(filepath.Join(tr.dir, name), body)
}

func (tr *traps) scriptAt(p, body string) string {
	tr.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	src := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> '" + tr.marker + "'\n" + body
	if err := os.WriteFile(p, []byte(src), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	return p
}

// fired returns what the traps recorded.
func (tr *traps) fired() string {
	b, _ := os.ReadFile(tr.marker)
	return string(b)
}

// ran reports whether the trap recorded a line starting with name.
func (tr *traps) ran(name string) bool {
	for _, line := range strings.Split(tr.fired(), "\n") {
		if line == name || strings.HasPrefix(line, name+" ") {
			return true
		}
	}
	return false
}

// assertQuiet fails if any trap has run.
func (tr *traps) assertQuiet(step string) {
	tr.t.Helper()
	if got := tr.fired(); got != "" {
		tr.t.Fatalf("%s ran a program named by the repository:\n%s", step, got)
	}
}

func TestCheckpointsNeverRunProgramsNamedByTheRepository(t *testing.T) {
	tr := newTraps(t)
	dir := newRepo(t)
	outside := t.TempDir()
	write(t, outside, "outside.txt", "not part of the Topic\n")

	filter := tr.script("filter", "cat\n")
	hooks := filepath.Join(tr.dir, "hooks")
	for _, name := range hookNames {
		tr.scriptAt(filepath.Join(hooks, name), "exit 0\n")
		tr.scriptAt(filepath.Join(dir, ".git", "hooks", name), "exit 0\n")
	}
	config := [][2]string{
		{"filter.evil.clean", filter + " clean %f"},
		{"filter.evil.smudge", filter + " smudge %f"},
		{"filter.evil.process", filter + " process"},
		{"filter.evil.required", "true"},
		{"diff.evil.textconv", tr.script("textconv", "cat \"$1\"\n")},
		{"diff.evil.command", tr.script("diff-driver", "exit 0\n")},
		{"diff.external", tr.script("external-diff", "exit 0\n")},
		{"commit.gpgSign", "true"},
		{"user.signingKey", "0xDEADBEEF"},
		{"gpg.program", tr.script("gpg", "exit 1\n")},
		{"core.fsmonitor", tr.script("fsmonitor", "exit 1\n")},
		{"core.hooksPath", hooks},
		{"hook.trap.command", tr.script("config-hook", "exit 0\n")},
		{"core.pager", tr.script("pager", "cat\n")},
		{"core.editor", tr.script("editor", "exit 0\n")},
		{"sequence.editor", tr.script("editor", "exit 0\n")},
		{"core.sshCommand", tr.script("ssh", "exit 1\n")},
		{"core.alternateRefsCommand", tr.script("alternate-refs", "exit 1\n")},
		{"core.worktree", outside},
	}
	for _, kv := range config {
		git(t, dir, "config", kv[0], kv[1])
	}
	for _, name := range hookNames {
		git(t, dir, "config", "--add", "hook.trap.event", name)
	}

	// CRLF line endings and an ident keyword: git add would normalise
	// both, a Checkpoint stores them as they are.
	raw := "line one\r\nline two\r\n$Id: left alone $\r\n"
	write(t, dir, ".gitattributes", "* filter=evil text eol=lf ident\n*.txt diff=evil\n")
	write(t, dir, "practice/l1/data.txt", raw)
	write(t, dir, "notes.md", "plain\n")

	ctx := context.Background()
	opts := checkpoint.Options{Role: checkpoint.Learner, Time: when}
	first, err := checkpoint.Take(ctx, dir, opts)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	tr.assertQuiet("the first Checkpoint")
	if !first.Committed {
		t.Fatal("no commit")
	}
	got := files(t, dir, "HEAD")
	if want := []string{".gitattributes", "notes.md", "practice/l1/data.txt"}; !slices.Equal(paths(got), want) {
		t.Fatalf("files = %v, want %v (core.worktree must not widen the Topic)", paths(got), want)
	}
	if b := blob(t, dir, got["practice/l1/data.txt"].oid); b != raw {
		t.Fatalf("stored %q, want the raw bytes %q", b, raw)
	}
	if strings.Contains(git(t, dir, "cat-file", "commit", "HEAD"), "gpgsig") {
		t.Fatal("the Checkpoint was signed")
	}

	if res, err := checkpoint.Take(ctx, dir, opts); err != nil || res.Committed {
		t.Fatalf("unchanged Take = %+v, %v", res, err)
	}
	tr.assertQuiet("a skipped Checkpoint")

	write(t, dir, "practice/l1/data.txt", raw+"line three\r\n")
	if err := os.Remove(filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpoint.Take(ctx, dir, checkpoint.Options{Role: checkpoint.Agent, Time: when, LargeFileThreshold: 1}); err != nil {
		t.Fatalf("Take: %v", err)
	}
	tr.assertQuiet("a second Checkpoint")

	sub, err := checkpoint.Snapshot(ctx, dir, "practice/l1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := checkpoint.Snapshot(ctx, dir, ""); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	tr.assertQuiet("Snapshot")
	if want := git(t, dir, "rev-parse", "HEAD:practice/l1"); sub != want {
		t.Fatalf("Snapshot = %s, want %s", sub, want)
	}

	// The traps are live: a learner's ordinary git runs every one of them.
	// The process filter fails its handshake and is required, which stops
	// most commands early, so it is disarmed after its own check.
	git(t, dir, "config", "--unset", "core.worktree")
	gitMayFail(dir, "status")
	git(t, dir, "config", "--unset", "filter.evil.process")
	git(t, dir, "config", "--unset", "filter.evil.required")
	gitMayFail(dir, "diff", "HEAD~1", "HEAD")
	gitMayFail(dir, "log", "-p", "-1", "--textconv", "--no-ext-diff")
	write(t, dir, "notes.md", "again\n")
	gitMayFail(dir, "add", "-A")
	gitMayFail(dir, "commit", "--allow-empty", "-m", "armed")
	for _, trap := range []string{
		"filter process", "filter clean", "filter smudge", "textconv", "diff-driver",
		"external-diff", "gpg", "fsmonitor", "pre-commit", "config-hook",
	} {
		if !tr.ran(trap) {
			t.Errorf("plain git never ran the %s trap, so the test proves nothing about it; fired:\n%s", trap, tr.fired())
		}
	}
}

func TestCheckpointsNeverFetchMissingObjects(t *testing.T) {
	tr := newTraps(t)
	dir := newRepo(t)
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")

	// HEAD names a tree that is not in the repository, and a promisor
	// remote would fetch it by running a local upload-pack program.
	missing := strings.Repeat("1234567890", 4)
	commit := strings.TrimSpace(gitStdin(t, dir,
		"tree "+missing+"\nauthor A <a@b> 0 +0000\ncommitter A <a@b> 0 +0000\n\nforged\n",
		"hash-object", "-t", "commit", "-w", "--literally", "--stdin"))
	git(t, dir, "update-ref", "refs/heads/main", commit)
	for _, kv := range [][2]string{
		{"core.repositoryFormatVersion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", remote},
		{"remote.origin.promisor", "true"},
		{"remote.origin.uploadpack", tr.script("upload-pack", "exit 1\n")},
	} {
		git(t, dir, "config", kv[0], kv[1])
	}
	write(t, dir, "a.md", "a\n")

	_, err := checkpoint.Take(context.Background(), dir, checkpoint.Options{Role: checkpoint.Learner, Time: when})
	if err == nil {
		t.Fatal("Take succeeded although HEAD's tree is missing")
	}
	tr.assertQuiet("a Checkpoint over a missing tree")

	gitMayFail(dir, "cat-file", "-p", missing)
	if !tr.ran("upload-pack") {
		t.Fatalf("plain git never ran the upload-pack trap, so the test proves nothing; fired: %q", tr.fired())
	}
}

// gitStdin runs plain git with input and fails the test if it fails.
func gitStdin(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
