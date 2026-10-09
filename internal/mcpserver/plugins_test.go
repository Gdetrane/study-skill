package mcpserver_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// The Knowledge base plugin registry says which program study starts for a
// plugin's name, or which address it contacts, and it is the learner's to
// change, with study knowledge-base (ADR-0012). The MCP server runs outside
// the agent's sandbox, so a tool that registered, changed or removed a
// plugin, or took a command line or a URL for one, would let the agent
// choose a program for study to run there. These tests fail if a tool could.

// The core's operations that change the registry, and the type that carries
// a plugin's command line or URL to them. Named here, they keep the test
// below in step with the core: renamed, this file no longer compiles.
var (
	_ = (*core.Core).AddPlugin
	_ = (*core.Core).RemovePlugin
	_ = core.PluginSpec{}
)

// pluginReads are the names with "Plugin" in them that the server may use
// from the core: what reads the registry, and what a reading returns. Every
// other such name is refused, as AddPlugin, RemovePlugin and PluginSpec are.
var pluginReads = []string{"Plugins", "Plugin", "PluginList"}

// changesPlugins reports whether a name the server's source uses could be an
// operation on the registry other than reading it.
func changesPlugins(name string) bool {
	return strings.Contains(name, "Plugin") && !slices.Contains(pluginReads, name)
}

// TestTheServerCannotChangeThePluginRegistry reads the server's own source.
// The server reaches files only through the core, so it cannot write the
// registry itself, and of the core's operations on plugins it uses none but
// the ones that only read.
func TestTheServerCannotChangeThePluginRegistry(t *testing.T) {
	for _, name := range []string{"AddPlugin", "RemovePlugin", "PluginSpec"} {
		if !changesPlugins(name) {
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
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && changesPlugins(sel.Sel.Name) {
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
	name = strings.ToLower(name)
	if strings.Contains(strings.ReplaceAll(name, "_", ""), "knowledgebase") {
		return true
	}
	return slices.ContainsFunc(strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' }),
		func(word string) bool { return slices.Contains(programOrAddressWords, word) })
}

// programOrAddressInputs are the inputs with such a name that were looked at
// and are not a plugin's command line or URL: what each is, by tool and
// path.
var programOrAddressInputs = map[string]string{
	"source_add.url":              "the address of a web page that is a Source",
	"topic_update.knowledge_base": "the Topic's Knowledge base: its kind alone",
}

// TestNoToolTakesAPluginsCommandOrURL looks at what every tool accepts. An
// input whose name could be a program, an address or a plugin must be one
// this test knows, and every object a tool accepts must list its fields, so
// that none arrives under a name this test never saw. And no tool is named
// after the registry.
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
	// The check catches what it is for, and leaves ordinary names alone.
	for _, name := range []string{"command", "plugin_command", "args", "program", "url", "plugin_url", "plugin", "endpoint",
		"knowledge_base", "knowledgeBase", "kb_plugin", "shell_script"} {
		if !namesAProgramOrAnAddress(name) {
			t.Errorf("an input named %s would pass unnoticed", name)
		}
	}
	for _, name := range []string{"topic", "report", "support", "next_step", "learner_said", "hours_per_week", "dry_run"} {
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
