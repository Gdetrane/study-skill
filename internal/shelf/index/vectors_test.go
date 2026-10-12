package index_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
)

// made is what the vectors of most tests here were made with.
var made = index.Made{Model: "a-model", Document: "title: {title} | text: {text}", Dimensions: 8}

// along is a vector of length 1 that points along the axes given, each
// with its weight: along(8, 2, 0.8, 6, 0.5) leans most towards axis 2.
func along(dimensions int, axesAndWeights ...float64) []float32 {
	vector := make([]float32, dimensions)
	var sum float64
	for i := 0; i+1 < len(axesAndWeights); i += 2 {
		vector[int(axesAndWeights[i])] = float32(axesAndWeights[i+1])
		sum += axesAndWeights[i+1] * axesAndWeights[i+1]
	}
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / math.Sqrt(sum))
	}
	return vector
}

// axes are n vectors, the first along axis from, the next along the one
// after it, and so on.
func axes(n, dimensions, from int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = along(dimensions, float64(from+i), 1)
	}
	return out
}

// directions are n vectors of length 1, each pointing its own way.
func directions(n, dimensions int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		vector := make([]float32, dimensions)
		var sum float64
		for k := range vector {
			x := math.Sin(float64(i+1)*float64(k+1)*0.7) + 0.1
			vector[k] = float32(x)
			sum += x * x
		}
		for k := range vector {
			vector[k] = float32(float64(vector[k]) / math.Sqrt(sum))
		}
		out[i] = vector
	}
	return out
}

func nearest(t *testing.T, c *index.Collection, made index.Made, question []float32, limit int, sources ...string) []index.Found {
	t.Helper()
	found, err := c.Nearest(ctx, made, question, limit, sources)
	if err != nil {
		t.Fatalf("Nearest: %v", err)
	}
	for i, f := range found {
		if i > 0 && f.Score > found[i-1].Score {
			t.Fatalf("Nearest: Passage %d scores %v, more than the one before it, %v", i, f.Score, found[i-1].Score)
		}
	}
	return found
}

// withVectors is a Source with some content and a vector for each of its
// Passages.
func withVectors(id, char string, made index.Made, vectors [][]float32) (index.Source, []index.Passage) {
	var pages []string
	for n := range vectors {
		pages = append(pages, fmt.Sprintf("Passage %d of %s.", n, id))
	}
	source, passages := book(id, char, pages...)
	source.Made = made
	return source, passages
}

