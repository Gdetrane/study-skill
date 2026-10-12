//go:build unix

package shelf_test

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

const module = "github.com/mordor-forge/lamplight/v2"

// goList runs go list from the module's root and returns its lines.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("go list %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// TestStudyLinksNothingOfShelf lists what study is built from. Shelf is a
// second program (ADR-0012): study starts it and speaks the contract with
// it, and links none of its code, nor SQLite, nor the PDF reader, nor what
// reads web pages. A package of study's that came to import any of them
// would put a document parser inside the program that holds the learner's
// records, and inside the MCP server, which runs outside the agent's
// sandbox.
func TestStudyLinksNothingOfShelf(t *testing.T) {
	// What only Shelf is built from: its own packages, and each library it
	// brought into the module.
	shelfs := []string{
		module + "/internal/shelf",
		module + "/cmd/study-shelf",
		"modernc.org/",                  // SQLite, and the C library it stands on
		"github.com/klippa-app/",        // PDFium
		"github.com/tetratelabs/wazero", // the WebAssembly runtime PDFium runs in
		"codeberg.org/readeck/",         // the readable text of a web page
		"golang.org/x/net/html",         // the HTML it is read from
	}
	// of names which of them a package is, or is under.
	of := func(dep string) string {
		for _, prefix := range shelfs {
			if dep == prefix || strings.HasPrefix(dep, strings.TrimSuffix(prefix, "/")+"/") {
				return prefix
			}
		}
		return ""
	}
	for _, pkg := range []string{"./cmd/study", "./internal/mcpserver", "./internal/core", "./internal/cli", "./internal/pluginclient"} {
		deps := goList(t, "-deps", pkg)
		for _, dep := range deps {
			if prefix := of(dep); prefix != "" {
				t.Errorf("%s links %s: study links nothing of Shelf's (%s). Shelf is the study-shelf program, which study starts "+
					"and speaks the Knowledge base plugin contract with (ADR-0012)", pkg, dep, prefix)
			}
		}
		// The list is what it seems: it holds the contract both speak.
		if !slices.Contains(deps, module+"/internal/plugincontract") {
			t.Fatalf("go list -deps %s does not list the contract's package, so this test proves nothing:\n%s", pkg, strings.Join(deps, "\n"))
		}
	}
	// And the names above are the ones Shelf is built from: each is in what
	// study-shelf links.
	deps := goList(t, "-deps", "./cmd/study-shelf")
	for _, prefix := range shelfs {
		if !slices.ContainsFunc(deps, func(dep string) bool { return of(dep) == prefix }) {
			t.Errorf("study-shelf links nothing under %s: this test names what Shelf no longer uses, or names it wrongly", prefix)
		}
	}
	// Nothing but Shelf and its own tests imports Shelf's packages.
	for _, line := range goList(t, "-f", `{{.ImportPath}} {{join .Imports ","}} {{join .TestImports ","}} {{join .XTestImports ","}}`, "./...") {
		pkg, imports, _ := strings.Cut(line, " ")
		if strings.HasPrefix(pkg, module+"/internal/shelf") || pkg == module+"/cmd/study-shelf" {
			continue
		}
		for _, imported := range strings.FieldsFunc(imports, func(r rune) bool { return r == ',' || r == ' ' }) {
			// The end-to-end tests build study-shelf, and install it.
			if strings.HasPrefix(imported, module+"/internal/shelf") && !(pkg == module+"/internal/e2e" && imported == module+"/internal/shelf/shelftest") {
				t.Errorf("%s imports %s: only Shelf imports Shelf", strings.TrimPrefix(pkg, module+"/"), imported)
			}
		}
	}
}

// The program the other tests start is study-shelf, built from this
// checkout: it says so, on its standard output and nowhere else, and exits.
func TestTheBuiltStudyShelfRuns(t *testing.T) {
	cmd := exec.Command(shelftest.Shelf(t), "--version")
	cmd.Env = []string{"HOME=" + t.TempDir()}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stdout.String() != "study-shelf version dev\n" || stderr.Len() != 0 {
		t.Errorf("study-shelf --version: %v\nstdout: %q\nstderr: %q", err, stdout.String(), stderr.String())
	}
}

