package plugincontract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// docs/knowledge-base-contract.md is what someone writes a plugin from, in
// any language, without reading this package. The tests here keep it in
// step with the Go types and with what study's client does: every tool,
// field, kind, state, code and limit is on the page where it belongs, the
// page names no field the types do not have, and every example on it is
// what the contract says.

const docPath = "../../docs/knowledge-base-contract.md"

func readDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// sections cuts the page into its sections, by the text of their headings.
// A section ends where the next heading begins, of whatever level.
func sections(doc string) map[string]string {
	out := map[string]string{}
	heading := ""
	var body strings.Builder
	flush := func() {
		if heading != "" {
			out[heading] = body.String()
		}
		body.Reset()
	}
	inCode := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
		}
		if !inCode && strings.HasPrefix(line, "#") {
			flush()
			heading = strings.Trim(strings.TrimLeft(line, "# "), "`")
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return out
}

// described says where the page describes each type's fields: in the
// section of a tool, or in the one on results and errors.
var described = []struct {
	section string
	types   []reflect.Type
}{
	{plugincontract.ToolContract, []reflect.Type{reflect.TypeFor[plugincontract.Contract]()}},
	{plugincontract.ToolIndex, []reflect.Type{reflect.TypeFor[plugincontract.IndexInput](), reflect.TypeFor[plugincontract.Source](),
		reflect.TypeFor[plugincontract.IndexResult]()}},
	{plugincontract.ToolStatus, []reflect.Type{reflect.TypeFor[plugincontract.StatusInput](), reflect.TypeFor[plugincontract.StatusResult](),
		reflect.TypeFor[plugincontract.SourceStatus]()}},
	{plugincontract.ToolCancel, []reflect.Type{reflect.TypeFor[plugincontract.CancelInput](), reflect.TypeFor[plugincontract.CancelResult]()}},
	{plugincontract.ToolSearch, []reflect.Type{reflect.TypeFor[plugincontract.SearchInput](), reflect.TypeFor[plugincontract.SearchResult](),
		reflect.TypeFor[plugincontract.ScoredPassage](), reflect.TypeFor[plugincontract.Location]()}},
	{plugincontract.ToolPassage, []reflect.Type{reflect.TypeFor[plugincontract.PassageInput](), reflect.TypeFor[plugincontract.PassageResult]()}},
	{"Results and errors", []reflect.Type{reflect.TypeFor[plugincontract.ErrorResult](), reflect.TypeFor[plugincontract.ErrorBody]()}},
}

