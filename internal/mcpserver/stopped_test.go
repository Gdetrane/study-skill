package mcpserver_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// TestNoStoppedOperationReportsInternal stops every operation of the core
// before it starts, as SIGTERM, Ctrl-C or a client that cancels its request
// stop one, and checks what each says: that it was canceled, or its result
// when it never needed to look, and never internal (docs/cli.md).
//
// The operations with a tool are stopped through the session of
// TestNoToolRunsACheck, which calls every tool with valid input: each call
// is made twice, first with a context that is already cancelled, then as the
// session made it. The others are for the command line alone and are called
// here; an operation that is in neither place fails the test.
func TestNoStoppedOperationReportsInternal(t *testing.T) {
	var mu sync.Mutex
	// said holds, for each tool, what each of its stopped calls answered:
	// an error's text, or nothing for a result.
	said := map[string][]string{}
	stopFirst := func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" {
				stopped, cancel := context.WithCancel(ctx)
				cancel()
				answer := ""
				res, err := next(stopped, method, req)
				if result, ok := res.(*mcp.CallToolResult); err != nil {
					answer = err.Error()
				} else if ok && result.IsError {
					answer = text(result)
				}
				mu.Lock()
				said[params.Name] = append(said[params.Name], answer)
				mu.Unlock()
			}
			return next(ctx, method, req)
		}
	}
	home, c := everyTool(t, func(s *mcp.Server) { s.AddReceivingMiddleware(stopFirst) })

	mu.Lock()
	defer mu.Unlock()
	canceled := func(answer string) bool { return strings.HasPrefix(answer, "canceled: ") }
	for tool, answers := range said {
		for _, answer := range answers {
			if answer != "" && !canceled(answer) {
				t.Errorf("%s, stopped, said %q; want canceled", tool, answer)
			}
		}
	}
	// What waits for the Topic's lock looks at its context first.
	for _, tool := range []string{"topic_update", "checkpoint", "session_open", "card_add"} {
		if !slices.ContainsFunc(said[tool], canceled) {
			t.Errorf("%s, stopped, said %q, not canceled: the calls are not stopped, so this test proves nothing", tool, said[tool])
		}
	}

	// The operations of the command line alone, each with valid input: once
	// it is not stopped, the same call does its work.
	ctx := context.Background()
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	v1 := filepath.Join(t.TempDir(), "go")
	if err := os.MkdirAll(v1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, ".study-config.json"), []byte(`{"version": 3, "topic": "Go"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "ran")
	ran, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := c.ProposeRevision(ctx, "d", core.RevisionSpec{Summary: "A first Syllabus", Syllabus: core.Syllabus{
		Milestones: []core.Milestone{{ID: "basics", Title: "Basics", Lessons: []core.SyllabusLesson{{ID: "answer", Title: "The answer"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The plugin registry is read only from outside the Study home, which is
	// where the session's HOME is. One plugin is registered there, for
	// study knowledge-base check to start: this test binary, run as the test
	// plugin.
	apart := t.TempDir()
	registry, err := json.Marshal(map[string]any{"format": 1, "plugins": []any{
		map[string]any{"name": "kb", "command": plugintest.Command()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(apart, ".config", "lamplight"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apart, ".config", "lamplight", "knowledge-base-plugins.json"), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	outside, err := core.Open(core.Options{Getenv: func(key string) string {
		return map[string]string{"STUDY_HOME": home, "HOME": apart}[key]
	}, Dir: home, Plugins: core.PluginOptions{StopGrace: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	// Topic c names that plugin, for study source index to have its Source
	// indexed.
	if _, err := outside.UpdateTopic(ctx, "c", core.TopicChanges{
		KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin, Plugin: "kb"}}); err != nil {
		t.Fatal(err)
	}
	// In this order: the Topic removed is the one restored.
	commandLine := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"ActiveTopic", func(ctx context.Context) error {
			_, err := c.ActiveTopic(ctx)
			return err
		}},
		{"BuildLibrary", func(ctx context.Context) error {
			_, err := c.BuildLibrary(ctx, "Books")
			return err
		}},
		{"ListAssessments", func(ctx context.Context) error {
			_, err := c.ListAssessments(ctx, "c")
			return err
		}},
		{"Plugins", func(ctx context.Context) error {
			_, err := outside.Plugins(ctx)
			return err
		}},
		{"CheckPlugin", func(ctx context.Context) error {
			_, err := outside.CheckPlugin(ctx, "kb")
			return err
		}},
		{"CheckPlugins", func(ctx context.Context) error {
			_, err := outside.CheckPlugins(ctx)
			return err
		}},
		{"IndexSources", func(ctx context.Context) error {
			_, err := outside.IndexSources(ctx, core.IndexSpec{Topic: "c"})
			return err
		}},
		{"ProposedRevision", func(ctx context.Context) error {
			_, err := c.ProposedRevision(ctx, "d", proposed.Revision)
			return err
		}},
		{"RunCheck", func(ctx context.Context) error {
			_, err := c.RunCheck(ctx, "c", "answer", core.CheckOptions{})
			return err
		}},
		{"ImportV1", func(ctx context.Context) error {
			_, err := c.ImportV1(ctx, core.ImportSpec{Dir: v1})
			return err
		}},
		{"RemoveTopic", func(ctx context.Context) error {
			_, err := c.RemoveTopic(ctx, "d", false)
			return err
		}},
		{"ListRemovedTopics", func(ctx context.Context) error {
			_, err := c.ListRemovedTopics(ctx)
			return err
		}},
		{"RestoreTopic", func(ctx context.Context) error {
			_, err := c.RestoreTopic(ctx, core.TopicRestoreSpec{Topic: "d"})
			return err
		}},
	}
	listed := map[string]bool{}
	for _, op := range commandLine {
		listed[op.name] = true
		if err := op.call(stopped); err != nil && core.CodeOf(err) != core.CodeCanceled {
			t.Errorf("%s, stopped, said %v (%s); want canceled", op.name, err, core.CodeOf(err))
		}
		if op.name == "RunCheck" {
			if now, err := os.ReadFile(marker); err != nil || string(now) != string(ran) {
				t.Errorf("a stopped Check ran its commands: %q, %v", now, err)
			}
		}
		if err := op.call(ctx); err != nil {
			t.Errorf("%s, not stopped: %v", op.name, err)
		}
	}

	// Every operation that takes a context was stopped above: through a
	// tool, or by name.
	served := namesInTheServer(t)
	operations := reflect.TypeFor[*core.Core]()
	for i := range operations.NumMethod() {
		op := operations.Method(i)
		if op.Type.NumIn() < 2 || op.Type.In(1) != reflect.TypeFor[context.Context]() {
			continue
		}
		if !served[op.Name] && !listed[op.Name] {
			t.Errorf("core.%s takes a context, and neither a tool calls it nor this test: add it to commandLine", op.Name)
		}
	}
}

// namesInTheServer are the names the server's own source selects, the
// core's operations it calls among them.
func namesInTheServer(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				names[sel.Sel.Name] = true
			}
			return true
		})
	}
	if !names["CreateTopic"] {
		t.Fatal("the server's source was not read")
	}
	return names
}
