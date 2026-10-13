package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// turnsTaken is how many searches of a plugin hold a turn now.
func turnsTaken(k *Core, plugin string) int {
	k.turnsMu.Lock()
	defer k.turnsMu.Unlock()
	return len(k.turns[plugin])
}

// No more than a few copies of one plugin search at a time: a search waits
// for its turn inside its own time, says so when the time ran out first,
// and starts no plugin then. Recording Evidence with a Passage asks the
// plugin too, and waits like a search.
func TestASearchWaitsForItsTurn(t *testing.T) {
	ctx := context.Background()
	m, src, found := quotedTopic(t)
	query := PassageQuery{Topic: "c", Query: "elimination"}
	spec := EvidenceSpec{Topic: "c", Lesson: "elimination", Source: src.ID, Quote: "Elimination produces an upper triangular system.",
		Passages: []string{found[0].ID}}
	far := time.Now().Add(time.Hour)

	// All turns but one are taken: a search takes the last, and gives it
	// back.
	var held []func()
	for range mostAtOnce - 1 {
		release, err := m.takeTurn(ctx, "kb", far)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	for range 2 {
		if res, err := m.SearchEvidence(ctx, query); err != nil || len(res.Passages) == 0 {
			t.Fatalf("with a turn left, SearchEvidence = %+v, %v", res, err)
		}
	}
	if got := turnsTaken(m.Core, "kb"); got != mostAtOnce-1 {
		t.Fatalf("after two searches %d turns are taken, want the %d this test holds", got, mostAtOnce-1)
	}

	// None is left: a search waits for all of its time, and is not started.
	release, err := m.takeTurn(ctx, "kb", far)
	if err != nil {
		t.Fatal(err)
	}
	held = append(held, release)
	m.plugins.EvidenceTimeout = 300 * time.Millisecond
	history := len(historyLines(t, filepath.Join(m.home, "c")))
	for name, ask := range map[string]func() error{
		"SearchEvidence": func() error { _, err := m.SearchEvidence(ctx, query); return err },
		"RecordEvidence": func() error { _, err := m.RecordEvidence(ctx, spec); return err },
	} {
		began := time.Now()
		err := ask()
		if CodeOf(err) != CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "Knowledge base plugin kb was busy with other searches for 300ms, so this one was not started") {
			t.Errorf("with no turn left, %s = %v (%s); want it to say that the plugin was busy", name, err, CodeOf(err))
		}
		if took := time.Since(began); took < 300*time.Millisecond {
			t.Errorf("%s gave up after %s; want it to wait for a turn for the time it has", name, took)
		}
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); len(after) != history {
		t.Errorf("Evidence that had no turn was recorded:\n%s", strings.Join(after[history:], "\n"))
	}
	// A command that is stopped while it waits says so, and not that the
	// plugin was busy.
	stopped, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = m.SearchEvidence(stopped, query)
	cancel()
	if CodeOf(err) != CodeBusy && CodeOf(err) != CodeCanceled || strings.Contains(err.Error(), "was busy with other searches") {
		t.Errorf("stopped while it waited, SearchEvidence = %v (%s)", err, CodeOf(err))
	}
	// Another plugin's turns are its own.
	if release, err := m.takeTurn(ctx, "other", time.Now().Add(50*time.Millisecond)); err != nil {
		t.Errorf("a turn of another plugin: %v", err)
	} else {
		release()
	}

	// A turn that is given back lets the next through. Giving it back
	// twice gives back one.
	held[0]()
	held[0]()
	if got := turnsTaken(m.Core, "kb"); got != mostAtOnce-1 {
		t.Errorf("after a turn was given back twice, %d are taken; want %d", got, mostAtOnce-1)
	}
	m.plugins.EvidenceTimeout = 20 * time.Second
	if res, err := m.RecordEvidence(ctx, spec); err != nil || !res.Evidence.Checked {
		t.Errorf("with a turn given back, RecordEvidence = %+v, %v", res, err)
	}
	for _, release := range held[1:] {
		release()
	}
	if got := turnsTaken(m.Core, "kb"); got != 0 {
		t.Errorf("%d turns are taken after all were given back", got)
	}
}

