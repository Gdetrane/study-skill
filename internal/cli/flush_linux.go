//go:build linux

package cli

import "golang.org/x/sys/unix"

// flushInput discards what was typed at the terminal but not read yet.
func flushInput(fd uintptr) { _ = unix.IoctlSetInt(int(fd), unix.TCFLSH, unix.TCIFLUSH) }
