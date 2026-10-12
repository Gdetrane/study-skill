// Package shelf is Shelf, the Knowledge base plugin that ships with
// Lamplight (ADR-0012): an MCP server with the contract's six tools
// (docs/knowledge-base-contract.md), which indexes a Topic's PDFs and web
// pages and searches them: by keyword always, and by what a question means
// when the learner gave it an embedding endpoint.
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
//   - search.go: one search, by keyword, by vectors and with a reranker;
//   - index: the index, one SQLite file for each collection;
//   - pdf and web: the readers of the two kinds of Source;
//   - cut: text into Passages;
//   - endpoint: the embedding and the reranking endpoint, as Shelf asks
//     them;
//   - settings: Shelf's settings file, and the Recipes.
package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
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

// Settings are what Shelf can be told about how it works. The learner's
// own, an embedding endpoint, a Recipe, a key, a reranking endpoint, are
// read from a settings file of Shelf's (package settings), never from the
// command line, which shows in the process list, nor from the environment.
// Tests set their own.
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
	// Embedder asks the embedding endpoint the learner gave Shelf. With
	// one, a Source is indexed with a vector for each of its Passages, and
	// a search is by what the question means as well as by its words. nil
	// is no endpoint: Shelf searches by keyword alone.
	Embedder *endpoint.Embedder
	// Reranker asks the reranking endpoint, which orders the best Passages
	// of a search before they are returned. nil is none.
	Reranker *endpoint.Reranker
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
	// Each limit by itself: whether pages on this computer are fetched is
	// said apart from how long and how large a page may be.
	if s.Fetch.Timeout == 0 {
		s.Fetch.Timeout = d.Fetch.Timeout
	}
	if s.Fetch.MaxBytes == 0 {
		s.Fetch.MaxBytes = d.Fetch.MaxBytes
	}
	if s.Fetch.MaxRedirects == 0 {
		s.Fetch.MaxRedirects = d.Fetch.MaxRedirects
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
	// Crash is only for tests, and must be nil otherwise. It is called at
	// each point of a write of the index where a crash could happen (see
	// index.Options), and at PointPagesRead.
	Crash func(point string)
}

// PointPagesRead is the point of a file's indexing where its pages are read
// and its hash is about to be taken again: where a test changes the file, to
// see that Shelf notices.
const PointPagesRead = "the file's pages are read"

// Shelf is one running Shelf: one index, served to any number of MCP
// sessions.
type Shelf struct {
	settings Settings
	// at is Options.Crash, or nothing.
	at func(point string)
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
		at:        opts.Crash,
	}
	if s.at == nil {
		s.at = func(string) {}
	}
	s.folder, s.folderErr = index.Open(opts.Folder, index.Options{Crash: opts.Crash})
	return s
}

// StopsWithin is how long Close waits for what Shelf is indexing to have
// stopped. study gives a plugin a quarter of a second to exit once its
// input has closed, after a glance at what is indexed, and two seconds
// otherwise.
const StopsWithin = 150 * time.Millisecond

