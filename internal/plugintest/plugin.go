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
	"context"
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
	"path/filepath"
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
	// Tools are the tools it has. Nil is the contract's six.
	Tools []string
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

	// asking makes one request to index wait for the one before it.
	asking sync.Mutex

	mu sync.Mutex
	// jobs are the Sources being indexed now, and failed the reasons of the
	// ones whose last indexing failed, both by collection and Source.
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
	Format   int             `json:"format"`
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Kind     string          `json:"kind"`
	Hash     string          `json:"hash"`
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
	return &Plugin{cfg: cfg, jobs: map[[2]string]*job{}, failed: map[[2]string]string{}}
}

// Close stops whatever the plugin is indexing.
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
	} {
		if !slices.Contains(p.cfg.Tools, tool.name) {
			continue
		}
		server.AddTool(&mcp.Tool{Name: tool.name, Description: tool.description, InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

// read reads a tool's input and checks it against the contract.
func read(args json.RawMessage, in interface{ Validate() error }) *plugincontract.ErrorBody {
	if err := plugincontract.Decode(args, in); err != nil {
		return refuse(plugincontract.CodeInvalidArgument, "%v", err)
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

func (p *Plugin) index(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.IndexInput
	if refusal := read(args, &in); refusal != nil {
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
		delete(p.failed, key)
		p.mu.Unlock()
		return plugincontract.IndexResult{Source: plugincontract.SourceStatus{ID: have.ID, State: plugincontract.StateIndexed, Hash: have.Hash}}, nil
	}

	ctx, stop := context.WithCancel(context.Background())
	j := &job{hash: in.Source.Hash, stop: stop, done: make(chan struct{})}
	p.mu.Lock()
	delete(p.failed, key)
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
			p.failed[key] = err.Error()
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
	entry := stored{Format: 1, ID: in.Source.ID, Title: in.Source.Title, Kind: in.Source.Kind, Hash: "sha256:" + hex.EncodeToString(sum[:])}
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
	switch {
	case p.jobs[key] != nil:
		progress := p.jobs[key].progress
		status.State, status.Progress = plugincontract.StateIndexing, &progress
	case p.failed[key] != "":
		status.State, status.Reason = plugincontract.StateFailed, p.failed[key]
	default:
		status.State = plugincontract.StateIndexed
	}
	return status
}

func (p *Plugin) status(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.StatusInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
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
	for key := range p.failed {
		if _, known := byID[key[1]]; key[0] == in.Collection && !known {
			byID[key[1]] = nil
		}
	}
	out := plugincontract.StatusResult{Sources: []plugincontract.SourceStatus{}}
	for id, have := range byID {
		out.Sources = append(out.Sources, p.statusOf([2]string{in.Collection, id}, have))
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	return out, nil
}

func (p *Plugin) cancel(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.CancelInput
	if refusal := read(args, &in); refusal != nil {
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
	return plugincontract.CancelResult{Stopped: true}, nil
}

func (p *Plugin) search(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.SearchInput
	if refusal := read(args, &in); refusal != nil {
		return nil, refusal
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
	if len(out.Passages) > in.Limit {
		out.Passages = out.Passages[:in.Limit]
	}
	return out, nil
}

func (p *Plugin) passage(_ context.Context, args json.RawMessage) (any, *plugincontract.ErrorBody) {
	var in plugincontract.PassageInput
	if refusal := read(args, &in); refusal != nil {
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

func (p *Plugin) file(collection, source string) string {
	return filepath.Join(p.cfg.Folder, collection, source+".json")
}

func (p *Plugin) load(collection, source string) (*stored, error) {
	data, err := os.ReadFile(p.file(collection, source))
	if err != nil {
		return nil, err
	}
	var entry stored
	if err := json.Unmarshal(data, &entry); err != nil || entry.Format != 1 || entry.ID != source {
		return nil, fmt.Errorf("%s is not a Source of this plugin's index", p.file(collection, source))
	}
	return &entry, nil
}

func (p *Plugin) loadAll(collection string) ([]stored, error) {
	files, err := os.ReadDir(filepath.Join(p.cfg.Folder, collection))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []stored
	for _, file := range files {
		id, isSource := strings.CutSuffix(file.Name(), ".json")
		if !isSource || strings.HasPrefix(id, ".") {
			continue
		}
		entry, err := p.load(collection, id)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	return entries, nil
}

func (p *Plugin) save(collection string, entry stored) error {
	dir := filepath.Join(p.cfg.Folder, collection)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), p.file(collection, entry.ID))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
