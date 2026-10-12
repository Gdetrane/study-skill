// Package shelf is Shelf, the Knowledge base plugin that ships with
// Lamplight (ADR-0012): an MCP server with the contract's six tools
// (docs/knowledge-base-contract.md), which indexes a Topic's PDFs and web
// pages and searches them by keyword.
//
// study links none of this. It starts the study-shelf program
// (cmd/study-shelf) and speaks the contract with it, as it does with any
// other plugin, and a test fails when cmd/study comes to import this
// package, SQLite or the PDF reader.
//
// What is where:
//
//   - shelf.go: the six tools, and what each says;
//   - work.go: the indexing of one Source, which goes on after index has
//     answered;
//   - index: the index, one SQLite file for each collection;
//   - pdf and web: the readers of the two kinds of Source;
//   - cut: text into Passages.
package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/web"
)

// Name is what Shelf calls itself to study.
const Name = "study-shelf"

// Version is Shelf's version. Release builds set it with -ldflags, to the
// version of the study it is released with.
var Version = ""

// A release's version, and the version Go makes up for a commit that is no
// release, which derives from the v1 skill's tags and would mislead.
var (
	releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	pseudoVersion  = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)
)

// VersionString is the version to show, decided as study decides its own:
// the release's, the module's for a tagged go install, and "dev" for
// anything else.
func VersionString() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; releaseVersion.MatchString(v) && !pseudoVersion.MatchString(v) {
			return v
		}
	}
	return "dev"
}

// Settings are what Shelf can be told about how it works. This version
// takes none from the learner: they are the defaults, and tests set their
// own. An embedding endpoint, a Recipe and a key will be read into them from
// a settings file of Shelf's own, never from the command line, which shows
// in the process list.
type Settings struct {
	// PassageSize is about how long a Passage is, in characters.
	PassageSize int
	// Fetch bounds the fetching of one web page.
	Fetch web.Limits
	// FileTime is how long reading one file may take.
	FileTime time.Duration
	// Workers is how many Sources are worked on at once. Two PDFs are never
	// read at once, whatever it says.
	Workers int
}

// DefaultSettings are the settings Shelf runs with.
func DefaultSettings() Settings {
	return Settings{PassageSize: cut.DefaultSize, Fetch: web.DefaultLimits, FileTime: 15 * time.Minute, Workers: 4}
}

// orDefault returns the settings with the default in the place of each that
// says nothing.
func (s Settings) orDefault() Settings {
	d := DefaultSettings()
	if s.PassageSize == 0 {
		s.PassageSize = d.PassageSize
	}
	if s.Fetch == (web.Limits{}) {
		s.Fetch = d.Fetch
	}
	if s.FileTime == 0 {
		s.FileTime = d.FileTime
	}
	if s.Workers < 1 {
		s.Workers = d.Workers
	}
	return s
}

// Options say where a Shelf keeps its index and how it works.
type Options struct {
	// Folder is the folder the index is kept in: what LAMPLIGHT_PLUGIN_DATA
	// names. It exists, and Shelf opens it once.
	Folder string
	// Settings are how Shelf works. Each that is left at zero is as in
	// DefaultSettings.
	Settings Settings
	// Crash is only for tests that simulate a crash, and must be nil
	// otherwise: see index.Options.
	Crash func(point string)
}

// Shelf is one running Shelf: one index, served to any number of MCP
// sessions.
type Shelf struct {
	settings Settings
	// folder is the index, and folderErr why its folder could not be
	// opened: Shelf then still says which contract it speaks, and every
	// other tool says what is wrong.
	folder    *index.Folder
	folderErr error
	fetcher   *web.Fetcher
	// slots are held by the Sources being worked on.
	slots chan struct{}

	// asking makes one request to index wait for the one before it.
	asking sync.Mutex

	mu sync.Mutex
	// jobs are the Sources being indexed now. A job is in it from the
	// moment index answers until what it came to is in the index.
	jobs map[key]*job
	// unwritten are the reasons of failures that could not be written to
	// the index, where every other failure is kept: the index itself was
	// what failed.
	unwritten map[key]string
	closing   bool
}

// key names a Source of a collection.
type key struct{ collection, source string }

