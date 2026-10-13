package index

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// lockExtension is what a collection's lock file is called after the
// collection's name.
const lockExtension = ".lock"

// Work is one Source being indexed by this Shelf, as the other copies of
// Shelf see it: a row in the collection, and a lock that says the row's
// Shelf is alive.
type Work struct {
	c      *Collection
	id     int64
	source string
	// said is when the row last said how far the work is, and over that
	// the row is gone, by Replace, Fail or End.
	said time.Time
	over bool
}

// lockFile returns the collection's lock file, opened once and kept. It is
// made when create says so, and nil is returned when it is not there
// otherwise: nobody ever indexed in the collection, so nobody is.
//
// To hold a lock, the file is opened for writing. Where Shelf may not write
// in its folder, it is opened for reading, which is enough to see what
// another copy holds.
func (c *Collection) lockFile(create bool) (*os.File, error) {
	c.working.Lock()
	defer c.working.Unlock()
	if c.locks != nil && (c.lockable || !create) {
		return c.locks, nil
	}
	if c.locks != nil {
		// Open for reading, and now to be held: this Shelf holds no lock
		// through it, so closing it lets go of none.
		_ = c.locks.Close()
		c.locks = nil
	}
	name := c.name + lockExtension
	info, err := c.folder.root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !create {
			return nil, nil
		}
	case err != nil:
		return nil, fmt.Errorf("the index of collection %s cannot be opened: %w", c.name, reasonOf(err))
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, &StrangerError{Collection: c.name, File: name, What: "a symbolic link"}
	case !info.Mode().IsRegular():
		return nil, &StrangerError{Collection: c.name, File: name, What: "no file"}
	}
	// Through the folder as it is held: nothing in it leads this out of it.
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	locks, err := c.folder.root.OpenFile(name, flags, 0o600)
	c.lockable = err == nil
	if err != nil && !create {
		locks, err = c.folder.root.OpenFile(name, os.O_RDONLY, 0)
	}
	if err != nil {
		if create {
			return nil, fmt.Errorf("the index of collection %s cannot be written: Shelf may not write in its folder here (%w). Allow "+
				"the folder where study is run, or index from a shell of your own", c.name, reasonOf(err))
		}
		return nil, fmt.Errorf("the index of collection %s cannot be opened: %w", c.name, reasonOf(err))
	}
	c.locks = locks
	return locks, nil
}

// close closes the collection's database and lets go of its lock file, and
// with it of every lock this Shelf held there.
func (c *Collection) close() error {
	err := c.db.Close()
	c.working.Lock()
	defer c.working.Unlock()
	if c.locks != nil {
		_ = c.locks.Close()
		c.locks = nil
	}
	return err
}

// Begin says, to the other copies of Shelf, that this one is indexing a
// Source, and forgets that the Source's last indexing failed: it is being
// indexed again. What it returns is ended by Replace, by Fail or by End.
func (c *Collection) Begin(ctx context.Context, source string) (*Work, error) {
	locks, err := c.lockFile(true)
	if err != nil {
		return nil, err
	}
	w := &Work{c: c, source: source}
	// A number no other work has, to hold the lock at: one of four million
	// million million, taken again in the case nobody will see.
	for tries := 0; tries < 8; tries++ {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		id := int64(binary.BigEndian.Uint64(raw[:])>>2) + 1
		c.working.Lock()
		err := errTaken
		if !c.mine[id] {
			if err = hold(locks, id); err == nil {
				c.mine[id] = true
			}
		}
		c.working.Unlock()
		if err == nil {
			w.id = id
		}
		if !errors.Is(err, errTaken) {
			break
		}
	}
	// Where the folder is on a file system that has no such locks, the work
	// is done all the same and not shown (its number stays 0): a row without
	// a lock behind it would count for nothing, and the other copies say
	// what is indexed, as if nobody were indexing.
	if err := c.begin(ctx, w, locks); err != nil {
		w.over = true
		w.End()
		return nil, err
	}
	return w, nil
}

