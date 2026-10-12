//go:build !unix

package index

import "os"

// Where a program cannot hold a lock that the system lets go of when it
// ends, a Shelf sees only its own work. See lock_unix.go.

func hold(*os.File, int64) error { return nil }

func release(*os.File, int64) {}

func heldElsewhere(*os.File, int64) bool { return false }
