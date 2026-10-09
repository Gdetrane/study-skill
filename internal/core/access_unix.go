//go:build unix

package core

import "golang.org/x/sys/unix"

// canWrite reports whether the current user may write to path, without
// writing anything.
func canWrite(path string) bool { return unix.Access(path, unix.W_OK) == nil }
