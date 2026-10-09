package plugintest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// Target is a Knowledge base plugin to hold to the contract, and what the
// suite needs to know to try it.
type Target struct {
	// Connect returns the transport of a new MCP connection to the plugin.
	// The suite connects many times, and every connection must reach the
	// same index: for a command, start it again with the same folder in
	// LAMPLIGHT_PLUGIN_DATA (see CommandTransport); for a service, give its
	// address (see URLTransport).
	Connect func(t *testing.T) mcp.Transport
	// Restarts says that each connection is another start of the plugin, as
	// it is for a command, so that closing a connection stops whatever the
	// plugin was indexing. A service goes on when a connection closes.
	Restarts bool
	// WriteFile writes, at path, a file the plugin can index that holds the
	// pages given, each a list of paragraphs. Nil writes plain text:
	// paragraphs divided by blank lines and pages by form feeds, as the
	// test plugin reads it.
	WriteFile func(t *testing.T, path string, pages [][]string)
	// Extension is the extension of the files WriteFile writes. Empty is
	// ".txt".
	Extension string
	// ServePage returns the address of a web page that holds the paragraphs
	// given. Nil serves it from this computer's loopback interface: a
	// plugin that fetches no such address needs a ServePage it does fetch.
	ServePage func(t *testing.T, title string, paragraphs []string) string
	// Patience is how long the plugin may take to index one Source of a few
	// pages. Zero is 30 seconds.
	Patience time.Duration
}

