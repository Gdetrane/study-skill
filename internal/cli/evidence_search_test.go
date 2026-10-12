package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// eliminationBook is a book of two pages and three paragraphs, which the
// test plugin makes three Passages of: two on the first page, and one on the
// second whose text is broken over two lines.
const eliminationBook = "Elimination produces an upper triangular system.\n\n" +
	"The pivots are on the diagonal of the triangle after elimination.\f" +
	"Back substitution solves the triangular\nsystem from the last equation up.\n"

// passages searches the Topic and returns the ids of the Passages found, as
// the plugin gave them.
func (s *shelfComputer) passages(t *testing.T, query string) []string {
	t.Helper()
	var ids []string
	for _, p := range s.found(t, query) {
		ids = append(ids, p.ID)
	}
	return ids
}

// found searches the Topic and returns the Passages found, with the ids the
// plugin and study gave, which what study prints in these tests hides.
func (s *shelfComputer) found(t *testing.T, query string) []core.FoundPassage {
	t.Helper()
	var out struct {
		Data core.PassageSearch `json:"data"`
	}
	s.asItIs(t, &out, "evidence", "search", "linear-algebra", query, "--json")
	return out.Data.Passages
}

// asItIs runs study, which must succeed, and reads the JSON document it
// prints into v, with every id as it is.
func (s *shelfComputer) asItIs(t *testing.T, v any, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := core.Options{Getenv: func(key string) string { return s.env[key] }, Dir: s.home, Now: func() time.Time { return fixedNow },
		NewID: func() string { return fmt.Sprintf("id%03d", s.ids.Add(1)) }, Plugins: s.plugins}
	code := cli.Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, opts)
	if err := json.Unmarshal(stdout.Bytes(), v); code != cli.ExitOK || err != nil {
		t.Fatalf("study %s: exit %d, %v, stdout %s, stderr %s", strings.Join(args, " "), code, err, stdout.String(), stderr.String())
	}
}

