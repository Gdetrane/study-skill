package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/mcpserver"
)

// connect starts the server over the SDK's in-memory transport and returns a
// connected client session.
func connect(t *testing.T, home string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	var n atomic.Int64
	c, err := core.Open(core.Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
		Now:    func() time.Time { return time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC) },
		NewID:  func() string { return fmt.Sprintf("id%03d", n.Add(1)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(c, "test").Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestServerSendsInstructionsAndTools(t *testing.T) {
	session := connect(t, t.TempDir())
	if got := session.InitializeResult().Instructions; got != mcpserver.Instructions {
		t.Errorf("instructions = %q", got)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Name == "status" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Error("status should be marked read-only")
		}
	}
	if strings.Join(names, ",") != "checkpoint,library_search,status,topic_create" {
		t.Errorf("tools = %v", names)
	}
}

func TestCreateTopicThenStatus(t *testing.T) {
	ctx := context.Background()
	session := connect(t, t.TempDir())

	created := call(t, session, "topic_create", map[string]any{"title": "Linear algebra", "goal": "Solve systems by hand"})
	var topic core.Topic
	decode(t, created, &topic)
	if topic.ID != "linear-algebra" || topic.Goal != "Solve systems by hand" {
		t.Fatalf("created %+v", topic)
	}

	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if status.ActiveTopic == nil || status.ActiveTopic.ID != "linear-algebra" || status.ActiveTopic.ChosenBy != core.ChosenByRecent {
		t.Fatalf("active topic = %+v", status.ActiveTopic)
	}

	duplicate, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "topic_create", Arguments: map[string]any{"title": "Linear algebra"}})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.IsError || !strings.Contains(text(duplicate), "already_exists") {
		t.Errorf("duplicate topic: IsError=%v, content %q", duplicate.IsError, text(duplicate))
	}
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	return res
}

func decode(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("structured content %s: %v", data, err)
	}
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestStatusOnAnEmptyStudyHome(t *testing.T) {
	session := connect(t, t.TempDir())
	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if status.ActiveTopic != nil || len(status.Topics) != 0 || len(status.Problems) != 0 {
		t.Errorf("status = %+v", status)
	}
}

func TestLibrarySearch(t *testing.T) {
	home := t.TempDir()
	books := filepath.Join(home, "Books", "Programming")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "The_C_Programming_Language.pdf"), []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := core.Open(core.Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BuildLibrary(context.Background(), "Books"); err != nil {
		t.Fatal(err)
	}

	session := connect(t, home)
	var out struct {
		Results []struct {
			Title string `json:"title"`
			Path  string `json:"path"`
			Score int    `json:"score"`
		} `json:"results"`
	}
	decode(t, call(t, session, "library_search", map[string]any{"query": "C"}), &out)
	if len(out.Results) != 1 || out.Results[0].Title != "The C Programming Language" || out.Results[0].Score <= 0 {
		t.Fatalf("results = %+v", out.Results)
	}

	empty, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "library_search", Arguments: map[string]any{"query": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !empty.IsError || !strings.Contains(text(empty), "invalid_argument") {
		t.Errorf("empty query: IsError=%v, content %q", empty.IsError, text(empty))
	}
}

func TestCheckpoint(t *testing.T) {
	ctx := context.Background()
	withGitIdentity(t)
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C"})

	var first core.CheckpointResult
	decode(t, call(t, session, "checkpoint", map[string]any{"topic": "c", "role": "agent", "message": "Lesson 1 notes"}), &first)
	if !first.Committed || first.Commit == "" {
		t.Fatalf("first checkpoint = %+v", first)
	}
	var again core.CheckpointResult
	decode(t, call(t, session, "checkpoint", map[string]any{"topic": "c", "role": "learner"}), &again)
	if again.Committed || again.Commit != first.Commit {
		t.Errorf("unchanged checkpoint = %+v, want a skip at %s", again, first.Commit)
	}

	bad, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "checkpoint", Arguments: map[string]any{"topic": "c", "role": "tutor"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError || !strings.Contains(text(bad), "invalid_argument") {
		t.Errorf("unknown role: IsError=%v, content %q", bad.IsError, text(bad))
	}
}

// withGitIdentity gives git a fixed identity through a temporary HOME, as a
// learner's global configuration would, and hides the developer's own.
func withGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config := "[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}
