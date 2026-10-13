// Package plugintest is what tests of Knowledge base plugins share: a small
// plugin that speaks Lamplight's contract (ADR-0012), which is the one
// program those tests start, and a conformance suite that checks any plugin
// against what the contract promises.
//
// The test plugin indexes plain text and web pages, keeps its index in a
// folder as JSON, and searches by counting the words of a query. It is no
// retrieval worth using. It is there so that study's client, the command
// line and the conformance suite have a plugin to talk to that needs
// nothing installed, and it can be told to misbehave (see Main).
//
// Nothing but tests imports this package.
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
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// The rules of the contract a test plugin can be told to break, each in one
// way, for the conformance suite to catch (Config.Breaks, and --break).
const (
	// BreakOverLimit gives every Passage a search found, whatever its limit.
	BreakOverLimit = "over-limit"
	// BreakStatusWaits and BreakSearchWaits answer only once nothing is
	// being indexed in the collection.
	BreakStatusWaits = "status-waits"
	BreakSearchWaits = "search-waits"
	// BreakCancelDropsEarlier forgets what was indexed for a Source when
	// the indexing of new content for it is stopped.
	BreakCancelDropsEarlier = "cancel-drops-earlier"
	// BreakNoEarlierHash leaves the hash out for a Source that is being
	// indexed, or failed, while its earlier content is still found.
	BreakNoEarlierHash = "no-earlier-hash"
	// BreakSearchReadsSource finds nothing in a Source whose file is no
	// longer there, as a plugin that reads the file to search it would.
	BreakSearchReadsSource = "search-reads-source"
	// BreakFailedForgotten says once that a Source failed, and then no more.
	BreakFailedForgotten = "failed-forgotten"
	// BreakIndexesWhatItReads indexes a file whatever its bytes are, also
	// when they are not the hash index was handed.
	BreakIndexesWhatItReads = "indexes-what-it-reads"
	// BreakFailedLost keeps what failed only for as long as its program
	// runs: started again, it says nothing of a Source that failed.
	BreakFailedLost = "failed-lost"
	// BreakFollowsLinks keeps the index by the folder's path, through
	// whatever symbolic link is in it.
	BreakFollowsLinks = "follows-links"
	// BreakStrict refuses arguments that hold a field the contract does not
	// name.
	BreakStrict = "strict"
	// BreakAnyCase takes an argument under its name in any letter case, as
	// a JSON library that matches names loosely does.
	BreakAnyCase = "any-case"
	// BreakMindsStrays writes nothing into a folder that holds a name it
	// did not make, such as the file study leaves there for a moment to
	// learn whether the folder can be written in.
	BreakMindsStrays = "minds-strays"
)

// Config says how a test plugin behaves.
type Config struct {
	// Folder is where the index is kept: what LAMPLIGHT_PLUGIN_DATA names
	// when the plugin is a command.
	Folder string
	// Sources are the kinds of Source it indexes. Nil is files and web
	// pages.
	Sources []string
	// PageDelay is how long it takes over each page, to make indexing slow
	// enough to watch and to stop.
	PageDelay time.Duration
	// Name and Version are what it calls itself. Empty is "test-plugin" and
	// "1.0.0".
	Name, Version string
	// Tools are the tools it has. Nil is the contract's seven.
	Tools []string
	// Findings are what its diagnose tool gives back: the test plugin
	// depends on nothing and tests nothing, so it says what it is told to.
	// Nil is no finding.
	Findings []plugincontract.Finding
	// Results replaces what a tool gives back with a JSON object, given as
	// text, whatever it was asked: for testing what study makes of an answer
	// that is not the contract's.
	Results map[string]string
	// Refusals makes a tool refuse every call, with "code:message".
	Refusals map[string]string
	// PlainErrors makes a tool fail every call with this text and no JSON,
	// as a library does for an error the plugin did not shape.
	PlainErrors map[string]string
	// TextOnly gives results as text alone, without structured content.
	TextOnly bool
	// Breaks are the rules of the contract it breaks: the Break constants.
	Breaks []string
	// Asked, when set, is told each call as it arrives, before anything
	// else is done with it: the tool's name and what it is asked. It is for
	// a test to see what a plugin was asked, and can be called from several
	// goroutines.
	Asked func(tool string, args json.RawMessage)
	// Before, when set, is called with a tool's name before the tool
	// answers: for a test to hold a call up.
	Before func(ctx context.Context, tool string)
	// Twist, when set, is given each answer on its way out, and what it
	// returns is sent in its place: for a plugin that breaks the contract in
	// one way and keeps the rest of it.
	Twist func(tool string, answer []byte, refused bool) []byte
}

