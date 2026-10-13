package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// elimination is a book of two pages and three paragraphs, which the test
// plugin makes three Passages of: two on the first page, and one on the
// second whose text is broken over two lines.
const elimination = "Elimination produces an upper triangular system.\n\n" +
	"The pivots are on the diagonal of the triangle after elimination.\f" +
	"Back substitution solves the triangular\nsystem from the last equation up.\n"

var passageID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// searched is what a search call asked the plugin.
func searched(t *testing.T, call plugintest.Call) plugincontract.SearchInput {
	t.Helper()
	var in plugincontract.SearchInput
	if err := json.Unmarshal(call.Args, &in); call.Tool != "search" || err != nil {
		t.Fatalf("the call is no search: %s %s (%v)", call.Tool, call.Args, err)
	}
	return in
}

// passageAsked is the id of the Passage a passage call asked the plugin for.
func passageAsked(t *testing.T, call plugintest.Call) string {
	t.Helper()
	var in plugincontract.PassageInput
	if err := json.Unmarshal(call.Args, &in); call.Tool != "passage" || err != nil {
		t.Fatalf("the call is no passage: %s %s (%v)", call.Tool, call.Args, err)
	}
	return in.ID
}

func (s *shelf) search(t *testing.T, q core.PassageQuery) core.PassageSearch {
	t.Helper()
	q.Topic = "c"
	res, err := s.k.SearchEvidence(context.Background(), q)
	if err != nil {
		t.Fatalf("SearchEvidence(%+v): %v", q, err)
	}
	return res
}

// newCalls are the tools the plugin was asked since it had received before
// calls, and those calls.
func (s *shelf) newCalls(t *testing.T, before int) ([]string, []plugintest.Call) {
	t.Helper()
	calls := s.calls(t)[before:]
	return plugintest.Tools(calls), calls
}

// A search asks the Topic's plugin for the Passages of the Topic's Sources
// that answer a query, and returns them best first, each with what Evidence
// is recorded with. It starts the plugin, asks what it has indexed and then
// to search, stops it, and records nothing.
func TestASearchReturnsThePassagesOfATopicsSources(t *testing.T) {
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	axler := s.book(t, "axler.txt", "Eigenvalues come first in this book.\n\nNo determinants are used.\n")
	s.index(t, core.IndexSpec{})
	topic := filepath.Join(s.StudyHome, "c")
	history, files := readFile(t, topic, "history.jsonl"), tree(t, topic)

	before := len(s.calls(t))
	res := s.search(t, core.PassageQuery{Query: "  elimination produces triangular\n"})
	if res.Topic != "c" || res.Query != "elimination produces triangular" || res.Plugin != "kb" ||
		!slices.Equal(res.Searched, []string{strang.ID, axler.ID}) || res.LeftOut != nil || res.Note != "" || res.Action != "" {
		t.Errorf("SearchEvidence = %+v; want both Sources searched, and nothing left out", res)
	}
	want := []core.FoundPassage{
		{Source: strang.ID, Title: strang.Title, Text: "Elimination produces an upper triangular system.", Location: "p. 1", Score: 3},
		{Source: strang.ID, Title: strang.Title, Text: "The pivots are on the diagonal of the triangle after elimination.", Location: "p. 1", Score: 1},
		{Source: strang.ID, Title: strang.Title, Text: "Back substitution solves the triangular\nsystem from the last equation up.", Location: "p. 2", Score: 1},
	}
	if len(res.Passages) != len(want) {
		t.Fatalf("SearchEvidence found %+v; want %d Passages", res.Passages, len(want))
	}
	ids := map[string]bool{}
	for i, got := range res.Passages {
		if !passageID.MatchString(got.ID) || ids[got.ID] {
			t.Errorf("Passage %d has the id %q; want one of its own, as the plugin gives them", i, got.ID)
		}
		ids[got.ID] = true
		got.ID = ""
		if got != want[i] {
			t.Errorf("Passage %d = %+v\nwant %+v", i, got, want[i])
		}
	}
	// The plugin was asked which contract it speaks, what it has indexed,
	// and then to search the Topic's Sources, by their ids.
	tools, calls := s.newCalls(t, before)
	if !slices.Equal(tools, []string{"contract", "status", "search"}) {
		t.Fatalf("the search asked the plugin %v", tools)
	}
	if got := searched(t, calls[2]); !collectionOfC.MatchString(got.Collection) || got.Collection != calls[1].Collection() ||
		got.Query != "elimination produces triangular" || got.Limit != core.DefaultPassageLimit ||
		!slices.Equal(got.Sources, []string{strang.ID, axler.ID}) {
		t.Errorf("the plugin was asked to search %+v", got)
	}
	s.stopped(t)
	// A search is no Event, and nothing of the Topic's.
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("searching recorded Events:\n%s", strings.TrimPrefix(after, history))
	}
	if after := tree(t, topic); !reflect.DeepEqual(after, files) {
		t.Errorf("searching changed the Topic's files: %v, were %v", after, files)
	}

	// A limit, and the Sources to search, are the plugin's to keep to.
	before = len(s.calls(t))
	one := s.search(t, core.PassageQuery{Query: "elimination", Limit: 1, Sources: []string{strang.ID, strang.ID}})
	if len(one.Passages) != 1 || !slices.Equal(one.Searched, []string{strang.ID}) {
		t.Errorf("a search of one Source for one Passage = %+v", one)
	}
	if _, calls := s.newCalls(t, before); len(calls) != 3 || searched(t, calls[2]).Limit != 1 ||
		!slices.Equal(searched(t, calls[2]).Sources, []string{strang.ID}) {
		t.Errorf("the plugin was asked %v", calls)
	}
	before = len(s.calls(t))
	s.search(t, core.PassageQuery{Query: "elimination", Limit: 5000})
	if _, calls := s.newCalls(t, before); len(calls) != 3 || searched(t, calls[2]).Limit != core.MaxPassageLimit {
		t.Errorf("a search for 5,000 Passages asked the plugin %v; want the most a search may ask for", calls)
	}
	// Nothing found is an answer: no Passages, of the Sources searched.
	if none := s.search(t, core.PassageQuery{Query: "zebra"}); none.Passages == nil || len(none.Passages) != 0 ||
		len(none.Searched) != 2 || none.Note != "" {
		t.Errorf("a search that finds nothing = %+v", none)
	}
}

