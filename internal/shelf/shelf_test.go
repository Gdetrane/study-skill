//go:build unix

package shelf_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// A book of three pages: its Passages are the book's own words, each on the
// page it is on, and asked for again by its id it is the same.
func TestAPDFIsIndexedWithItsPages(t *testing.T) {
	_, s := start(t, t.TempDir())
	book := file(t, "strang.k3f9a2", pdf(
		[]string{"Linear algebra begins with the solution of linear equations.", "This first page says nothing about pivots."},
		[]string{"Elimination produces an upper triangular system.", "The pivots are on the diagonal of the triangle after elimination."},
		[]string{"Back substitution solves the triangular system from the bottom up."},
	))
	if first := s.index("linear-algebra", book); first.State != plugincontract.StateIndexing {
		t.Errorf("index answered %+v; want the Source being indexed", first)
	}
	done, _ := s.settled("linear-algebra", book.ID)
	if done.State != plugincontract.StateIndexed || done.Hash != book.Hash || done.Progress != nil || done.Reason != "" {
		t.Fatalf("status = %+v; want the Source indexed, with the hash of its file", done)
	}

	found := s.search("linear-algebra", "where are the pivots after elimination?", 10)
	if len(found) == 0 {
		t.Fatal("nothing found")
	}
	best := found[0]
	if got := strings.Join(strings.Fields(best.Text), " "); got != "Elimination produces an upper triangular system. The pivots are on the diagonal of the triangle after elimination." {
		t.Errorf("the best Passage is %q, want the second page", got)
	}
	if l := best.Location; l == nil || l.Kind != plugincontract.LocationPages || l.First != 2 || l.Last != 2 {
		t.Errorf("the best Passage's location is %+v, want page 2", best.Location)
	}
	if best.Source != book.ID || best.Score <= 0 {
		t.Errorf("the best Passage = %+v", best)
	}
	for _, p := range found {
		if p.Location == nil || p.Location.First < 1 || p.Location.Last > 3 {
			t.Errorf("Passage %s has the location %+v, in a book of three pages", p.ID, p.Location)
		}
	}
	again, ok := s.passage("linear-algebra", best.ID)
	if !ok || again.Text != best.Text || again.Source != best.Source || again.Location == nil || *again.Location != *best.Location {
		t.Errorf("passage gave %+v (found: %v), and the search gave %+v", again, ok, best.Passage)
	}
}

// A sentence that runs over the end of a page is in one Passage, which is
// on both pages: a quote of it can be checked.
func TestAPassageOverAPageBreakIsOnBothPages(t *testing.T) {
	_, s := start(t, t.TempDir())
	book := file(t, "pages.a1", pdf(
		[]string{"The first page ends well.", "Its last sentence, about the determinant, runs on to the"},
		[]string{"next page and ends there. The second page then goes on with something else."},
	))
	s.indexed("notes", book)
	found := holding(s.search("notes", "determinant", 5), "determinant")
	if len(found) != 1 {
		t.Fatalf("found %+v", found)
	}
	if got := strings.Join(strings.Fields(found[0].Text), " "); !strings.Contains(got, "about the determinant, runs on to the next page and ends there.") {
		t.Errorf("the Passage does not hold the whole sentence: %q", got)
	}
	if l := found[0].Location; l == nil || l.First != 1 || l.Last != 2 {
		t.Errorf("the Passage's location is %+v, want pages 1 to 2", found[0].Location)
	}
	// And what it took from the second page is in no other Passage.
	if twice := holding(s.search("notes", "ends there", 5), "ends there"); len(twice) != 1 {
		t.Errorf("%d Passages hold the end of the sentence, want one", len(twice))
	}
}