// Plugin is a test plugin: one index, served to any number of MCP sessions.
type Plugin struct {
	cfg Config

	// root is the plugin's folder, opened once: everything the plugin reads
	// and writes of its index goes through it, so nothing in the folder,
	// whatever it is exchanged for while the plugin runs, leads the plugin
	// out of it. rootErr is why it could not be opened.
	root    *os.Root
	rootErr error

	// asking makes one request to index wait for the one before it.
	asking sync.Mutex

	mu sync.Mutex
	// jobs are the Sources being indexed now, and failed the reasons of the
	// ones whose last indexing failed, both by collection and Source. A
	// failure is in the folder too (fail), where the next start of the plugin
	// finds it.
	jobs   map[[2]string]*job
	failed map[[2]string]string
}

// job is one Source being indexed.
type job struct {
	hash     string // of the content asked for, when it is a file
	stop     context.CancelFunc
	done     chan struct{}
	progress float64
}

// stored is one Source in the index, as its file holds it.
type stored struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Hash   string `json:"hash"`
	// Path is where a file Source was read from. Nothing reads it there
	// again but a plugin told to break that rule.
	Path     string          `json:"path,omitempty"`
	Passages []storedPassage `json:"passages"`
}

type storedPassage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Page is the page the text is on, from 1, or 0 for a web page.
	Page int `json:"page,omitempty"`
}

// New returns a test plugin.
func New(cfg Config) *Plugin {
	if cfg.Sources == nil {
		cfg.Sources = []string{plugincontract.SourceFile, plugincontract.SourceURL}
	}
	if cfg.Name == "" {
		cfg.Name = "test-plugin"
	}
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}
	if cfg.Tools == nil {
		cfg.Tools = plugincontract.Tools
	}
	p := &Plugin{cfg: cfg, jobs: map[[2]string]*job{}, failed: map[[2]string]string{}}
	p.root, p.rootErr = os.OpenRoot(cfg.Folder)
	return p
}

// breaks reports whether the plugin was told to break a rule.
func (p *Plugin) breaks(rule string) bool { return slices.Contains(p.cfg.Breaks, rule) }

// Close stops whatever the plugin is indexing, and lets go of its folder.
func (p *Plugin) Close() {
	p.mu.Lock()
	jobs := make([]*job, 0, len(p.jobs))
	for _, j := range p.jobs {
		jobs = append(jobs, j)
	}
	p.mu.Unlock()
	for _, j := range jobs {
		j.stop()
		<-j.done
	}
	if p.root != nil {
		_ = p.root.Close()
	}
}

// Handler serves the plugin over streamable HTTP, as a service the learner
// runs would: every session reaches the same index.
func (p *Plugin) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return p.Server() }, nil)
}

// Server returns the plugin as an MCP server, for one session.
func (p *Plugin) Server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: p.cfg.Name, Version: p.cfg.Version}, nil)
	for _, tool := range []struct {
		name, description string
		handle            func(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody)
	}{
		{plugincontract.ToolContract, "The contract version this plugin speaks, and what it can do.", p.contract},
		{plugincontract.ToolIndex, "Index one Source of a collection. Answers at once; status follows the work.", p.index},
		{plugincontract.ToolStatus, "What is indexed, being indexed or failed in a collection.", p.status},
		{plugincontract.ToolCancel, "Stop indexing a Source.", p.cancel},
		{plugincontract.ToolSearch, "The Passages of a collection that answer a query, best first.", p.search},
		{plugincontract.ToolPassage, "One Passage of a collection, by its id.", p.passage},
		{plugincontract.ToolDiagnose, "Test what this plugin depends on, and say what was found.", p.diagnose},
	} {
		if !slices.Contains(p.cfg.Tools, tool.name) {
			continue
		}
		server.AddTool(&mcp.Tool{Name: tool.name, Description: tool.description, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if p.cfg.Asked != nil {
					p.cfg.Asked(tool.name, req.Params.Arguments)
				}
				if p.cfg.Before != nil {
					p.cfg.Before(ctx, tool.name)
				}
				return p.answer(ctx, tool.name, tool.handle, req.Params.Arguments), nil
			})
	}
	return server
}

