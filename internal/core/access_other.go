//go:build !unix

package core

import "os"

// canWrite reports whether path looks writable from its permission bits.
func canWrite(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o200 != 0
}

// canExecute reports whether the file at path can be run, as far as this
// operating system's permission bits tell: they do not.
func canExecute(string) bool { return true }