// New returns a Shelf. It does not fail: a folder that cannot be opened is
// said by the tools, to whoever asks.
func New(opts Options) *Shelf {
	settings := opts.Settings.orDefault()
	s := &Shelf{
		settings:  settings,
		fetcher:   web.NewFetcher(settings.Fetch, Name+"/"+VersionString()+" (+https://github.com/mordor-forge/lamplight)"),
		slots:     make(chan struct{}, settings.Workers),
		jobs:      map[key]*job{},
		unwritten: map[key]string{},
	}
	s.folder, s.folderErr = index.Open(opts.Folder, index.Options{Crash: opts.Crash})
	return s
}

// Close stops whatever Shelf is indexing, and lets go of its folder. It
// does not wait long: study gives a plugin two seconds to exit once its
// input has closed, and an index that was being written is whole whether or
// not the writing ended (index.Collection.Replace).
func (s *Shelf) Close() {
	s.mu.Lock()
	s.closing = true
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		j.stop()
	}
	patience := time.After(time.Second)
	for _, j := range jobs {
		select {
		case <-j.done:
		case <-patience:
			// Something is still at work on a file. The program is about to
			// exit, which stops it, and closing the index under it would
			// only wait for it.
			return
		}
	}
	if s.folder != nil {
		_ = s.folder.Close()
	}
}

// Server returns Shelf as an MCP server, for one session. It sends the
// client no request of its own, a ping included: the first would end the
// session (the contract).
func (s *Shelf) Server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: strings.TrimPrefix(VersionString(), "v")}, nil)
	for _, tool := range []struct {
		name, description string
		handle            func(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody)
	}{
		{plugincontract.ToolContract, "The contract version Shelf speaks, and what it can do.", s.contract},
		{plugincontract.ToolIndex, "Index one Source of a collection: a PDF file or a web page. Answers at once; status follows the work.", s.index},
		{plugincontract.ToolStatus, "What is indexed, being indexed or failed in a collection.", s.status},
		{plugincontract.ToolCancel, "Stop indexing a Source. What was indexed before stays.", s.cancel},
		{plugincontract.ToolSearch, "The Passages of a collection that answer a query, best first, by keyword.", s.search},
		{plugincontract.ToolPassage, "One Passage of a collection, by its id.", s.passage},
	} {
		// The arguments are read by the contract's own rules (read), which
		// say what is wrong in the contract's words: the schema only says
		// that they are an object.
		server.AddTool(&mcp.Tool{Name: tool.name, Description: tool.description, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return answer(ctx, tool.handle, req.Params.Arguments), nil
			})
	}
	return server
}

// answer runs a tool and shapes its result as the contract says: one JSON
// object, as structured content and as text, with isError set for a
// refusal. A tool that panics has failed, and Shelf goes on.
func answer(ctx context.Context, handle func(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody), args json.RawMessage) (res *mcp.CallToolResult) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	out, refusal := func() (out any, refusal *plugincontract.ErrorBody) {
		defer func() {
			if p := recover(); p != nil {
				out, refusal = nil, refuse(plugincontract.CodeInternal, "Shelf failed in a way it should not: %v", p)
			}
		}()
		return handle(ctx, args)
	}()
	isError := refusal != nil
	if isError {
		refusal.Message = plugincontract.Cut(refusal.Message, plugincontract.MaxTextBytes)
		out = plugincontract.ErrorResult{Error: *refusal}
	}
	data, err := json.Marshal(out)
	if err != nil {
		data, _ = json.Marshal(plugincontract.ErrorResult{Error: plugincontract.ErrorBody{Code: plugincontract.CodeInternal, Message: "Shelf could not write its answer"}})
		isError = true
	}
	return &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: json.RawMessage(data)}
}

func refuse(code, format string, args ...any) *plugincontract.ErrorBody {
	return &plugincontract.ErrorBody{Code: code, Message: fmt.Sprintf(format, args...)}
}

// read reads a tool's arguments and holds them to the contract: every
// argument it requires is there, under exactly its name, with a value it
// allows. A field the contract does not name is passed over.
func read(args json.RawMessage, in interface{ Validate() error }) *plugincontract.ErrorBody {
	if err := plugincontract.Decode(args, in); err != nil {
		return refuse(plugincontract.CodeInvalidArgument, "%v", err)
	}
	if err := in.Validate(); err != nil {
		return refuse(plugincontract.CodeInvalidArgument, "%v", err)
	}
	return nil
}

