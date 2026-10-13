package index_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
)

var ctx = context.Background()

// tree describes everything in a folder: each name with what it is and, for
// a file, what it holds. Two descriptions are equal when nothing was added,
// removed or changed.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			leads, _ := os.Readlink(path)
			lines = append(lines, rel+" -> "+leads)
		case entry.IsDir():
			lines = append(lines, rel+"/")
		case !entry.Type().IsRegular():
			lines = append(lines, rel+" "+entry.Type().String())
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			lines = append(lines, fmt.Sprintf("%s %d bytes %s", rel, len(data), hex.EncodeToString(sum[:6])))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("looking at %s: %v", dir, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// folder opens a new, empty folder for an index.
func folder(t *testing.T) (*index.Folder, string) {
	t.Helper()
	dir := t.TempDir()
	return reopen(t, dir), dir
}

// reopen opens the folder at dir, as a Shelf that is started does.
func reopen(t *testing.T, dir string) *index.Folder {
	t.Helper()
	f, err := index.Open(dir, index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// collection returns a collection, made when it is not there.
func collection(t *testing.T, f *index.Folder, name string) *index.Collection {
	t.Helper()
	c, err := f.Collection(ctx, name, true)
	if err != nil || c == nil {
		t.Fatalf("Collection(%s) = %v, %v", name, c, err)
	}
	return c
}

func hash(char string) string { return "sha256:" + strings.Repeat(char, 64) }

// book is a Source with some content: a hash, and Passages on pages.
func book(id, char string, texts ...string) (index.Source, []index.Passage) {
	var passages []index.Passage
	for i, text := range texts {
		passages = append(passages, index.Passage{Text: text, First: i + 1, Last: i + 1})
	}
	return index.Source{ID: id, Kind: plugincontract.SourceFile, Title: "The " + id + " book", Hash: hash(char)}, passages
}

func replace(t *testing.T, c *index.Collection, source index.Source, passages []index.Passage) {
	t.Helper()
	if err := c.Replace(ctx, source, passages, nil); err != nil {
		t.Fatalf("Replace(%s): %v", source.ID, err)
	}
}

func search(t *testing.T, c *index.Collection, query string, limit int, sources ...string) []index.Found {
	t.Helper()
	found, err := c.Search(ctx, query, limit, sources)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	for i, f := range found {
		if i > 0 && f.Score > found[i-1].Score {
			t.Fatalf("Search(%q): Passage %d scores %v, more than the one before it, %v", query, i, f.Score, found[i-1].Score)
		}
	}
	return found
}

func texts(found []index.Found) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.Text)
	}
	return out
}

func TestWhatIsIndexedIsFound(t *testing.T) {
	f, _ := folder(t)
	c := collection(t, f, "linear-algebra")
	source, passages := book("strang.k3f9a2", "a",
		"Elimination produces an upper triangular system.",
		"The pivots are on the diagonal of the triangle after elimination. Elimination is the heart of it.",
		"Back substitution solves the triangular system from the bottom up.")
	replace(t, c, source, passages)

	sources, err := c.Sources(ctx)
	if err != nil || len(sources) != 1 || sources[0] != source {
		t.Fatalf("Sources = %+v, %v; want %+v", sources, err, source)
	}
	found := search(t, c, "Elimination: pivots?", 10)
	if len(found) != 2 {
		t.Fatalf("a search for elimination and pivots found %q, want the two Passages that speak of them", texts(found))
	}
	// The one that says both, and says elimination twice, comes first.
	if found[0].Text != passages[1].Text || found[0].Score <= found[1].Score || found[0].First != 2 || found[0].Last != 2 || found[0].Source != source.ID {
		t.Errorf("the best Passage = %+v (the next scores %v)", found[0], found[1].Score)
	}
	// passage gives the same Passage again, and nothing for an id there is
	// none of.
	again, err := c.Passage(ctx, found[0].ID)
	if err != nil || again == nil || *again != found[0].Passage {
		t.Errorf("Passage(%s) = %+v, %v; want %+v", found[0].ID, again, err, found[0].Passage)
	}
	if none, err := c.Passage(ctx, "0000000000000000"); none != nil || err != nil {
		t.Errorf("Passage of an id there is none of = %+v, %v", none, err)
	}
}