// fieldsOf lists the JSON names of a type's fields, those of an embedded
// type among them.
func fieldsOf(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous {
			names = append(names, fieldsOf(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
}

var (
	// A row of a table of fields: its first cell names one field or more,
	// each in backticks.
	fieldRow = regexp.MustCompile("(?m)^\\| ((?:`[a-z_]+`(?:, )?)+) \\|")
	backtick = regexp.MustCompile("`([^`]+)`")
)

// documentedFields are the fields a section's tables describe.
func documentedFields(section string) []string {
	var names []string
	for _, row := range fieldRow.FindAllStringSubmatch(section, -1) {
		for _, name := range backtick.FindAllStringSubmatch(row[1], -1) {
			names = append(names, name[1])
		}
	}
	return names
}

func TestThePageDescribesEveryToolAndField(t *testing.T) {
	doc := readDoc(t)
	parts := sections(doc)
	for _, tool := range plugincontract.Tools {
		if _, ok := parts[tool]; !ok {
			t.Errorf("the page has no section headed `%s`", tool)
		}
	}
	covered := map[reflect.Type]bool{}
	for _, d := range described {
		section, ok := parts[d.section]
		if !ok {
			t.Errorf("the page has no section headed %q", d.section)
			continue
		}
		documented := documentedFields(section)
		var known []string
		for _, typ := range d.types {
			covered[typ] = true
			for _, name := range fieldsOf(typ) {
				known = append(known, name)
				if !slices.Contains(documented, name) {
					t.Errorf("the section %q does not describe `%s`, a field of %s, in a table", d.section, name, typ.Name())
				}
			}
		}
		// And it describes nothing the types do not have. The codes of an
		// error are in a table too, under the field they are values of.
		for _, name := range documented {
			if !slices.Contains(known, name) && !slices.Contains(codes, name) {
				t.Errorf("the section %q describes `%s`, which is no field of %v", d.section, name, d.types)
			}
		}
	}
	// Every type of the contract is described somewhere.
	for _, typ := range []reflect.Type{
		reflect.TypeFor[plugincontract.Contract](), reflect.TypeFor[plugincontract.Source](), reflect.TypeFor[plugincontract.SourceStatus](),
		reflect.TypeFor[plugincontract.Location](), reflect.TypeFor[plugincontract.ScoredPassage](),
		reflect.TypeFor[plugincontract.IndexInput](), reflect.TypeFor[plugincontract.IndexResult](), reflect.TypeFor[plugincontract.StatusInput](),
		reflect.TypeFor[plugincontract.StatusResult](), reflect.TypeFor[plugincontract.CancelInput](), reflect.TypeFor[plugincontract.CancelResult](),
		reflect.TypeFor[plugincontract.SearchInput](), reflect.TypeFor[plugincontract.SearchResult](), reflect.TypeFor[plugincontract.PassageInput](),
		reflect.TypeFor[plugincontract.PassageResult](), reflect.TypeFor[plugincontract.ErrorResult](), reflect.TypeFor[plugincontract.ErrorBody](),
	} {
		if !covered[typ] {
			t.Errorf("%s is described in no section: add it to described", typ.Name())
		}
	}
}

var codes = []string{plugincontract.CodeInvalidArgument, plugincontract.CodeNotFound, plugincontract.CodeUnsupported, plugincontract.CodeInternal}

func TestThePageNamesEveryKindStateAndCode(t *testing.T) {
	doc := readDoc(t)
	parts := sections(doc)
	in := func(section, word string) {
		t.Helper()
		if !strings.Contains(parts[section], "`"+word+"`") {
			t.Errorf("the section %q does not name `%s`", section, word)
		}
	}
	for _, kind := range []string{plugincontract.SourceFile, plugincontract.SourceURL, plugincontract.SearchKeyword,
		plugincontract.SearchEmbedding, plugincontract.LocationPages} {
		in(plugincontract.ToolContract, kind)
	}
	for _, state := range []string{plugincontract.StateIndexed, plugincontract.StateIndexing, plugincontract.StateFailed} {
		in(plugincontract.ToolStatus, state)
	}
	// Each code, and the code study reports it as, in one row of the table.
	for _, code := range codes {
		row := regexp.MustCompile("(?m)^\\| `" + code + "` \\|.*\\| `" + pluginclient.StudyCode(code) + "` \\|$")
		if !row.MatchString(parts["Results and errors"]) {
			t.Errorf("no row of the table of codes says that `%s` is reported as `%s`", code, pluginclient.StudyCode(code))
		}
	}
	if !strings.HasPrefix(doc, fmt.Sprintf("# The Knowledge base plugin contract, version %d\n", plugincontract.Version)) {
		t.Errorf("the page is not headed with version %d of the contract", plugincontract.Version)
	}
	for what, word := range map[string]string{
		"the variable that names the plugin's folder": "`" + plugincontract.DataFolderEnv + "`",
		"the folder that holds the plugins' folders":  "`$XDG_STATE_HOME/lamplight/" + pluginclient.FoldersName + "/<name>`",
		"where that is without XDG_STATE_HOME":        "`~/.local/state/lamplight/" + pluginclient.FoldersName + "/<name>`",
		"the MCP version study asks for":              "`" + pluginclient.MCPVersion + "`",
		"how many tools study lists":                  number(pluginclient.MaxTools) + " tools",
		"how much of standard error study keeps":      strconv.Itoa(pluginclient.StderrKept>>10) + " KiB",
		"how much of standard error study repeats":    "the last " + number(pluginclient.StderrShown) + " bytes",
	} {
		if !strings.Contains(doc, word) {
			t.Errorf("the page does not give %s: %s", what, word)
		}
	}
	// The folder is nowhere said to be in the Study home any more.
	if strings.Contains(doc, ".lamplight/"+pluginclient.FoldersName) {
		t.Errorf("the page puts a plugin's folder under the Study home's .lamplight")
	}
}

// The page lists the environment a plugin is started with, variable by
// variable: it is a list of what goes in, and a plugin's author plans by it.
func TestThePageListsAPluginsEnvironment(t *testing.T) {
	section := sections(readDoc(t))["A plugin started by a command"]
	listed := map[string]bool{}
	for _, row := range regexp.MustCompile("(?m)^  \\| ((?:`[A-Z0-9_*]+`(?:, )?)+) \\|").FindAllStringSubmatch(section, -1) {
		for _, name := range backtick.FindAllStringSubmatch(row[1], -1) {
			listed[name[1]] = true
		}
	}
	given := append(pluginclient.EnvironmentVariables(), plugincontract.DataFolderEnv)
	for _, name := range given {
		if !listed[name] {
			t.Errorf("the table of a plugin's environment does not list `%s`, which a plugin is given", name)
		}
	}
	for name := range listed {
		if !slices.Contains(given, name) {
			t.Errorf("the table of a plugin's environment lists `%s`, which a plugin is not given", name)
		}
	}
	if len(listed) < 10 {
		t.Fatalf("the table of a plugin's environment is read as %v", listed)
	}
}

// number writes a number as the page does: with a comma between thousands.
func number(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func TestThePageGivesEveryLimitAndDeadline(t *testing.T) {
	table := sections(readDoc(t))["Deadlines and limits"]
	seconds := func(d time.Duration) string { return strconv.Itoa(int(d/time.Second)) + " seconds" }
	for what, want := range map[string]string{
		"the deadline for starting": "answer of `contract` | " + seconds(pluginclient.DefaultStartTimeout),
		"the deadline for a call":   "each call | " + seconds(pluginclient.DefaultCallTimeout),
		"the deadline for a search": "| `search` | " + seconds(pluginclient.DefaultSearchTimeout),
		"the grace for stopping":    "after `SIGTERM` | " + seconds(pluginclient.DefaultStopGrace),
		// What the core gives a plugin that is asked what it has indexed,
		// and one that is told to stop indexing.
		"the time to say what is indexed": "when `study` shows what is indexed | " + seconds(core.PluginGlanceTimeout),
		"the grace after saying it": "Exiting then, once standard input is closed, and again after `SIGTERM` | " +
			strconv.Itoa(int(core.PluginGlanceGrace/time.Millisecond)) + " milliseconds",
		"the time to stop indexing":         "when `study source index` was stopped | " + seconds(core.DefaultIndexCancelTimeout),
		"the size of a message":             "| " + number(plugincontract.MaxMessageBytes) + " bytes |",
		"the values of a message":           "| Values in one MCP message from the plugin | " + number(plugincontract.MaxMessageValues) + " |",
		"the length of a Passage":           "| " + number(plugincontract.MaxPassageBytes) + " bytes of UTF-8 |",
		"the length of a query":             "| " + number(plugincontract.MaxQueryBytes) + " bytes of UTF-8 |",
		"the highest limit of a search":     "| `limit` of a search | " + number(plugincontract.MaxSearchLimit) + " |",
		"the number of Sources":             "| " + number(plugincontract.MaxSources) + " |",
		"the length of a collection's name": "| A collection's name | " + number(plugincontract.MaxNameBytes) + " characters |",
		"the length of an id":               "| " + number(plugincontract.MaxIDBytes) + " characters |",
		"the length of a title":             "| " + number(plugincontract.MaxTitleRunes) + " characters |",
		"the length of a path":              "| " + number(plugincontract.MaxPathBytes) + " bytes |",
		"the number and length of kinds":    "| " + number(plugincontract.MaxKinds) + ", and " + number(plugincontract.MaxKindBytes) + " characters |",
		"the length of a reason":            "| " + number(plugincontract.MaxTextBytes) + " bytes |",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("the table of deadlines and limits does not give %s as %q", what, want)
		}
	}
}

// example is one of the page's examples: JSON under a comment that says
// which tool's input or result it is.
var example = regexp.MustCompile("(?s)<!-- example: ([a-z]+)(?: (input|result))? -->\n```json\n(.*?)\n```")

// strict reads an example as the contract's type, and fails for a field
// the type does not have: an example must not teach what is not there.
func strict(t *testing.T, what, data string, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("the example of %s is not the contract's: %v\n%s", what, err, data)
	}
}

func TestThePagesExamplesAreTheContracts(t *testing.T) {
	doc := readDoc(t)
	seen := map[string]int{}
	var (
		indexed  []plugincontract.IndexInput
		searched plugincontract.SearchInput
		asked    plugincontract.PassageInput
	)
	check := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("the example of %s breaks the contract: %v", what, err)
		}
	}
	for _, m := range example.FindAllStringSubmatch(doc, -1) {
		what := strings.TrimSpace(m[1] + " " + m[2])
		seen[what]++
		switch what {
		case "error":
			var e plugincontract.ErrorResult
			strict(t, what, m[3], &e)
			if !slices.Contains(codes, e.Error.Code) || e.Error.Message == "" {
				t.Errorf("the example of an error has the code %q and the message %q", e.Error.Code, e.Error.Message)
			}
		case "contract result":
			var c plugincontract.Contract
			strict(t, what, m[3], &c)
			parsed, err := plugincontract.ParseContract([]byte(m[3]))
			check(what, err)
			if !reflect.DeepEqual(parsed, c) {
				t.Errorf("the example of %s reads as %+v and as %+v", what, parsed, c)
			}
		case "index input":
			var in plugincontract.IndexInput
			strict(t, what, m[3], &in)
			check(what, in.Validate())
			indexed = append(indexed, in)
		case "index result":
			var out plugincontract.IndexResult
			strict(t, what, m[3], &out)
			check(what, out.Validate(indexed[0]))
		case "status input":
			var in plugincontract.StatusInput
			strict(t, what, m[3], &in)
			check(what, in.Validate())
		case "status result":
			var out plugincontract.StatusResult
			strict(t, what, m[3], &out)
			check(what, out.Validate())
			var states []string
			for _, source := range out.Sources {
				states = append(states, source.State)
			}
			slices.Sort(states)
			if !slices.Equal(states, []string{plugincontract.StateFailed, plugincontract.StateIndexed, plugincontract.StateIndexing}) {
				t.Errorf("the example of %s shows the states %v, want one of each", what, states)
			}
		case "cancel input":
			var in plugincontract.CancelInput
			strict(t, what, m[3], &in)
			check(what, in.Validate())
		case "cancel result":
			var out plugincontract.CancelResult
			strict(t, what, m[3], &out)
		case "search input":
			strict(t, what, m[3], &searched)
			check(what, searched.Validate())
		case "search result":
			var out plugincontract.SearchResult
			strict(t, what, m[3], &out)
			check(what, out.Validate(searched))
			if len(out.Passages) < 2 {
				t.Errorf("the example of %s shows %d Passages: show the order of two", what, len(out.Passages))
			}
		case "passage input":
			strict(t, what, m[3], &asked)
			check(what, asked.Validate())
		case "passage result":
			var out plugincontract.PassageResult
			strict(t, what, m[3], &out)
			check(what, out.Validate(asked))
		default:
			t.Errorf("the page has an example of %q, which this test does not know", what)
		}
	}
	// Every tool is shown being asked and answering, and a refusal is shown.
	want := map[string]int{"error": 1, "contract result": 1, "index input": 2, "index result": 1}
	for _, tool := range []string{plugincontract.ToolStatus, plugincontract.ToolCancel, plugincontract.ToolSearch, plugincontract.ToolPassage} {
		want[tool+" input"], want[tool+" result"] = 1, 1
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("the page has these examples: %v\nwant: %v", seen, want)
	}
	// The two examples of index show the two kinds of Source.
	if len(indexed) == 2 && (indexed[0].Source.Kind != plugincontract.SourceFile || indexed[1].Source.Kind != plugincontract.SourceURL) {
		t.Errorf("the examples of index show the kinds %s and %s, want a file and then a web page", indexed[0].Source.Kind, indexed[1].Source.Kind)
	}
	// And every block of JSON on the page is one of them.
	if blocks := strings.Count(doc, "```json"); blocks != len(example.FindAllString(doc, -1)) {
		t.Errorf("the page has %d blocks of JSON and %d examples this test reads: head each with <!-- example: ... -->", blocks,
			len(example.FindAllString(doc, -1)))
	}
}

// The check itself must catch a field the page lost and one it made up.
func TestThePageCheckCatchesGaps(t *testing.T) {
	section := sections(readDoc(t))[plugincontract.ToolSearch]
	if !slices.Contains(documentedFields(section), "score") || !slices.Contains(documentedFields(section), "last") {
		t.Fatalf("the fields of the search section are read as %v", documentedFields(section))
	}
	lost := strings.Replace(section, "| `score` |", "| the score |", 1)
	if lost == section || slices.Contains(documentedFields(lost), "score") {
		t.Error("a field taken out of the table still counts as described")
	}
	made := documentedFields(section + "| `rank` | The Passage's rank. |\n")
	if !slices.Contains(made, "rank") {
		t.Error("a field added to the table is not read")
	}
}
