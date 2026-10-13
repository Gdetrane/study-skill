package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"sort"
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
	// CrashVectorsHalfWritten: the new Passages are written, the Source is
	// said to have vectors, and half of them are there. It is met only by
	// content that comes with vectors, in either transaction that writes
	// them.
	CrashVectorsHalfWritten = "half of the new vectors are written"
	// CrashBeforeCommit: all of them are, and the transaction is still open.
	CrashBeforeCommit = "the new Passages are written, and not committed"
	// CrashCommitted: the transaction has committed.
	CrashCommitted = "the new Passages are committed"
	// CrashEmbeddedBeforeCommit: new vectors of content that is indexed
	// already are written in the place of its earlier ones, and the
	// transaction is still open.
	CrashEmbeddedBeforeCommit = "the vectors of indexed content are replaced, and not committed"
	// CrashEmbeddedCommitted: that transaction has committed.
	CrashEmbeddedCommitted = "the vectors of indexed content are committed"
)

// schema is what a collection's file held in Format 1, and still holds.
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

// vectorSchema is what Format 2 added: the vectors of a Source's Passages,
// for a search by what a question means.
//
// A Source has a row in embeddings when every one of its Passages has a
// vector, and never otherwise: the row and the vectors are written in the
// transaction that writes the Passages, or in one that gives vectors to
// Passages that are there. The row says what the vectors were made with,
// since a question's vector is compared only with vectors made the same
// way.
//
// The vectors are kept in runs: a row of vectors holds those of up to
// runLength Passages that follow each other, from the Passage at the place
// first, each vector as its numbers in 32 bits, the low byte first. A search
// reads every vector of a collection, and Shelf is started anew for every
// search: a few large rows are read several times faster than one row for
// each Passage.
//
// The format was raised for them, and not only added to: a Shelf that knows
// nothing of vectors would replace a Source's Passages and leave its vectors
// standing, and the next search would hand out Passages by the vectors of
// others. It refuses a file in Format 2 instead.
var vectorSchema = []string{
	`CREATE TABLE embeddings (
		source     TEXT PRIMARY KEY REFERENCES sources(id),
		model      TEXT NOT NULL,
		document   TEXT NOT NULL,
		dimensions INTEGER NOT NULL
	) STRICT, WITHOUT ROWID`,
	`CREATE TABLE vectors (
		id      INTEGER PRIMARY KEY,
		source  TEXT NOT NULL REFERENCES embeddings(source),
		first   INTEGER NOT NULL,
		vectors BLOB NOT NULL,
		UNIQUE (source, first)
	) STRICT`,
}

// runLength is how many Passages' vectors one row of vectors holds, at
// most: two megabytes of them, for vectors of 512 numbers.
var runLength = 1024

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
	// Made is what the vectors of the Source's Passages were made with, and
	// the zero Made while they have none.
	Made Made
}

// Made says what vectors were made with: the model, by the name the
// endpoint knows it under, how a stretch of a document was marked for it,
// and how many numbers a vector has. A question's vector is compared only
// with vectors made the same way.
type Made struct {
	Model, Document string
	Dimensions      int
}

// Any reports whether there are vectors at all.
func (m Made) Any() bool { return m.Dimensions > 0 }

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
	for i, statement := range append(append([]string{}, schema...), vectorSchema...) {
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
	query, err := c.sourcesQuery(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, query+` ORDER BY s.id LIMIT ?`, plugincontract.MaxSources)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Kind, &s.Title, &s.Hash, &s.Address, &s.Failed, &s.Asked, &s.Made.Model, &s.Made.Document, &s.Made.Dimensions); err != nil {
			return nil, c.wrap(err)
		}
		out = append(out, s)
	}
	return out, c.wrap(rows.Err())
}

// sourcesQuery is the query that reads what the index says of Sources, s,
// with what their vectors were made with: which a file in Format 1 says
// nothing of.
func (c *Collection) sourcesQuery(ctx context.Context) (string, error) {
	const columns = `SELECT s.id, s.kind, s.title, coalesce(s.hash, ''), coalesce(s.address, ''), coalesce(s.failed, ''), coalesce(s.asked, '')`
	vectors, err := c.hasVectors(ctx)
	switch {
	case err != nil:
		return "", err
	case !vectors:
		return columns + `, '', '', 0 FROM sources s`, nil
	}
	return columns + `, coalesce(e.model, ''), coalesce(e.document, ''), coalesce(e.dimensions, 0)
		FROM sources s LEFT JOIN embeddings e ON e.source = s.id`, nil
}

