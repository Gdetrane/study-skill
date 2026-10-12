//go:build unix

package index

import (
	"os"
	"syscall"
)

// A Shelf that is indexing a Source holds a lock on one byte of the
// collection's lock file, at the number of that piece of work, for as long
// as it works. The system lets go of the lock when the program ends, however
// it ends: so another copy of Shelf can tell work that is going on now from
// work that was cut short, which a row in the database alone cannot say.
//
// The lock is the kind a program holds as a whole (POSIX, by fcntl), on
// Linux and on macOS. Such a lock is let go of when the program closes any
// descriptor of the file, so a collection opens its lock file once and keeps
// it (Collection.lockFile).

func lockAt(id int64, kind int16) *syscall.Flock_t {
	return &syscall.Flock_t{Type: kind, Whence: 0, Start: id, Len: 1}
}

// hold takes the lock of the work with that number.
func hold(f *os.File, id int64) error {
	return syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, lockAt(id, syscall.F_WRLCK))
}

// release lets go of it.
func release(f *os.File, id int64) {
	_ = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, lockAt(id, syscall.F_UNLCK))
}

// heldElsewhere reports whether another program holds the lock of the work
// with that number: whether that work is going on now. It takes nothing.
func heldElsewhere(f *os.File, id int64) bool {
	lock := lockAt(id, syscall.F_WRLCK)
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, lock); err != nil {
		return false
	}
	return lock.Type != syscall.F_UNLCK
}
