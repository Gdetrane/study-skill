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

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// A plugin that searched with less than it was set to says so in a notice,
// and evidence_search returns it to the agent in a field of its own, beside
// the Passages, which are found and returned as ever: a notice is no tool
// error. A search whose plugin says nothing returns no such field. And the
// tool asks the plugin what it asks for any search.
func TestEvidenceSearchReturnsThePluginsNotice(t *testing.T) {
	const notice = "These Passages were found by keyword alone: the embedding endpoint models.example did not answer within 10s."
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	plugins := core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond,
		GlanceTimeout: 20 * time.Second, EvidenceTimeout: 20 * time.Second, IndexPoll: 10 * time.Millisecond}
	session := connectOn(t, c.Getenv, c.Home, nil, nil, nil, plugins)
	book := filepath.Join(c.Home, "Books", "book.txt")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("Elimination produces an upper\ntriangular system.\fThe pivot is the first entry.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "kb"}})
	call(t, session, "source_add", map[string]any{"topic": "c", "file": book})
	// Indexed the one way there is, in a shell.
	k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home, Plugins: plugins})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}
	search := map[string]any{"topic": "c", "query": "elimination pivot"}

	// The plugin says nothing of its search: the agent is sent no notice,
	// and no empty one.
	res := call(t, session, "evidence_search", search)
	var plain core.PassageSearch
	decode(t, res, &plain)
	if plain.Notice != "" || len(plain.Passages) != 2 || strings.Contains(text(res), "notice") {
		t.Fatalf("evidence_search of a plugin that gives no notice sent %s", text(res))
	}

	c.Register(t, "kb", plugintest.Command("--record", record, "--notice", notice)...)
	before := len(plugintest.ReadCalls(t, record))
	res = call(t, session, "evidence_search", search)
	var found core.PassageSearch
	decode(t, res, &found)
	if found.Notice != notice {
		t.Errorf("evidence_search returned the notice %q, want the plugin's: %q", found.Notice, notice)
	}
	// What the agent reads is one JSON document that holds it under its own
	// name, in neither the note nor a Passage, and before the Passages.
	sent := text(res)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sent), &fields); err != nil || string(fields["notice"]) != `"`+notice+`"` || strings.Count(sent, notice) != 1 {
		t.Errorf("evidence_search sent %s (%v); want the notice once, in a field of its own", sent, err)
	}
	if at, passages := strings.Index(sent, `"notice"`), strings.Index(sent, `"passages"`); at < 0 || passages < at {
		t.Errorf("evidence_search sent %s; want the notice before the Passages it is about", sent)
	}
	// The rest is what it is without a notice.
	found.Notice = ""
	with, _ := json.Marshal(found)
	without, _ := json.Marshal(plain)
	if string(with) != string(without) {
		t.Errorf("with a notice evidence_search returned, the notice aside, %s\nand without one %s", with, without)
	}
	if got := plugintest.Tools(plugintest.ReadCalls(t, record)[before:]); !slices.Equal(got, []string{"contract", "status", "search"}) {
		t.Errorf("evidence_search asked a plugin that gives a notice %v", got)
	}

	// The tool's result says that there is such a field: an agent, and the
	// skill, can name it.
	described := false
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name != "evidence_search" {
			continue
		}
		schema, _ := json.Marshal(tool.OutputSchema)
		described = strings.Contains(string(schema), `"notice"`) && strings.Contains(tool.Description, "notice, when the answer has one")
	}
	if !described {
		t.Error("evidence_search does not describe notice, in its result's schema and in its description")
	}

	// A notice is the plugin's text. What a terminal would interpret in it
	// reaches the agent as an escape, in one JSON document. So does a
	// character of the tag block, which shows nothing and spells out text:
	// the agent reads of a notice what the learner reads of it.
	c.Register(t, "kb", plugintest.Command("--record", record, "--escaped", "--notice", `found\x1b[2J by keyword\nalone\U000E0049\U000E0067`)...)
	res = call(t, session, "evidence_search", search)
	decode(t, res, &found)
	if sent := text(res); !json.Valid([]byte(sent)) || strings.ContainsAny(sent, "\x1b\U000E0049\U000E0067") ||
		found.Notice != `found\u001b[2J by keyword alone\ue0049\ue0067` {
		t.Errorf("evidence_search sent %q, with the notice %q; want the plugin's words on one line, with its escape sequence "+
			"and its tag characters written out", sent, found.Notice)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("a tool left the plugin, process %d, running", started.PID)
	}
}
