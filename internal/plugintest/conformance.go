package plugintest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// Target is a Knowledge base plugin to hold to the contract, and what the
// suite needs to know to try it.
type Target struct {
	// Connect opens a new connection to the plugin. The suite connects many
	// times, and every connection must reach the same index: AsCommand
	// starts a command again each time, as study starts it, and AsService
	// reaches a service at its address.
	Connect func(t *testing.T) *Link
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
	// Patience is how long the plugin may take to answer one call, and to
	// index one Source of a few pages. Zero is 30 seconds.
	Patience time.Duration
}

// Link is one connection to a plugin.
type Link struct {
	// Transport carries MCP to the plugin.
	Transport mcp.Transport
	// Command is the program, when the connection is a command the suite
	// started, and nil for a service. The suite stops it, and checks what
	// the contract asks of a command through it: that it exits when its
	// input closes, writes nothing where it was started, and stays inside
	// its folder.
	Command *pluginclient.Command
}

// AsCommand returns a Target's Connect for a plugin that is a command, on a
// computer made for the test (NewComputer). Every connection starts the
// command again exactly as study starts a registered plugin
// (pluginclient.StartCommand): the same program, working folder, pipes,
// session, environment and folder. Use Restarts with it.
//
// So the plugin gets the small environment study gives one, with the
// computer's empty home folder as its HOME: it finds nothing of the real
// computer's, and leaves nothing there. What it needs besides, give it in
// its arguments.
func AsCommand(c *Computer, command ...string) func(*testing.T) *Link {
	var (
		register sync.Once
		plugin   pluginregistry.Plugin
	)
	return func(t *testing.T) *Link {
		t.Helper()
		// Once: the registry is in the folder the plugin is started in, and
		// the suite checks that nothing changes there.
		register.Do(func() { plugin = c.Register(t, "conformance", command...) })
		started, err := pluginclient.StartCommand(c.Env(""), plugin, pluginclient.Options{Environ: c.Environ(), StopGrace: 2 * time.Second})
		if err != nil {
			t.Fatalf("the plugin could not be started as study starts one: %v", err)
		}
		t.Cleanup(func() {
			// Whatever became of the test, nothing is left running.
			started.Stop()
			if said := started.Wrote(); t.Failed() && said != "" {
				// Quoted: it is the plugin's text, and a terminal would act
				// on what is in it.
				t.Logf("the plugin wrote on its standard error: %q", said)
			}
		})
		return &Link{Transport: started.Transport(), Command: started}
	}
}

// AsService returns a Target's Connect for a plugin that is a service
// answering at address, reached as study reaches a plugin registered by
// URL (pluginclient.ServiceTransport).
func AsService(address string) func(*testing.T) *Link {
	return func(*testing.T) *Link {
		return &Link{Transport: pluginclient.ServiceTransport(address)}
	}
}