func TestASearchKeepsToItsLimitAndItsSources(t *testing.T) {
	f, _ := folder(t)
	c := collection(t, f, "notes")
	for _, id := range []string{"one.a1", "two.b2"} {
		var pages []string
		for n := range 6 {
			pages = append(pages, fmt.Sprintf("Page %d of %s mentions the marker and number %d.", n+1, id, n))
		}
		source, passages := book(id, string(id[0]), pages...)
		source.Hash = hash(map[string]string{"one.a1": "1", "two.b2": "2"}[id])
		replace(t, c, source, passages)
	}
	if all := search(t, c, "marker", 50); len(all) != 12 {
		t.Fatalf("a search of the whole collection found %d Passages, want 12", len(all))
	}
	for _, limit := range []int{1, 5, 11} {
		if got := search(t, c, "marker", limit); len(got) != limit {
			t.Errorf("a search with a limit of %d gave %d Passages", limit, len(got))
		}
	}
	only := search(t, c, "marker", 50, "two.b2")
	if len(only) != 6 {
		t.Errorf("a search of one Source found %d Passages, want its 6", len(only))
	}
	for _, p := range only {
		if p.Source != "two.b2" {
			t.Errorf("a search of two.b2 found a Passage of %s", p.Source)
		}
	}
	if none := search(t, c, "marker", 50, "no-such-source.x9"); len(none) != 0 {
		t.Errorf("a search of a Source the collection does not have found %q", texts(none))
	}
	// Passages that score the same come in the order of the text, every
	// time.
	first, second := search(t, c, "marker", 50), search(t, c, "marker", 50)
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Error("the same search gave another answer the second time")
	}
}

// manyWords are more words than a search looks for, none of them in any
// Passage.
var manyWords = func() string {
	var words []string
	for n := range 100 {
		words = append(words, fmt.Sprintf("nonesuch%d", n))
	}
	return strings.Join(words, " ")
}()

// A query is words to find, whatever it looks like: nothing in it is taken
// for the full-text index's own syntax.
func TestAQueryIsWordsAndNothingElse(t *testing.T) {
	f, _ := folder(t)
	c := collection(t, f, "queries")
	source, passages := book("book.a1", "a",
		"The quick brown fox jumps over the lazy dog near the old stone bridge.",
		"Café Möbius serves naïve résumés. Eliminating a variable leaves a triangle.",
		"func (c *Collection) Search(ctx context.Context) error { return nil }")
	replace(t, c, source, passages)
	for query, want := range map[string]int{
		`"quick`:                    1, // an open quotation
		`quick" OR "`:               1,
		`fox AND NOT dog`:           1, // AND and NOT are words nobody wrote
		`NEAR(fox dog, 2)`:          1,
		`text:fox`:                  1, // "text" is the column, and a word nobody wrote
		`fox*`:                      1,
		`^fox`:                      1,
		`-fox`:                      1,
		`(fox`:                      1,
		`fox) OR (dog`:              1,
		`{fox dog}`:                 1,
		`fox + dog`:                 1,
		`QUICK Brown FOX`:           1, // without their case
		`cafe mobius naive resume`:  1, // and without their accents
		`elimination`:               1, // by their stem
		`c.Search(ctx)`:             1,
		`Collection`:                1,
		`quick triangle Search`:     3, // any of its words is enough
		`zebra`:                     0,
		`?!...`:                     0, // no word at all
		`é`:                         0,
		strings.Repeat("fox ", 900): 1,
		manyWords + " fox":          0, // more words than are searched for
	} {
		if got := search(t, c, query, 50); len(got) != want {
			t.Errorf("Search(%.40q) found %d Passages, want %d: %q", query, len(got), want, texts(got))
		}
	}
}