// hasVectors reports whether the collection's file is in a format that
// holds vectors. A file in Format 1 is read as it is, where Shelf may not
// write among them, and becomes one in Format 2 with the next content that
// is written into it (upgrade).
func (c *Collection) hasVectors(ctx context.Context) (bool, error) {
	var format int
	if err := c.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&format); err != nil {
		return false, c.wrap(err)
	}
	return format >= 2, nil
}

// upgrade gives a file in Format 1 what Format 2 added, inside the
// transaction that is about to write content: the file is in the one format
// or in the other, whatever stops Shelf on the way.
func (c *Collection) upgrade(ctx context.Context, tx *sql.Tx) error {
	var format int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&format); err != nil {
		return c.wrap(err)
	}
	switch {
	case format > Format:
		// A newer Shelf brought the file to a format of its own since this
		// one opened it: what this one would write is not what that format
		// holds.
		return &NewerError{Collection: c.name, Format: int64(format)}
	case format == Format:
		return nil
	}
	for _, statement := range append(append([]string{}, vectorSchema...), fmt.Sprintf("PRAGMA user_version = %d", Format)) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return c.wrap(err)
		}
	}
	return nil
}

// Source returns what the index says of one Source, and nil when it says
// nothing.
func (c *Collection) Source(ctx context.Context, id string) (*Source, error) {
	query, err := c.sourcesQuery(ctx)
	if err != nil {
		return nil, err
	}
	var s Source
	err = c.db.QueryRowContext(ctx, query+` WHERE s.id = ?`, id).Scan(&s.ID, &s.Kind, &s.Title, &s.Hash, &s.Address, &s.Failed, &s.Asked,
		&s.Made.Model, &s.Made.Document, &s.Made.Dimensions)
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
// order, which Replace gives their ids. vectors are the Passages' vectors,
// one for each and in their order, made as source.Made says, or nil for
// content without vectors. It is one transaction. A search finds the
// earlier content until it commits and the new content from then on, with
// its vectors, and never part of either, whatever stops Shelf on the way.
func (c *Collection) Replace(ctx context.Context, source Source, passages []Passage, vectors [][]float32) error {
	return c.replace(ctx, source, passages, vectors, nil)
}

// checkVectors checks that vectors are one for each of count Passages, made
// as made says.
func checkVectors(made Made, vectors [][]float32, count int) error {
	if !made.Any() {
		if made != (Made{}) || vectors != nil {
			return errors.New("vectors come with what they were made with, and a vector has at least one number")
		}
		return nil
	}
	if made.Model == "" || len(vectors) != count {
		return fmt.Errorf("a Source's vectors name their model and are one for each Passage: %d vectors for %d Passages", len(vectors), count)
	}
	for _, vector := range vectors {
		if len(vector) != made.Dimensions {
			return fmt.Errorf("a vector has %d numbers, and the Source's are said to have %d", len(vector), made.Dimensions)
		}
	}
	return nil
}

// replace is Replace, for the work that read the content when there is
// one: the work is over in the transaction that puts its content there.
func (c *Collection) replace(ctx context.Context, source Source, passages []Passage, vectors [][]float32, work *Work) error {
	if source.Hash == "" || len(passages) == 0 {
		return errors.New("a Source is indexed with a hash and at least one Passage")
	}
	if err := checkVectors(source.Made, vectors, len(passages)); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := c.upgrade(ctx, tx); err != nil {
		return err
	}
	if err := c.dropVectors(ctx, tx, source.ID); err != nil {
		return err
	}
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
	if err := c.writeVectors(ctx, tx, source.ID, source.Made, vectors); err != nil {
		return err
	}
	c.crash(CrashBeforeCommit)
	if err := tx.Commit(); err != nil {
		return c.wrap(err)
	}
	c.crash(CrashCommitted)
	return nil
}

// dropVectors takes a Source's vectors out, with what says that it has any.
func (c *Collection) dropVectors(ctx context.Context, tx *sql.Tx, source string) error {
	for _, statement := range []string{`DELETE FROM vectors WHERE source = ?`, `DELETE FROM embeddings WHERE source = ?`} {
		if _, err := tx.ExecContext(ctx, statement, source); err != nil {
			return c.wrap(err)
		}
	}
	return nil
}

// writeVectors writes the vectors of a Source's Passages, in runs, and what
// they were made with. It writes nothing for content without vectors.
func (c *Collection) writeVectors(ctx context.Context, tx *sql.Tx, source string, made Made, vectors [][]float32) error {
	if !made.Any() {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings (source, model, document, dimensions) VALUES (?, ?, ?, ?)`,
		source, made.Model, made.Document, made.Dimensions); err != nil {
		return c.wrap(err)
	}
	runs := (len(vectors) + runLength - 1) / runLength
	for run := range runs {
		if run == runs/2 {
			c.crash(CrashVectorsHalfWritten)
		}
		first := run * runLength
		of := vectors[first:min(first+runLength, len(vectors))]
		packed := make([]byte, 0, 4*made.Dimensions*len(of))
		for _, vector := range of {
			for _, x := range vector {
				packed = binary.LittleEndian.AppendUint32(packed, math.Float32bits(x))
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO vectors (source, first, vectors) VALUES (?, ?, ?)`, source, first, packed); err != nil {
			return c.wrap(err)
		}
	}
	return nil
}

