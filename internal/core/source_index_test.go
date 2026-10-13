package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// patient is how long these tests give a plugin to say what it has indexed,
// unless a test is about a plugin that does not: long enough for the test
// plugin to start on a computer that is busy with every other test. busy is
// how long something that waits for no plugin may still take on such a
// computer: far less than a plugin has to start, and far more than a moment.
const (
	patient = 20 * time.Second
	busy    = 10 * time.Second
)

// shelf is a learner's computer on which the test plugin is registered as
// kb, with a Topic, c, that names it.
type shelf struct {
	*plugintest.Computer
	k *core.Core
	// record is where the plugin writes how it was started, and what it was
	// asked beside it.
	record string
}

// studyOn returns a Core for a study started on the computer, with the Study
// home given, or the computer's own when it is empty, whose plugins have
// short deadlines.
func studyOn(t *testing.T, c *plugintest.Computer, studyHome string, plugins core.PluginOptions) *core.Core {
	t.Helper()
	if studyHome == "" {
		studyHome = c.StudyHome
	}
	if plugins.Environ == nil {
		plugins.Environ = func() []string {
			return []string{"HOME=" + c.Home, "STUDY_HOME=" + studyHome, "PATH=" + os.Getenv("PATH")}
		}
	}
	for _, d := range []struct {
		value *time.Duration
		usual time.Duration
	}{{&plugins.StartTimeout, 20 * time.Second}, {&plugins.StopGrace, 300 * time.Millisecond}, {&plugins.GlanceTimeout, patient},
		{&plugins.IndexPoll, 10 * time.Millisecond}, {&plugins.CancelTimeout, 5 * time.Second}, {&plugins.EvidenceTimeout, patient}} {
		if *d.value == 0 {
			*d.value = d.usual
		}
	}
	n := &eventIDs
	k, err := core.Open(core.Options{
		Getenv:  func(key string) string { return map[string]string{"HOME": c.Home, "STUDY_HOME": studyHome}[key] },
		Dir:     c.Home,
		Now:     func() time.Time { return fixedNow },
		NewID:   func() string { return fmt.Sprintf("id%03d", n.Add(1)) },
		Plugins: plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// newShelf returns a computer with the test plugin registered as kb, started
// with args, and a Topic c that names it.
func newShelf(t *testing.T, args ...string) *shelf {
	t.Helper()
	s := &shelf{Computer: plugintest.NewComputer(t), record: filepath.Join(t.TempDir(), "record.json")}
	s.registerWith(t, args...)
	s.k = studyOn(t, s.Computer, "", core.PluginOptions{})
	namedTopic(t, s.k, "c", "kb")
	return s
}

// registerWith registers the test plugin as kb anew, started with args: the
// registry is read each time a plugin is needed.
func (s *shelf) registerWith(t *testing.T, args ...string) {
	t.Helper()
	s.Register(t, "kb", plugintest.Command(append([]string{"--record", s.record}, args...)...)...)
}

// namedTopic creates a Topic that names a Knowledge base plugin.
func namedTopic(t *testing.T, k *core.Core, id, plugin string) {
	t.Helper()
	ctx := context.Background()
	if _, err := k.CreateTopic(ctx, core.TopicSpec{Title: strings.ToUpper(id), ID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(ctx, id, core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin, Plugin: plugin}}); err != nil {
		t.Fatal(err)
	}
}

// book writes a file in the learner's Books folder and adds it to Topic c
// as a Source. The content's pages are divided by form feeds.
func (s *shelf) book(t *testing.T, name, content string) core.Source {
	t.Helper()
	return addBook(t, s.k, "c", filepath.Join(s.Home, "Books", name), content)
}

func addBook(t *testing.T, k *core.Core, topic, path, content string) core.Source {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := k.AddSource(context.Background(), core.SourceSpec{Topic: topic, File: path})
	if err != nil {
		t.Fatal(err)
	}
	return res.Source
}

// pages is a text of n pages, each with one paragraph that names word.
func pages(n int, word string) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteString("\f")
		}
		fmt.Fprintf(&b, "Page %d speaks of %s.\n", i+1, word)
	}
	return b.String()
}

func (s *shelf) sources(t *testing.T) core.SourceList {
	t.Helper()
	list, err := s.k.ListSources(context.Background(), "c")
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	return list
}

// states are the index states of a Topic's Sources, in order.
func states(list core.SourceList) []string {
	var out []string
	for _, src := range list.Sources {
		if src.Indexed == nil {
			out = append(out, "")
			continue
		}
		out = append(out, src.Indexed.State)
	}
	return out
}

func (s *shelf) index(t *testing.T, spec core.IndexSpec) core.IndexResult {
	t.Helper()
	spec.Topic = "c"
	res, err := s.k.IndexSources(context.Background(), spec)
	if err != nil {
		t.Fatalf("IndexSources(%+v): %v", spec, err)
	}
	return res
}

func (s *shelf) calls(t *testing.T) []plugintest.Call { return plugintest.ReadCalls(t, s.record) }

// asked are the calls of one tool the plugin received.
func asked(calls []plugintest.Call, tool string) []plugintest.Call {
	var out []plugintest.Call
	for _, call := range calls {
		if call.Tool == tool {
			out = append(out, call)
		}
	}
	return out
}

// sourcesOf are the ids of the Sources some calls were about.
func sourcesOf(calls []plugintest.Call) []string {
	var out []string
	for _, call := range calls {
		out = append(out, call.Source())
	}
	return out
}

// stopped fails the test when the plugin's last program is still running.
func (s *shelf) stopped(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(s.record); err != nil {
		return
	}
	started := plugintest.ReadRecord(t, s.record)
	if !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
}

// askedFor are the ids of the Sources an IndexResult says the plugin was
// asked to index.
func askedFor(res core.IndexResult) []string {
	out := []string{}
	for _, src := range res.Sources {
		if src.Asked {
			out = append(out, src.ID)
		}
	}
	return out
}

var collectionOfC = regexp.MustCompile(`^c-[a-z2-7]{32}$`)

