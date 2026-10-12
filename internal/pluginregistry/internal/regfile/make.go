package regfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// MakeFolder finds the folder at an absolute path, makes what is missing of
// it, for the user alone to read, and returns the folder open with its path
// as the walk went: every symbolic link followed. The caller closes it.
//
// It is the walk that decides, as for the registry's own folder: a path
// that is the Study home, is inside it or is reached through it at any hop
// is refused with an InsideError, and nothing is made. What is made is made
// through the folders the walk opened, never through their names, and the
// path is then walked again from the start, so that the folder returned is
// one that was checked whole.
func (w *Walker) MakeFolder(path string) (*os.File, string, error) {
	for range 3 {
		place, err := w.Walk(path)
		if err != nil {
			return nil, "", err
		}
		switch {
		case place.Leaf != "":
			place.Close()
			return nil, "", &fs.PathError{Op: "mkdir", Path: path, Err: errors.New("it is a file, not a folder")}
		case place.DanglingLink != "":
			link := place.DanglingLink
			place.Close()
			return nil, "", &fs.PathError{Op: "mkdir", Path: path, Err: errors.New("the symbolic link " + link + " on the way leads nowhere")}
		case len(place.Missing) == 0:
			folder, err := place.Folder().Open(".")
			resolved := place.Path()
			place.Close()
			if err != nil {
				return nil, "", err
			}
			return folder, resolved, nil
		case slices.Contains(place.Missing, ".."):
			place.Close()
			return nil, "", &fs.PathError{Op: "mkdir", Path: path, Err: errors.New("the path has .. in a part that is not there")}
		}
		err = makeBelow(place.Folder(), place.Path(), place.Missing)
		place.Close()
		if err != nil {
			return nil, "", err
		}
	}
	return nil, "", &fs.PathError{Op: "mkdir", Path: path, Err: errors.New("it kept being removed while study was making it")}
}

// makeBelow makes the folders named, one inside the other, below an open
// folder.
func makeBelow(dir *os.Root, path string, names []string) error {
	var opened []*os.Root
	defer func() {
		for _, d := range opened {
			_ = d.Close()
		}
	}()
	for _, name := range names {
		path = filepath.Join(path, name)
		if err := dir.Mkdir(name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return &fs.PathError{Op: "mkdir", Path: path, Err: reasonOf(err)}
		}
		next, err := dir.OpenRoot(name)
		if err != nil {
			return &fs.PathError{Op: "open", Path: path, Err: reasonOf(err)}
		}
		opened = append(opened, next)
		dir = next
	}
	return nil
}

// reasonOf is an operating system error without the path it names.
func reasonOf(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
