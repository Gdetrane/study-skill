//go:build linux || darwin

package pluginclient

import (
	"os"

	"golang.org/x/sys/unix"
)

// chmodFolder changes the mode of a folder that is open, by its own name
// beside its descriptor. A folder its owner may not list is open only to
// pass through (regfile.Dir), and the system changes no mode through such a
// descriptor itself: it says the descriptor is a bad one.
func chmodFolder(folder *os.File, mode os.FileMode) error {
	conn, err := folder.SyscallConn()
	if err != nil {
		return err
	}
	var changed error
	if err := conn.Control(func(fd uintptr) {
		changed = unix.Fchmodat(int(fd), ".", uint32(mode.Perm()), 0)
	}); err != nil {
		return err
	}
	return changed
}
