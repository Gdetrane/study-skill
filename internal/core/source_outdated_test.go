package core_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// A plugin says of a Source that it is outdated when it holds the Source's
// content and would index it otherwise now, its settings having changed:
// Shelf says so of a book that was indexed before it was given an embedding
// model. Such a Source is still found, so a search covers it and a quote is
// checked against its Passages. It is shown with the plugin's reason, and
// study source index is what to do next: it asks for the Source again,
// without being told to ask for what is indexed.
func TestASourceThatIsOutdatedIsFoundAndIndexedAgain(t *testing.T) {
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	const reason = "its Passages were indexed before an embedding model was set"
	// What the plugin says of the Source from here on, whatever it holds.
	s.registerWith(t, "--result", `status={"sources":[{"id":"`+strang.ID+`","state":"outdated","hash":"`+strang.Hash+`","reason":"`+reason+`"}]}`)

	shown := func(t *testing.T, action string) {
		t.Helper()
		want := core.SourceIndex{State: core.IndexOutdated, Reason: reason}
		// A listing does not name itself as what to do next.
		listed := action
		if strings.HasPrefix(action, "study source list") {
			listed = ""
		}
		list := s.sources(t)
		if len(list.Sources) != 1 || list.Sources[0].Indexed == nil || *list.Sources[0].Indexed != want ||
			list.Indexing == nil || list.Indexing.Action != listed {
			t.Errorf("a listing says %+v, with %+v; want the Source outdated, with the plugin's reason, and the action %q",
				list.Sources[0].Indexed, list.Indexing, listed)
		}
		x := indexingOf(t, s.k, "c")
		if x == nil || x.Action != action || len(x.Sources) != 1 || x.Sources[0].SourceIndex != want {
			t.Errorf("status says %+v; want the Source outdated, and the action %q", x, action)
		}
	}
	shown(t, "study source index c")

	// It is found as it is: searched, with nothing left out and nothing to
	// run for what a search left out, and a quote is checked against it.
	res := s.search(t, core.PassageQuery{Query: "elimination produces triangular"})
	if !slices.Equal(res.Searched, []string{strang.ID}) || len(res.Passages) == 0 || len(res.LeftOut) != 0 || res.Note != "" || res.Action != "" {
		t.Errorf("SearchEvidence = %+v; want the Source searched, and nothing said of what was left out", res)
	}
	got, err := s.k.RecordEvidence(t.Context(), core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID,
		Quote: "Elimination produces an upper triangular system.", Passages: []string{res.Passages[0].ID}})
	if err != nil || !got.Evidence.Checked {
		t.Errorf("RecordEvidence = %+v, %v; want the quote checked against the Passage", got.Evidence, err)
	}

	// The command asks for it again, as for a Source that is not indexed: a
	// dry run says so, and the real run asks the plugin.
	dry := s.index(t, core.IndexSpec{DryRun: true})
	if len(dry.Sources) != 1 || !dry.Sources[0].Asked || dry.Sources[0].State != core.IndexOutdated || dry.Sources[0].Reason != reason {
		t.Errorf("a dry run says %+v; want the Source asked for again", dry.Sources)
	}
	before := len(s.calls(t))
	s.index(t, core.IndexSpec{})
	indexed := asked(s.calls(t)[before:], "index")
	if len(indexed) != 1 || indexed[0].Source() != strang.ID {
		t.Errorf("study source index asked the plugin to index %v; want the Source that is outdated, once", plugintest.Tools(s.calls(t)[before:]))
	}

	// With its file gone it cannot be handed over: it stays as it is, with
	// the plugin's reason, still found, and the listing is what says where
	// the file went.
	if err := os.Remove(filepath.Join(s.Home, "Books", "strang.txt")); err != nil {
		t.Fatal(err)
	}
	shown(t, "study source list c")
	before = len(s.calls(t))
	gone := s.index(t, core.IndexSpec{})
	if len(gone.Sources) != 1 || gone.Sources[0].Asked || !strings.Contains(gone.Sources[0].Skipped, "not on this computer") ||
		len(asked(s.calls(t)[before:], "index")) != 0 {
		t.Errorf("with its file gone, study source index = %+v; want the Source skipped, and the plugin not asked", gone.Sources)
	}
	if res := s.search(t, core.PassageQuery{Query: "elimination"}); !slices.Equal(res.Searched, []string{strang.ID}) || len(res.Passages) == 0 {
		t.Errorf("with its file gone, SearchEvidence = %+v; want the Source still searched", res)
	}
}

// A Source the plugin calls outdated while holding other content than the
// Source's is not indexed: a Source is its content, whatever state the
// plugin gives.
func TestAnOutdatedSourceOfOtherContentIsNotIndexed(t *testing.T) {
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	other := "sha256:" + strings.Repeat("0", 64)
	s.registerWith(t, "--result", `status={"sources":[{"id":"`+strang.ID+`","state":"outdated","hash":"`+other+`","reason":"no vectors"}]}`)
	list := s.sources(t)
	if x := list.Sources[0].Indexed; x == nil || x.State != core.IndexNotIndexed || !strings.Contains(x.Reason, "holds other content") {
		t.Errorf("a listing says %+v; want it not indexed, for the content the plugin holds", x)
	}
	if res := s.search(t, core.PassageQuery{Query: "elimination"}); len(res.Searched) != 0 || len(res.LeftOut) != 1 || res.Action != "study source index c" {
		t.Errorf("SearchEvidence = %+v; want the Source left out, and the command that indexes it", res)
	}
}
