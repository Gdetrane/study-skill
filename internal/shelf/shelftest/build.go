package shelftest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// built is the study-shelf built once for a test binary, and gives its path to
// every test that asks. A package whose tests ask calls Main from TestMain.
var built struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// Main runs a package's tests for its TestMain, and removes the study-shelf
// that Shelf built for them afterwards.
//
// It clears the caller's GIT_* variables first: a commit hook that runs the
// tests sets GIT_INDEX_FILE, and GIT_DIR from a linked worktree, and the go
// command these tests run would then work on the repository being committed.
func Main(m *testing.M) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	code := m.Run()
	Remove()
	os.Exit(code)
}

// Remove removes the study-shelf that Shelf built, if it built one. A
// package with a TestMain of its own calls it once its tests have run.
func Remove() {
	if built.dir != "" {
		_ = os.RemoveAll(built.dir)
	}
}

// Shelf returns the path of a study-shelf built from this checkout. It is
// built without the race detector, whatever the tests run with: every start
// of it compiles the PDF reader, which the detector makes ten times slower.
// Shelf's own code runs under the detector in the tests that call it in
// their own process.
func Shelf(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the study-shelf binary")
	}
	built.once.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			built.err = fmt.Errorf("the test builds study-shelf with the go command: %w", err)
			return
		}
		if built.dir, built.err = os.MkdirTemp("", "study-shelf-test-"); built.err != nil {
			return
		}
		built.path = filepath.Join(built.dir, "study-shelf")
		// -buildvcs=false: the build never needs git to report on the
		// checkout.
		cmd := exec.Command(goTool, "build", "-buildvcs=false", "-o", built.path, "./cmd/study-shelf")
		cmd.Dir = moduleRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			built.err = fmt.Errorf("building study-shelf: %w\n%s", err, out)
		}
	})
	if built.err != nil {
		t.Fatal(built.err)
	}
	return built.path
}

// moduleRoot is the folder that holds go.mod, found from the folder the
// test runs in, which is its package's.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