// A turn is given back however the search or the recording ends: were one
// kept, a few failures would leave the plugin busy for good.
func TestATurnIsGivenBackHoweverItEnds(t *testing.T) {
	ctx := context.Background()
	c, k := studyWithAPlugin(t, t0)
	for _, id := range []string{"c", "g"} {
		if _, err := k.CreateTopic(ctx, TopicSpec{Title: strings.ToUpper(id), ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	// Topic g names a plugin this computer does not have.
	if _, err := k.UpdateTopic(ctx, "g", TopicChanges{KnowledgeBase: plugin("ghost")}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.AddSource(ctx, SourceSpec{Topic: "g", File: writeBook(t, filepath.Join(c.Home, "Books", "g.txt"), twoPages)}); err != nil {
		t.Fatal(err)
	}
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), twoPages)})
	if err != nil {
		t.Fatal(err)
	}
	found := indexedPassages(t, k)
	query := PassageQuery{Topic: "c", Query: "elimination"}
	spec := EvidenceSpec{Topic: "c", Lesson: "elimination", Source: added.Source.ID, Passages: []string{found[0].ID}}
	quoting := func(quote string, passage string) func() error {
		return func() error {
			of := spec
			of.Quote, of.Passages = quote, []string{passage}
			_, err := k.RecordEvidence(ctx, of)
			return err
		}
	}
	searching := func(q PassageQuery) func() error {
		return func() error { _, err := k.SearchEvidence(ctx, q); return err }
	}
	for _, tc := range []struct {
		name string
		// args are the flags the plugin is registered with.
		args []string
		ask  func() error
		// want is the error's code, or nothing when there is to be none.
		want ErrorCode
	}{
		{"a search", nil, searching(query), ""},
		{"a quote that is recorded", nil, quoting("Elimination produces an upper triangular system.", found[0].ID), ""},
		{"a quote that is refused", nil, quoting("Elimination gives a triangle.", found[0].ID), CodeInvalidArgument},
		{"a Passage the plugin does not have", nil, quoting("Elimination produces", "0123456789abcdef01234567"), CodeNotFound},
		{"a plugin that is not registered", nil, searching(PassageQuery{Topic: "g", Query: "elimination"}), CodeFailedPrecondition},
		{"a plugin that does not start", []string{"--exit", "3"}, searching(query), CodeFailedPrecondition},
		{"a plugin that refuses the search", []string{"--refuse", "search=internal"}, searching(query), CodeFailedPrecondition},
		{"a plugin that is late", []string{"--hang", "search"}, searching(query), CodeFailedPrecondition},
		{"a plugin that is late with a Passage", []string{"--hang", "passage"},
			quoting("Elimination produces an upper triangular system.", found[0].ID), CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.Register(t, "kb", plugintest.Command(tc.args...)...)
			k.plugins.EvidenceTimeout = 20 * time.Second
			if len(tc.args) > 0 && tc.args[0] == "--hang" {
				k.plugins.EvidenceTimeout = 500 * time.Millisecond
			}
			// More times than there are turns.
			for range mostAtOnce + 1 {
				err := tc.ask()
				if CodeOf(err) != tc.want && (tc.want != "" || err != nil) || err != nil && strings.Contains(err.Error(), "was busy with other searches") {
					t.Fatalf("%v (%s); want %q, and never that the plugin was busy", err, CodeOf(err), tc.want)
				}
			}
			for _, name := range []string{"kb", "ghost"} {
				if got := turnsTaken(k, name); got != 0 {
					t.Errorf("%d turns of %s are still taken", got, name)
				}
			}
		})
	}
}

// stoppedOnceATurnIsTaken is a command that is stopped at the moment its
// turn comes: it reads as stopped as soon as a turn of the plugin is held.
type stoppedOnceATurnIsTaken struct {
	context.Context
	k *Core
}

func (c stoppedOnceATurnIsTaken) Err() error {
	if turnsTaken(c.k, "kb") > 0 {
		return context.Canceled
	}
	return nil
}

