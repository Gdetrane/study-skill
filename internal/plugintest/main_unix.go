//go:build unix

package plugintest

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ignoreStops makes the plugin deaf to the signals that ask it to stop.
func ignoreStops() { signal.Ignore(syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP) }

// stopsCancel returns a context that ends when the plugin is asked to stop,
// unless it is stubborn.
func stopsCancel(ctx context.Context, stubborn bool) (context.Context, context.CancelFunc) {
	if stubborn {
		return context.WithCancel(ctx)
	}
	return signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
}

// stopDead stops the whole program where it is, as a program that hangs in
// a system call is stopped: it reads nothing and writes nothing until it is
// killed.
func stopDead() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
	forever()
}

func streamOf(f *os.File) Stream {
	info, err := f.Stat()
	if err != nil {
		return Stream{}
	}
	s := Stream{Pipe: info.Mode()&os.ModeNamedPipe != 0}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Device, s.Inode = uint64(st.Dev), uint64(st.Ino)
	}
	return s
}

func leadsSession() bool {
	sid, err := unix.Getsid(0)
	return err == nil && sid == os.Getpid()
}

// hasTerminal reports whether the program has a terminal that controls it.
func hasTerminal() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

// processName is what the system calls this process, where /proc can say.
func processName() string {
	name, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(name))
}

// inheritedFiles lists the open files after the three standard streams,
// where /proc can say what each is.
func inheritedFiles() map[int]string {
	files := map[int]string{}
	for fd := 3; fd < 256; fd++ {
		if target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd)); err == nil {
			files[fd] = target
		}
	}
	return files
}

// Gone waits for a process to be gone, for as long as patience allows, and
// reports whether it is. A process that was killed and not yet collected
// still counts as there, which is why this waits.
func Gone(pid int, patience time.Duration) bool {
	for deadline := time.Now().Add(patience); ; time.Sleep(5 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// Kill kills a process a test left behind, so that a failed test does not
// leave it running.
func Kill(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
