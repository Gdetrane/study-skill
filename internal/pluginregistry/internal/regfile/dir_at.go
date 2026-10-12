//go:build linux || darwin

package regfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// A Dir is a folder a walk has open: where it looks at the next name of a
// path and opens it, and what whoever goes on from the walk reads, writes
// and starts a program in.
//
// It is the folder's descriptor, and every name is handed to the system
// beside that descriptor. So a name is looked up in the folder that is
// open, whatever became of the path it was found by, and a Dir takes one
// name at a time: nothing with a / in it, and no .., which would lead out
// of the folder.
//
// The os package has such a type, os.Root, and the walk used it. But
// os.Root opens a folder to list it, and a folder that may be passed
// through and not listed stopped every walk: /home is one on many shared
// computers (mode 711), and everything a learner has is below it. A Dir
// asks to list the folder, as before, and where that is refused it asks
// only to pass through (see passThrough). With that much it can still look
// at a name in the folder, read a link there, open what is in it and make
// something in it, which is all that following a path needs. Only listing
// it is left out.
type Dir struct {
	// file holds the descriptor, and the folder's path as the walk went, for
	// the names of the files opened in it.
	file *os.File
}

// errNotOneName marks a name that is more than one hop.
var errNotOneName = errors.New("not the name of one thing in the folder")

// openDir opens the folder at a path, as the operating system finds it. The
// walk opens only the root folder so; every other folder is opened in the
// one before it, with OpenDir.
func openDir(path string) (*Dir, error) {
	var dir *Dir
	err := uninterrupted(func() (err error) {
		dir, err = openFolder(unix.AT_FDCWD, path, path, 0)
		return err
	})
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return dir, nil
}

// betweenOpens, when a test sets it, is called with a folder's name after
// study was refused to list the folder and before it opens it to pass
// through: where another program could put something else under the name.
var betweenOpens func(name string)

// openFolder opens the folder called name beside the descriptor at: to list
// it, and where that is refused, to pass through it.
//
// The name is looked up twice then, and may name something else the second
// time. Whoever opens a folder so compares what was opened with what they
// looked at before, as the walk does.
func openFolder(at int, name, path string, flags int) (*Dir, error) {
	flags |= unix.O_DIRECTORY | unix.O_CLOEXEC
	fd, err := unix.Openat(at, name, unix.O_RDONLY|flags, 0)
	if errors.Is(err, fs.ErrPermission) {
		if betweenOpens != nil {
			betweenOpens(name)
		}
		fd, err = unix.Openat(at, name, passThrough|flags, 0)
	}
	if err != nil {
		return nil, err
	}
	return &Dir{file: os.NewFile(uintptr(fd), path)}, nil
}

// uninterrupted calls f again for as long as a signal interrupts it, as the
// os package does around its own calls.
func uninterrupted(f func() error) error {
	for {
		if err := f(); err != unix.EINTR {
			return err
		}
	}
}

// oneName reports whether a name is one hop into a folder: the folder
// itself, ".", or the name of something in it.
func oneName(name string) bool {
	return name != "" && name != ".." && !strings.ContainsRune(name, '/')
}

// in calls f with the folder's descriptor, which stays open while f runs,
// for a name that is one hop. op names what f does, for the error.
func (d *Dir) in(op, name string, f func(fd int) error) error {
	err := errNotOneName
	if oneName(name) {
		var conn syscall.RawConn
		if conn, err = d.file.SyscallConn(); err == nil {
			if cerr := conn.Control(func(fd uintptr) {
				err = uninterrupted(func() error { return f(int(fd)) })
			}); cerr != nil {
				err = cerr
			}
		}
	}
	if err != nil {
		return &fs.PathError{Op: op, Path: name, Err: err}
	}
	return nil
}

// path is the path of a name in the folder, as the walk went.
func (d *Dir) path(name string) string { return filepath.Join(d.file.Name(), name) }

// Close closes the folder.
func (d *Dir) Close() error { return d.file.Close() }

// OpenDir opens the folder called name in the folder. A symbolic link under
// the name is not followed.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	var next *Dir
	err := d.in("open", name, func(fd int) (err error) {
		next, err = openFolder(fd, name, d.path(name), unix.O_NOFOLLOW)
		return err
	})
	return next, err
}

