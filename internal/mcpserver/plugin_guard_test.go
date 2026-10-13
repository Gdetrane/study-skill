package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

// The three tests of plugin_index_test.go say that no MCP tool has a
// Knowledge base plugin index, stop indexing, or test what it depends on
// (ADR-0012). A review found ways around all three at once, each of which a
// test here closes:
//
//   - a method of the client that calls another tool's method when its own
//     call fails, so that a search that failed asked diagnose: the test of
//     the client holds it to calling none
//     (TestTheClientAsksEachToolInOneMethod);
//   - the core handing a Connection to a function of another package, which
//     asks it through an interface, and a package between the server and the
//     core that calls what only the command line may: every package the
//     server links is read, not the core alone
//     (TestNoPackageTheServerLinksReachesAPluginBesidesTheCore);
//   - any of them, where it happened only when something was wrong, a
//     plugin whose status fails or a Topic with a problem, which the one
//     session of TestNoToolHasAPluginIndex never met
//     (TestNoToolAsksAPluginForMoreWhenSomethingIsWrong).

// commandLineOnly are the operations of the core that start a plugin for
// the learner alone: to check it, which has it test what it depends on, and
// to index. The command line names them. No package the server links does,
// but the core, which defines them.
var commandLineOnly = []string{"CheckPlugin", "CheckPlugins", "IndexSources"}

// linkedPackages are the packages of this module that the MCP server is
// built from, each with its folder.
func linkedPackages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range goList(t, "-deps", "-f", "{{.ImportPath}} {{.Dir}}", "./internal/mcpserver") {
		pkg, dir, _ := strings.Cut(line, " ")
		if strings.HasPrefix(pkg, module+"/") {
			out[pkg] = dir
		}
	}
	return out
}

// outsideTheCore reads source files of one package the server links, which
// is not the core, and returns what in them could have a plugin asked for
// what only the command line may ask: by whatever the plugin is reached
// through there, a Connection, an interface that one fits, or the core.
//
// It goes by names, as the reading of the core does. Index, Cancel and
// Diagnose are used nowhere outside the core, as a call or as a value: no
// type of these packages has a field or a method of such a name but the
// Connection itself, which defines them. Search and Passage are names of
// fields of the contract's types too, so only a call of them counts. And
// the operations of the core that are the command line's alone are named
// nowhere.
func outsideTheCore(t *testing.T, pkg string, sources []coreSource) (problems []string) {
	t.Helper()
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, source := range sources {
		file, err := parser.ParseFile(token.NewFileSet(), source.name, source.text, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		// called are the selectors that are called: x.Search(...).
		called := map[*ast.SelectorExpr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					called[sel] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				name := x.Sel.Name
				switch {
				case slices.Contains([]string{"Index", "Cancel", "Diagnose"}, name), called[x] && (name == "Search" || name == "Passage"):
					problem("%s (%s) uses .%s, which a Knowledge base plugin's Connection has: outside the core nothing asks a "+
						"plugin to index, to stop, to search, for a Passage or to test what it depends on, by whatever stands for the "+
						"Connection there. What asks it is in the core, where another test reads who may (ADR-0012)", source.name, pkg, name)
				case name == "MethodByName":
					problem("%s (%s) calls a method by a name made at run time (MethodByName), which this test cannot read: a "+
						"plugin's index or diagnose tool could be asked so", source.name, pkg)
				}
			case *ast.Ident:
				if slices.Contains(commandLineOnly, x.Name) {
					problem("%s (%s) names %s: only the command line calls it, and no package the MCP server is built from but "+
						"the core, which defines it. A tool that reached it through this package would start a plugin to check "+
						"it, which has it test what it depends on, or have one index (ADR-0012)", source.name, pkg, x.Name)
				}
			}
			return true
		})
	}
	return problems
}

