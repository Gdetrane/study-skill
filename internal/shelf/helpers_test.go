//go:build unix

package shelf_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	pdfreader "github.com/mordor-forge/lamplight/v2/internal/shelf/pdf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// patience is how long a test waits for Shelf to have indexed a Source, or
// to answer: long enough for a computer that is busy with other tests, and
// only ever waited out by a test that fails.
const patience = 5 * time.Minute

// session is an MCP session with a Shelf, as study has one.
type session struct {
	t  *testing.T
	cs *mcp.ClientSession
}

// start starts a Shelf on a folder in this test's own process, and opens a
// session with it. The Shelf is stopped when the test ends.
func start(t *testing.T, folder string) (*shelf.Shelf, *session) {
	t.Helper()
	return startWith(t, folder, shelf.Settings{})
}

// warmed is done once the PDF reader is compiled in this process.
var warmed sync.Once

// warm has the PDF reader compiled before a test begins. A Shelf compiles it
// when it first reads a PDF, which takes a few seconds, and under the race
// detector the better part of a minute: no test's own clock should be
// running then.
func warm(t *testing.T) {
	t.Helper()
	warmed.Do(func() {
		file := pdf([]string{"A page that is read to have the reader compiled."})
		if err := pdfreader.Read(context.Background(), bytes.NewReader(file), int64(len(file)), func(int, int, string) error { return nil }); err != nil {
			t.Fatalf("the PDF reader does not read a page: %v", err)
		}
	})
}

// startWith starts a Shelf that works as settings say, and as a Shelf does
// by itself where they say nothing.
func startWith(t *testing.T, folder string, settings shelf.Settings) (*shelf.Shelf, *session) {
	t.Helper()
	warm(t)
	// The tests serve their web pages from this computer.
	settings.Fetch.Loopback = true
	plugin := shelf.New(shelf.Options{Folder: folder, Settings: settings})
	t.Cleanup(plugin.Close)
	return plugin, connect(t, plugin)
}

// connect opens another session with a Shelf in this process.
func connect(t *testing.T, plugin *shelf.Shelf) *session {
	t.Helper()
	here, there := mcp.NewInMemoryTransports()
	served, err := plugin.Server().Connect(context.Background(), there, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = served.Close() })
	return dial(t, here)
}

// run starts the built study-shelf as study starts a registered plugin, on a
// computer of temporary folders, and opens a session with it. It returns the
// command too, for a test that stops or kills it.
func run(t *testing.T, computer *plugintest.Computer) (*session, *pluginclient.Command) {
	t.Helper()
	link := plugintest.AsCommand(computer, shelftest.Shelf(t))(t)
	return dial(t, pluginclient.AnswersOnly(link.Transport, func(method string) {
		t.Errorf("Shelf sent study a request (%s): a plugin sends none", method)
	})), link.Command
}

func dial(t *testing.T, transport mcp.Transport) *session {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "shelf-test", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: pluginclient.MCPVersion})
	if err != nil {
		t.Fatalf("no session with Shelf: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &session{t: t, cs: cs}
}

// call calls a tool. It reads the result into out and returns nil, or
// returns the refusal.
func (s *session) call(tool string, in, out any) *plugincontract.ErrorBody {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: in})
	if err != nil {
		s.t.Fatalf("%s got no answer: %v", tool, err)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil || res.StructuredContent == nil {
		s.t.Fatalf("%s gave no structured content (%v)", tool, err)
	}
	// The same JSON as text, for a client that reads only that.
	var said any
	if text, ok := res.Content[0].(*mcp.TextContent); !ok || json.Unmarshal([]byte(text.Text), &said) != nil || compact(said) != string(data) {
		s.t.Errorf("%s gave %s as structured content, and other text beside it", tool, data)
	}
	if res.IsError {
		var refusal plugincontract.ErrorResult
		if err := plugincontract.Decode(data, &refusal); err != nil {
			s.t.Fatalf("%s refused without the contract's error: %v\n%s", tool, err, data)
		}
		return &refusal.Error
	}
	if err := plugincontract.Decode(data, out); err != nil {
		s.t.Fatalf("what %s gave back: %v\n%s", tool, err, data)
	}
	return nil
}