// answer runs a tool and shapes its result as the contract says: one JSON
// object, as structured content and as text, with isError set for a
// refusal.
func (p *Plugin) answer(ctx context.Context, tool string, handle func(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody), args json.RawMessage) *mcp.CallToolResult {
	if text, ok := p.cfg.PlainErrors[tool]; ok {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	}
	var data []byte
	isError := false
	if refusal, ok := p.cfg.Refusals[tool]; ok {
		code, message, _ := strings.Cut(refusal, ":")
		data, _ = json.Marshal(plugincontract.ErrorResult{Error: plugincontract.ErrorBody{Code: code, Message: message}})
		isError = true
	} else if result, ok := p.cfg.Results[tool]; ok {
		data = []byte(result)
	} else {
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		out, refusal := handle(ctx, args)
		if refusal != nil {
			out, isError = plugincontract.ErrorResult{Error: *refusal}, true
		}
		data, _ = json.Marshal(out)
	}
	if p.cfg.Twist != nil {
		data = p.cfg.Twist(tool, data, isError)
	}
	res := &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
	if !p.cfg.TextOnly {
		res.StructuredContent = json.RawMessage(data)
	}
	return res
}

func refuse(code, format string, args ...any) *plugincontract.ErrorBody {
	return &plugincontract.ErrorBody{Code: code, Message: fmt.Sprintf(format, args...)}
}

// read reads a tool's input and checks it against the contract: every
// argument it requires is there, under exactly its name. A field the
// contract does not name is passed over.
func (p *Plugin) read(args json.RawMessage, in interface{ Validate() error }) *plugincontract.ErrorBody {
	err := plugincontract.Decode(args, in)
	if p.breaks(BreakAnyCase) {
		err = json.Unmarshal(args, in)
	}
	if err != nil {
		return refuse(plugincontract.CodeInvalidArgument, "%v", err)
	}
	if p.breaks(BreakStrict) {
		strict := json.NewDecoder(bytes.NewReader(args))
		strict.DisallowUnknownFields()
		if err := strict.Decode(in); err != nil {
			return refuse(plugincontract.CodeInvalidArgument, "%v", err)
		}
	}
	if err := in.Validate(); err != nil {
		return refuse(plugincontract.CodeInvalidArgument, "%v", err)
	}
	return nil
}

func (p *Plugin) contract(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody) {
	return plugincontract.Contract{
		Version:   plugincontract.Version,
		Sources:   p.cfg.Sources,
		Search:    []string{plugincontract.SearchKeyword},
		Locations: []string{plugincontract.LocationPages},
	}, nil
}

// diagnose gives the findings the plugin was told to give. It takes no
// arguments, and passes over whatever it is given.
func (p *Plugin) diagnose(context.Context, json.RawMessage) (any, *plugincontract.ErrorBody) {
	findings := p.cfg.Findings
	if findings == nil {
		findings = []plugincontract.Finding{}
	}
	return plugincontract.DiagnoseResult{Findings: findings}, nil
}