// A query is cut into words where the index cuts a Passage into them: a word
// the index holds can be asked for, however it is typed.
func TestAQueryFindsTheWordThePassageHolds(t *testing.T) {
	f, _ := folder(t)
	c := collection(t, f, "words")
	source, passages := book("book.a1", "a",
		"Le mathématicien Évariste Galois est mort à vingt ans.",
		"Water is H₂O, and the plants take up CO₂ from the air.",
		"The field covers 40 m² and the function is x² + 1.",
		"Half of it, ½, is left over.")
	replace(t, c, source, passages)
	for query, want := range map[string]int{
		"mathématicien":       1, // as one character
		"mathe\u0301maticien": 1, // as a letter and a mark
		"mathematicien":       1,
		"E\u0301variste":      1,
		"H₂O":                 1,
		"CO₂":                 1,
		"m²":                  1,
		"x²":                  1,
		"½":                   1,
		"H2O":                 0, // another word than the page holds
	} {
		if got := search(t, c, query, 50); len(got) != want {
			t.Errorf("Search(%q) found %d Passages, want %d: %q", query, len(got), want, texts(got))
		}
	}
}

// A Passage's id names its text for good: the same on every computer and
// after every restart, and never the id of another text.
func TestAPassagesIDNamesOneText(t *testing.T) {
	const want = "422b54da43dd5e2c"
	if got := index.PassageID("strang.k3f9a2", hash("a"), 0, "Elimination produces an upper triangular system."); got != want {
		t.Errorf("PassageID = %s, want %s: an id that changes from one version of Shelf to the next names nothing a learner's "+
			"Evidence still quotes", got, want)
	}
	seen := map[string]string{}
	for _, tc := range []struct {
		source, hash string
		place        int
		text         string
	}{
		{"strang.k3f9a2", hash("a"), 0, "Elimination produces an upper triangular system."},
		{"strang.k3f9a2", hash("a"), 1, "Elimination produces an upper triangular system."}, // said twice in one Source
		{"strang.k3f9a2", hash("b"), 0, "Elimination produces an upper triangular system."}, // other content
		{"axler.p2x7q1", hash("a"), 0, "Elimination produces an upper triangular system."},  // another Source
		{"strang.k3f9a2", hash("a"), 0, "Elimination produces a lower triangular system."},  // cut another way
		{"strang.k3f9a", "2" + hash("a"), 0, "Elimination produces an upper triangular system."},
	} {
		id := index.PassageID(tc.source, tc.hash, tc.place, tc.text)
		if err := plugincontract.CheckPassageID(id); err != nil {
			t.Errorf("PassageID gave %q, which is no Passage's id: %v", id, err)
		}
		if other, taken := seen[id]; taken {
			t.Errorf("%+v has the id of %s", tc, other)
		}
		seen[id] = fmt.Sprint(tc)
	}

	f, dir := folder(t)
	source, passages := book("strang.k3f9a2", "a", "Elimination produces an upper triangular system.", "The pivots are on the diagonal.")
	replace(t, collection(t, f, "linear-algebra"), source, passages)
	before := search(t, collection(t, f, "linear-algebra"), "elimination pivots", 10)
	if len(before) != 2 || before[0].ID == before[1].ID {
		t.Fatalf("found %+v", before)
	}
	// The same content, indexed again: the same ids.
	replace(t, collection(t, f, "linear-algebra"), source, passages)
	// And after Shelf was stopped and started.
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	c := collection(t, reopen(t, dir), "linear-algebra")
	if after := search(t, c, "elimination pivots", 10); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("after the same content was indexed again and Shelf was started again, a search gives\n%+v\nand gave\n%+v", after, before)
	}
	// Other content of the Source: the old ids name nothing.
	changed, passages := book("strang.k3f9a2", "b", "Elimination produces an upper triangular system.", "A new second page.")
	replace(t, c, changed, passages)
	for _, old := range before {
		if p, err := c.Passage(ctx, old.ID); p != nil || err != nil {
			t.Errorf("the id of a Passage of content that was replaced now names %+v (%v)", p, err)
		}
	}
	if now := search(t, c, "pivots", 10); len(now) != 0 {
		t.Errorf("a search finds text of content that was replaced: %q", texts(now))
	}
	if now := search(t, c, "elimination", 10); len(now) != 1 || now[0].ID == before[0].ID {
		t.Errorf("the same words in other content have the id they had: %+v", now)
	}
}

