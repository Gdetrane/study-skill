//go:build unix

package pluginclient

import (
	"os"
	"syscall"
)

// ownSession starts a plugin in a session of its own, which makes it the
// leader of a process group of its own too. So everything it starts can be
// stopped with it, a Ctrl-C in the learner's terminal reaches study and not
// the plugin, and the plugin has no controlling terminal to write to, or to
// read from, behind study's back.
func ownSession() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// signalGroup sends sig to the process group the program leads: the program
// and what it started, unless that moved to a group of its own.
func signalGroup(program *os.Process, sig syscall.Signal) { _ = syscall.Kill(-program.Pid, sig) }