// What is wrong with a file is the Source's state, with a reason that tells
// the learner what it is and what to do, and none of it stops Shelf.
func TestAFileThatCannotBeIndexedSaysWhy(t *testing.T) {
	folder := t.TempDir()
	_, s := start(t, folder)
	good := pdf([]string{"A page of a book that is whole."}, []string{"And its second page, which is as whole."})
	dir := t.TempDir()
	pipe := filepath.Join(dir, "a-pipe.pdf")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := file(t, "unreadable.h8", good)
	if err := os.Chmod(unreadable.Path, 0); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		source plugincontract.Source
		want   []string
	}{
		{"a scanned book", file(t, "scanned.a1", shelftest.PDF{Pages: [][]string{{"unseen"}, {"unseen"}, {"unseen"}}, Pictures: true}.Bytes()),
			[]string{"no text layer", "pictures", "OCR"}},
		{"a file cut short", file(t, "cut-short.b2", good[:len(good)/3]), []string{"damaged", "Download or export it again"}},
		{"a header and no PDF behind it", file(t, "garbage.c3", append([]byte("%PDF-1.7\n"), strings.Repeat("nothing a PDF holds\n", 400)...)), []string{"damaged"}},
		{"a file that needs a password", file(t, "locked.d4", shelftest.PDF{Pages: [][]string{{"Nobody reads this."}}, Password: "open sesame"}.Bytes()),
			[]string{"protected by a password", "copy without the password"}},
		{"a text file", file(t, "notes.e5", []byte("Elimination produces an upper triangular system.\n")), []string{"not a PDF", "PDF files and web pages"}},
		{"a web page saved under a book's name", file(t, "saved.f6", []byte("<!DOCTYPE html>\n<html><body><p>Not found</p></body></html>\n")), []string{"not a PDF"}},
		{"an empty file", file(t, "empty.g7", nil), []string{"not a PDF"}},
		{"a file Shelf may not read", unreadable, []string{"not allowed to open it"}},
		{"a file that is not there", plugincontract.Source{ID: "gone.i9", Title: "Gone", Kind: "file", Path: filepath.Join(dir, "gone.pdf"), Hash: hashOf(good)},
			[]string{"not there", "moved or removed"}},
		{"a folder", plugincontract.Source{ID: "folder.j1", Title: "A folder", Kind: "file", Path: dir, Hash: hashOf(good)}, []string{"a folder, not a file"}},
		{"a pipe nobody writes to", plugincontract.Source{ID: "pipe.k2", Title: "A pipe", Kind: "file", Path: pipe, Hash: hashOf(good)}, []string{"not a file"}},
	}
	if os.Getuid() == 0 {
		cases = append(cases[:7], cases[8:]...) // root reads what nobody may
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &session{t: t, cs: s.cs}
			began := time.Now()
			status := s.fails("bad", tc.source, tc.want...)
			if took := time.Since(began); took > time.Minute {
				t.Errorf("it took %s to find out", took)
			}
			if status.Hash != "" {
				t.Errorf("status gives the hash %s for a Source nothing was indexed of", status.Hash)
			}
			// It stays failed, with its reason, however often it is asked.
			if again := s.status("bad")[tc.source.ID]; again != status {
				t.Errorf("asked again, status says %+v, and said %+v", again, status)
			}
			if found := s.search("bad", "unseen Elimination whole Nobody", 10, tc.source.ID); len(found) != 0 {
				t.Errorf("a search finds %+v in a Source that failed", found)
			}
		})
	}

	// None of it stopped Shelf: a good book is indexed after them, and found.
	s.indexed("bad", file(t, "good.z9", good))
	if found := s.search("bad", "whole", 10); len(found) == 0 {
		t.Error("a good book indexed after the bad ones is not found")
	}

	// A Shelf that is started later, as the one in the agent's MCP server is
	// after study source index has exited, still says why each failed.
	before := s.status("bad")
	_, later := start(t, folder)
	after := later.status("bad")
	if len(after) != len(before) {
		t.Fatalf("a Shelf started later lists %d Sources, and the one that indexed them %d", len(after), len(before))
	}
	for id, was := range before {
		if after[id] != was {
			t.Errorf("a Shelf started later says %+v of %s, and the one that indexed it said %+v", after[id], id, was)
		}
	}
	// And one that failed is indexed once its file is one Shelf reads.
	fixed := file(t, "scanned.a1", good)
	later.indexed("bad", fixed)
	if now := later.status("bad")["scanned.a1"]; now.State != plugincontract.StateIndexed || now.Hash != fixed.Hash || now.Reason != "" {
		t.Errorf("once its file can be read, status says %+v", now)
	}
}

