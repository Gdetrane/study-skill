//go:build unix

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// flockTry takes an exclusive flock on f without waiting. flock locks belong
// to the open file, so two opens in one process exclude each other too.
func flockTry(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EINTR):
		return false, nil
	default:
		return false, err
	}
}

func flockRelease(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// agentProcessGroup runs an agent's command in a process group of its own:
// when its time is up the whole group gets SIGTERM, so a child it started
// cannot keep study waiting on the output pipe, and WaitDelay bounds the wait
// for anything that escaped the group.
func agentProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
}

// killAgentGroup kills whatever is left of an agent command's process group
// after it was stopped.
func killAgentGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
