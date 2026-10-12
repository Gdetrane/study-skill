package index

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
)

// connector opens the connections of one collection's database. database/sql
// asks it for one whenever its pool needs another, at any time while Shelf
// runs, and each is held to the same checks as the first.
type connector struct {
	folder     *Folder
	collection string
	file       string
}

func (c *connector) Driver() driver.Driver { return &sqlite.Driver{} }

// Connect opens a connection to the collection's file, and gives it back
// only when the file SQLite opened is the collection's, in the folder Shelf
// holds, and in a format this Shelf reads. Nothing is written through the
// connection before that is known.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if info, err := c.folder.plain(c.collection, c.file); err != nil {
		return nil, err
	} else if info == nil {
		return nil, fmt.Errorf("the index of collection %s is no longer there", c.collection)
	}
	if err := c.folder.held(); err != nil {
		return nil, err
	}
	path := filepath.Join(c.folder.path, c.file)
	// mode=rw: SQLite opens the file Shelf made, and makes none. A URI, so
	// the path is escaped: a state folder may have any character in its name.
	base, err := sqlite.NewConnector("file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=rw&_txlock=immediate&_defensive=true")
	if err != nil {
		return nil, c.unreadable(err)
	}
	conn, err := base.Connect(ctx)
	if err != nil {
		return nil, c.unreadable(err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	// SQLite follows the links in a path itself before it opens it, and
	// says here where that led. Anywhere but the name it was given is a link
	// that was not there a moment ago.
	if err := patiently(ctx, func() error {
		_, err := conn.(driver.ExecerContext).ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyFor.Milliseconds()), nil)
		return err
	}); err != nil {
		return nil, c.unreadable(err)
	}
	opened, err := values(ctx, conn, "SELECT file FROM pragma_database_list WHERE name = 'main'")
	if err != nil {
		return nil, c.unreadable(err)
	}
	if name, _ := opened[0].(string); name != path {
		return nil, &StrangerError{Collection: c.collection, File: c.file, What: "a link to another place"}
	}
	// Then what the file says it is, from its header: an index of Shelf's,
	// in a format this Shelf reads, or a file with nothing in it yet.
	header, err := values(ctx, conn, "SELECT (SELECT application_id FROM pragma_application_id), "+
		"(SELECT user_version FROM pragma_user_version), (SELECT count(*) FROM sqlite_schema)")
	if err != nil {
		return nil, c.unreadable(err)
	}
	application, _ := header[0].(int64)
	format, _ := header[1].(int64)
	objects, _ := header[2].(int64)
	switch {
	case application == 0 && format == 0 && objects == 0:
		// New: Collection writes what a collection's file holds.
	case application != applicationID || format < 1:
		return nil, &StrangerError{Collection: c.collection, File: c.file, What: "a database of another program's"}
	case format > Format:
		return nil, &NewerError{Collection: c.collection, Format: format}
	}
	for _, pragma := range []string{
		// Readers do not wait for the one writer, nor it for them: a search
		// is answered while a book is being written into the index.
		"PRAGMA journal_mode = WAL",
		// A transaction that committed stays, a loss of power included.
		"PRAGMA synchronous = FULL",
		"PRAGMA foreign_keys = ON",
		// The file holds data. No function in its schema is trusted, and
		// nothing SQLite needs for a while is written to a temporary folder.
		"PRAGMA trusted_schema = OFF",
		"PRAGMA temp_store = MEMORY",
	} {
		if err := patiently(ctx, func() error {
			_, err := conn.(driver.ExecerContext).ExecContext(ctx, pragma, nil)
			return err
		}); err != nil {
			return nil, c.unreadable(err)
		}
	}
	ok = true
	return conn, nil
}

// patiently does something again for as long as another copy of Shelf is in
// its way, up to busyFor. SQLite waits by itself for a lock that a statement
// needs, with one exception that matters here: a database that two copies
// find new at once is put into WAL mode by one of them, and the other is
// told that it is busy, at once.
func patiently(ctx context.Context, do func() error) error {
	deadline := time.Now().Add(busyFor)
	for {
		err := do()
		var failure *sqlite.Error
		// SQLITE_BUSY, and the codes that say why.
		if err == nil || !errors.As(err, &failure) || failure.Code()&0xFF != 5 || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// values runs a query that gives one row, and returns the row.
func values(ctx context.Context, conn driver.Conn, query string) (row []driver.Value, err error) {
	err = patiently(ctx, func() error {
		rows, err := conn.(driver.QueryerContext).QueryContext(ctx, query, nil)
		if err != nil {
			return err
		}
		defer rows.Close()
		row = make([]driver.Value, len(rows.Columns()))
		if err := rows.Next(row); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the database has no main file")
			}
			return err
		}
		return nil
	})
	return row, err
}

// unreadable words an error of SQLite's for the learner.
func (c *connector) unreadable(err error) error {
	var stranger *StrangerError
	if errors.As(err, &stranger) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("the index of collection %s cannot be read (%w). Remove %s from Shelf's folder and index the Topic's "+
		"Sources again, if this does not pass", c.collection, reasonOf(err), c.file)
}
