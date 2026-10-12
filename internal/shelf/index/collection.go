package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// The points of a write where Options.Crash is called.
const (
	// CrashFileMade: the collection's file is there, with nothing in it.
	CrashFileMade = "the collection's file is made"
	// CrashSchemaHalf: the first table of a new collection is made, inside
	// the transaction that makes all of them.
	CrashSchemaHalf = "half of a new collection's tables are made"
	// CrashEarlierGone: a Source's earlier Passages are deleted, inside the
	// transaction that replaces them.
	CrashEarlierGone = "the earlier Passages are deleted"
	// CrashHalfWritten: half of a Source's new Passages are written, inside
	// that transaction.
	CrashHalfWritten = "half of the new Passages are written"
	// CrashBeforeCommit: all of them are, and the transaction is still open.
	CrashBeforeCommit = "the new Passages are written, and not committed"
	// CrashCommitted: the transaction has committed.
	CrashCommitted = "the new Passages are committed"
)

// schema is what a collection's file holds, in Format 1.
//
// A Source's row says which content of it is indexed: hash is that
// content's, and is NULL while nothing of the Source is. failed is why its
// last indexing failed, and NULL when it did not, and asked is what that
// indexing was asked with: the hash it was handed for a file, the address of
// a web page. A Source can have both a hash and a failure: new content that
// could not be read, and earlier content that is still found.
//
// A Passage's text is kept once, in the full-text table, under the Passage's
// key. Whatever else is said of a Passage, a vector for it, hangs on that
// key.
//
// A row of work says that a Shelf is indexing a Source, to the other copies
// of Shelf: study starts one for every question it has, and the one that is
// asked what is being indexed is seldom the one that indexes. The row alone
// would outlive a Shelf that was killed, so it counts only while that Shelf
// holds the lock at its id (lock_unix.go).
var schema = []string{
	`CREATE TABLE sources (
		id      TEXT PRIMARY KEY,
		kind    TEXT NOT NULL,
		title   TEXT NOT NULL,
		hash    TEXT,
		address TEXT,
		failed  TEXT,
		asked   TEXT
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE work (
		id       INTEGER PRIMARY KEY,
		source   TEXT NOT NULL,
		progress REAL
	) STRICT`,
	`CREATE TABLE passages (
		key        INTEGER PRIMARY KEY,
		id         TEXT NOT NULL UNIQUE,
		source     TEXT NOT NULL REFERENCES sources(id),
		place      INTEGER NOT NULL,
		first_page INTEGER,
		last_page  INTEGER
	) STRICT`,
	`CREATE INDEX passages_of_source ON passages(source, place)`,
	// Words are compared without their case and their accents, and by
	// their stem, so that "eliminating" finds "elimination".
	`CREATE VIRTUAL TABLE passages_text USING fts5(text, tokenize = 'porter unicode61 remove_diacritics 2')`,
}

// Collection is the index of one collection.
type Collection struct {
	name string
	db   *sql.DB
	// info is the collection's file as it was when it was opened.
	info  fs.FileInfo
	crash func(string)
	// folder is the folder the collection's files are in, as it is held.
	folder *Folder

	// locks is the collection's lock file, opened once and kept for as long
	// as the collection is, and mine the work this Shelf is doing: the
	// numbers it holds locks at.
	working sync.Mutex
	locks   *os.File
	// lockable says that the lock file is open for writing, which holding
	// a lock takes.
	lockable bool
	mine     map[int64]bool
}

// Source is what the index says of one Source.
type Source struct {
	// ID, Kind and Title are the Source's, as index was given them.
	ID, Kind, Title string
	// Hash is the content hash of what is indexed for the Source, and empty
	// while nothing is.
	Hash string
	// Address is the address a web page was fetched from.
	Address string
	// Failed is why the last indexing of the Source failed, and empty when
	// it did not. Asked is then what that indexing was asked with: the hash
	// it was handed for a file, or a web page's address.
	Failed, Asked string
}

// Passage is a stretch of a Source's text in the index. First and Last are
// the pages it is on, and 0 for a Source without pages.
type Passage struct {
	ID, Source  string
	Text        string
	First, Last int
}

// Found is a Passage a search found, with its score: higher is better.
type Found struct {
	Passage
	Score float64
}

// PassageID is the id of the Passage at place, counted from 0, of the
// content with that hash of a Source, which holds text.
//
// It is made of all four, so that it names that text and no other for good:
// the same content, indexed again on any computer, gives the same ids, and
// content that changed, or was cut into other Passages, gives ids that were
// never used. The place sets two Passages of the same words apart.
func PassageID(source, hash string, place int, text string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + hash + "\x00" + strconv.Itoa(place) + "\x00" + text))
	return hex.EncodeToString(sum[:8])
}

// empty reports whether the collection's file holds nothing yet.
func (c *Collection) empty(ctx context.Context) (bool, error) {
	var format int
	if err := c.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&format); err != nil {
		return false, c.wrap(err)
	}
	return format == 0, nil
}