// A file whose content changed is indexed anew: the new text is found, the
// old is not, and the old Passages' ids name nothing.
func TestAChangedFileReplacesWhatWasIndexed(t *testing.T) {
	_, s := start(t, t.TempDir())
	before := file(t, "notes.a1", pdf([]string{"The first draft speaks of quaternions."}, []string{"Its second page too mentions quaternions."}))
	s.indexed("notes", before)
	old := s.search("notes", "quaternions", 10)
	if len(old) != 2 {
		t.Fatalf("found %+v", old)
	}
	after := before
	data := pdf([]string{"The second draft speaks of octonions instead."})
	if err := os.WriteFile(after.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	after.Hash = hashOf(data)
	if now := s.indexed("notes", after); now.Hash != after.Hash {
		t.Errorf("status gives the hash %s, want the new content's, %s", now.Hash, after.Hash)
	}
	if found := s.search("notes", "quaternions", 10); len(found) != 0 {
		t.Errorf("a search still finds the old content: %+v", found)
	}
	if found := s.search("notes", "octonions", 10); len(found) != 1 || found[0].Location.First != 1 {
		t.Errorf("a search for the new content finds %+v", found)
	}
	for _, p := range old {
		if got, found := s.passage("notes", p.ID); found {
			t.Errorf("the id of a Passage of the old content now names %+v", got)
		}
	}
	// The hash study took before the file changed is not the one Shelf
	// gives: it says what it read.
	stale := after
	stale.ID, stale.Hash = "stale.b2", before.Hash
	if now := s.indexed("notes", stale); now.Hash != after.Hash {
		t.Errorf("status gives the hash %s for a file that hashes to %s", now.Hash, after.Hash)
	}
}

// A file that changes while Shelf reads it: its pages belong to no one
// content, so Shelf says so, or it gives the hash of content the file did
// hold. It never gives a hash for pages that were read of another content.
func TestAFileThatChangesWhileItIsReadIsNotIndexedUnderAnotherHash(t *testing.T) {
	_, s := start(t, t.TempDir())
	data := long(1500, "moving")
	book := file(t, "moving.a1", data)
	held := map[string]bool{book.Hash: true}
	growing, err := os.OpenFile(book.Path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer growing.Close()

	s.index("moving", book)
	var status plugincontract.SourceStatus
	listed := true
	for deadline := time.Now().Add(patience); time.Now().Before(deadline); {
		// A comment after its end leaves a PDF a PDF, with other bytes.
		line := []byte("% a line added while Shelf reads\n")
		if _, err := growing.Write(line); err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		held[hashOf(data)] = true
		if status, listed = s.status("moving")[book.ID]; !listed || status.State != plugincontract.StateIndexing {
			break
		}
	}
	switch {
	case listed && status.State == plugincontract.StateFailed && strings.Contains(status.Reason, "changed while it was being read") && status.Hash == "":
	case listed && status.State == plugincontract.StateIndexed && held[status.Hash]:
		t.Log("the file was read between two changes: Shelf gave the hash of what it held then")
	default:
		t.Errorf("status of a file that changed while it was read = %+v (listed: %v); want it failed for that, or indexed under a hash "+
			"of content the file held", status, listed)
	}
}

// A Passage's id is the same after Shelf was stopped and started, and after
// the same content was indexed on another computer.
func TestAPassagesIDOutlivesShelf(t *testing.T) {
	folder := t.TempDir()
	first, s := start(t, folder)
	book := file(t, "book.a1", pdf([]string{"A page about eigenvalues."}, []string{"A page about eigenvectors."}))
	s.indexed("notes", book)
	before := s.search("notes", "eigenvalues eigenvectors", 10)
	if len(before) != 2 {
		t.Fatalf("found %+v", before)
	}
	first.Close()

	_, s = start(t, folder)
	for _, was := range before {
		now, found := s.passage("notes", was.ID)
		if !found || now.Text != was.Text || *now.Location != *was.Location {
			t.Errorf("after Shelf was started again, Passage %s is %+v (found: %v), and was %+v", was.ID, now, found, was.Passage)
		}
	}
	// Indexed already: asking again does nothing, and answers at once.
	if again := s.index("notes", book); again.State != plugincontract.StateIndexed || again.Hash != book.Hash {
		t.Errorf("index of content that is indexed gave %+v", again)
	}
	// Another computer, another folder: the same content has the same ids.
	_, elsewhere := start(t, t.TempDir())
	elsewhere.indexed("notes", book)
	if there := elsewhere.search("notes", "eigenvalues eigenvectors", 10); fmt.Sprint(ids(there)) != fmt.Sprint(ids(before)) {
		t.Errorf("the same content indexed elsewhere has the ids %v, and here %v", ids(there), ids(before))
	}
}

func ids(found []plugincontract.ScoredPassage) []string {
	var out []string
	for _, p := range found {
		out = append(out, p.ID)
	}
	return out
}

// Indexing that was stopped leaves the Source as it was, and finishes when
// it is asked for again.
func TestIndexingThatWasStoppedIsTakenUpAgain(t *testing.T) {
	_, s := start(t, t.TempDir())
	kept := file(t, "kept.a1", pdf([]string{"A book that was indexed before speaks of determinants."}))
	s.indexed("notes", kept)

	book := file(t, "long.b2", long(600, "marker"))
	if first := s.index("notes", book); first.State != plugincontract.StateIndexing {
		t.Fatalf("index answered %+v", first)
	}
	// It says how far it is while it works, and never that it is done.
	if now := s.status("notes")[book.ID]; now.State != plugincontract.StateIndexing || (now.Progress != nil && (*now.Progress < 0 || *now.Progress >= 1)) {
		t.Errorf("status of a Source being indexed = %+v", now)
	}
	if !s.cancel("notes", book.ID) {
		t.Skip("the book was indexed before cancel arrived: stopping halfway was not checked")
	}
	status := s.status("notes")
	if now, listed := status[book.ID]; listed {
		t.Errorf("after a cancel, status says %+v of a Source that was never indexed; want it not listed", now)
	}
	if now := status[kept.ID]; now.State != plugincontract.StateIndexed || now.Hash != kept.Hash {
		t.Errorf("after a cancel, status of a Source that was indexed before = %+v", now)
	}
	if found := s.search("notes", "marker1 marker300 marker600", 10); len(found) != 0 {
		t.Errorf("after a cancel, a search finds %d Passages of the Source that was stopped: half a Source is found as if it were whole", len(found))
	}
	if s.cancel("notes", book.ID) {
		t.Error("cancel says it stopped the same indexing twice")
	}
	// Asked again, it is indexed, all of it.
	s.indexed("notes", book)
	for _, word := range []string{"marker1", "marker300", "marker600"} {
		found := holding(s.search("notes", word, 50, book.ID), word+" ")
		if len(found) != 1 {
			t.Errorf("after it was indexed again, %d Passages hold %s", len(found), word)
		}
	}
	if last := holding(s.search("notes", "marker600", 5), "marker600 "); len(last) == 1 && last[0].Location.First != 600 {
		t.Errorf("the last page's Passage is said to be on page %d", last[0].Location.First)
	}
}

// New content that is being indexed, or was stopped, or failed, leaves the
// earlier content found, with its hash.
func TestEarlierContentStaysUntilNewContentReplacesIt(t *testing.T) {
	_, s := start(t, t.TempDir())
	earlier := file(t, "book.a1", pdf([]string{"The first edition speaks of tensors."}))
	s.indexed("notes", earlier)

	later := earlier
	data := long(600, "later")
	later.Path = filepath.Join(t.TempDir(), "second-edition.pdf")
	if err := os.WriteFile(later.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	later.Hash = hashOf(data)
	if first := s.index("notes", later); first.State != plugincontract.StateIndexing || first.Hash != earlier.Hash {
		t.Fatalf("index of new content answered %+v; want it being indexed, with the hash of the earlier content", first)
	}
	if found := s.search("notes", "tensors", 5); len(found) != 1 {
		t.Errorf("while new content is being indexed, a search finds %d Passages of the earlier content", len(found))
	}
	if !s.cancel("notes", earlier.ID) {
		t.Skip("the new content was indexed before cancel arrived")
	}
	if now := s.status("notes")[earlier.ID]; now.State != plugincontract.StateIndexed || now.Hash != earlier.Hash {
		t.Errorf("after the indexing of new content was stopped, status says %+v; want the earlier content indexed", now)
	}

	// New content that cannot be read: failed, and the earlier content is
	// still found, with its hash.
	bad := earlier
	bad.Path, bad.Hash = filepath.Join(t.TempDir(), "missing.pdf"), hashOf([]byte("another"))
	failed := s.fails("notes", bad, "not there")
	if failed.Hash != earlier.Hash {
		t.Errorf("status of a Source whose new content failed gives the hash %q, want the earlier content's", failed.Hash)
	}
	if found := s.search("notes", "tensors", 5); len(found) != 1 {
		t.Errorf("after new content failed, a search finds %d Passages of the earlier content", len(found))
	}
	// Asked for with the content that is indexed, it is indexed: nothing
	// failed that is still asked for.
	if again := s.index("notes", earlier); again.State != plugincontract.StateIndexed || again.Hash != earlier.Hash {
		t.Errorf("index of the content that is indexed gave %+v", again)
	}
	if now := s.status("notes")[earlier.ID]; now.State != plugincontract.StateIndexed || now.Reason != "" {
		t.Errorf("status then says %+v", now)
	}
}

func TestTwoCollectionsAreKeptApart(t *testing.T) {
	folder := t.TempDir()
	_, s := start(t, folder)
	mine := file(t, "book.a1", pdf([]string{"In this Topic the book speaks of manifolds."}))
	theirs := file(t, "book.a1", pdf([]string{"In that Topic a book of the same id speaks of lattices."}))
	s.indexed("geometry", mine)
	s.indexed("algebra", theirs)
	for collection, tc := range map[string]struct {
		word, other string
		source      plugincontract.Source
	}{"geometry": {"manifolds", "lattices", mine}, "algebra": {"lattices", "manifolds", theirs}} {
		found := s.search(collection, "manifolds lattices", 10)
		if len(holding(found, tc.word)) != 1 || len(holding(found, tc.other)) != 0 {
			t.Errorf("a search of %s finds %+v", collection, found)
		}
		if status := s.status(collection); len(status) != 1 || status["book.a1"].Hash != tc.source.Hash {
			t.Errorf("status of %s = %+v", collection, status)
		}
		// A Passage of one is no Passage of the other.
		other := map[string]string{"geometry": "algebra", "algebra": "geometry"}[collection]
		if p, found := s.passage(other, found[0].ID); found {
			t.Errorf("the id of a Passage of %s names %+v in %s", collection, p, other)
		}
	}
	// One file for each, and nothing else that stays.
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "geometry.sqlite") && !strings.HasPrefix(name, "algebra.sqlite") {
			t.Errorf("Shelf's folder holds %s", name)
		}
	}
}

// A web page: fetched when it is indexed and at no other time, its Passages
// without pages, and fetched again only when that is asked for.
func TestAWebPageIsIndexedAndFetchedOnlyByIndex(t *testing.T) {
	var fetched atomic.Int64
	body := atomic.Pointer[string]{}
	first := "<html><head><title>Notes on groups</title></head><body><h1>Notes on groups</h1>" +
		"<p>A group is a set with one operation that is associative.</p><p>Every element has an inverse.</p></body></html>"
	body.Store(&first)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, *body.Load())
	}))
	defer server.Close()

	_, s := start(t, t.TempDir())
	page := plugincontract.Source{ID: "groups.m4k8s3", Title: "Notes on groups", Kind: plugincontract.SourceURL, URL: server.URL + "/groups"}
	done := s.indexed("algebra", page)
	if done.Hash != hashOf([]byte(first)) {
		t.Errorf("status gives the hash %s, want the one of the bytes that were fetched", done.Hash)
	}
	found := s.search("algebra", "inverse", 5)
	if len(found) != 1 || found[0].Location != nil || found[0].Source != page.ID {
		t.Fatalf("found %+v; want one Passage, without a location", found)
	}
	if got := found[0].Text; got != "A group is a set with one operation that is associative.\n\nEvery element has an inverse." {
		t.Errorf("the Passage is %q", got)
	}
	if again, ok := s.passage("algebra", found[0].ID); !ok || again.Location != nil || again.Text != found[0].Text {
		t.Errorf("passage gave %+v (found: %v)", again, ok)
	}
	// Searching, reading a Passage, asking for the status and asking again
	// for what is indexed fetch nothing.
	s.status("algebra")
	if again := s.index("algebra", page); again.State != plugincontract.StateIndexed {
		t.Errorf("index of a page that is indexed gave %+v", again)
	}
	if n := fetched.Load(); n != 1 {
		t.Errorf("the page was fetched %d times, want once", n)
	}
	// The page changed. It is read again when the learner asks for that.
	second := strings.Replace(first, "Every element has an inverse.", "Every element has an inverse, and there is an identity.", 1)
	body.Store(&second)
	page.Refetch = true
	if now := s.indexed("algebra", page); now.Hash != hashOf([]byte(second)) {
		t.Errorf("after fetching again, status gives the hash %s, want the new content's", now.Hash)
	}
	if n := fetched.Load(); n != 2 {
		t.Errorf("the page was fetched %d times, want twice", n)
	}
	if found := s.search("algebra", "identity", 5); len(found) != 1 {
		t.Errorf("the page's new text is not found: %+v", found)
	}
	// A page that is gone since: failed, with the reason, and the text that
	// was fetched before is still found.
	server.Close()
	failed := s.fails("algebra", page, "could not be reached")
	if failed.Hash != hashOf([]byte(second)) {
		t.Errorf("status of a page that could not be fetched again gives the hash %q, want the one of what is still found", failed.Hash)
	}
	if found := s.search("algebra", "identity", 5); len(found) != 1 {
		t.Errorf("after a fetch failed, the text fetched before is no longer found")
	}
}

