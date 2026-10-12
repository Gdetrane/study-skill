//go:build unix

package index_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnly takes from the learner the permission to write in a folder and
// to write the files in it, as a sandbox that lets a program read there and
// no more does, and gives it back when the test ends.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes where nobody may")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Chmod(filepath.Join(dir, entry.Name()), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// A glance from inside a sandbox that lets Shelf read its folder and not
// write there, as study status typed by an agent is: what is indexed is
// still said, and still found, and nothing is left behind. Indexing there
// fails, and says what to do.
func TestACollectionIsReadInAFolderThatCannotBeWritten(t *testing.T) {
	dir := stored(t, "notes")
	readOnly(t, dir)
	before := tree(t, dir)

	f := reopen(t, dir)
	c, err := f.Collection(ctx, "notes", false)
	if err != nil || c == nil {
		t.Fatalf("Collection in a folder that cannot be written = %v, %v", c, err)
	}
	sources, err := c.Sources(ctx)
	if err != nil || len(sources) != 1 || sources[0].ID != "book.a1" {
		t.Errorf("Sources = %+v, %v", sources, err)
	}
	if found := search(t, c, "elimination pivots", 5); len(found) != 2 {
		t.Errorf("a search finds %d Passages", len(found))
	}
	if working, err := c.Working(ctx); err != nil || len(working) != 0 {
		t.Errorf("Working = %v, %v", working, err)
	}
	// What would write says why it cannot, and what to do about it.
	source, passages := book("other.b2", "b", "A page that is not written.")
	for name, err := range map[string]error{
		"Replace": c.Replace(ctx, source, passages, nil),
		"Fail":    c.Fail(ctx, source, hash("b"), "the file is not a PDF"),
		"Begin":   func() error { _, err := c.Begin(ctx, source.ID); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "Shelf may not write in its folder here") || !strings.Contains(err.Error(), "Allow the folder") {
			t.Errorf("%s in a folder that cannot be written: %v; want it to say so, and what to do", name, err)
		}
	}
	// A collection nobody indexed is still none, and is not made.
	if none, err := f.Collection(ctx, "nothing", false); none != nil || err != nil {
		t.Errorf("Collection of a name nothing was indexed under = %v, %v", none, err)
	}
	if made, err := f.Collection(ctx, "nothing", true); made != nil || err == nil {
		t.Errorf("Collection, to be made where nothing can be = %v, %v", made, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, dir); after != before {
		t.Errorf("reading changed a folder that could not be written.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The same glance while another Shelf, one that may write, indexes in the
// collection: it sees what is indexed and what the other is working on.
func TestAnotherCopysWorkIsSeenFromAFolderThatCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	f := reopen(t, dir)
	source, passages := book("book.a1", "a", "A page about elimination.")
	replace(t, collection(t, f, "worked-on"), source, passages)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// The other copy has the collection open, and holds its lock, before
	// this one loses the permission to write.
	stop, _ := another(t, dir, "new.b2")
	readOnly(t, dir)

	glance := reopen(t, dir)
	c, err := glance.Collection(ctx, "worked-on", false)
	if err != nil || c == nil {
		t.Fatalf("Collection = %v, %v", c, err)
	}
	if found := search(t, c, "elimination", 5); len(found) != 1 {
		t.Errorf("a search finds %d Passages", len(found))
	}
	working, err := c.Working(ctx)
	if progress, ok := working["new.b2"]; err != nil || len(working) != 1 || !ok || progress == nil {
		t.Errorf("Working = %v, %v; want the Source the other copy is indexing", working, err)
	}
	// The other one ends its work: what it had open, it may still write.
	stop()
}

// A file from before vectors, in a folder that cannot be written: it cannot
// be brought up to date there, and is read as it is all the same.
func TestAFileFromBeforeVectorsIsReadWhereItCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	old := formatOne(t, dir, "earlier")
	// As the Shelf that wrote it left it: opened once, and closed.
	first := reopen(t, dir)
	if c, err := first.Collection(ctx, "earlier", false); err != nil || c == nil {
		t.Fatalf("Collection = %v, %v", c, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly(t, dir)
	before := tree(t, dir)

	f := reopen(t, dir)
	c, err := f.Collection(ctx, "earlier", false)
	if err != nil || c == nil {
		t.Fatalf("Collection in a folder that cannot be written = %v, %v", c, err)
	}
	if sources, err := c.Sources(ctx); err != nil || len(sources) != 1 || sources[0] != old {
		t.Errorf("Sources = %+v, %v", sources, err)
	}
	if found := search(t, c, "determinants traces", 5); len(found) != 2 {
		t.Errorf("a search finds %d Passages", len(found))
	}
	if found := nearest(t, c, made, along(8, 0, 1), 5); len(found) != 0 {
		t.Errorf("%d Passages are found by vectors the file does not hold", len(found))
	}
	if err := c.Embed(ctx, old.ID, old.Hash, made, axes(2, 8, 0)); err == nil || !strings.Contains(err.Error(), "Shelf may not write in its folder here") {
		t.Errorf("Embed in a folder that cannot be written: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, dir); after != before {
		t.Errorf("reading changed a folder that could not be written.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