// collection returns the index of a collection, and nil when nothing was
// ever indexed in it and create does not say to make it.
func (s *Shelf) collection(ctx context.Context, name string, create bool) (*index.Collection, error) {
	if s.folderErr != nil {
		return nil, s.folderErr
	}
	return s.folder.Collection(ctx, name, create)
}

// stored is what the index holds of a Source, and nil when it holds
// nothing.
func (s *Shelf) stored(ctx context.Context, k key) (*index.Source, error) {
	c, err := s.collection(ctx, k.collection, false)
	if err != nil || c == nil {
		return nil, err
	}
	return c.Source(ctx, k.source)
}

func (s *Shelf) contract(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody) {
	return plugincontract.Contract{
		Version:   plugincontract.Version,
		Sources:   []string{plugincontract.SourceFile, plugincontract.SourceURL},
		Search:    []string{plugincontract.SearchKeyword},
		Locations: []string{plugincontract.LocationPages},
	}, nil
}

func (s *Shelf) index(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.IndexInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
	}
	s.asking.Lock()
	defer s.asking.Unlock()
	k := key{in.Collection, in.Source.ID}
	have, err := s.stored(ctx, k)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, refuse(plugincontract.CodeInternal, "Shelf is stopping")
	}
	running := s.jobs[k]
	// The same content being indexed already: one piece of work is enough.
	// Work that was told to stop, and has not stopped yet, is no work on it.
	if running != nil && running.indexes(in.Source) && running.ctx.Err() == nil {
		s.mu.Unlock()
		status, _ := statusOf(have, running, "")
		return plugincontract.IndexResult{Source: status}, nil
	}
	s.mu.Unlock()
	if running != nil {
		// Other content: what was being indexed is stopped, and the Source
		// is as it was before that.
		running.stop()
		<-running.done
		if have, err = s.stored(ctx, k); err != nil {
			return nil, refuse(plugincontract.CodeInternal, "%v", err)
		}
	}
	// Content that is indexed already is left as it is, its Passages and
	// their ids with it. A web page is fetched again only when that is asked
	// for.
	if have != nil && have.Hash != "" && indexed(have, in.Source) {
		if have.Failed != "" {
			// What failed since was other content of the Source, which is
			// no longer asked for.
			c, err := s.collection(ctx, k.collection, false)
			if err == nil && c != nil {
				err = c.Recover(ctx, k.source)
			}
			if err != nil {
				return nil, refuse(plugincontract.CodeInternal, "%v", err)
			}
		}
		s.mu.Lock()
		delete(s.unwritten, k)
		s.mu.Unlock()
		return plugincontract.IndexResult{Source: plugincontract.SourceStatus{ID: k.source, State: plugincontract.StateIndexed, Hash: have.Hash}}, nil
	}

	work, stop := context.WithCancel(context.Background())
	j := &job{source: in.Source, ctx: work, stop: stop, done: make(chan struct{})}
	s.mu.Lock()
	if s.closing {
		// Close has stopped what was being indexed, and starts nothing.
		s.mu.Unlock()
		stop()
		return nil, refuse(plugincontract.CodeInternal, "Shelf is stopping")
	}
	delete(s.unwritten, k)
	s.jobs[k] = j
	s.mu.Unlock()
	go s.work(k, in, j)
	status, _ := statusOf(have, j, "")
	return plugincontract.IndexResult{Source: status}, nil
}

// indexed reports whether what the index holds for a Source is the content
// that is asked for.
func indexed(have *index.Source, asked plugincontract.Source) bool {
	if have.Kind != asked.Kind {
		return false
	}
	if asked.Kind == plugincontract.SourceURL {
		return have.Address == asked.URL && !asked.Refetch
	}
	return have.Hash == asked.Hash
}

