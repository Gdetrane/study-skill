//go:build unix

package cli

import "golang.org/x/sys/unix"

// mayRun reports whether whoever runs study may run the file at path: the
// system is asked, since a file's mode says that someone may, and not who.
func mayRun(path string) bool {
	return unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS) == nil
}