// CommandTransport returns a Target's Connect for a plugin that is a
// command: every connection starts the command again, in an empty folder,
// with folder as its LAMPLIGHT_PLUGIN_DATA. Use Restarts with it.
//
// The plugin gets this test's environment, with an empty folder for a home
// and no XDG_* folders, so that it finds nothing of the real computer's and
// leaves nothing there. Set what it needs besides with t.Setenv.
func CommandTransport(folder string, command ...string) func(*testing.T) mcp.Transport {
	return func(t *testing.T) mcp.Transport {
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = t.TempDir()
		for _, kv := range os.Environ() {
			if name, _, _ := strings.Cut(kv, "="); name != "HOME" && name != plugincontract.DataFolderEnv && !strings.HasPrefix(name, "XDG_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+t.TempDir(), plugincontract.DataFolderEnv+"="+folder)
		var said bytes.Buffer
		cmd.Stderr = &said
		t.Cleanup(func() {
			if t.Failed() && said.Len() > 0 {
				t.Logf("the plugin wrote on its standard error:\n%s", said.String())
			}
		})
		return &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	}
}

// URLTransport returns a Target's Connect for a plugin that is a service
// answering at address.
func URLTransport(address string) func(*testing.T) mcp.Transport {
	return func(*testing.T) mcp.Transport {
		return &mcp.StreamableClientTransport{Endpoint: address, DisableStandaloneSSE: true, MaxRetries: -1}
	}
}

// Conformance holds a plugin to everything version 1 of the contract
// promises (docs/knowledge-base-contract.md), over MCP as study speaks it.
// It indexes Sources it writes itself, in collections of its own that no
// other run shares, and fails the test for whatever the plugin does that
// the contract does not allow.
//
// A plugin passes it by being a plugin. It knows nothing of how the plugin
// works: only what the six tools take and give back.
func Conformance(t *testing.T, target Target) {
	t.Helper()
	if target.Connect == nil {
		t.Fatal("the Target has no Connect")
	}
	if target.Patience <= 0 {
		target.Patience = 30 * time.Second
	}
	if target.Extension == "" {
		target.Extension = ".txt"
	}
	if target.WriteFile == nil {
		target.WriteFile = writeText
	}
	run := make([]byte, 4)
	_, _ = rand.Read(run)
	s := &suite{Target: target, run: hex.EncodeToString(run), dir: t.TempDir()}

	var contract plugincontract.Contract
	if !t.Run("it is an MCP server with the six tools", func(t *testing.T) {
		cs := s.connect(t)
		var names []string
		for tool, err := range cs.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatalf("listing its tools: %v", err)
			}
			names = append(names, tool.Name)
		}
		for _, tool := range plugincontract.Tools {
			if !slices.Contains(names, tool) {
				t.Errorf("it has no %s tool: it has %v", tool, names)
			}
		}
	}) {
		return
	}
	if !t.Run("contract says which version it speaks and what it can do", func(t *testing.T) {
		cs := s.connect(t)
		first := s.ok(t, cs, plugincontract.ToolContract, struct{}{}, reflect.TypeFor[plugincontract.Contract]())
		c, err := plugincontract.ParseContract(first)
		if err != nil {
			t.Fatalf("what contract gave back is not the contract's: %v\n%s", err, first)
		}
		if c.Version != plugincontract.Version {
			t.Fatalf("it speaks version %d, and this suite checks version %d", c.Version, plugincontract.Version)
		}
		// It says the same each time: study asks once for each Connection.
		second, err := plugincontract.ParseContract(s.ok(t, cs, plugincontract.ToolContract, struct{}{}, reflect.TypeFor[plugincontract.Contract]()))
		if err != nil || !reflect.DeepEqual(c, second) {
			t.Errorf("contract gave %+v and then %+v (%v)", c, second, err)
		}
		contract = c
	}) {
		return
	}
	s.contract = contract
	indexesFiles := slices.Contains(contract.Sources, plugincontract.SourceFile)
	indexesPages := slices.Contains(contract.Sources, plugincontract.SourceURL)

	t.Run("a collection nobody indexed is empty", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("empty")
		var status plugincontract.StatusResult
		s.into(t, cs, plugincontract.ToolStatus, plugincontract.StatusInput{Collection: collection}, &status)
		if len(status.Sources) != 0 {
			t.Errorf("status lists %+v in a collection nothing was indexed in", status.Sources)
		}
		in := plugincontract.SearchInput{Collection: collection, Query: "anything at all", Limit: 5}
		if found := s.search(t, cs, in); len(found) != 0 {
			t.Errorf("search found %+v in a collection nothing was indexed in", found)
		}
		if got := s.cancel(t, cs, collection, "nothing.a1"); got {
			t.Error("cancel says it stopped indexing a Source nobody asked to index")
		}
		s.refused(t, cs, plugincontract.ToolPassage, plugincontract.PassageInput{Collection: collection, ID: "nothing"}, plugincontract.CodeNotFound)
	})

	t.Run("what the contract does not allow is refused", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("refusals")
		const hash = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		file := map[string]any{"id": "book.a1", "title": "A book", "kind": "file", "path": filepath.Join(s.dir, "book"+s.Extension), "hash": hash}
		with := func(change func(map[string]any)) map[string]any {
			source := map[string]any{}
			for k, v := range file {
				source[k] = v
			}
			change(source)
			return source
		}
		for name, call := range map[string]struct {
			tool string
			in   any
		}{
			"status without a collection":          {plugincontract.ToolStatus, map[string]any{}},
			"a collection that is a path":          {plugincontract.ToolStatus, map[string]any{"collection": "../" + collection}},
			"a collection in capital letters":      {plugincontract.ToolStatus, map[string]any{"collection": "Linear-Algebra"}},
			"a collection that is no text":         {plugincontract.ToolStatus, map[string]any{"collection": 7}},
			"index without a Source":               {plugincontract.ToolIndex, map[string]any{"collection": collection}},
			"a Source without an id":               {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { delete(s, "id") })}},
			"a Source whose id is a path":          {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { s["id"] = "../book" })}},
			"a Source of no kind":                  {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { delete(s, "kind") })}},
			"a Source of a kind there is none of":  {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { s["kind"] = "recording" })}},
			"a file without a path":                {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { delete(s, "path") })}},
			"a file by a relative path":            {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { s["path"] = "book" + target.Extension })}},
			"a file without a hash":                {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { delete(s, "hash") })}},
			"a file whose hash is no hash":         {plugincontract.ToolIndex, map[string]any{"collection": collection, "source": with(func(s map[string]any) { s["hash"] = "md5:d41d8cd9" })}},
			"cancel without a Source":              {plugincontract.ToolCancel, map[string]any{"collection": collection}},
			"search without a query":               {plugincontract.ToolSearch, map[string]any{"collection": collection, "limit": 5}},
			"search for nothing":                   {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "  ", "limit": 5}},
			"search without a limit":               {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything"}},
			"search with a limit of none":          {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": 0}},
			"search with a limit over the highest": {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": plugincontract.MaxSearchLimit + 1}},
			"search in a list of no Sources":       {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": 5, "sources": []string{}}},
			"passage without an id":                {plugincontract.ToolPassage, map[string]any{"collection": collection}},
		} {
			s.refused(t, cs, call.tool, call.in, plugincontract.CodeInvalidArgument, name)
		}
		// What its contract does not list, it says it cannot do.
		if !indexesFiles {
			s.refused(t, cs, plugincontract.ToolIndex, map[string]any{"collection": collection, "source": file}, plugincontract.CodeUnsupported)
		}
		if !indexesPages {
			page := map[string]any{"id": "page.a1", "title": "A page", "kind": "url", "url": "https://example.org/"}
			s.refused(t, cs, plugincontract.ToolIndex, map[string]any{"collection": collection, "source": page}, plugincontract.CodeUnsupported)
		}
		// Nothing of that was indexed, and the plugin still answers.
		var status plugincontract.StatusResult
		s.into(t, cs, plugincontract.ToolStatus, plugincontract.StatusInput{Collection: collection}, &status)
		if len(status.Sources) != 0 {
			t.Errorf("status lists %+v after requests that were all refused", status.Sources)
		}
	})

	if indexesFiles {
		s.files(t)
	} else {
		t.Log("the plugin indexes no files: nothing about them was checked")
	}
	if indexesPages {
		s.pages(t)
	} else {
		t.Log("the plugin indexes no web pages: nothing about them was checked")
	}
}

// suite is one run of the conformance suite against one plugin.
type suite struct {
	Target
	// run names this run's collections apart from any other's, in a plugin
	// that is a service and keeps what earlier runs indexed.
	run      string
	dir      string
	contract plugincontract.Contract
	// made counts the Sources this run wrote.
	made atomic.Int64
}

func (s *suite) collection(name string) string { return "conformance-" + s.run + "-" + name }

// connect opens an MCP session with the plugin, as study does, and closes it
// when the test ends.
func (s *suite) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cs := s.open(t)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (s *suite) open(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Patience)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "lamplight-conformance", Version: "1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	cs, err := client.Connect(ctx, s.Connect(t), &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatalf("no MCP session could be opened with the plugin: %v", err)
	}
	return cs
}

// answer is what a tool call came to: the JSON object of its result, and
// what kind of answer it is.
type answer struct {
	data json.RawMessage
	// refusal is the contract's error when the tool refused with one, and
	// refused says that it refused at all: with isError, or with an error
	// of the protocol.
	refusal *plugincontract.ErrorBody
	refused bool
	said    string
}

