//go:build unix

package pluginclient

import (
	"os"
	"strconv"
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

// sealInherited keeps a plugin from inheriting what study itself inherited.
//
// Every file Go opens is closed in a program it starts, unless it is handed
// on by name. A descriptor study was started with is not: whoever started
// study with 9<secret, or with a copy of its own standard output, would
// hand that to the plugin, which could read the one and write past study's
// --json on the other. So every descriptor of this process after its three
// standard streams is marked to close when a program is started. That holds
// for the whole process from then on, which is what study wants of them: it
// hands a program it starts only what it names.
func sealInherited() {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if fd, err := strconv.Atoi(entry.Name()); err == nil && fd > 2 {
				syscall.CloseOnExec(fd)
			}
		}
		return
	}
	// Nowhere lists them: every number a descriptor can have, then.
	most := 65536
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err == nil && uint64(limit.Cur) < uint64(most) {
		most = int(limit.Cur)
	}
	for fd := 3; fd < most; fd++ {
		syscall.CloseOnExec(fd)
	}
}