// A Topic that names a plugin shows, for each Source, whether the plugin
// has indexed it, and the one command to run when it has not. study source
// index asks the plugin to, hands a file over by its path and its content
// hash and a web page by its address, and records nothing: what is indexed
// is the plugin's to say on this computer.
func TestSourcesSayWhatThePluginHasIndexed(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body><p>A goroutine is a lightweight thread.</p></body></html>")
	}))
	defer page.Close()
	strang := s.book(t, "strang.txt", pages(3, "elimination"))
	axler := s.book(t, "axler.txt", pages(2, "eigenvalues"))
	web, err := s.k.AddSource(ctx, core.SourceSpec{Topic: "c", URL: page.URL + "/blog", Title: "Go blog"})
	if err != nil {
		t.Fatal(err)
	}

	// Adding a Source indexed nothing, and asked no plugin anything.
	if calls := s.calls(t); len(calls) != 0 {
		t.Fatalf("adding Sources asked the plugin %v", plugintest.Tools(calls))
	}
	list := s.sources(t)
	want := &core.Indexing{Plugin: "kb", Action: "study source index c"}
	if !reflect.DeepEqual(list.Indexing, want) || !slices.Equal(states(list), []string{"not_indexed", "not_indexed", "not_indexed"}) {
		t.Fatalf("before indexing: %+v, states %v; want %+v and nothing indexed", list.Indexing, states(list), want)
	}
	status, err := s.k.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTopic := &core.Indexing{Plugin: "kb", Action: "study source index c", Sources: []core.IndexedSource{
		{ID: strang.ID, Title: strang.Title, SourceIndex: core.SourceIndex{State: core.IndexNotIndexed}},
		{ID: axler.ID, Title: axler.Title, SourceIndex: core.SourceIndex{State: core.IndexNotIndexed}},
		{ID: web.Source.ID, Title: "Go blog", SourceIndex: core.SourceIndex{State: core.IndexNotIndexed}},
	}}
	if len(status.Topics) != 1 || !reflect.DeepEqual(status.Topics[0].Indexing, wantTopic) {
		t.Fatalf("status before indexing: %+v; want %+v", status.Topics[0].Indexing, wantTopic)
	}
	// Asking what is indexed asks the plugin just that.
	if tools := plugintest.Tools(s.calls(t)); !slices.Equal(tools, []string{"contract", "status", "contract", "status"}) {
		t.Errorf("sources and status asked the plugin %v", tools)
	}

	topic := filepath.Join(s.StudyHome, "c")
	history, files := readFile(t, topic, "history.jsonl"), tree(t, topic)
	res := s.index(t, core.IndexSpec{})
	if res.Topic != "c" || res.Plugin != "kb" || !collectionOfC.MatchString(res.Collection) || res.DryRun {
		t.Errorf("IndexSources = %+v", res)
	}
	for i, src := range res.Sources {
		if !src.Asked || src.State != core.IndexIndexed || src.Skipped != "" || src.Reason != "" || src.ID != list.Sources[i].ID {
			t.Errorf("Source %d after indexing = %+v", i, src)
		}
	}
	// Whether a Source is indexed is no Event, and nothing of the Topic's.
	if after := readFile(t, topic, "history.jsonl"); after != history {
		t.Errorf("indexing recorded Events:\n%s", strings.TrimPrefix(after, history))
	}
	if after := tree(t, topic); !reflect.DeepEqual(after, files) {
		t.Errorf("indexing changed the Topic's files: %v, were %v", after, files)
	}

	indexed := asked(s.calls(t), "index")
	if len(indexed) != 3 {
		t.Fatalf("the plugin was asked to index %d times: %v", len(indexed), plugintest.Tools(s.calls(t)))
	}
	for _, call := range s.calls(t) {
		if call.Tool != "contract" && call.Collection() != res.Collection {
			t.Errorf("%s was asked of the collection %q, want %s", call.Tool, call.Collection(), res.Collection)
		}
	}
	if got := indexed[0].Indexed(); got.ID != strang.ID || got.Title != strang.Title || got.Kind != "file" ||
		got.Path != filepath.Join(s.Home, "Books", "strang.txt") || got.Hash != strang.Hash || got.URL != "" || got.Refetch {
		t.Errorf("the file was handed over as %+v", got)
	}
	if got := indexed[2].Indexed(); got.ID != web.Source.ID || got.Kind != "url" || got.URL != page.URL+"/blog" || got.Path != "" ||
		got.Hash != "" || got.Refetch {
		t.Errorf("the web page was handed over as %+v", got)
	}

	list = s.sources(t)
	if want := (&core.Indexing{Plugin: "kb"}); !reflect.DeepEqual(list.Indexing, want) ||
		!slices.Equal(states(list), []string{"indexed", "indexed", "indexed"}) {
		t.Errorf("after indexing: %+v, states %v; want everything indexed and nothing to do", list.Indexing, states(list))
	}
	// Asked again, there is nothing to ask the plugin for.
	before := len(asked(s.calls(t), "index"))
	again := s.index(t, core.IndexSpec{})
	if len(askedFor(again)) != 0 || len(asked(s.calls(t), "index")) != before {
		t.Errorf("indexing again asked for %v", askedFor(again))
	}
	// --again asks all the same, and has the web page fetched again.
	once := s.index(t, core.IndexSpec{Again: true, Sources: []string{web.Source.ID, axler.ID}})
	if !slices.Equal(askedFor(once), []string{axler.ID, web.Source.ID}) || len(once.Sources) != 2 {
		t.Errorf("indexing two Sources again asked for %v of %+v", askedFor(once), once.Sources)
	}
	last := asked(s.calls(t), "index")
	if n := len(last); n != before+2 || last[n-2].Indexed().Refetch || !last[n-1].Indexed().Refetch {
		t.Errorf("indexing again asked the plugin %+v", last[before:])
	}
	s.stopped(t)
	// The plugin's folder is the learner's state, outside the Study home.
	if _, err := os.Stat(filepath.Join(s.DataFolder("kb"), res.Collection)); err != nil {
		t.Errorf("the plugin's index: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.StudyHome, ".lamplight", "knowledge-base-plugins")); err == nil {
		t.Error("indexing made a folder for the plugin in the Study home")
	}
}

// study source index says how far the plugin is, and when it is stopped
// halfway it tells the plugin to stop, with a context of its own, and stops
// the plugin's program: nothing indexes behind a command that has exited.
// What was indexed stays, and running it again carries on.
func TestIndexingIsFollowedStoppedAndCarriedOn(t *testing.T) {
	s := newShelf(t, "--page-delay", "25ms")
	first := s.book(t, "first.txt", pages(2, "one"))
	long := s.book(t, "long.txt", pages(400, "two"))
	third := s.book(t, "third.txt", pages(2, "three"))

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var mu sync.Mutex
	var seen []core.IndexProgress
	began := time.Now()
	_, err := s.k.IndexSources(ctx, core.IndexSpec{Topic: "c", ProgressEvery: time.Millisecond, Progress: func(p core.IndexProgress) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, p)
		// Stopped once the long one is under way, as Ctrl-C stops a command.
		if p.Source == long.ID && p.State == core.ProgressRunning && p.Progress != nil && *p.Progress > 0 {
			stop()
		}
	}})
	if core.CodeOf(err) != core.CodeCanceled {
		t.Fatalf("IndexSources, stopped = %v (%s); want canceled", err, core.CodeOf(err))
	}
	for _, want := range []string{"study source index was stopped before it finished",
		"Knowledge base plugin kb was told to stop indexing Source " + long.ID + ", and stopped",
		"What is indexed stays indexed, and study source index c carries on from there"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if took := time.Since(began); took > 8*time.Second {
		t.Errorf("the stopped command took %s: the long Source alone takes 10s to index", took)
	}
	// It said how far it was: each Source it began, in order, and the long
	// one going on, with how much of it was done.
	mu.Lock()
	var story []string
	for _, p := range seen {
		if p.State != core.ProgressRunning {
			story = append(story, fmt.Sprintf("%s %s %d/%d %s", p.Source, p.State, p.Number, p.Total, p.Outcome))
		} else if p.Source == third.ID || p.Progress == nil || *p.Progress < 0 || *p.Progress > 1 || p.Elapsed <= 0 {
			// From 0 to 1, as the contract has it: a plugin that has read
			// the last page and not yet written its index says 1.
			t.Errorf("a report of a Source being indexed: %+v", p)
		}
	}
	mu.Unlock()
	// And, stopped, that it was telling the plugin to stop: at once, before
	// the plugin answered.
	if want := []string{first.ID + " started 1/3 ", first.ID + " finished 1/3 indexed", long.ID + " started 2/3 ",
		long.ID + " stopping 2/3 "}; !slices.Equal(story, want) {
		t.Errorf("the command reported %q, want %q", story, want)
	}

	// The plugin was asked to index two Sources, and told to stop the one it
	// had not finished. The third was never asked for.
	calls := s.calls(t)
	if got := sourcesOf(asked(calls, "index")); !slices.Equal(got, []string{first.ID, long.ID}) {
		t.Errorf("the plugin was asked to index %v", got)
	}
	if got := sourcesOf(asked(calls, "cancel")); !slices.Equal(got, []string{long.ID}) {
		t.Errorf("the plugin was told to stop %v, want the Source it was indexing", got)
	}
	if last := plugintest.LastBesidesStatus(calls); last.Tool != "cancel" {
		t.Errorf("the last thing the plugin was asked, besides its status, is %s, want cancel", last.Tool)
	}
	// Its program is gone, and with it whatever it was doing.
	s.stopped(t)
	if got := states(s.sources(t)); !slices.Equal(got, []string{"indexed", "not_indexed", "not_indexed"}) {
		t.Errorf("after the stop the Sources are %v", got)
	}

	// Run again, with a plugin that takes no time over a page: it carries on
	// with what was not indexed, and leaves alone what was.
	s.registerWith(t)
	before := len(s.calls(t))
	res := s.index(t, core.IndexSpec{})
	if got := askedFor(res); !slices.Equal(got, []string{long.ID, third.ID}) {
		t.Errorf("the second run asked for %v", got)
	}
	if got := sourcesOf(asked(s.calls(t)[before:], "index")); !slices.Equal(got, []string{long.ID, third.ID}) {
		t.Errorf("the plugin was asked, the second time, to index %v", got)
	}
	if len(asked(s.calls(t)[before:], "cancel")) != 0 {
		t.Error("a run that finished told the plugin to stop")
	}
	if got := states(s.sources(t)); !slices.Equal(got, []string{"indexed", "indexed", "indexed"}) {
		t.Errorf("after the second run the Sources are %v", got)
	}
	s.stopped(t)
}