// call calls a tool. A call that gets no answer at all fails the test.
func (s *suite) call(t *testing.T, cs *mcp.ClientSession, tool string, in any) answer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), s.Patience)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: in})
	var protocol *jsonrpc.Error
	switch {
	// The MCP library reports a request it could not send, or a connection
	// that closed, with errors of the protocol's type and codes of its own
	// (-32001 to -32005). Those are no answer of the plugin's.
	case errors.As(err, &protocol) && (protocol.Code > -32001 || protocol.Code < -32005) && ctx.Err() == nil:
		return answer{refused: true, said: protocol.Message}
	case err != nil:
		t.Fatalf("%s got no answer: %v", tool, err)
	}
	var a answer
	if len(res.Content) > 0 {
		if block, ok := res.Content[0].(*mcp.TextContent); ok {
			a.said = block.Text
		}
	}
	switch structured := res.StructuredContent.(type) {
	case nil:
		if strings.HasPrefix(strings.TrimSpace(a.said), "{") && json.Valid([]byte(a.said)) {
			a.data = json.RawMessage(a.said)
		}
	case json.RawMessage:
		a.data = structured
	default:
		a.data, _ = json.Marshal(structured)
	}
	if res.IsError {
		a.refused = true
		var refusal plugincontract.ErrorResult
		if a.data != nil && json.Unmarshal(a.data, &refusal) == nil && (refusal.Error.Code != "" || refusal.Error.Message != "") {
			a.refusal = &refusal.Error
			for _, problem := range shape("", a.data, reflect.TypeFor[plugincontract.ErrorResult]()) {
				t.Errorf("the error of %s: %s\n%s", tool, problem, a.data)
			}
		}
	}
	return a
}

// ok calls a tool that must succeed, checks that its result is spelled as
// the contract spells it, and returns the result.
func (s *suite) ok(t *testing.T, cs *mcp.ClientSession, tool string, in any, result reflect.Type) json.RawMessage {
	t.Helper()
	a := s.call(t, cs, tool, in)
	switch {
	case a.refused:
		t.Fatalf("%s refused %s: %s", tool, compact(in), a.said)
	case a.data == nil:
		t.Fatalf("%s gave back no JSON object, as structured content or as text: %q", tool, a.said)
	}
	if problems := shape("", a.data, result); len(problems) > 0 {
		t.Fatalf("what %s gave back is not shaped as the contract says: %s\n%s", tool, strings.Join(problems, "; "), a.data)
	}
	return a.data
}

// into calls a tool that must succeed and reads its result into out.
func (s *suite) into(t *testing.T, cs *mcp.ClientSession, tool string, in, out any) {
	t.Helper()
	data := s.ok(t, cs, tool, in, reflect.TypeOf(out).Elem())
	if err := plugincontract.Decode(data, out); err != nil {
		t.Fatalf("what %s gave back: %v\n%s", tool, err, data)
	}
}

// refused calls a tool that must refuse. A refusal shaped as the contract's
// must carry the code; one that is plain text, or an error of the protocol,
// is taken for invalid_argument and nothing else.
func (s *suite) refused(t *testing.T, cs *mcp.ClientSession, tool string, in any, code string, what ...string) {
	t.Helper()
	name := tool + " of " + compact(in)
	if len(what) > 0 {
		name = what[0] + " (" + name + ")"
	}
	a := s.call(t, cs, tool, in)
	switch {
	case !a.refused:
		t.Errorf("%s was not refused: it gave %s", name, a.data)
	case a.refusal != nil && a.refusal.Code != code:
		t.Errorf("%s was refused with the code %q, want %q (%s)", name, a.refusal.Code, code, a.refusal.Message)
	case a.refusal == nil && code != plugincontract.CodeInvalidArgument:
		t.Errorf("%s was refused without the contract's error, want one with the code %q: %q", name, code, a.said)
	case a.refusal != nil && strings.TrimSpace(a.refusal.Message) == "":
		t.Errorf("%s was refused without a message", name)
	}
}

func compact(v any) string {
	data, _ := json.Marshal(v)
	if len(data) > 300 {
		return string(data[:300]) + "…"
	}
	return string(data)
}

func (s *suite) search(t *testing.T, cs *mcp.ClientSession, in plugincontract.SearchInput) []plugincontract.ScoredPassage {
	t.Helper()
	var out plugincontract.SearchResult
	s.into(t, cs, plugincontract.ToolSearch, in, &out)
	if err := out.Validate(in); err != nil {
		t.Fatalf("what search gave back for %s breaks the contract: %v", compact(in), err)
	}
	return out.Passages
}

func (s *suite) cancel(t *testing.T, cs *mcp.ClientSession, collection, source string) bool {
	t.Helper()
	var out plugincontract.CancelResult
	s.into(t, cs, plugincontract.ToolCancel, plugincontract.CancelInput{Collection: collection, Source: source}, &out)
	return out.Stopped
}

// status asks what the plugin says of a collection, and returns it by
// Source.
func (s *suite) status(t *testing.T, cs *mcp.ClientSession, collection string) map[string]plugincontract.SourceStatus {
	t.Helper()
	var out plugincontract.StatusResult
	s.into(t, cs, plugincontract.ToolStatus, plugincontract.StatusInput{Collection: collection}, &out)
	if err := out.Validate(); err != nil {
		t.Fatalf("what status gave back breaks the contract: %v", err)
	}
	by := map[string]plugincontract.SourceStatus{}
	for _, source := range out.Sources {
		by[source.ID] = source
	}
	return by
}