// File opens the folder once more, as a file of its own, which the caller
// closes: to list it where study may, and otherwise to pass through it, as
// the folder itself is open. It is found by the descriptor, so it is the
// folder that is open and no other.
func (d *Dir) File() (*os.File, error) {
	again, err := d.OpenDir(".")
	if err != nil {
		return nil, err
	}
	return again.file, nil
}

// Stat looks at the folder itself, through its descriptor: "." is the one
// name it takes. What is in the folder is looked at with Lstat, which
// follows no link out of it.
func (d *Dir) Stat(name string) (fs.FileInfo, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	return d.file.Stat()
}

// Lstat looks at what has the name in the folder. A symbolic link is looked
// at itself, not followed.
func (d *Dir) Lstat(name string) (fs.FileInfo, error) {
	l := &look{name: name}
	if err := d.in("lstat", name, func(fd int) error {
		return unix.Fstatat(fd, name, &l.stat, unix.AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return nil, err
	}
	return l, nil
}

// Readlink reads where the symbolic link called name in the folder leads.
func (d *Dir) Readlink(name string) (string, error) {
	for size := 256; ; size *= 2 {
		target := make([]byte, size)
		var n int
		if err := d.in("readlink", name, func(fd int) (err error) {
			n, err = unix.Readlinkat(fd, name, target)
			return err
		}); err != nil {
			return "", err
		}
		if n < size {
			return string(target[:n]), nil
		}
	}
}

// OpenFile opens the file called name in the folder, as os.OpenFile opens
// one at a path. A symbolic link under the name is not followed: opening
// one is an error.
func (d *Dir) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	var file *os.File
	err := d.in("open", name, func(at int) error {
		fd, err := unix.Openat(at, name, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
		if err != nil {
			return err
		}
		file = os.NewFile(uintptr(fd), d.path(name))
		return nil
	})
	return file, err
}

// Mkdir makes a folder called name in the folder.
func (d *Dir) Mkdir(name string, perm fs.FileMode) error {
	return d.in("mkdir", name, func(fd int) error { return unix.Mkdirat(fd, name, uint32(perm.Perm())) })
}

// Remove removes the file called name from the folder.
func (d *Dir) Remove(name string) error {
	return d.in("remove", name, func(fd int) error { return unix.Unlinkat(fd, name, 0) })
}

// Rename gives the file called oldname in the folder the name newname
// there, in place of whatever has that name.
func (d *Dir) Rename(oldname, newname string) error {
	if !oneName(newname) {
		return &fs.PathError{Op: "rename", Path: newname, Err: errNotOneName}
	}
	return d.in("rename", oldname, func(fd int) error { return unix.Renameat(fd, oldname, fd, newname) })
}

// look is what the system says of a name in an open folder: what it is and
// which file it is. The os package looks at a name only by a path or
// through an os.Root, so a Dir takes its own look, and same compares it.
type look struct {
	name string
	stat unix.Stat_t
}

func (l *look) Name() string       { return l.name }
func (l *look) Size() int64        { return l.stat.Size }
func (l *look) ModTime() time.Time { return time.Unix(l.stat.Mtim.Unix()) }
func (l *look) IsDir() bool        { return l.Mode().IsDir() }
func (l *look) Sys() any           { return &l.stat }

// Mode is the file's mode as the os package gives it. What the system calls
// by a kind this does not know is no regular file.
func (l *look) Mode() fs.FileMode {
	mode := fs.FileMode(l.stat.Mode & 0o777)
	switch l.stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	default:
		mode |= fs.ModeIrregular
	}
	if l.stat.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if l.stat.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if l.stat.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

// identity is what tells one file from every other on a computer: the
// device it is on, and its number there.
type identity struct{ device, number uint64 }

// identityOf takes a file's identity from a look at it, the os package's or
// a Dir's own.
func identityOf(info fs.FileInfo) (identity, bool) {
	switch stat := info.Sys().(type) {
	case *unix.Stat_t:
		return identity{uint64(stat.Dev), uint64(stat.Ino)}, true
	case *syscall.Stat_t:
		return identity{uint64(stat.Dev), uint64(stat.Ino)}, true
	}
	return identity{}, false
}

// same reports whether two looks are at one file, by what identifies it, as
// os.SameFile does. os.SameFile knows only the looks the os package took
// itself, and calls any other two different files.
func same(a, b fs.FileInfo) bool {
	one, ok := identityOf(a)
	other, okToo := identityOf(b)
	if !ok || !okToo {
		return os.SameFile(a, b)
	}
	return one == other
}