// Only the Sources the plugin holds indexed are searched. The others are
// left out, and the result says which, why, and the command that has them
// indexed. With nothing indexed, and with no Sources at all, nothing is
// searched, which is no error.
func TestASearchLeavesOutWhatIsNotIndexed(t *testing.T) {
	s := newShelf(t)
	// A Topic without Sources asks no plugin anything.
	empty := s.search(t, core.PassageQuery{Query: "elimination"})
	if len(empty.Passages) != 0 || empty.Passages == nil || empty.Searched == nil || len(empty.Searched) != 0 ||
		!strings.Contains(empty.Note, "Topic c has no Sources yet") || empty.Action != "" {
		t.Errorf("a search of a Topic without Sources = %+v", empty)
	}
	if calls := s.calls(t); len(calls) != 0 {
		t.Errorf("a search of a Topic without Sources asked the plugin %v", plugintest.Tools(calls))
	}

	strang := s.book(t, "strang.txt", elimination)
	blank := s.book(t, "blank.txt", " \n\n ")
	// Nothing is indexed: the plugin is asked what it holds, and not to
	// search.
	nothing := s.search(t, core.PassageQuery{Query: "elimination"})
	wantLeft := []core.IndexedSource{
		{ID: strang.ID, Title: strang.Title, SourceIndex: core.SourceIndex{State: core.IndexNotIndexed}},
		{ID: blank.ID, Title: blank.Title, SourceIndex: core.SourceIndex{State: core.IndexNotIndexed}},
	}
	if len(nothing.Passages) != 0 || len(nothing.Searched) != 0 || !reflect.DeepEqual(nothing.LeftOut, wantLeft) ||
		nothing.Note != "nothing was searched: Knowledge base plugin kb holds none of the 2 Sources indexed on this computer as the Topic records them" ||
		nothing.Action != "study source index c" {
		t.Errorf("a search with nothing indexed = %+v", nothing)
	}
	if tools := plugintest.Tools(s.calls(t)); !slices.Equal(tools, []string{"contract", "status"}) {
		t.Errorf("a search with nothing indexed asked the plugin %v; want what it holds, and no search", tools)
	}

	// One is indexed, and the other failed: the first is searched, and the
	// plugin is asked to search it alone. The one that failed is left out
	// as a listing of the Sources shows it, with the plugin's reason and the
	// command that has it tried again, and is no next action: indexing it
	// again would fail again, after every search.
	s.index(t, core.IndexSpec{})
	before := len(s.calls(t))
	some := s.search(t, core.PassageQuery{Query: "elimination"})
	wantLeft = []core.IndexedSource{{ID: blank.ID, Title: blank.Title,
		SourceIndex: core.SourceIndex{State: core.IndexFailed, Reason: "there is no text in it", Retry: "study source index c " + blank.ID}}}
	if len(some.Passages) != 2 || !slices.Equal(some.Searched, []string{strang.ID}) || !reflect.DeepEqual(some.LeftOut, wantLeft) ||
		some.Note != "1 of the 2 Sources was not searched: Knowledge base plugin kb does not hold it indexed on this computer as the Topic records it" ||
		some.Action != "" {
		t.Errorf("a search with one Source of two indexed = %+v", some)
	}
	tools, calls := s.newCalls(t, before)
	if !slices.Equal(tools, []string{"contract", "status", "search"}) || !slices.Equal(searched(t, calls[2]).Sources, []string{strang.ID}) {
		t.Errorf("the plugin was asked %v", calls)
	}
	if list := s.sources(t); list.Indexing == nil || list.Indexing.Action != some.Action {
		t.Errorf("the search names the action %q, and a listing of the Sources %+v", some.Action, list.Indexing)
	}
	// Asked for the Source that is not indexed alone, nothing is searched.
	only := s.search(t, core.PassageQuery{Query: "elimination", Sources: []string{blank.ID}})
	if len(only.Passages) != 0 || len(only.Searched) != 0 || len(only.LeftOut) != 1 ||
		only.Note != "nothing was searched: Knowledge base plugin kb does not hold the Source indexed on this computer as the Topic records it" {
		t.Errorf("a search of the one Source that is not indexed = %+v", only)
	}
	s.stopped(t)
}

// storedSource is a Source as the test plugin keeps it in its folder.
func storedSource(s *shelf, collection, source string) string {
	return filepath.Join(s.DataFolder("kb"), collection, source+".json")
}

// A Topic that is removed and another made later under its id share a
// collection, and the removed Topic's Sources are still in it. A search of
// the later Topic is limited to the Sources it has, so it finds nothing of
// the removed one's, and a Passage of the removed one's is no Passage to
// record Evidence with.
func TestASearchFindsNothingOfARemovedTopicsSources(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	old := s.book(t, "old.txt", "The removed Topic spoke of zanzibar.\n")
	indexed := s.index(t, core.IndexSpec{})
	found := s.search(t, core.PassageQuery{Query: "zanzibar"})
	if len(found.Passages) != 1 {
		t.Fatalf("the first Topic's search found %+v", found.Passages)
	}
	if _, err := s.k.RemoveTopic(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	namedTopic(t, s.k, "c", "kb")
	later := s.book(t, "later.txt", "The later Topic speaks of elimination.\n")
	if again := s.index(t, core.IndexSpec{}); again.Collection != indexed.Collection {
		t.Fatalf("the later Topic's collection is %s, and the removed one's was %s: they no longer share one, so this test proves nothing",
			again.Collection, indexed.Collection)
	}
	// The plugin still holds the removed Topic's Source, in that collection.
	if _, err := os.Stat(storedSource(s, indexed.Collection, old.ID)); err != nil {
		t.Fatalf("the plugin no longer holds the removed Topic's Source, so this test proves nothing: %v", err)
	}

	before := len(s.calls(t))
	res := s.search(t, core.PassageQuery{Query: "zanzibar elimination"})
	if len(res.Passages) != 1 || res.Passages[0].Source != later.ID || !slices.Equal(res.Searched, []string{later.ID}) {
		t.Errorf("the later Topic's search = %+v; want the one Passage of its own Source", res)
	}
	if _, calls := s.newCalls(t, before); len(calls) != 3 || !slices.Equal(searched(t, calls[2]).Sources, []string{later.ID}) {
		t.Errorf("the plugin was asked %v; want a search limited to the later Topic's Source", calls)
	}
	// Evidence of the later Topic cannot quote the removed Topic's Passage.
	history := readFile(t, filepath.Join(s.StudyHome, "c"), "history.jsonl")
	_, err := s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "intro", Source: later.ID,
		Quote: "The removed Topic spoke of zanzibar.", Passages: []string{found.Passages[0].ID}})
	if core.CodeOf(err) != core.CodeInvalidArgument || !strings.Contains(err.Error(), "is of Source "+old.ID) {
		t.Errorf("Evidence quoted from the removed Topic's Passage = %v (%s); want it refused as a Passage of another Source", err, core.CodeOf(err))
	}
	if after := readFile(t, filepath.Join(s.StudyHome, "c"), "history.jsonl"); after != history {
		t.Errorf("the refused Evidence was recorded:\n%s", strings.TrimPrefix(after, history))
	}
}