// initialise makes the tables of a new collection. Two copies of Shelf may
// both find the file empty: the second waits for the first, and finds the
// tables made.
func (c *Collection) initialise(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	var format int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&format); err != nil {
		return c.wrap(err)
	}
	if format != 0 {
		return nil
	}
	for i, statement := range schema {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return c.wrap(err)
		}
		if i == 0 {
			c.crash(CrashSchemaHalf)
		}
	}
	// The header says what the file is last: a file without it is not yet
	// a collection, whatever it holds.
	for _, pragma := range []string{
		fmt.Sprintf("PRAGMA application_id = %d", applicationID),
		fmt.Sprintf("PRAGMA user_version = %d", Format),
	} {
		if _, err := tx.ExecContext(ctx, pragma); err != nil {
			return c.wrap(err)
		}
	}
	return c.wrap(tx.Commit())
}

// wrap words an error of the database for the learner.
func (c *Collection) wrap(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var stranger *StrangerError
	var newer *NewerError
	if errors.As(err, &stranger) || errors.As(err, &newer) || strings.HasPrefix(err.Error(), "the index of collection ") {
		return err
	}
	if unwritable(err) {
		// Reading goes on where Shelf may not write (connect.go). What
		// would write says so, and what to do.
		return fmt.Errorf("the index of collection %s cannot be written: Shelf may not write in its folder here. Allow the folder "+
			"where study is run, or index from a shell of your own", c.name)
	}
	return fmt.Errorf("the index of collection %s: %w", c.name, err)
}

// Count returns how many Sources the index holds anything of: content, or
// that their last indexing failed.
func (c *Collection) Count(ctx context.Context) (int, error) {
	var n int
	err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM sources`).Scan(&n)
	return n, c.wrap(err)
}

// Sources returns what the index says of the collection's Sources, by id,
// and no more of them than a status may list.
func (c *Collection) Sources(ctx context.Context) ([]Source, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id, kind, title, coalesce(hash, ''), coalesce(address, ''), coalesce(failed, ''), coalesce(asked, '')
		FROM sources ORDER BY id LIMIT ?`, plugincontract.MaxSources)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Kind, &s.Title, &s.Hash, &s.Address, &s.Failed, &s.Asked); err != nil {
			return nil, c.wrap(err)
		}
		out = append(out, s)
	}
	return out, c.wrap(rows.Err())
}

// Source returns what the index says of one Source, and nil when it says
// nothing.
func (c *Collection) Source(ctx context.Context, id string) (*Source, error) {
	s := Source{ID: id}
	err := c.db.QueryRowContext(ctx, `SELECT kind, title, coalesce(hash, ''), coalesce(address, ''), coalesce(failed, ''), coalesce(asked, '')
		FROM sources WHERE id = ?`, id).Scan(&s.Kind, &s.Title, &s.Hash, &s.Address, &s.Failed, &s.Asked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, c.wrap(err)
	}
	return &s, nil
}

// Replace puts new content of a Source in the place of what the index held
// for it: source.Hash is the content's hash, and passages its text, in
// order, which Replace gives their ids. It is one transaction. A search
// finds the earlier content until it commits and the new content from then
// on, and never part of either, whatever stops Shelf on the way.
func (c *Collection) Replace(ctx context.Context, source Source, passages []Passage) error {
	return c.replace(ctx, source, passages, nil)
}

// replace is Replace, for the work that read the content when there is
// one: the work is over in the transaction that puts its content there.
func (c *Collection) replace(ctx context.Context, source Source, passages []Passage, work *Work) error {
	if source.Hash == "" || len(passages) == 0 {
		return errors.New("a Source is indexed with a hash and at least one Passage")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM passages_text WHERE rowid IN (SELECT key FROM passages WHERE source = ?)`, source.ID); err != nil {
		return c.wrap(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM passages WHERE source = ?`, source.ID); err != nil {
		return c.wrap(err)
	}
	c.crash(CrashEarlierGone)
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources (id, kind, title, hash, address) VALUES (?, ?, ?, ?, nullif(?, ''))
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, hash = excluded.hash,
			address = excluded.address, failed = NULL, asked = NULL`, source.ID, source.Kind, source.Title, source.Hash, source.Address); err != nil {
		return c.wrap(err)
	}
	if work != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, work.id); err != nil {
			return c.wrap(err)
		}
	}
	passage, err := tx.PrepareContext(ctx, `INSERT INTO passages (id, source, place, first_page, last_page) VALUES (?, ?, ?, nullif(?, 0), nullif(?, 0))`)
	if err != nil {
		return c.wrap(err)
	}
	defer passage.Close()
	text, err := tx.PrepareContext(ctx, `INSERT INTO passages_text (rowid, text) VALUES (?, ?)`)
	if err != nil {
		return c.wrap(err)
	}
	defer text.Close()
	for place, p := range passages {
		id := PassageID(source.ID, source.Hash, place, p.Text)
		res, err := passage.ExecContext(ctx, id, source.ID, place, p.First, p.Last)
		if err != nil {
			return c.wrap(err)
		}
		key, err := res.LastInsertId()
		if err != nil {
			return c.wrap(err)
		}
		if _, err := text.ExecContext(ctx, key, p.Text); err != nil {
			return c.wrap(err)
		}
		if place == len(passages)/2 {
			c.crash(CrashHalfWritten)
		}
	}
	c.crash(CrashBeforeCommit)
	if err := tx.Commit(); err != nil {
		return c.wrap(err)
	}
	c.crash(CrashCommitted)
	return nil
}

// Fail records that the indexing of a Source failed: why, and what it was
// asked with, the hash a file was handed with or a web page's address. What
// was indexed for the Source before stays, with its hash. The failure is
// kept in the collection, since the Shelf that is asked about it later is
// another than the one it happened to.
func (c *Collection) Fail(ctx context.Context, source Source, asked, reason string) error {
	return c.fail(ctx, source, asked, reason, nil)
}

func (c *Collection) fail(ctx context.Context, source Source, asked, reason string, work *Work) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources (id, kind, title, failed, asked) VALUES (?, ?, ?, ?, nullif(?, ''))
		ON CONFLICT (id) DO UPDATE SET failed = excluded.failed, asked = excluded.asked`, source.ID, source.Kind, source.Title, reason, asked); err != nil {
		return c.wrap(err)
	}
	if work != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, work.id); err != nil {
			return c.wrap(err)
		}
	}
	return c.wrap(tx.Commit())
}