// checkVectors checks what a collection's file says of vectors against what
// it holds: a Source that is said to have them has one for each of its
// Passages, in runs that follow each other, and no vector belongs to a
// Source that is said to have none.
func checkVectors(t *testing.T, dir, name string) {
	t.Helper()
	db := raw(t, filepath.Join(dir, name+".sqlite"))
	defer db.Close()
	var format int
	if err := db.QueryRow("PRAGMA user_version").Scan(&format); err != nil || format < 2 {
		return // an empty file, or one from before vectors
	}
	var orphans int
	if err := db.QueryRow("SELECT count(*) FROM vectors WHERE source NOT IN (SELECT source FROM embeddings)").Scan(&orphans); err != nil || orphans != 0 {
		t.Errorf("%s holds %d rows of vectors of Sources that are said to have none (%v)", name, orphans, err)
	}
	rows, err := db.Query(`SELECT e.source, e.dimensions, (SELECT count(*) FROM passages p WHERE p.source = e.source),
		v.first, length(v.vectors) FROM embeddings e LEFT JOIN vectors v ON v.source = e.source ORDER BY e.source, v.first`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	next := map[string]int{}
	passages := map[string]int{}
	for rows.Next() {
		var source string
		var dimensions, count int
		var first, size *int
		if err := rows.Scan(&source, &dimensions, &count, &first, &size); err != nil {
			t.Fatal(err)
		}
		passages[source] = count
		if first == nil {
			continue
		}
		if *first != next[source] || *size%(4*dimensions) != 0 {
			t.Errorf("%s: a run of the vectors of %s begins at Passage %d and holds %d bytes, after %d Passages, with %d numbers a vector",
				name, source, *first, *size, next[source], dimensions)
		}
		next[source] += *size / (4 * dimensions)
	}
	for source, count := range passages {
		if next[source] != count {
			t.Errorf("%s holds %d vectors for the %d Passages of %s", name, next[source], count, source)
		}
	}
}

// Vectors are kept with the Passages they are of, and a question's vector
// finds the Passages nearest it, among the vectors made as it was made.
func TestPassagesAreFoundByTheirVectors(t *testing.T) {
	f, dir := folder(t)
	c := collection(t, f, "linear-algebra")
	a, aPassages := withVectors("a.a1", "a", made, axes(5, 8, 0))
	b, bPassages := withVectors("b.b2", "b", made, axes(3, 8, 5))
	other := index.Made{Model: "another-model", Document: made.Document, Dimensions: 8}
	d, dPassages := withVectors("d.d4", "d", other, axes(4, 8, 0))
	for _, write := range []struct {
		source   index.Source
		passages []index.Passage
		vectors  [][]float32
	}{{a, aPassages, axes(5, 8, 0)}, {b, bPassages, axes(3, 8, 5)}, {d, dPassages, axes(4, 8, 0)}} {
		if err := c.Replace(ctx, write.source, write.passages, write.vectors); err != nil {
			t.Fatal(err)
		}
	}
	plain, plainPassages := book("c.c3", "c", "A Source without vectors speaks of axes.")
	replace(t, c, plain, plainPassages)

	// What the index says of each Source's vectors.
	sources, err := c.Sources(ctx)
	if err != nil || len(sources) != 4 {
		t.Fatalf("Sources = %+v, %v", sources, err)
	}
	for i, want := range []index.Made{made, made, {}, other} {
		if sources[i].Made != want {
			t.Errorf("the vectors of %s are said to be made with %+v, want %+v", sources[i].ID, sources[i].Made, want)
		}
	}
	if one, err := c.Source(ctx, "b.b2"); err != nil || one == nil || *one != b {
		t.Errorf("Source(b.b2) = %+v, %v; want %+v", one, err, b)
	}

	// A question that leans most towards the third Passage of a, then the
	// second of b, then the first of a. Every other vector that counts is
	// at a right angle to it, and comes in the order of the text.
	question := along(8, 2, 0.8, 6, 0.5, 0, 0.2)
	found := nearest(t, c, made, question, 50)
	var got []string
	for _, p := range found {
		got = append(got, p.Text)
	}
	want := []string{"Passage 2 of a.a1.", "Passage 1 of b.b2.", "Passage 0 of a.a1.", "Passage 1 of a.a1.", "Passage 3 of a.a1.",
		"Passage 4 of a.a1.", "Passage 0 of b.b2.", "Passage 2 of b.b2."}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the nearest Passages are\n%q\nwant\n%q", got, want)
	}
	norm := math.Sqrt(0.8*0.8 + 0.5*0.5 + 0.2*0.2)
	for i, cosine := range []float64{0.8 / norm, 0.5 / norm, 0.2 / norm, 0} {
		if math.Abs(found[i].Score-cosine) > 1e-6 {
			t.Errorf("Passage %d scores %v, want the cosine %v", i, found[i].Score, cosine)
		}
	}
	// Each is the Passage a search by keyword gives: its id, its Source,
	// its page.
	if by := search(t, c, "Passage 2 of a.a1", 1); len(by) != 1 || by[0].Passage != found[0].Passage || found[0].First != 3 {
		t.Errorf("by its vector the Passage is %+v, and by its words %+v", found[0].Passage, by)
	}
	if again, err := c.Passage(ctx, found[0].ID); err != nil || again == nil || *again != found[0].Passage {
		t.Errorf("Passage(%s) = %+v, %v", found[0].ID, again, err)
	}

	// No more than the limit, and only in the Sources that are named.
	if few := nearest(t, c, made, question, 2); len(few) != 2 || few[1].Text != "Passage 1 of b.b2." {
		t.Errorf("with a limit of 2: %+v", few)
	}
	if in := nearest(t, c, made, question, 50, "b.b2", "no-such.z9"); len(in) != 3 || in[0].Text != "Passage 1 of b.b2." {
		t.Errorf("in one Source: %+v", in)
	}
	if none := nearest(t, c, made, question, 50, "c.c3"); len(none) != 0 {
		t.Errorf("in a Source without vectors: %+v", none)
	}
	// Vectors of another making are another matter: a question is compared
	// with the vectors of its own model, marked its own way, of its size.
	if theirs := nearest(t, c, other, question, 50); len(theirs) != 4 || theirs[0].Source != "d.d4" {
		t.Errorf("among the vectors of another model: %+v", theirs)
	}
	for name, as := range map[string]index.Made{
		"a model nothing was made with": {Model: "nobody's", Document: made.Document},
		"another marking":               {Model: made.Model, Document: "{text}"},
	} {
		if found := nearest(t, c, as, question, 50); len(found) != 0 {
			t.Errorf("%s finds %d Passages", name, len(found))
		}
	}
	if found := nearest(t, c, made, along(4, 2, 1), 50); len(found) != 0 {
		t.Errorf("a question of 4 numbers finds %d Passages by vectors of 8", len(found))
	}

	// New content takes the earlier vectors with it, whether or not it has
	// its own.
	changed, changedPassages := book("a.a1", "e", "The second edition has no vectors.")
	replace(t, c, changed, changedPassages)
	if now := nearest(t, c, made, question, 50); len(now) != 3 || now[0].Source != "b.b2" {
		t.Errorf("after a Source's content was replaced by content without vectors: %+v", now)
	}
	if one, err := c.Source(ctx, "a.a1"); err != nil || one.Made.Any() {
		t.Errorf("the Source is still said to have vectors: %+v, %v", one, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	checkIntegrity(t, dir, "linear-algebra")
	checkVectors(t, dir, "linear-algebra")
}

// The vectors of a long Source are several rows, and each still belongs to
// its Passage.
func TestVectorsOfManyPassagesAreKeptInRuns(t *testing.T) {
	t.Cleanup(index.SetRunLength(4))
	f, dir := folder(t)
	c := collection(t, f, "long")
	wide := index.Made{Model: "a-model", Document: "{text}", Dimensions: 16}
	source, passages := withVectors("long.a1", "a", wide, axes(10, 16, 0))
	if err := c.Replace(ctx, source, passages, axes(10, 16, 0)); err != nil {
		t.Fatal(err)
	}
	for n := range 10 {
		found := nearest(t, c, wide, along(16, float64(n), 1), 1)
		if want := fmt.Sprintf("Passage %d of long.a1.", n); len(found) != 1 || found[0].Text != want || math.Abs(found[0].Score-1) > 1e-6 {
			t.Errorf("the Passage nearest the vector of Passage %d is %+v", n, found)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	db := raw(t, filepath.Join(dir, "long.sqlite"))
	rows, err := db.Query("SELECT first, length(vectors) FROM vectors ORDER BY first")
	if err != nil {
		t.Fatal(err)
	}
	var runs []string
	for rows.Next() {
		var first, size int
		if err := rows.Scan(&first, &size); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, fmt.Sprintf("%d:%d", first, size))
	}
	if got := fmt.Sprint(runs); got != "[0:256 4:256 8:128]" {
		t.Errorf("the vectors of 10 Passages, 4 to a row, are the rows %s", got)
	}
	_ = rows.Close()
	_ = db.Close()
	checkVectors(t, dir, "long")
}

// Content that is indexed is given vectors, or other vectors, without being
// read again: its Passages and their ids stay as they are.
func TestIndexedContentIsGivenVectors(t *testing.T) {
	f, dir := folder(t)
	c := collection(t, f, "notes")
	source, passages := book("book.a1", "a", "The first page speaks of rank.", "The second page speaks of nullity.", "The third page speaks of bases.")
	replace(t, c, source, passages)
	before := search(t, c, "rank nullity bases", 10)
	if err := c.Fail(ctx, source, hash("a"), "the embedding endpoint could not be reached"); err != nil {
		t.Fatal(err)
	}

	hashed, texts, err := c.Texts(ctx, source.ID)
	if err != nil || hashed != source.Hash || len(texts) != 3 || texts[1] != "The second page speaks of nullity." {
		t.Fatalf("Texts = %q, %q, %v; want the hash of what is indexed and its Passages in order", hashed, texts, err)
	}
	if none, texts, err := c.Texts(ctx, "nothing.z9"); none != "" || len(texts) != 0 || err != nil {
		t.Errorf("Texts of a Source nothing is indexed of = %q, %q, %v", none, texts, err)
	}

	if err := c.Embed(ctx, source.ID, source.Hash, made, axes(3, 8, 0)); err != nil {
		t.Fatal(err)
	}
	now, err := c.Source(ctx, source.ID)
	if err != nil || now.Made != made || now.Hash != source.Hash {
		t.Fatalf("after its Passages were given vectors, the Source is %+v, %v", now, err)
	}
	if now.Failed != "" || now.Asked != "" {
		t.Errorf("a failure of an earlier try is still recorded: %+v", now)
	}
	if found := nearest(t, c, made, along(8, 1, 1), 1); len(found) != 1 || found[0].Text != "The second page speaks of nullity." {
		t.Errorf("the Passage nearest the second vector is %+v", found)
	}
	if after := search(t, c, "rank nullity bases", 10); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("giving vectors changed the Passages:\n%+v\nwere\n%+v", after, before)
	}

	// Vectors of another model take the place of the first.
	other := index.Made{Model: "another-model", Document: "{text}", Dimensions: 4}
	if err := c.Embed(ctx, source.ID, source.Hash, other, axes(3, 4, 1)); err != nil {
		t.Fatal(err)
	}
	if found := nearest(t, c, made, along(8, 1, 1), 5); len(found) != 0 {
		t.Errorf("the earlier vectors still find %d Passages", len(found))
	}
	if found := nearest(t, c, other, along(4, 3, 1), 1); len(found) != 1 || found[0].Text != "The third page speaks of bases." {
		t.Errorf("by the new vectors: %+v", found)
	}

	// Vectors of content that is no longer the Source's are not written:
	// another hash, another number of Passages, a Source nothing is indexed
	// of.
	for name, try := range map[string]func() error{
		"another hash":            func() error { return c.Embed(ctx, source.ID, hash("f"), made, axes(3, 8, 0)) },
		"too few vectors":         func() error { return c.Embed(ctx, source.ID, source.Hash, made, axes(2, 8, 0)) },
		"a Source nobody indexed": func() error { return c.Embed(ctx, "nothing.z9", hash("a"), made, axes(3, 8, 0)) },
	} {
		if err := try(); !errors.Is(err, index.ErrChanged) {
			t.Errorf("%s: Embed = %v, want it to say the content changed", name, err)
		}
	}
	if still, err := c.Source(ctx, source.ID); err != nil || still.Made != other {
		t.Errorf("a refused Embed changed the Source's vectors: %+v, %v", still, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	checkIntegrity(t, dir, "notes")
	checkVectors(t, dir, "notes")
}

func TestWhatAreNoVectorsOfTheContentIsRefused(t *testing.T) {
	f, dir := folder(t)
	c := collection(t, f, "notes")
	source, passages := book("book.a1", "a", "One page.", "Another page.")
	for name, tc := range map[string]struct {
		made    index.Made
		vectors [][]float32
	}{
		"vectors of no making":           {index.Made{}, axes(2, 8, 0)},
		"a making without vectors":       {made, nil},
		"too few vectors":                {made, axes(1, 8, 0)},
		"vectors of another size":        {made, axes(2, 4, 0)},
		"a making without a model":       {index.Made{Document: "{text}", Dimensions: 8}, axes(2, 8, 0)},
		"a model and a size of nothing":  {index.Made{Model: "a-model", Document: "{text}"}, nil},
		"one vector shorter than others": {made, [][]float32{along(8, 0, 1), along(7, 0, 1)}},
	} {
		with := source
		with.Made = tc.made
		if err := c.Replace(ctx, with, passages, tc.vectors); err == nil {
			t.Errorf("%s: Replace took them", name)
		}
	}
	if sources, err := c.Sources(ctx); err != nil || len(sources) != 0 {
		t.Errorf("content that was refused left %+v, %v", sources, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	checkVectors(t, dir, "notes")
}

// formatOne makes a collection's file as a Shelf from before vectors left
// it: in Format 1, with one Source of two Passages.
func formatOne(t *testing.T, dir, name string) index.Source {
	t.Helper()
	source := index.Source{ID: "old.a1", Kind: plugincontract.SourceFile, Title: "An old book", Hash: hash("a")}
	db := raw(t, filepath.Join(dir, name+".sqlite"))
	statements := append([]string{}, index.FormatOneSchema...)
	statements = append(statements, fmt.Sprintf("PRAGMA application_id = %d", index.ApplicationID), "PRAGMA user_version = 1",
		fmt.Sprintf("INSERT INTO sources (id, kind, title, hash) VALUES ('%s', 'file', '%s', '%s')", source.ID, source.Title, source.Hash))
	for place, text := range []string{"An old page speaks of determinants.", "Another old page speaks of traces."} {
		statements = append(statements,
			fmt.Sprintf("INSERT INTO passages (key, id, source, place, first_page, last_page) VALUES (%d, '%s', '%s', %d, %d, %d)",
				place+1, index.PassageID(source.ID, source.Hash, place, text), source.ID, place, place+1, place+1),
			fmt.Sprintf("INSERT INTO passages_text (rowid, text) VALUES (%d, '%s')", place+1, text))
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, name+".sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	return source
}

func formatOf(t *testing.T, dir, name string) int {
	t.Helper()
	db := raw(t, filepath.Join(dir, name+".sqlite"))
	defer db.Close()
	var format int
	if err := db.QueryRow("PRAGMA user_version").Scan(&format); err != nil {
		t.Fatal(err)
	}
	return format
}

// A file that a Shelf without embedding search wrote, in Format 1, is read
// as it is: what it holds is said and found. It becomes a file in Format 2
// with the next content that is written into it, and what it held stays.
func TestAFileFromBeforeVectorsIsReadAndThenBroughtUpToDate(t *testing.T) {
	for _, how := range []string{"new content", "vectors for what is there"} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			old := formatOne(t, dir, "earlier")
			f := reopen(t, dir)
			c, err := f.Collection(ctx, "earlier", false)
			if err != nil || c == nil {
				t.Fatalf("Collection of a file in Format 1 = %v, %v", c, err)
			}
			if sources, err := c.Sources(ctx); err != nil || len(sources) != 1 || sources[0] != old {
				t.Errorf("Sources = %+v, %v; want %+v", sources, err, old)
			}
			if one, err := c.Source(ctx, old.ID); err != nil || one == nil || *one != old {
				t.Errorf("Source = %+v, %v", one, err)
			}
			if found := search(t, c, "determinants", 5); len(found) != 1 || found[0].First != 1 {
				t.Errorf("a search finds %+v", found)
			}
			if found := nearest(t, c, made, along(8, 0, 1), 5); len(found) != 0 {
				t.Errorf("a file without vectors finds %d Passages by them", len(found))
			}
			// Reading, and recording a failure, leave the format as it is.
			if err := c.Fail(ctx, index.Source{ID: "bad.b2", Kind: "file", Title: "Bad"}, hash("b"), "the file is not a PDF"); err != nil {
				t.Fatal(err)
			}
			if got := formatOf(t, dir, "earlier"); got != 1 {
				t.Errorf("reading a file in Format 1 left it in format %d", got)
			}

			switch how {
			case "new content":
				source, passages := withVectors("new.c3", "c", made, axes(2, 8, 0))
				if err := c.Replace(ctx, source, passages, axes(2, 8, 0)); err != nil {
					t.Fatal(err)
				}
				if found := nearest(t, c, made, along(8, 1, 1), 1); len(found) != 1 || found[0].Text != "Passage 1 of new.c3." {
					t.Errorf("the new content's vectors find %+v", found)
				}
			default:
				if err := c.Embed(ctx, old.ID, old.Hash, made, axes(2, 8, 0)); err != nil {
					t.Fatal(err)
				}
				if found := nearest(t, c, made, along(8, 1, 1), 1); len(found) != 1 || found[0].Text != "Another old page speaks of traces." {
					t.Errorf("the old content's new vectors find %+v", found)
				}
			}
			if got := formatOf(t, dir, "earlier"); got != index.Format {
				t.Errorf("after content was written, the file is in format %d, want %d", got, index.Format)
			}
			// What it held is there, for this Shelf and for the next.
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			c, err = reopen(t, dir).Collection(ctx, "earlier", false)
			if err != nil || c == nil {
				t.Fatalf("Collection, once it is in Format 2 = %v, %v", c, err)
			}
			if found := search(t, c, "determinants traces", 5); len(found) != 2 {
				t.Errorf("what the file held is found %d times, want 2", len(found))
			}
			if failed, err := c.Source(ctx, "bad.b2"); err != nil || failed == nil || !strings.Contains(failed.Failed, "not a PDF") {
				t.Errorf("the failure it held = %+v, %v", failed, err)
			}
			checkIntegrity(t, dir, "earlier")
			checkVectors(t, dir, "earlier")
		})
	}
}

// What a search by vectors costs, with nothing kept from one search to the
// next: study starts a Shelf for every search, so each one opens the
// collection and reads every vector. Run it with
//
//	go test ./internal/shelf/index -run '^$' -bench Nearest -benchtime 5x
func BenchmarkNearest(b *testing.B) {
	for _, size := range []struct{ passages, dimensions int }{{5000, 512}, {5000, 768}, {50000, 512}, {50000, 768}} {
		b.Run(fmt.Sprintf("%d Passages of %d numbers", size.passages, size.dimensions), func(b *testing.B) {
			dir := b.TempDir()
			wide := index.Made{Model: "a-model", Document: "{text}", Dimensions: size.dimensions}
			f, err := index.Open(dir, index.Options{})
			if err != nil {
				b.Fatal(err)
			}
			c, err := f.Collection(ctx, "bench", true)
			if err != nil {
				b.Fatal(err)
			}
			// Books of 1,250 Passages each, as the four of the measurements
			// were on average.
			const perBook = 1250
			for book := 0; book*perBook < size.passages; book++ {
				vectors := directions(perBook, size.dimensions)
				passages := make([]index.Passage, perBook)
				for i := range passages {
					passages[i] = index.Passage{Text: fmt.Sprintf("Passage %d of book %d, with the words a Passage has.", i, book), First: i + 1, Last: i + 1}
				}
				source := index.Source{ID: fmt.Sprintf("book%d.a1", book), Kind: "file", Title: "A book", Hash: hash("a"), Made: wide}
				if err := c.Replace(ctx, source, passages, vectors); err != nil {
					b.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				b.Fatal(err)
			}
			question := directions(7, size.dimensions)[6]
			b.ResetTimer()
			for range b.N {
				f, err := index.Open(dir, index.Options{})
				if err != nil {
					b.Fatal(err)
				}
				c, err := f.Collection(ctx, "bench", false)
				if err != nil {
					b.Fatal(err)
				}
				found, err := c.Nearest(ctx, wide, question, 50, nil)
				if err != nil || len(found) != 50 {
					b.Fatalf("Nearest = %d Passages, %v", len(found), err)
				}
				if err := f.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
