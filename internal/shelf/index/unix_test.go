//go:build unix

package index_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
)

// names are the names Shelf has for a collection: its file, what SQLite
// keeps beside it, and the file its work is locked in.
func names(collection string) []string {
	file := collection + ".sqlite"
	return []string{file, file + "-wal", file + "-shm", file + "-journal", collection + ".lock"}
}

// A link in Shelf's folder, under any name Shelf has for a collection, is
// not followed: nothing is written where it leads, whether it leads to a
// file that is there, to a copy of the collection's own file, or nowhere.
func TestNoLinkInTheFolderIsFollowed(t *testing.T) {
	for _, name := range names("notes") {
		for _, leads := range []string{"nowhere", "an empty file", "a copy of the index"} {
			t.Run(name+" leads to "+leads, func(t *testing.T) {
				dir := stored(t, "notes")
				outside := t.TempDir()
				target := filepath.Join(outside, "target")
				switch leads {
				case "an empty file":
					if err := os.WriteFile(target, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "a copy of the index":
					data, err := os.ReadFile(filepath.Join(dir, "notes.sqlite"))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(target, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				_ = os.Remove(filepath.Join(dir, name))
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				before := tree(t, outside)

				f := reopen(t, dir)
				for _, create := range []bool{false, true} {
					c, err := f.Collection(ctx, "notes", create)
					var stranger *index.StrangerError
					if c != nil || !errors.As(err, &stranger) || stranger.File != name {
						t.Errorf("Collection(create: %v) = %v, %v; want it refused for %s, which is a link", create, c, err, name)
					}
					if err != nil && (strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), dir)) {
						t.Errorf("the refusal says where the folder is, or where the link leads: %v", err)
					}
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				if after := tree(t, outside); after != before {
					t.Errorf("Shelf wrote where a link in its folder leads.\nbefore:\n%s\nafter:\n%s", before, after)
				}
			})
		}
	}
}

// The same while Shelf runs: a collection that is open, and then exchanged
// for a link to a copy of itself, is not written through.
func TestALinkThatTakesAnOpenCollectionsPlaceIsNotFollowed(t *testing.T) {
	for _, name := range names("notes")[:3] {
		t.Run(name, func(t *testing.T) {
			f, dir := folder(t)
			source, passages := book("book.a1", "a", "A page about elimination.")
			replace(t, collection(t, f, "notes"), source, passages)

			outside := t.TempDir()
			target := filepath.Join(outside, name)
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(filepath.Join(dir, name))
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			before := tree(t, outside)

			// Whatever Shelf makes of it, a refusal or an answer from the
			// file it still holds, it writes nothing where the link leads.
			for n := range 20 {
				c, err := f.Collection(ctx, "notes", true)
				if err != nil || c == nil {
					continue
				}
				changed, passages := book("book.a1", "b", fmt.Sprintf("Other content, number %d, about pivots.", n))
				changed.Hash = "sha256:" + fmt.Sprintf("%064x", n)
				_ = c.Replace(ctx, changed, passages)
				_, _ = c.Search(ctx, "pivots elimination", 5, nil)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if after := tree(t, outside); after != before {
				t.Errorf("Shelf wrote where a link that took the place of %s leads.\nbefore:\n%s\nafter:\n%s", name, before, after)
			}
		})
	}
}

// A file with a second name elsewhere is one that writing to would change
// there too.
func TestAFileWithASecondNameIsRefused(t *testing.T) {
	dir := stored(t, "notes")
	elsewhere := filepath.Join(t.TempDir(), "the-same-file")
	if err := os.Link(filepath.Join(dir, "notes.sqlite"), elsewhere); err != nil {
		t.Skipf("this file system makes no second name for a file: %v", err)
	}
	before, err := os.ReadFile(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	f := reopen(t, dir)
	c, err := f.Collection(ctx, "notes", true)
	var stranger *index.StrangerError
	if c != nil || !errors.As(err, &stranger) {
		t.Errorf("Collection of a file with a second name = %v, %v; want it refused", c, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(elsewhere); err != nil || string(after) != string(before) {
		t.Errorf("the file was changed under its other name (%v)", err)
	}
}

// crashEnv names, to a test binary started as the helper below, the folder
// to write in and the point to be killed at. workEnv names, in its place,
// the Source another copy of Shelf is to be seen working on.
const (
	crashDirEnv   = "SHELF_INDEX_TEST_CRASH_DIR"
	crashPointEnv = "SHELF_INDEX_TEST_CRASH_POINT"
	workEnv       = "SHELF_INDEX_TEST_WORK"
)

// TestMain runs this test binary as another copy of Shelf, when it was
// started as one.
//
// As a Shelf that is killed in the middle of a write, it indexes new content
// of a Source and, at the point it was given, is killed as a power cut or a
// kill -9 would kill it. Nothing of the process survives to tidy up.
//
// As a Shelf at work, it says that it is indexing a Source and how far it
// is, says "working" once that is said, and goes on until its input closes,
// when it ends the work as work that was stopped, or until it is killed.
func TestMain(m *testing.M) {
	dir, point := os.Getenv(crashDirEnv), os.Getenv(crashPointEnv)
	if dir == "" {
		os.Exit(m.Run())
	}
	if source := os.Getenv(workEnv); source != "" {
		f, err := index.Open(dir, index.Options{})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		c, err := f.Collection(ctx, "worked-on", true)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		work, err := c.Begin(ctx, source)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		work.Progress(ctx, 0.25)
		fmt.Println("working")
		_, _ = io.Copy(io.Discard, os.Stdin)
		work.End()
		_ = f.Close()
		os.Exit(0)
	}
	f, err := index.Open(dir, index.Options{Crash: func(at string) {
		if at == point {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	c, err := f.Collection(ctx, "crashed", true)
	if err == nil {
		source, passages := newContent()
		err = c.Replace(ctx, source, passages)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	// The point was not reached.
	os.Exit(4)
}

// another starts another copy of Shelf that works on a Source in the folder
// at dir, and returns once it has said so. stop ends its work as work that
// was stopped, and kill ends the program as a kill -9 does.
func another(t *testing.T, dir, source string) (stop, kill func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), crashDirEnv+"="+dir, workEnv+"="+source)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ended := false
	end := func(how func()) func() {
		return func() {
			if !ended {
				ended = true
				how()
				_ = cmd.Wait()
			}
		}
	}
	stop, kill = end(func() { _ = input.Close() }), end(func() { _ = cmd.Process.Kill() })
	t.Cleanup(kill)
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "working\n" {
		kill()
		t.Fatalf("the other copy of Shelf did not get to work: %q, %v\n%s", line, err, stderr.String())
	}
	return stop, kill
}

// rows counts the rows of work in a collection's file, whoever they are of.
func rows(t *testing.T, dir, collection string) int {
	t.Helper()
	db := raw(t, filepath.Join(dir, collection+".sqlite"))
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM work").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// study starts a Shelf for every question it has, so the one that is asked
// what is being indexed is seldom the one that indexes. It sees the other's
// work for as long as the other lives, and not a moment longer: a Shelf that
// was killed halfway is not indexing, whatever it left written.
func TestAnotherCopysWorkIsSeenWhileItLives(t *testing.T) {
	f, dir := folder(t)
	c := collection(t, f, "worked-on")
	working := func() map[string]*float64 {
		t.Helper()
		got, err := c.Working(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := working(); len(got) != 0 {
		t.Fatalf("in a collection nobody indexes, Working = %v", got)
	}

	// Another copy indexes a book, and has said how far it is.
	_, kill := another(t, dir, "book.a1")
	got := working()
	if progress, ok := got["book.a1"]; len(got) != 1 || !ok || progress == nil || *progress != 0.25 {
		t.Fatalf("while another copy indexes a Source, Working = %v; want that Source, a quarter done", got)
	}
	// What this copy indexes itself is its own to know, and is not among
	// the others' work.
	own, err := c.Begin(ctx, "own.b2")
	if err != nil {
		t.Fatal(err)
	}
	if got := working(); len(got) != 1 || got["book.a1"] == nil {
		t.Errorf("Working = %v; want the other copy's work alone", got)
	}
	// And a second copy on a Source of its own.
	stop, _ := another(t, dir, "page.c3")
	if got := working(); len(got) != 2 {
		t.Errorf("with two other copies at work, Working = %v", got)
	}

	// The first is killed in the middle of it. Its row is still written,
	// and counts for nothing: nobody is indexing that Source now.
	kill()
	if n := rows(t, dir, "worked-on"); n != 3 {
		t.Fatalf("the collection holds %d rows of work, want the three that were begun", n)
	}
	if got := working(); len(got) != 1 || got["page.c3"] == nil {
		t.Errorf("after the copy that indexed a Source was killed, Working = %v; want the other one's work alone", got)
	}
	// The second is stopped, and takes its row with it.
	stop()
	if got := working(); len(got) != 0 {
		t.Errorf("after the other copies ended, Working = %v", got)
	}
	// The row of the one that was killed is swept out by the next work.
	next, err := c.Begin(ctx, "next.d4")
	if err != nil {
		t.Fatal(err)
	}
	if n := rows(t, dir, "worked-on"); n != 2 {
		t.Errorf("the collection holds %d rows of work, want this copy's two", n)
	}
	// Work that ends takes its row with it, however it ends.
	own.End()
	source, passages := book("next.d4", "d", "A page about pivots.")
	if err := next.Replace(ctx, source, passages); err != nil {
		t.Fatal(err)
	}
	next.End()
	failing, err := c.Begin(ctx, "bad.e5")
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Fail(ctx, index.Source{ID: "bad.e5", Kind: "file", Title: "Bad"}, hash("e"), "the file is not a PDF"); err != nil {
		t.Fatal(err)
	}
	failing.End()
	if n := rows(t, dir, "worked-on"); n != 0 {
		t.Errorf("the collection holds %d rows of work once all of it has ended", n)
	}
	if got, err := c.Source(ctx, "bad.e5"); err != nil || got == nil || got.Failed != "the file is not a PDF" || got.Asked != hash("e") {
		t.Errorf("the Source whose work failed = %+v, %v", got, err)
	}
	if found := search(t, c, "pivots", 5); len(found) != 1 {
		t.Errorf("the content of work that ended well is found %d times", len(found))
	}
}

func oldContent() (index.Source, []index.Passage) {
	var pages []string
	for n := range 40 {
		pages = append(pages, fmt.Sprintf("Page %d of the earlier content speaks of elimination and of nothing newer.", n+1))
	}
	return book("book.a1", "a", pages...)
}

func newContent() (index.Source, []index.Passage) {
	var pages []string
	for n := range 60 {
		pages = append(pages, fmt.Sprintf("Page %d of the new content speaks of pivots and of nothing earlier.", n+1))
	}
	return book("book.a1", "b", pages...)
}

// Killed at any point of a write, Shelf leaves an index that is whole: what
// was indexed before, all of it and nothing of the new, or the new content,
// all of it and nothing of the old. And whichever it is, the next Shelf
// reads it, and indexes the Source again.
func TestAShelfKilledWhileWritingLeavesAnIndexThatIsWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("starts this test binary again for each point")
	}
	for _, tc := range []struct {
		point string
		// had says that the Source was indexed before the write, and whole
		// which content the index holds after the crash: "old", "new", or
		// "none" for a collection that was being made.
		had   bool
		whole string
	}{
		{index.CrashFileMade, false, "none"},
		{index.CrashSchemaHalf, false, "none"},
		{index.CrashEarlierGone, false, "none"},
		{index.CrashHalfWritten, false, "none"},
		{index.CrashBeforeCommit, false, "none"},
		{index.CrashCommitted, false, "new"},
		{index.CrashEarlierGone, true, "old"},
		{index.CrashHalfWritten, true, "old"},
		{index.CrashBeforeCommit, true, "old"},
		{index.CrashCommitted, true, "new"},
	} {
		t.Run(fmt.Sprintf("%s (indexed before: %v)", tc.point, tc.had), func(t *testing.T) {
			dir := t.TempDir()
			if tc.had {
				f := reopen(t, dir)
				source, passages := oldContent()
				replace(t, collection(t, f, "crashed"), source, passages)
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), crashDirEnv+"="+dir, crashPointEnv+"="+tc.point)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("the Shelf that was to be killed at %q ended with %v, and was not killed there:\n%s", tc.point, err, out)
			}

			// The next Shelf.
			f := reopen(t, dir)
			c, err := f.Collection(ctx, "crashed", false)
			if err != nil {
				t.Fatalf("after the crash, the collection cannot be opened: %v", err)
			}
			old, _ := oldContent()
			fresh, freshPassages := newContent()
			switch tc.whole {
			case "none":
				if c != nil {
					if sources, err := c.Sources(ctx); err != nil || len(sources) != 0 {
						t.Errorf("after a crash before anything was indexed, the index lists %+v (%v)", sources, err)
					}
					if found := search(t, c, "pivots elimination", 50); len(found) != 0 {
						t.Errorf("after a crash before anything was indexed, a search finds %d Passages", len(found))
					}
				}
			default:
				want, word, other, count := old, "elimination", "pivots", 40
				if tc.whole == "new" {
					want, word, other, count = fresh, "pivots", "elimination", 60
				}
				if c == nil {
					t.Fatal("after the crash, the collection is gone")
				}
				if sources, err := c.Sources(ctx); err != nil || len(sources) != 1 || sources[0] != want {
					t.Errorf("after the crash, the index says %+v (%v); want %+v", sources, err, want)
				}
				// All of the one content, and nothing of the other: a
				// half-indexed Source would be found as if it were whole.
				if found := search(t, c, word, 50); len(found) != min(count, 50) {
					t.Errorf("a search for a word of the %s content finds %d Passages, want %d", tc.whole, len(found), min(count, 50))
				}
				if found := search(t, c, other, 50); len(found) != 0 {
					t.Errorf("a search finds %d Passages of the content the index does not hold", len(found))
				}
				var passages int
				db := raw(t, filepath.Join(dir, "crashed.sqlite"))
				if err := db.QueryRow("SELECT count(*) FROM passages").Scan(&passages); err != nil || passages != count {
					t.Errorf("the index holds %d Passages (%v), want the %d of the %s content", passages, err, count, tc.whole)
				}
				_ = db.Close()
			}

			// Asked again, the Source is indexed.
			c = collection(t, f, "crashed")
			replace(t, c, fresh, freshPassages)
			if found := search(t, c, "pivots", 50); len(found) != 50 {
				t.Errorf("indexed again after the crash, a search finds %d Passages of the new content", len(found))
			}
			if found := search(t, c, "elimination", 50); len(found) != 0 {
				t.Errorf("indexed again after the crash, a search still finds %d Passages of the earlier content", len(found))
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			checkIntegrity(t, dir, "crashed")
		})
	}
}
