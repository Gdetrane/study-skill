//go:build darwin

package cli

import "golang.org/x/sys/unix"

// flushInput discards what was typed at the terminal but not read yet:
// TIOCFLUSH with FREAD (1), the input queue.
func flushInput(fd uintptr) { _ = unix.IoctlSetPointerInt(int(fd), unix.TIOCFLUSH, 1) }
