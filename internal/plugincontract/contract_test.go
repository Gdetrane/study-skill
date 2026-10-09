package plugincontract_test

import (
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
	// A kind this version does not know is kept, and no location is fine.
	c, err = plugincontract.ParseContract([]byte(`{"version": 1, "sources": ["recording", "file"], "search": ["embedding", "hybrid_rank"]}`))
	if err != nil || c.Sources[0] != "recording" || c.Search[1] != "hybrid_rank" || len(c.Locations) != 0 {
		t.Errorf("ParseContract = %+v, %v", c, err)
	}
	// A newer version is newer whatever else the answer holds.
	var newer *plugincontract.NewerVersionError
	for _, data := range []string{`{"version": 2}`, `{"version": 12, "sources": "all", "search": 7}`} {
		if _, err := plugincontract.ParseContract([]byte(data)); !errors.As(err, &newer) || newer.Version < 2 {
			t.Errorf("ParseContract(%s) = %v; want a NewerVersionError", data, err)
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
		`{"version": -1}`:   "version is not a whole number of 1 or more",
		`{"version": 1.5}`:  "version is not a whole number of 1 or more",
		`{"version": "1"}`:  "version is not a whole number of 1 or more",
		`{"version": null}`: "version is not a whole number of 1 or more",
		`{"version": 1}`:    "sources lists none of file, url",
		`{"version": 1, "sources": ["recording"], "search": ["keyword"]}`:                                              "sources lists none of file, url",
		`{"version": 1, "sources": ["file"], "search": []}`:                                                            "search lists none of keyword, embedding",
		`{"version": 1, "sources": ["file", "file"], "search": ["keyword"]}`:                                           "sources lists file twice",
		`{"version": 1, "sources": ["file"], "search": ["Keyword", "keyword"]}`:                                        `search[0] is "Keyword", not a kind`,
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
	}}
	if err := status.Validate(); err != nil {
		t.Errorf("a status: %v", err)
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