func (p *Plugin) index(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.IndexInput
	if refusal := p.read(args, &in); refusal != nil {
		return nil, refusal
	}
	if !slices.Contains(p.cfg.Sources, in.Source.Kind) {
		return nil, refuse(plugincontract.CodeUnsupported, "this plugin does not index a Source of the kind %s", in.Source.Kind)
	}
	p.asking.Lock()
	defer p.asking.Unlock()
	key := [2]string{in.Collection, in.Source.ID}
	have, _ := p.load(in.Collection, in.Source.ID)

	p.mu.Lock()
	running := p.jobs[key]
	// The same content being indexed already: one piece of work is enough.
	if running != nil && in.Source.Kind == plugincontract.SourceFile && running.hash == in.Source.Hash {
		status := p.statusOf(key, have)
		p.mu.Unlock()
		return plugincontract.IndexResult{Source: status}, nil
	}
	p.mu.Unlock()
	if running != nil {
		running.stop()
		<-running.done
		have, _ = p.load(in.Collection, in.Source.ID)
	}
	// Content that is indexed already is left as it is. A web page is
	// fetched again only when that is asked for.
	if have != nil && have.Kind == in.Source.Kind &&
		(in.Source.Kind == plugincontract.SourceFile && have.Hash == in.Source.Hash ||
			in.Source.Kind == plugincontract.SourceURL && !in.Source.Refetch) {
		p.mu.Lock()
		p.forget(key)
		p.mu.Unlock()
		return plugincontract.IndexResult{Source: plugincontract.SourceStatus{ID: have.ID, State: plugincontract.StateIndexed, Hash: have.Hash}}, nil
	}

	ctx, stop := context.WithCancel(context.Background())
	j := &job{hash: in.Source.Hash, stop: stop, done: make(chan struct{})}
	p.mu.Lock()
	p.forget(key)
	p.jobs[key] = j
	status := p.statusOf(key, have)
	p.mu.Unlock()
	go func() {
		defer close(j.done)
		err := p.work(ctx, in, j)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.jobs[key] == j {
			delete(p.jobs, key)
		}
		if err != nil && ctx.Err() == nil {
			p.fail(key, err.Error())
		}
	}()
	return plugincontract.IndexResult{Source: status}, nil
}

// work indexes one Source: it reads the content, cuts it into Passages page
// by page, and replaces what the index held for the Source, whole, once it
// has all of it. Stopped halfway, it leaves the index as it was.
func (p *Plugin) work(ctx context.Context, in plugincontract.IndexInput, j *job) error {
	var content []byte
	var err error
	if in.Source.Kind == plugincontract.SourceFile {
		content, err = os.ReadFile(in.Source.Path)
		if err != nil {
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return fmt.Errorf("the file cannot be read: %v", pathErr.Err)
			}
			return err
		}
	} else if content, err = fetch(ctx, in.Source.URL); err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	entry := stored{Format: 1, ID: in.Source.ID, Title: in.Source.Title, Kind: in.Source.Kind, Hash: "sha256:" + hex.EncodeToString(sum[:]),
		Path: in.Source.Path}
	// A file is the one study read only when its bytes are the hash study
	// handed over. Of another, nothing is kept.
	if in.Source.Kind == plugincontract.SourceFile && entry.Hash != in.Source.Hash && !p.breaks(BreakIndexesWhatItReads) {
		return errors.New("the file changed since study read it")
	}
	pages := pagesOf(in.Source.Kind, string(content))
	if len(pages) == 0 {
		return errors.New("there is no text in it")
	}
	for n, page := range pages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.cfg.PageDelay):
		}
		for _, paragraph := range page {
			id := sha256.Sum256([]byte(entry.ID + "\x00" + entry.Hash + "\x00" + strconv.Itoa(len(entry.Passages))))
			passage := storedPassage{ID: hex.EncodeToString(id[:12]), Text: paragraph}
			if in.Source.Kind == plugincontract.SourceFile {
				passage.Page = n + 1
			}
			entry.Passages = append(entry.Passages, passage)
		}
		p.mu.Lock()
		j.progress = float64(n+1) / float64(len(pages))
		p.mu.Unlock()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return p.save(in.Collection, entry)
}