// A plugin that answers a search with a Passage of a Source it was not
// asked for breaks the contract: its whole answer is refused, and no
// Passage of it reaches whoever searched.
func TestASearchRefusesAPassageOfASourceItDidNotAskFor(t *testing.T) {
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	s.registerWith(t, "--result", `search={"passages":[{"id":"p1","source":"another-book.a1b2c3","text":"A key of the learner's.","score":1}]}`)
	res, err := s.k.SearchEvidence(context.Background(), core.PassageQuery{Topic: "c", Query: "key"})
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not speak Lamplight's contract") ||
		!strings.Contains(err.Error(), "which the search was not limited to") || len(res.Passages) != 0 {
		t.Errorf("SearchEvidence = %+v, %v (%s); want the plugin's answer refused", res, err, core.CodeOf(err))
	}
	if strings.Contains(err.Error(), "A key of the learner's") {
		t.Errorf("the error repeats the Passage's text: %v", err)
	}
	// The plugin was asked to search the Topic's Source, and no other.
	calls := asked(s.calls(t), "search")
	if len(calls) != 1 || !slices.Equal(searched(t, calls[0]).Sources, []string{strang.ID}) {
		t.Errorf("the plugin was asked to search %v", calls)
	}
	s.stopped(t)
}

// A plugin that breaks the contract may hold another file's text under a
// Source's id. It says so through the hash of what it holds, and a Source
// whose hash is not the Topic's is not searched, nor is a quote checked
// against its Passages.
func TestASearchLeavesOutContentTheTopicDoesNotRecord(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	indexed := s.index(t, core.IndexSpec{})
	found := s.search(t, core.PassageQuery{Query: "elimination"})
	if len(found.Passages) == 0 {
		t.Fatal("the search found nothing before the index was changed")
	}
	// The plugin's index comes to hold other content for the Source: a
	// file of the learner's that the Topic never had.
	path := storedSource(s, indexed.Collection, strang.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := json.Unmarshal(data, &kept); err != nil {
		t.Fatal(err)
	}
	kept["hash"] = "sha256:" + strings.Repeat("ab", 32)
	kept["passages"] = []any{map[string]any{"id": found.Passages[0].ID, "text": "PRIVATE KEY of the learner's", "page": 1}}
	if data, err = json.Marshal(kept); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	before := len(s.calls(t))
	res := s.search(t, core.PassageQuery{Query: "private key elimination"})
	if len(res.Passages) != 0 || len(res.Searched) != 0 || len(res.LeftOut) != 1 || res.LeftOut[0].State != core.IndexNotIndexed ||
		!strings.Contains(res.LeftOut[0].Reason, "holds other content for it") || res.Action != "study source index c" {
		t.Errorf("a search of a Source whose indexed content is another = %+v; want it left out, and nothing found", res)
	}
	if tools, _ := s.newCalls(t, before); !slices.Equal(tools, []string{"contract", "status"}) {
		t.Errorf("the plugin was asked %v; want no search of content the Topic does not record", tools)
	}
	// Nor is a quote checked against it: the Passage is never asked for.
	before = len(s.calls(t))
	history := readFile(t, filepath.Join(s.StudyHome, "c"), "history.jsonl")
	_, err = s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "intro", Source: strang.ID,
		Quote: "PRIVATE KEY of the learner's", Passages: []string{found.Passages[0].ID}})
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not hold Source "+strang.ID+" indexed") ||
		!strings.Contains(err.Error(), "study source index c "+strang.ID) {
		t.Errorf("Evidence quoted from content the Topic does not record = %v (%s)", err, core.CodeOf(err))
	}
	if tools, _ := s.newCalls(t, before); !slices.Equal(tools, []string{"contract", "status"}) {
		t.Errorf("the plugin was asked %v; want no Passage of content the Topic does not record", tools)
	}
	if after := readFile(t, filepath.Join(s.StudyHome, "c"), "history.jsonl"); after != history {
		t.Errorf("the refused Evidence was recorded:\n%s", strings.TrimPrefix(after, history))
	}
}