// What Shelf is asked that the contract does not allow, it refuses, and it
// says that it cannot find a Passage with the contract's own code.
func TestWhatTheContractDoesNotAllowIsRefused(t *testing.T) {
	folder := t.TempDir()
	_, s := start(t, folder)
	for name, tc := range map[string]struct {
		tool string
		in   any
		code string
	}{
		"a collection that is a path":   {"status", map[string]any{"collection": "../other"}, "invalid_argument"},
		"a collection that is absolute": {"search", map[string]any{"collection": "/etc", "query": "x", "limit": 1}, "invalid_argument"},
		"a Source without a hash":       {"index", map[string]any{"collection": "a", "source": map[string]any{"id": "b.c1", "title": "T", "kind": "file", "path": "/b.pdf"}}, "invalid_argument"},
		"a Source by a relative path":   {"index", map[string]any{"collection": "a", "source": map[string]any{"id": "b.c1", "title": "T", "kind": "file", "path": "b.pdf", "hash": hashOf(nil)}}, "invalid_argument"},
		"a web page that is a file":     {"index", map[string]any{"collection": "a", "source": map[string]any{"id": "b.c1", "title": "T", "kind": "url", "url": "file:///etc/hostname"}}, "invalid_argument"},
		"a web page with a password":    {"index", map[string]any{"collection": "a", "source": map[string]any{"id": "b.c1", "title": "T", "kind": "url", "url": "https://ada:secret@example.org/"}}, "invalid_argument"},
		"a kind there is none of":       {"index", map[string]any{"collection": "a", "source": map[string]any{"id": "b.c1", "title": "T", "kind": "recording", "url": "https://example.org/"}}, "invalid_argument"},
		"a query of white space":        {"search", map[string]any{"collection": "a", "query": " \n ", "limit": 5}, "invalid_argument"},
		"a limit over the highest":      {"search", map[string]any{"collection": "a", "query": "x", "limit": 51}, "invalid_argument"},
		"a list of no Sources":          {"search", map[string]any{"collection": "a", "query": "x", "limit": 5, "sources": []string{}}, "invalid_argument"},
		"a list that is null":           {"search", map[string]any{"collection": "a", "query": "x", "limit": 5, "sources": nil}, "invalid_argument"},
		"an argument in capitals":       {"status", map[string]any{"Collection": "a"}, "invalid_argument"},
		"a Passage's id that is a path": {"passage", map[string]any{"collection": "a", "id": "../../etc/passwd"}, "invalid_argument"},
		"a Passage there is none of":    {"passage", map[string]any{"collection": "a", "id": "0000000000000000"}, "not_found"},
		"a cancel without a Source":     {"cancel", map[string]any{"collection": "a"}, "invalid_argument"},
	} {
		var out map[string]any
		refusal := s.call(tc.tool, tc.in, &out)
		if refusal == nil || refusal.Code != tc.code || strings.TrimSpace(refusal.Message) == "" {
			t.Errorf("%s: %s gave %v and the refusal %+v; want it refused with %s", name, tc.tool, out, refusal, tc.code)
		}
	}
	// A field the contract does not name is passed over.
	var status plugincontract.StatusResult
	s.ok("status", map[string]any{"collection": "a", "added_later": true}, &status)
	// Nothing of all that left a trace.
	if left, _ := os.ReadDir(folder); len(left) != 0 {
		t.Errorf("refused requests left %v in Shelf's folder", left)
	}
	if left, _ := os.ReadDir(filepath.Dir(folder)); len(left) != 1 {
		t.Errorf("refused requests left %v beside Shelf's folder", left)
	}
}