// fetch reads a web page.
func fetch(ctx context.Context, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("the address cannot be fetched")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("the page could not be fetched")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the page could not be fetched: the server answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

var (
	paragraphTag = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
	anyTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	blankLine    = regexp.MustCompile(`\n[ \t]*\n`)
)

// pagesOf cuts a Source's content into pages, and each page into the
// paragraphs that become its Passages. A file's pages are divided by form
// feeds, as pdftotext divides them; a web page is one page, of its <p>
// elements when it has any.
func pagesOf(kind, content string) [][]string {
	var pages [][]string
	if kind == plugincontract.SourceURL {
		var paragraphs []string
		for _, m := range paragraphTag.FindAllStringSubmatch(content, -1) {
			if text := strings.Join(strings.Fields(html.UnescapeString(anyTag.ReplaceAllString(m[1], " "))), " "); text != "" {
				paragraphs = append(paragraphs, text)
			}
		}
		if len(paragraphs) == 0 {
			paragraphs = paragraphsOf(content)
		}
		if len(paragraphs) > 0 {
			pages = append(pages, paragraphs)
		}
		return pages
	}
	for _, page := range strings.Split(content, "\f") {
		pages = append(pages, paragraphsOf(page))
	}
	if !slices.ContainsFunc(pages, func(page []string) bool { return len(page) > 0 }) {
		return nil
	}
	return pages
}

func paragraphsOf(text string) []string {
	var out []string
	for _, paragraph := range blankLine.Split(strings.ReplaceAll(text, "\r\n", "\n"), -1) {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			out = append(out, plugincontract.Cut(paragraph, plugincontract.MaxPassageBytes))
		}
	}
	return out
}

// statusOf is what the plugin says of a Source now. have is what the index
// holds for it, if anything. p.mu is held.
func (p *Plugin) statusOf(key [2]string, have *stored) plugincontract.SourceStatus {
	status := plugincontract.SourceStatus{ID: key[1]}
	if have != nil {
		status.Hash = have.Hash
	}
	reason := p.failure(key)
	switch {
	case p.jobs[key] != nil:
		progress := p.jobs[key].progress
		status.State, status.Progress = plugincontract.StateIndexing, &progress
	case reason != "":
		status.State, status.Reason = plugincontract.StateFailed, reason
	default:
		status.State = plugincontract.StateIndexed
	}
	if status.State != plugincontract.StateIndexed && p.breaks(BreakNoEarlierHash) {
		status.Hash = ""
	}
	return status
}

// quiet waits until nothing is being indexed in a collection: what a
// plugin told to wait for indexing does before it answers.
func (p *Plugin) quiet(ctx context.Context, collection string) {
	for {
		var running *job
		p.mu.Lock()
		for key, j := range p.jobs {
			if key[0] == collection {
				running = j
			}
		}
		p.mu.Unlock()
		if running == nil {
			return
		}
		select {
		case <-running.done:
		case <-ctx.Done():
			return
		}
	}
}

func (p *Plugin) status(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.StatusInput
	if refusal := p.read(args, &in); refusal != nil {
		return nil, refusal
	}
	if p.breaks(BreakStatusWaits) {
		p.quiet(ctx, in.Collection)
	}
	// Read under the lock: a Source whose indexing ends meanwhile is then
	// still listed as indexing, never left out.
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := p.loadAll(in.Collection)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "reading the index: %v", err)
	}
	byID := map[string]*stored{}
	for i := range entries {
		byID[entries[i].ID] = &entries[i]
	}
	for key := range p.jobs {
		if _, known := byID[key[1]]; key[0] == in.Collection && !known {
			byID[key[1]] = nil
		}
	}
	for _, id := range p.failures(in.Collection) {
		if _, known := byID[id]; !known {
			byID[id] = nil
		}
	}
	out := plugincontract.StatusResult{Sources: []plugincontract.SourceStatus{}}
	for id, have := range byID {
		key := [2]string{in.Collection, id}
		out.Sources = append(out.Sources, p.statusOf(key, have))
		if p.breaks(BreakFailedForgotten) {
			p.forget(key)
		}
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	return out, nil
}

func (p *Plugin) cancel(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.CancelInput
	if refusal := p.read(args, &in); refusal != nil {
		return nil, refusal
	}
	p.mu.Lock()
	running := p.jobs[[2]string{in.Collection, in.Source}]
	p.mu.Unlock()
	if running == nil {
		return plugincontract.CancelResult{}, nil
	}
	// The answer waits for the work to have stopped, so that nothing goes on
	// behind a command that has exited.
	running.stop()
	<-running.done
	if p.breaks(BreakCancelDropsEarlier) && p.root != nil {
		_ = p.root.Remove(file(in.Collection, in.Source))
	}
	return plugincontract.CancelResult{Stopped: true}, nil
}

