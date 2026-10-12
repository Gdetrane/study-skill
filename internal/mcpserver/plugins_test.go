package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// The Knowledge base plugin registry says which program study starts for a
// plugin's name, or which address it contacts, and it is the learner's to
// change, with study knowledge-base (ADR-0012). The MCP server runs outside
// the agent's sandbox, so a tool that registered, changed or removed a
// plugin, or took a command line or a URL for one, would let the agent
// choose a program for study to run there.
//
// What keeps it from happening is how the program is built, and the first
// test below checks that: what changes the registry is in a package the
// server does not link, so no tool can reach it, however it is named and
// through however many wrappers. The other two are a second line. They look
// at names, and a name can be chosen to pass them.

const (
	module = "github.com/mordor-forge/lamplight/v2"
	// registrar is the one package that changes the registry, and
	// registryReader the one that reads it.
	registrar      = module + "/internal/pluginregistry/registrar"
	registryReader = module + "/internal/pluginregistry"
)

// goList runs go list from the module's root and returns its lines.
func goList(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("go list %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// TestOnlyTheCommandLineLinksWhatChangesThePluginRegistry lists what the
// server is built from. The package that registers and removes plugins is
// not in it, nor in the core, which the server links whole: a wrapper around
// it in either would not compile without importing it, and importing it
// fails this test. Only the command line imports it.
func TestOnlyTheCommandLineLinksWhatChangesThePluginRegistry(t *testing.T) {
	for _, pkg := range []string{"./internal/mcpserver", "./internal/core"} {
		deps := goList(t, "-deps", pkg)
		if slices.Contains(deps, registrar) {
			t.Errorf("%s links %s: nothing the MCP server is built from may change the Knowledge base plugin registry "+
				"(ADR-0012). Registering and removing a plugin stay in the command line", pkg, registrar)
		}
		// The list is what it seems: it holds what reads the registry.
		if !slices.Contains(deps, registryReader) || !slices.Contains(deps, module+"/internal/core") {
			t.Fatalf("go list -deps %s does not list %s, so this test proves nothing:\n%s", pkg, registryReader, strings.Join(deps, "\n"))
		}
	}
	// The package is there, under that name, and the command line links it.
	if deps := goList(t, "-deps", "./internal/cli"); !slices.Contains(deps, registrar) {
		t.Fatalf("the command line does not link %s: this test names the wrong package", registrar)
	}

	// And nothing but the command line imports it.
	var importers []string
	for _, line := range goList(t, "-f", `{{.ImportPath}} {{join .Imports ","}}`, "./...") {
		pkg, imports, _ := strings.Cut(line, " ")
		if slices.Contains(strings.Split(imports, ","), registrar) {
			importers = append(importers, strings.TrimPrefix(pkg, module+"/"))
		}
	}
	if !slices.Equal(importers, []string{"internal/cli"}) {
		t.Errorf("%s is imported by %v: only internal/cli may", registrar, importers)
	}
}

// words splits a name into its words, in lower case: at underscores, hyphens
// and dots, and where camelCase changes case, so that pluginCommand,
// plugin_command and PluginURL all hold the word plugin.
func words(name string) []string {
	var out []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = nil
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
			continue
		case unicode.IsUpper(r) && i > 0:
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			// A capital after a small letter starts a word, and so does the
			// last capital of a run when a small letter follows: URLPath.
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || unicode.IsUpper(prev) && nextLower {
				flush()
			}
		}
		word = append(word, r)
	}
	flush()
	return out
}

func TestWords(t *testing.T) {
	for name, want := range map[string]string{
		"plugin_command":    "plugin command",
		"pluginCommand":     "plugin command",
		"PluginURL":         "plugin url",
		"pluginURLPath":     "plugin url path",
		"SetSourceReader":   "set source reader",
		"kb-plugin.name":    "kb plugin name",
		"knowledgeBase":     "knowledge base",
		"knowledge_base":    "knowledge base",
		"AddPlugin":         "add plugin",
		"HTTPEndpoint":      "http endpoint",
		"next_step":         "next step",
		"hours_per_week":    "hours per week",
		"PluginRegistryEnv": "plugin registry env",
		"args2":             "args2",
	} {
		if got := strings.Join(words(name), " "); got != want {
			t.Errorf("words(%q) = %q, want %q", name, got, want)
		}
	}
}

