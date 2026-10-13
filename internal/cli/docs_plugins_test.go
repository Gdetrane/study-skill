package cli_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// inWords writes a small number as docs/cli.md does in a sentence.
func inWords(t *testing.T, n int) string {
	t.Helper()
	words := map[int]string{2: "two", 6: "six", 7: "seven", 8: "eight", 10: "ten", 12: "twelve", 14: "fourteen"}
	if words[n] == "" {
		t.Fatalf("this test has no word for %d: add it", n)
	}
	return words[n]
}

// docs/cli.md says how long study doctor waits for plugins, in so many
// seconds, and how many tools the contract has. Those are sums of what the
// core and the contract give a plugin: a time that changes, or a tool that
// is added, changes the page too. And it says of a plugin that did not say
// what it found what the commands say of it.
func TestDocsGiveTheDoctorsWaitForPlugins(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(data)), " ")
	glance, diagnose := int(core.PluginGlanceTimeout/time.Second), int(plugincontract.DiagnoseTime/time.Second)
	upper := func(word string) string { return strings.ToUpper(word[:1]) + word[1:] }
	for what, want := range map[string]string{
		"how many tools check looks for":       "that the " + inWords(t, len(plugincontract.Tools)) + " tools of the contract are there",
		"the time a plugin has to say":         "**The plugin has " + strconv.Itoa(diagnose) + " seconds for the whole answer**",
		"the doctor's whole wait":              "**The doctor waits for plugins " + inWords(t, 2*glance+diagnose) + " seconds at most**",
		"the time to start":                    "- **" + upper(inWords(t, glance)) + " seconds** for reading the registry and for every plugin to start",
		"the time to test":                     "- **" + upper(inWords(t, diagnose)) + " seconds** for testing what the plugins depend on",
		"the time to say what is indexed":      "- **" + upper(inWords(t, glance)) + " seconds** for the plugins of your Topics to say which Sources they have indexed",
		"what one plugin that hangs costs":     "a single plugin that hangs costs " + inWords(t, glance+diagnose) + " seconds and not " + inWords(t, 2*glance+diagnose),
		"why no tool asks diagnose":            "no MCP tool waits " + strconv.Itoa(diagnose) + " seconds for a plugin",
		"what a stopped doctor reports":        "reports `canceled` with exit code 1, and prints no Finding and no verdict",
		"that an untested plugin has no fix":   "is a warning too, **with no `fix`**",
		"that check names no command for it":   "**It names no command**",
		"the code of an untested plugin":       "is `failed_precondition` and exit code 1, whatever code the plugin itself refused with",
		"what is shown of a finding's message": "cut after " + strconv.Itoa(plugincontract.MaxFindingBytes) + " bytes, with `…` where it was cut",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/cli.md does not give %s as %q", what, want)
		}
	}
	if core.PluginGlanceGrace != 250*time.Millisecond {
		t.Errorf("a plugin that was glanced at has %s to exit, and docs/cli.md says a quarter of a second: say there what it is now",
			core.PluginGlanceGrace)
	}
	for _, not := range []string{"The doctor waits for no plugin", "send requests to wherever its settings point",
		"like any plugin that does not speak the contract"} {
		if strings.Contains(doc, not) {
			t.Errorf("docs/cli.md says %q, which the code does not bear out", not)
		}
	}
}

// The shape docs/cli.md gives for study knowledge-base check, in the table
// of commands and in its example, is the one the command prints: every
// field, the ones that are not always there marked so, findings among them.
func TestDocsGiveTheChecksShape(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	var row string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| `study knowledge-base check <name>` |") {
			row = line
		}
	}
	_, example, found := strings.Cut(doc, "\n### study knowledge-base check\n")
	_, example, fenced := strings.Cut(example, "```json\n")
	example, _, closed := strings.Cut(example, "\n```")
	if row == "" || !found || !fenced || !closed {
		t.Fatal("docs/cli.md has no row, or no JSON example, for study knowledge-base check: this test no longer reads the page as it is")
	}
	var shown map[string]json.RawMessage
	if err := json.Unmarshal([]byte(example), &shown); err != nil {
		t.Fatalf("the example of study knowledge-base check is no JSON object: %v\n%s", err, example)
	}
	// keys are the JSON names of a type's fields, each with whether it is
	// always there.
	keys := func(typ reflect.Type) map[string]bool {
		out := map[string]bool{}
		for i := range typ.NumField() {
			name, rest, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			out[name] = !strings.Contains(rest, "omitempty")
		}
		return out
	}
	fields := keys(reflect.TypeFor[core.PluginCheck]())
	if !fields["findings"] || len(fields) < 6 {
		t.Fatalf("a check's fields are read as %v", fields)
	}
	for name, always := range fields {
		quoted := `"` + name + `"`
		switch {
		case !strings.Contains(row, quoted):
			t.Errorf("the row of study knowledge-base check does not give the field %s", quoted)
		case always == strings.Contains(row, quoted+"?"):
			t.Errorf("the row of study knowledge-base check marks %s as not always there, or does not, and it is always there: %v",
				quoted, always)
		}
		if _, ok := shown[name]; always && !ok {
			t.Errorf("the example of study knowledge-base check lacks %s, which is always there", quoted)
		}
	}
	for name := range shown {
		if _, ok := fields[name]; !ok {
			t.Errorf("the example of study knowledge-base check holds %q, which the command does not print", name)
		}
	}
	// A finding, in the row and in the example.
	var finding []string
	typ := reflect.TypeFor[core.PluginFinding]()
	for i := range typ.NumField() {
		finding = append(finding, `"`+typ.Field(i).Tag.Get("json")+`"`)
	}
	if want := `"findings": [{` + strings.Join(finding, ", ") + `}]`; !strings.Contains(row, want) {
		t.Errorf("the row of study knowledge-base check does not give a finding's shape as %s", want)
	}
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(shown["findings"], &findings); err != nil || len(findings) == 0 {
		t.Fatalf("the example of study knowledge-base check shows no finding: %v", err)
	}
	for _, f := range findings {
		if len(f) != len(finding) {
			t.Errorf("a finding of the example holds %d fields, and a finding has %v", len(f), finding)
		}
		for _, name := range finding {
			if _, ok := f[strings.Trim(name, `"`)]; !ok {
				t.Errorf("a finding of the example lacks %s", name)
			}
		}
	}
}