// Registered with --stdio, as docs/cli.md's examples register it, study-shelf
// is the same plugin: the flag says outright how it is reached.
func TestStudyShelfTakesStdioSaidOutright(t *testing.T) {
	link := plugintest.AsCommand(plugintest.NewComputer(t), shelftest.Shelf(t), "--stdio")(t)
	s := dial(t, link.Transport)
	var contract plugincontract.Contract
	s.ok(plugincontract.ToolContract, struct{}{}, &contract)
	if contract.Version != plugincontract.Version {
		t.Errorf("contract = %+v", contract)
	}
	if got := s.cs.InitializeResult().ServerInfo; got.Name != "study-shelf" || got.Version != "dev" {
		t.Errorf("it calls itself %+v, want study-shelf, in a version without a v before it", got)
	}
}

// A learner's home may lie below folders they can pass through and not list,
// as /home with mode 711 is on a shared computer. study starts a plugin
// there, and Shelf keeps its index there: it opens its own folder, and asks
// to list none above it.
func TestShelfWorksBelowFoldersThatCannotBeListed(t *testing.T) {
	computer := plugintest.NewComputer(t)
	plugintest.PassThrough(t, computer.Base(), computer.Home)
	s, _ := run(t, computer)
	book := file(t, "book.a1", pdf([]string{"A book on a shared computer speaks of sheaves."}))
	s.indexed("shared-computer", book)
	if found := s.search("shared-computer", "sheaves", 5); len(found) != 1 || found[0].Location == nil || found[0].Location.First != 1 {
		t.Errorf("found %+v", found)
	}
	// Started again, it finds its index where it left it.
	again, _ := run(t, computer)
	if now := again.status("shared-computer")[book.ID]; now.State != plugincontract.StateIndexed || now.Hash != book.Hash {
		t.Errorf("a study-shelf started again says %+v", now)
	}
}

// whole checks, through a Shelf that was started after another was stopped,
// that a Source is in the index all of it or not at all: listed as indexed,
// with every one of its pages found, or not listed, with none.
func whole(t *testing.T, s *session, collection string, book plugincontract.Source, pages int, word string) (indexed bool) {
	t.Helper()
	status, listed := s.status(collection)[book.ID]
	if listed && status.State != plugincontract.StateIndexed {
		t.Fatalf("a Shelf that was just started says %+v of a Source nobody asked it to index", status)
	}
	found := 0
	for _, n := range []int{1, pages / 4, pages / 2, pages - 1, pages} {
		w := fmt.Sprintf("%s%d", word, n)
		found += len(holding(s.search(collection, w, 5, book.ID), w+" "))
	}
	switch {
	case listed && (status.Hash != book.Hash || found != 5):
		t.Fatalf("the Source is listed as indexed (%+v), and a search finds %d of 5 of its pages: half a Source is found as if it were whole", status, found)
	case !listed && found != 0:
		t.Fatalf("the Source is not listed, and a search finds %d of its pages", found)
	}
	return listed
}

// integrity has SQLite check a collection's file.
func integrity(t *testing.T, folder, collection string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(folder, collection+".sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		t.Errorf("the integrity check of %s says %q (%v)", collection, result, err)
	}
	var passages, texts int
	if err := db.QueryRow("SELECT (SELECT count(*) FROM passages), (SELECT count(*) FROM passages_text)").Scan(&passages, &texts); err != nil || passages != texts {
		t.Errorf("%s holds %d Passages and %d texts (%v)", collection, passages, texts, err)
	}
}

// study-shelf is killed while it indexes a book, as a kill -9 or a power cut
// would kill it, at several moments: before it has read the book, while it
// reads it, and while it writes it into the index. What was indexed before
// is there for the next study-shelf, the book is there whole or not at all,
// and indexed again it is whole.
func TestAStudyShelfThatIsKilledLeavesItsIndexWhole(t *testing.T) {
	const pages = 1500
	computer := plugintest.NewComputer(t)
	folder := computer.DataFolder("conformance")
	kept := file(t, "kept.a1", pdf([]string{"A book that was indexed before speaks of determinants."}))
	book := file(t, "long.b2", long(pages, "page"))

	s, _ := run(t, computer)
	s.indexed("crash", kept)

	indexedOnce := false
	// How far the indexing is when the kill comes: at once, in the reading,
	// and at its end, which is where the writing begins.
	for round, until := range []float64{-1, 0.4, 0.95, 0.95} {
		s, command := run(t, computer)
		if whole(t, s, "crash", book, pages, "page") {
			// A kill came after the book was in the index. From nothing
			// again: under another id the same book is new.
			indexedOnce = true
			book.ID = fmt.Sprintf("long.c%d", round)
		}
		s.index("crash", book)
		for deadline := time.Now().Add(patience); until >= 0 && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			now, listed := s.status("crash")[book.ID]
			if !listed || now.State != plugincontract.StateIndexing || (now.Progress != nil && *now.Progress >= until) {
				break
			}
		}
		command.Kill()
		// The kill is asked for, and the program is gone a moment later:
		// until then it still holds its lock, and the next study-shelf
		// would rightly say that the book is being indexed.
		command.Stop()
		integrity(t, folder, "crash")

		next, _ := run(t, computer)
		if now := next.status("crash")[kept.ID]; now.State != plugincontract.StateIndexed || now.Hash != kept.Hash {
			t.Fatalf("round %d: after study-shelf was killed, the next says %+v of a Source that was indexed before", round, now)
		}
		if found := next.search("crash", "determinants", 5, kept.ID); len(found) != 1 {
			t.Fatalf("round %d: after study-shelf was killed, what was indexed before is no longer found", round)
		}
		whole(t, next, "crash", book, pages, "page")
	}
	// Asked again, the book is indexed, all of it.
	s, _ = run(t, computer)
	s.indexed("crash", book)
	if !whole(t, s, "crash", book, pages, "page") {
		t.Error("indexed again after the kills, the book is not in the index")
	}
	integrity(t, folder, "crash")
	if indexedOnce {
		t.Log("one kill came after the book was written: the index held it whole")
	}
}