// index asks for a Source to be indexed, and checks that the answer comes
// at once and speaks of that Source.
func (s *suite) index(t *testing.T, cs *mcp.ClientSession, collection string, source plugincontract.Source) plugincontract.SourceStatus {
	t.Helper()
	in := plugincontract.IndexInput{Collection: collection, Source: source}
	var out plugincontract.IndexResult
	began := time.Now()
	s.into(t, cs, plugincontract.ToolIndex, in, &out)
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("index took %s to answer: it answers at once, and the work goes on afterwards", took)
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("what index gave back breaks the contract: %v", err)
	}
	return out.Source
}

// settled waits until the plugin is no longer indexing a Source, and
// returns what it says of it then; ok is false when it does not list it.
func (s *suite) settled(t *testing.T, cs *mcp.ClientSession, collection, source string) (status plugincontract.SourceStatus, ok bool) {
	t.Helper()
	for deadline := time.Now().Add(s.Patience); ; time.Sleep(20 * time.Millisecond) {
		status, ok = s.status(t, cs, collection)[source]
		if !ok || status.State != plugincontract.StateIndexing {
			return status, ok
		}
		if time.Now().After(deadline) {
			t.Fatalf("Source %s was still being indexed after %s", source, s.Patience)
		}
	}
}

// indexed indexes a Source and waits for it to be indexed.
func (s *suite) indexed(t *testing.T, cs *mcp.ClientSession, collection string, source plugincontract.Source) plugincontract.SourceStatus {
	t.Helper()
	s.index(t, cs, collection, source)
	status, ok := s.settled(t, cs, collection, source.ID)
	if !ok || status.State != plugincontract.StateIndexed {
		t.Fatalf("Source %s was not indexed: status says %+v (listed: %v)", source.ID, status, ok)
	}
	return status
}

// book is a file the suite wrote to be a Source: pages of sentences that
// are in no other, each with a word of its own to find it by.
type book struct {
	source plugincontract.Source
	pages  [][]string
	// word is a word only the marked paragraph holds, sentence that
	// paragraph, and page the page it is on, from 1.
	word, sentence string
	page           int
}

// unique returns a word that is in nothing else: letters that no text
// extraction joins or splits.
func unique() string {
	const letters = "abcdeghjkmnopqrstuvwxyz"
	raw := make([]byte, 12)
	_, _ = rand.Read(raw)
	word := make([]byte, len(raw))
	for i, b := range raw {
		word[i] = letters[int(b)%len(letters)]
	}
	return "zq" + string(word)
}

// write writes a book of three pages, with its marked paragraph on the
// second.
func (s *suite) write(t *testing.T, name string) book {
	t.Helper()
	word, other := unique(), unique()
	b := book{word: word, page: 2, sentence: "The marker " + word + " stands on the second page and nowhere else."}
	b.pages = [][]string{
		{"This book was written to test a plugin, and opens with a page that says so.", "Its opening page has a second paragraph, about " + other + " and nothing more."},
		{b.sentence, "A later paragraph on the same page says something else about plain words."},
		{"The last page ends the book with one short paragraph."},
	}
	return s.save(t, name, b)
}

