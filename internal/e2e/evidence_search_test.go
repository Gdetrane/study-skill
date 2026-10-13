//go:build unix

package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// A built study, run as an agent runs it in its shell: a Topic's Sources
// are searched, a quote is recorded with the Passage it is from and so
// checked, and a paraphrase is refused. The plugin is started for each
// command, asked what that command may ask it, and gone when study has
// exited. Then the plugin is taken off the computer: the search and the
// checked recording say so, and record nothing.
//
// The binary gives a plugin the time it gives one anywhere, which no test
// can change: nothing here is about a plugin that is late, and nothing is
// said about how long anything took.
func TestABuiltStudySearchesAndChecksAQuote(t *testing.T) {
	c := newPluginComputer(t)
	c.must("topic", "create", "--title", "C", "--json")
	c.must("topic", "update", "c", "--knowledge-base", "kb", "--json")
	book := filepath.Join(c.home, "Books", "book.txt")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("Elimination produces an upper\ntriangular system.\fThe pivots are on the diagonal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var added struct {
		Data core.SourceResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(c.must("source", "add", "c", "--file", book, "--json")), &added); err != nil {
		t.Fatal(err)
	}
	source := added.Data.Source.ID
	// asked are the tools the plugin was asked since it had received before
	// calls, and gone fails the test when the plugin's last program is
	// still running.
	asked := func(before int) []string { return plugintest.Tools(plugintest.ReadCalls(t, c.record)[before:]) }
	received := func() int { return len(plugintest.ReadCalls(t, c.record)) }
	gone := func(after string) {
		t.Helper()
		if plugin := plugintest.ReadRecord(t, c.record); !plugintest.Gone(plugin.PID, 5*time.Second) {
			plugintest.Kill(plugin.PID)
			t.Errorf("the plugin, process %d, outlived study %s", plugin.PID, after)
		}
	}
	search := func(args ...string) core.PassageSearch {
		t.Helper()
		var found struct {
			Data core.PassageSearch `json:"data"`
		}
		if err := json.Unmarshal([]byte(c.must("evidence", append([]string{"search", "c"}, append(args, "--json")...)...)), &found); err != nil {
			t.Fatal(err)
		}
		return found.Data
	}

	// Nothing is indexed: the search says so, and names the command.
	before := received()
	if nothing := search("elimination"); len(nothing.Passages) != 0 || len(nothing.LeftOut) != 1 || nothing.Action != "study source index c" {
		t.Errorf("study evidence search with nothing indexed = %+v", nothing)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status"}) {
		t.Errorf("a search with nothing indexed asked the plugin %v", got)
	}
	gone("evidence search")
	c.must("source", "index", "c", "--json")

	before = received()
	found := search("elimination", "pivots", "--limit", "5")
	if len(found.Passages) != 2 || found.Passages[0].Text != "Elimination produces an upper\ntriangular system." ||
		found.Passages[0].Location != "p. 1" || found.Passages[1].Location != "p. 2" || !slices.Equal(found.Searched, []string{source}) {
		t.Fatalf("study evidence search = %+v", found)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "search"}) {
		t.Errorf("a search asked the plugin %v", got)
	}
	gone("evidence search")
	// For people, the Passages with their ids, the text line by line.
	if stdout, stderr, err := c.run("evidence", "search", "c", "elimination"); err != nil ||
		!strings.Contains(stdout, found.Passages[0].ID) || !strings.Contains(stdout, "  > Elimination produces an upper\n  > triangular system.\n") {
		t.Errorf("study evidence search, for people: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// The quote is recorded with the Passage's id, and so checked.
	before = received()
	record := []string{"record", "c", "--lesson", "elimination", "--source", source, "--passage", found.Passages[0].ID, "--json"}
	var recorded struct {
		Data core.EvidenceResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(c.must("evidence", append(record, "--quote", "Elimination produces an upper triangular system.")...)), &recorded); err != nil {
		t.Fatal(err)
	}
	if e := recorded.Data.Evidence; !recorded.Data.Changed || !e.Checked || !slices.Equal(e.Passages, []string{found.Passages[0].ID}) ||
		e.Plugin != "kb" || e.Location != "p. 1" || e.LocationFrom != core.LocationFromKnowledgeBase {
		t.Errorf("study evidence record --passage = %+v", recorded.Data)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "passage"}) {
		t.Errorf("recording Evidence with a Passage asked the plugin %v", got)
	}
	gone("evidence record")
	// A paraphrase is refused as a mistake of the command line's, exit 2.
	stdout, _, err := c.run("evidence", append(record, "--quote", "Elimination yields a triangular system.")...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(stdout, `"code": "invalid_argument"`) ||
		!strings.Contains(stdout, "the quote is not in the text of Passage "+found.Passages[0].ID) {
		t.Errorf("a paraphrase: %v\nstdout: %s", err, stdout)
	}
	gone("evidence record")

	// The plugin is taken off the computer: nothing is searched, and the
	// Evidence that names a Passage is not recorded.
	c.must("knowledge-base", "remove", "kb", "--json")
	for name, args := range map[string][]string{
		"evidence search": {"search", "c", "elimination", "--json"},
		"evidence record": append(record, "--quote", "The pivots are on the diagonal."),
	} {
		stdout, _, err := c.run("evidence", args...)
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stdout, `"code": "failed_precondition"`) ||
			!strings.Contains(stdout, "kb, which is not registered on this computer") {
			t.Errorf("study %s without the plugin: %v\nstdout: %s", name, err, stdout)
		}
	}
	var list struct {
		Data core.EvidenceList `json:"data"`
	}
	if err := json.Unmarshal([]byte(c.must("evidence", "list", "c", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data.Evidence) != 1 || !list.Data.Evidence[0].Checked {
		t.Errorf("study evidence list = %+v; want the one quote that was checked", list.Data.Evidence)
	}
}