// TestNoPackageTheServerLinksReachesAPluginBesidesTheCore reads every
// package of this module that the MCP server is built from, the ones a later
// change adds among them, and not the core alone: the core is read by
// TestTheCoreAsksAPluginToIndexInOnePlace, which knows who in it may ask a
// plugin what, and that says who has a plugin index or test what it depends
// on only while nothing asks it from elsewhere.
func TestNoPackageTheServerLinksReachesAPluginBesidesTheCore(t *testing.T) {
	linked := linkedPackages(t)
	for _, pkg := range []string{module + "/internal/core", module + "/internal/mcpserver", pluginClient, module + "/internal/plugincontract"} {
		if linked[pkg] == "" {
			t.Fatalf("go list -deps ./internal/mcpserver does not list %s, so this test proves nothing: it lists %v", pkg, linked)
		}
	}
	read := 0
	for pkg, dir := range linked {
		if pkg == module+"/internal/core" {
			continue
		}
		names, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		var sources []coreSource
		for _, name := range names {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, coreSource{strings.TrimPrefix(name, dir+string(filepath.Separator)), string(data)})
		}
		if len(sources) == 0 {
			t.Fatalf("%s has no source files in %s, so this test proves nothing of it", pkg, dir)
		}
		read += len(sources)
		for _, problem := range outsideTheCore(t, strings.TrimPrefix(pkg, module+"/"), sources) {
			t.Error(problem)
		}
	}
	if read < 20 {
		t.Fatalf("this test read %d source files of the packages the server links: it no longer reads them as they are", read)
	}
	// The core defines what the command line alone calls: the names this
	// test looks for elsewhere are the ones in use.
	defined := map[string]bool{}
	names, err := filepath.Glob(filepath.Join(linked[module+"/internal/core"], "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil {
				defined[fn.Name.Name] = true
			}
		}
	}
	for _, name := range commandLineOnly {
		if !defined[name] {
			t.Errorf("the core has no method %s any more: say in commandLineOnly what the command line alone calls now", name)
		}
	}

	// And it catches what it is for.
	const honest = `package plugincontract
import "context"
type SearchResult struct{ Passages []ScoredPassage }
type Contract struct{ Search []string }
func (c Contract) Validate() error { _ = c.Search; return nil }
func (r PassageResult) Validate() error { return r.Passage.Validate() }
func cancelled(ctx context.Context) bool { _, cancel := context.WithCancel(ctx); cancel(); return ctx.Err() != nil }
`
	if problems := outsideTheCore(t, "internal/plugincontract", []coreSource{{"honest.go", honest}}); len(problems) != 0 {
		t.Fatalf("a package as it should be is refused: %v", problems)
	}
	for what, source := range map[string]string{
		"a helper has what it is handed test what it depends on, through an interface": `package plugincontract
func Tested(ctx context.Context, d interface{ Diagnose(context.Context) ([]Finding, error) }) { _, _ = d.Diagnose(ctx) }
`,
		"a helper takes the method as a value": `package plugincontract
func Tested(ctx context.Context, d tester) { test := d.Diagnose; test(ctx) }
`,
		"a helper has what it is handed index": `package library
func Kept(c indexer, s Source) { c.Index(nil, "c", s) }
`,
		"a helper tells what it is handed to stop indexing": `package checkpoint
func Stop(c stopper) { c.Cancel(nil, "c", "s") }
`,
		"a helper searches what it is handed": `package xdg
func Find(c finder) { c.Search(nil, "c", "q", 5, nil) }
`,
		"a helper reads a Passage of what it is handed": `package xdg
func Read(c reader) { c.Passage(nil, "c", "p") }
`,
		"a package between the server and the core checks every plugin": `package health
func Glance(ctx context.Context, c *core.Core) { _, _ = c.CheckPlugins(ctx) }
`,
		"such a package checks one plugin": `package health
func Glance(ctx context.Context, c *core.Core) { _, _ = c.CheckPlugin(ctx, "kb") }
`,
		"such a package has a Topic's Sources indexed": `package health
func Glance(ctx context.Context, c *core.Core) { _, _ = c.IndexSources(ctx, core.IndexSpec{Topic: "c"}) }
`,
		"such a package takes the operation as a value": `package health
var check = (*core.Core).CheckPlugins
`,
		"such a package takes it from the core's names without the core's": `package health
import . "github.com/mordor-forge/lamplight/v2/internal/core"
func Glance(ctx context.Context, c *Core) { check := CheckPlugins; _ = check }
`,
		"a method is called by a name made at run time": `package health
func Glance(conn any) { reflect.ValueOf(conn).MethodByName("Diag" + "nose").Call(nil) }
`,
	} {
		if problems := outsideTheCore(t, "sly", []coreSource{{"sly.go", source}}); len(problems) == 0 {
			t.Errorf("%s, and this test does not notice:\n%s", what, source)
		}
	}
}