// save writes a book's pages to a file of its own and makes it a Source.
func (s *suite) save(t *testing.T, name string, b book) book {
	t.Helper()
	path := filepath.Join(s.dir, fmt.Sprintf("%s-%d%s", name, s.made.Add(1), s.Extension))
	s.WriteFile(t, path, b.pages)
	b.source = plugincontract.Source{ID: name + "." + unique()[:8], Title: "The " + name + " book", Kind: plugincontract.SourceFile,
		Path: path, Hash: hashOf(t, path)}
	return b
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeText writes pages as plain text: paragraphs divided by blank lines,
// pages by form feeds.
func writeText(t *testing.T, path string, pages [][]string) {
	t.Helper()
	var text []string
	for _, page := range pages {
		text = append(text, strings.Join(page, "\n\n"))
	}
	if err := os.WriteFile(path, []byte(strings.Join(text, "\n\f")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// squeeze makes text comparable whatever its line breaks and spaces.
func squeeze(text string) string { return strings.Join(strings.Fields(text), " ") }

// holding returns the Passages among found whose text holds the word.
func holding(found []plugincontract.ScoredPassage, word string) []plugincontract.ScoredPassage {
	var out []plugincontract.ScoredPassage
	for _, p := range found {
		if strings.Contains(p.Text, word) {
			out = append(out, p)
		}
	}
	return out
}

// ownText checks that a Passage is the Source's own text and nothing the
// plugin wrote: every word of it is a word of the Source.
func ownText(t *testing.T, passage plugincontract.Passage, paragraphs []string) {
	t.Helper()
	known := map[string]bool{}
	for _, paragraph := range paragraphs {
		for _, word := range words(paragraph) {
			known[word] = true
		}
	}
	for _, word := range words(passage.Text) {
		if !known[word] {
			t.Errorf("Passage %s holds %q, which is not in its Source: a Passage is the Source's own text, never a summary "+
				"and never an answer\n%s", passage.ID, word, passage.Text)
			return
		}
	}
}

func all(pages [][]string) []string {
	var out []string
	for _, page := range pages {
		out = append(out, page...)
	}
	return out
}

// files checks what the contract promises of file Sources, and through them
// of indexing, searching and Passages.
func (s *suite) files(t *testing.T) {
	t.Run("a file is indexed, and found by what it says", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("file")
		b := s.write(t, "found")
		first := s.index(t, cs, collection, b.source)
		if first.State == plugincontract.StateFailed {
			t.Fatalf("index says the Source failed at once: %s", first.Reason)
		}
		done, _ := s.settled(t, cs, collection, b.source.ID)
		if done.State != plugincontract.StateIndexed {
			t.Fatalf("the Source was not indexed: status says %+v", done)
		}
		if done.Hash != b.source.Hash {
			t.Errorf("status gives the hash %s for a file whose content hashes to %s", done.Hash, b.source.Hash)
		}

		in := plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit}
		found := s.search(t, cs, in)
		marked := holding(found, b.word)
		if len(marked) == 0 {
			t.Fatalf("a search for %q, which the Source says once, found no Passage that holds it: %+v", b.word, found)
		}
		for _, passage := range found {
			if passage.Source != b.source.ID {
				t.Errorf("Passage %s is from Source %s, and the collection holds only %s", passage.ID, passage.Source, b.source.ID)
			}
			ownText(t, passage.Passage, all(b.pages))
		}
		passage := marked[0]
		if !strings.Contains(squeeze(passage.Text), squeeze(b.sentence)) {
			t.Errorf("the Passage that holds %q does not hold the sentence it is in, word for word:\n%s\nwant\n%s", b.word, passage.Text, b.sentence)
		}
		switch l := passage.Location; {
		case slices.Contains(s.contract.Locations, plugincontract.LocationPages) && l == nil:
			t.Errorf("Passage %s has no location, and contract says the plugin gives pages", passage.ID)
		case l != nil && l.Kind == plugincontract.LocationPages && (l.First > b.page || l.Last < b.page || l.Last > len(b.pages)):
			t.Errorf("Passage %s is said to be on pages %d to %d of %d, and its text is on page %d", passage.ID, l.First, l.Last, len(b.pages), b.page)
		case l != nil && !slices.Contains(s.contract.Locations, l.Kind):
			t.Errorf("Passage %s has a location of the kind %s, which contract does not list: %v", passage.ID, l.Kind, s.contract.Locations)
		}

		// passage gives the same Passage again, without a score.
		var again plugincontract.PassageResult
		ask := plugincontract.PassageInput{Collection: collection, ID: passage.ID}
		s.into(t, cs, plugincontract.ToolPassage, ask, &again)
		if err := again.Validate(ask); err != nil {
			t.Fatalf("what passage gave back breaks the contract: %v", err)
		}
		if !reflect.DeepEqual(again.Passage, passage.Passage) {
			t.Errorf("passage gave %+v (location %+v), and the search gave %+v (location %+v)", again.Passage, again.Passage.Location,
				passage.Passage, passage.Location)
		}

		// Indexing content that is indexed does nothing, and says so at
		// once: the Passages are the ones they were.
		if second := s.index(t, cs, collection, b.source); second.State != plugincontract.StateIndexed || second.Hash != b.source.Hash {
			t.Errorf("index of a file that is indexed already gave %+v; want it indexed, with its hash", second)
		}
		if now := s.status(t, cs, collection)[b.source.ID]; now.State != plugincontract.StateIndexed {
			t.Errorf("after indexing a file that was indexed already, status says %+v", now)
		}
		s.into(t, cs, plugincontract.ToolPassage, ask, &again)
		if again.Passage.Text != passage.Text {
			t.Errorf("Passage %s changed although nothing was indexed again", passage.ID)
		}
		// A new title is no new content.
		renamed := b.source
		renamed.Title = "Another title for the same book"
		if third := s.index(t, cs, collection, renamed); third.State != plugincontract.StateIndexed {
			t.Errorf("index of the same content under another title gave %+v; want it indexed already", third)
		}

		// One Passage at most when one is asked for.
		in.Limit = 1
		if found := s.search(t, cs, in); len(found) > 1 {
			t.Errorf("a search with a limit of 1 gave %d Passages", len(found))
		}
		// An id the collection does not have.
		s.refused(t, cs, plugincontract.ToolPassage, plugincontract.PassageInput{Collection: collection, ID: "no-such-passage"}, plugincontract.CodeNotFound)
	})

	t.Run("a search can be limited to some Sources", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("sources")
		one, two := s.write(t, "one"), s.write(t, "two")
		s.indexed(t, cs, collection, one.source)
		s.indexed(t, cs, collection, two.source)
		everywhere := s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: one.word + " " + two.word, Limit: plugincontract.MaxSearchLimit})
		if len(holding(everywhere, one.word)) == 0 || len(holding(everywhere, two.word)) == 0 {
			t.Errorf("a search of the whole collection for a word of each Source did not find both: %+v", everywhere)
		}
		// Validate, in search, already refuses a Passage of another Source.
		only := s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: one.word + " " + two.word,
			Limit: plugincontract.MaxSearchLimit, Sources: []string{two.source.ID}})
		if len(holding(only, two.word)) == 0 || len(holding(only, one.word)) != 0 {
			t.Errorf("a search limited to one Source: %+v", only)
		}
		// A Source the collection does not have limits the search to
		// nothing.
		if none := s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: one.word, Limit: 5, Sources: []string{"no-such-source.a1"}}); len(none) != 0 {
			t.Errorf("a search limited to a Source the collection does not have found %+v", none)
		}
	})

	t.Run("collections are kept apart", func(t *testing.T) {
		cs := s.connect(t)
		here, there := s.collection("here"), s.collection("there")
		mine, theirs := s.write(t, "apart"), s.write(t, "apart")
		// The same Source id in both, with other content in each.
		theirs.source.ID = mine.source.ID
		s.indexed(t, cs, here, mine.source)
		s.indexed(t, cs, there, theirs.source)
		for _, tc := range []struct {
			collection string
			own, other book
		}{{here, mine, theirs}, {there, theirs, mine}} {
			found := s.search(t, cs, plugincontract.SearchInput{Collection: tc.collection, Query: tc.own.word + " " + tc.other.word, Limit: plugincontract.MaxSearchLimit})
			if len(holding(found, tc.own.word)) == 0 || len(holding(found, tc.other.word)) != 0 {
				t.Errorf("a search of %s found what is in the other collection, or not its own: %+v", tc.collection, found)
			}
			if status := s.status(t, cs, tc.collection); len(status) != 1 || status[tc.own.source.ID].Hash != tc.own.source.Hash {
				t.Errorf("status of %s = %+v; want its one Source with the hash %s", tc.collection, status, tc.own.source.Hash)
			}
		}
	})

	t.Run("content that changed is indexed anew", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("changed")
		before := s.write(t, "changing")
		s.indexed(t, cs, collection, before.source)
		old := holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: before.word, Limit: plugincontract.MaxSearchLimit}), before.word)
		if len(old) == 0 {
			t.Fatalf("no Passage holds %q", before.word)
		}
		// The same Source, with other content.
		after := s.write(t, "changing")
		after.source.ID = before.source.ID
		if now := s.indexed(t, cs, collection, after.source); now.Hash != after.source.Hash {
			t.Errorf("status gives the hash %s after the content changed to %s", now.Hash, after.source.Hash)
		}
		found := s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: before.word + " " + after.word, Limit: plugincontract.MaxSearchLimit})
		if len(holding(found, after.word)) == 0 || len(holding(found, before.word)) != 0 {
			t.Errorf("after the content changed, a search finds the old text or not the new: %+v", found)
		}
		// An old Passage's id names nothing now, or the text it named:
		// never another text.
		if a := s.call(t, cs, plugincontract.ToolPassage, plugincontract.PassageInput{Collection: collection, ID: old[0].ID}); !a.refused {
			var kept plugincontract.PassageResult
			if err := plugincontract.Decode(a.data, &kept); err != nil || kept.Passage.Text != old[0].Text {
				t.Errorf("the id of a Passage of the old content now names another text: %s", a.data)
			}
		} else if a.refusal == nil || a.refusal.Code != plugincontract.CodeNotFound {
			t.Errorf("passage refused the id of a Passage that is gone with %+v (%s); want not_found", a.refusal, a.said)
		}
	})

	t.Run("the hash is of what the plugin read", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("hash")
		b := s.write(t, "hashed")
		real := b.source.Hash
		// study hashed the file, and the file changed before the plugin
		// read it. A plugin indexes what it read and says which content
		// that is, or fails: it never gives the hash it was handed for
		// content it did not hash.
		b.source.Hash = "sha256:" + strings.Repeat("0", 64)
		s.index(t, cs, collection, b.source)
		status, ok := s.settled(t, cs, collection, b.source.ID)
		switch {
		case ok && status.State == plugincontract.StateIndexed && status.Hash != real:
			t.Errorf("status gives the hash %s for content that hashes to %s: it is the hash of what was read, not the one index was given", status.Hash, real)
		case !ok:
			t.Error("status does not list a Source whose file changed since study hashed it: it is indexed with the hash of what was read, or failed")
		}
	})

	t.Run("a file that cannot be read fails, with the reason", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("missing")
		b := s.write(t, "missing")
		good := b.source
		b.source.Path = filepath.Join(s.dir, "there-is-no-such-file"+s.Extension)
		// Asking is no error: what goes wrong with a Source is the Source's
		// state.
		s.index(t, cs, collection, b.source)
		status, ok := s.settled(t, cs, collection, b.source.ID)
		if !ok || status.State != plugincontract.StateFailed || strings.TrimSpace(status.Reason) == "" {
			t.Fatalf("status of a Source whose file is not there = %+v (listed: %v); want failed, with a reason", status, ok)
		}
		// Asked again with a file that is there, it is indexed.
		s.indexed(t, cs, collection, good)
	})

	t.Run("cancel stops the indexing of a Source", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("cancel")
		kept := s.write(t, "kept")
		s.indexed(t, cs, collection, kept.source)
		long := s.long(t, "long")
		s.index(t, cs, collection, long.source)
		stopped := s.cancel(t, cs, collection, long.source.ID)
		// However it went, nothing is being indexed once cancel has
		// answered.
		for range 5 {
			status := s.status(t, cs, collection)
			if now, listed := status[long.source.ID]; listed && now.State == plugincontract.StateIndexing {
				t.Fatalf("status says the Source is being indexed after cancel answered (stopped: %v)", stopped)
			} else if stopped && listed {
				// It was never indexed before, so it is as it was: not
				// listed. Being stopped is no failure of the Source's.
				t.Fatalf("cancel says it stopped the indexing of a Source that was not indexed before, and status says %+v: "+
					"want it not listed", now)
			} else if !stopped && (!listed || now.State == plugincontract.StateIndexing) {
				t.Fatalf("cancel says it stopped nothing, and status says %+v (listed: %v)", now, listed)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !stopped {
			t.Log("the plugin had indexed the long Source before cancel arrived: stopping halfway was not checked")
		}
		// What was indexed stays.
		if now := s.status(t, cs, collection)[kept.source.ID]; now.State != plugincontract.StateIndexed || now.Hash != kept.source.Hash {
			t.Errorf("after a cancel, status of a Source that was indexed before = %+v", now)
		}
		// Stopping what is not being indexed is no error, twice over.
		if again := s.cancel(t, cs, collection, long.source.ID); again && stopped {
			t.Error("cancel says it stopped the same indexing twice")
		}
		// Asked again, the plugin carries on or starts over, and finishes.
		s.indexed(t, cs, collection, long.source)
	})

	t.Run("two connections at once reach one index", func(t *testing.T) {
		// The MCP server an agent uses and a study source index in a shell
		// each have a connection of their own, and, to a plugin that is a
		// command, each a copy of the program.
		collection := s.collection("two")
		one, other := s.connect(t), s.connect(t)
		b := s.write(t, "shared")
		s.indexed(t, one, collection, b.source)
		in := plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit}
		if now := s.status(t, other, collection)[b.source.ID]; now.State != plugincontract.StateIndexed || now.Hash != b.source.Hash {
			t.Errorf("status over one connection of a Source indexed over another = %+v; want it indexed, with its hash", now)
		}
		found := holding(s.search(t, other, in), b.word)
		if len(found) == 0 {
			t.Fatalf("a search over one connection does not find what was indexed over another")
		}
		// And searching goes on while the other indexes.
		long := s.long(t, "meanwhile")
		s.index(t, one, collection, long.source)
		if during := holding(s.search(t, other, in), b.word); len(during) == 0 {
			t.Errorf("a search over one connection, while a Source is being indexed over another, no longer finds what was indexed")
		}
		var again plugincontract.PassageResult
		s.into(t, other, plugincontract.ToolPassage, plugincontract.PassageInput{Collection: collection, ID: found[0].ID}, &again)
		if again.Passage.Text != found[0].Text {
			t.Errorf("a Passage read over one connection while the other indexes is %q, and was %q", again.Passage.Text, found[0].Text)
		}
		s.cancel(t, one, collection, long.source.ID)
	})

	t.Run("what is indexed is there when the plugin is reached again", func(t *testing.T) {
		collection := s.collection("again")
		b := s.write(t, "again")
		first := s.open(t)
		s.indexed(t, first, collection, b.source)
		found := holding(s.search(t, first, plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit}), b.word)
		if len(found) == 0 {
			t.Fatalf("no Passage holds %q", b.word)
		}
		// With Restarts, the long Source is still being indexed when the
		// plugin is stopped.
		long := s.long(t, "interrupted")
		if s.Restarts {
			s.index(t, first, collection, long.source)
		}
		if err := first.Close(); err != nil {
			t.Logf("closing the connection: %v", err)
		}

		cs := s.connect(t)
		status := s.status(t, cs, collection)
		if now := status[b.source.ID]; now.State != plugincontract.StateIndexed || now.Hash != b.source.Hash {
			t.Errorf("status of a Source indexed over an earlier connection = %+v; want it indexed, with its hash", now)
		}
		var again plugincontract.PassageResult
		s.into(t, cs, plugincontract.ToolPassage, plugincontract.PassageInput{Collection: collection, ID: found[0].ID}, &again)
		if !reflect.DeepEqual(again.Passage, found[0].Passage) {
			t.Errorf("a Passage found over an earlier connection is now %+v, and was %+v", again.Passage, found[0].Passage)
		}
		if s.Restarts {
			// Nothing is being indexed until index is called: a plugin that
			// was stopped halfway carries on, or starts over, when asked.
			if now, listed := status[long.source.ID]; listed && now.State == plugincontract.StateIndexing {
				t.Errorf("a plugin that was stopped while indexing says, started again, that it is indexing: %+v", now)
			}
			s.indexed(t, cs, collection, long.source)
		}
	})
}