// statusOf is what Shelf says of a Source now: have is what the index holds
// of it, running the work on it, and unwritten the reason of a failure the
// index could not be told. ok is false for a Source there is nothing to say
// about.
func statusOf(have *index.Source, running *job, unwritten string) (status plugincontract.SourceStatus, ok bool) {
	if have != nil {
		// The hash of what a search finds now: there while earlier content
		// is, whatever became of the content asked for since.
		status.ID, status.Hash = have.ID, have.Hash
	}
	switch {
	case running != nil:
		status.ID, status.State = running.source.ID, plugincontract.StateIndexing
		if progress, known := running.progress(); known {
			status.Progress = &progress
		}
	case unwritten != "":
		status.State, status.Reason = plugincontract.StateFailed, unwritten
	case have != nil && have.Failed != "":
		status.State, status.Reason = plugincontract.StateFailed, have.Failed
	case have != nil && have.Hash != "":
		status.State = plugincontract.StateIndexed
	default:
		return status, false
	}
	return status, true
}

func (s *Shelf) status(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.StatusInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
	}
	// What is being indexed is looked at before the index is read: a job
	// writes what it came to into the index before it leaves the list, so a
	// Source whose indexing ends meanwhile is said to be indexing, or is
	// found in the index, and is never left out.
	s.mu.Lock()
	running, unwritten := map[string]*job{}, map[string]string{}
	for k, j := range s.jobs {
		if k.collection == in.Collection {
			running[k.source] = j
		}
	}
	for k, reason := range s.unwritten {
		if k.collection == in.Collection {
			unwritten[k.source] = reason
		}
	}
	s.mu.Unlock()

	c, err := s.collection(ctx, in.Collection, false)
	if err != nil && len(running) == 0 && len(unwritten) == 0 {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	var sources []index.Source
	if err == nil && c != nil {
		if sources, err = c.Sources(ctx); err != nil {
			return nil, refuse(plugincontract.CodeInternal, "%v", err)
		}
	}
	out := plugincontract.StatusResult{Sources: []plugincontract.SourceStatus{}}
	add := func(have *index.Source, id string) {
		if status, ok := statusOf(have, running[id], unwritten[id]); ok {
			status.ID = id
			out.Sources = append(out.Sources, status)
		}
		delete(running, id)
		delete(unwritten, id)
	}
	for i := range sources {
		add(&sources[i], sources[i].ID)
	}
	// And the Sources the index holds nothing of yet.
	for _, id := range append(keys(running), keys(unwritten)...) {
		add(nil, id)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	if len(out.Sources) > plugincontract.MaxSources {
		out.Sources = out.Sources[:plugincontract.MaxSources]
	}
	return out, nil
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Shelf) cancel(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.CancelInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
	}
	s.mu.Lock()
	running := s.jobs[key{in.Collection, in.Source}]
	s.mu.Unlock()
	if running == nil {
		return plugincontract.CancelResult{}, nil
	}
	// The answer waits for the work to have stopped, so that nothing goes on
	// behind a command that has exited. It says that it stopped something
	// only when it did: work that had its content in the index, or had
	// failed, before it was told to stop was not stopped.
	running.stop()
	<-running.done
	return plugincontract.CancelResult{Stopped: running.stopped}, nil
}

func (s *Shelf) search(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.SearchInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
	}
	out := plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{}}
	c, err := s.collection(ctx, in.Collection, false)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	if c == nil {
		return out, nil
	}
	found, err := c.Search(ctx, in.Query, in.Limit, in.Sources)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	for _, f := range found {
		out.Passages = append(out.Passages, plugincontract.ScoredPassage{Passage: passageOf(f.Passage), Score: f.Score})
	}
	return out, nil
}

func (s *Shelf) passage(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.PassageInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
	}
	c, err := s.collection(ctx, in.Collection, false)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	var found *index.Passage
	if c != nil {
		if found, err = c.Passage(ctx, in.ID); err != nil {
			return nil, refuse(plugincontract.CodeInternal, "%v", err)
		}
	}
	if found == nil {
		return nil, refuse(plugincontract.CodeNotFound, "collection %s has no Passage %s", in.Collection, in.ID)
	}
	return plugincontract.PassageResult{Passage: passageOf(*found)}, nil
}

// passageOf is a Passage of the index as the contract shapes it. A Passage
// of a web page has no location.
func passageOf(p index.Passage) plugincontract.Passage {
	out := plugincontract.Passage{ID: p.ID, Source: p.Source, Text: p.Text}
	if p.First > 0 {
		out.Location = &plugincontract.Location{Kind: plugincontract.LocationPages, First: p.First, Last: p.Last}
	}
	return out
}

// errStopped is why work ends that was told to stop.
var errStopped = errors.New("the indexing was stopped")