// What a search cannot do is an error that says why, and what to do: a
// Topic that names no plugin, a plugin this computer does not have, one
// that does not start, and one that does not answer in its time, which is
// stopped. None is left running.
func TestASearchSaysWhyItCouldNotSearch(t *testing.T) {
	ctx := context.Background()
	const yourself = "read its Sources yourself (study source list "
	t.Run("the Topic names no plugin", func(t *testing.T) {
		s := newShelf(t)
		if _, err := s.k.CreateTopic(ctx, core.TopicSpec{Title: "N", ID: "n"}); err != nil {
			t.Fatal(err)
		}
		addBook(t, s.k, "n", filepath.Join(s.Home, "Books", "n.txt"), elimination)
		_, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "n", Query: "elimination"})
		if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "Topic n names no Knowledge base plugin") ||
			!strings.Contains(err.Error(), yourself+"n shows where they are)") {
			t.Errorf("SearchEvidence = %v (%s)", err, core.CodeOf(err))
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("the plugin of another Topic was asked %v", plugintest.Tools(calls))
		}
	})
	t.Run("the plugin is not registered on this computer", func(t *testing.T) {
		s := newShelf(t)
		namedTopic(t, s.k, "g", "ghost")
		addBook(t, s.k, "g", filepath.Join(s.Home, "Books", "g.txt"), elimination)
		_, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "g", Query: "elimination"})
		if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "ghost, which is not registered on this computer") ||
			!strings.Contains(err.Error(), "study knowledge-base add ghost -- <command>") || !strings.Contains(err.Error(), yourself+"g shows where they are)") {
			t.Errorf("SearchEvidence = %v (%s)", err, core.CodeOf(err))
		}
	})
	t.Run("the plugin does not start", func(t *testing.T) {
		s := newShelf(t)
		s.book(t, "strang.txt", elimination)
		s.registerWith(t, "--exit", "3")
		_, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "elimination"})
		if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "Knowledge base plugin kb stopped before it could") ||
			!strings.Contains(err.Error(), "Meanwhile the Topic works as one without a Knowledge base: "+yourself+"c shows where they are)") {
			t.Errorf("SearchEvidence = %v (%s)", err, core.CodeOf(err))
		}
		s.stopped(t)
	})
	for _, where := range []string{"start", "status", "search"} {
		t.Run("the plugin does not answer in time: "+where, func(t *testing.T) {
			s := newShelf(t)
			s.book(t, "strang.txt", elimination)
			s.index(t, core.IndexSpec{})
			s.registerWith(t, "--hang", where)
			// The plugin has a second for all of it, and never answers.
			k := studyOn(t, s.Computer, "", core.PluginOptions{EvidenceTimeout: time.Second})
			began := time.Now()
			_, err := k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "elimination"})
			if core.CodeOf(err) != core.CodeFailedPrecondition ||
				!strings.Contains(err.Error(), "Knowledge base plugin kb did not answer a search of the Topic's Sources within 1s, starting it included, so study stopped it") ||
				!strings.Contains(err.Error(), "study knowledge-base check kb") || !strings.Contains(err.Error(), yourself+"c shows where they are)") {
				t.Errorf("SearchEvidence = %v (%s)", err, core.CodeOf(err))
			}
			if took := time.Since(began); took > busy {
				t.Errorf("the search took %s over a plugin that had a second", took)
			}
			s.stopped(t)
		})
	}
	t.Run("the command was stopped", func(t *testing.T) {
		s := newShelf(t)
		s.book(t, "strang.txt", elimination)
		stopped, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := s.k.SearchEvidence(stopped, core.PassageQuery{Topic: "c", Query: "elimination"}); core.CodeOf(err) != core.CodeCanceled {
			t.Errorf("SearchEvidence, stopped = %v (%s); want canceled", err, core.CodeOf(err))
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("a search that was stopped asked the plugin %v", plugintest.Tools(calls))
		}
	})
	t.Run("what is asked is not a search", func(t *testing.T) {
		s := newShelf(t)
		strang := s.book(t, "strang.txt", elimination)
		for name, q := range map[string]core.PassageQuery{
			"no query":                {Topic: "c", Query: " \n"},
			"a control character":     {Topic: "c", Query: "elimi\x1bnation"},
			"a bidi override":         {Topic: "c", Query: "elimi\U0000202enation"},
			"a query too long":        {Topic: "c", Query: strings.Repeat("word ", 1000)},
			"a negative limit":        {Topic: "c", Query: "elimination", Limit: -1},
			"an id that is none":      {Topic: "c", Query: "elimination", Sources: []string{"../other"}},
			"a Topic id that is none": {Topic: "../c", Query: "elimination"},
		} {
			if _, err := s.k.SearchEvidence(ctx, q); core.CodeOf(err) != core.CodeInvalidArgument {
				t.Errorf("%s: SearchEvidence = %v (%s); want invalid_argument", name, err, core.CodeOf(err))
			}
		}
		if _, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "elimination", Sources: []string{strang.ID, "no-such-book.a1b2c3"}}); core.CodeOf(err) != core.CodeNotFound {
			t.Errorf("a search of a Source the Topic does not have = %v (%s); want not_found", err, core.CodeOf(err))
		}
		if _, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "nowhere", Query: "elimination"}); core.CodeOf(err) != core.CodeNotFound {
			t.Errorf("a search of a Topic that is not there = %v (%s); want not_found", err, core.CodeOf(err))
		}
		if calls := s.calls(t); len(calls) != 0 {
			t.Errorf("what was refused asked the plugin %v", plugintest.Tools(calls))
		}
	})
}

// lastEventOf is the last Event of a Topic's History: its type and payload.
func lastEventOf(t *testing.T, topic string) (string, map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(readFile(t, topic, "history.jsonl")), "\n")
	var ev struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Type, ev.Data
}