// A Topic's Sources are searched, a quote is recorded with the Passage it
// is from and so checked, and the Evidence is listed with whether it was:
// what each command prints, for people and as JSON, and what the plugin is
// asked for it.
func TestEvidenceSearchCommands(t *testing.T) {
	s := newShelfComputer(t)
	check := func(name string, code int, args ...string) result {
		t.Helper()
		r := s.run(t, args...)
		if r.code != code {
			t.Errorf("%s: exit %d, want %d (stdout: %s, stderr: %s)", name, r.code, code, r.stdout, r.stderr)
		}
		if strings.HasSuffix(name, ".json") && (r.stderr != "" || !json.Valid([]byte(r.stdout))) {
			t.Errorf("%s: --json wrote %q to stderr, and to stdout what is one JSON document: %v", name, r.stderr, json.Valid([]byte(r.stdout)))
		}
		golden(t, name, r.stdout)
		return r
	}
	asked := func(before int) []string { return plugintest.Tools(s.calls(t)[before:]) }
	search := []string{"evidence", "search", "linear-algebra"}
	record := []string{"evidence", "record", "linear-algebra", "--lesson", "elimination"}

	// Nothing to search yet, and then nothing indexed: neither is an error.
	check("evidence_search_no_sources.txt", cli.ExitOK, append(search, "elimination")...)
	strang := s.book(t, "strang.txt", eliminationBook)
	// A book whose text holds what a terminal would interpret, and a tab.
	notes := s.book(t, "notes.txt", "The pivot\x1b[2J is the\tfirst entry of a row.\n")
	before := len(s.calls(t))
	check("evidence_search_nothing_indexed.txt", cli.ExitOK, append(search, "elimination")...)
	check("evidence_search_nothing_indexed.json", cli.ExitOK, append(search, "elimination", "--json")...)
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "contract", "status"}) {
		t.Errorf("two searches with nothing indexed asked the plugin %v", got)
	}

	// One Source is indexed: it is searched, and the other is left out.
	s.ok(t, "source", "index", "linear-algebra", strang)
	before = len(s.calls(t))
	check("evidence_search.txt", cli.ExitOK, append(search, "elimination", "produces", "triangular")...)
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "search"}) {
		t.Errorf("a search asked the plugin %v", got)
	}
	check("evidence_search.json", cli.ExitOK, append(search, "elimination produces triangular", "--json")...)
	s.ok(t, "source", "index", "linear-algebra")
	check("evidence_search_limit.txt", cli.ExitOK, append(search, "elimination", "--limit", "1", "--source", strang)...)
	check("evidence_search_nothing_found.txt", cli.ExitOK, append(search, "zebra")...)
	// A Passage's text is the plugin's: on a terminal it is escaped, and in
	// JSON it is a string like any other.
	escaped := check("evidence_search_escaped.txt", cli.ExitOK, append(search, "pivot")...)
	if strings.ContainsAny(escaped.stdout, "\x1b\t") || !strings.Contains(escaped.stdout, `\x1b[2J`) {
		t.Errorf("a Passage's text reached the terminal as it is: %q", escaped.stdout)
	}
	if r := check("evidence_search_escaped.json", cli.ExitOK, append(search, "pivot", "--json")...); strings.Contains(r.stdout, "\x1b") {
		t.Errorf("a Passage's control character is in the JSON as it is: %q", r.stdout)
	}

	// A quote is recorded with the Passage it is from, and checked.
	ids := s.passages(t, "elimination produces triangular")
	if len(ids) != 3 {
		t.Fatalf("the search found the Passages %v, want three", ids)
	}
	quote := []string{"--source", strang, "--quote", "Elimination produces an upper triangular system.", "--passage", ids[0]}
	check("evidence_record_passage_dry_run.txt", cli.ExitOK, append(append(slices.Clone(record), quote...), "--dry-run")...)
	before = len(s.calls(t))
	check("evidence_record_passage.json", cli.ExitOK, append(append(slices.Clone(record), quote...), "--json")...)
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "passage"}) {
		t.Errorf("recording Evidence with a Passage asked the plugin %v", got)
	}
	check("evidence_record_passage_again.txt", cli.ExitOK, append(slices.Clone(record), quote...)...)
	// A quote that runs from one Passage into the next names both.
	check("evidence_record_passages.txt", cli.ExitOK, append(slices.Clone(record), "--source", strang,
		"--quote", "after elimination. Back substitution solves", "--passage", ids[1], "--passage", ids[2])...)
	// A paraphrase is refused, and so is a location beside a Passage.
	paraphrase := []string{"--source", strang, "--quote", "Elimination gives an upper triangular system.", "--passage", ids[0]}
	check("evidence_record_paraphrase.json", cli.ExitUsage, append(append(slices.Clone(record), paraphrase...), "--json")...)
	if r := s.run(t, append(slices.Clone(record), paraphrase...)...); r.code != cli.ExitUsage || r.stdout != "" ||
		!strings.Contains(r.stderr, "the quote is not in the text of Passage <passage>, so nothing was recorded") {
		t.Errorf("a paraphrase: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	check("evidence_record_passage_with_location.json", cli.ExitUsage, append(append(slices.Clone(record), quote...),
		"--location", "p. 46", "--location-from", "source", "--json")...)
	// Evidence without a Passage is recorded as ever, and asks no plugin.
	before = len(s.calls(t))
	s.ok(t, append(slices.Clone(record), "--source", notes, "--quote", "A sentence the agent read itself.",
		"--location", "§2", "--location-from", "source")...)
	if got := asked(before); len(got) != 0 {
		t.Errorf("recording Evidence without a Passage asked the plugin %v", got)
	}
	check("evidence_list_checked.txt", cli.ExitOK, "evidence", "list", "linear-algebra")
	check("evidence_list_checked.json", cli.ExitOK, "evidence", "list", "linear-algebra", "--json")

	// What cannot be searched, and why.
	check("evidence_search_no_query.json", cli.ExitUsage, append(search, "--json")...)
	check("evidence_search_unknown_source.json", cli.ExitError, append(search, "elimination", "--source", "other.abc123", "--json")...)
	check("evidence_search_unknown_topic.json", cli.ExitError, "evidence", "search", "biology", "cells", "--json")
	s.ok(t, "topic", "create", "--title", "C")
	check("evidence_search_no_plugin.json", cli.ExitError, "evidence", "search", "c", "pointers", "--json")
	// A human failure goes to stderr, with what to do, and nothing to stdout.
	if r := s.run(t, "evidence", "search", "c", "pointers"); r.code != cli.ExitError || r.stdout != "" ||
		!strings.Contains(r.stderr, "Topic c names no Knowledge base plugin, so there is nothing to search: read its Sources yourself (study source list c shows where they are)") {
		t.Errorf("study evidence search of a Topic that names no plugin: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	// The plugin is gone: nothing is searched, and nothing recorded.
	s.registerWith(t, "--exit", "3")
	check("evidence_search_plugin_gone.json", cli.ExitError, append(search, "elimination", "--json")...)
	check("evidence_record_plugin_gone.json", cli.ExitError, append(slices.Clone(record), "--source", strang,
		"--quote", "The pivots are on the diagonal", "--passage", ids[1], "--json")...)
	check("evidence_list_after_plugin_gone.txt", cli.ExitOK, "evidence", "list", "linear-algebra", "--lesson", "elimination")
	s.nothingRunning(t)
}

// A Passage's text is the plugin's, and the text of Evidence is the
// History's, which syncs and can be written by hand. Neither is a
// terminal's to interpret: for people each line is escaped, and as JSON
// every character a terminal acts on is an escape of the document, which
// says the same text all the same.
func TestEvidenceTextIsNeverATerminalsToInterpret(t *testing.T) {
	// An escape sequence, a C1 control that begins one, DEL, a character
	// that changes the direction text is shown in, a separator of lines,
	// and a tag character, which shows nothing and is read by a program.
	const tag = "\U000E0041"
	const raw = "\x1b\U0000009b\x7f\U0000202e\U00002028" + tag
	const text = "The pivot\x1b[2J is\U0000009b6n the\U0000202e first\x7f entry.\U00002028Not" + tag + " a line."
	s := newShelfComputer(t)
	strang := s.book(t, "strang.txt", text+"\n")
	s.ok(t, "source", "index", "linear-algebra")
	// Evidence as another computer's History, or a hand, could hold it.
	line, err := json.Marshal(map[string]any{"format": 1, "id": "zz1", "time": "2026-10-02T10:00:00Z", "wall": "2026-10-02T10:00:00Z",
		"type": "evidence.recorded", "data": map[string]any{"id": "e9", "lesson": "pivots\x1b[2J", "source": strang,
			"quote": text, "location": "p. 1\U0000009b6n", "location_from": "source\x1b[2J"}})
	if err != nil {
		t.Fatal(err)
	}
	history, err := os.OpenFile(filepath.Join(s.env["STUDY_HOME"], "linear-algebra", "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"study evidence search": {"evidence", "search", "linear-algebra", "pivot"},
		"study evidence list":   {"evidence", "list", "linear-algebra"},
	} {
		r := s.ok(t, args...)
		if strings.ContainsAny(r.stdout, raw) || !strings.Contains(r.stdout, `The pivot\x1b[2J is`) ||
			!strings.Contains(r.stdout, `entry.\u2028Not`) {
			t.Errorf("%s printed text a terminal interprets, or does not show: %q", name, r.stdout)
		}
		r = s.ok(t, append(args, "--json")...)
		if strings.ContainsAny(r.stdout, raw) || r.stderr != "" {
			t.Errorf("%s --json printed text a terminal interprets: %q (stderr %q)", name, r.stdout, r.stderr)
		}
		// The document is JSON, and says the text as it is.
		var out struct {
			Data struct {
				Passages []core.FoundPassage `json:"passages"`
				Evidence []core.Evidence     `json:"evidence"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatalf("%s --json printed no JSON document: %v\n%s", name, err, r.stdout)
		}
		switch {
		case len(out.Data.Passages) == 1:
			// A Passage is handed on without its tag characters.
			if want := strings.ReplaceAll(text, tag, ""); out.Data.Passages[0].Text != want {
				t.Errorf("%s --json gives the Passage's text as %q, want %q", name, out.Data.Passages[0].Text, want)
			}
		case len(out.Data.Evidence) == 1:
			if e := out.Data.Evidence[0]; e.Quote != text || e.Lesson != "pivots\x1b[2J" || e.Location != "p. 1\U0000009b6n" {
				t.Errorf("%s --json gives the Evidence as %+v", name, e)
			}
		default:
			t.Errorf("%s --json = %s; want one Passage, or one piece of Evidence", name, r.stdout)
		}
	}
}

// --passage names one Passage each time it is given. An id is never cut at
// a comma, and one that is given and empty is a mistake, of a script whose
// variable holds nothing: it is refused, and not recorded as Evidence that
// was not checked.
func TestThePassageFlagNamesOnePassageEachTime(t *testing.T) {
	s := newShelfComputer(t)
	strang := s.book(t, "strang.txt", eliminationBook)
	s.ok(t, "source", "index", "linear-algebra")
	ids := s.passages(t, "elimination produces triangular")
	if len(ids) != 3 {
		t.Fatalf("the search found the Passages %v, want three", ids)
	}
	record := []string{"evidence", "record", "linear-algebra", "--lesson", "elimination", "--source", strang,
		"--quote", "Elimination produces an upper triangular system."}
	before := len(s.calls(t))
	for name, passages := range map[string][]string{
		"an empty id":             {"--passage", ""},
		"an id of spaces":         {"--passage", "  "},
		"an empty id after one":   {"--passage", ids[0], "--passage", ""},
		"an empty id with =":      {"--passage="},
		"an empty id, in a trial": {"--passage", "", "--dry-run"},
	} {
		r := s.run(t, append(slices.Clone(record), passages...)...)
		if r.code != cli.ExitUsage || r.stdout != "" || !strings.Contains(r.stderr, "--passage is given without a Passage's id") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want it refused", name, r.code, r.stdout, r.stderr)
		}
		r = s.run(t, append(append(slices.Clone(record), passages...), "--json")...)
		if r.code != cli.ExitUsage || r.stderr != "" || !strings.Contains(r.stdout, `"code": "usage"`) ||
			!strings.Contains(r.stdout, "--passage is given without a Passage's id") {
			t.Errorf("%s --json: exit %d, stdout %q, stderr %q; want it refused", name, r.code, r.stdout, r.stderr)
		}
	}
	// Two ids with a comma between them are one id, which is none.
	r := s.run(t, append(slices.Clone(record), "--passage", ids[0]+","+ids[1])...)
	if r.code != cli.ExitUsage || !strings.Contains(r.stderr, "is not a Passage's id") {
		t.Errorf("two ids in one --passage: exit %d, stdout %q, stderr %q; want them taken as one id, and refused", r.code, r.stdout, r.stderr)
	}
	if got := plugintest.Tools(s.calls(t)[before:]); len(got) != 0 {
		t.Errorf("what was refused asked the plugin %v", got)
	}
	if r := s.ok(t, "evidence", "list", "linear-algebra"); !strings.Contains(r.stdout, "No Evidence recorded yet.") {
		t.Errorf("what was refused was recorded: %s", r.stdout)
	}
}