// ErrChanged says that a Source's content is no longer the content whose
// Passages were given vectors.
var ErrChanged = errors.New("the Source's content changed while its Passages were given vectors")

// Texts returns the text of the Passages that are indexed for a Source, in
// their order, and the hash of the content they are of: what there is to
// give vectors to. It returns no hash for a Source nothing is indexed of.
func (c *Collection) Texts(ctx context.Context, source string) (hash string, texts []string, err error) {
	rows, err := c.db.QueryContext(ctx, `SELECT coalesce(s.hash, ''), passages_text.text
		FROM passages p JOIN sources s ON s.id = p.source JOIN passages_text ON passages_text.rowid = p.key
		WHERE p.source = ? ORDER BY p.place`, source)
	if err != nil {
		return "", nil, c.wrap(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err := rows.Scan(&hash, &text); err != nil {
			return "", nil, c.wrap(err)
		}
		texts = append(texts, text)
	}
	return hash, texts, c.wrap(rows.Err())
}

// Embed gives vectors to the Passages that are indexed for a Source, in the
// place of the vectors they had: one for each Passage, in their order, made
// as made says. The Passages and their ids stay as they are. hash is the
// hash of the content the vectors are of, and texts the Passages' texts they
// were made of, as Texts gave them: when the Source's content is another by
// now, or its Passages are, though of the same content and as many, nothing
// is written and the error is ErrChanged. A vector is so never kept for a
// text it was not made of. It is one transaction, so a Source's vectors are
// all of one making, whatever stops Shelf on the way.
func (c *Collection) Embed(ctx context.Context, source, hash string, texts []string, made Made, vectors [][]float32) error {
	return c.embed(ctx, source, hash, texts, made, vectors, nil)
}

func (c *Collection) embed(ctx context.Context, source, hash string, texts []string, made Made, vectors [][]float32, work *Work) error {
	if len(texts) != len(vectors) {
		return fmt.Errorf("%d vectors for %d Passages", len(vectors), len(texts))
	}
	if !made.Any() {
		return errors.New("vectors come with what they were made with")
	}
	if err := checkVectors(made, vectors, len(vectors)); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrap(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := c.upgrade(ctx, tx); err != nil {
		return err
	}
	var has string
	err = tx.QueryRowContext(ctx, `SELECT coalesce(hash, '') FROM sources WHERE id = ?`, source).Scan(&has)
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && has != hash):
		return ErrChanged
	case err != nil:
		return c.wrap(err)
	}
	// The Passages are the ones the vectors were made of, each in its
	// place: the same content cut otherwise since is other Passages.
	rows, err := tx.QueryContext(ctx, `SELECT passages_text.text FROM passages p JOIN passages_text ON passages_text.rowid = p.key
		WHERE p.source = ? ORDER BY p.place`, source)
	if err != nil {
		return c.wrap(err)
	}
	at, same := 0, true
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return c.wrap(err)
		}
		same = same && at < len(texts) && texts[at] == text
		at++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return c.wrap(err)
	}
	rows.Close()
	if !same || at != len(texts) {
		return ErrChanged
	}
	if err := c.dropVectors(ctx, tx, source); err != nil {
		return err
	}
	if err := c.writeVectors(ctx, tx, source, made, vectors); err != nil {
		return err
	}
	// A failure that was recorded was of an earlier try to do this.
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET failed = NULL, asked = NULL WHERE id = ? AND failed IS NOT NULL`, source); err != nil {
		return c.wrap(err)
	}
	if work != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM work WHERE id = ?`, work.id); err != nil {
			return c.wrap(err)
		}
	}
	c.crash(CrashEmbeddedBeforeCommit)
	if err := tx.Commit(); err != nil {
		return c.wrap(err)
	}
	c.crash(CrashEmbeddedCommitted)
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

