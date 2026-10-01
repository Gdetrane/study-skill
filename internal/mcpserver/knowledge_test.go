package mcpserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// TestKnowledgeSeam drives the notebooklm kind the way an agent would: choose
// the Knowledge base, add a Source it also added to the notebook, record
// Evidence from a NotebookLM citation, and read it all back.
func TestKnowledgeSeam(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	session := connect(t, home)
	call(t, session, "topic_create", map[string]any{"title": "C"})

	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c",
		"knowledge_base": map[string]any{"kind": "notebooklm", "notebook": "nb-42"}}), &updated)
	if !updated.Changed || updated.Topic.KnowledgeBase == nil || updated.Topic.KnowledgeBase.Notebook != "nb-42" {
		t.Fatalf("topic_update = %+v", updated)
	}

	book := filepath.Join(home, "Books", "k_and_r.pdf")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("%PDF-1.4 K&R"), 0o644); err != nil {
		t.Fatal(err)
	}
	var added core.SourceResult
	decode(t, call(t, session, "source_add", map[string]any{"topic": "c", "file": book, "notebooklm_id": "nlm-1"}), &added)
	if added.Source.Kind != core.SourceFile || added.Source.NotebookLMID != "nlm-1" || added.Source.Title != "K And R" {
		t.Fatalf("source_add = %+v", added)
	}

	var recorded core.EvidenceResult
	decode(t, call(t, session, "evidence_record", map[string]any{"topic": "c", "lesson": "pointers",
		"source": added.Source.ID, "quote": "A pointer is a variable that contains the address of a variable.",
		"location": "citation 2", "location_from": "knowledge_base"}), &recorded)
	if !recorded.Changed || recorded.Evidence.LocationFrom != core.LocationFromKnowledgeBase {
		t.Fatalf("evidence_record = %+v", recorded)
	}

	var sources core.SourceList
	decode(t, call(t, session, "sources", map[string]any{"topic": "c"}), &sources)
	if sources.KnowledgeBase == nil || sources.KnowledgeBase.Kind != core.KnowledgeBaseNotebookLM ||
		len(sources.Sources) != 1 || sources.Sources[0].State != core.SourceOK {
		t.Errorf("sources = %+v", sources)
	}
	var evidence core.EvidenceList
	decode(t, call(t, session, "evidence", map[string]any{"topic": "c", "lesson": "pointers"}), &evidence)
	if len(evidence.Evidence) != 1 || evidence.Evidence[0].ID != recorded.Evidence.ID {
		t.Errorf("evidence = %+v", evidence)
	}

	var renamed core.SourceResult
	decode(t, call(t, session, "source_update", map[string]any{"topic": "c", "source": added.Source.ID,
		"title": "The C Programming Language"}), &renamed)
	if !renamed.Changed || renamed.Source.Title != "The C Programming Language" {
		t.Errorf("source_update = %+v", renamed)
	}

	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "evidence_record", Arguments: map[string]any{
		"topic": "c", "lesson": "pointers", "source": "nothing.abc123", "quote": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError || !strings.Contains(text(bad), "not_found") {
		t.Errorf("Evidence from an unknown Source: IsError=%v, content %q", bad.IsError, text(bad))
	}
}