// study keeps no plugin running: it starts a study-shelf for every question
// it has, a glance at what is indexed among them, while the study-shelf that
// a study source index started in another terminal is at work. The one that
// is asked says what the other is doing: that the Source is being indexed,
// how far, and what a search still finds of it meanwhile. And once the other
// was killed, it says so no longer: a Source that was being indexed by a
// program that is gone is not being indexed.
func TestAGlanceSeesWhatAnotherStudyShelfIsIndexing(t *testing.T) {
	const pages = 3000
	computer := plugintest.NewComputer(t)
	earlier := file(t, "book.a1", pdf([]string{"The first edition speaks of spectra."}))
	later := earlier
	data := long(pages, "leaf")
	later.Path = filepath.Join(t.TempDir(), "second-edition.pdf")
	if err := os.WriteFile(later.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	later.Hash = hashOf(data)
	never := file(t, "new.b2", long(pages, "sheet"))

	indexer, command := run(t, computer)
	indexer.indexed("glanced", earlier)
	// New content of the book is asked for.
	indexer.index("glanced", later)

	// Glances, each a study-shelf of its own, until one has seen the work
	// say how far it is.
	var seen plugincontract.SourceStatus
	for deadline := time.Now().Add(patience); seen.Progress == nil || *seen.Progress == 0; {
		glance, stop := run(t, computer)
		status := glance.status("glanced")
		now, listed := status[earlier.ID]
		if !listed || now.State != plugincontract.StateIndexing {
			t.Fatalf("a glance says %+v (listed: %v) of a Source another study-shelf is indexing; want it being indexed", now, listed)
		}
		// What a search finds meanwhile is the earlier content, and the
		// glance gives its hash.
		if now.Hash != earlier.Hash {
			t.Fatalf("a glance gives the hash %q for a Source whose earlier content is still found, want %s", now.Hash, earlier.Hash)
		}
		if found := glance.search("glanced", "spectra", 5); len(found) != 1 {
			t.Fatalf("while another study-shelf indexes new content, a glance finds the earlier content %d times", len(found))
		}
		// It is not this one's to stop.
		if glance.cancel("glanced", earlier.ID) {
			t.Fatal("a study-shelf says it stopped what another is indexing")
		}
		seen = now
		stop.Stop()
		if time.Now().After(deadline) {
			t.Fatalf("no glance saw how far the indexing is: the last said %+v", now)
		}
	}
	if *seen.Progress <= 0 || *seen.Progress >= 1 {
		t.Errorf("a glance says the work is %v done", *seen.Progress)
	}
	// A Source that was never indexed is asked for while the book is being
	// read: it waits for the one reader, and a glance says it is being
	// indexed too, with no hash, since nothing of it is found yet. It is
	// asked for only now, with the reader taken: asked for at once, it
	// could be the one to get the reader, and be indexed before a glance
	// looked.
	indexer.index("glanced", never)
	waiting, stop := run(t, computer)
	if other, listed := waiting.status("glanced")[never.ID]; !listed || other.State != plugincontract.StateIndexing || other.Hash != "" {
		t.Fatalf("a glance says %+v (listed: %v) of a Source another study-shelf is about to index", other, listed)
	}
	stop.Stop()

	// The one that indexes is killed. Once it is gone, no Source is being
	// indexed.
	command.Kill()
	command.Stop()
	glance, _ := run(t, computer)
	status := glance.status("glanced")
	if now := status[earlier.ID]; now.State != plugincontract.StateIndexed || now.Hash != earlier.Hash {
		t.Errorf("after the study-shelf that indexed was killed, a glance says %+v of the Source; want its earlier content indexed", now)
	}
	if now, listed := status[never.ID]; listed {
		t.Errorf("after the study-shelf that indexed was killed, a glance says %+v of a Source that never was indexed; want nothing", now)
	}
	// Asked, this one indexes them, and says so itself.
	glance.indexed("glanced", later)
	glance.indexed("glanced", never)
	if found := glance.search("glanced", "leaf3000 sheet3000", 5); len(found) != 2 {
		t.Errorf("indexed after the kill, a search finds %d of the two last pages", len(found))
	}
	integrity(t, computer.DataFolder("conformance"), "glanced")
}

// Two study-shelf programs on one folder, a glance beside a study source
// index or two of either: each finds what the other indexed, and one
// searches while the other indexes.
func TestTwoStudyShelvesShareOneIndex(t *testing.T) {
	computer := plugintest.NewComputer(t)
	searcher, _ := run(t, computer)
	indexer, _ := run(t, computer)

	first := file(t, "first.a1", pdf([]string{"The first book speaks of homology."}))
	indexer.indexed("shared", first)
	if now := searcher.status("shared")[first.ID]; now.State != plugincontract.StateIndexed || now.Hash != first.Hash {
		t.Fatalf("one study-shelf says %+v of what the other indexed", now)
	}
	found := searcher.search("shared", "homology", 5)
	if len(found) != 1 {
		t.Fatalf("one study-shelf does not find what the other indexed: %+v", found)
	}

	// While the one indexes a long book, the other searches, reads a Passage
	// and indexes a book of its own in the same collection.
	book := file(t, "long.b2", long(1500, "sheet"))
	indexer.index("shared", book)
	second := file(t, "second.c3", pdf([]string{"The second book speaks of cohomology."}))
	searcher.index("shared", second)
	searches := 0
	for {
		if during := searcher.search("shared", "homology", 5, first.ID); len(during) != 1 || during[0].ID != found[0].ID {
			t.Fatalf("while the other study-shelf indexes, a search finds %+v", during)
		}
		if again, ok := searcher.passage("shared", found[0].ID); !ok || again.Text != found[0].Text {
			t.Fatalf("while the other study-shelf indexes, Passage %s is %+v (found: %v)", found[0].ID, again, ok)
		}
		searches++
		if now, listed := indexer.status("shared")[book.ID]; listed && now.State != plugincontract.StateIndexing {
			break
		}
	}
	if done, _ := searcher.settled("shared", second.ID); done.State != plugincontract.StateIndexed {
		t.Fatalf("the book one study-shelf indexed while the other was indexing: %+v", done)
	}
	t.Logf("%d searches were answered while the other study-shelf indexed", searches)

	// Both wrote, and a third finds all of it.
	third, _ := run(t, computer)
	status := third.status("shared")
	for _, source := range []plugincontract.Source{first, book, second} {
		if now := status[source.ID]; now.State != plugincontract.StateIndexed || now.Hash != source.Hash {
			t.Errorf("a third study-shelf says %+v of %s", now, source.ID)
		}
	}
	for word, want := range map[string]string{"homology": first.ID, "cohomology": second.ID, "sheet1500": book.ID} {
		if found := third.search("shared", word, 5); len(found) != 1 || found[0].Source != want {
			t.Errorf("a third study-shelf, searching for %s, finds %+v", word, found)
		}
	}
	integrity(t, computer.DataFolder("conformance"), "shared")
}

// A session that ends for a reason study did not give is said, last on
// standard error, where study looks for the reason, and the program does not
// exit as if nothing had happened. A session that study ends, by closing
// the program's input, ends without a word.
func TestAStudyShelfSaysWhyItsSessionEnded(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		code  int
		says  string
	}{
		"what it is sent is no MCP message": {"this is no MCP message\n", 1, "study-shelf: the session ended:"},
		"its input is closed":               {"", 0, ""},
	} {
		cmd := exec.Command(shelftest.Shelf(t))
		cmd.Env = []string{plugincontract.DataFolderEnv + "=" + t.TempDir()}
		cmd.Stdin = strings.NewReader(tc.input)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if code != tc.code || !strings.Contains(stderr.String(), tc.says) || (tc.says == "" && stderr.Len() != 0) {
			t.Errorf("%s: study-shelf exited with %d and said %q; want %d and %q", name, code, stderr.String(), tc.code, tc.says)
		}
	}
}