// Nearest returns the Passages whose vectors are nearest a question's, the
// nearest first, and no more than limit. Only vectors made as made says are
// compared, with as many numbers as the question's has: vectors made
// otherwise say nothing about it. sources, when not nil, are the ids of the
// Sources to search. A Passage's score is the cosine of the angle between
// the two vectors, from -1 to 1.
//
// Every vector that counts is read and compared: no index of them is kept.
// A collection is a Topic's books, a few thousand Passages, and the
// comparing is a fraction of the reading (docs/shelf.md has the numbers).
func (c *Collection) Nearest(ctx context.Context, made Made, question []float32, limit int, sources []string) ([]Found, error) {
	if vectors, err := c.hasVectors(ctx); err != nil || !vectors || len(question) == 0 || limit < 1 {
		return nil, err
	}
	statement := `SELECT v.source, v.first, v.vectors FROM vectors v JOIN embeddings e ON e.source = v.source
		WHERE e.model = ? AND e.document = ? AND e.dimensions = ?`
	args := []any{made.Model, made.Document, len(question)}
	if sources != nil {
		list, err := json.Marshal(sources)
		if err != nil {
			return nil, err
		}
		statement += ` AND v.source IN (SELECT value FROM json_each(?))`
		args = append(args, string(list))
	}
	rows, err := c.db.QueryContext(ctx, statement+` ORDER BY v.source, v.first`, args...)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer rows.Close()
	type scored struct {
		source int
		place  int
		score  float32
	}
	var (
		names []string
		all   []scored
		width = 4 * len(question)
	)
	for rows.Next() {
		var source string
		var first int
		var packed sql.RawBytes
		if err := rows.Scan(&source, &first, &packed); err != nil {
			return nil, c.wrap(err)
		}
		if len(packed)%width != 0 {
			return nil, fmt.Errorf("the index of collection %s holds vectors of Source %s that are cut short. Index the Source again", c.name, source)
		}
		if len(names) == 0 || names[len(names)-1] != source {
			names = append(names, source)
		}
		for i := 0; i < len(packed)/width; i++ {
			all = append(all, scored{source: len(names) - 1, place: first + i, score: dot(packed[i*width:(i+1)*width], question)})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrap(err)
	}
	_ = rows.Close()
	// Passages that score the same come in the order of the text, so that
	// the same search gives the same answer.
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.source != b.source {
			return a.source < b.source
		}
		return a.place < b.place
	})
	passage, err := c.db.PrepareContext(ctx, `SELECT p.id, passages_text.text, coalesce(p.first_page, 0), coalesce(p.last_page, 0)
		FROM passages p JOIN passages_text ON passages_text.rowid = p.key WHERE p.source = ? AND p.place = ?`)
	if err != nil {
		return nil, c.wrap(err)
	}
	defer passage.Close()
	var out []Found
	for _, s := range all {
		if len(out) == limit {
			break
		}
		f := Found{Passage: Passage{Source: names[s.source]}, Score: float64(s.score)}
		err := passage.QueryRowContext(ctx, f.Source, s.place).Scan(&f.ID, &f.Text, &f.First, &f.Last)
		if errors.Is(err, sql.ErrNoRows) {
			// Another copy of Shelf replaced the Source's content just now.
			continue
		}
		if err != nil {
			return nil, c.wrap(err)
		}
		out = append(out, f)
	}
	return out, nil
}

// dot is the sum of the products of a packed vector's numbers and a
// question's: for two vectors of length 1, the cosine of the angle between
// them. It is summed in four parts, which a processor does at once.
func dot(packed []byte, question []float32) float32 {
	var a, b, c, d float32
	i := 0
	for ; i+4 <= len(question); i += 4 {
		four := packed[4*i : 4*i+16 : 4*i+16]
		a += math.Float32frombits(binary.LittleEndian.Uint32(four[0:4])) * question[i]
		b += math.Float32frombits(binary.LittleEndian.Uint32(four[4:8])) * question[i+1]
		c += math.Float32frombits(binary.LittleEndian.Uint32(four[8:12])) * question[i+2]
		d += math.Float32frombits(binary.LittleEndian.Uint32(four[12:16])) * question[i+3]
	}
	for ; i < len(question); i++ {
		a += math.Float32frombits(binary.LittleEndian.Uint32(packed[4*i:4*i+4])) * question[i]
	}
	return a + b + c + d
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
