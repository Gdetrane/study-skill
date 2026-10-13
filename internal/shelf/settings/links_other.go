//go:build !unix

package settings

import "io/fs"

// links is how many names a file has, where the system says. See
// links_unix.go.
func links(fs.FileInfo) uint64 { return 1 }