// A command that is stopped just as its turn comes gives the turn back: an
// MCP client cancels calls, and study mcp is one process for the whole of
// the agent's session.
func TestACommandStoppedAsItsTurnComesGivesItBack(t *testing.T) {
	m, src, found := quotedTopic(t)
	ctx := stoppedOnceATurnIsTaken{Context: context.Background(), k: m.Core}
	for range mostAtOnce + 1 {
		if _, err := m.SearchEvidence(ctx, PassageQuery{Topic: "c", Query: "elimination"}); CodeOf(err) != CodeCanceled {
			t.Fatalf("SearchEvidence, stopped as its turn came = %v (%s); want canceled", err, CodeOf(err))
		}
		_, err := m.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "elimination", Source: src.ID,
			Quote: "Elimination produces an upper triangular system.", Passages: []string{found[0].ID}})
		if CodeOf(err) != CodeCanceled {
			t.Fatalf("RecordEvidence, stopped as its turn came = %v (%s); want canceled", err, CodeOf(err))
		}
		if got := turnsTaken(m.Core, "kb"); got != 0 {
			t.Fatalf("%d turns are still taken after a command that was stopped", got)
		}
	}
	// And the plugin is not busy for the next one.
	if res, err := m.SearchEvidence(context.Background(), PassageQuery{Topic: "c", Query: "elimination"}); err != nil || len(res.Passages) == 0 {
		t.Errorf("the next SearchEvidence = %+v, %v", res, err)
	}
}

// Looking for the files of the Sources a plugin has not indexed is study's
// own work, inside the time a search has. Stopped there, or out of time
// there, a search says so: it does not answer as if nothing were indexed.
func TestASearchStoppedWhileItLooksForFilesSaysSo(t *testing.T) {
	_, k := studyWithAPlugin(t, t0)
	bg := context.Background()
	if _, err := k.CreateTopic(bg, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(bg, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	// A Source the plugin has not indexed: its file is looked for.
	sources := []Source{{ID: "book.id001", Title: "Book", Kind: SourceFile, Hash: "sha256:" + strings.Repeat("0", 64)}}

	t.Run("stopped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		p, err := k.startTopicPlugin(ctx, "c", "kb", "answer a search of the Topic's Sources", "Read them yourself.")
		if err != nil {
			t.Fatal(err)
		}
		defer p.stop()
		asked := 0
		held, leftOut, _, err := p.indexed(ctx, sources, func(context.Context, Source) string {
			asked++
			cancel()
			return ""
		})
		if asked != 1 || CodeOf(err) != CodeCanceled || held != nil || leftOut != nil {
			t.Errorf("indexed, stopped while the file was looked for (asked %d times) = %v, %v, %v (%s); want canceled", asked, held,
				leftOut, err, CodeOf(err))
		}
	})
	t.Run("out of time", func(t *testing.T) {
		k.plugins.EvidenceTimeout = 2 * time.Second
		defer func() { k.plugins.EvidenceTimeout = 20 * time.Second }()
		p, err := k.startTopicPlugin(bg, "c", "kb", "answer a search of the Topic's Sources", "Read them yourself.")
		if err != nil {
			t.Fatal(err)
		}
		defer p.stop()
		_, _, _, err = p.indexed(bg, sources, func(ctx context.Context, _ Source) string {
			// As a file that is hashed until the time is over.
			<-ctx.Done()
			return ""
		})
		if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "looking on this computer for the files of the Sources") ||
			!strings.Contains(err.Error(), "took what was left of the 2s study has to answer a search of the Topic's Sources") ||
			!strings.Contains(err.Error(), "study source list c") || !strings.Contains(err.Error(), "Read them yourself.") ||
			strings.Contains(err.Error(), "did not answer") {
			t.Errorf("indexed, out of time while the file was looked for = %v (%s); want it to say what took the time, and not that the plugin was late", err, CodeOf(err))
		}
	})
	if got := turnsTaken(k, "kb"); got != 0 {
		t.Errorf("%d turns are still taken", got)
	}
}
