//go:build !linux && !darwin

package regfile

import (
	"io/fs"
	"os"
)

// A Dir is a folder a walk has open: where it looks at the next name of a
// path and opens it, and what whoever goes on from the walk reads, writes
// and starts a program in.
//
// Lamplight supports Linux and macOS, where a Dir is the folder's
// descriptor and can be one that only passes through the folder. Anywhere
// else it is an os.Root, which opens a folder to list it: a folder that may
// be passed through and not listed stops the walk there.
type Dir struct{ *os.Root }

// openDir opens the folder at a path, as the operating system finds it. The
// walk opens only the root folder so; every other folder is opened in the
// one before it, with OpenDir.
func openDir(path string) (*Dir, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Dir{root}, nil
}

// OpenDir opens the folder called name in the folder.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	root, err := d.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &Dir{root}, nil
}

// File opens the folder once more, as a file of its own, which the caller
// closes.
func (d *Dir) File() (*os.File, error) { return d.Root.Open(".") }

// same reports whether two looks are at one file.
func same(a, b fs.FileInfo) bool { return os.SameFile(a, b) }