// What goes wrong with a Source is no error of the command: the Source has
// failed, with the plugin's reason, and the others are indexed.
func TestAFailedSourceCarriesThePluginsReason(t *testing.T) {
	s := newShelf(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	good := s.book(t, "good.txt", pages(2, "one"))
	empty := s.book(t, "empty.txt", " \n\n ")
	page, err := s.k.AddSource(context.Background(), core.SourceSpec{Topic: "c", URL: gone.URL + "/missing"})
	if err != nil {
		t.Fatal(err)
	}
	res := s.index(t, core.IndexSpec{})
	// Each with what the plugin was handed: whoever runs the command sees
	// which files it has a plugin read.
	books := filepath.Join(s.Home, "Books")
	want := []core.IndexOutcome{
		{ID: good.ID, Title: good.Title, Path: filepath.Join(books, "good.txt"), SourceIndex: core.SourceIndex{State: core.IndexIndexed}, Asked: true},
		{ID: empty.ID, Title: empty.Title, Path: filepath.Join(books, "empty.txt"),
			SourceIndex: core.SourceIndex{State: core.IndexFailed, Reason: "there is no text in it"}, Asked: true},
		{ID: page.Source.ID, Title: page.Source.Title, URL: gone.URL + "/missing", SourceIndex: core.SourceIndex{State: core.IndexFailed,
			Reason: "the page could not be fetched: the server answered 404"}, Asked: true},
	}
	if !reflect.DeepEqual(res.Sources, want) {
		t.Errorf("IndexSources = %+v\nwant %+v", res.Sources, want)
	}
	// The plugin of the next command is another start of its program, and
	// says the same: the Sources have failed, each with its reason. A failure
	// is no next action, so there is none.
	list := s.sources(t)
	if got := states(list); !slices.Equal(got, []string{"indexed", "failed", "failed"}) || list.Indexing.Action != "" {
		t.Errorf("afterwards the Sources are %v, and the action %q; want the failures shown, and nothing to run", got, list.Indexing.Action)
	}
	if got := list.Sources[1].Indexed; got == nil || got.Reason != "there is no text in it" {
		t.Errorf("afterwards the empty file's index is %+v; want it failed with the plugin's reason", got)
	}
	// Whoever runs the command has them tried again.
	again := s.index(t, core.IndexSpec{})
	if len(again.Sources) != 3 || again.Sources[0].Asked || !again.Sources[1].Asked || !again.Sources[2].Asked {
		t.Errorf("run again, the plugin was asked for %+v; want the two that failed, and not the one that is indexed", again.Sources)
	}
}

// A Source that failed beside one that was never tried: the command is the
// next action, for the one that was never tried.
func TestAFailureIsNoNextActionAndAnUntriedSourceIs(t *testing.T) {
	s := newShelf(t)
	s.book(t, "empty.txt", " \n\n ")
	s.index(t, core.IndexSpec{})
	if list := s.sources(t); !slices.Equal(states(list), []string{"failed"}) || list.Indexing.Action != "" {
		t.Fatalf("after a failure the Sources are %v, and the action %q", states(list), list.Indexing.Action)
	}
	s.book(t, "good.txt", pages(2, "one"))
	if list := s.sources(t); !slices.Equal(states(list), []string{"failed", "not_indexed"}) || list.Indexing.Action != "study source index c" {
		t.Errorf("with a Source that was never tried the Sources are %v, and the action %q", states(list), list.Indexing.Action)
	}
}

// A service remembers a failure, and says it to whoever asks.
func TestAServicesFailureIsShownWithItsReason(t *testing.T) {
	c := plugintest.NewComputer(t)
	service := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	defer service.Close()
	c.RegisterURL(t, "remote", server.URL+"/mcp")
	k := studyOn(t, c, "", core.PluginOptions{})
	namedTopic(t, k, "c", "remote")
	empty := addBook(t, k, "c", filepath.Join(c.Home, "Books", "empty.txt"), "\n")
	if _, err := k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}
	list, err := k.ListSources(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	// With the command that has it tried again, beside the Source: the file
	// is there to hand over.
	want := &core.SourceIndex{State: core.IndexFailed, Reason: "there is no text in it", Retry: "study source index c " + empty.ID}
	if len(list.Sources) != 1 || !reflect.DeepEqual(list.Sources[0].Indexed, want) || list.Indexing.Action != "" ||
		list.Sources[0].ID != empty.ID {
		t.Errorf("ListSources = %+v, %+v; want the Source failed, with the reason, and no action", list.Sources, list.Indexing)
	}
	// A service gets no folder from study.
	if _, err := os.Stat(c.DataFolder("remote")); err == nil {
		t.Error("study made a folder for a plugin reached by URL")
	}
}

// A Source is its content. A file that has other content now than was added
// is not handed to the plugin, with --again either: what the plugin indexed
// before stays the Source's, and a file that changed before it was ever
// indexed is skipped, with why.
func TestAFileThatChangedSinceItWasIndexed(t *testing.T) {
	s := newShelf(t)
	book := s.book(t, "book.txt", pages(3, "elimination"))
	never := s.book(t, "never.txt", pages(2, "eigenvalues"))
	quiet := s.book(t, "quiet.txt", pages(2, "determinants"))
	path := func(name string) string { return filepath.Join(s.Home, "Books", name) }
	// One file is indexed, and then all three change: the last in a way
	// that finding it does not notice, with the same size and the same time.
	s.index(t, core.IndexSpec{Sources: []string{book.ID}})
	info, err := os.Stat(path("quiet.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"book.txt": pages(4, "pivots"), "never.txt": pages(5, "traces"),
		"quiet.txt": strings.Replace(pages(2, "determinants"), "determinants", "DETERMINANTS", 1)} {
		if err := os.WriteFile(path(name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(path("quiet.txt"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	list := s.sources(t)
	if list.Sources[0].State != core.SourceChanged || list.Sources[0].Indexed.State != core.IndexIndexed {
		t.Errorf("the indexed file that changed = %+v, index %+v; want changed on this computer, and still indexed",
			list.Sources[0].State, list.Sources[0].Indexed)
	}
	if list.Sources[1].State != core.SourceChanged || list.Sources[1].Indexed.State != core.IndexNotIndexed {
		t.Errorf("the file that changed before it was indexed = %+v, index %+v", list.Sources[1].State, list.Sources[1].Indexed)
	}

	before := len(asked(s.calls(t), "index"))
	for _, again := range []bool{false, true} {
		res := s.index(t, core.IndexSpec{Again: again})
		if len(askedFor(res)) != 0 {
			t.Errorf("again=%v: the plugin was asked to index %v", again, askedFor(res))
		}
		changed := "the file at " + path("book.txt") + " has other content now than was added, so it is not this Source"
		switch src := res.Sources[0]; {
		case src.State != core.IndexIndexed:
			t.Errorf("again=%v: the Source that was indexed = %+v", again, src)
		case again && !strings.HasPrefix(src.Skipped, changed), !again && src.Skipped != "":
			t.Errorf("again=%v: the Source that was indexed is skipped with %q", again, src.Skipped)
		}
		for i, name := range []string{"never.txt", "quiet.txt"} {
			src := res.Sources[i+1]
			if src.State != core.IndexNotIndexed || !strings.HasPrefix(src.Skipped, "the file at "+path(name)+" has other content now") ||
				!strings.Contains(src.Skipped, "add the file as it is now as a new Source with study source add") {
				t.Errorf("again=%v: %s = %+v; want it skipped, with what to do", again, name, src)
			}
		}
	}
	if n := len(asked(s.calls(t), "index")); n != before {
		t.Errorf("the plugin was handed a file that changed: %v", sourcesOf(asked(s.calls(t), "index")[before:]))
	}
	// A file that is no longer there at all is skipped too.
	if err := os.Remove(path("never.txt")); err != nil {
		t.Fatal(err)
	}
	res := s.index(t, core.IndexSpec{Sources: []string{never.ID}})
	if want := "its file is not on this computer: say where it is with study source update c " + never.ID + " --path <file>"; res.Sources[0].Skipped != want || res.Sources[0].Asked {
		t.Errorf("the Source whose file is gone = %+v; want skipped with %q", res.Sources[0], want)
	}
	// Restored, the file is the Source again, and is indexed.
	if err := os.WriteFile(path("quiet.txt"), []byte(pages(2, "determinants")), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := s.index(t, core.IndexSpec{Sources: []string{quiet.ID}}); !res.Sources[0].Asked || res.Sources[0].State != core.IndexIndexed {
		t.Errorf("the file, restored = %+v", res.Sources[0])
	}
}

// What a plugin cannot index it is not asked to: a web page, for a plugin
// that indexes files. The Source is not indexed, with why, and that alone
// is no reason to run the command.
func TestAKindOfSourceThePluginDoesNotIndex(t *testing.T) {
	s := newShelf(t, "--sources", "file")
	page, err := s.k.AddSource(context.Background(), core.SourceSpec{Topic: "c", URL: "https://example.com/a"})
	if err != nil {
		t.Fatal(err)
	}
	list := s.sources(t)
	want := &core.SourceIndex{State: core.IndexNotIndexed, Reason: "Knowledge base plugin kb does not index web pages"}
	if !reflect.DeepEqual(list.Sources[0].Indexed, want) || list.Indexing.Action != "" {
		t.Errorf("ListSources = %+v, %+v; want not indexed with why, and nothing to do", list.Sources[0].Indexed, list.Indexing)
	}
	res := s.index(t, core.IndexSpec{Again: true})
	if want := (core.IndexOutcome{ID: page.Source.ID, Title: page.Source.Title, URL: "https://example.com/a",
		SourceIndex: core.SourceIndex{State: core.IndexNotIndexed},
		Skipped:     "Knowledge base plugin kb does not index web pages"}); !reflect.DeepEqual(res.Sources, []core.IndexOutcome{want}) {
		t.Errorf("IndexSources = %+v, want %+v", res.Sources, want)
	}
	if n := len(asked(s.calls(t), "index")); n != 0 {
		t.Errorf("the plugin was asked to index %d times", n)
	}
	// With a file beside it, the file is what there is to index.
	book := s.book(t, "book.txt", pages(2, "one"))
	if list := s.sources(t); list.Indexing.Action != "study source index c" {
		t.Errorf("with a file to index the action is %q", list.Indexing.Action)
	}
	if res := s.index(t, core.IndexSpec{}); !slices.Equal(askedFor(res), []string{book.ID}) {
		t.Errorf("IndexSources asked for %v", askedFor(res))
	}
	if list := s.sources(t); list.Indexing.Action != "" {
		t.Errorf("once the file is indexed the action is %q", list.Indexing.Action)
	}
}

// The dry run starts the plugin, asks what it has indexed, and says what the
// real run then asks it to index, having asked for nothing itself.
func TestTheIndexDryRunSaysWhatTheRealRunDoes(t *testing.T) {
	s := newShelf(t, "--sources", "file")
	done := s.book(t, "done.txt", pages(2, "one"))
	s.index(t, core.IndexSpec{})
	todo := s.book(t, "todo.txt", pages(2, "two"))
	s.book(t, "empty.txt", "\n")
	lost := s.book(t, "lost.txt", pages(2, "four"))
	if err := os.Remove(filepath.Join(s.Home, "Books", "lost.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.k.AddSource(context.Background(), core.SourceSpec{Topic: "c", URL: "https://example.com/a"}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []core.IndexSpec{{}, {Again: true}, {Sources: []string{todo.ID, lost.ID}}, {Sources: []string{done.ID}, Again: true}} {
		t.Run(fmt.Sprintf("again=%v sources=%d", spec.Again, len(spec.Sources)), func(t *testing.T) {
			before := s.calls(t)
			spec.DryRun = true
			dry := s.index(t, spec)
			if !dry.DryRun {
				t.Error("the dry run does not say that it is one")
			}
			if made := s.calls(t)[len(before):]; len(asked(made, "index")) != 0 || len(asked(made, "cancel")) != 0 {
				t.Errorf("the dry run asked the plugin %v", plugintest.Tools(made))
			}
			before = s.calls(t)
			spec.DryRun = false
			real := s.index(t, spec)
			if got, want := askedFor(real), askedFor(dry); !slices.Equal(got, want) {
				t.Errorf("the dry run said %v would be asked for, and the real run asked for %v", want, got)
			}
			if got := sourcesOf(asked(s.calls(t)[len(before):], "index")); !slices.Equal(got, askedFor(dry)) {
				t.Errorf("the dry run said %v would be asked for, and the plugin was asked for %v", askedFor(dry), got)
			}
			for i, src := range dry.Sources {
				if got := real.Sources[i]; got.ID != src.ID || got.Skipped != src.Skipped || got.Asked != src.Asked {
					t.Errorf("the dry run said %+v, and the real run %+v", src, got)
				}
			}
		})
	}
	// What a dry run says after them: the one that failed in those runs is
	// still failed, and would be asked for again, and why each of the others
	// is not asked for.
	s.registerWith(t, "--sources", "file")
	dry := s.index(t, core.IndexSpec{DryRun: true})
	var got []string
	for _, src := range dry.Sources {
		got = append(got, fmt.Sprintf("%s asked=%v skipped=%v", src.State, src.Asked, src.Skipped != ""))
	}
	want := []string{"indexed asked=false skipped=false", "indexed asked=false skipped=false", "failed asked=true skipped=false",
		"not_indexed asked=false skipped=true", "not_indexed asked=false skipped=true"}
	if !slices.Equal(got, want) {
		t.Errorf("the dry run = %q\nwant %q", got, want)
	}
}

// Nothing waits for a plugin. A Topic whose plugin is not registered on this
// computer, does not start, does not answer or does not speak the contract
// shows its Sources' index state as not known, with the one command that
// says what is wrong, and status, a Topic's Sources and opening a Session
// are as quick as ever.
func TestNothingWaitsForAPlugin(t *testing.T) {
	const patience = 500 * time.Millisecond
	check, add := "study knowledge-base check kb", "study knowledge-base add kb -- <command>"
	hangs := func(t *testing.T) http.Handler {
		over := make(chan struct{})
		t.Cleanup(func() { close(over) })
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-over })
	}
	for name, tc := range map[string]struct {
		// register registers the plugin kb, or does not.
		register func(t *testing.T, s *shelf)
		problem  string
		action   string
	}{
		"a plugin that is not registered": {func(t *testing.T, s *shelf) {
			// The registry holds the test plugin under another name.
			registry, err := json.Marshal(map[string]any{"format": 1, "plugins": []any{
				map[string]any{"name": "other", "command": plugintest.Command("--record", s.record)}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.ConfigDir, "knowledge-base-plugins.json"), registry, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "no Knowledge base plugin named kb is registered on this computer", add},
		"no registry at all": {func(t *testing.T, s *shelf) {
			if err := os.Remove(filepath.Join(s.ConfigDir, "knowledge-base-plugins.json")); err != nil {
				t.Fatal(err)
			}
		}, "no Knowledge base plugin named kb is registered on this computer", add},
		"a registry that is damaged": {func(t *testing.T, s *shelf) {
			if err := os.WriteFile(filepath.Join(s.ConfigDir, "knowledge-base-plugins.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "knowledge-base-plugins.json", "study knowledge-base list"},
		"a plugin that never answers": {func(t *testing.T, s *shelf) { s.registerWith(t, "--hang", "start") },
			"Knowledge base plugin kb did not say what it has indexed within 500ms, and study did not wait for it", check},
		"a plugin that never answers and will not stop": {func(t *testing.T, s *shelf) { s.registerWith(t, "--hang", "start", "--stubborn") },
			"Knowledge base plugin kb did not say what it has indexed within 500ms", check},
		"a plugin that exits at once": {func(t *testing.T, s *shelf) {
			s.registerWith(t, "--exit", "3", "--stderr", "cannot open the index")
		}, "Knowledge base plugin kb stopped before it could open an MCP session (exit status 3)", check},
		"a plugin that is slow to start": {func(t *testing.T, s *shelf) { s.registerWith(t, "--delay", "contract=30s") },
			"Knowledge base plugin kb did not say what it has indexed within 500ms", check},
		"a plugin that answers slowly": {func(t *testing.T, s *shelf) { s.registerWith(t, "--delay", "status=30s") },
			"Knowledge base plugin kb did not say what it has indexed within 500ms", check},
		"a plugin that never answers what is indexed": {func(t *testing.T, s *shelf) { s.registerWith(t, "--hang", "status") },
			"Knowledge base plugin kb did not say what it has indexed within 500ms", check},
		"a plugin that speaks a newer contract": {func(t *testing.T, s *shelf) { s.registerWith(t, "--contract", `{"version": 2}`) },
			"Knowledge base plugin kb speaks version 2 of Lamplight's contract, and this version of study knows version 1: upgrade study", check},
		"a plugin that speaks no MCP": {func(t *testing.T, s *shelf) { s.registerWith(t, "--garbage") },
			"Knowledge base plugin kb does not speak MCP on its standard output", check},
		"a plugin that does not speak the contract": {func(t *testing.T, s *shelf) { s.registerWith(t, "--tools", "status,search") },
			"Knowledge base plugin kb does not speak Lamplight's contract", check},
		"a plugin whose status is not the contract's": {func(t *testing.T, s *shelf) { s.registerWith(t, "--result", `status={"sources": "all of them"}`) },
			"Knowledge base plugin kb does not speak Lamplight's contract: what its status tool gave back", check},
		"a plugin that asks study something": {func(t *testing.T, s *shelf) { s.registerWith(t, "--ask", "status") },
			"Knowledge base plugin kb sent study a request", check},
		"a service nobody runs": {func(t *testing.T, s *shelf) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := "http://" + listener.Addr().String() + "/mcp"
			listener.Close()
			s.RegisterURL(t, "kb", address)
		}, "Knowledge base plugin kb does not answer as an MCP server", check},
		"a service that never answers": {func(t *testing.T, s *shelf) {
			// The server closes after its handler was let go: a server
			// waits for what it is still answering.
			var server *httptest.Server
			t.Cleanup(func() { server.Close() })
			server = httptest.NewServer(hangs(t))
			s.RegisterURL(t, "kb", server.URL+"/mcp")
		}, "Knowledge base plugin kb did not say what it has indexed within 500ms", check},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newShelf(t)
			src := s.book(t, "book.txt", pages(2, "one"))
			tc.register(t, s)
			// A plugin that says nothing has half a second here, and thirty
			// to start when it is asked to index: what waited for it would
			// take that long. One that says what is wrong with it is given
			// the time to say it on a computer that is busy.
			wait := patient
			if strings.Contains(tc.problem, "did not say what it has indexed within") {
				wait = patience
			}
			k := studyOn(t, s.Computer, "", core.PluginOptions{GlanceTimeout: wait, StartTimeout: 30 * time.Second})
			quick := func(what string, began time.Time) {
				t.Helper()
				if took := time.Since(began); took > busy {
					t.Errorf("%s took %s with %s, and may take %s for a plugin", what, took, name, wait)
				}
			}
			want := &core.Indexing{Plugin: "kb", Problem: tc.problem, Action: tc.action}
			matches := func(got *core.Indexing) bool {
				return got != nil && got.Plugin == "kb" && strings.Contains(got.Problem, tc.problem) && got.Action == tc.action
			}

			began := time.Now()
			list, err := k.ListSources(ctx, "c")
			quick("ListSources", began)
			if err != nil || !matches(list.Indexing) || len(list.Sources) != 1 || list.Sources[0].ID != src.ID ||
				list.Sources[0].State != core.SourceOK || !reflect.DeepEqual(list.Sources[0].Indexed, &core.SourceIndex{State: core.IndexUnknown}) {
				t.Errorf("ListSources = %+v, %+v, %v\nwant the Source, not known to be indexed, with %+v", list.Indexing, list.Sources, err, want)
			}
			began = time.Now()
			status, err := k.Status(ctx)
			quick("Status", began)
			if err != nil || len(status.Topics) != 1 || !matches(status.Topics[0].Indexing) || !reflect.DeepEqual(status.Topics[0].Indexing.Sources,
				[]core.IndexedSource{{ID: src.ID, Title: src.Title, SourceIndex: core.SourceIndex{State: core.IndexUnknown}}}) {
				t.Errorf("Status = %+v, %v\nwant the Source, not known to be indexed, with %+v", status.Topics, err, want)
			}
			// The rest of where the learner is is there all the same.
			if status.ActiveTopic == nil || status.Recommended == nil || len(status.Problems) != 0 {
				t.Errorf("Status = %+v", status)
			}
			// Opening a Session asks no plugin anything, and starts none: the
			// record a plugin writes when it starts is as it was.
			written := func() string {
				data, err := os.ReadFile(s.record)
				return fmt.Sprintf("%s %v, %d calls", data, err != nil, len(s.calls(t)))
			}
			s.stopped(t)
			before := written()
			began = time.Now()
			opened, err := k.OpenSession(ctx, "c", core.SessionSpec{Energy: core.EnergyFull})
			quick("OpenSession", began)
			if err != nil || opened.Session == "" {
				t.Errorf("OpenSession = %+v, %v", opened, err)
			}
			if after := written(); after != before {
				t.Errorf("opening a Session started the Topic's plugin: its record was %s, and is %s", before, after)
			}
		})
	}
}

// A plugin that takes its time, and less than it has, is waited for.
func TestAPluginThatAnswersInTimeIsHeard(t *testing.T) {
	s := newShelf(t, "--delay", "status=200ms", "--delay", "contract=100ms")
	s.book(t, "book.txt", pages(2, "one"))
	began := time.Now()
	list := s.sources(t)
	if list.Indexing.Problem != "" || !slices.Equal(states(list), []string{"not_indexed"}) || time.Since(began) < 300*time.Millisecond {
		t.Errorf("ListSources after %s = %+v, states %v", time.Since(began), list.Indexing, states(list))
	}
}

// A Topic without Sources has nothing to ask its plugin: the plugin is not
// started, though one that is not registered is still said to be missing.
func TestATopicWithoutSourcesStartsNoPlugin(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t, "--hang", "start")
	namedTopic(t, s.k, "d", "shelf")
	began := time.Now()
	status, err := s.k.Status(ctx)
	if err != nil || len(status.Topics) != 2 {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	if took := time.Since(began); took > busy {
		t.Errorf("Status took %s with nothing to ask a plugin, which has %s to answer", took, patient)
	}
	if want := (&core.Indexing{Plugin: "kb"}); !reflect.DeepEqual(status.Topics[0].Indexing, want) {
		t.Errorf("the Topic whose plugin is registered = %+v, want %+v", status.Topics[0].Indexing, want)
	}
	if want := (&core.Indexing{Plugin: "shelf", Problem: "no Knowledge base plugin named shelf is registered on this computer",
		Action: "study knowledge-base add shelf -- <command>"}); !reflect.DeepEqual(status.Topics[1].Indexing, want) {
		t.Errorf("the Topic whose plugin is not registered = %+v, want %+v", status.Topics[1].Indexing, want)
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("the plugin was started with nothing to ask it")
	}
	// A Topic that names no plugin says nothing of one, and reads no
	// registry.
	if _, err := s.k.UpdateTopic(ctx, "d", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBaseNone}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ConfigDir, "knowledge-base-plugins.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.k.ListSources(ctx, "d")
	if err != nil || list.Indexing != nil || len(list.Sources) != 0 {
		t.Errorf("ListSources of a Topic without a plugin = %+v, %v", list, err)
	}
}

// A command that only has to know which Topic it is about, as study check
// does inside a Topic's folder, learns it without a plugin being asked
// anything: it is what status says, and no plugin was started for it.
func TestTheActiveTopicIsFoundWithoutAPlugin(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t, "--hang", "start")
	s.book(t, "book.txt", pages(2, "one"))
	// The plugin never answers, and has twenty seconds to: whatever asked it
	// would take that long.
	began := time.Now()
	active, err := s.k.ActiveTopic(ctx)
	if err != nil || active == nil || active.ID != "c" || active.ChosenBy != core.ChosenByRecent {
		t.Fatalf("ActiveTopic = %+v, %v", active, err)
	}
	if took := time.Since(began); took > busy {
		t.Errorf("ActiveTopic took %s with a plugin that never answers", took)
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("finding the Active topic started the Topic's plugin")
	}
	status, err := studyOn(t, s.Computer, "", core.PluginOptions{GlanceTimeout: 300 * time.Millisecond}).Status(ctx)
	if err != nil || !reflect.DeepEqual(status.ActiveTopic, active) {
		t.Errorf("status says the Active topic is %+v, %v; ActiveTopic says %+v", status.ActiveTopic, err, active)
	}
	s.stopped(t)
	// No Study home yet, no Active topic, and no error.
	empty := studyOn(t, plugintest.NewComputer(t), filepath.Join(t.TempDir(), "nowhere"), core.PluginOptions{})
	if active, err := empty.ActiveTopic(ctx); err != nil || active != nil {
		t.Errorf("ActiveTopic without a Study home = %+v, %v", active, err)
	}
}

// status asks each plugin once, however many Topics name it, and each Topic
// has a collection of its own.
func TestStatusAsksEachPluginOnce(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	s.book(t, "c.txt", pages(2, "one"))
	for _, id := range []string{"a", "b"} {
		namedTopic(t, s.k, id, "kb")
		addBook(t, s.k, id, filepath.Join(s.Home, "Books", id+".txt"), pages(2, id+id))
	}
	namedTopic(t, s.k, "e", "kb") // no Sources
	namedTopic(t, s.k, "f", "missing")
	if _, err := s.k.CreateTopic(ctx, core.TopicSpec{Title: "G", ID: "g"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.k.IndexSources(ctx, core.IndexSpec{Topic: "b"}); err != nil {
		t.Fatal(err)
	}
	before := len(s.calls(t))
	status, err := s.k.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, topic := range status.Topics {
		switch x := topic.Indexing; {
		case x == nil:
			got = append(got, topic.ID+": no plugin")
		case x.Problem != "":
			got = append(got, topic.ID+": "+x.Plugin+" has a problem, "+x.Action)
		default:
			line := topic.ID + ": " + x.Plugin
			for _, src := range x.Sources {
				line += " " + src.State
			}
			got = append(got, strings.TrimSpace(line+" "+x.Action))
		}
	}
	want := []string{"a: kb not_indexed study source index a", "b: kb indexed", "c: kb not_indexed study source index c", "e: kb",
		"f: missing has a problem, study knowledge-base add missing -- <command>", "g: no plugin"}
	if !slices.Equal(got, want) {
		t.Errorf("status = %q\nwant %q", got, want)
	}
	made := s.calls(t)[before:]
	if tools := plugintest.Tools(made); !slices.Equal(tools, []string{"contract", "status", "status", "status"}) {
		t.Errorf("status asked the plugin %v, want it started once and asked of three Topics", tools)
	}
	collections := map[string]bool{}
	for _, call := range asked(made, "status") {
		collections[call.Collection()] = true
	}
	if len(collections) != 3 {
		t.Errorf("three Topics were asked about as %v", collections)
	}
	s.stopped(t)
}

// A plugin's folder is one for the plugin on a computer, not one for each
// Study home. Two Study homes that each hold a Topic of the same name do
// not share a collection: what one indexed, the other does not find.
func TestTwoStudyHomesDoNotShareACollection(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	other := filepath.Join(s.Base(), "other-study")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	second := studyOn(t, s.Computer, other, core.PluginOptions{})
	namedTopic(t, second, "c", "kb")
	// The same book, by the same path, in the Topic of the same name.
	s.book(t, "book.txt", pages(2, "one"))
	if _, err := second.AddSource(ctx, core.SourceSpec{Topic: "c", File: filepath.Join(s.Home, "Books", "book.txt")}); err != nil {
		t.Fatal(err)
	}
	first := s.index(t, core.IndexSpec{})
	list, err := second.ListSources(ctx, "c")
	if err != nil || !slices.Equal(states(list), []string{"not_indexed"}) {
		t.Fatalf("the other Study home's Topic = %v, %v; want its Source not indexed", states(list), err)
	}
	res, err := second.IndexSources(ctx, core.IndexSpec{Topic: "c"})
	if err != nil || res.Collection == first.Collection || !collectionOfC.MatchString(res.Collection) {
		t.Fatalf("the two Study homes' Topics are the collections %s and %s (%v)", first.Collection, res.Collection, err)
	}
	// One folder, with a collection for each.
	entries, err := os.ReadDir(s.DataFolder("kb"))
	if err != nil || len(entries) != 2 {
		t.Errorf("the plugin's folder holds %v, %v; want the two collections", entries, err)
	}
	// And the same Study home is the same collection on every run, from
	// wherever study is started.
	again := s.index(t, core.IndexSpec{DryRun: true})
	if again.Collection != first.Collection || again.Sources[0].State != core.IndexIndexed {
		t.Errorf("a second run names the collection %s, the first %s: %+v", again.Collection, first.Collection, again.Sources)
	}
}

// What cannot be indexed carries a documented code and says what to do, and
// starts no plugin it need not.
func TestIndexSourcesRefusals(t *testing.T) {
	ctx := context.Background()
	halted, halt := context.WithCancel(ctx)
	halt()
	s := newShelf(t)
	book := s.book(t, "book.txt", pages(2, "one"))
	if _, err := s.k.CreateTopic(ctx, core.TopicSpec{Title: "Plain", ID: "plain"}); err != nil {
		t.Fatal(err)
	}
	namedTopic(t, s.k, "elsewhere", "shelf")
	for name, tc := range map[string]struct {
		ctx  context.Context
		spec core.IndexSpec
		code core.ErrorCode
		want string
	}{
		"no Topic":             {ctx, core.IndexSpec{}, core.CodeInvalidArgument, "name the Topic"},
		"no such Topic":        {ctx, core.IndexSpec{Topic: "biology"}, core.CodeNotFound, "there is no Topic named biology"},
		"what is no Source id": {ctx, core.IndexSpec{Topic: "c", Sources: []string{"../book"}}, core.CodeInvalidArgument, `"../book" is not a Source id`},
		"a Source it lacks":    {ctx, core.IndexSpec{Topic: "c", Sources: []string{book.ID, "other.k3f9a2"}}, core.CodeNotFound, "has no Source other.k3f9a2"},
		"a Topic without a plugin": {ctx, core.IndexSpec{Topic: "plain"}, core.CodeFailedPrecondition,
			"Topic plain names no Knowledge base plugin, so nothing indexes its Sources: name one with study topic update plain --knowledge-base <name>"},
		"a plugin this computer lacks": {ctx, core.IndexSpec{Topic: "elsewhere"}, core.CodeFailedPrecondition,
			"Topic elsewhere names the Knowledge base plugin shelf, which is not registered on this computer: register it with study " +
				"knowledge-base add shelf -- <command>, or name another with study topic update elsewhere --knowledge-base <name>"},
		"a command that was stopped": {halted, core.IndexSpec{Topic: "c"}, core.CodeCanceled, ""},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := s.k.IndexSources(tc.ctx, tc.spec)
			if core.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(res, core.IndexResult{}) {
				t.Errorf("IndexSources = %+v, %v (%s); want %s with %q", res, err, core.CodeOf(err), tc.code, tc.want)
			}
		})
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("a command that was refused started the plugin")
	}
	// A plugin that cannot index is reported as study knowledge-base check
	// reports it, and is not left running.
	for name, tc := range map[string]struct {
		args []string
		code core.ErrorCode
		want string
	}{
		"a newer contract":       {[]string{"--contract", `{"version": 2}`}, core.CodeNewerFormat, "speaks version 2 of Lamplight's contract"},
		"a plugin that exits":    {[]string{"--exit", "3"}, core.CodeFailedPrecondition, "stopped before it could open an MCP session"},
		"no status to speak of":  {[]string{"--refuse", "status=internal:the index is locked"}, core.CodeFailedPrecondition, "failed in its status tool: the index is locked"},
		"a status that is none":  {[]string{"--result", `status={"sources": 3}`}, core.CodeFailedPrecondition, "does not speak Lamplight's contract"},
		"a plugin that is stuck": {[]string{"--hang", "start"}, core.CodeFailedPrecondition, "did not open an MCP session within 400ms"},
	} {
		t.Run(name, func(t *testing.T) {
			s.registerWith(t, tc.args...)
			k := studyOn(t, s.Computer, "", core.PluginOptions{StartTimeout: 400 * time.Millisecond})
			res, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c"})
			if core.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(res, core.IndexResult{}) {
				t.Errorf("IndexSources = %+v, %v (%s); want %s with %q", res, err, core.CodeOf(err), tc.code, tc.want)
			}
			if strings.Contains(err.Error(), "carries on from there") {
				t.Errorf("a run that asked for nothing speaks of carrying on: %v", err)
			}
			s.stopped(t)
		})
	}
}

// A plugin that refuses to index one Source has failed that Source, with
// what it said, and is asked for the next.
func TestAPluginThatRefusesToIndex(t *testing.T) {
	s := newShelf(t, "--refuse", "index=internal:the disk is full")
	s.book(t, "one.txt", pages(2, "one"))
	s.book(t, "two.txt", pages(2, "two"))
	res := s.index(t, core.IndexSpec{})
	for _, src := range res.Sources {
		if !src.Asked || src.State != core.IndexFailed || !strings.Contains(src.Reason, "Knowledge base plugin kb failed in its index tool: the disk is full") {
			t.Errorf("a Source the plugin refused = %+v", src)
		}
	}
	if n := len(asked(s.calls(t), "index")); n != 2 {
		t.Errorf("the plugin was asked to index %d times, want both Sources", n)
	}
	if n := len(asked(s.calls(t), "cancel")); n != 0 {
		t.Errorf("the plugin was told to stop %d times", n)
	}
}

// A plugin that goes away while it indexes ends the command, which says so
// and leaves nothing running.
func TestAPluginThatDiesWhileItIndexes(t *testing.T) {
	s := newShelf(t, "--page-delay", "25ms")
	s.book(t, "first.txt", pages(2, "one"))
	long := s.book(t, "long.txt", pages(400, "two"))
	var once sync.Once
	began := time.Now()
	_, err := s.k.IndexSources(context.Background(), core.IndexSpec{Topic: "c", ProgressEvery: time.Millisecond, Progress: func(p core.IndexProgress) {
		if p.Source == long.ID && p.State == core.ProgressRunning {
			once.Do(func() { plugintest.Kill(plugintest.ReadRecord(t, s.record).PID) })
		}
	}})
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "Knowledge base plugin kb") ||
		!strings.Contains(err.Error(), "What is indexed stays indexed, and study source index c carries on from there") {
		t.Errorf("IndexSources with a plugin that was killed = %v (%s)", err, core.CodeOf(err))
	}
	if took := time.Since(began); took > 8*time.Second {
		t.Errorf("the command took %s to notice that its plugin was gone", took)
	}
	s.stopped(t)
	if got := states(s.sources(t)); !slices.Equal(got, []string{"indexed", "not_indexed"}) {
		t.Errorf("afterwards the Sources are %v", got)
	}
}

// A service goes on indexing when a connection closes, so stopping the
// command has to tell it: with a context of the command's own, since the one
// that was cancelled asks a plugin nothing.
func TestStoppingTellsAServiceToStop(t *testing.T) {
	for name, deaf := range map[string]bool{"a service that stops": false, "a service that does not answer": true} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			var mu sync.Mutex
			var calls []plugintest.Call
			var silenced atomic.Bool
			release := make(chan struct{})
			service := plugintest.New(plugintest.Config{Folder: t.TempDir(), PageDelay: 25 * time.Millisecond,
				Asked: func(tool string, args json.RawMessage) {
					mu.Lock()
					defer mu.Unlock()
					calls = append(calls, plugintest.Call{Tool: tool, Args: slices.Clone(args)})
				},
				Before: func(ctx context.Context, tool string) {
					if tool == "cancel" && silenced.Load() {
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
				}})
			server := httptest.NewServer(service.Handler())
			t.Cleanup(func() {
				close(release)
				server.Close()
				service.Close()
			})
			silenced.Store(deaf)
			c.RegisterURL(t, "remote", server.URL+"/mcp")
			k := studyOn(t, c, "", core.PluginOptions{CancelTimeout: 400 * time.Millisecond})
			namedTopic(t, k, "c", "remote")
			long := addBook(t, k, "c", filepath.Join(c.Home, "Books", "long.txt"), pages(400, "two"))

			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			began := time.Now()
			_, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c", ProgressEvery: time.Millisecond, Progress: func(p core.IndexProgress) {
				if p.State == core.ProgressRunning && p.Progress != nil && *p.Progress > 0 {
					stop()
				}
			}})
			if core.CodeOf(err) != core.CodeCanceled {
				t.Fatalf("IndexSources, stopped = %v (%s); want canceled", err, core.CodeOf(err))
			}
			if took := time.Since(began); took > 8*time.Second {
				t.Errorf("the stopped command took %s", took)
			}
			mu.Lock()
			told := sourcesOf(asked(calls, "cancel"))
			mu.Unlock()
			if !slices.Equal(told, []string{long.ID}) {
				t.Fatalf("the service was told to stop %v, want the Source it was indexing", told)
			}
			if deaf {
				if !strings.Contains(err.Error(), "study could not tell Knowledge base plugin remote to stop indexing Source "+long.ID) ||
					!strings.Contains(err.Error(), "the service may still be indexing it") {
					t.Errorf("the error does not say that the service may still be indexing: %v", err)
				}
				return
			}
			if !strings.Contains(err.Error(), "Knowledge base plugin remote was told to stop indexing Source "+long.ID+", and stopped") {
				t.Errorf("the error does not say that the service stopped: %v", err)
			}
			// The service is still there, and is indexing nothing.
			list, err := k.ListSources(context.Background(), "c")
			if err != nil || !slices.Equal(states(list), []string{"not_indexed"}) {
				t.Errorf("after the stop the service says %v, %v", states(list), err)
			}
		})
	}
}

// While a service indexes a Source, whoever asks sees it being indexed, and
// how far: that is no reason to run the command.
func TestASourceBeingIndexedIsShownSo(t *testing.T) {
	c := plugintest.NewComputer(t)
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), PageDelay: 25 * time.Millisecond})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	c.RegisterURL(t, "remote", server.URL+"/mcp")
	k := studyOn(t, c, "", core.PluginOptions{})
	namedTopic(t, k, "c", "remote")
	addBook(t, k, "c", filepath.Join(c.Home, "Books", "long.txt"), pages(400, "two"))
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	seen := make(chan core.SourceList, 1)
	var once sync.Once
	_, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c", ProgressEvery: time.Millisecond, Progress: func(p core.IndexProgress) {
		if p.State == core.ProgressRunning && p.Progress != nil && *p.Progress > 0 {
			once.Do(func() {
				list, err := k.ListSources(context.Background(), "c")
				if err != nil {
					t.Errorf("ListSources while indexing: %v", err)
				}
				seen <- list
				stop()
			})
		}
	}})
	if core.CodeOf(err) != core.CodeCanceled {
		t.Fatalf("IndexSources = %v", err)
	}
	list := <-seen
	if got := list.Sources[0].Indexed; got.State != core.IndexIndexing || got.Progress == nil || *got.Progress <= 0 || *got.Progress >= 1 ||
		list.Indexing.Action != "" || list.Indexing.Problem != "" {
		t.Errorf("a Source being indexed = %+v, %+v", got, list.Indexing)
	}
}