func (p *Plugin) search(ctx context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.SearchInput
	if refusal := p.read(args, &in); refusal != nil {
		return nil, refusal
	}
	if p.breaks(BreakSearchWaits) {
		p.quiet(ctx, in.Collection)
	}
	entries, err := p.loadAll(in.Collection)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "reading the index: %v", err)
	}
	query := words(in.Query)
	out := plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{}}
	for _, entry := range entries {
		if in.Sources != nil && !slices.Contains(in.Sources, entry.ID) {
			continue
		}
		if p.breaks(BreakSearchReadsSource) && entry.Kind == plugincontract.SourceFile {
			if _, err := os.Stat(entry.Path); err != nil {
				continue
			}
		}
		for _, passage := range entry.Passages {
			score := 0
			for _, word := range words(passage.Text) {
				if slices.Contains(query, word) {
					score++
				}
			}
			if score > 0 {
				out.Passages = append(out.Passages, plugincontract.ScoredPassage{Passage: passage.of(entry), Score: float64(score)})
			}
		}
	}
	// The best first, and always in the same order.
	sort.SliceStable(out.Passages, func(i, j int) bool { return out.Passages[i].Score > out.Passages[j].Score })
	if len(out.Passages) > in.Limit && !p.breaks(BreakOverLimit) {
		out.Passages = out.Passages[:in.Limit]
	}
	return out, nil
}

func (p *Plugin) passage(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.PassageInput
	if refusal := p.read(args, &in); refusal != nil {
		return nil, refusal
	}
	entries, err := p.loadAll(in.Collection)
	if err != nil {
		return nil, refuse(plugincontract.CodeInternal, "reading the index: %v", err)
	}
	for _, entry := range entries {
		for _, passage := range entry.Passages {
			if passage.ID == in.ID {
				return plugincontract.PassageResult{Passage: passage.of(entry)}, nil
			}
		}
	}
	return nil, refuse(plugincontract.CodeNotFound, "collection %s has no Passage %s", in.Collection, in.ID)
}

// of is the Passage as the contract shapes it.
func (s storedPassage) of(entry stored) plugincontract.Passage {
	passage := plugincontract.Passage{ID: s.ID, Source: entry.ID, Text: s.Text}
	if s.Page > 0 {
		passage.Location = &plugincontract.Location{Kind: plugincontract.LocationPages, First: s.Page, Last: s.Page}
	}
	return passage
}

// words are the words of a text, in lower case.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// The index is one folder for each collection, with one file for each
// Source in it. A Source's file is replaced whole, so another copy of the
// plugin that reads the same folder sees the Source as it was or as it is.
//
// Every name below is taken from the plugin's folder as it was opened when
// the plugin started (os.Root), never from the folder's path again: a
// symbolic link in the folder that leads out of it is not followed, and the
// call that meets one fails. The contract asks that of every plugin.

// file is the name of a Source's file in the plugin's folder. A collection's
// name and a Source's id are each safe as a file's name: the contract says
// so, and read checked it.
func file(collection, source string) string { return collection + "/" + source + ".json" }

// folder returns the folder the index is in, as the plugin holds it open.
// For a plugin told to follow links it returns nil, and the folder's path
// is used instead.
func (p *Plugin) folder() (*os.Root, error) {
	if p.rootErr != nil {
		return nil, fmt.Errorf("the plugin's folder cannot be opened: %v", reasonOf(p.rootErr))
	}
	if p.breaks(BreakFollowsLinks) {
		// By its path, each time, and through whatever is there now.
		return nil, nil
	}
	return p.root, nil
}

// reasonOf is an operating system error without the path it names: a
// plugin's message is for the learner, and the path is the plugin's own.
func reasonOf(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}

// failedFile is the name of the file that holds why a Source's last indexing
// failed, beside the Source's own.
func failedFile(collection, source string) string { return collection + "/" + source + ".failed" }

// fail records that the indexing of a Source failed, and why. The reason is
// kept in the folder: study starts a command anew for each of its own
// commands, and the learner reads the reason in a later one. It is kept in
// memory too, since a folder that cannot be written in is itself a reason.
// p.mu is held.
func (p *Plugin) fail(key [2]string, reason string) {
	p.failed[key] = reason
	if !p.breaks(BreakFailedLost) {
		_ = p.put(key[0], failedFile(key[0], key[1]), []byte(reason))
	}
}

