//go:build !unix

package pluginclient

import (
	"os"
	"syscall"
)

// Lamplight supports Linux and macOS. Elsewhere a plugin is started without
// a session of its own, and stopped alone.
func ownSession() *syscall.SysProcAttr { return nil }

func signalGroup(program *os.Process, _ syscall.Signal) { _ = program.Kill() }
