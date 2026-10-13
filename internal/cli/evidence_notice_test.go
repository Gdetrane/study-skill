package cli_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// searchNotice is what a plugin says of a search it made with less than it
// was set to.
const searchNotice = "These Passages were found by keyword alone: the embedding endpoint models.example did not answer within 10s."

// A plugin that searched with less than it was set to says so in a notice,
// and study evidence search shows it: for people under the line that says
// what was found and before the Passages, so that it is read before they
// are relied on, and as JSON in a field of its own. It is no error: the
// command succeeds, and says nothing else it would not say without one. A
// search without a notice prints what it always printed.
func TestEvidenceSearchShowsThePluginsNotice(t *testing.T) {
	s := newShelfComputer(t)
	s.book(t, "strang.txt", eliminationBook)
	s.ok(t, "source", "index", "linear-algebra")
	search := []string{"evidence", "search", "linear-algebra"}
	without := map[string]string{}
	for name, args := range map[string][]string{
		"text": {"elimination produces triangular"}, "json": {"elimination produces triangular", "--json"}, "nothing found": {"zebra"},
	} {
		without[name] = s.ok(t, append(slices.Clone(search), args...)...).stdout
		if strings.Contains(without[name], "notice") || strings.Contains(without[name], "The plugin says") {
			t.Errorf("a search whose plugin gives no notice prints one (%s):\n%s", name, without[name])
		}
	}

	s.registerWith(t, "--notice", searchNotice)
	before := len(s.calls(t))
	check := func(name string, args ...string) result {
		t.Helper()
		r := s.run(t, append(slices.Clone(search), args...)...)
		if r.code != cli.ExitOK || r.stderr != "" {
			t.Errorf("%s: exit %d, stderr %q; want a search with a notice to succeed, and to say all it says on standard output",
				name, r.code, r.stderr)
		}
		golden(t, name, r.stdout)
		return r
	}
	text := check("evidence_search_notice.txt", "elimination produces triangular")
	shown := "! The plugin says of this search: " + searchNotice + "\n"
	if at, first := strings.Index(text.stdout, shown), strings.Index(text.stdout, "<passage>"); at < 0 || first < at ||
		!strings.HasPrefix(text.stdout, "Knowledge base plugin kb found 3 Passages") {
		t.Errorf("the notice is not shown under the first line and before the first Passage:\n%s", text.stdout)
	}
	// But for the notice, each prints what it prints without one.
	if got := strings.Replace(text.stdout, shown, "", 1); got != without["text"] {
		t.Errorf("with a notice the search prints, the notice aside:\n%s\nand without one:\n%s", got, without["text"])
	}
	asJSON := check("evidence_search_notice.json", "elimination produces triangular", "--json")
	var out struct {
		OK   bool               `json:"ok"`
		Data core.PassageSearch `json:"data"`
	}
	if err := json.Unmarshal([]byte(asJSON.stdout), &out); err != nil || !out.OK || out.Data.Notice != searchNotice || len(out.Data.Passages) != 3 ||
		out.Data.Note != "" || out.Data.Action != "" {
		t.Errorf("study evidence search --json = %s (%v); want the notice in a field of its own, and nothing else changed", asJSON.stdout, err)
	}
	line, _ := json.Marshal(searchNotice)
	if got := strings.Replace(asJSON.stdout, "    \"notice\": "+string(line)+",\n", "", 1); got != without["json"] {
		t.Errorf("with a notice the search prints as JSON, the notice aside:\n%s\nand without one:\n%s", got, without["json"])
	}
	// A search that found nothing says it too: nothing found by keyword
	// alone is not nothing there.
	none := check("evidence_search_notice_nothing_found.txt", "zebra")
	if got := strings.Replace(none.stdout, shown, "", 1); got != without["nothing found"] || !strings.HasSuffix(none.stdout, shown) {
		t.Errorf("a search that found nothing, with a notice, prints:\n%s\nand without one:\n%s", none.stdout, without["nothing found"])
	}
	// The plugin was asked what it is asked for any search.
	if got := plugintest.Tools(s.calls(t)[before:]); !slices.Equal(got, []string{"contract", "status", "search", "contract", "status", "search",
		"contract", "status", "search"}) {
		t.Errorf("three searches with a notice asked the plugin %v", got)
	}

	// A notice is the plugin's text, and not a terminal's to interpret. The
	// plugin's escape sequence, its line break and its mark that turns the
	// direction of what follows reach nobody as they are, and a character
	// that shows nothing is written as an escape.
	const tag = "\U000E0041"
	s.registerWith(t, "--escaped", "--notice", `found\x1b[2J by keyword\nalone\U0000202e, as\U000E0041 said`)
	hostile := s.ok(t, append(slices.Clone(search), "elimination")...)
	if strings.ContainsAny(hostile.stdout, "\x1b\U0000202e"+tag) || strings.Contains(hostile.stdout, "keyword\nalone") ||
		!strings.Contains(hostile.stdout, `found\\u001b[2J by keyword alone\\u202e, as\U000e0041 said`) {
		t.Errorf("a notice reached the terminal as the plugin wrote it: %q", hostile.stdout)
	}
	hostile = s.ok(t, append(slices.Clone(search), "elimination", "--json")...)
	if strings.ContainsAny(hostile.stdout, "\x1b\U0000202e"+tag) || !json.Valid([]byte(hostile.stdout)) {
		t.Errorf("a notice reached the terminal as the plugin wrote it, in JSON: %q", hostile.stdout)
	}
	s.nothingRunning(t)
}