func compact(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// ok calls a tool that must not refuse.
func (s *session) ok(tool string, in, out any) {
	s.t.Helper()
	if refusal := s.call(tool, in, out); refusal != nil {
		s.t.Fatalf("%s refused %s: %s (%s)", tool, compact(in), refusal.Message, refusal.Code)
	}
}

func (s *session) index(collection string, source plugincontract.Source) plugincontract.SourceStatus {
	s.t.Helper()
	in := plugincontract.IndexInput{Collection: collection, Source: source}
	var out plugincontract.IndexResult
	began := time.Now()
	s.ok(plugincontract.ToolIndex, in, &out)
	if took := time.Since(began); took > 5*time.Second {
		s.t.Errorf("index took %s to answer: it answers at once", took)
	}
	if err := out.Validate(in); err != nil {
		s.t.Fatalf("what index gave back breaks the contract: %v", err)
	}
	return out.Source
}

// status returns what Shelf says of a collection, by Source.
func (s *session) status(collection string) map[string]plugincontract.SourceStatus {
	s.t.Helper()
	var out plugincontract.StatusResult
	s.ok(plugincontract.ToolStatus, plugincontract.StatusInput{Collection: collection}, &out)
	if err := out.Validate(); err != nil {
		s.t.Fatalf("what status gave back breaks the contract: %v", err)
	}
	by := map[string]plugincontract.SourceStatus{}
	for _, source := range out.Sources {
		by[source.ID] = source
		if strings.Contains(source.Reason, testKey) {
			s.t.Errorf("the reason status gives for %s holds the key: %s", source.ID, source.Reason)
		}
	}
	return by
}

// settled waits until a Source is no longer being indexed, and returns what
// Shelf says of it then; listed is false when it says nothing.
func (s *session) settled(collection, source string) (status plugincontract.SourceStatus, listed bool) {
	s.t.Helper()
	for deadline := time.Now().Add(patience); ; time.Sleep(10 * time.Millisecond) {
		status, listed = s.status(collection)[source]
		if !listed || status.State != plugincontract.StateIndexing {
			return status, listed
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("Source %s was still being indexed after %s", source, patience)
		}
	}
}

// indexed indexes a Source and waits for it to be indexed.
func (s *session) indexed(collection string, source plugincontract.Source) plugincontract.SourceStatus {
	s.t.Helper()
	s.index(collection, source)
	status, listed := s.settled(collection, source.ID)
	if !listed || status.State != plugincontract.StateIndexed {
		s.t.Fatalf("Source %s was not indexed: status says %+v (listed: %v)", source.ID, status, listed)
	}
	return status
}

// fails indexes a Source that cannot be indexed, and returns the reason
// status gives, which holds each of the words given.
func (s *session) fails(collection string, source plugincontract.Source, words ...string) plugincontract.SourceStatus {
	s.t.Helper()
	if first := s.index(collection, source); first.State == plugincontract.StateIndexed {
		s.t.Fatalf("index says the Source is indexed: %+v", first)
	}
	status, listed := s.settled(collection, source.ID)
	if !listed || status.State != plugincontract.StateFailed {
		s.t.Fatalf("status of %s = %+v (listed: %v); want it failed", source.ID, status, listed)
	}
	for _, word := range words {
		if !strings.Contains(status.Reason, word) {
			s.t.Errorf("the reason does not say %q: %s", word, status.Reason)
		}
	}
	// The reason is for the learner, beside the Source: it does not repeat
	// where the file is, or all of an address.
	for _, not := range []string{source.Path, source.URL} {
		if not != "" && strings.Contains(status.Reason, not) {
			s.t.Errorf("the reason repeats %q: %s", not, status.Reason)
		}
	}
	return status
}

func (s *session) search(collection, query string, limit int, sources ...string) []plugincontract.ScoredPassage {
	s.t.Helper()
	in := plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit, Sources: sources}
	var out plugincontract.SearchResult
	s.ok(plugincontract.ToolSearch, in, &out)
	if err := out.Validate(in); err != nil {
		s.t.Fatalf("what search gave back breaks the contract: %v", err)
	}
	return out.Passages
}