// A Shelf whose folder cannot be opened still says which contract it
// speaks, and says what is wrong to whoever asks for anything else.
func TestShelfStartsWithoutItsFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-made")
	_, s := start(t, missing)
	var contract plugincontract.Contract
	s.ok("contract", struct{}{}, &contract)
	if contract.Version != 1 || fmt.Sprint(contract.Sources, contract.Search, contract.Locations) != "[file url] [keyword] [pages]" {
		t.Errorf("contract = %+v", contract)
	}
	for tool, in := range map[string]any{
		"status":  map[string]any{"collection": "a"},
		"search":  map[string]any{"collection": "a", "query": "x", "limit": 1},
		"passage": map[string]any{"collection": "a", "id": "x"},
		"index":   plugincontract.IndexInput{Collection: "a", Source: plugincontract.Source{ID: "b.c1", Title: "T", Kind: "file", Path: "/b.pdf", Hash: hashOf(nil)}},
	} {
		var out map[string]any
		refusal := s.call(tool, in, &out)
		if refusal == nil || refusal.Code != plugincontract.CodeInternal || !strings.Contains(refusal.Message, "Shelf's folder cannot be opened") {
			t.Errorf("%s in a Shelf without a folder gave %v and %+v", tool, out, refusal)
		}
		if refusal != nil && strings.Contains(refusal.Message, missing) {
			t.Errorf("%s says where the folder is: %s", tool, refusal.Message)
		}
	}
	if s.cancel("a", "b.c1") {
		t.Error("cancel says it stopped something")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("Shelf made the folder it was to find")
	}
}

