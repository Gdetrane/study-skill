package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// An agent searches a Topic's Sources, quotes a Passage and records the
// quote with the Passage's id, which has it checked: what each tool gives
// back, what it has the plugin asked, and what it says when the quote is no
// quote, and when the plugin is gone.
func TestAnAgentSearchesAndRecordsCheckedEvidence(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	// A plugin has twenty seconds for whatever it is asked: nothing here is
	// about one that is late.
	plugins := core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond,
		GlanceTimeout: 20 * time.Second, EvidenceTimeout: 20 * time.Second, IndexPoll: 10 * time.Millisecond}
	session := connectOn(t, c.Getenv, c.Home, nil, nil, nil, plugins)
	// asked are the tools the plugin was asked since it had received
	// before calls.
	asked := func(before int) []string { return plugintest.Tools(plugintest.ReadCalls(t, record)[before:]) }
	received := func() int { return len(plugintest.ReadCalls(t, record)) }

	book := filepath.Join(c.Home, "Books", "book.txt")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	// The second page holds what a terminal would interpret.
	if err := os.WriteFile(book, []byte("Elimination produces an upper\ntriangular system.\fThe pivot\x1b[2J is the first entry.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "kb"}})
	var added core.SourceResult
	decode(t, call(t, session, "source_add", map[string]any{"topic": "c", "file": book}), &added)

	// Nothing is indexed yet: the search says so, with the command that
	// has it indexed, and the plugin was not asked to search.
	before := received()
	var nothing core.PassageSearch
	decode(t, call(t, session, "evidence_search", map[string]any{"topic": "c", "query": "elimination"}), &nothing)
	if len(nothing.Passages) != 0 || len(nothing.Searched) != 0 || len(nothing.LeftOut) != 1 || nothing.LeftOut[0].ID != added.Source.ID ||
		nothing.LeftOut[0].State != core.IndexNotIndexed || !strings.Contains(nothing.Note, "nothing was searched") ||
		nothing.Action != "study source index c" {
		t.Errorf("evidence_search with nothing indexed = %+v", nothing)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status"}) {
		t.Errorf("evidence_search with nothing indexed asked the plugin %v", got)
	}
	// Indexed the one way there is, in a shell.
	k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home, Plugins: plugins})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}

	before = received()
	res := call(t, session, "evidence_search", map[string]any{"topic": "c", "query": "elimination pivot", "limit": 5,
		"sources": []any{added.Source.ID}})
	var found core.PassageSearch
	decode(t, res, &found)
	if found.Plugin != "kb" || !slices.Equal(found.Searched, []string{added.Source.ID}) || len(found.LeftOut) != 0 || found.Note != "" ||
		len(found.Passages) != 2 || found.Passages[0].Text != "Elimination produces an upper\ntriangular system." ||
		found.Passages[0].Location != "p. 1" || found.Passages[0].Source != added.Source.ID || found.Passages[0].ID == "" ||
		found.Passages[1].Text != "The pivot\x1b[2J is the first entry." || found.Passages[1].Location != "p. 2" {
		t.Fatalf("evidence_search = %+v", found)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "search"}) {
		t.Errorf("evidence_search asked the plugin %v", got)
	}
	// What the agent is sent is JSON, with the Passage's text as a string
	// of it: nothing of the plugin's is there as it is.
	if sent := text(res); !json.Valid([]byte(sent)) || strings.Contains(sent, "\x1b") || !strings.Contains(sent, `\u001b[2J`) {
		t.Errorf("evidence_search sent %q; want one JSON document, with the Passage's control characters escaped", sent)
	}

	// The quote is recorded with the Passage's id, and so checked.
	before = received()
	var recorded core.EvidenceResult
	decode(t, call(t, session, "evidence_record", map[string]any{"topic": "c", "lesson": "elimination", "source": added.Source.ID,
		"quote": "Elimination produces an upper triangular system.", "passages": []any{found.Passages[0].ID}}), &recorded)
	if e := recorded.Evidence; !recorded.Changed || !e.Checked || !slices.Equal(e.Passages, []string{found.Passages[0].ID}) ||
		e.Plugin != "kb" || e.Location != "p. 1" || e.LocationFrom != core.LocationFromKnowledgeBase {
		t.Errorf("evidence_record = %+v", recorded)
	}
	if got := asked(before); !slices.Equal(got, []string{"contract", "status", "passage"}) {
		t.Errorf("evidence_record with a Passage asked the plugin %v", got)
	}
	// A paraphrase is refused, with what to do, and nothing is recorded.
	refused, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_record", Arguments: map[string]any{"topic": "c",
		"lesson": "elimination", "source": added.Source.ID, "quote": "Elimination yields a triangular system.",
		"passages": []any{found.Passages[0].ID}}})
	if err != nil || !refused.IsError || !strings.Contains(text(refused), "invalid_argument: the quote is not in the text of Passage "+found.Passages[0].ID) ||
		!strings.Contains(text(refused), "quote the Passage as the search gave it, never a paraphrase or a summary") {
		t.Errorf("a paraphrase: %v, %s", err, text(refused))
	}
	// Evidence without a Passage is recorded, unchecked, and asks no plugin.
	before = received()
	var plain core.EvidenceResult
	decode(t, call(t, session, "evidence_record", map[string]any{"topic": "c", "lesson": "elimination", "source": added.Source.ID,
		"quote": "A sentence the agent read itself.", "location": "p. 3", "location_from": "source"}), &plain)
	if !plain.Changed || plain.Evidence.Checked || len(plain.Evidence.Passages) != 0 {
		t.Errorf("evidence_record without a Passage = %+v", plain)
	}
	if got := asked(before); len(got) != 0 {
		t.Errorf("evidence_record without a Passage asked the plugin %v", got)
	}
	var list core.EvidenceList
	decode(t, call(t, session, "evidence", map[string]any{"topic": "c"}), &list)
	if len(list.Evidence) != 2 || !list.Evidence[0].Checked || list.Evidence[1].Checked {
		t.Errorf("evidence = %+v; want the checked quote and the unchecked one, and no paraphrase", list.Evidence)
	}

	// The plugin is gone: the search says so, and Evidence that names a
	// Passage is not recorded.
	c.Register(t, "kb", plugintest.Command("--record", record, "--exit", "3")...)
	for tool, args := range map[string]map[string]any{
		"evidence_search": {"topic": "c", "query": "elimination"},
		"evidence_record": {"topic": "c", "lesson": "elimination", "source": added.Source.ID, "quote": "The pivot",
			"passages": []any{found.Passages[1].ID}},
	} {
		gone, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || !gone.IsError || !strings.Contains(text(gone), "failed_precondition: Knowledge base plugin kb stopped before it could") {
			t.Errorf("%s with a plugin that is gone: %v, %s", tool, err, text(gone))
		}
	}
	decode(t, call(t, session, "evidence", map[string]any{"topic": "c"}), &list)
	if len(list.Evidence) != 2 {
		t.Errorf("Evidence was recorded with a plugin that is gone: %+v", list.Evidence)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("a tool left the plugin, process %d, running", started.PID)
	}
}
