package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// collectionsAsked are the collections the plugin was asked its status tool
// about, in the order it was asked.
func collectionsAsked(t *testing.T, calls []plugintest.Call) []string {
	t.Helper()
	var out []string
	for _, call := range asked(calls, "status") {
		var in struct {
			Collection string `json:"collection"`
		}
		if err := json.Unmarshal(call.Args, &in); err != nil {
			t.Fatal(err)
		}
		out = append(out, in.Collection)
	}
	return out
}

// indexingOf is what status says of one Topic's indexing.
func indexingOf(t *testing.T, k *core.Core, topic string) *core.Indexing {
	t.Helper()
	status, err := k.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, found := range status.Topics {
		if found.ID == topic {
			return found.Indexing
		}
	}
	t.Fatalf("status lists no Topic %s", topic)
	return nil
}

// status asks a plugin about the Topics the learner is working on. A Topic
// that is paused or finished has nothing to do next, indexing included, and
// its plugin is not asked about it; a listing of its Sources still asks.
func TestStatusAsksOnlyAboutActiveTopics(t *testing.T) {
	s := newShelf(t)
	s.book(t, "book.txt", pages(2, "one"))
	namedTopic(t, s.k, "d", "kb")
	addBook(t, s.k, "d", filepath.Join(s.Home, "Books", "other.txt"), pages(2, "two"))
	for _, state := range []string{core.TopicPaused, core.TopicFinished} {
		if _, err := s.k.UpdateTopic(context.Background(), "d", core.TopicChanges{State: &state}); err != nil {
			t.Fatal(err)
		}
		before := len(s.calls(t))
		if x := indexingOf(t, s.k, "d"); x != nil {
			t.Errorf("status says of a %s Topic's indexing: %+v", state, x)
		}
		if x := indexingOf(t, s.k, "c"); x == nil || x.Action != "study source index c" {
			t.Errorf("status says of the active Topic's indexing: %+v", x)
		}
		for _, collection := range collectionsAsked(t, s.calls(t)[before:]) {
			if !strings.HasPrefix(collection, "c-") {
				t.Errorf("status asked the plugin about %s, the collection of a %s Topic", collection, state)
			}
		}
		list, err := s.k.ListSources(context.Background(), "d")
		if err != nil || len(list.Sources) != 1 || list.Sources[0].Indexed == nil || list.Sources[0].Indexed.State != core.IndexNotIndexed {
			t.Errorf("the Sources of a %s Topic = %+v, %v; want its plugin asked", state, list.Sources, err)
		}
	}
	s.stopped(t)
}

// The Topics that name one plugin share its time, and the Active topic is
// asked about first: it is the one the learner is on.
func TestTheActiveTopicIsAskedAboutFirst(t *testing.T) {
	s := newShelf(t)
	s.book(t, "book.txt", pages(2, "one"))
	// z is made last, so it is the Topic worked on most recently, and the
	// last by its id.
	namedTopic(t, s.k, "z", "kb")
	addBook(t, s.k, "z", filepath.Join(s.Home, "Books", "z.txt"), pages(2, "two"))
	status, err := s.k.Status(context.Background())
	if err != nil || status.ActiveTopic == nil || status.ActiveTopic.ID != "z" {
		t.Fatalf("the Active topic = %+v, %v; want z", status.ActiveTopic, err)
	}
	got := collectionsAsked(t, s.calls(t))
	if len(got) != 2 || !strings.HasPrefix(got[0], "z-") || !strings.HasPrefix(got[1], "c-") {
		t.Errorf("the plugin was asked about %v; want the Active topic's collection first", got)
	}
}

// A plugin that answered about one Topic works. A Topic the shared time did
// not reach says so, and names the command that asks about it alone: the
// one that says what is wrong with a plugin would say that nothing is.
func TestATopicThePluginsTimeDidNotReachSaysSo(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record, "--delay", "status=3s")...)
	k := studyOn(t, c, "", core.PluginOptions{GlanceTimeout: 5 * time.Second})
	for _, topic := range []string{"a", "b"} {
		namedTopic(t, k, topic, "kb")
		addBook(t, k, topic, filepath.Join(c.Home, "Books", topic+".txt"), pages(2, topic))
	}
	began := time.Now()
	status, err := k.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > busy {
		t.Errorf("status took %s with a plugin that has 5s", took)
	}
	// b was made last, so it is the Active topic, and asked about first.
	a, b := status.Topics[0].Indexing, status.Topics[1].Indexing
	if b == nil || b.Problem != "" || b.Action != "study source index b" {
		t.Errorf("the Topic the plugin answered about: %+v", b)
	}
	if a == nil || !strings.Contains(a.Problem, "they were over before it said so of this one") || a.Action != "study source list a" ||
		len(a.Sources) != 1 || a.Sources[0].State != core.IndexUnknown {
		t.Errorf("the Topic the time did not reach: %+v; want it to say so, and to name the command that asks about it alone", a)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, is still running", started.PID)
	}
}

// A plugin that was only asked what it has indexed has nothing to finish:
// one that stays when its input closes is given a quarter of a second, and
// again once it is signalled, whatever a plugin that indexes is given.
func TestAGlanceGivesAPluginLittleTimeToLeave(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record, "--linger")...)
	k := studyOn(t, c, "", core.PluginOptions{StopGrace: 15 * time.Second})
	namedTopic(t, k, "c", "kb")
	addBook(t, k, "c", filepath.Join(c.Home, "Books", "book.txt"), pages(2, "one"))
	began := time.Now()
	x := indexingOf(t, k, "c")
	if took := time.Since(began); took > busy {
		t.Errorf("status took %s over a plugin that stays when its input closes: it is given %s to leave, twice", took, core.PluginGlanceGrace)
	}
	if x == nil || x.Problem != "" || len(x.Sources) != 1 || x.Sources[0].State != core.IndexNotIndexed {
		t.Errorf("status says of the Topic's indexing %+v; want the plugin's answer", x)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, is still running", started.PID)
	}
}

