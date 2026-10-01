//go:build !unix

package core

import "os"

// canWrite reports whether path looks writable from its permission bits.
func canWrite(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o200 != 0
}
