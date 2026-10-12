//go:build linux || darwin

package pluginclient

import (
	"crypto/rand"
	"encoding/hex"
	"os"

	"golang.org/x/sys/unix"
)

// writable reports whether a file can be made in a folder that is open, by
// making an empty one and removing it again, both by its name beside the
// folder's descriptor: the folder is the one that was checked, whatever its
// path names by now. Asking the system whether the folder may be written
// (access) would not do. It answers by the folder's mode, and a sandbox
// refuses what the mode allows.
func writable(folder *os.File) error {
	conn, err := folder.SyscallConn()
	if err != nil {
		return err
	}
	random := make([]byte, 8)
	_, _ = rand.Read(random) // crypto/rand.Read never returns an error.
	name := probeName + hex.EncodeToString(random)
	var made error
	if err := conn.Control(func(fd uintptr) {
		var file int
		if file, made = unix.Openat(int(fd), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600); made != nil {
			return
		}
		_ = unix.Close(file)
		made = unix.Unlinkat(int(fd), name, 0)
	}); err != nil {
		return err
	}
	return made
}

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
