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

// TestKnowledgeSeam drives the Knowledge seam the way an agent would: choose
// the Knowledge base, add a Source, record Evidence with the location a
// Knowledge base gave, and read it all back.
func TestKnowledgeSeam(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	session := connect(t, home)
	call(t, session, "topic_create", map[string]any{"title": "C"})

	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c",
		"knowledge_base": map[string]any{"kind": "none"}}), &updated)
	if !updated.Changed || updated.Topic.KnowledgeBase == nil || updated.Topic.KnowledgeBase.Kind != core.KnowledgeBaseNone {
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
	decode(t, call(t, session, "source_add", map[string]any{"topic": "c", "file": book}), &added)
	if added.Source.Kind != core.SourceFile || added.Source.Title != "K And R" {
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
	if sources.KnowledgeBase == nil || sources.KnowledgeBase.Kind != core.KnowledgeBaseNone ||
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

	var retracted core.EvidenceResult
	decode(t, call(t, session, "evidence_retract", map[string]any{"topic": "c", "evidence": recorded.Evidence.ID}), &retracted)
	if !retracted.Changed || !retracted.Evidence.Retracted {
		t.Errorf("evidence_retract = %+v", retracted)
	}
	decode(t, call(t, session, "evidence", map[string]any{"topic": "c"}), &evidence)
	if len(evidence.Evidence) != 0 {
		t.Errorf("evidence after retracting = %+v", evidence)
	}
	decode(t, call(t, session, "evidence", map[string]any{"topic": "c", "all": true}), &evidence)
	if len(evidence.Evidence) != 1 || !evidence.Evidence[0].Retracted {
		t.Errorf("all evidence = %+v", evidence)
	}

	for _, tc := range []struct {
		tool string
		args map[string]any
		code string
	}{
		{"evidence_record", map[string]any{"topic": "c", "lesson": "pointers", "source": "nothing.abc123", "quote": "x"}, "not_found"},
		// The server's folder is not the agent's: relative paths are refused.
		{"source_add", map[string]any{"topic": "c", "file": "Books/k_and_r.pdf"}, "invalid_argument"},
		{"source_update", map[string]any{"topic": "c", "source": added.Source.ID, "path": "k_and_r.pdf"}, "invalid_argument"},
		// A kind this version does not have.
		{"topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "rag"}}, "invalid_argument"},
	} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.HasPrefix(text(res), tc.code) {
			t.Errorf("%s %v: IsError=%v, content %q, want %s", tc.tool, tc.args, res.IsError, text(res), tc.code)
		}
	}
	// ~/ is the home folder.
	if err := os.WriteFile(filepath.Join(home, "notes.pdf"), []byte("%PDF-1.4 notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var tilde core.SourceResult
	decode(t, call(t, session, "source_add", map[string]any{"topic": "c", "file": "~/notes.pdf"}), &tilde)
	if tilde.Path != filepath.Join(home, "notes.pdf") {
		t.Errorf("source_add ~/notes.pdf = %+v", tilde)
	}
}