// Stopped between two Sources, study source index has the plugin indexing
// nothing: the Source that came to its end is not one to tell it to stop.
func TestStoppedBetweenTwoSourcesNothingIsToldToStop(t *testing.T) {
	s := newShelf(t)
	first := s.book(t, "first.txt", pages(2, "one"))
	s.book(t, "second.txt", pages(2, "two"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := s.k.IndexSources(ctx, core.IndexSpec{Topic: "c", Progress: func(p core.IndexProgress) {
		if p.Source == first.ID && p.State == core.ProgressFinished {
			cancel()
		}
	}})
	if core.CodeOf(err) != core.CodeCanceled || !strings.Contains(err.Error(), "The plugin was indexing nothing for it") {
		t.Errorf("IndexSources stopped between two Sources = %v (%s)", err, core.CodeOf(err))
	}
	if told := asked(s.calls(t), "cancel"); len(told) != 0 {
		t.Errorf("the plugin was told to stop indexing %v, which had come to its end", sourcesOf(told))
	}
	if list := s.sources(t); !slices.Equal(states(list), []string{"indexed", "not_indexed"}) {
		t.Errorf("afterwards the Sources are %v", states(list))
	}
	s.stopped(t)
}

// topic.toml can be written by hand. A name there that is none a plugin can
// have names no plugin, also when what study shows of it, cleaned, is one:
// " kb" is not kb.
func TestANameThatOnlyLooksLikeOneNamesNoPlugin(t *testing.T) {
	for _, written := range []string{" kb", "kb ", `kb\n`, `kb\t`, `kb\u0001`} {
		s := newShelf(t)
		s.book(t, "book.txt", pages(2, "one"))
		path := filepath.Join(s.StudyHome, "c", "topic.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(string(data), `plugin = "kb"`, `plugin = "`+written+`"`, 1)
		if edited == string(data) {
			t.Fatalf("topic.toml names no plugin to change:\n%s", data)
		}
		if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		x := indexingOf(t, s.k, "c")
		if x == nil || !strings.Contains(x.Problem, "without a name a plugin can have") {
			t.Errorf("a Topic whose file names the plugin %q: status says %+v", written, x)
		}
		if _, err := s.k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"}); core.CodeOf(err) != core.CodeFailedPrecondition {
			t.Errorf("a Topic whose file names the plugin %q: IndexSources = %v (%s)", written, err, core.CodeOf(err))
		}
		if _, err := os.Stat(s.record); err == nil {
			t.Errorf("a Topic whose file names the plugin %q had kb started", written)
		}
		// Naming kb is then a change, and puts the file right.
		res, err := s.k.UpdateTopic(context.Background(), "c", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{
			Kind: core.KnowledgeBasePlugin, Plugin: "kb"}})
		if err != nil || !res.Changed {
			t.Errorf("naming kb where the file says %q: %+v, %v; want a change", written, res, err)
		}
		if x := indexingOf(t, s.k, "c"); x == nil || x.Problem != "" {
			t.Errorf("after naming kb, status says %+v", x)
		}
	}
}

// A file that is where it was found and cannot be opened is none to hand to
// a plugin: study source index would skip it, so it is not the next action.
func TestAFileThatCannotBeReadIsNotOneToIndex(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	s := newShelf(t)
	book := s.book(t, "book.txt", pages(2, "one"))
	path := filepath.Join(s.Home, "Books", "book.txt")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	list := s.sources(t)
	if got := list.Sources[0].Indexed; got == nil || got.State != core.IndexNotIndexed || got.Reason != "its file cannot be read" ||
		list.Indexing.Action != "" {
		t.Errorf("a Source whose file cannot be read: %+v, %+v", got, list.Indexing)
	}
	if x := indexingOf(t, s.k, "c"); x == nil || x.Action != "study source list c" {
		t.Errorf("status says of the Topic's indexing %+v; want the listing as the action", x)
	}
	res := s.index(t, core.IndexSpec{})
	if got := res.Sources[0]; got.ID != book.ID || got.Asked || !strings.Contains(got.Skipped, "cannot be read") {
		t.Errorf("IndexSources = %+v; want the file skipped", got)
	}
}

// What a plugin refuses is that Source's failure, and the run goes on. A
// plugin that answers its index tool with what is not the contract's ends
// the run: it would do the same for the next Source, and what it holds for
// this one is not known.
func TestAPluginThatAnswersIndexWrongEndsTheRun(t *testing.T) {
	s := newShelf(t, "--result", `index={"source": {"id": "book", "state": "sideways"}}`)
	s.book(t, "first.txt", pages(2, "one"))
	s.book(t, "second.txt", pages(2, "two"))
	_, err := s.k.IndexSources(context.Background(), core.IndexSpec{Topic: "c"})
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not speak Lamplight's contract") {
		t.Fatalf("IndexSources = %v (%s); want failed_precondition, since the plugin does not speak the contract", err, core.CodeOf(err))
	}
	if got := asked(s.calls(t), "index"); len(got) != 1 {
		t.Errorf("the plugin was asked to index %d Sources; want the run to end at the first", len(got))
	}
	s.stopped(t)
}
