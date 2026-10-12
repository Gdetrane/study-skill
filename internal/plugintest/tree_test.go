//go:build unix

package plugintest

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What a plugin left in a folder is described without opening anything but
// a file: a pipe nobody writes to would hold the suite for ever, where it
// should say that the plugin made one.
func TestAFolderIsDescribedWithoutReadingAPipe(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("no pipe can be made here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	described := make(chan string, 1)
	go func() { described <- tree(t, dir) }()
	select {
	case got := <-described:
		if !strings.Contains(got, "pipe "+os.ModeNamedPipe.String()) || !strings.Contains(got, "file 1 bytes") {
			t.Errorf("the folder is described as:\n%s\nwant the pipe named as one, and the file read", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("describing a folder that holds a pipe did not end: the pipe was opened")
	}
}