// Evidence recorded with the Passage it quotes is checked: the plugin is
// asked for the Passage again, the quote must be in its text, and the
// location is the Passage's. It is recorded by an Event type of its own, and
// the Evidence says that it was checked, and against what.
func TestEvidenceIsCheckedAgainstItsPassage(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	found := s.search(t, core.PassageQuery{Query: "elimination produces triangular"}).Passages
	if len(found) != 3 {
		t.Fatalf("the search found %+v", found)
	}
	first, second, third := found[0].ID, found[1].ID, found[2].ID
	topic := filepath.Join(s.StudyHome, "c")
	record := func(spec core.EvidenceSpec) (core.EvidenceResult, error) {
		spec.Topic, spec.Lesson, spec.Source = "c", "elimination", strang.ID
		return s.k.RecordEvidence(ctx, spec)
	}

	// A dry run asks the plugin, checks the quote, and records nothing.
	history := readFile(t, topic, "history.jsonl")
	before := len(s.calls(t))
	dry, err := record(core.EvidenceSpec{Quote: "Elimination produces an upper triangular system.", Passages: []string{first}, DryRun: true})
	if err != nil || !dry.Changed || !dry.DryRun || !dry.Evidence.Checked {
		t.Fatalf("a dry run = %+v, %v", dry, err)
	}
	if tools, _ := s.newCalls(t, before); !slices.Equal(tools, []string{"contract", "status", "passage"}) {
		t.Errorf("a dry run asked the plugin %v", tools)
	}
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("a dry run recorded Events:\n%s", strings.TrimPrefix(after, history))
	}

	before = len(s.calls(t))
	res, err := record(core.EvidenceSpec{Quote: "Elimination produces an upper triangular system.", Passages: []string{first}})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	want := core.Evidence{ID: res.Evidence.ID, Lesson: "elimination", Source: strang.ID, Quote: "Elimination produces an upper triangular system.",
		Location: "p. 1", LocationFrom: core.LocationFromKnowledgeBase, Checked: true, Passages: []string{first}, Plugin: "kb",
		Recorded: fixedNow}
	if !res.Changed || res.DryRun || res.Evidence.ID == "" || !reflect.DeepEqual(res.Evidence, want) {
		t.Errorf("RecordEvidence = %+v\nwant %+v", res, want)
	}
	// The dry run said what the real run did.
	dry.Evidence.ID, dry.DryRun = res.Evidence.ID, false
	if !reflect.DeepEqual(dry, res) {
		t.Errorf("the dry run said %+v, and the real run did %+v", dry, res)
	}
	// The plugin was asked what it has indexed, and for that Passage: not
	// to search, and never to index.
	tools, calls := s.newCalls(t, before)
	if !slices.Equal(tools, []string{"contract", "status", "passage"}) || passageAsked(t, calls[2]) != first ||
		!collectionOfC.MatchString(calls[2].Collection()) {
		t.Errorf("recording Evidence asked the plugin %v", calls)
	}
	s.stopped(t)
	// It is an Event of its own type, whose payload names the Passage.
	typ, data := lastEventOf(t, topic)
	wantData := map[string]any{"id": res.Evidence.ID, "lesson": "elimination", "source": strang.ID,
		"quote": "Elimination produces an upper triangular system.", "passages": []any{first}, "plugin": "kb", "location": "p. 1"}
	if typ != "evidence_passages.recorded" || !reflect.DeepEqual(data, wantData) {
		t.Errorf("the Event is %s %v\nwant evidence_passages.recorded %v", typ, data, wantData)
	}
	// Recording it again records nothing, and a dry run says so too.
	for _, dryRun := range []bool{true, false} {
		again, err := record(core.EvidenceSpec{Quote: "Elimination produces an upper triangular system.", Passages: []string{first}, DryRun: dryRun})
		if err != nil || again.Changed || again.Evidence.ID != res.Evidence.ID {
			t.Errorf("recording the same Evidence again (dry run: %v) = %+v, %v", dryRun, again, err)
		}
	}

	// What may differ from the Passage: its line breaks, and a quotation
	// mark's shape. The quote is recorded as it was given.
	broken, err := record(core.EvidenceSpec{Quote: "Back substitution solves the triangular system\nfrom the last equation up.", Passages: []string{third}})
	if err != nil || !broken.Changed || broken.Evidence.Location != "p. 2" ||
		broken.Evidence.Quote != "Back substitution solves the triangular system\nfrom the last equation up." {
		t.Errorf("a quote with other line breaks than the Passage's = %+v, %v", broken, err)
	}

	// A quote that runs from one Passage into the next names both, in
	// order, and its location is from the first page to the last.
	across, err := record(core.EvidenceSpec{Quote: "after elimination. Back substitution solves", Passages: []string{second, third}})
	if err != nil || !across.Changed || across.Evidence.Location != "pp. 1–2" || !slices.Equal(across.Evidence.Passages, []string{second, third}) {
		t.Errorf("a quote that runs through two Passages = %+v, %v", across, err)
	}
	if tools, calls := s.newCalls(t, len(s.calls(t))-4); !slices.Equal(tools, []string{"contract", "status", "passage", "passage"}) ||
		passageAsked(t, calls[2]) != second || passageAsked(t, calls[3]) != third {
		t.Errorf("recording it asked the plugin %v", calls)
	}

	// What is refused records nothing.
	history = readFile(t, topic, "history.jsonl")
	for name, tc := range map[string]struct {
		spec core.EvidenceSpec
		code core.ErrorCode
		says string
	}{
		"a paraphrase": {core.EvidenceSpec{Quote: "Elimination gives an upper triangular system.", Passages: []string{first}},
			core.CodeInvalidArgument, "the quote is not in the text of Passage " + first},
		"another letter case": {core.EvidenceSpec{Quote: "elimination produces an upper triangular system.", Passages: []string{first}},
			core.CodeInvalidArgument, "quote the Passage as the search gave it, never a paraphrase or a summary"},
		"a quote of another Passage": {core.EvidenceSpec{Quote: "The pivots are on the diagonal", Passages: []string{first}},
			core.CodeInvalidArgument, "A quote that runs on into the next Passage names both, in order"},
		"a quote that runs on, with the first Passage alone": {core.EvidenceSpec{Quote: "after elimination. Back substitution solves", Passages: []string{second}},
			core.CodeInvalidArgument, "the quote is not in the text of Passage " + second},
		"a quote that is all in one of two Passages": {core.EvidenceSpec{Quote: "The pivots are on the diagonal", Passages: []string{second, third}},
			core.CodeInvalidArgument, "the quote is all in Passage " + second + ", and does not run through the other Passages named"},
		"the Passages in the other order": {core.EvidenceSpec{Quote: "after elimination. Back substitution solves", Passages: []string{third, second}},
			core.CodeInvalidArgument, "the quote is not in the texts of Passages " + third + ", " + second + " read one after the other"},
		"a location of the agent's": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{first}, Location: "p. 46", LocationFrom: "source"},
			core.CodeInvalidArgument, "takes its location from the Passage"},
		"where a location came from, alone": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{first}, LocationFrom: "knowledge_base"},
			core.CodeInvalidArgument, "takes its location from the Passage"},
		"a Passage the plugin does not have": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{"0123456789abcdef01234567"}},
			core.CodeNotFound, "Knowledge base plugin kb has no Passage 0123456789abcdef01234567 for Topic c"},
		"an id that is no Passage's": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{"../p"}},
			core.CodeInvalidArgument, "is not a Passage's id"},
		"a Passage named twice": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{first, first}},
			core.CodeInvalidArgument, "is named twice"},
		"more Passages than a quote runs through": {core.EvidenceSpec{Quote: "Elimination produces", Passages: []string{"a", "b", "c", "d", "e", "f"}},
			core.CodeInvalidArgument, "at most 5 Passages"},
		"a quote of nothing that is shown": {core.EvidenceSpec{Quote: "\U0000200b\U0000200b", Passages: []string{first}},
			core.CodeInvalidArgument, "a character that is not shown (U+200B)"},
		"a quote with a direction override in it": {core.EvidenceSpec{Quote: "Elimination \U0000202Eproduces\U0000202C", Passages: []string{first}},
			core.CodeInvalidArgument, "a character that is not shown (U+202E)"},
		"a quote with a tag character in it": {core.EvidenceSpec{Quote: "Elimination\U000E0041 produces", Passages: []string{first}},
			core.CodeInvalidArgument, "a character that is not shown (U+E0041)"},
		"a quote of marks alone": {core.EvidenceSpec{Quote: ". , ;", Passages: []string{first}},
			core.CodeInvalidArgument, "the quote holds no word to check"},
		"the end of a word": {core.EvidenceSpec{Quote: "limination produces", Passages: []string{first}},
			core.CodeInvalidArgument, "It begins and ends where a word of the Passage does"},
	} {
		got, err := record(tc.spec)
		if core.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.says) || got.Changed {
			t.Errorf("%s: RecordEvidence = %+v, %v (%s); want %s, saying %q", name, got, err, core.CodeOf(err), tc.code, tc.says)
		}
		// And a dry run says the same.
		tc.spec.DryRun = true
		if _, dryErr := record(tc.spec); core.CodeOf(dryErr) != tc.code {
			t.Errorf("%s: a dry run = %v (%s); want %s, as the real run", name, dryErr, core.CodeOf(dryErr), tc.code)
		}
	}
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("what was refused recorded Events:\n%s", strings.TrimPrefix(after, history))
	}

	// Evidence without a Passage is recorded as ever: nothing is checked,
	// and no plugin is asked anything.
	before = len(s.calls(t))
	plain, err := record(core.EvidenceSpec{Quote: "A sentence nobody checked.", Location: "p. 46", LocationFrom: core.LocationFromSource})
	if err != nil || !plain.Changed || plain.Evidence.Checked || plain.Evidence.Passages != nil || plain.Evidence.Plugin != "" {
		t.Errorf("Evidence without a Passage = %+v, %v", plain, err)
	}
	if tools, _ := s.newCalls(t, before); len(tools) != 0 {
		t.Errorf("Evidence without a Passage asked the plugin %v", tools)
	}
	if typ, _ := lastEventOf(t, topic); typ != "evidence.recorded" {
		t.Errorf("Evidence without a Passage is recorded by %s", typ)
	}
	// The same quote, checked and not, are two pieces of Evidence.
	same, err := record(core.EvidenceSpec{Quote: "Elimination produces an upper triangular system."})
	if err != nil || !same.Changed || same.Evidence.Checked || same.Evidence.ID == res.Evidence.ID {
		t.Errorf("the checked quote recorded again without its Passage = %+v, %v", same, err)
	}

	// The listing says which Evidence was checked.
	list, err := s.k.ListEvidence(ctx, core.EvidenceQuery{Topic: "c"})
	if err != nil {
		t.Fatal(err)
	}
	var checked []bool
	for _, e := range list.Evidence {
		checked = append(checked, e.Checked)
	}
	if !slices.Equal(checked, []bool{true, true, true, false, false}) || !reflect.DeepEqual(list.Evidence[0], want) {
		t.Errorf("ListEvidence says checked %v, and the first is %+v", checked, list.Evidence[0])
	}
	// Checked Evidence is retracted as any other.
	if gone, err := s.k.RetractEvidence(ctx, "c", res.Evidence.ID, false); err != nil || !gone.Changed || !gone.Evidence.Retracted || !gone.Evidence.Checked {
		t.Errorf("RetractEvidence = %+v, %v", gone, err)
	}
}