// Close stops whatever Shelf is indexing, and lets go of its folder. It
// does not wait long (StopsWithin): an index that was being written is whole
// whether or not the writing ended (index.Collection.Replace), and work that
// was cut short is known for that by the lock it no longer holds.
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
	patience := time.After(StopsWithin)
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
	if s.settings.Embedder != nil {
		s.settings.Embedder.Close()
	}
	if s.settings.Reranker != nil {
		s.settings.Reranker.Close()
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
		{plugincontract.ToolSearch, "The Passages of a collection that answer a query, best first: by keyword, and by what the query means when Shelf has an embedding endpoint.", s.search},
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

// contract says which ways Shelf has of searching now: by keyword always,
// and by embeddings when the settings name an endpoint. It reads the
// settings and asks the endpoint nothing: whether the endpoint answers is
// said by the search that needs it.
func (s *Shelf) contract(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody) {
	search := []string{plugincontract.SearchKeyword}
	if s.settings.Embedder != nil {
		search = append(search, plugincontract.SearchEmbedding)
	}
	return plugincontract.Contract{
		Version:   plugincontract.Version,
		Sources:   []string{plugincontract.SourceFile, plugincontract.SourceURL},
		Search:    search,
		Locations: []string{plugincontract.LocationPages},
	}, nil
}

// fits reports whether vectors made as have says are ones a question's
// vector is compared with, as the settings are now: of the model they name,
// with a document marked as they say, and of the size they ask for, when
// they ask for one.
func (s *Shelf) fits(have index.Made) bool {
	e := s.settings.Embedder
	return e != nil && have.Any() && have.Model == e.Model() && have.Document == e.Document() &&
		(e.Dimensions() == 0 || have.Dimensions == e.Dimensions())
}

// behind says why a Source that is indexed is not found by what a question
// means, though Shelf has an embedding endpoint: its Passages have no
// vectors, or vectors of another making. It is empty for a Source there is
// nothing of the kind to say of. Indexing the Source again puts it right,
// which gives its Passages their vectors and leaves them as they are.
//
// The contract has three states, and this is `failed` with the hash of
// what is still found: the one state whose reason study shows the learner,
// and the one study has indexed again by study source index. What failed
// is not the last indexing but the index, against the settings as they are
// now.
func (s *Shelf) behind(have *index.Source) string {
	e := s.settings.Embedder
	if e == nil || have == nil || have.Hash == "" || s.fits(have.Made) {
		return ""
	}
	const found, again = "it is found by keyword alone: ", ". Index it again to have its Passages embedded"
	now := "the model " + short(e.Model())
	switch made := have.Made; {
	case !made.Any():
		return found + "it was indexed without vectors, and Shelf now has an embedding endpoint, for " + now + again
	case made.Model != e.Model():
		return found + "its vectors are of the model " + short(made.Model) + ", and Shelf's settings now name " + now + again
	case made.Document != e.Document():
		return found + "its Passages were marked for " + now + " in another way than Shelf's settings now say" + again
	default:
		return found + fmt.Sprintf("its vectors have %d numbers, and Shelf's settings now ask for %d", made.Dimensions, e.Dimensions()) + again
	}
}

// short is a name from the settings, or from the index, short enough for a
// reason that study shows in 300 bytes.
func short(name string) string {
	const most = 60
	if len(name) > most {
		name = plugincontract.Cut(name, most) + "…"
	}
	return name
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
		status, _ := statusOf(have, working{job: running}, "", "")
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
	//
	// One thing can still be to do for it: its Passages have no vectors, or
	// vectors Shelf no longer compares a question's with, since the learner
	// gave Shelf an endpoint or another model. status says so of the Source,
	// and asking for it again is what puts it right: the Passages that are
	// there are given vectors, and the Source is read no second time.
	vectorsOnly := have != nil && have.Hash != "" && indexed(have, in.Source)
	if vectorsOnly && s.behind(have) == "" {
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
	j := &job{source: in.Source, vectorsOnly: vectorsOnly, ctx: work, stop: stop, done: make(chan struct{})}
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
	status, _ := statusOf(have, working{job: j}, "", "")
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

// working is the indexing of a Source that is going on now: this Shelf's
// own job, or the work of another copy of Shelf, with how far that copy
// says it is.
type working struct {
	job       *job
	elsewhere bool
	progress  *float64
}

// statusOf is what Shelf says of a Source now: have is what the index holds
// of it, at the work on it, unwritten the reason of a failure the index
// could not be told, and behind why its Passages are not found by what a
// question means when they should be (Shelf.behind). ok is false for a
// Source there is nothing to say about.
func statusOf(have *index.Source, at working, unwritten, behind string) (status plugincontract.SourceStatus, ok bool) {
	if have != nil {
		// The hash of what a search finds now: there while earlier content
		// is, whatever became of the content asked for since.
		status.ID, status.Hash = have.ID, have.Hash
	}
	switch {
	case at.job != nil:
		status.ID, status.State = at.job.source.ID, plugincontract.StateIndexing
		if progress, known := at.job.progress(); known {
			status.Progress = &progress
		}
	case at.elsewhere:
		// "Indexing" is Shelf working on the Source now, whichever copy of
		// it: the one study asks is seldom the one that indexes.
		status.State, status.Progress = plugincontract.StateIndexing, at.progress
	case unwritten != "":
		status.State, status.Reason = plugincontract.StateFailed, unwritten
	case have != nil && have.Failed != "":
		status.State, status.Reason = plugincontract.StateFailed, have.Failed
	case have != nil && have.Hash != "" && behind != "":
		// Found, by its words, and to be indexed again: the hash is there,
		// as it is for every Source whose earlier content is still found.
		status.State, status.Reason = plugincontract.StateFailed, behind
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
	// Then what other copies of Shelf are indexing, and the index last, for
	// the same reason: their work ends in the transaction that puts its
	// content, or its failure, in the index.
	var elsewhere map[string]*float64
	var sources []index.Source
	if err == nil && c != nil {
		if elsewhere, err = c.Working(ctx); err == nil {
			sources, err = c.Sources(ctx)
		}
		if err != nil {
			return nil, refuse(plugincontract.CodeInternal, "%v", err)
		}
	}
	out := plugincontract.StatusResult{Sources: []plugincontract.SourceStatus{}}
	add := func(have *index.Source, id string) {
		progress, there := elsewhere[id]
		if status, ok := statusOf(have, working{job: running[id], elsewhere: there, progress: progress}, unwritten[id], s.behind(have)); ok {
			status.ID = id
			out.Sources = append(out.Sources, status)
		}
		delete(running, id)
		delete(unwritten, id)
		delete(elsewhere, id)
	}
	for i := range sources {
		add(&sources[i], sources[i].ID)
	}
	// And the Sources the index holds nothing of yet.
	for _, id := range slices.Concat(keys(running), keys(unwritten), keys(elsewhere)) {
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
	out := searchResult{SearchResult: plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{}}, Search: []string{plugincontract.SearchKeyword}}
	c, err := s.collection(ctx, in.Collection, false)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	if c == nil {
		return out, nil
	}
	got, err := s.find(ctx, c, in)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "%v", err)
	}
	for _, f := range got.found {
		out.Passages = append(out.Passages, plugincontract.ScoredPassage{Passage: passageOf(f.Passage), Score: f.Score})
	}
	out.Search, out.Reranked = got.kinds, got.reranked
	out.Notice = plugincontract.Cut(strings.Join(got.notices, " "), plugincontract.MaxTextBytes)
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
