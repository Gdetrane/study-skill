//go:build unix

package regfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// Writable reports whether the current user may make and replace files in
// an open folder, without writing anything. It asks about the folder that
// is open, not about a name.
func Writable(dir *os.Root) bool {
	d, err := dir.Open(".")
	if err != nil {
		return false
	}
	defer d.Close()
	return unix.Faccessat(int(d.Fd()), ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS) == nil
}
