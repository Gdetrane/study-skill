//go:build unix

package core

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// ownProcessGroup runs cmd in a process group of its own, so everything it
// starts can be stopped together: when ctx is done the whole group gets
// SIGTERM, and whatever still runs after the grace period is killed (see
// stopGroup).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = checkGrace
}

// stopGroup kills whatever is left in the process group led by pid once its
// leader has exited, and reports whether anything was. A Check that leaves
// programs running in the background is not a valid Attempt.
func stopGroup(pid int) bool {
	if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Killed processes linger until their new parent reaps them; wait,
	// briefly, until the group is gone.
	for deadline := time.Now().Add(checkGrace); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
	}
	return true
}
