//go:build unix

package pluginclient_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// A plugin that is started to index writes its index in its folder. study
// makes sure of the folder first, by writing in it, and leaves nothing
// there: a plugin finds its folder as it left it.
func TestAPluginStartedToIndexCanWriteInItsFolder(t *testing.T) {
	c := plugintest.NewComputer(t)
	plugin := c.Register(t, "kb", plugintest.Command()...)
	opts := quick(c)
	opts.Indexes = true
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	entries, err := os.ReadDir(s.DataFolder())
	if err != nil || len(entries) != 0 {
		t.Errorf("the plugin's folder holds %v after study checked it, %v; want nothing", entries, err)
	}
}

// A folder nothing can be written in, as inside a sandbox that allows the
// Study home alone, stops a plugin that is started to index before it
// starts, and the error gives both ways out. A plugin that is only asked
// what it has indexed is started all the same: the check is not made for it.
func TestAPluginIsNotStartedToIndexWithoutAFolderToWriteIn(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
	// The folder refuses as a sandbox does: its mode allows what the system
	// then denies, which no folder of a test's own does.
	var checked atomic.Int64
	defer pluginclient.SetCheckWritable(func(folder *os.File) error {
		checked.Add(1)
		return &os.PathError{Op: "openat", Path: filepath.Join(folder.Name(), pluginclient.ProbeName+"x"), Err: syscall.EROFS}
	})()

	opts := quick(c)
	opts.Indexes = true
	_, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts)
	if codeOf(err) != pluginclient.CodeFailedPrecondition {
		t.Fatalf("Start, to index = %v (%s); want failed_precondition", err, codeOf(err))
	}
	for _, want := range []string{"Knowledge base plugin kb cannot keep an index: nothing can be written in its folder, " + c.DataFolder("kb") +
		" (read-only file system)", "either allow that folder in the sandbox, or run the command in a shell of your own"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	// The name of the file study tried is the plugin's folder's business.
	if strings.Contains(err.Error(), pluginclient.ProbeName) {
		t.Errorf("the error names the file study tried to make: %v", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the plugin was started without a folder it can write in")
	}
	if checked.Load() != 1 {
		t.Errorf("the folder was checked %d times, want once", checked.Load())
	}

	// Asked only what it has indexed, it is started: it may well be able to
	// read its index there.
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if err != nil {
		t.Fatalf("Start, not to index: %v", err)
	}
	s.Close()
	if checked.Load() != 1 {
		t.Error("a plugin that is not started to index had its folder written in")
	}
}

// A folder that cannot be made says both ways out too.
func TestAFolderThatCannotBeMadeSaysBothWaysOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write in every folder, whatever its mode")
	}
	c := plugintest.NewComputer(t)
	plugin := c.Register(t, "kb", plugintest.Command()...)
	state := filepath.Join(c.Home, ".local", "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o755) })
	_, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "study cannot make the folder Knowledge base plugin kb keeps its index in") ||
		!strings.Contains(err.Error(), "either allow that folder in the sandbox, or run the command in a shell of your own") {
		t.Errorf("Start = %v (%s)", err, codeOf(err))
	}
}