func (c *Collection) begin(ctx context.Context, w *Work, locks *os.File) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Rows of work whose Shelf is gone, killed in the middle of it, are
	// swept out by the next Shelf that has work of its own.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work`)
	if err != nil {
		return c.wrap(err)
	}
	var gone []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return c.wrap(err)
		}
		if !c.own(id) && !heldElsewhere(locks, id) {
			gone = append(gone, id)
		}
	}
	if err := rows.Close(); err != nil {
		return c.wrap(err)
	}
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, id); err != nil {
			return c.wrap(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET failed = NULL, asked = NULL WHERE id = ? AND failed IS NOT NULL`, w.source); err != nil {
		return c.wrap(err)
	}
	if w.id != 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO work (id, source) VALUES (?, ?)`, w.id, w.source); err != nil {
			return c.wrap(err)
		}
	}
	return c.wrap(tx.Commit())
}

// own reports whether a piece of work is this Shelf's.
func (c *Collection) own(id int64) bool {
	c.working.Lock()
	defer c.working.Unlock()
	return c.mine[id]
}

// Progress says how far the work is, from 0 to 1, to the other copies of
// Shelf. It is said twice a second at most, and a time it cannot be said is
// passed over: the work goes on.
func (w *Work) Progress(ctx context.Context, done float64) {
	if w.over || time.Since(w.said) < 500*time.Millisecond {
		return
	}
	w.said = time.Now()
	_, _ = w.c.db.ExecContext(ctx, `UPDATE work SET progress = ? WHERE id = ?`, done, w.id)
}

// Replace puts the content the work read in the place of what the index
// held for the Source, as Collection.Replace does, and ends the work in the
// same transaction: no other copy sees the Source being indexed once its
// content is there.
func (w *Work) Replace(ctx context.Context, source Source, passages []Passage, vectors [][]float32) error {
	if err := w.c.replace(ctx, source, passages, vectors, w); err != nil {
		return err
	}
	w.over = true
	return nil
}

// Embed gives vectors to the Passages that are indexed for the Source, as
// Collection.Embed does, and ends the work in the same transaction.
func (w *Work) Embed(ctx context.Context, hash string, texts []string, made Made, vectors [][]float32) error {
	if err := w.c.embed(ctx, w.source, hash, texts, made, vectors, w); err != nil {
		return err
	}
	w.over = true
	return nil
}

// Fail records why the work failed, as Collection.Fail does, and ends the
// work in the same transaction.
func (w *Work) Fail(ctx context.Context, source Source, asked, reason string) error {
	if err := w.c.fail(ctx, source, asked, reason, w); err != nil {
		return err
	}
	w.over = true
	return nil
}

// End ends work that was stopped, and lets go of what Replace or Fail left
// held. Nothing of the Source changes.
func (w *Work) End() {
	if !w.over {
		w.over = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// A row that cannot be removed is one no lock stands behind: it
		// counts for nothing, and is swept out later.
		_, _ = w.c.db.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, w.id)
		cancel()
	}
	w.c.working.Lock()
	defer w.c.working.Unlock()
	if w.c.mine[w.id] {
		delete(w.c.mine, w.id)
		if w.c.locks != nil {
			release(w.c.locks, w.id)
		}
	}
}

// Working returns the Sources that other copies of Shelf are indexing now,
// each with how far its work is, or nil where that is not known. What this
// Shelf is indexing itself is not among them.
func (c *Collection) Working(ctx context.Context) (map[string]*float64, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, source, progress FROM work`)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer rows.Close()
	type row struct {
		id       int64
		source   string
		progress *float64
	}
	var work []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.source, &r.progress); err != nil {
			return nil, c.wrap(err)
		}
		work = append(work, r)
	}
	if err := rows.Err(); err != nil || len(work) == 0 {
		return nil, c.wrap(err)
	}
	locks, err := c.lockFile(false)
	if err != nil || locks == nil {
		return nil, err
	}
	var out map[string]*float64
	for _, r := range work {
		if c.own(r.id) || !heldElsewhere(locks, r.id) {
			continue
		}
		if out == nil {
			out = map[string]*float64{}
		}
		// Two copies on one Source: the one that is further.
		if had, ok := out[r.source]; !ok || had == nil || (r.progress != nil && *r.progress > *had) {
			out[r.source] = r.progress
		}
	}
	return out, nil
}