func TestCollectionsAreFilesApart(t *testing.T) {
	f, dir := folder(t)
	here, there := collection(t, f, "here"), collection(t, f, "there")
	mine, minePages := book("book.a1", "a", "A marker called alpha stands here.")
	theirs, theirPages := book("book.a1", "b", "A marker called beta stands there.")
	replace(t, here, mine, minePages)
	replace(t, there, theirs, theirPages)
	if got := search(t, here, "alpha beta", 10); len(got) != 1 || !strings.Contains(got[0].Text, "alpha") {
		t.Errorf("a search of here found %q", texts(got))
	}
	if got := search(t, there, "alpha beta", 10); len(got) != 1 || !strings.Contains(got[0].Text, "beta") {
		t.Errorf("a search of there found %q", texts(got))
	}
	for _, name := range []string{"here.sqlite", "there.sqlite"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v; want a file only the learner reads", name, info, err)
		}
	}
	// A collection nobody indexed is no file, and asking about it makes
	// none.
	if c, err := f.Collection(ctx, "nowhere", false); c != nil || err != nil {
		t.Errorf("Collection of a name nothing was indexed under = %v, %v", c, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("asking about a collection made its file: %v", err)
	}
}

// A collection's name becomes a file's name, and is held to what the
// contract says a name is before it does.
func TestANameThatIsNoCollectionsIsRefused(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "shelf")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := reopen(t, dir)
	for _, name := range []string{"../other", "..", ".", "", "a/b", "/etc/passwd", "Linear-Algebra", "a.b", "a b", "a\x00b", "-a", "a--b",
		strings.Repeat("a", 65), "café", "a.sqlite", "notes\n"} {
		for _, create := range []bool{false, true} {
			if c, err := f.Collection(ctx, name, create); err == nil || c != nil {
				t.Errorf("Collection(%q, %v) = %v, %v; want it refused", name, create, c, err)
			}
		}
	}
	if left, _ := os.ReadDir(base); len(left) != 1 {
		t.Errorf("a refused name left %v beside Shelf's folder", left)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("a refused name left %v in Shelf's folder", left)
	}
}

// A failure is kept beside what was indexed before, and is over when the
// Source is indexed.
func TestAFailureIsKeptUntilTheSourceIsIndexed(t *testing.T) {
	f, dir := folder(t)
	c := collection(t, f, "notes")
	never := index.Source{ID: "scan.t8w3n5", Kind: plugincontract.SourceFile, Title: "Scanned notes"}
	if err := c.Fail(ctx, never, hash("5"), "the file has no text layer"); err != nil {
		t.Fatal(err)
	}
	source, passages := book("book.a1", "a", "A page about elimination.")
	replace(t, c, source, passages)
	if err := c.Fail(ctx, source, hash("b"), "the file is not there"); err != nil {
		t.Fatal(err)
	}
	page := index.Source{ID: "page.m4k8s3", Kind: plugincontract.SourceURL, Title: "A page", Address: "https://example.org/page"}
	if err := c.Fail(ctx, page, page.Address, "example.org answered 404 Not Found"); err != nil {
		t.Fatal(err)
	}
	// Shelf is stopped and started, as it is after every question study
	// has: all three are still said, each with what it was asked with.
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	c = collection(t, reopen(t, dir), "notes")
	sources, err := c.Sources(ctx)
	if err != nil || len(sources) != 3 {
		t.Fatalf("Sources = %+v, %v", sources, err)
	}
	if got := sources[0]; got.ID != "book.a1" || got.Failed != "the file is not there" || got.Asked != hash("b") || got.Hash != hash("a") {
		t.Errorf("a Source whose new content failed = %+v; want the reason, the hash it was asked with, and the hash of what is still found", got)
	}
	if got := sources[1]; got.ID != "page.m4k8s3" || got.Failed != "example.org answered 404 Not Found" || got.Asked != page.Address || got.Hash != "" {
		t.Errorf("a web page that could not be fetched = %+v; want the reason, its address and no hash", got)
	}
	if got := sources[2]; got.ID != "scan.t8w3n5" || got.Failed != "the file has no text layer" || got.Asked != hash("5") || got.Hash != "" {
		t.Errorf("a Source that never was indexed = %+v; want the reason and no hash", got)
	}
	if found := search(t, c, "elimination", 5); len(found) != 1 {
		t.Errorf("the earlier content of a Source that failed is no longer found")
	}
	// Asked for again as it is indexed, a Source is no longer failed; nor is
	// one that is being indexed again, or was indexed.
	if err := c.Recover(ctx, "scan.t8w3n5"); err != nil {
		t.Fatal(err)
	}
	work, err := c.Begin(ctx, "page.m4k8s3")
	if err != nil {
		t.Fatal(err)
	}
	work.End()
	replace(t, c, source, passages)
	for _, id := range []string{"scan.t8w3n5", "book.a1", "page.m4k8s3"} {
		if got, err := c.Source(ctx, id); err != nil || got == nil || got.Failed != "" || got.Asked != "" {
			t.Errorf("Source(%s) = %+v, %v; want it failed no longer", id, got, err)
		}
	}
	if got, err := c.Source(ctx, "nothing.x1"); got != nil || err != nil {
		t.Errorf("Source of an id there is none of = %+v, %v", got, err)
	}
}

