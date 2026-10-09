//go:build !unix

package regfile

import "os"

// Writable reports whether an open folder looks writable from its
// permission bits.
func Writable(dir *os.Root) bool {
	info, err := dir.Stat(".")
	return err == nil && info.Mode().Perm()&0o200 != 0
}
