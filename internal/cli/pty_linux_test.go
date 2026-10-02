//go:build linux

package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal pair, or skips the test where there is none.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	raw, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) {
		if ioctlErr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioctlErr == nil {
			n, ioctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN)
		}
	}); err != nil || ioctlErr != nil {
		t.Skipf("cannot unlock the pseudo-terminal: %v %v", err, ioctlErr)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

// TestJSONNeverQueriesTheTerminal runs study with the process's stdout on a
// terminal. fang asks the terminal for its background colour before styling
// help and errors; with --json, or for doctor's report, nothing may be
// written to the terminal and nothing may wait for its answer.
func TestJSONNeverQueriesTheTerminal(t *testing.T) {
	master, slave := openPTY(t)
	savedOut, savedIn := os.Stdout, os.Stdin
	os.Stdout, os.Stdin = slave, slave
	t.Cleanup(func() { os.Stdout, os.Stdin = savedOut, savedIn })

	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // no git, so doctor finds a failure
	env := map[string]string{"HOME": home, "STUDY_HOME": filepath.Join(home, "study")}
	for _, args := range [][]string{
		{"doctor", "--json"},
		{"doctor"},
		{"status", "--bogus", "--json"},
		{"completion", "--json"},
		{"topic", "update", "nosuch", "--title", "X", "--json"},
	} {
		start := time.Now()
		runEnv(t, env, home, nil, args...)
		if took := time.Since(start); took > time.Second {
			t.Errorf("%v took %v: it waited for the terminal", args, took)
		}
	}
	_ = master.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 256)
	if n, _ := master.Read(buf); n > 0 {
		t.Errorf("study wrote to the terminal: %q", buf[:n])
	}

	// A human usage error still goes through fang, which does query the
	// terminal: this proves the check above can see a query.
	_ = master.SetReadDeadline(time.Time{})
	seen := make(chan []byte, 1)
	go func() {
		var all []byte
		for {
			n, err := master.Read(buf)
			if err != nil {
				seen <- all
				return
			}
			all = append(all, buf[:n]...)
			if bytes.Contains(all, []byte("\x1b]11;?")) {
				// Answer as a dark terminal would, so fang stops waiting.
				_, _ = master.Write([]byte("\x1b]11;rgb:0000/0000/0000\x07\x1b[?62;c"))
				seen <- all
				return
			}
		}
	}()
	runEnv(t, env, home, nil, "status", "--bogus")
	select {
	case got := <-seen:
		if !bytes.Contains(got, []byte("\x1b]11;?")) {
			t.Errorf("no query seen from a human error: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("a human error did not query the terminal: the check above may be vacuous")
	}
}