func TestAWebPagesAddressIsKeptAndItsPassagesHaveNoPages(t *testing.T) {
	f, _ := folder(t)
	c := collection(t, f, "go")
	page := index.Source{ID: "blog.m4k8s3", Kind: plugincontract.SourceURL, Title: "Context", Hash: hash("c"), Address: "https://go.dev/blog/context"}
	replace(t, c, page, []index.Passage{{Text: "In Go servers, each incoming request is handled in its own goroutine."}})
	if got, err := c.Source(ctx, page.ID); err != nil || got == nil || *got != page {
		t.Errorf("Source = %+v, %v; want %+v", got, err, page)
	}
	if found := search(t, c, "goroutine", 5); len(found) != 1 || found[0].First != 0 || found[0].Last != 0 {
		t.Errorf("a Passage of a web page = %+v; want it without pages", found)
	}
}

// raw opens a collection's file as any SQLite database, past Shelf.
func raw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// stored makes a collection with one Source in it, closes the folder, and
// returns the folder's path.
func stored(t *testing.T, name string) string {
	t.Helper()
	f, dir := folder(t)
	source, passages := book("book.a1", "a", "A page about elimination.", "A page about pivots.")
	replace(t, collection(t, f, name), source, passages)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A file in a format newer than this Shelf reads is refused, with the
// reason, for reading as for writing, and left as it is.
func TestANewerFormatIsRefused(t *testing.T) {
	dir := stored(t, "future")
	file := filepath.Join(dir, "future.sqlite")
	db := raw(t, file)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", index.Format+1)); err != nil {
		t.Fatal(err)
	}
	// A table this Shelf does not know, as a newer format would have.
	if _, err := db.Exec("CREATE TABLE contexts (passage INTEGER PRIMARY KEY, context TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	f := reopen(t, dir)
	for _, create := range []bool{false, true} {
		c, err := f.Collection(ctx, "future", create)
		var newer *index.NewerError
		if c != nil || !errors.As(err, &newer) || newer.Format != index.Format+1 {
			t.Fatalf("Collection(create: %v) of a file in a newer format = %v, %v; want it refused as newer", create, c, err)
		}
		for _, want := range []string{fmt.Sprintf("format %d", index.Format+1), "newer Shelf", fmt.Sprintf("reads format %d", index.Format), "Update Lamplight", "future.sqlite"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Errorf("a file in a newer format was changed by being refused (%v)", err)
	}
	// The collections beside it are served as ever.
	replace(t, collection(t, reopen(t, dir), "present"), index.Source{ID: "a.b1", Kind: "file", Title: "A", Hash: hash("a")},
		[]index.Passage{{Text: "Other collections do not suffer for it."}})
}

// What is in Shelf's folder under a collection's name, and is not what
// Shelf made, is refused and left as it is.
func TestAFileThatIsNotShelfsIsRefused(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, file string){
		"another program's database": func(t *testing.T, file string) {
			db := raw(t, file)
			if _, err := db.Exec("CREATE TABLE notes (body TEXT); INSERT INTO notes VALUES ('the learner''s own')"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"a database that says it is Shelf's and has no format": func(t *testing.T, file string) {
			db := raw(t, file)
			if _, err := db.Exec("PRAGMA application_id = 1399352422; CREATE TABLE sources (id TEXT)"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"a file that is no database": func(t *testing.T, file string) {
			if err := os.WriteFile(file, []byte(strings.Repeat("These are the learner's notes, and no index.\n", 40)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a folder": func(t *testing.T, file string) {
			if err := os.Mkdir(file, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "notes.sqlite")
			prepare(t, file)
			before := tree(t, dir)
			f := reopen(t, dir)
			for _, create := range []bool{false, true} {
				if c, err := f.Collection(ctx, "notes", create); c != nil || err == nil || !strings.Contains(err.Error(), "collection notes") {
					t.Errorf("Collection(create: %v) = %v, %v; want it refused, by the collection's name", create, c, err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if after := tree(t, dir); after != before {
				t.Errorf("what was not Shelf's was changed by being refused:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// A file with nothing in it is what a Shelf that was stopped left between
// making the file and writing to it: no collection yet, and one as soon as
// something is indexed.
func TestAnEmptyFileIsACollectionNobodyIndexed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "started.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := reopen(t, dir)
	if c, err := f.Collection(ctx, "started", false); c != nil || err != nil {
		t.Fatalf("Collection of an empty file = %v, %v; want none, and no error", c, err)
	}
	source, passages := book("book.a1", "a", "A page about elimination.")
	c := collection(t, f, "started")
	replace(t, c, source, passages)
	if found := search(t, c, "elimination", 5); len(found) != 1 {
		t.Errorf("found %q", texts(found))
	}
}

// The learner removes a collection's file, to have it indexed from nothing.
// A Shelf that was running all the while sees that, and the new file once
// another copy has made it.
func TestAFileRemovedWhileShelfRunsIsGone(t *testing.T) {
	f, dir := folder(t)
	source, passages := book("book.a1", "a", "A page about elimination.")
	replace(t, collection(t, f, "notes"), source, passages)
	for _, name := range []string{"notes.sqlite", "notes.sqlite-wal", "notes.sqlite-shm"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if c, err := f.Collection(ctx, "notes", false); c != nil || err != nil {
		t.Fatalf("Collection of a file that was removed = %v, %v; want none", c, err)
	}
	// Another copy indexes other content under the name.
	other := reopen(t, dir)
	changed, passages := book("book.a1", "b", "A page about pivots.")
	replace(t, collection(t, other, "notes"), changed, passages)
	c, err := f.Collection(ctx, "notes", false)
	if err != nil || c == nil {
		t.Fatalf("Collection = %v, %v", c, err)
	}
	if found := search(t, c, "elimination pivots", 5); len(found) != 1 || !strings.Contains(found[0].Text, "pivots") {
		t.Errorf("after the file was replaced, the Shelf that was running finds %q", texts(found))
	}
}

// Two copies of Shelf on one folder, as a glance beside a study source
// index in a shell are: each finds what the other indexed, a search goes on
// while the other writes, and nothing either wrote is lost.
func TestTwoCopiesShareOneIndex(t *testing.T) {
	dir := t.TempDir()
	one, other := reopen(t, dir), reopen(t, dir)

	// Both find the collection new at once, and both make it.
	var wg sync.WaitGroup
	for _, f := range []*index.Folder{one, other, reopen(t, dir), reopen(t, dir)} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := f.Collection(ctx, "shared", true); err != nil || c == nil {
				t.Errorf("Collection, made by several copies at once = %v, %v", c, err)
			}
		}()
	}
	wg.Wait()

	source, passages := book("first.a1", "a", "The first book has a marker called alpha.")
	replace(t, collection(t, one, "shared"), source, passages)
	if found := search(t, collection(t, other, "shared"), "alpha", 5); len(found) != 1 {
		t.Fatalf("one copy does not find what the other indexed: %q", texts(found))
	}

	// One writes book after book, the other does too, and a third searches
	// all the while.
	const each = 25
	stop := make(chan struct{})
	searched := make(chan int, 1)
	go func() {
		n := 0
		c := collection(t, reopen(t, dir), "shared")
		for {
			select {
			case <-stop:
				searched <- n
				return
			default:
			}
			found, err := c.Search(ctx, "alpha", 5, nil)
			if err != nil || len(found) != 1 {
				t.Errorf("a search while two copies write = %q, %v; want the first book's Passage, as ever", texts(found), err)
				searched <- n
				return
			}
			n++
		}
	}()
	for w, f := range []*index.Folder{one, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := collection(t, f, "shared")
			for n := range each {
				source, passages := book(fmt.Sprintf("writer%d.n%d", w, n), "c", fmt.Sprintf("Writer %d wrote book %d about gamma.", w, n),
					"And a second page for it.")
				source.Hash = "sha256:" + fmt.Sprintf("%064x", w*1000+n)
				if err := c.Replace(ctx, source, passages, nil); err != nil {
					t.Errorf("writer %d, book %d: %v", w, n, err)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	if n := <-searched; n == 0 {
		t.Error("no search was made while the copies wrote")
	}
	for _, f := range []*index.Folder{one, other} {
		c := collection(t, f, "shared")
		sources, err := c.Sources(ctx)
		if err != nil || len(sources) != 1+2*each {
			t.Errorf("Sources lists %d Sources (%v), want the %d that were indexed", len(sources), err, 1+2*each)
		}
		if found := search(t, c, "gamma", 50); len(found) != 50 {
			t.Errorf("a search finds %d of what two copies wrote, want the 50 asked for", len(found))
		}
	}
	checkIntegrity(t, dir, "shared")
}

// checkIntegrity has SQLite check a collection's file, as a learner who
// doubts it would.
func checkIntegrity(t *testing.T, dir, name string) {
	t.Helper()
	db := raw(t, filepath.Join(dir, name+".sqlite"))
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		t.Errorf("the integrity check of %s says %q (%v)", name, result, err)
	}
	// The full-text index agrees with the table of Passages.
	var passages, texts int
	if err := db.QueryRow("SELECT (SELECT count(*) FROM passages), (SELECT count(*) FROM passages_text)").Scan(&passages, &texts); err != nil || passages != texts {
		t.Errorf("%s holds %d Passages and %d texts (%v)", name, passages, texts, err)
	}
	if _, err := db.Exec("INSERT INTO passages_text (passages_text) VALUES ('integrity-check')"); err != nil {
		t.Errorf("the full-text index of %s does not pass its own check: %v", name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesWhatIsNoFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"a relative path": "shelf", "a file": file, "nothing": filepath.Join(dir, "missing"), "no path": ""} {
		if f, err := index.Open(path, index.Options{}); err == nil {
			_ = f.Close()
			t.Errorf("Open(%s) succeeded", name)
		} else if strings.Contains(err.Error(), dir) {
			t.Errorf("Open(%s) says where Shelf's folder is, which is nothing a learner's Source failed for: %v", name, err)
		}
	}
}

// A folder that was moved away while Shelf ran is no longer where SQLite
// would look for it: Shelf opens nothing by a name that leads elsewhere now.
func TestAFolderThatWasMovedIsNotOpenedByItsOldName(t *testing.T) {
	base := t.TempDir()
	dir, elsewhere := filepath.Join(base, "shelf"), filepath.Join(base, "elsewhere")
	for _, d := range []string{dir, elsewhere} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := reopen(t, dir)
	if err := os.Rename(dir, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if c, err := f.Collection(ctx, "notes", true); c != nil || err == nil || !strings.Contains(err.Error(), "moved or replaced") {
		t.Errorf("Collection in a folder that was moved = %v, %v; want it refused", c, err)
	}
	if left, _ := os.ReadDir(elsewhere); len(left) != 0 {
		t.Errorf("Shelf wrote %v where the folder's old name leads now", left)
	}
}