// Closing stops what is being indexed, quickly: study gives a plugin two
// seconds to exit once its input has closed.
func TestClosingStopsWhatIsBeingIndexed(t *testing.T) {
	folder := t.TempDir()
	plugin, s := start(t, folder)
	// Once, so that the reader is compiled before the clock starts.
	s.indexed("notes", file(t, "first.a1", pdf([]string{"A first book, which stays."})))
	book := file(t, "long.b2", long(3000, "word"))
	if first := s.index("notes", book); first.State != plugincontract.StateIndexing {
		t.Fatalf("index answered %+v", first)
	}
	began := time.Now()
	plugin.Close()
	if took := time.Since(began); took > 1500*time.Millisecond {
		t.Errorf("closing took %s", took)
	}
	// What was indexed before is there for the next Shelf, and nothing of
	// what was stopped.
	_, s = start(t, folder)
	status := s.status("notes")
	if now := status["first.a1"]; now.State != plugincontract.StateIndexed {
		t.Errorf("after a Shelf was closed while indexing, the next says %+v of what was indexed before", now)
	}
	if now, listed := status[book.ID]; listed {
		t.Errorf("after a Shelf was closed while indexing, the next says %+v of the Source it was indexing; want nothing", now)
	}
	if found := s.search("notes", "word1 word1500", 5); len(found) != 0 {
		t.Errorf("a search finds %d Passages of a Source whose indexing was stopped", len(found))
	}
}