// TestNoToolAsksAPluginForMoreWhenSomethingIsWrong calls the tools that
// reach a Knowledge base plugin where the one session of
// TestNoToolHasAPluginIndex never goes: with a plugin whose status tool
// fails, or answers what the contract does not have; whose search tool does;
// whose passage tool does; with two Topics that name the one plugin; and
// with a Topic that has a problem. A change that had a plugin asked for more
// only then, to say why a search failed or to look closer at a Study home
// that is not in order, is what this test is for: whatever the tools met,
// the plugin was asked which contract it speaks, what it has indexed, to
// search and for a Passage, and nothing else.
func TestNoToolAsksAPluginForMoreWhenSomethingIsWrong(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		// args are what the plugin is started with once a Source is
		// indexed: what makes one of its tools fail.
		args []string
		// second names a second Topic after the plugin, and broken leaves a
		// Topic in the Study home that cannot be read.
		second, broken bool
		// asked is a tool the plugin must have been asked in this case, so
		// that the case met what it is about.
		asked string
	}{
		"status refuses":                 {args: []string{"--refuse", "status=internal:the index is locked"}, asked: "status"},
		"status breaks the contract":     {args: []string{"--result", `status={"sources": "none"}`}, asked: "status"},
		"search refuses":                 {args: []string{"--refuse", "search=internal:the index is locked"}, asked: "search"},
		"search breaks the contract":     {args: []string{"--result", `search={"passages": 7}`}, asked: "search"},
		"passage refuses":                {args: []string{"--refuse", "passage=internal:the index is locked"}, asked: "passage"},
		"passage breaks the contract":    {args: []string{"--result", `passage={"passage": "none"}`}, asked: "passage"},
		"two Topics name the one plugin": {second: true, asked: "search"},
		"a Topic has a problem":          {broken: true, asked: "search"},
		"all of it at once":              {args: []string{"--refuse", "search=internal:the index is locked"}, second: true, broken: true, asked: "search"},
	} {
		t.Run(name, func(t *testing.T) {
			withGitIdentity(t)
			c := plugintest.NewComputer(t)
			record := filepath.Join(t.TempDir(), "record.json")
			c.Register(t, "kb", plugintest.Command("--record", record)...)
			plugins := core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond,
				GlanceTimeout: 20 * time.Second, EvidenceTimeout: 20 * time.Second, IndexPoll: 10 * time.Millisecond}
			k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home, Plugins: plugins})
			if err != nil {
				t.Fatal(err)
			}
			// The Topics, each with a Source the plugin has indexed the one
			// way there is, and one it has not: none of it by a tool.
			topics := []string{"c"}
			if tc.second {
				topics = append(topics, "go")
			}
			indexed := map[string]string{}
			for _, topic := range topics {
				if _, err := k.CreateTopic(ctx, core.TopicSpec{Title: strings.ToUpper(topic), ID: topic}); err != nil {
					t.Fatal(err)
				}
				if _, err := k.UpdateTopic(ctx, topic, core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin,
					Plugin: "kb"}}); err != nil {
					t.Fatal(err)
				}
				for _, book := range []struct{ name, text string }{
					{"indexed.txt", "A pointer is a variable that contains the address of a variable.\n"},
					{"notes.txt", "Notes the plugin has not indexed.\n"},
				} {
					path := filepath.Join(c.Home, "Books", topic, book.name)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(book.text), 0o644); err != nil {
						t.Fatal(err)
					}
					added, err := k.AddSource(ctx, core.SourceSpec{Topic: topic, File: path})
					if err != nil {
						t.Fatal(err)
					}
					if book.name == "indexed.txt" {
						indexed[topic] = added.Source.ID
					}
				}
				if res, err := k.IndexSources(ctx, core.IndexSpec{Topic: topic, Sources: []string{indexed[topic]}}); err != nil ||
					len(res.Sources) != 1 || res.Sources[0].State != core.IndexIndexed {
					t.Fatalf("study source index %s = %+v, %v", topic, res, err)
				}
			}
			// A Passage of the indexed Source, to have a quote checked
			// against: found through the core, while the plugin still answers.
			found, err := k.SearchEvidence(ctx, core.PassageQuery{Topic: "c", Query: "pointer variable address", Limit: 5})
			if err != nil || len(found.Passages) == 0 {
				t.Fatalf("a search through the core = %+v, %v", found, err)
			}
			passage := found.Passages[0].ID
			if tc.broken {
				if _, err := k.CreateTopic(ctx, core.TopicSpec{Title: "Broken", ID: "broken"}); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(c.StudyHome, "broken", "topic.toml")); err != nil {
					t.Fatal(err)
				}
				if status, err := k.Status(ctx); err != nil || len(status.Problems) != 1 {
					t.Fatalf("status = %+v, %v; want the one Topic that cannot be read among its problems, or this case proves nothing", status.Problems, err)
				}
			}
			// From here on the plugin is the one of this case.
			c.Register(t, "kb", plugintest.Command(append([]string{"--record", record}, tc.args...)...)...)
			seen := len(plugintest.ReadCalls(t, record))

			session := connectOn(t, c.Getenv, c.Home, nil, nil, nil, plugins)
			quote := "A pointer is a variable that contains the address"
			calls := []struct {
				tool string
				args map[string]any
			}{
				{"status", map[string]any{}},
				{"sources", map[string]any{"topic": "c"}},
				{"evidence_search", map[string]any{"topic": "c", "query": "pointer variable address"}},
				{"evidence_search", map[string]any{"topic": "c", "query": "pointer", "sources": []any{indexed["c"]}}},
				{"evidence_record", map[string]any{"topic": "c", "lesson": "answer", "source": indexed["c"], "quote": quote, "passages": []any{passage}}},
				{"evidence_record", map[string]any{"topic": "c", "lesson": "answer", "source": indexed["c"], "quote": "A pointer holds an address",
					"passages": []any{passage}}},
				{"evidence_record", map[string]any{"topic": "c", "lesson": "answer", "source": indexed["c"], "quote": quote,
					"passages": []any{"0123456789abcdef01234567"}}},
				{"session_open", map[string]any{"topic": "c", "energy": "full"}},
				{"status", map[string]any{}},
			}
			if tc.second {
				calls = append(calls, []struct {
					tool string
					args map[string]any
				}{
					{"sources", map[string]any{"topic": "go"}},
					{"evidence_search", map[string]any{"topic": "go", "query": "pointer variable address"}},
				}...)
			}
			for _, call := range calls {
				// Whatever the tool answers: it may well refuse, with a
				// plugin that fails. It must answer.
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
				if err != nil {
					t.Fatalf("%s %v got no answer: %v", call.tool, call.args, err)
				}
				asked := plugintest.ReadCalls(t, record)
				for _, got := range asked[seen:] {
					if !slices.Contains(askedOfAPlugin, got.Tool) {
						args, _ := json.Marshal(call.args)
						t.Fatalf("calling %s %s, which answered %q, had Knowledge base plugin kb asked its %s tool: whatever a "+
							"tool meets, a plugin that fails or a Study home that is not in order, it has a plugin asked %v and "+
							"nothing else. No tool has a plugin index, stop indexing, or test what it depends on (ADR-0012)",
							call.tool, args, text(res), got.Tool, askedOfAPlugin)
					}
				}
				seen = len(asked)
			}
			// The case met what it is about: the plugin was asked the tool
			// that fails here, or was searched for a Topic.
			if all := plugintest.Tools(plugintest.ReadCalls(t, record)); !slices.Contains(all, tc.asked) {
				t.Fatalf("no tool had the plugin asked its %s tool (it was asked %v), so this case proves nothing", tc.asked, all)
			}
			if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
				plugintest.Kill(started.PID)
				t.Errorf("a tool left the plugin, process %d, running", started.PID)
			}
		})
	}
}
