package core_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// byKeyword is what a plugin says of a search it made with less than it was
// set to.
const byKeyword = "These Passages were found by keyword alone: the embedding endpoint 127.0.0.1:8080 did not answer within 10s."

// A plugin that searched with less than it was set to says so beside its
// Passages, and the search carries what it says, as it is. That is all a
// notice does: the Passages are the ones the plugin gave, nothing is left
// out or to be run for it, nothing is recorded, and the plugin was asked
// nothing more than for any search. A search of a plugin that says nothing
// has no notice at all.
func TestASearchCarriesWhatThePluginSaysItDidLess(t *testing.T) {
	s := newShelf(t)
	strang := s.book(t, "strang.txt", elimination)
	s.index(t, core.IndexSpec{})
	plain := s.search(t, core.PassageQuery{Query: "elimination produces triangular"})
	if plain.Notice != "" || len(plain.Passages) != 3 {
		t.Fatalf("a search of a plugin that says nothing of it = %+v; want its Passages, and no notice", plain)
	}
	if data, err := json.Marshal(plain); err != nil || strings.Contains(string(data), "notice") {
		t.Errorf("a search without a notice is written as %s (%v); want no notice in it, not an empty one", data, err)
	}

	s.registerWith(t, "--notice", byKeyword)
	topic := filepath.Join(s.StudyHome, "c")
	history, files := readFile(t, topic, "history.jsonl"), tree(t, topic)
	before := len(s.calls(t))
	res := s.search(t, core.PassageQuery{Query: "elimination produces triangular"})
	if res.Notice != byKeyword {
		t.Errorf("the search's notice is %q, want the plugin's: %q", res.Notice, byKeyword)
	}
	// Everything else is as it was without one.
	res.Notice = ""
	if !reflect.DeepEqual(res, plain) {
		t.Errorf("with a notice the search = %+v\nwithout one it was %+v", res, plain)
	}
	if res.Note != "" || res.Action != "" || res.LeftOut != nil || !slices.Equal(res.Searched, []string{strang.ID}) {
		t.Errorf("a notice changed what the search says was searched, or what to do next: %+v", res)
	}
	if tools, _ := s.newCalls(t, before); !slices.Equal(tools, []string{"contract", "status", "search"}) {
		t.Errorf("a search whose plugin gives a notice asked the plugin %v", tools)
	}
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("a search with a notice recorded Events:\n%s", strings.TrimPrefix(after, history))
	}
	if after := tree(t, topic); !reflect.DeepEqual(after, files) {
		t.Errorf("a search with a notice changed the Topic's files: %v, were %v", after, files)
	}
	// It is in the answer under its own name, before the Passages it is
	// about.
	data, err := json.Marshal(s.search(t, core.PassageQuery{Query: "elimination"}))
	notice, passages := strings.Index(string(data), `"notice":"These Passages were found by keyword alone`), strings.Index(string(data), `"passages":`)
	if err != nil || notice < 0 || passages < notice {
		t.Errorf("the search is written as %s (%v); want its notice in a field of its own, before its Passages", data, err)
	}

	// A search that found nothing says it too: by keyword alone, nothing
	// found is not nothing there.
	if none := s.search(t, core.PassageQuery{Query: "zebra"}); none.Notice != byKeyword || len(none.Passages) != 0 || none.Note != "" {
		t.Errorf("a search that found nothing, by keyword alone = %+v; want the plugin's notice", none)
	}
	// Recording a quote from a Passage of such a search is as ever.
	found := s.search(t, core.PassageQuery{Query: "elimination produces triangular"})
	recorded, err := s.k.RecordEvidence(context.Background(), core.EvidenceSpec{Topic: "c", Lesson: "intro", Source: strang.ID,
		Quote: "Elimination produces an upper triangular system.", Passages: []string{found.Passages[0].ID}})
	if err != nil || !recorded.Evidence.Checked {
		t.Errorf("Evidence quoted from a Passage of a search with a notice = %+v, %v; want it checked and recorded", recorded, err)
	}

	// A plugin that is not asked to search says nothing of a search: with
	// nothing indexed there is no notice.
	blank := s.book(t, "blank.txt", " \n\n ")
	if only := s.search(t, core.PassageQuery{Query: "elimination", Sources: []string{blank.ID}}); only.Notice != "" || len(only.Searched) != 0 {
		t.Errorf("a search that asked the plugin for no search = %+v; want no notice", only)
	}
	// What a plugin says of a search in fields of its own, as Shelf says how
	// it searched and whether it reranked, is not the contract's: the search
	// is answered, and carries the notice and none of them.
	s.registerWith(t, "--result", `search={"passages": [], "search": ["keyword"], "reranked": false, "notice": "by keyword alone"}`)
	own := s.search(t, core.PassageQuery{Query: "elimination", Sources: []string{strang.ID}})
	if own.Notice != "by keyword alone" || len(own.Passages) != 0 {
		t.Errorf("a search whose plugin adds fields of its own = %+v; want it answered, with its notice", own)
	}
	if data, err := json.Marshal(own); err != nil || strings.Contains(string(data), "reranked") || strings.Contains(string(data), `"search"`) {
		t.Errorf("the search is written as %s (%v); want nothing of the plugin's own fields", data, err)
	}
	s.stopped(t)
}