// Conformance holds a plugin to everything version 1 of the contract
// promises (docs/knowledge-base-contract.md), over MCP as study speaks it.
// It indexes Sources it writes itself, in collections of its own that no
// other run shares, and fails the test for whatever the plugin does that
// the contract does not allow.
//
// A plugin passes it by being a plugin. It knows nothing of how the plugin
// works: only what the six tools take and give back, and, of a command,
// what can be seen of a program from outside it.
//
// A plugin that stops answering fails the check that met it, and the suite
// ends there: it does not wait for the same silence once for each check
// that follows.
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
	defer func() {
		if why := s.dead.Load(); why != nil {
			t.Errorf("the suite stopped at the first call the plugin did not answer (%s): nothing after it was checked", *why)
		}
	}()

	var contract plugincontract.Contract
	if !s.sub(t, "it is an MCP server with the six tools", func(t *testing.T) {
		cs := s.connect(t)
		var names []string
		ctx, cancel := context.WithTimeout(context.Background(), s.Patience)
		defer cancel()
		for tool, err := range cs.Tools(ctx, nil) {
			if err != nil {
				s.gaveUp(t, cs, fmt.Sprintf("listing its tools: %v", err))
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
	if !s.sub(t, "contract says which version it speaks and what it can do", func(t *testing.T) {
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

	if s.command {
		s.sub(t, "it exits when its standard input closes", func(t *testing.T) {
			cs := s.open(t)
			s.ok(t, cs, plugincontract.ToolContract, struct{}{}, reflect.TypeFor[plugincontract.Contract]())
			if !s.hangUp(cs) {
				t.Errorf("the plugin did not exit within 2 seconds of its standard input closing: study had to signal it. " +
					"Exit when your standard input closes")
			}
		})
	}

	s.sub(t, "a collection nobody indexed is empty", func(t *testing.T) {
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

	s.sub(t, "what the contract does not allow is refused", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("refusals")
		const hash = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		file := map[string]any{"id": "book.a1", "title": "A book", "kind": "file", "path": filepath.Join(s.dir, "book"+s.Extension), "hash": hash}
		page := map[string]any{"id": "page.a1", "title": "A page", "kind": "url", "url": "https://example.org/"}
		with := func(source map[string]any, change func(map[string]any)) map[string]any {
			changed := map[string]any{}
			for k, v := range source {
				changed[k] = v
			}
			change(changed)
			return changed
		}
		index := func(source map[string]any) any { return map[string]any{"collection": collection, "source": source} }
		type call struct {
			tool string
			in   any
		}
		calls := map[string]call{
			"status without a collection":     {plugincontract.ToolStatus, map[string]any{}},
			"a collection that is a path":     {plugincontract.ToolStatus, map[string]any{"collection": "../" + collection}},
			"a collection in capital letters": {plugincontract.ToolStatus, map[string]any{"collection": "Linear-Algebra"}},
			"a collection that is no text":    {plugincontract.ToolStatus, map[string]any{"collection": 7}},
			// An argument has its own name. A plugin whose JSON library
			// matches names in any letter case takes this for one it has.
			"a collection under another spelling":  {plugincontract.ToolStatus, map[string]any{"Collection": collection}},
			"a query under another spelling":       {plugincontract.ToolSearch, map[string]any{"collection": collection, "Query": "anything", "limit": 5}},
			"index without a Source":               {plugincontract.ToolIndex, map[string]any{"collection": collection}},
			"cancel without a Source":              {plugincontract.ToolCancel, map[string]any{"collection": collection}},
			"cancel without a collection":          {plugincontract.ToolCancel, map[string]any{"source": "book.a1"}},
			"search without a collection":          {plugincontract.ToolSearch, map[string]any{"query": "anything", "limit": 5}},
			"search without a query":               {plugincontract.ToolSearch, map[string]any{"collection": collection, "limit": 5}},
			"search for nothing":                   {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "", "limit": 5}},
			"search for white space":               {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": " \t\n ", "limit": 5}},
			"search without a limit":               {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything"}},
			"search with a limit of none":          {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": 0}},
			"search with a limit over the highest": {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": plugincontract.MaxSearchLimit + 1}},
			"search in a list of no Sources":       {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": 5, "sources": []string{}}},
			"search in a list that is null":        {plugincontract.ToolSearch, map[string]any{"collection": collection, "query": "anything", "limit": 5, "sources": nil}},
			"passage without an id":                {plugincontract.ToolPassage, map[string]any{"collection": collection}},
			"passage without a collection":         {plugincontract.ToolPassage, map[string]any{"id": "nothing"}},
		}
		// A Source that is not what the contract describes is asked of a
		// plugin in a kind it indexes: of a kind it does not index, it may
		// as well say that it cannot do it at all.
		source := page
		if indexesFiles {
			source = file
		}
		calls["index without a collection"] = call{plugincontract.ToolIndex, map[string]any{"source": source}}
		calls["a Source without an id"] = call{plugincontract.ToolIndex, index(with(source, func(s map[string]any) { delete(s, "id") }))}
		calls["a Source whose id is a path"] = call{plugincontract.ToolIndex, index(with(source, func(s map[string]any) { s["id"] = "../book" }))}
		calls["a Source without a title"] = call{plugincontract.ToolIndex, index(with(source, func(s map[string]any) { delete(s, "title") }))}
		calls["a Source of no kind"] = call{plugincontract.ToolIndex, index(with(source, func(s map[string]any) { delete(s, "kind") }))}
		calls["a Source of a kind there is none of"] = call{plugincontract.ToolIndex, index(with(source, func(s map[string]any) { s["kind"] = "recording" }))}
		if indexesFiles {
			calls["a file without a path"] = call{plugincontract.ToolIndex, index(with(file, func(s map[string]any) { delete(s, "path") }))}
			calls["a file by a relative path"] = call{plugincontract.ToolIndex, index(with(file, func(s map[string]any) { s["path"] = "book" + target.Extension }))}
			calls["a file without a hash"] = call{plugincontract.ToolIndex, index(with(file, func(s map[string]any) { delete(s, "hash") }))}
			calls["a file whose hash is no hash"] = call{plugincontract.ToolIndex, index(with(file, func(s map[string]any) { s["hash"] = "md5:d41d8cd9" }))}
			calls["a file with an address"] = call{plugincontract.ToolIndex, index(with(file, func(s map[string]any) { s["url"] = "https://example.org/" }))}
		}
		if indexesPages {
			calls["a web page without an address"] = call{plugincontract.ToolIndex, index(with(page, func(s map[string]any) { delete(s, "url") }))}
			calls["a web page that is a file"] = call{plugincontract.ToolIndex, index(with(page, func(s map[string]any) { s["url"] = "file:///etc/hostname" }))}
			calls["a web page with a password"] = call{plugincontract.ToolIndex, index(with(page, func(s map[string]any) { s["url"] = "https://ada:secret@example.org/" }))}
			calls["a web page with a path"] = call{plugincontract.ToolIndex, index(with(page, func(s map[string]any) { s["path"] = "/tmp/page.html" }))}
		}
		for name, c := range calls {
			s.refused(t, cs, c.tool, c.in, plugincontract.CodeInvalidArgument, name)
		}
		// What its contract does not list, it says it cannot do.
		if !indexesFiles {
			s.refused(t, cs, plugincontract.ToolIndex, map[string]any{"collection": collection, "source": file}, plugincontract.CodeUnsupported)
		}
		if !indexesPages {
			s.refused(t, cs, plugincontract.ToolIndex, map[string]any{"collection": collection, "source": page}, plugincontract.CodeUnsupported)
		}
		// A field this version does not name is passed over, not refused:
		// a later study may say more.
		if a := s.call(t, cs, plugincontract.ToolStatus, map[string]any{"collection": collection, "added_later": true}); a.refused {
			t.Errorf("status refused arguments that hold a field the contract does not name (%s): a plugin passes over what "+
				"it does not know", a.said)
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
	if s.command {
		if indexesFiles {
			// Last of what it indexes: it leaves the plugin's folder in
			// pieces.
			s.links(t)
		}
		s.sub(t, "it writes nothing in the folder it is started in", func(t *testing.T) {
			if now := tree(t, s.workFolder); now != s.workBefore {
				t.Errorf("the folder the plugin is started in, %s, changed while it ran: a plugin writes in its own folder, "+
					"the one LAMPLIGHT_PLUGIN_DATA names, and nowhere else.\nbefore:\n%s\nnow:\n%s", s.workFolder, s.workBefore, now)
			}
		})
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

	// dead says why the suite gave up on the plugin, once it has: a call
	// got no answer.
	dead atomic.Pointer[string]
	// lingers says that the plugin, a command, once did not exit when its
	// input was closed.
	lingers atomic.Bool

	// command says that the plugin is a command the suite starts. Its
	// folders are then known: the one it keeps its index in, and the one it
	// is started in, with what that one held before the plugin ran.
	first                              sync.Once
	command                            bool
	dataFolder, workFolder, workBefore string
}

func (s *suite) collection(name string) string { return "conformance-" + s.run + "-" + name }

// sub runs one check, unless the plugin has stopped answering.
func (s *suite) sub(t *testing.T, name string, check func(t *testing.T)) bool {
	t.Helper()
	if s.dead.Load() != nil {
		return false
	}
	return t.Run(name, check)
}

// session is one MCP session with the plugin.
type session struct {
	*mcp.ClientSession
	link *Link

	once     sync.Once
	onItsOwn bool
	// asked is the method of the first request the plugin sent, if it sent
	// one: that ended the session.
	asked atomic.Pointer[string]
}

// connect opens an MCP session with the plugin, as study does, and ends it
// when the test ends.
func (s *suite) connect(t *testing.T) *session {
	t.Helper()
	cs := s.open(t)
	t.Cleanup(func() { s.hangUp(cs) })
	return cs
}

func (s *suite) open(t *testing.T) *session {
	t.Helper()
	if why := s.dead.Load(); why != nil {
		t.Fatalf("not checked: the plugin stopped answering (%s)", *why)
	}
	link := s.Connect(t)
	if link == nil || link.Transport == nil {
		t.Fatal("the Target's Connect gave no connection")
	}
	s.first.Do(func() {
		if link.Command == nil {
			return
		}
		s.command, s.dataFolder, s.workFolder = true, link.Command.DataFolder(), link.Command.WorkingFolder()
		s.workBefore = tree(t, s.workFolder)
	})
	cs := &session{link: link}
	ctx, cancel := context.WithTimeout(context.Background(), s.Patience)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "lamplight-conformance", Version: "1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	// A plugin answers, and asks nothing: the first request it sends ends
	// the session, as it does with study.
	transport := pluginclient.AnswersOnly(link.Transport, func(method string) { cs.asked.CompareAndSwap(nil, &method) })
	opened, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: pluginclient.MCPVersion})
	if err != nil {
		s.gaveUp(t, cs, fmt.Sprintf("no MCP session could be opened with the plugin: %v", err))
	}
	cs.ClientSession = opened
	return cs
}

// hangUp ends a session as study ends one, and reports whether a command
// then exited on its own: its input is closed, and it is signalled, and
// then killed, only if it stays. A service is told the session is over.
func (s *suite) hangUp(cs *session) (onItsOwn bool) {
	cs.once.Do(func() {
		cs.onItsOwn = true
		if command := cs.link.Command; command != nil {
			// A plugin that stayed once is not waited for again: the check
			// that met it has failed, and every later connection would cost
			// the same two seconds.
			if s.lingers.Load() {
				command.Kill()
			}
			if cs.onItsOwn = command.Stop(); !cs.onItsOwn {
				s.lingers.Store(true)
			}
		}
		if cs.ClientSession == nil {
			return
		}
		// The library closes a connection once nothing is under way on it,
		// which a plugin that stopped answering never lets it be.
		done := make(chan struct{})
		go func() {
			_ = cs.ClientSession.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return cs.onItsOwn
}

// gaveUp ends the suite at a call the plugin did not answer: the plugin is
// stopped, and no later check waits for it again.
func (s *suite) gaveUp(t *testing.T, cs *session, why string) {
	t.Helper()
	if method := cs.asked.Load(); method != nil {
		why = fmt.Sprintf("the plugin sent a request (%s), which ended the session: a plugin answers what it is asked, and "+
			"sends study no requests. Turn off whatever in its MCP library sends them, such as a keep-alive ping", *method)
	}
	s.dead.CompareAndSwap(nil, &why)
	s.hangUp(cs)
	t.Fatal(why)
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

// call calls a tool. A call that gets no answer at all fails the test, and
// ends the suite.
func (s *suite) call(t *testing.T, cs *session, tool string, in any) answer {
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
	case err != nil && ctx.Err() != nil:
		s.gaveUp(t, cs, fmt.Sprintf("%s got no answer within %s", tool, s.Patience))
	case err != nil:
		s.gaveUp(t, cs, fmt.Sprintf("%s got no answer: %v", tool, err))
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
			for _, problem := range plugincontract.Shape(a.data, reflect.TypeFor[plugincontract.ErrorResult]()) {
				t.Errorf("the error of %s: %s\n%s", tool, problem, a.data)
			}
		}
	}
	return a
}

// ok calls a tool that must succeed, checks that its result is spelled as
// the contract spells it, and returns the result.
func (s *suite) ok(t *testing.T, cs *session, tool string, in any, result reflect.Type) json.RawMessage {
	t.Helper()
	a := s.call(t, cs, tool, in)
	switch {
	case a.refused:
		t.Fatalf("%s refused %s: %s", tool, compact(in), a.said)
	case a.data == nil:
		t.Fatalf("%s gave back no JSON object, as structured content or as text: %q", tool, a.said)
	}
	if problems := plugincontract.Shape(a.data, result); len(problems) > 0 {
		t.Fatalf("what %s gave back is not shaped as the contract says: %s\n%s", tool, strings.Join(problems, "; "), a.data)
	}
	return a.data
}

// into calls a tool that must succeed and reads its result into out.
func (s *suite) into(t *testing.T, cs *session, tool string, in, out any) {
	t.Helper()
	data := s.ok(t, cs, tool, in, reflect.TypeOf(out).Elem())
	if err := plugincontract.Decode(data, out); err != nil {
		t.Fatalf("what %s gave back: %v\n%s", tool, err, data)
	}
}

// refused calls a tool that must refuse. A refusal shaped as the contract's
// must carry the code; one that is plain text, or an error of the protocol,
// is taken for invalid_argument and nothing else.
func (s *suite) refused(t *testing.T, cs *session, tool string, in any, code string, what ...string) {
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

func (s *suite) search(t *testing.T, cs *session, in plugincontract.SearchInput) []plugincontract.ScoredPassage {
	t.Helper()
	var out plugincontract.SearchResult
	s.into(t, cs, plugincontract.ToolSearch, in, &out)
	if err := out.Validate(in); err != nil {
		t.Fatalf("what search gave back for %s breaks the contract: %v", compact(in), err)
	}
	return out.Passages
}

// passage asks for one Passage again, and checks that what comes back is a
// Passage and no more: a score belongs to a search.
func (s *suite) passage(t *testing.T, cs *session, collection, id string) plugincontract.Passage {
	t.Helper()
	in := plugincontract.PassageInput{Collection: collection, ID: id}
	data := s.ok(t, cs, plugincontract.ToolPassage, in, reflect.TypeFor[plugincontract.PassageResult]())
	var out plugincontract.PassageResult
	if err := plugincontract.Decode(data, &out); err != nil {
		t.Fatalf("what passage gave back: %v\n%s", err, data)
	}
	if err := out.Validate(in); err != nil {
		t.Fatalf("what passage gave back breaks the contract: %v", err)
	}
	var raw struct {
		Passage map[string]json.RawMessage `json:"passage"`
	}
	if err := json.Unmarshal(data, &raw); err == nil {
		if _, scored := raw.Passage["score"]; scored {
			t.Errorf("passage gave Passage %s with a score: a score belongs to one search, so search gives it and passage does not", id)
		}
	}
	return out.Passage
}

func (s *suite) cancel(t *testing.T, cs *session, collection, source string) bool {
	t.Helper()
	var out plugincontract.CancelResult
	s.into(t, cs, plugincontract.ToolCancel, plugincontract.CancelInput{Collection: collection, Source: source}, &out)
	return out.Stopped
}

// status asks what the plugin says of a collection, and returns it by
// Source.
func (s *suite) status(t *testing.T, cs *session, collection string) map[string]plugincontract.SourceStatus {
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
func (s *suite) index(t *testing.T, cs *session, collection string, source plugincontract.Source) plugincontract.SourceStatus {
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
func (s *suite) settled(t *testing.T, cs *session, collection, source string) (status plugincontract.SourceStatus, ok bool) {
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
func (s *suite) indexed(t *testing.T, cs *session, collection string, source plugincontract.Source) plugincontract.SourceStatus {
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

// timed says how long a call took at its quickest, of three: what the
// plugin needs for it when nothing else is going on.
func timed(call func()) time.Duration {
	quickest := time.Duration(-1)
	for range 3 {
		began := time.Now()
		call()
		if took := time.Since(began); quickest < 0 || took < quickest {
			quickest = took
		}
	}
	return quickest
}

// files checks what the contract promises of file Sources, and through them
// of indexing, searching and Passages.
func (s *suite) files(t *testing.T) {
	s.sub(t, "a file is indexed, and found by what it says", func(t *testing.T) {
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
		again := s.passage(t, cs, collection, passage.ID)
		if !reflect.DeepEqual(again, passage.Passage) {
			t.Errorf("passage gave %+v (location %+v), and the search gave %+v (location %+v)", again, again.Location,
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
		if again := s.passage(t, cs, collection, passage.ID); again.Text != passage.Text {
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

	s.sub(t, "a search gives no more Passages than its limit", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("limit")
		// A word that many paragraphs hold, three on each of four pages: a
		// search for it has more to give than a small limit allows, however
		// the plugin cuts, by the page or by the paragraph.
		b := book{word: unique()}
		for n := range 4 {
			var page []string
			for m := range 3 {
				page = append(page, fmt.Sprintf("Paragraph %d of page %d of this book holds the marker %s, as every one does, beside %s.",
					m+1, n+1, b.word, unique()))
			}
			b.pages = append(b.pages, page)
		}
		b = s.save(t, "many", b)
		s.indexed(t, cs, collection, b.source)
		in := plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit}
		every := holding(s.search(t, cs, in), b.word)
		if len(every) < 3 {
			t.Skipf("a search for a word that 12 paragraphs on 4 pages hold found %d Passages: the plugin cuts too coarsely for "+
				"the limit to be tried", len(every))
		}
		for _, limit := range []int{1, 2, len(every) - 1} {
			in.Limit = limit
			// search holds the answer to the contract, the limit included;
			// this says it in the words of the rule.
			var out plugincontract.SearchResult
			s.into(t, cs, plugincontract.ToolSearch, in, &out)
			if len(out.Passages) > limit {
				t.Errorf("a search with a limit of %d gave %d Passages: limit is the most a search gives back", limit, len(out.Passages))
			}
		}
	})

	s.sub(t, "a search can be limited to some Sources", func(t *testing.T) {
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

	s.sub(t, "collections are kept apart", func(t *testing.T) {
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

	s.sub(t, "content that changed is indexed anew", func(t *testing.T) {
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

	s.sub(t, "the hash is of what the plugin read", func(t *testing.T) {
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

	s.sub(t, "search and passage read the index, never the Source", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("gone")
		b := s.write(t, "gone")
		s.indexed(t, cs, collection, b.source)
		in := plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit}
		before := holding(s.search(t, cs, in), b.word)
		if len(before) == 0 {
			t.Fatalf("no Passage holds %q", b.word)
		}
		// The learner moved the book away. What was indexed of it is still
		// there to find, and to quote from.
		if err := os.Remove(b.source.Path); err != nil {
			t.Fatal(err)
		}
		after := holding(s.search(t, cs, in), b.word)
		if len(after) == 0 || after[0].ID != before[0].ID || after[0].Text != before[0].Text {
			t.Fatalf("once the file of an indexed Source is gone, a search for %q finds %+v, and found %+v before: a search reads "+
				"the index and never the Source", b.word, after, before)
		}
		if again := s.passage(t, cs, collection, before[0].ID); again.Text != before[0].Text {
			t.Errorf("once the file is gone, passage gives %q for a Passage that held %q", again.Text, before[0].Text)
		}
		if now := s.status(t, cs, collection)[b.source.ID]; now.State != plugincontract.StateIndexed || now.Hash != b.source.Hash {
			t.Errorf("once the file is gone, status says %+v of a Source nobody asked to index again; want it indexed, with its hash", now)
		}
	})

	s.sub(t, "a file that cannot be read fails, with the reason, and stays failed", func(t *testing.T) {
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
		// It stays failed, however often it is asked about: study asks more
		// than once, and the learner reads the reason later.
		for range 3 {
			time.Sleep(20 * time.Millisecond)
			again, ok := s.status(t, cs, collection)[b.source.ID]
			if !ok || again.State != plugincontract.StateFailed || strings.TrimSpace(again.Reason) == "" {
				t.Fatalf("status said a Source failed, and asked again says %+v (listed: %v): a Source stays failed until it is "+
					"indexed again", again, ok)
			}
		}
		// And however often the plugin is started: study starts a command
		// anew for each of its own commands, so the one that shows the
		// learner what failed is never the one that saw it fail. A service
		// is asked over another connection.
		later := s.connect(t)
		if again, ok := s.status(t, later, collection)[b.source.ID]; !ok || again.State != plugincontract.StateFailed ||
			strings.TrimSpace(again.Reason) == "" {
			t.Fatalf("a Source failed, and asked over a new connection the plugin says %+v (listed: %v): a Source stays failed, "+
				"with its reason, until it is indexed again, however often the plugin is started", again, ok)
		}
		// Asked again with a file that is there, it is indexed.
		s.indexed(t, cs, collection, good)

		// A Source that was indexed, and whose new content cannot be read:
		// what status says of it and what a search finds agree.
		bad := good
		bad.Path, bad.Hash = b.source.Path, "sha256:"+strings.Repeat("1", 64)
		s.index(t, cs, collection, bad)
		status, ok = s.settled(t, cs, collection, good.ID)
		if !ok || status.State != plugincontract.StateFailed {
			t.Fatalf("status of an indexed Source whose new file is not there = %+v (listed: %v); want failed", status, ok)
		}
		s.agrees(t, cs, collection, b, status)
	})

	s.sub(t, "cancel stops the indexing of a Source", func(t *testing.T) {
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

	s.sub(t, "what was indexed stays until new content replaces it", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("earlier")
		earlier := s.write(t, "earlier")
		s.indexed(t, cs, collection, earlier.source)
		// The same Source, with other content that takes a while.
		later := s.long(t, "later")
		later.source.ID = earlier.source.ID
		if first := s.index(t, cs, collection, later.source); first.State != plugincontract.StateIndexing {
			t.Skipf("index of new, long content answered %+v: the plugin is too quick for the time in between to be looked at", first)
		}
		// While the new content is being indexed, status and search agree
		// on what is there to find.
		before := s.status(t, cs, collection)[earlier.source.ID]
		found := len(holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: earlier.word,
			Limit: plugincontract.MaxSearchLimit, Sources: []string{earlier.source.ID}}), earlier.word)) > 0
		after := s.status(t, cs, collection)[earlier.source.ID]
		if before.State == plugincontract.StateIndexing && after.State == plugincontract.StateIndexing && before.Hash == after.Hash {
			switch {
			case found && before.Hash == "":
				t.Errorf("while new content is being indexed, a search still finds the earlier content of the Source, and status " +
					"gives no hash for it: hash is the hash of what search finds now, and is there while earlier content is")
			case found && before.Hash != earlier.source.Hash:
				t.Errorf("while new content is being indexed, a search finds the earlier content, which hashes to %s, and status "+
					"gives the hash %s", earlier.source.Hash, before.Hash)
			case !found && before.Hash != "":
				t.Errorf("while new content is being indexed, status gives the hash %s for the Source, and a search finds "+
					"nothing of that content", before.Hash)
			}
		} else {
			t.Log("the plugin had indexed the new content before it could be asked: the time in between was not checked")
		}
		// Stopped, the Source is as it was before index was called.
		if !s.cancel(t, cs, collection, earlier.source.ID) {
			t.Skip("the plugin had indexed the new content before cancel arrived: stopping over earlier content was not checked")
		}
		now, listed := s.status(t, cs, collection)[earlier.source.ID]
		if !listed || now.State != plugincontract.StateIndexed || now.Hash != earlier.source.Hash {
			t.Fatalf("after a cancel that stopped the indexing of new content, status says %+v (listed: %v) of a Source that was "+
				"indexed before: what was indexed stays, so want it indexed with the hash %s", now, listed, earlier.source.Hash)
		}
		if kept := holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: earlier.word, Limit: plugincontract.MaxSearchLimit}), earlier.word); len(kept) == 0 {
			t.Errorf("after a cancel that stopped the indexing of new content, a search no longer finds the earlier content: " +
				"what was indexed stays")
		}
	})

	s.sub(t, "status and search never wait for indexing", func(t *testing.T) {
		cs := s.connect(t)
		collection := s.collection("waiting")
		b := s.write(t, "quiet")
		s.indexed(t, cs, collection, b.source)
		// How long each takes when nothing is being indexed.
		quietStatus := timed(func() { s.status(t, cs, collection) })
		quietSearch := timed(func() {
			s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: plugincontract.MaxSearchLimit})
		})
		// A call waited when it took far longer than that and came back
		// only once the indexing was over. One slow answer can be the
		// computer's doing, so each is tried twice.
		slow := func(took, quiet time.Duration) bool { return took > 3*quiet+500*time.Millisecond }

		waited := 0
		for range 2 {
			long := s.long(t, "status-meanwhile")
			if first := s.index(t, cs, collection, long.source); first.State != plugincontract.StateIndexing {
				t.Logf("index of a long Source answered %+v: the plugin is too quick for status to be asked meanwhile", first)
				break
			}
			began := time.Now()
			now, listed := s.status(t, cs, collection)[long.source.ID]
			took := time.Since(began)
			s.cancel(t, cs, collection, long.source.ID)
			if listed && now.State == plugincontract.StateIndexing || !slow(took, quietStatus) {
				waited = 0
				break
			}
			waited++
			t.Logf("status took %s while a Source was being indexed, and %s otherwise, and then said %+v", took, quietStatus, now)
		}
		if waited == 2 {
			t.Errorf("status answers only once indexing is over: it answers from what the plugin knows, and never waits for indexing")
		}

		waited = 0
		for range 2 {
			long := s.long(t, "search-meanwhile")
			if first := s.index(t, cs, collection, long.source); first.State != plugincontract.StateIndexing {
				t.Logf("index of a long Source answered %+v: the plugin is too quick for a search to be made meanwhile", first)
				break
			}
			// The word is on the last page: found, the indexing is over.
			began := time.Now()
			found := holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: long.word, Limit: plugincontract.MaxSearchLimit}), long.word)
			took := time.Since(began)
			s.cancel(t, cs, collection, long.source.ID)
			if len(found) == 0 || !slow(took, quietSearch) {
				waited = 0
				break
			}
			waited++
			t.Logf("a search took %s while a Source was being indexed, and %s otherwise, and found what was indexed last", took, quietSearch)
		}
		if waited == 2 {
			t.Errorf("search answers only once indexing is over: it finds what is indexed now, and never waits for indexing")
		}
	})

	s.sub(t, "two connections at once reach one index", func(t *testing.T) {
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
		if again := s.passage(t, other, collection, found[0].ID); again.Text != found[0].Text {
			t.Errorf("a Passage read over one connection while the other indexes is %q, and was %q", again.Text, found[0].Text)
		}
		s.cancel(t, one, collection, long.source.ID)
	})

	s.sub(t, "what is indexed is there when the plugin is reached again", func(t *testing.T) {
		collection := s.collection("again")
		b := s.write(t, "again")
		first := s.open(t)
		t.Cleanup(func() { s.hangUp(first) })
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
		if lingered := s.lingers.Load(); !s.hangUp(first) && !lingered {
			t.Errorf("the plugin did not exit within 2 seconds of its standard input closing while it was indexing: study had " +
				"to signal it. Exit when your standard input closes, whatever you are doing")
		}

		cs := s.connect(t)
		status := s.status(t, cs, collection)
		if now := status[b.source.ID]; now.State != plugincontract.StateIndexed || now.Hash != b.source.Hash {
			t.Errorf("status of a Source indexed over an earlier connection = %+v; want it indexed, with its hash", now)
		}
		if again := s.passage(t, cs, collection, found[0].ID); !reflect.DeepEqual(again, found[0].Passage) {
			t.Errorf("a Passage found over an earlier connection is now %+v, and was %+v", again, found[0].Passage)
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

// agrees checks that what status says of a Source that is not indexed, and
// was, agrees with what a search finds: hash is the hash of what search
// finds for the Source now, there when its earlier content is and left out
// when it is not.
func (s *suite) agrees(t *testing.T, cs *session, collection string, earlier book, status plugincontract.SourceStatus) {
	t.Helper()
	found := len(holding(s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: earlier.word,
		Limit: plugincontract.MaxSearchLimit}), earlier.word)) > 0
	switch {
	case found && status.Hash == "":
		t.Errorf("status says %s of a Source whose earlier content a search still finds, and gives no hash for it: hash is the "+
			"hash of what search finds now", status.State)
	case found && status.Hash != earlier.source.Hash:
		t.Errorf("status says %s of a Source whose earlier content, which hashes to %s, a search still finds, and gives the hash %s",
			status.State, earlier.source.Hash, status.Hash)
	case !found && status.Hash != "":
		t.Errorf("status says %s of a Source and gives the hash %s, and a search finds nothing of that content", status.State, status.Hash)
	}
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
	s.sub(t, "a web page is indexed, and fetched again only when asked", func(t *testing.T) {
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
	s.sub(t, "a web page that cannot be fetched fails, with the reason", func(t *testing.T) {
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

// links checks that a plugin that is a command stays inside its folder: it
// follows no symbolic link there that leads out of it.
//
// The folder is outside the Study home, where the agent cannot write. Even
// so a plugin runs outside the agent's sandbox for as long as it lives, and
// names its folder by a path: what it does with a name there that leads
// elsewhere is what stands between a mistake in that folder and a file of
// the learner's being overwritten.
func (s *suite) links(t *testing.T) {
	// try indexes a small book and searches for it, taking whatever the
	// plugin answers: with its folder in pieces it may refuse, or fail the
	// Source. Only what it writes is looked at.
	try := func(t *testing.T, collection string) {
		cs := s.connect(t)
		b := s.write(t, "linked")
		if a := s.call(t, cs, plugincontract.ToolIndex, plugincontract.IndexInput{Collection: collection, Source: b.source}); a.refused {
			return
		}
		for deadline := time.Now().Add(s.Patience); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			a := s.call(t, cs, plugincontract.ToolStatus, plugincontract.StatusInput{Collection: collection})
			var status plugincontract.StatusResult
			if a.refused || a.data == nil || plugincontract.Decode(a.data, &status) != nil {
				break
			}
			if !slices.ContainsFunc(status.Sources, func(st plugincontract.SourceStatus) bool {
				return st.ID == b.source.ID && st.State == plugincontract.StateIndexing
			}) {
				break
			}
		}
		s.call(t, cs, plugincontract.ToolSearch, plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: 5})
		s.hangUp(cs)
	}

	// Before it starts a plugin to index, study makes an empty file whose
	// name begins with .lamplight- in the plugin's folder and removes it
	// again, to learn whether the folder can be written in. Another copy of
	// the plugin may be running meanwhile, and may see the file.
	s.sub(t, "it passes over a file study leaves in its folder for a moment", func(t *testing.T) {
		stray := filepath.Join(s.dataFolder, ".lamplight-write-check-conformance")
		if err := os.WriteFile(stray, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(stray) })
		collection := s.collection("stray")
		cs := s.connect(t)
		b := s.write(t, "stray")
		a := s.call(t, cs, plugincontract.ToolIndex, plugincontract.IndexInput{Collection: collection, Source: b.source})
		if a.refused {
			t.Fatalf("with a file called %s in its folder, the plugin refused to index: a plugin passes over a name there "+
				"that begins with .lamplight-, which study makes and removes to learn whether the folder can be written in",
				filepath.Base(stray))
		}
		status, ok := s.settled(t, cs, collection, b.source.ID)
		if !ok || status.State != plugincontract.StateIndexed {
			t.Fatalf("with a file called %s in its folder, the plugin did not index a Source: status says %+v (listed: %v). "+
				"A plugin passes over a name there that begins with .lamplight-", filepath.Base(stray), status, ok)
		}
		if listed := s.status(t, cs, collection); len(listed) != 1 {
			t.Errorf("with a file called %s in its folder, status lists %d Sources of a collection that holds one: %v",
				filepath.Base(stray), len(listed), listed)
		}
		if found := s.search(t, cs, plugincontract.SearchInput{Collection: collection, Query: b.word, Limit: 5}); len(found) == 0 {
			t.Errorf("with a file called %s in its folder, a search finds nothing of what was indexed", filepath.Base(stray))
		}
	})

	s.sub(t, "it follows no link out of its folder that was there before it", func(t *testing.T) {
		collection := s.collection("link")
		outside := t.TempDir()
		folder := filepath.Join(outside, "folder")
		if err := os.Mkdir(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		// Names a plugin is likely to give what it keeps for a collection:
		// the collection's own, as a folder, and as a file of some kind.
		planted := map[string]string{collection: folder}
		for _, ending := range []string{".json", ".db", ".sqlite", ".sqlite3", ".idx", ".index", ".bin"} {
			planted[collection+ending] = filepath.Join(outside, "file"+ending)
		}
		for name, leads := range planted {
			if err := os.Symlink(leads, filepath.Join(s.dataFolder, name)); err != nil {
				t.Fatal(err)
			}
		}
		before := tree(t, outside)
		try(t, collection)
		if now := tree(t, outside); now != before {
			t.Errorf("the plugin wrote outside its folder, through a symbolic link that was in it: a plugin opens its folder "+
				"once and follows no link out of it.\nwhere the links lead, before:\n%s\nnow:\n%s", before, now)
		}
		for name := range planted {
			_ = os.Remove(filepath.Join(s.dataFolder, name))
		}
	})

	s.sub(t, "it follows no link out of its folder that takes the place of what it made", func(t *testing.T) {
		collection := s.collection("swap")
		// The plugin makes what it makes for a collection, and stops.
		cs := s.open(t)
		t.Cleanup(func() { s.hangUp(cs) })
		s.indexed(t, cs, collection, s.write(t, "swapped").source)
		s.hangUp(cs)
		// Everything in its folder is exchanged for a link to a copy of it
		// elsewhere, as someone who can write the folder could do.
		entries, err := os.ReadDir(s.dataFolder)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Skipf("the plugin's folder, %s, is empty after it indexed a Source: it keeps its index elsewhere", s.dataFolder)
		}
		outside := t.TempDir()
		for _, entry := range entries {
			from, to := filepath.Join(s.dataFolder, entry.Name()), filepath.Join(outside, entry.Name())
			// A link is left as it is, and so is a pipe or a socket, which
			// is never opened: reading a pipe nobody writes to waits for
			// ever.
			if !entry.IsDir() && !entry.Type().IsRegular() {
				continue
			}
			if err := os.CopyFS(to, os.DirFS(from)); entry.IsDir() && err != nil {
				t.Fatal(err)
			}
			if !entry.IsDir() {
				data, err := os.ReadFile(from)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(to, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.RemoveAll(from); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(to, from); err != nil {
				t.Fatal(err)
			}
		}
		before := tree(t, outside)
		try(t, collection)
		if now := tree(t, outside); now != before {
			t.Errorf("the plugin wrote outside its folder, through a symbolic link that had taken the place of what it keeps "+
				"there: a plugin opens its folder once and follows no link out of it.\nwhere the links lead, before:\n%s\nnow:\n%s",
				before, now)
		}
	})
}

// tree describes everything in a folder: each name with what it is and, for
// a file, what it holds. Two descriptions are equal when nothing was added,
// removed or changed.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			leads, _ := os.Readlink(path)
			lines = append(lines, rel+" -> "+leads)
		case entry.IsDir():
			lines = append(lines, rel+"/")
		case !entry.Type().IsRegular():
			// A pipe, a socket or a device is named for what it is and
			// never opened: reading a pipe nobody writes to waits for ever.
			lines = append(lines, rel+" "+entry.Type().String())
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			lines = append(lines, fmt.Sprintf("%s %d bytes %s", rel, len(data), hex.EncodeToString(sum[:6])))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("looking at %s: %v", dir, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
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