// long writes a book of many pages, which takes a plugin a while to index.
func (s *suite) long(t *testing.T, name string) book {
	t.Helper()
	b := book{word: unique()}
	for n := range 400 {
		b.pages = append(b.pages, []string{fmt.Sprintf("Page %d of a long book says %s and a little more, to give the plugin work.", n+1, unique())})
	}
	b.sentence, b.page = "The marker "+b.word+" stands on the last page.", len(b.pages)
	b.pages[len(b.pages)-1] = append(b.pages[len(b.pages)-1], b.sentence)
	return s.save(t, name, b)
}

// pages checks what the contract promises of web page Sources.
func (s *suite) pages(t *testing.T) {
	t.Run("a web page is indexed, and fetched again only when asked", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("page")
		word := unique()
		sentence := "The marker " + word + " stands on this page and nowhere else."
		paragraphs := []string{"This page was written to test a plugin.", sentence}
		var fetched atomic.Int64
		address, counted := "", false
		if s.ServePage != nil {
			address = s.ServePage(t, "A page for the conformance suite", paragraphs)
		} else {
			address, counted = servePage(t, &fetched, "A page for the conformance suite", paragraphs), true
		}
		source := plugincontract.Source{ID: "page." + unique()[:8], Title: "A page", Kind: plugincontract.SourceURL, URL: address}
		done := s.indexed(t, cs, collection, source)
		// A web page has no hash until it is fetched: the plugin gives the
		// one of what it fetched.
		if err := plugincontract.CheckHash(done.Hash); err != nil {
			t.Errorf("status of an indexed web page gives no hash of what was fetched: %v", err)
		}
		found := holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: word, Limit: plugincontract.MaxSearchLimit}), word)
		if len(found) == 0 {
			t.Fatalf("a search for %q, which the page says once, found no Passage that holds it", word)
		}
		if !strings.Contains(squeeze(found[0].Text), squeeze(sentence)) {
			t.Errorf("the Passage that holds %q does not hold its sentence, word for word:\n%s", word, found[0].Text)
		}
		ownText(t, found[0].Passage, paragraphs)
		if l := found[0].Location; l != nil && l.Kind == plugincontract.LocationPages {
			t.Errorf("a Passage of a web page is said to be on pages %d to %d: a web page has none", l.First, l.Last)
		}

		// Indexed already, the page is not fetched again...
		times := fetched.Load()
		if again := s.index(t, cs, collection, source); again.State != plugincontract.StateIndexed {
			t.Errorf("index of a web page that is indexed already gave %+v; want it indexed", again)
		}
		if _, ok := s.settled(t, cs, collection, source.ID); ok && counted && fetched.Load() != times {
			t.Errorf("the page was fetched %d more times although it was indexed and no fetch was asked for", fetched.Load()-times)
		}
		// ...unless that is asked for.
		source.Refetch = true
		s.index(t, cs, collection, source)
		if now, _ := s.settled(t, cs, collection, source.ID); now.State != plugincontract.StateIndexed {
			t.Errorf("after fetching the page again, status says %+v", now)
		}
		if counted && fetched.Load() == times {
			t.Error("the page was not fetched again when that was asked for")
		}
	})

	if s.ServePage != nil {
		return
	}
	t.Run("a web page that cannot be fetched fails, with the reason", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("unfetched")
		// An address nothing answers at: the port was free a moment ago.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := "http://" + listener.Addr().String() + "/page"
		listener.Close()
		source := plugincontract.Source{ID: "page." + unique()[:8], Title: "A page that is not there", Kind: plugincontract.SourceURL, URL: address}
		s.index(t, cs, collection, source)
		status, ok := s.settled(t, cs, collection, source.ID)
		if !ok || status.State != plugincontract.StateFailed || strings.TrimSpace(status.Reason) == "" {
			t.Errorf("status of a web page that cannot be fetched = %+v (listed: %v); want failed, with a reason", status, ok)
		}
	})
}