// hostile is text a plugin would write to have the learner's terminal act
// on it: an escape that sets the window's title, one that clears the
// screen, a mark that turns the direction of what follows, and a new line.
var hostile = "\x1b]0;gotcha\x07\x1b[2J" + string(rune(0x202e)) + "\nsecond line "

// What a plugin writes is not trusted with the terminal, and is not repeated
// at any length: a failure's reason reaches status and a Topic's Sources on
// one line, with nothing a terminal would act on, and cut short. So does
// what is wrong with a plugin.
func TestWhatAPluginSaysIsCleanedAndCutShort(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	// A service, which answers whatever the test has it answer: a command's
	// arguments, which the registry holds, could not carry such text.
	var mu sync.Mutex
	answers := map[string]string{}
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Twist: func(tool string, answer []byte, _ bool) []byte {
		mu.Lock()
		defer mu.Unlock()
		if set, ok := answers[tool]; ok {
			return []byte(set)
		}
		return answer
	}})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	c.RegisterURL(t, "kb", server.URL+"/mcp")
	k := studyOn(t, c, "", core.PluginOptions{})
	namedTopic(t, k, "c", "kb")
	book := addBook(t, k, "c", filepath.Join(c.Home, "Books", "book.txt"), pages(2, "one"))
	answer := func(tool string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		answers[tool] = string(data)
		mu.Unlock()
	}
	answer("status", map[string]any{"sources": []any{map[string]any{"id": book.ID, "state": "failed",
		"reason": "no text layer" + hostile + strings.Repeat("long ", 5000)}}})

	safe := func(what, text, begins string, most int) {
		t.Helper()
		if strings.ContainsAny(text, "\x1b\x07\n") || strings.ContainsRune(text, 0x202e) {
			t.Errorf("%s holds what a terminal would act on: %q", what, text)
		}
		if len(text) > most {
			t.Errorf("%s is %d bytes long, want %d at most", what, len(text), most)
		}
		if !strings.HasPrefix(text, begins) {
			t.Errorf("%s = %.80q, want it to begin with %q", what, text, begins)
		}
	}
	list, err := k.ListSources(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	safe("a Source's reason", list.Sources[0].Indexed.Reason, "no text layer", 310)
	status, err := k.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	safe("a Source's reason in status", status.Topics[0].Indexing.Sources[0].Reason, "no text layer", 310)
	// study source index gives the reason whole, as far as study keeps one.
	res, err := k.IndexSources(ctx, core.IndexSpec{Topic: "c", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	safe("a Source's reason for study source index", res.Sources[0].Reason, "no text layer", 2100)
	if len(res.Sources[0].Reason) < 1500 {
		t.Errorf("study source index cut the reason to %d bytes", len(res.Sources[0].Reason))
	}
	for what, v := range map[string]any{"sources": list, "status": status, "index": res} {
		data, err := json.Marshal(v)
		if err != nil || !json.Valid(data) || len(data) > 8<<10 {
			t.Errorf("%s as JSON: %d bytes, %v", what, len(data), err)
		}
	}

	// What is wrong with a plugin is said in a few lines' worth as well: one
	// that answers its contract tool with something else, at length.
	answer("contract", map[string]any{"version": 1, "sources": []any{"file" + hostile + strings.Repeat("x", 100000)},
		"search": []any{"keyword"}, "locations": []any{}})
	if list, err = k.ListSources(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	safe("what is wrong with the plugin", list.Indexing.Problem, "Knowledge base plugin kb does not speak Lamplight's contract", 610)
	if list.Sources[0].Indexed.State != core.IndexUnknown || list.Indexing.Action != "study knowledge-base check kb" {
		t.Errorf("ListSources = %+v, %+v", list.Sources[0].Indexed, list.Indexing)
	}
}

// A Topic can name a plugin this computer does not have: another of the
// learner's computers may. The name is recorded, the update says what is
// missing here, and the Topic works as one without a Knowledge base.
func TestAnUnregisteredNameIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := newShelf(t)
	named := &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin, Plugin: "shelf"}
	for _, dry := range []bool{true, false} {
		res, err := s.k.UpdateTopic(ctx, "c", core.TopicChanges{KnowledgeBase: named, DryRun: dry})
		want := "no Knowledge base plugin named shelf is registered on this computer. Until that is put right, c works on this computer " +
			"as a Topic without a Knowledge base. Run: study knowledge-base add shelf -- <command>"
		if err != nil || !res.Changed || !reflect.DeepEqual(res.Topic.KnowledgeBase, named) || res.Note != want {
			t.Errorf("dry run %v: UpdateTopic = %+v, note %q, %v\nwant the name recorded, and the note %q", dry, res.Topic.KnowledgeBase, res.Note, err, want)
		}
	}
	if got := readFile(t, filepath.Join(s.StudyHome, "c"), "topic.toml"); !strings.Contains(got, `plugin = "shelf"`) {
		t.Errorf("topic.toml:\n%s", got)
	}
	book := s.book(t, "book.txt", pages(2, "one"))
	list := s.sources(t)
	if want := (&core.Indexing{Plugin: "shelf", Problem: "no Knowledge base plugin named shelf is registered on this computer",
		Action: "study knowledge-base add shelf -- <command>"}); !reflect.DeepEqual(list.Indexing, want) ||
		!slices.Equal(states(list), []string{"unknown"}) || list.Sources[0].Path == "" {
		t.Errorf("ListSources = %+v, states %v; want %+v", list.Indexing, states(list), want)
	}
	// Teaching goes on as without a Knowledge base: Evidence is recorded
	// from the Source the agent read itself.
	if _, err := s.k.RecordEvidence(ctx, core.EvidenceSpec{Topic: "c", Lesson: "intro", Source: book.ID, Quote: "Page 1 speaks of one."}); err != nil {
		t.Errorf("RecordEvidence on a Topic whose plugin is missing: %v", err)
	}
	// Naming a plugin this computer has says nothing more, and started none.
	res, err := s.k.UpdateTopic(ctx, "c", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin, Plugin: "kb"}})
	if err != nil || res.Note != "" || !res.Changed {
		t.Errorf("naming a registered plugin = %+v, note %q, %v", res.Topic.KnowledgeBase, res.Note, err)
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("naming a plugin started it")
	}
	// And back to none.
	res, err = s.k.UpdateTopic(ctx, "c", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBaseNone}})
	if err != nil || res.Note != "" || res.Topic.KnowledgeBase.Plugin != "" || s.sources(t).Indexing != nil {
		t.Errorf("going back to none = %+v, note %q, %v", res.Topic.KnowledgeBase, res.Note, err)
	}
}

