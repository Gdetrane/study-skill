//go:build unix

package index

import (
	"io/fs"
	"syscall"
)

// links is how many names a file has. A file with a second name elsewhere
// is one that writing to would change there too.
func links(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 1
}