// servePage serves a web page from this computer's loopback interface and
// returns its address. It counts how often the page is fetched.
func servePage(t *testing.T, fetched *atomic.Int64, title string, paragraphs []string) string {
	t.Helper()
	var body strings.Builder
	fmt.Fprintf(&body, "<!doctype html>\n<html><head><title>%s</title></head><body>\n<h1>%s</h1>\n", html.EscapeString(title), html.EscapeString(title))
	for _, paragraph := range paragraphs {
		fmt.Fprintf(&body, "<p>%s</p>\n", html.EscapeString(paragraph))
	}
	body.WriteString("</body></html>\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body.String()))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/page"
}

// shape checks that a JSON value is spelled as the Go type that describes
// it: every field the contract requires is there, under exactly its name,
// and holds the kind of value it should. What the contract does not name is
// let through, since a plugin may say more. It returns what is wrong.
func shape(path string, raw json.RawMessage, typ reflect.Type) []string {
	at := func(what string) string {
		if path == "" {
			return what
		}
		return path + ": " + what
	}
	raw = bytes.TrimSpace(raw)
	if typ.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		return shape(path, raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &object) != nil {
			return []string{at("it is not a JSON object")}
		}
		var problems []string
		for _, f := range fields(typ) {
			value, ok := object[f.name]
			if !ok {
				for key := range object {
					if strings.EqualFold(key, f.name) {
						problems = append(problems, at(fmt.Sprintf("%q is spelled %q", f.name, key)))
						ok = true
					}
				}
				if !ok && !f.optional {
					problems = append(problems, at(fmt.Sprintf("%q is missing", f.name)))
				}
				continue
			}
			if f.optional && string(bytes.TrimSpace(value)) == "null" {
				continue
			}
			problems = append(problems, shape(strings.TrimPrefix(path+"."+f.name, "."), value, f.typ)...)
		}
		return problems
	case reflect.Slice:
		var items []json.RawMessage
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
			return []string{at("it is not a list")}
		}
		var problems []string
		for i, item := range items {
			problems = append(problems, shape(fmt.Sprintf("%s[%d]", path, i), item, typ.Elem())...)
		}
		return problems
	case reflect.String:
		if len(raw) == 0 || raw[0] != '"' {
			return []string{at("it is not text")}
		}
	case reflect.Bool:
		if string(raw) != "true" && string(raw) != "false" {
			return []string{at("it is not true or false")}
		}
	case reflect.Int:
		var n int64
		if json.Unmarshal(raw, &n) != nil {
			return []string{at("it is not a whole number")}
		}
	case reflect.Float64:
		var n float64
		if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
			return []string{at("it is not a number")}
		}
	}
	return nil
}

// field is one field of a contract type, as JSON names it.
type field struct {
	name     string
	optional bool
	typ      reflect.Type
}

// fields lists a contract type's fields, those of an embedded type among
// them.
func fields(typ reflect.Type) []field {
	var out []field
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous {
			out = append(out, fields(f.Type)...)
			continue
		}
		name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, field{name: name, optional: strings.Contains(options, "omitempty"), typ: f.Type})
	}
	return out
}
