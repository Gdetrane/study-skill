//go:build !unix

package core

import (
	"errors"
	"os"
)

// Windows packages come later (see the design's Scope); until then writes
// that need the Topic lock report that they are unsupported.
func tryLock(*os.File) (bool, error) {
	return false, errors.New("locking Topics is not supported on this operating system yet")
}

func unlockFile(*os.File) error { return nil }