// Inside a sandbox that lets study write in the Study home alone, a
// plugin's folder cannot be made. The error gives both ways out: allow the
// folder, or run the command in a shell of one's own.
func TestIndexingWhereThePluginsFolderCannotBeMade(t *testing.T) {
	s := newShelf(t)
	s.book(t, "book.txt", pages(2, "one"))
	state := filepath.Join(s.Home, ".local", "state", "lamplight")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root may write in every folder, whatever its mode")
	}
	_, err := s.k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"})
	if core.CodeOf(err) != core.CodeFailedPrecondition {
		t.Fatalf("IndexSources = %v (%s); want failed_precondition", err, core.CodeOf(err))
	}
	for _, want := range []string{"study cannot make the folder Knowledge base plugin kb keeps its index in, " + s.DataFolder("kb"),
		"either allow that folder in the sandbox, or run the command in a shell of your own"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("the plugin was started without a folder")
	}
	// The Topic's Sources are still listed, with the same advice a command
	// away.
	list := s.sources(t)
	if !strings.Contains(list.Indexing.Problem, "study cannot make the folder") || list.Indexing.Action != "study knowledge-base check kb" {
		t.Errorf("ListSources = %+v", list.Indexing)
	}
}

// A command that was stopped says so, wherever it was.
func TestAskingWhatIsIndexedCanBeStopped(t *testing.T) {
	s := newShelf(t, "--hang", "status")
	s.book(t, "book.txt", pages(2, "one"))
	for name, ask := range map[string]func(ctx context.Context) error{
		"ListSources": func(ctx context.Context) error { _, err := s.k.ListSources(ctx, "c"); return err },
		"Status":      func(ctx context.Context) error { _, err := s.k.Status(ctx); return err },
	} {
		ctx, stop := context.WithCancel(context.Background())
		time.AfterFunc(150*time.Millisecond, stop)
		began := time.Now()
		if err := ask(ctx); core.CodeOf(err) != core.CodeCanceled {
			t.Errorf("%s, stopped = %v (%s); want canceled", name, err, core.CodeOf(err))
		}
		if took := time.Since(began); took > busy {
			t.Errorf("%s took %s to end once it was stopped, with %s for the plugin to answer", name, took, patient)
		}
		stop()
		s.stopped(t)
	}
}
