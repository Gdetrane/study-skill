//go:build unix

package cli

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// canWrite reports whether the current user may write to path, without
// writing anything.
func canWrite(path string) bool { return unix.Access(path, unix.W_OK) == nil }

// linkCount returns the number of hard links to the file info describes.
func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
