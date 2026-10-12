//go:build !unix

package index

import (
	"errors"
	"os"
)

// Where a program cannot hold a lock that the system lets go of when it
// ends, a Shelf sees only its own work, and shows none. See lock_unix.go.

// errTaken says that another program holds the lock that was asked for.
var errTaken = errors.New("the lock is held")

func hold(*os.File, int64) error { return errors.ErrUnsupported }

func release(*os.File, int64) {}

func heldElsewhere(*os.File, int64) bool { return false }
