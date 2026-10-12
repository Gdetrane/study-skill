// Package index is Shelf's index: one SQLite file for each collection, in
// the folder study gives Shelf to keep it in (ADR-0012).
//
// The folder is opened once and held (os.Root), and everything Shelf itself
// does in it goes through what was opened: the contract has a plugin follow
// no link out of its folder. SQLite opens its files by name, so three things
// stand between a link in the folder and a write where it leads:
//
//   - before every connection, the names SQLite is about to open, the
//     collection's file and the three it keeps beside it, are looked at
//     through the held folder: each is a plain file with one name, or not
//     there;
//   - SQLite follows no link at the last name of a path it opens
//     (O_NOFOLLOW), and never makes the collection's file itself: Shelf
//     makes it, through the held folder;
//   - once a connection is open, and before anything is written through it,
//     SQLite is asked which file it opened, and that must be the collection's
//     file in the held folder, by the folder's real path.
//
// What that leaves is a folder above Shelf's, in the learner's state folder,
// exchanged for a link between two of SQLite's own system calls.
//
// A collection's file holds its Sources and their Passages, and a full-text
// index of the Passages (FTS5). It carries a format number, and a file in a
// newer format is refused, never opened and guessed at.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

const (
	// Format is the format of a collection's file that this package writes,
	// and the newest it reads. It is the file's user_version, which SQLite
	// keeps in the file's header: it can be read before anything else is.
	Format = 1
	// applicationID marks a file as an index of Shelf's, in the header's
	// application_id: "Shlf".
	applicationID = 0x53686C66
	// extension is what a collection's file is called after the collection's
	// name. A collection's name holds no dot, so no two collections share a
	// file, and none shares one with what SQLite keeps beside another's.
	extension = ".sqlite"
	// busyFor is how long a connection waits for another copy of Shelf that
	// is writing the same collection. A write is one short transaction.
	busyFor = 10 * time.Second
)

// beside are the endings of the files SQLite keeps beside a database: its
// write-ahead log, the log's index, and the journal of a database that is
// not in WAL mode.
var beside = []string{"-wal", "-shm", "-journal"}

// Options say how a Folder behaves.
type Options struct {
	// Crash is only for tests that simulate a crash, and must be nil
	// otherwise. It is called at each point of a write where a crash could
	// happen, with the point's name.
	Crash func(point string)
}

// Folder is the folder Shelf keeps its index in, opened once.
type Folder struct {
	root *os.Root
	// path is the folder's real path, with no link in it, and info what the
	// folder was when it was opened: SQLite is given names under path, and
	// only for as long as path still names the folder that is held.
	path  string
	info  fs.FileInfo
	crash func(string)

	mu     sync.Mutex
	open   map[string]*Collection
	closed bool
}

// Open opens the folder at dir, an absolute path of a folder that exists.
func Open(dir string, opts Options) (*Folder, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("Shelf's folder is not named by an absolute path")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("Shelf's folder cannot be opened: %w", reasonOf(err))
	}
	f := &Folder{root: root, crash: opts.Crash, open: map[string]*Collection{}}
	if f.crash == nil {
		f.crash = func(string) {}
	}
	if f.info, err = root.Stat("."); err == nil {
		f.path, err = filepath.EvalSymlinks(dir)
	}
	if err == nil {
		err = f.held()
	}
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("Shelf's folder cannot be opened: %w", reasonOf(err))
	}
	return f, nil
}

// held checks that the folder's real path still names the folder that was
// opened.
func (f *Folder) held() error {
	now, err := os.Stat(f.path)
	if err != nil || !os.SameFile(now, f.info) {
		return errors.New("Shelf's folder was moved or replaced while Shelf was running: start Shelf again")
	}
	return nil
}

// Close closes every collection and lets go of the folder.
func (f *Folder) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	var first error
	for name, c := range f.open {
		if err := c.db.Close(); err != nil && first == nil {
			first = err
		}
		delete(f.open, name)
	}
	if err := f.root.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// Collection returns the collection of that name. When nothing was ever