// One file has so much time and no more: a file that takes longer fails,
// with the reason, and Shelf goes on with the next.
func TestAFileThatTakesTooLongFails(t *testing.T) {
	folder := t.TempDir()
	// First with all the time there is, so that the reader is compiled.
	_, s := start(t, folder)
	quick := file(t, "quick.a1", pdf([]string{"A book that is read in no time speaks of fibrations."}))
	s.indexed("slow", quick)

	_, hurried := startWith(t, folder, shelf.Settings{FileTime: 20 * time.Millisecond})
	failed := hurried.fails("slow", file(t, "slow.b2", long(4000, "leaf")), "took longer than 20ms")
	if failed.Hash != "" {
		t.Errorf("status gives the hash %s for a file that was not read to its end", failed.Hash)
	}
	if found := hurried.search("slow", "leaf1 leaf2 leaf3", 5); len(found) != 0 {
		t.Errorf("a search finds %d Passages of a file that was not read to its end", len(found))
	}
	// What was indexed is found, and the reader reads the next file: the one
	// it was stopped over took nothing with it.
	if found := hurried.search("slow", "fibrations", 5); len(found) != 1 {
		t.Errorf("after a file took too long, what was indexed before is found %d times", len(found))
	}
	s.indexed("slow", file(t, "next.c3", pdf([]string{"The next book speaks of cobordisms."})))
	if found := hurried.search("slow", "cobordisms", 5); len(found) != 1 {
		t.Errorf("a file indexed after one took too long is found %d times", len(found))
	}
}