// Evidence cannot be checked without the plugin: a Topic that names none, a
// plugin that is gone or does not answer, and a Source that is not indexed
// each give an error, and nothing is recorded.
func TestEvidenceIsNotRecordedWhenItCannotBeChecked(t *testing.T) {
	ctx := context.Background()
	const unchecked = "record the Evidence without a Passage, which is then not checked"
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	first := s.search(t, core.PassageQuery{Query: "elimination produces triangular"}).Passages[0].ID
	topic := filepath.Join(s.StudyHome, "c")
	spec := core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID, Quote: "Elimination produces an upper triangular system.",
		Passages: []string{first}}
	// refused records the Evidence, as a dry run and for real, and checks
	// that each is refused in those words and leaves the History as it was.
	refused := func(t *testing.T, k *core.Core, spec core.EvidenceSpec, says ...string) {
		t.Helper()
		history := readFile(t, topic, "history.jsonl")
		for _, dry := range []bool{true, false} {
			spec.DryRun = dry
			res, err := k.RecordEvidence(ctx, spec)
			if core.CodeOf(err) != core.CodeFailedPrecondition || res.Changed {
				t.Errorf("RecordEvidence (dry run: %v) = %+v, %v (%s); want failed_precondition", dry, res, err, core.CodeOf(err))
				continue
			}
			for _, want := range says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error (dry run: %v) lacks %q: %v", dry, want, err)
				}
			}
		}
		if after := readFile(t, topic, "history.jsonl"); after != history {
			t.Errorf("Evidence that could not be checked was recorded:\n%s", strings.TrimPrefix(after, history))
		}
	}

	t.Run("a Source that is not indexed", func(t *testing.T) {
		other := s.book(t, "other.txt", "Another book, not indexed.\n")
		of := spec
		of.Source = other.ID
		refused(t, s.k, of, "Knowledge base plugin kb does not hold Source "+other.ID+" indexed on this computer as the Topic records it (not indexed)",
			"nothing was recorded", "run study source index c "+other.ID+" and search again")
	})
	t.Run("a Source whose file is gone", func(t *testing.T) {
		// The command that indexes would skip it, so it is not what the
		// error says to run.
		gone := s.book(t, "gone.txt", "A book that is taken away.\n")
		if err := os.Remove(filepath.Join(s.Home, "Books", "gone.txt")); err != nil {
			t.Fatal(err)
		}
		of := spec
		of.Source = gone.ID
		refused(t, s.k, of, "its file is not on this computer", "study source list c says what keeps it from being indexed")
		if res := s.search(t, core.PassageQuery{Query: "elimination", Sources: []string{gone.ID}}); res.Action != "study source list c" ||
			len(res.LeftOut) != 1 || res.LeftOut[0].Reason != "its file is not on this computer" {
			t.Errorf("a search of the Source whose file is gone = %+v; want the listing as the action", res)
		}
	})
	t.Run("a plugin that is gone", func(t *testing.T) {
		s.registerWith(t, "--exit", "3")
		refused(t, s.k, spec, "Knowledge base plugin kb stopped before it could", "Nothing was recorded", unchecked)
		s.stopped(t)
	})
	t.Run("a plugin this computer no longer has", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(s.ConfigDir, "knowledge-base-plugins.json"), []byte(`{"format": 1, "plugins": []}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		refused(t, s.k, spec, "kb, which is not registered on this computer", "Nothing was recorded", unchecked)
	})
	t.Run("a plugin that does not answer in time", func(t *testing.T) {
		s.registerWith(t, "--hang", "passage")
		k := studyOn(t, s.Computer, "", core.PluginOptions{EvidenceTimeout: time.Second})
		began := time.Now()
		refused(t, k, spec, "Knowledge base plugin kb did not give the Passages the quote is from within 1s, starting it included, so study stopped it",
			"Nothing was recorded", unchecked)
		if took := time.Since(began); took > 2*busy {
			t.Errorf("two recordings took %s over a plugin that had a second each", took)
		}
		s.stopped(t)
	})
	t.Run("a Topic that names no plugin", func(t *testing.T) {
		if _, err := s.k.UpdateTopic(ctx, "c", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBaseNone}}); err != nil {
			t.Fatal(err)
		}
		refused(t, s.k, spec, "Topic c names no Knowledge base plugin, so there is no Passage to check a quote against", unchecked)
	})
	// Stopped, it records nothing either.
	history := readFile(t, topic, "history.jsonl")
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.k.RecordEvidence(stopped, spec); core.CodeOf(err) != core.CodeCanceled {
		t.Errorf("RecordEvidence, stopped = %v (%s); want canceled", err, core.CodeOf(err))
	}
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("Evidence whose recording was stopped was recorded:\n%s", strings.TrimPrefix(after, history))
	}
	// And without a Passage, it is recorded as ever.
	spec.Passages = nil
	if res, err := s.k.RecordEvidence(ctx, spec); err != nil || !res.Changed || res.Evidence.Checked {
		t.Errorf("Evidence without a Passage, with no plugin to be had = %+v, %v", res, err)
	}
}

// A Passage's text is the plugin's, and is returned as it is: what a
// terminal would interpret is in it still, for whoever prints it to escape.
// What the plugin writes in a refusal is cleaned before it is an error.
func TestAPassagesTextIsReturnedAsItIsAndARefusalIsCleaned(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	strang := s.book(t, "strang.txt", "The pivot\x1b[2J is the\U0000202e first entry.\n")
	s.index(t, core.IndexSpec{})
	found := s.search(t, core.PassageQuery{Query: "pivot"}).Passages
	if len(found) != 1 || found[0].Text != "The pivot\x1b[2J is the\U0000202e first entry." {
		t.Fatalf("the search found %+v", found)
	}
	// It is the Source's own text all the same: a quote of what is shown
	// of it is in it.
	if res, err := s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "pivots", Source: strang.ID,
		Quote: "The pivot[2J is the first entry.", Passages: []string{found[0].ID}}); err != nil || !res.Changed {
		t.Errorf("RecordEvidence = %+v, %v", res, err)
	}

	s.registerWith(t, "--escaped", "--refuse", `search=invalid_argument:the query\x1b[2J is\nnot one\U0000202e this plugin takes`,
		"--refuse", `passage=internal:the index\x1b[2J is\nbroken`)
	for name, err := range map[string]error{
		"a search": func() error {
			_, err := s.k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "pivot"})
			return err
		}(),
		"recording Evidence": func() error {
			_, err := s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "pivots", Source: strang.ID,
				Quote: "The pivot", Passages: []string{found[0].ID}})
			return err
		}(),
	} {
		if err == nil || strings.ContainsAny(err.Error(), "\x1b\n\U0000202e") || !strings.Contains(err.Error(), `\u001b[2J is`) {
			t.Errorf("%s, refused by the plugin = %q; want the plugin's words on one line, with what a terminal would interpret escaped", name, err)
		}
	}
}

// A search has one deadline for all of it: starting the plugin, asking what
// it holds and searching are not each given the time anew.
func TestASearchHasOneDeadlineForAllOfIt(t *testing.T) {
	s := newShelf(t)
	s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	// Each of the three answers comes inside the time, and the three
	// together do not.
	s.registerWith(t, "--delay", "contract=700ms", "--delay", "status=700ms", "--delay", "search=700ms")
	k := studyOn(t, s.Computer, "", core.PluginOptions{EvidenceTimeout: 1500 * time.Millisecond})
	began := time.Now()
	res, err := k.SearchEvidence(context.Background(), core.PassageQuery{Topic: "c", Query: "elimination"})
	took := time.Since(began).Round(10 * time.Millisecond)
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "within 1.5s, starting it included") {
		t.Errorf("after %s SearchEvidence = %d Passages, %v; want the plugin late: its three answers take 2.1s, and all of it has 1.5s",
			took, len(res.Passages), err)
	}
	if took > busy {
		t.Errorf("the search took %s over a plugin that had a second and a half", took)
	}
	s.stopped(t)
}

// A search returns no more text than whoever asked can take in: the best
// Passages, as many as fit, and its note says how many more were found.
func TestASearchReturnsNoMoreTextThanFits(t *testing.T) {
	s := newShelf(t)
	// Ten paragraphs of thirty thousand bytes, each a Passage with the word
	// that is asked for: eight of them fit.
	var book strings.Builder
	for i := range 10 {
		book.WriteString("pivot " + strconv.Itoa(i) + " " + strings.Repeat("word ", 5998) + "end\n\n")
	}
	strang := s.book(t, "strang.txt", book.String())
	s.index(t, core.IndexSpec{})
	res := s.search(t, core.PassageQuery{Query: "pivot", Limit: 10})
	size := 0
	for i, p := range res.Passages {
		size += len(p.Text)
		if !strings.HasPrefix(p.Text, "pivot "+strconv.Itoa(i)+" ") || !strings.HasSuffix(p.Text, " end") {
			t.Errorf("Passage %d is not the book's paragraph %d, whole: %.20q … %d bytes", i, i, p.Text, len(p.Text))
		}
	}
	const note = "2 more Passages were found and are left out, to keep the answer under 256 KiB of text: ask for fewer, " +
		"or in words that say more closely what the Lesson needs"
	if len(res.Passages) != 8 || size > core.MaxSearchTextBytes || size+len(res.Passages[0].Text) <= core.MaxSearchTextBytes ||
		res.Note != note || !slices.Equal(res.Searched, []string{strang.ID}) || res.Action != "" {
		t.Errorf("SearchEvidence = %d Passages of %d bytes, note %q, action %q; want the 8 that fit in %d bytes", len(res.Passages),
			size, res.Note, res.Action, core.MaxSearchTextBytes)
	}
	// One more than fits is one Passage, and is said so.
	if res := s.search(t, core.PassageQuery{Query: "pivot", Limit: 9}); len(res.Passages) != 8 ||
		!strings.HasPrefix(res.Note, "1 more Passage was found and is left out, to keep") {
		t.Errorf("a search for nine = %d Passages, note %q", len(res.Passages), res.Note)
	}
	// What fits is returned whole, with no note.
	if res := s.search(t, core.PassageQuery{Query: "pivot", Limit: 8}); len(res.Passages) != 8 || res.Note != "" {
		t.Errorf("a search for eight = %d Passages, note %q", len(res.Passages), res.Note)
	}
	// The note of what was left out for its size follows the note of what
	// was not searched.
	blank := s.book(t, "blank.txt", "\n")
	res = s.search(t, core.PassageQuery{Query: "pivot", Limit: 10})
	if want := "1 of the 2 Sources was not searched: Knowledge base plugin kb does not hold it indexed on this computer as the " +
		"Topic records it. " + note; res.Note != want || len(res.LeftOut) != 1 || res.LeftOut[0].ID != blank.ID {
		t.Errorf("with a Source left out, the note is %q and what is left out %+v", res.Note, res.LeftOut)
	}
}

// A Passage is handed on without the characters of Unicode's tag block,
// which show nothing anywhere and can spell out text that only a program
// reads. What a quote is checked against is the Passage without them too.
func TestAPassageIsHandedOnWithoutTheCharactersThatShowNothing(t *testing.T) {
	s := newShelf(t)
	// "Ignore" in tag characters, between two words of the book.
	const hidden = "\U000E0049\U000E0067\U000E006E\U000E006F\U000E0072\U000E0065"
	strang := s.book(t, "strang.txt", "Elimination produces"+hidden+" an upper triangular system.\n")
	s.index(t, core.IndexSpec{})
	res := s.search(t, core.PassageQuery{Query: "elimination"})
	if len(res.Passages) != 1 || res.Passages[0].Text != "Elimination produces an upper triangular system." {
		t.Fatalf("SearchEvidence = %+v; want the Passage without the tag characters", res.Passages)
	}
	got, err := s.k.RecordEvidence(context.Background(), core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID,
		Quote: res.Passages[0].Text, Passages: []string{res.Passages[0].ID}})
	if err != nil || !got.Evidence.Checked {
		t.Errorf("the Passage's text as the search gave it, quoted = %+v, %v; want it checked", got.Evidence, err)
	}
	// A quote may not hold them: what is recorded is what is shown.
	_, err = s.k.RecordEvidence(context.Background(), core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID,
		Quote: "Elimination produces" + hidden + " an upper", Passages: []string{res.Passages[0].ID}})
	if core.CodeOf(err) != core.CodeInvalidArgument || !strings.Contains(err.Error(), "a character that is not shown (U+E0049)") {
		t.Errorf("a quote with tag characters = %v (%s); want it refused", err, core.CodeOf(err))
	}
}

// A plugin that missed its deadline was given its time already: it is
// stopped at once, and not waited for as one that answered is, however long
// a plugin is otherwise given to leave.
func TestALatePluginIsStoppedAtOnce(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	first := s.search(t, core.PassageQuery{Query: "elimination produces triangular"}).Passages[0].ID
	for where, ask := range map[string]func(*core.Core) error{
		"search": func(k *core.Core) error {
			_, err := k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "elimination"})
			return err
		},
		"passage": func(k *core.Core) error {
			_, err := k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID,
				Quote: "Elimination produces an upper triangular system.", Passages: []string{first}})
			return err
		},
	} {
		t.Run(where, func(t *testing.T) {
			// The plugin never answers, and stays when its input closes
			// until it is signalled.
			s.registerWith(t, "--hang", where, "--linger")
			k := studyOn(t, s.Computer, "", core.PluginOptions{EvidenceTimeout: 500 * time.Millisecond, StopGrace: 2 * busy})
			began := time.Now()
			err := ask(k)
			if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "within 500ms, starting it included, so study stopped it") {
				t.Errorf("%v (%s); want the plugin late", err, core.CodeOf(err))
			}
			if took := time.Since(began); took > busy {
				t.Errorf("study took %s over a plugin that had half a second: a late plugin is not given %s more to leave", took, 2*busy)
			}
			s.stopped(t)
		})
	}
}

// A search covers what the plugin says, by the hash, that a search finds. A
// Source that is being indexed anew, or whose last indexing failed, is found
// as it was while its earlier content is still indexed, and a quote is
// checked against its Passages. It is left out when the plugin gives no
// hash for it, or the hash of other content than the Source's.
func TestASourceWhoseOwnContentIsStillIndexedIsSearched(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	first := s.search(t, core.PassageQuery{Query: "elimination produces triangular"}).Passages[0].ID
	const other = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	n := 0
	for state, with := range map[string]string{
		plugincontract.StateFailed:   `"reason":"its vectors are of another model"`,
		plugincontract.StateIndexing: `"progress":0.5`,
	} {
		for hash, searched := range map[string]bool{strang.Hash: true, other: false, "": false} {
			t.Run(state+" with the hash "+hash, func(t *testing.T) {
				said := `{"id":"` + strang.ID + `","state":"` + state + `",` + with
				if hash != "" {
					said += `,"hash":"` + hash + `"`
				}
				// What the plugin says of the Source, whatever its index holds.
				s.registerWith(t, "--result", `status={"sources":[`+said+`}]}`)
				res := s.search(t, core.PassageQuery{Query: "elimination"})
				n++
				// A quote of its own each time, so that none was recorded before.
				quote := []string{"Elimination produces", "produces an upper", "an upper triangular", "upper triangular system",
					"Elimination produces an upper", "triangular system"}[n-1]
				got, err := s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "elimination", Source: strang.ID,
					Passages: []string{first}, Quote: quote})
				if searched {
					if !slices.Equal(res.Searched, []string{strang.ID}) || len(res.Passages) == 0 || len(res.LeftOut) != 0 || res.Note != "" {
						t.Errorf("SearchEvidence = %+v; want the Source searched: the plugin says a search finds its own content", res)
					}
					if err != nil || !got.Evidence.Checked {
						t.Errorf("RecordEvidence = %+v, %v; want the quote checked against the Passage", got, err)
					}
					return
				}
				if len(res.Searched) != 0 || len(res.Passages) != 0 || len(res.LeftOut) != 1 || res.LeftOut[0].State != state {
					t.Errorf("SearchEvidence = %+v; want the Source left out, as %s", res, state)
				}
				if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not hold Source "+strang.ID+" indexed") {
					t.Errorf("RecordEvidence = %+v, %v (%s); want it refused: the Source's own content is not what a search finds", got, err, core.CodeOf(err))
				}
			})
		}
	}
}