// indexed in it, it is made if create says so, and nil is returned
// otherwise: a collection nobody indexed is empty, and asking about it
// leaves nothing behind.
func (f *Folder) Collection(ctx context.Context, name string, create bool) (*Collection, error) {
	// The name becomes a file's name. study checked it and so did Shelf's
	// tools; it is checked where it is used all the same.
	if err := plugincontract.CheckCollection(name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("Shelf is stopping")
	}
	if err := f.held(); err != nil {
		return nil, err
	}
	file := name + extension
	if c := f.open[name]; c != nil {
		// Still the file that was opened: the learner may have removed it,
		// to have a collection indexed from nothing, while this copy of
		// Shelf went on running.
		if now, err := f.root.Lstat(file); err == nil && os.SameFile(now, c.info) {
			return c, nil
		}
		_ = c.db.Close()
		delete(f.open, name)
	}

	info, err := f.plain(name, file)
	if err != nil {
		return nil, err
	}
	if info == nil {
		if !create {
			return nil, nil
		}
		made, err := f.root.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			_ = made.Close()
		case !errors.Is(err, fs.ErrExist): // another copy of Shelf made it just now
			return nil, fmt.Errorf("the index of collection %s cannot be made: %w", name, reasonOf(err))
		}
		f.crash(CrashFileMade)
		if info, err = f.plain(name, file); err != nil || info == nil {
			return nil, fmt.Errorf("the index of collection %s cannot be made: %w", name, cmpOr(err, fs.ErrNotExist))
		}
	}

	db := sql.OpenDB(&connector{folder: f, collection: name, file: file})
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	c := &Collection{name: name, db: db, info: info, crash: f.crash}
	empty, err := c.empty(ctx)
	if err == nil && empty && create {
		err = c.initialise(ctx)
		empty = false
	}
	if err != nil || empty {
		// A file with nothing in it is what a Shelf that was stopped left
		// between making the file and writing to it: no collection yet.
		_ = db.Close()
		return nil, err
	}
	f.open[name] = c
	return c, nil
}

// plain looks, through the held folder, at the file of a collection and at
// what SQLite keeps beside it: each is a plain file with one name, or is not
// there. It returns what the collection's file is, and nil when it is not
// there.
func (f *Folder) plain(collection, file string) (fs.FileInfo, error) {
	var main fs.FileInfo
	for _, name := range append([]string{file}, besides(file)...) {
		info, err := f.root.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("the index of collection %s cannot be opened: %w", collection, reasonOf(err))
		case info.Mode()&fs.ModeSymlink != 0:
			return nil, &StrangerError{Collection: collection, File: name, What: "a symbolic link"}
		case !info.Mode().IsRegular():
			return nil, &StrangerError{Collection: collection, File: name, What: "no file"}
		case links(info) > 1:
			return nil, &StrangerError{Collection: collection, File: name, What: "a file that has a second name elsewhere"}
		}
		if name == file {
			main = info
		}
	}
	return main, nil
}

func besides(file string) []string {
	names := make([]string, len(beside))
	for i, ending := range beside {
		names[i] = file + ending
	}
	return names
}

// cmpOr returns err, or other when err is nil.
func cmpOr(err, other error) error {
	if err != nil {
		return err
	}
	return other
}

// reasonOf is an operating system error without the path it names: a
// message of Shelf's is for the learner, and the path is Shelf's own.
func reasonOf(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}

// StrangerError says that something in Shelf's folder, under a name Shelf
// uses, is not what Shelf made there.
type StrangerError struct {
	Collection, File, What string
}

func (e *StrangerError) Error() string {
	return fmt.Sprintf("the index of collection %s cannot be opened: %s in Shelf's folder is %s, and not what Shelf made. "+
		"Remove it, and index the Topic's Sources again", e.Collection, e.File, e.What)
}

// NewerError says that a collection's file was written in a format newer
// than this Shelf reads.
type NewerError struct {
	Collection string
	Format     int64
}

func (e *NewerError) Error() string {
	return fmt.Sprintf("the index of collection %s is in format %d, which a newer Shelf wrote, and this one reads format %d. "+
		"Update Lamplight, or remove %s from Shelf's folder and index the Topic's Sources again", e.Collection, e.Format, Format,
		e.Collection+extension)
}