// Passages are as long as the settings say: a Recipe will say how long a
// Passage may be for its model.
func TestPassagesAreAsLongAsTheSettingsSay(t *testing.T) {
	var paragraphs []string
	for n := range 40 {
		paragraphs = append(paragraphs, fmt.Sprintf("Sentence %d of this page is about the spectral theorem and its uses.", n+1))
	}
	book := file(t, "book.a1", pdf(paragraphs))
	count := func(size int) int {
		_, s := startWith(t, t.TempDir(), shelf.Settings{PassageSize: size})
		s.indexed("sized", book)
		found := s.search("sized", "spectral", 50)
		for _, p := range found {
			if n := len([]rune(p.Text)); n > size+size/3 {
				t.Errorf("with Passages of about %d characters, one holds %d", size, n)
			}
		}
		return len(found)
	}
	if short, usual := count(300), count(1500); short <= usual || usual < 2 {
		t.Errorf("a page of 2,700 characters is %d Passages of about 300 characters and %d of about 1,500", short, usual)
	}
}

// The same content asked for while it is being indexed is one piece of
// work, and a Source's title is no content.
func TestAskingTwiceIsOnePieceOfWork(t *testing.T) {
	_, s := start(t, t.TempDir())
	book := file(t, "book.a1", long(400, "twice"))
	first := s.index("notes", book)
	renamed := book
	renamed.Title = "Another title for the same book"
	second := s.index("notes", renamed)
	if first.State != plugincontract.StateIndexing || second.State != plugincontract.StateIndexing {
		t.Fatalf("index answered %+v and then %+v", first, second)
	}
	if done, _ := s.settled("notes", book.ID); done.State != plugincontract.StateIndexed {
		t.Fatalf("status = %+v", done)
	}
	if found := holding(s.search("notes", "twice7", 50), "twice7 "); len(found) != 1 {
		t.Errorf("%d Passages hold a word the book says once", len(found))
	}
	if third := s.index("notes", renamed); third.State != plugincontract.StateIndexed {
		t.Errorf("index under another title gave %+v; want the Source indexed already", third)
	}
}

// Many Sources asked for at once are all indexed, a few at a time.
func TestManySourcesAtOnce(t *testing.T) {
	_, s := start(t, t.TempDir())
	var books []plugincontract.Source
	for n := range 12 {
		books = append(books, file(t, fmt.Sprintf("book%d.a1", n), pdf([]string{fmt.Sprintf("Book number %d speaks of topic%d.", n, n)})))
	}
	for _, book := range books {
		s.index("many", book)
	}
	for n, book := range books {
		if done, _ := s.settled("many", book.ID); done.State != plugincontract.StateIndexed || done.Hash != book.Hash {
			t.Errorf("status of %s = %+v", book.ID, done)
		}
		if found := s.search("many", fmt.Sprintf("topic%d", n), 5); len(found) != 1 || found[0].Source != book.ID {
			t.Errorf("a search for book %d finds %+v", n, found)
		}
	}
	if status := s.status("many"); len(status) != len(books) {
		t.Errorf("status lists %d Sources, want %d", len(status), len(books))
	}
}

func TestShelfSaysWhatItIs(t *testing.T) {
	if got := shelf.VersionString(); got != "dev" {
		t.Errorf("VersionString() = %q in a test, want dev", got)
	}
	was := shelf.Version
	shelf.Version = "v2.0.0"
	defer func() { shelf.Version = was }()
	if got := shelf.VersionString(); got != "v2.0.0" {
		t.Errorf("VersionString() = %q, want the release's", got)
	}
}