// pluginReads are the names about plugins that the server may use from the
// core: what reads the registry, and what a reading returns.
var pluginReads = []string{"Plugins", "Plugin", "PluginList"}

// aboutTheRegistry reports whether a name the server's source uses speaks of
// plugins or of the registry, and is not one of the names that only read.
func aboutTheRegistry(name string) bool {
	if slices.Contains(pluginReads, name) {
		return false
	}
	return slices.ContainsFunc(words(name), func(word string) bool {
		return slices.Contains([]string{"plugin", "plugins", "registry", "registrar", "registrars"}, word)
	})
}

// TestTheServerNamesNothingThatChangesThePluginRegistry reads the server's
// own source, as a second line behind the test above. The server reaches
// files only through the core, so it cannot write the registry itself; it
// imports nothing of the registry's packages; and it uses no name about
// plugins or the registry but the ones that only read.
func TestTheServerNamesNothingThatChangesThePluginRegistry(t *testing.T) {
	for _, name := range []string{"AddPlugin", "RemovePlugin", "PluginSpec", "PluginRegistryEnv", "registrar", "SetPluginReader", "pluginRegistry",
		// Nor what starts a plugin or reaches one (see
		// TestTheCoreStartsAPluginInOnePlace).
		"CheckPlugin", "PluginCheck", "PluginOptions", "startPlugin"} {
		if !aboutTheRegistry(name) {
			t.Fatalf("%s is not refused, so this test proves nothing", name)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "os" || p == "io/ioutil" || p == "os/exec" || p == "syscall" || strings.HasPrefix(p, "golang.org/x/sys") {
				t.Errorf("%s imports %s: the server reaches files only through the core, so that no tool can write "+
					"the Knowledge base plugin registry itself (ADR-0012)", name, p)
			}
			if strings.HasPrefix(p, registryReader) {
				t.Errorf("%s imports %s: the server asks the core about plugins, and has nothing to do with the "+
					"registry's own packages (ADR-0012)", name, p)
			}
			if strings.HasPrefix(p, module+"/internal/plugin") {
				t.Errorf("%s imports %s: the server reaches a Knowledge base plugin only through the core (ADR-0012)", name, p)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && aboutTheRegistry(sel.Sel.Name) {
				t.Errorf("%s uses %s: no tool registers, changes or removes a Knowledge base plugin (ADR-0012). "+
					"If %s only reads the registry, add it to pluginReads", name, sel.Sel.Name, sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files were checked")
	}
}

// pluginClient is the package that starts a Knowledge base plugin, or
// reaches one, and pluginTest the test plugin and the conformance suite,
// which starts a plugin as study does and is linked by tests alone.
const (
	pluginClient = module + "/internal/pluginclient"
	pluginTest   = module + "/internal/plugintest"
)

// TestNoToolStartsOrReachesAPlugin: so far study starts a plugin, or reaches
// one, for study knowledge-base check alone, which is for the learner and
// CLI-only. The server runs outside the agent's sandbox, and until a Topic
// can name a plugin no tool has a reason to start one.
//
// This is what the server does, not how it is written. Two plugins are
// registered on the computer, a command that leaves a record when it is
// started and a service that counts what reaches it; every tool the server
// offers is called, each with valid input so that it does its work; and
// neither plugin was started or reached by any of them. However a tool came
// to start a plugin, through whatever function and under whatever name, it
// would show here. When a tool comes to search a Topic's plugin, this test
// is where that is decided: on purpose.
func TestNoToolStartsOrReachesAPlugin(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	var reached atomic.Int64
	service := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	handler := service.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	c.RegisterURL(t, "remote", server.URL+"/mcp")

	// The registry is one the core reads, with both plugins in it and
	// nothing wrong with either: a registry it refused would start nothing
	// whatever the tools did.
	k, err := core.Open(core.Options{Getenv: c.Getenv, Dir: c.Home,
		Plugins: core.PluginOptions{Environ: func() []string { return c.Environ() }, StopGrace: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := k.Plugins(context.Background())
	if err != nil || len(list.Plugins) != 2 || list.Plugins[0].Problem != "" || list.Plugins[1].Problem != "" {
		t.Fatalf("the registry of this test = %+v, %v; want the two plugins, with nothing wrong", list, err)
	}

	untouched := func(tool string) {
		t.Helper()
		if _, err := os.Stat(record); err == nil {
			started := plugintest.ReadRecord(t, record)
			plugintest.Kill(started.PID)
			t.Fatalf("calling %s started a Knowledge base plugin: until a Topic can name one, no tool does (ADR-0012)", tool)
		}
		if n := reached.Load(); n != 0 {
			t.Fatalf("calling %s reached a Knowledge base plugin at its URL, %d times: until a Topic can name one, no tool "+
				"does (ADR-0012)", tool, n)
		}
		if _, err := os.Stat(c.DataFolder("kb")); err == nil {
			t.Fatalf("calling %s made a Knowledge base plugin its folder", tool)
		}
	}
	everyToolOn(t, c.Home, c.StudyHome, nil, untouched)

	// The record is real: checking each plugin the one supported way starts
	// the one and reaches the other.
	for _, name := range []string{"kb", "remote"} {
		if _, err := k.CheckPlugin(context.Background(), name); err != nil {
			t.Fatalf("study knowledge-base check %s: %v", name, err)
		}
	}
	if _, err := os.Stat(record); err != nil || reached.Load() == 0 {
		t.Fatalf("study knowledge-base check left no record of the command (%v) or did not reach the service (%d times), "+
			"so this test proves nothing", err, reached.Load())
	}
}

// TestTheCoreStartsAPluginInOnePlace reads how the program is written, as a
// second line behind the test above: that one sees what the tools do with
// the input it gives them, and this one what the core could do at all.
//
// Only the core imports the package that starts a plugin, besides the test
// plugin, which nothing study is built from links. In the core, every use
// of that package is one this test knows; the one function that starts a
// plugin is used in one place, startPlugin; startPlugin is used in one,
// CheckPlugin; and CheckPlugin is used nowhere in the core, and cannot be
// named by the server (TestTheServerNamesNothingThatChangesThePluginRegistry).
// It reads whole files, the declarations outside any function among them,
// and takes the package by whatever name a file imports it under.
func TestTheCoreStartsAPluginInOnePlace(t *testing.T) {
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
	found, problems := pluginStarts(t, sources)
	for _, problem := range problems {
		t.Error(problem)
	}
	// The core is read as this test expects: each is used where it may be,
	// once.
	for _, name := range []string{"Start", "startPlugin"} {
		if found[name] != 1 {
			t.Errorf("%s is used %d times in the core, want once: this test no longer reads the core as it is", name, found[name])
		}
	}
	if !aboutTheRegistry("CheckPlugin") {
		t.Fatal("the server could name CheckPlugin, so this test proves nothing")
	}

	// And it catches what it is for: each way a plugin was once started
	// from elsewhere without this test noticing.
	const honest = `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) CheckPlugin() { c.startPlugin() }
func (c *Core) startPlugin() { pluginclient.Start(nil, pluginregistry.Env{}, pluginregistry.Plugin{}, pluginclient.Options{}) }
`
	if _, problems := pluginStarts(t, []coreSource{{"honest.go", honest}}); len(problems) != 0 {
		t.Fatalf("the core as it should be is refused: %v", problems)
	}
	for what, source := range map[string]string{
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
		"another function calls startPlugin": `package core
func (c *Core) Sources() { c.startPlugin(nil, pluginregistry.Plugin{}) }
`,
		"startPlugin is taken as a value": `package core
var start = (*Core).startPlugin
`,
		"a name of the package this test does not know": `package core
import "github.com/mordor-forge/lamplight/v2/internal/pluginclient"
func (c *Core) Status() { _ = pluginclient.Reach }
`,
	} {
		if _, problems := pluginStarts(t, []coreSource{{"honest.go", honest}, {"sly.go", source}}); len(problems) == 0 {
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
	"Start":   "startPlugin",
	"Options": "startPlugin",
	// What a reading returns, and how an address is shown.
	"Connection": "",
	"Server":     "",
	"Address":    "",
}

// pluginStarts reads source files of the core and returns how often each
// name that starts a plugin is used, and what is wrong: a use of the
// package that starts one anywhere but where it may be.
func pluginStarts(t *testing.T, sources []coreSource) (found map[string]int, problems []string) {
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
			}
			where := func() string {
				if in == "" {
					return source.name + ", outside any function,"
				}
				return source.name + ": " + in
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SelectorExpr:
					pkg, _ := n.X.(*ast.Ident)
					switch {
					case local != "" && pkg != nil && pkg.Name == local:
						may, known := pluginClientNames[n.Sel.Name]
						switch {
						case !known:
							problem("%s uses %s.%s, which this test does not know: say in pluginClientNames what it is, "+
								"and where it may be used", where(), local, n.Sel.Name)
						case may != "" && in != may:
							problem("%s uses %s.%s, which starts a Knowledge base plugin: only %s may, so that no MCP "+
								"tool starts or reaches one (ADR-0012)", where(), local, n.Sel.Name, may)
						}
						if n.Sel.Name == "Start" {
							found["Start"]++
						}
					case n.Sel.Name == "startPlugin":
						found["startPlugin"]++
						if in != "CheckPlugin" {
							problem("%s uses startPlugin, which starts a Knowledge base plugin: only CheckPlugin may, so "+
								"that no MCP tool starts or reaches one (ADR-0012)", where())
						}
					case n.Sel.Name == "CheckPlugin":
						problem("%s uses CheckPlugin, which starts a Knowledge base plugin: nothing in the core may, so "+
							"that no operation an MCP tool calls starts or reaches one (ADR-0012)", where())
					}
				}
				return true
			})
		}
	}
	return found, problems
}

// programOrAddressWords are the words, in the name of an input, that could
// mean what a plugin's registration holds: a program, its arguments or an
// address; or that say the input is about a plugin or the registry.
var programOrAddressWords = []string{
	"command", "commands", "cmd", "program", "exec", "executable", "binary", "bin", "argv", "args", "arguments",
	"shell", "script", "url", "urls", "uri", "endpoint", "address", "host", "port", "server",
	"plugin", "plugins", "registry", "kb",
}

// namesAProgramOrAnAddress reports whether an input's name holds one of
// those words, or speaks of the Knowledge base.
func namesAProgramOrAnAddress(name string) bool {
	parts := words(name)
	if strings.Contains(strings.Join(parts, ""), "knowledgebase") {
		return true
	}
	return slices.ContainsFunc(parts, func(word string) bool { return slices.Contains(programOrAddressWords, word) })
}

// programOrAddressInputs are the inputs with such a name that were looked at
// and are not a plugin's command line or URL: what each is, by tool and
// path.
var programOrAddressInputs = map[string]string{
	"source_add.url":              "the address of a web page that is a Source",
	"topic_update.knowledge_base": "the Topic's Knowledge base: its kind alone",
}

// TestNoToolTakesAPluginsCommandOrURL looks at what every tool accepts, as a
// second line too. An input whose name could be a program, an address or a
// plugin must be one this test knows, and every object a tool accepts must
// list its fields, so that none arrives under a name this test never saw.
// And no tool is named after the registry.
func TestNoToolTakesAPluginsCommandOrURL(t *testing.T) {
	tools, err := connect(t, t.TempDir()).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	inputs := 0
	for _, tool := range tools.Tools {
		if namesAProgramOrAnAddress(tool.Name) {
			t.Errorf("the server offers %s: the Knowledge base plugin registry has no tool (ADR-0012). "+
				"If the tool is about something else, teach this test the difference", tool.Name)
		}
		// The schema as a client receives it, whatever type the SDK holds.
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema any
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		walkInputs(t, tool.Name, schema, func(path string) {
			inputs++
			if !namesAProgramOrAnAddress(path[strings.LastIndexByte(path, '.')+1:]) {
				return
			}
			seen[path] = true
			if _, ok := programOrAddressInputs[path]; !ok {
				t.Errorf("%s is an input this test does not know. No tool takes a command line or a URL for a "+
					"Knowledge base plugin, and none registers, changes or removes one (ADR-0012): that is the "+
					"learner's to do, with study knowledge-base. If the input is something else, say what in "+
					"programOrAddressInputs", path)
			}
		})
	}
	// The schemas were read: these are inputs the server has had from the start.
	if inputs < 50 || !seen["source_add.url"] || !seen["topic_update.knowledge_base"] {
		t.Fatalf("%d inputs were looked at, and of the known ones only %v: the schemas are not read as this test expects", inputs, seen)
	}
	for path := range programOrAddressInputs {
		if !seen[path] {
			t.Errorf("programOrAddressInputs lists %s, which no tool has any more", path)
		}
	}
	// The check catches what it is for, however the name is cased, and
	// leaves ordinary names alone.
	for _, name := range []string{"command", "plugin_command", "pluginCommand", "PluginURL", "args", "program", "url", "plugin_url",
		"plugin", "endpoint", "knowledge_base", "knowledgeBase", "kb_plugin", "shellScript", "serverAddress"} {
		if !namesAProgramOrAnAddress(name) {
			t.Errorf("an input named %s would pass unnoticed", name)
		}
	}
	for _, name := range []string{"topic", "report", "support", "next_step", "learner_said", "hours_per_week", "dry_run", "reportedBy"} {
		if namesAProgramOrAnAddress(name) {
			t.Errorf("an input named %s is taken for a program or an address", name)
		}
	}
}

// walkInputs calls visit with the path of every field in a tool's input
// schema, such as topic_update.knowledge_base.kind, through objects and
// lists. It fails the test for an object that accepts fields it does not
// list, and for a schema it cannot follow.
func walkInputs(t *testing.T, path string, schema any, visit func(path string)) {
	t.Helper()
	s, ok := schema.(map[string]any)
	if !ok {
		t.Errorf("%s: its schema is %T, not an object", path, schema)
		return
	}
	for _, key := range []string{"$ref", "$defs", "anyOf", "oneOf", "allOf", "not", "if", "then", "else", "patternProperties",
		"dependentSchemas", "prefixItems", "unevaluatedProperties", "propertyNames"} {
		if _, ok := s[key]; ok {
			t.Errorf("%s: its schema uses %s, which this test does not follow: teach it to", path, key)
		}
	}
	props, _ := s["properties"].(map[string]any)
	if isType(s["type"], "object") || props != nil {
		if extra, ok := s["additionalProperties"]; !ok || extra != false {
			t.Errorf("%s accepts fields it does not list (additionalProperties is %v), so anything could arrive in it", path, extra)
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		visit(path + "." + name)
		walkInputs(t, path+"."+name, props[name], visit)
	}
	if items, ok := s["items"]; ok {
		walkInputs(t, path, items, visit)
	}
}

// isType reports whether a schema's type is name, alone or among others.
func isType(v any, name string) bool {
	switch v := v.(type) {
	case string:
		return v == name
	case []any:
		return slices.Contains(v, any(name))
	}
	return false
}