// passage asks for one Passage again. found is false when the collection
// has none with that id.
func (s *session) passage(collection, id string) (passage plugincontract.Passage, found bool) {
	s.t.Helper()
	in := plugincontract.PassageInput{Collection: collection, ID: id}
	var out plugincontract.PassageResult
	if refusal := s.call(plugincontract.ToolPassage, in, &out); refusal != nil {
		if refusal.Code != plugincontract.CodeNotFound {
			s.t.Fatalf("passage refused with %s: %s", refusal.Code, refusal.Message)
		}
		return plugincontract.Passage{}, false
	}
	if err := out.Validate(in); err != nil {
		s.t.Fatalf("what passage gave back breaks the contract: %v", err)
	}
	return out.Passage, true
}

func (s *session) cancel(collection, source string) bool {
	s.t.Helper()
	var out plugincontract.CancelResult
	s.ok(plugincontract.ToolCancel, plugincontract.CancelInput{Collection: collection, Source: source}, &out)
	return out.Stopped
}

// file writes a file for a test and returns it as a Source, with the hash
// study would have taken of it.
func file(t *testing.T, id string, data []byte) plugincontract.Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".pdf")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return plugincontract.Source{ID: id, Title: "The " + id + " book", Kind: plugincontract.SourceFile, Path: path, Hash: hashOf(data)}
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pdf is a PDF of pages, each a list of paragraphs.
func pdf(pages ...[]string) []byte { return shelftest.PDF{Pages: pages}.Bytes() }

// long is a PDF that takes Shelf a while to index: many pages, each with a
// word of its own.
func long(pages int, word string) []byte {
	var book shelftest.PDF
	for n := range pages {
		book.Pages = append(book.Pages, []string{fmt.Sprintf("Page %d of a long book says %s%d and a little more, to give Shelf work.", n+1, word, n+1)})
	}
	return book.Bytes()
}

// testKey is the key the tests give Shelf for its endpoints. Nothing Shelf
// says, to study or on its standard error, may hold it.
const testKey = "sk-shelf-test-7d1e09bb3c"

// embedderAt returns what asks a test endpoint for vectors, set as the
// Recipe for EmbeddingGemma 2 sets a server that embeds a text as it is
// sent: Shelf writes the model's marks around each text, and keeps 512
// numbers of a vector.
func embedderAt(t *testing.T, at *shelftest.Endpoint, change ...func(*endpoint.Embedding)) *endpoint.Embedder {
	t.Helper()
	cfg := endpoint.Embedding{
		Address: at.URL, Key: testKey, Model: "test-embedder", Dimensions: 512,
		Query:    endpoint.Marking{Text: "task: search result | query: {text}"},
		Document: endpoint.Marking{Text: "title: {title} | text: {text}"},
		Agent:    "study-shelf/test",
	}
	for _, c := range change {
		c(&cfg)
	}
	e, err := endpoint.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// rerankerAt returns what asks a test endpoint to order Passages.
func rerankerAt(t *testing.T, at *shelftest.Endpoint, change ...func(*endpoint.Reranking)) *endpoint.Reranker {
	t.Helper()
	cfg := endpoint.Reranking{Address: at.URL, Key: testKey, Model: "test-reranker", Agent: "study-shelf/test"}
	for _, c := range change {
		c(&cfg)
	}
	r, err := endpoint.NewReranker(cfg)
	if err != nil {
		t.Fatalf("NewReranker: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

// found is what Shelf's search gives back: the contract's Passages and
// notice, and what Shelf says beside them of how it searched.
type found struct {
	plugincontract.SearchResult
	Search   []string `json:"search"`
	Reranked bool     `json:"reranked"`
}

// find searches, and returns all of Shelf's answer.
func (s *session) find(collection, query string, limit int, sources ...string) found {
	s.t.Helper()
	in := plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit, Sources: sources}
	var out found
	s.ok(plugincontract.ToolSearch, in, &out)
	if err := out.SearchResult.Validate(in); err != nil {
		s.t.Fatalf("what search gave back breaks the contract: %v", err)
	}
	if strings.Contains(out.Notice, testKey) {
		s.t.Errorf("the search's notice holds the key: %s", out.Notice)
	}
	return out
}

// texts are the texts of Passages, each on one line.
func texts(passages []plugincontract.ScoredPassage) []string {
	out := make([]string, len(passages))
	for i, p := range passages {
		out[i] = strings.Join(strings.Fields(p.Text), " ")
	}
	return out
}

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
