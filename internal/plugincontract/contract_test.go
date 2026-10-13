package plugincontract_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

const hash = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func file() plugincontract.Source {
	return plugincontract.Source{ID: "strang-linear-algebra.k3f9a2", Title: "Linear Algebra", Kind: "file", Path: "/home/ada/Books/strang.pdf", Hash: hash}
}

func page() plugincontract.Source {
	return plugincontract.Source{ID: "go-dev-blog-context.m4k8s3", Title: "Context", Kind: "url", URL: "https://go.dev/blog/context"}
}

// refusedFor fails the test unless err says want.
func refusedFor(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: %v; want an error with %q", what, err, want)
	}
	if err != nil && strings.ContainsFunc(err.Error(), func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Errorf("%s: the error holds a control character: %q", what, err.Error())
	}
}

func TestNamesIdsAndHashes(t *testing.T) {
	for _, name := range []string{"c", "linear-algebra", "a1-b2", strings.Repeat("a", plugincontract.MaxNameBytes)} {
		if err := plugincontract.CheckCollection(name); err != nil {
			t.Errorf("CheckCollection(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "Linear", "a--b", "-a", "a-", "a b", "../a", "a/b", "a.b", "ä", strings.Repeat("a", plugincontract.MaxNameBytes+1), "a\x1b[2J"} {
		if err := plugincontract.CheckCollection(name); err == nil {
			t.Errorf("CheckCollection(%q) passed", name)
		} else {
			refusedFor(t, name, err, "collection")
		}
	}
	for _, id := range []string{"a", "strang-linear-algebra.k3f9a2", "0.a", "a..b", strings.Repeat("a", plugincontract.MaxIDBytes)} {
		if err := plugincontract.CheckSourceID(id); err != nil {
			t.Errorf("CheckSourceID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", ".a", "-a", "A", "a/b", "a b", "a_b", strings.Repeat("a", plugincontract.MaxIDBytes+1), "a\nb"} {
		if err := plugincontract.CheckSourceID(id); err == nil {
			t.Errorf("CheckSourceID(%q) passed", id)
		}
	}
	for _, id := range []string{"a", "7f3a9c12e5b04d68", "strang.k3f9a2:46:2", "A_b-c.d", ".a", "_", strings.Repeat("a", plugincontract.MaxIDBytes)} {
		if err := plugincontract.CheckPassageID(id); err != nil {
			t.Errorf("CheckPassageID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "-a", "--help", "a b", "a/b", "a;b", "$(x)", "é", strings.Repeat("a", plugincontract.MaxIDBytes+1), "a\x00"} {
		if err := plugincontract.CheckPassageID(id); err == nil {
			t.Errorf("CheckPassageID(%q) passed", id)
		}
	}
	if err := plugincontract.CheckHash(hash); err != nil {
		t.Errorf("CheckHash = %v", err)
	}
	for _, h := range []string{"", strings.TrimPrefix(hash, "sha256:"), "sha256:" + strings.Repeat("0", 63), strings.ToUpper(hash), "md5:" + strings.Repeat("0", 64),
		hash + "0", "sha256:" + strings.Repeat("g", 64)} {
		if err := plugincontract.CheckHash(h); err == nil {
			t.Errorf("CheckHash(%q) passed", h)
		}
	}
}

func TestParseContract(t *testing.T) {
	c, err := plugincontract.ParseContract([]byte(`{"version": 1, "sources": ["file", "url"], "search": ["keyword"], "locations": ["pages"], "more": true}`))
	if err != nil || c.Version != 1 || len(c.Sources) != 2 || len(c.Search) != 1 || len(c.Locations) != 1 {
		t.Fatalf("ParseContract = %+v, %v", c, err)
	}
	// A kind this version does not know is kept, and a plugin that knows no
	// location lists none.
	c, err = plugincontract.ParseContract([]byte(`{"version": 1, "sources": ["recording", "file"], "search": ["embedding", "hybrid_rank"], "locations": []}`))
	if err != nil || c.Sources[0] != "recording" || c.Search[1] != "hybrid_rank" || len(c.Locations) != 0 {
		t.Errorf("ParseContract = %+v, %v", c, err)
	}
	// A newer version is newer whatever else the answer holds.
	var newer *plugincontract.NewerVersionError
	// And however large the number is, or however it is written: a version
	// past what this program counts with is no broken answer.
	for data, said := range map[string]string{
		`{"version": 2}`: "2",
		`{"version": 12, "sources": "all", "search": 7}`: "12",
		`{"version": 2.0}`:                               "2.0",
		`{"version": 9223372036854775807}`:               "9223372036854775807",
		`{"version": 9223372036854775808}`:               "9223372036854775808",
		`{"version": 18446744073709551616}`:              "18446744073709551616",
		`{"version": 1e30}`:                              "1e30",
		`{"version": 1e999}`:                             "1e999",
		`{"version": 1` + strings.Repeat("0", 100) + `}`: "1" + strings.Repeat("0", 39) + "…",
	} {
		_, err := plugincontract.ParseContract([]byte(data))
		if !errors.As(err, &newer) || newer.Version != said {
			t.Errorf("ParseContract(%s) = %v; want a NewerVersionError for version %s", data, err, said)
			continue
		}
		if want := "the plugin speaks version " + said + " of the contract, and this is version 1"; err.Error() != want {
			t.Errorf("ParseContract(%s) says %q; want %q", data, err, want)
		}
	}
	// Version 1 is version 1 however it is written.
	for _, data := range []string{`{"version": 1.0, "sources": ["file"], "search": ["keyword"], "locations": []}`,
		`{"version": 1e0, "sources": ["file"], "search": ["keyword"], "locations": []}`} {
		if c, err := plugincontract.ParseContract([]byte(data)); err != nil || c.Version != 1 || len(c.Sources) != 1 {
			t.Errorf("ParseContract(%s) = %+v, %v; want version 1", data, c, err)
		}
	}
	many := `"a` + strings.Repeat(`", "a`, plugincontract.MaxKinds) + `"`
	for data, want := range map[string]string{
		``:    "it is not a JSON object",
		`[]`:  "it is not a JSON object",
		`"1"`: "it is not a JSON object",
		`{`:   "it is not valid JSON",
		`{}`:  "version is missing",
		`{"version": 0, "sources": ["file"], "search": ["keyword"]}`: "version is not a whole number of 1 or more",
		`{"version": -1}`:     "version is not a whole number of 1 or more",
		`{"version": -1e999}`: "version is not a whole number of 1 or more",
		`{"version": 2.5}`:    "version is not a whole number of 1 or more",
		`{"version": [2]}`:    "version is not a whole number of 1 or more",
		`{"version": "2"}`:    "version is not a whole number of 1 or more",
		`{"version": true}`:   "version is not a whole number of 1 or more",
		`{"version": 1.5}`:    "version is not a whole number of 1 or more",
		`{"version": "1"}`:    "version is not a whole number of 1 or more",
		`{"version": null}`:   "version is not a whole number of 1 or more",
		`{"version": 1, "sources": [], "search": ["keyword"], "locations": []}`:                  "sources lists none of file, url",
		`{"version": 1, "sources": ["recording"], "search": ["keyword"], "locations": []}`:       "sources lists none of file, url",
		`{"version": 1, "sources": ["file"], "search": [], "locations": []}`:                     "search lists none of keyword, embedding",
		`{"version": 1, "sources": ["file", "file"], "search": ["keyword"], "locations": []}`:    "sources lists file twice",
		`{"version": 1, "sources": ["file"], "search": ["Keyword", "keyword"], "locations": []}`: `search[0] is "Keyword", not a kind`,
		// Every field is there, under its own name: a list that is missing
		// says nothing, where an empty one says "none".
		`{"version": 1}`: `"sources" is missing`,
		`{"version": 1, "sources": ["file"], "search": ["keyword"]}`:                                                   `"locations" is missing`,
		`{"version": 1, "sources": ["file"], "search": ["keyword"], "locations": null}`:                                "locations: it is not a list",
		`{"Version": 1, "sources": ["file"], "search": ["keyword"], "locations": []}`:                                  `"version" is spelled "Version"`,
		`{"version": 1, "Sources": ["file"], "search": ["keyword"], "locations": []}`:                                  `"sources" is spelled "Sources"`,
		`{"version": 1, "sources": ["file"], "search": ["keyword"], "LOCATIONS": ["pages"]}`:                           `"locations" is spelled "LOCATIONS"`,
		`{"version": 1, "sources": ["file"], "search": ["keyword"], "locations": ["two words"]}`:                       `locations[0] is "two words", not a kind`,
		`{"version": 1, "sources": ["file"], "search": ["keyword"], "locations": [` + many + `]}`:                      "locations lists more than 16 kinds",
		`{"version": 1, "sources": "file", "search": ["keyword"]}`:                                                     "sources is a string, not a []string",
		`{"version": 1, "sources": ["file"], "search": ["keyword"], "locations": ["` + strings.Repeat("a", 33) + `"]}`: "not a kind",
	} {
		_, err := plugincontract.ParseContract([]byte(data))
		refusedFor(t, data, err, want)
		if errors.As(err, &newer) {
			t.Errorf("ParseContract(%s) is taken for a newer version", data)
		}
	}
}

func TestInputsAreHeldToTheContract(t *testing.T) {
	for what, in := range map[string]interface{ Validate() error }{
		"a file":            plugincontract.IndexInput{Collection: "c", Source: file()},
		"a web page":        plugincontract.IndexInput{Collection: "c", Source: page()},
		"a web page again":  plugincontract.IndexInput{Collection: "c", Source: func() plugincontract.Source { s := page(); s.Refetch = true; return s }()},
		"a status":          plugincontract.StatusInput{Collection: "c"},
		"a cancel":          plugincontract.CancelInput{Collection: "c", Source: "a.1"},
		"a search":          plugincontract.SearchInput{Collection: "c", Query: "why?\n\tbecause", Limit: 1},
		"the widest search": plugincontract.SearchInput{Collection: "c", Query: strings.Repeat("q", plugincontract.MaxQueryBytes), Limit: plugincontract.MaxSearchLimit, Sources: []string{"a.1", "b.2"}},
		"a passage":         plugincontract.PassageInput{Collection: "c", ID: "7f3a"},
		"a title of 200 letters": plugincontract.IndexInput{Collection: "c", Source: func() plugincontract.Source {
			s := file()
			s.Title = strings.Repeat("é", plugincontract.MaxTitleRunes)
			return s
		}()},
	} {
		if err := in.Validate(); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	change := func(base plugincontract.Source, f func(*plugincontract.Source)) plugincontract.IndexInput {
		f(&base)
		return plugincontract.IndexInput{Collection: "c", Source: base}
	}
	for want, in := range map[string]interface{ Validate() error }{
		"collection is missing":                  plugincontract.StatusInput{},
		"the Source's id is missing":             change(file(), func(s *plugincontract.Source) { s.ID = "" }),
		"source.title is missing":                change(file(), func(s *plugincontract.Source) { s.Title = " " }),
		"source.title is not text":               change(file(), func(s *plugincontract.Source) { s.Title = "a\x1b[2Jb" }),
		"up to 200 characters":                   change(file(), func(s *plugincontract.Source) { s.Title = strings.Repeat("a", plugincontract.MaxTitleRunes+1) }),
		"source.kind is missing":                 change(file(), func(s *plugincontract.Source) { s.Kind = "" }),
		`source.kind is "recording"`:             change(file(), func(s *plugincontract.Source) { s.Kind = "recording" }),
		"source.path is missing":                 change(file(), func(s *plugincontract.Source) { s.Path = "" }),
		"source.path is not an absolute path":    change(file(), func(s *plugincontract.Source) { s.Path = "Books/strang.pdf" }),
		"of up to 4096 bytes":                    change(file(), func(s *plugincontract.Source) { s.Path = "/" + strings.Repeat("a", plugincontract.MaxPathBytes) }),
		"source.hash is missing":                 change(file(), func(s *plugincontract.Source) { s.Hash = "" }),
		"source.hash: ":                          change(file(), func(s *plugincontract.Source) { s.Hash = "sha1:abc" }),
		"a file Source has a path and a hash":    change(file(), func(s *plugincontract.Source) { s.URL = "https://go.dev/" }),
		"and neither url nor refetch":            change(file(), func(s *plugincontract.Source) { s.Refetch = true }),
		"source.url is missing":                  change(page(), func(s *plugincontract.Source) { s.URL = "" }),
		"source.url is not an http or https":     change(page(), func(s *plugincontract.Source) { s.URL = "file:///etc/passwd" }),
		"without a user name or password":        change(page(), func(s *plugincontract.Source) { s.URL = "https://ada:secret@go.dev/" }),
		"a url Source has a url":                 change(page(), func(s *plugincontract.Source) { s.Hash = hash }),
		"query is missing":                       plugincontract.SearchInput{Collection: "c", Query: "\n", Limit: 5},
		"query is not text of up to 4096 bytes":  plugincontract.SearchInput{Collection: "c", Query: strings.Repeat("q", plugincontract.MaxQueryBytes+1), Limit: 5},
		"without control characters":             plugincontract.SearchInput{Collection: "c", Query: "a\x1b[2Jb", Limit: 5},
		"limit is 0, not a number from 1 to 50":  plugincontract.SearchInput{Collection: "c", Query: "q"},
		"limit is 51, not a number from 1 to 50": plugincontract.SearchInput{Collection: "c", Query: "q", Limit: plugincontract.MaxSearchLimit + 1},
		"sources is empty":                       plugincontract.SearchInput{Collection: "c", Query: "q", Limit: 5, Sources: []string{}},
		"sources[1]: ":                           plugincontract.SearchInput{Collection: "c", Query: "q", Limit: 5, Sources: []string{"a.1", "../b"}},
		"sources lists a.1 twice":                plugincontract.SearchInput{Collection: "c", Query: "q", Limit: 5, Sources: []string{"a.1", "a.1"}},
		"the Passage's id is missing":            plugincontract.PassageInput{Collection: "c"},
		`"-a" is not a Passage's id`:             plugincontract.PassageInput{Collection: "c", ID: "-a"},
		`"../a" is not a Source's id`:            plugincontract.CancelInput{Collection: "c", Source: "../a"},
	} {
		refusedFor(t, want, in.Validate(), want)
	}
	// No error repeats an address's password.
	if err := change(page(), func(s *plugincontract.Source) { s.URL = "https://ada:secret@go.dev/" }).Validate(); strings.Contains(err.Error(), "secret") {
		t.Errorf("the error repeats a password: %v", err)
	}
}

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{{"abc", 5, "abc"}, {"abc", 3, "abc"}, {"abc", 2, "ab"}, {"aé", 2, "a"}, {"aé", 3, "aé"}, {"é", 1, ""}, {"", 0, ""}} {
		if got := plugincontract.Cut(tc.in, tc.max); got != tc.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

// Results are checked where study's client is tested, against a plugin that
// gives each broken answer (internal/pluginclient). Here: what is allowed.
func TestResultsTheContractAllows(t *testing.T) {
	half := 0.5
	status := plugincontract.StatusResult{Sources: []plugincontract.SourceStatus{
		{ID: "a.1", State: plugincontract.StateIndexed, Hash: hash},
		{ID: "b.2", State: plugincontract.StateIndexing, Progress: &half, Hash: hash},
		{ID: "c.3", State: plugincontract.StateIndexing},
		{ID: "d.4", State: plugincontract.StateFailed, Reason: "no text layer", Hash: hash},
		{ID: "e.5", State: plugincontract.StateOutdated, Reason: "its Passages have no vectors of the model", Hash: hash},
	}}
	if err := status.Validate(); err != nil {
		t.Errorf("a status: %v", err)
	}
	// A Source that is outdated is found as it is, so of some content, and
	// says what indexing it again would add. And index never answers it:
	// what was outdated was just asked to be brought up to date.
	for name, said := range map[string]plugincontract.SourceStatus{
		"without a hash":   {ID: "e.5", State: plugincontract.StateOutdated, Reason: "no vectors"},
		"without a reason": {ID: "e.5", State: plugincontract.StateOutdated, Hash: hash},
		"with a blank one": {ID: "e.5", State: plugincontract.StateOutdated, Hash: hash, Reason: " \n"},
	} {
		if err := said.Validate(); err == nil || !strings.Contains(err.Error(), "is outdated and has no") {
			t.Errorf("a Source that is outdated %s: Validate() = %v", name, err)
		}
	}
	asked := plugincontract.IndexInput{Collection: "c", Source: plugincontract.Source{ID: "e.5", Title: "E", Kind: plugincontract.SourceFile,
		Path: "/books/e.pdf", Hash: hash}}
	answer := plugincontract.IndexResult{Source: status.Sources[4]}
	if err := answer.Validate(asked); err == nil || !strings.Contains(err.Error(), "which a Source that was just asked to be indexed is not") {
		t.Errorf("index answered that the Source is outdated: Validate() = %v", err)
	}
	if err := (plugincontract.SourceStatus{ID: "f.6", State: "stale", Hash: hash}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "not indexed, indexing, failed or outdated") {
		t.Errorf("a state the contract does not have: Validate() = %v", err)
	}
	if err := (plugincontract.StatusResult{}).Validate(); err != nil {
		t.Errorf("an empty status: %v", err)
	}
	in := plugincontract.SearchInput{Collection: "c", Query: "q", Limit: 3, Sources: []string{"a.1"}}
	found := plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{
		{Passage: plugincontract.Passage{ID: "p1", Source: "a.1", Text: "x", Location: &plugincontract.Location{Kind: "pages", First: 1, Last: 1}}, Score: 2},
		// The same score twice, a location of a later kind, and none.
		{Passage: plugincontract.Passage{ID: "p2", Source: "a.1", Text: strings.Repeat("y", plugincontract.MaxPassageBytes), Location: &plugincontract.Location{Kind: "time"}}, Score: 2},
		{Passage: plugincontract.Passage{ID: "p3", Source: "a.1", Text: "z"}, Score: -1.5},
	}}
	if err := found.Validate(in); err != nil {
		t.Errorf("a search result: %v", err)
	}
	if err := (plugincontract.SearchResult{}).Validate(in); err != nil {
		t.Errorf("a search that found nothing: %v", err)
	}
}

// A search may say what it did less than the plugin was set to, in a notice
// beside its Passages, and one that did it all says nothing: the field is
// left out. The notice is words for the learner: an answer is refused
// neither for how long they are nor for a character in them, as it is not
// for a reason's. study's client cuts and cleans them (internal/pluginclient).
func TestASearchMaySayWhatItDidLess(t *testing.T) {
	in := plugincontract.SearchInput{Collection: "c", Query: "q", Limit: 3}
	found := plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{
		{Passage: plugincontract.Passage{ID: "p1", Source: "a.1", Text: "x"}, Score: 1},
	}}
	plain, err := json.Marshal(found)
	if err != nil || string(plain) != `{"passages":[{"id":"p1","source":"a.1","text":"x","score":1}]}` {
		t.Errorf("a search without a notice is written as %s (%v); want no notice in it, not an empty one", plain, err)
	}
	for name, notice := range map[string]string{
		"a notice":                           "These Passages were found by keyword alone: the embedding endpoint 127.0.0.1:8080 did not answer within 10s.",
		"one of the most bytes":              strings.Repeat("é", plugincontract.MaxNoticeBytes/2),
		"one over the limit":                 strings.Repeat("x", plugincontract.MaxNoticeBytes+1),
		"one a terminal acts on, over lines": "found\x1b[2J by keyword\nalone",
		"one that is white space":            " \n",
	} {
		said := found
		said.Notice = notice
		if err := said.Validate(in); err != nil {
			t.Errorf("a search with %s: Validate() = %v; want it let through, for study to clean and to cut", name, err)
		}
		data, err := json.Marshal(said)
		if err != nil {
			t.Fatal(err)
		}
		var back plugincontract.SearchResult
		if err := plugincontract.Decode(data, &back); err != nil || back.Notice != notice || len(back.Passages) != 1 {
			t.Errorf("a search with %s is read back as %+v (%v)", name, back, err)
		}
	}
	// A plugin may say more of a search in fields of its own, which are
	// passed over: these are the two Shelf adds.
	var more plugincontract.SearchResult
	if err := plugincontract.Decode([]byte(`{"passages": [], "search": ["keyword"], "reranked": false, "notice": "by keyword alone"}`), &more); err != nil ||
		more.Notice != "by keyword alone" || more.Passages == nil {
		t.Errorf("a search with fields of the plugin's own is read as %+v (%v)", more, err)
	}
	// The notice is optional, so it is left out and never null, and it is
	// text, under its own name.
	for data, want := range map[string]string{
		`{"passages": [], "notice": null}`:       `"notice" is null: a field that is optional is left out, not given as null`,
		`{"passages": [], "notice": 7}`:          "notice is a number, not a string",
		`{"passages": [], "notice": ["a", "b"]}`: "notice is a array, not a string",
		`{"passages": [], "Notice": "a"}`:        `"notice" is spelled "Notice"`,
	} {
		var out plugincontract.SearchResult
		refusedFor(t, data, plugincontract.Decode([]byte(data), &out), want)
	}
	if plugincontract.MaxNoticeBytes < 500 || plugincontract.MaxNoticeBytes > plugincontract.MaxTextBytes {
		t.Errorf("a notice may have %d bytes; want room for a sentence or two that name an endpoint and repeat what it said, and no "+
			"more than a reason has (%d)", plugincontract.MaxNoticeBytes, plugincontract.MaxTextBytes)
	}
}
