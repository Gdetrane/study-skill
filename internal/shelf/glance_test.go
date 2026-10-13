//go:build unix

package shelf_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// study keeps no plugin running. A glance, study status or the agent's
// sources tool, starts Shelf, asks which contract it speaks and what is
// indexed, and stops it, all within two seconds, and gives it a quarter of a
// second to exit once its input has closed. So Shelf does nothing slow when
// it starts: the PDF reader is compiled when a PDF is first indexed, and no
// collection is opened before it is asked about.
//
// Nor does it ask its endpoints anything, to start or to say what is
// indexed: a model that has to be loaded takes longer than a glance has,
// and what is indexed is in the index. The study-shelf here has both
// endpoints in its settings file, as a learner's has.
func TestAGlanceIsQuick(t *testing.T) {
	computer := plugintest.NewComputer(t)
	at := shelftest.NewEndpoint(t)
	writeSettings(t, computer, at, "embeddinggemma-2", true)
	// A collection with a book in it, and one that failed, as a Topic has.
	s, _ := run(t, computer)
	book := file(t, "book.a1", pdf([]string{"A book that is indexed speaks of adjoint functors."}))
	s.indexed("glanced-at", book)
	s.fails("glanced-at", file(t, "notes.b2", []byte("no PDF at all")), "not a PDF")
	// What study leaves in the folder for a moment before it indexes: no
	// collection, and nothing of Shelf's.
	check := filepath.Join(computer.DataFolder("conformance"), ".lamplight-write-check-0a1b2c3d")
	if err := os.WriteFile(check, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The book was indexed with its vectors, so the endpoint is the one the
	// settings name and is reached. From here on it is asked nothing.
	if asked := at.AskedAt("/v1/embeddings"); len(asked) != 1 || asked[0].Body["model"] != "google/embeddinggemma-2" {
		t.Fatalf("indexing the book, study-shelf asked the embedding endpoint %+v; want its one Passage, once", asked)
	}
	at.Forget()

	var slowest, slowestExit time.Duration
	for range 5 {
		began := time.Now()
		link := plugintest.AsCommand(computer, shelftest.Shelf(t))(t)
		glance := dial(t, link.Transport)
		var contract plugincontract.Contract
		glance.ok(plugincontract.ToolContract, struct{}{}, &contract)
		if fmt.Sprint(contract.Search) != "[keyword embedding]" {
			t.Errorf("a glance is told that Shelf searches by %v", contract.Search)
		}
		status := glance.status("glanced-at")
		answered := time.Since(began)
		onItsOwn := link.Command.Stop()
		exit := time.Since(began) - answered

		if now := status[book.ID]; now.State != plugincontract.StateIndexed || now.Hash != book.Hash {
			t.Errorf("a glance says %+v of the book", now)
		}
		if now := status["notes.b2"]; now.State != plugincontract.StateFailed || now.Reason == "" {
			t.Errorf("a glance says %+v of the Source that failed", now)
		}
		if len(status) != 2 {
			t.Errorf("a glance lists %d Sources, want the two that were asked for", len(status))
		}
		if !onItsOwn {
			t.Errorf("study-shelf did not exit when its input closed")
		}
		slowest, slowestExit = max(slowest, answered), max(slowestExit, exit)
	}
	t.Logf("a glance: started, contract and status answered in %s at most, and exited in %s at most", slowest, slowestExit)
	if asked := at.Asked(); len(asked) != 0 {
		t.Errorf("to start and to say what is indexed, study-shelf asked its endpoints %+v: a glance asks them nothing", asked)
	}
	// Compiling the PDF reader alone takes longer than this.
	if slowest > 1500*time.Millisecond {
		t.Errorf("a glance took %s: study gives a plugin 2 seconds to start and to answer contract and status", slowest)
	}
	if slowestExit > 250*time.Millisecond {
		t.Errorf("study-shelf took %s to exit once its input had closed: study gives it 250 ms after a glance", slowestExit)
	}
	if _, err := os.Stat(check); err != nil {
		t.Errorf("Shelf removed, or moved, a file of study's in its folder: %v", err)
	}
}
