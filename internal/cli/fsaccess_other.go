//go:build !unix

package cli

import "os"

// canWrite reports whether path looks writable from its permission bits.
func canWrite(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o200 != 0
}

// linkCount is 1 where hard links cannot be counted.
func linkCount(os.FileInfo) uint64 { return 1 }