// Recover forgets that the last indexing of a Source failed: what is
// indexed for it was asked for again.
func (c *Collection) Recover(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, `UPDATE sources SET failed = NULL, asked = NULL WHERE id = ? AND failed IS NOT NULL`, id)
	return c.wrap(err)
}

// mostTerms is how many words of a query are searched for. A query can be
// four thousand bytes, and the words after the first few dozen change what
// is found very little, for a search that looks at each of them.
const mostTerms = 64

// terms are the words of a query, as the full-text index is asked for them:
// each quoted, so that nothing in a query is taken for the index's own
// syntax, and any of them enough for a Passage to be found. The Passages
// that hold more of them, and rarer ones, come first.
//
// A query is cut into words where the index cuts a Passage into them, or a
// word the index holds could not be asked for. Its tokenizer keeps letters,
// numbers of every kind, the ₂ of H₂O and the ² of m² among them, and
// private-use characters together, with the marks that follow a letter. And
// the query's accents are written as Passages' are (NFC): typed as a letter
// and a mark, "é" would otherwise be cut in two.
func terms(query string) string {
	var quoted []string
	seen := map[string]bool{}
	inWord := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || unicode.Is(unicode.Co, r)
	}
	for _, word := range strings.FieldsFunc(norm.NFC.String(query), func(r rune) bool { return !inWord(r) }) {
		word = strings.ToLower(word)
		if seen[word] || len(quoted) == mostTerms {
			continue
		}
		seen[word] = true
		quoted = append(quoted, `"`+word+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// Search returns the Passages that answer a query, the best first, and no
// more than limit. sources, when not nil, are the ids of the Sources to
// search.
func (c *Collection) Search(ctx context.Context, query string, limit int, sources []string) ([]Found, error) {
	match := terms(query)
	if match == "" {
		return nil, nil // a query of punctuation alone names nothing to find
	}
	statement := `SELECT p.id, p.source, passages_text.text, coalesce(p.first_page, 0), coalesce(p.last_page, 0), -bm25(passages_text)
		FROM passages_text JOIN passages p ON p.key = passages_text.rowid
		WHERE passages_text MATCH ?`
	args := []any{match}
	if sources != nil {
		list, err := json.Marshal(sources)
		if err != nil {
			return nil, err
		}
		statement += ` AND p.source IN (SELECT value FROM json_each(?))`
		args = append(args, string(list))
	}
	// Passages that score the same come in the order of the text, so that
	// the same search gives the same answer.
	statement += ` ORDER BY bm25(passages_text), p.source, p.place LIMIT ?`
	rows, err := c.db.QueryContext(ctx, statement, append(args, limit)...)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer rows.Close()
	var out []Found
	for rows.Next() {
		var f Found
		if err := rows.Scan(&f.ID, &f.Source, &f.Text, &f.First, &f.Last, &f.Score); err != nil {
			return nil, c.wrap(err)
		}
		out = append(out, f)
	}
	return out, c.wrap(rows.Err())
}

// Passage returns the Passage with that id, and nil when the collection has
// none.
func (c *Collection) Passage(ctx context.Context, id string) (*Passage, error) {
	p := Passage{ID: id}
	err := c.db.QueryRowContext(ctx, `SELECT p.source, passages_text.text, coalesce(p.first_page, 0), coalesce(p.last_page, 0)
		FROM passages p JOIN passages_text ON passages_text.rowid = p.key WHERE p.id = ?`, id).Scan(&p.Source, &p.Text, &p.First, &p.Last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, c.wrap(err)
	}
	return &p, nil
}