// failure is why the last indexing of a Source failed, or nothing. p.mu is
// held.
func (p *Plugin) failure(key [2]string) string {
	if reason := p.failed[key]; reason != "" {
		return reason
	}
	reason, _ := p.get(failedFile(key[0], key[1]))
	return string(reason)
}

// failures are the Sources of a collection whose last indexing failed. p.mu
// is held.
func (p *Plugin) failures(collection string) []string {
	var ids []string
	for key := range p.failed {
		if key[0] == collection {
			ids = append(ids, key[1])
		}
	}
	files, _ := p.names(collection)
	for _, f := range files {
		if id, failed := strings.CutSuffix(f.Name(), ".failed"); failed && !strings.HasPrefix(id, ".") && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// forget drops what is kept of a Source's failure: it is asked for again.
// p.mu is held.
func (p *Plugin) forget(key [2]string) {
	delete(p.failed, key)
	name := failedFile(key[0], key[1])
	if root, err := p.folder(); err != nil {
		return
	} else if root == nil {
		_ = os.Remove(p.cfg.Folder + "/" + name)
	} else {
		_ = root.Remove(name)
	}
}

// get reads a file of the index.
func (p *Plugin) get(name string) ([]byte, error) {
	root, err := p.folder()
	if err != nil {
		return nil, err
	}
	if root == nil {
		return os.ReadFile(p.cfg.Folder + "/" + name)
	}
	return root.ReadFile(name)
}

// names lists what a collection's folder holds, which is nothing when there
// is no such folder.
func (p *Plugin) names(collection string) ([]fs.DirEntry, error) {
	root, err := p.folder()
	if err != nil {
		return nil, err
	}
	var files []fs.DirEntry
	if root == nil {
		files, err = os.ReadDir(p.cfg.Folder + "/" + collection)
	} else {
		files, err = fs.ReadDir(root.FS(), collection)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return files, err
}

func (p *Plugin) load(collection, source string) (*stored, error) {
	data, err := p.get(file(collection, source))
	if err != nil {
		return nil, err
	}
	var entry stored
	if err := json.Unmarshal(data, &entry); err != nil || entry.Format != 1 || entry.ID != source {
		return nil, fmt.Errorf("%s is not a Source of this plugin's index", file(collection, source))
	}
	return &entry, nil
}

func (p *Plugin) loadAll(collection string) ([]stored, error) {
	files, err := p.names(collection)
	if err != nil {
		return nil, reasonOf(err)
	}
	var entries []stored
	for _, f := range files {
		id, isSource := strings.CutSuffix(f.Name(), ".json")
		if !isSource || strings.HasPrefix(id, ".") {
			continue
		}
		entry, err := p.load(collection, id)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, reasonOf(err)
		}
		entries = append(entries, *entry)
	}
	return entries, nil
}

func (p *Plugin) save(collection string, entry stored) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return p.put(collection, file(collection, entry.ID), data)
}

// put writes a file of the index in a collection's folder, whole or not at
// all: under another name first, and then in place of what was there.
func (p *Plugin) put(collection, final string, data []byte) error {
	root, err := p.folder()
	if err != nil {
		return err
	}
	name := make([]byte, 8)
	_, _ = rand.Read(name)
	tmp := collection + "/.tmp-" + hex.EncodeToString(name)
	if root == nil {
		dir := p.cfg.Folder + "/"
		if err := os.MkdirAll(dir+collection, 0o700); err != nil {
			return reasonOf(err)
		}
		if err := os.WriteFile(dir+tmp, data, 0o600); err != nil {
			return reasonOf(err)
		}
		return reasonOf(os.Rename(dir+tmp, dir+final))
	}
	if p.breaks(BreakMindsStrays) {
		if entries, err := fs.ReadDir(root.FS(), "."); err == nil {
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") {
					return errors.New("the index holds something this plugin did not make")
				}
			}
		}
	}
	if err := root.MkdirAll(collection, 0o700); err != nil {
		return fmt.Errorf("the index cannot be written: %v", reasonOf(err))
	}
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("the index cannot be written: %v", reasonOf(err))
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, final)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("the index cannot be written: %v", reasonOf(err))
	}
	return nil
}