// A notice is text a plugin wrote, and is not trusted with the terminal or
// repeated at any length: it reaches a search on one line, with nothing a
// terminal would act on, and cut short. That is what becomes of a reason,
// and the same words as a Source's reason come to the same text.
func TestWhatAPluginSaysOfASearchIsCleanedAndCutAsAReasonIs(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	said := "These Passages were found by keyword alone" + hostile + strings.Repeat("long ", 5000)
	// A service, which can say what a command's arguments could not carry.
	// It says the same words of every search, and of the Source it comes to
	// call failed.
	var failed atomic.Pointer[string]
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Notice: said, Twist: func(tool string, answer []byte, refused bool) []byte {
		var status plugincontract.StatusResult
		id := failed.Load()
		if tool != "status" || refused || id == nil || json.Unmarshal(answer, &status) != nil {
			return answer
		}
		status.Sources = append(status.Sources, plugincontract.SourceStatus{ID: *id, State: plugincontract.StateFailed, Reason: said})
		twisted, _ := json.Marshal(status)
		return twisted
	}})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	c.RegisterURL(t, "kb", server.URL+"/mcp")
	k := studyOn(t, c, "", core.PluginOptions{})
	namedTopic(t, k, "c", "kb")
	addBook(t, k, "c", filepath.Join(c.Home, "Books", "book.txt"), pages(2, "one"))
	if _, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}
	scan := addBook(t, k, "c", filepath.Join(c.Home, "Books", "scan.txt"), pages(1, "two"))
	failed.Store(&scan.ID)

	res, err := k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "one"})
	if err != nil || len(res.Passages) != 2 {
		t.Fatalf("SearchEvidence = %+v, %v; want the book's two Passages", res, err)
	}
	if strings.ContainsAny(res.Notice, "\x1b\x07\n") || strings.ContainsRune(res.Notice, 0x202e) {
		t.Errorf("the notice holds what a terminal would act on: %q", res.Notice)
	}
	if n := len(res.Notice); n > plugincontract.MaxNoticeBytes+len("…") || n < plugincontract.MaxNoticeBytes-100 || !strings.HasSuffix(res.Notice, "…") {
		t.Errorf("a notice of %d bytes is carried as %d bytes, ending in %q; want it cut at %d, and marked as cut",
			len(said), n, res.Notice[max(0, n-12):], plugincontract.MaxNoticeBytes)
	}
	if want := `These Passages were found by keyword alone\u001b]0;gotcha\u0007\u001b[2J`; !strings.HasPrefix(res.Notice, want) ||
		!strings.Contains(res.Notice, " second line long long ") {
		t.Errorf("the notice begins %.120q; want the plugin's words on one line, beginning %q", res.Notice, want)
	}
	// The same words as the reason of a Source that failed, which study
	// source index gives as far as study keeps one.
	listed, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, source := range listed.Sources {
		if source.ID == scan.ID {
			reason = source.Reason
		}
	}
	if reason == "" || res.Notice != reason {
		t.Errorf("as a notice the plugin's words are\n%q\nand as a reason\n%q\nwant the same of both", res.Notice, reason)
	}
	// And the search is one JSON document of a size to read.
	if data, err := json.Marshal(res); err != nil || !json.Valid(data) || len(data) > 8<<10 {
		t.Errorf("the search as JSON: %d bytes, %v", len(data), err)
	}
}
