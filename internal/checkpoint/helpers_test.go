package checkpoint_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// when is the fixed clock for every Checkpoint in these tests.
var when = time.Date(2026, 10, 1, 9, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

// TestMain isolates the tests from the developer's git configuration: HOME
// and XDG_CONFIG_HOME point at an empty folder and GIT_* variables are
// cleared.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "checkpoint-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// newRepo creates an empty repository on branch main with a local identity.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", "Ada Learner")
	git(t, dir, "config", "user.email", "ada@example.com")
	return dir
}

// gitCmd builds a plain, unhardened git command, as a learner would run it.
func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Ada Learner", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Learner", "GIT_COMMITTER_EMAIL=ada@example.com",
		"LC_ALL=C",
		// No background maintenance: it would still be writing to .git
		// when the test removes its folder.
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
	)
	return cmd
}

// git runs plain git and fails the test if it fails.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitMayFail runs plain git and returns its output whatever the outcome.
func gitMayFail(dir string, args ...string) string {
	out, _ := gitCmd(dir, args...).CombinedOutput()
	return string(out)
}

// write creates or replaces a file, creating its folders.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// take takes a Checkpoint at the fixed time and fails the test on error.
func take(t *testing.T, dir string, role checkpoint.Role, msg string) checkpoint.Result {
	t.Helper()
	res, err := checkpoint.Take(context.Background(), dir, checkpoint.Options{Role: role, Message: msg, Time: when})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return res
}

// snapshot computes a Snapshot and fails the test on error.
func snapshot(t *testing.T, dir, sub string) string {
	t.Helper()
	tree, err := checkpoint.Snapshot(context.Background(), dir, sub)
	if err != nil {
		t.Fatalf("Snapshot(%q): %v", sub, err)
	}
	return tree
}

// entry is one file in a tree.
type entry struct{ mode, oid string }

// files lists every file in a commit or tree, by path.
func files(t *testing.T, dir, rev string) map[string]entry {
	t.Helper()
	out := git(t, dir, "ls-tree", "-r", "-z", "--full-tree", rev)
	got := map[string]entry{}
	for _, record := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		meta, path, _ := strings.Cut(record, "\t")
		f := strings.Fields(meta)
		got[path] = entry{mode: f[0], oid: f[2]}
	}
	return got
}

// blob returns the raw contents of a blob.
func blob(t *testing.T, dir, oid string) string {
	t.Helper()
	out, err := gitCmd(dir, "cat-file", "blob", oid).Output()
	if err != nil {
		t.Fatalf("git cat-file blob %s: %v", oid, err)
	}
	return string(out)
}

// paths returns the sorted paths of a file listing.
func paths(m map[string]entry) []string {
	return slices.Sorted(maps.Keys(m))
}

// readIndex returns the bytes of the learner's index ("" when missing).
func readIndex(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}
