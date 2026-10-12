package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// The plugin has given all it is asked for before the Topic is written: it
// is stopped, and its turn given back, before study waits for the Topic's
// lock. A plugin is never kept running by another process that writes the
// Topic.
func TestThePluginIsStoppedBeforeTheTopicIsWritten(t *testing.T) {
	ctx := context.Background()
	c, k := studyWithAPlugin(t, t0)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	if _, err := k.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), twoPages)})
	if err != nil {
		t.Fatal(err)
	}
	found := indexedPassages(t, k)
	for _, dry := range []bool{true, false} {
		reached := false
		k.crash = func(point string) error {
			if point != crashBeforeLock {
				return nil
			}
			reached = true
			if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 2*time.Second) {
				t.Errorf("dry run %v: the plugin, process %d, is still running when the Topic's lock is taken", dry, started.PID)
			}
			if got := turnsTaken(k, "kb"); got != 0 {
				t.Errorf("dry run %v: %d turns of the plugin are still taken when the Topic's lock is taken", dry, got)
			}
			return nil
		}
		res, err := k.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "elimination", Source: added.Source.ID,
			Quote: "Elimination produces an upper triangular system.", Passages: []string{found[0].ID}, DryRun: dry})
		k.crash = nil
		if err != nil || !res.Changed || !res.Evidence.Checked {
			t.Fatalf("RecordEvidence (dry run: %v) = %+v, %v", dry, res, err)
		}
		// A dry run takes no lock, and its plugin is stopped all the same.
		if reached == dry {
			t.Errorf("dry run %v: the Topic's lock was taken: %v", dry, reached)
		}
		if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
			plugintest.Kill(started.PID)
			t.Errorf("dry run %v: the plugin, process %d, is still running", dry, started.PID)
		}
	}
}

// Waiting for another process that writes the Topic has what is left of the
// plugin's time, and none of its own on top: Evidence with its Passage is
// recorded or refused within the one time it has.
func TestEvidenceWithAPassageWaitsForTheTopicInsideItsTime(t *testing.T) {
	ctx := context.Background()
	m, src, found := quotedTopic(t)
	root, err := os.OpenRoot(m.home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockTopic(ctx, root, "c")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	before := len(historyLines(t, filepath.Join(m.home, "c")))
	m.plugins.EvidenceTimeout = time.Second
	began := time.Now()
	_, err = m.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "elimination", Source: src.ID,
		Quote: "Elimination produces an upper triangular system.", Passages: []string{found[0].ID}})
	took := time.Since(began)
	if CodeOf(err) != CodeBusy || !strings.Contains(err.Error(), "writing to Topic c") {
		t.Errorf("RecordEvidence while another process writes the Topic = %v (%s); want busy", err, CodeOf(err))
	}
	// A write waits thirty seconds for a lock when nothing says otherwise.
	if took > lockWait/3 {
		t.Errorf("RecordEvidence waited %s for the Topic's lock; want it to give up when the second it had was over", took)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); len(after) != before {
		t.Errorf("the Evidence was recorded:\n%s", strings.Join(after[before:], "\n"))
	}
}

// The same quote from the same Passages is the same Evidence when the same
// plugin's Passages were what it was checked against. Checked against
// another plugin's, it is recorded again: what the first check says is what
// the first plugin held.
func TestTheSameQuoteCheckedAgainstAnotherPluginIsRecordedAgain(t *testing.T) {
	ctx := context.Background()
	c, k := studyWithAPlugin(t, t0)
	c.Register(t, "other", plugintest.Command()...)
	if _, err := k.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), twoPages)})
	if err != nil {
		t.Fatal(err)
	}
	spec := EvidenceSpec{Topic: "c", Lesson: "elimination", Source: added.Source.ID,
		Quote: "Elimination produces an upper triangular system."}
	record := func(named string, changes bool) Evidence {
		t.Helper()
		of := spec
		// Each plugin gives the Passage the id it has there: the test
		// plugin makes it of the Source and its content, so it is the same.
		of.Passages = []string{indexedPassages(t, k)[0].ID}
		res, err := k.RecordEvidence(ctx, of)
		if err != nil || res.Changed != changes || !res.Evidence.Checked || res.Evidence.Plugin != named {
			t.Fatalf("RecordEvidence with %s = %+v, %v; want changed %v, checked against %s", named, res, err, changes, named)
		}
		return res.Evidence
	}
	first := record("kb", true)
	if again := record("kb", false); again.ID != first.ID {
		t.Errorf("the same quote, checked against the same plugin, is Evidence %s and then %s", first.ID, again.ID)
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("other")}); err != nil {
		t.Fatal(err)
	}
	second := record("other", true)
	if second.ID == first.ID || !equalStrings(second.Passages, first.Passages) {
		t.Errorf("checked against another plugin, the quote is Evidence %s of Passages %v; the first is %s of %v", second.ID,
			second.Passages, first.ID, first.Passages)
	}
	if again := record("other", false); again.ID != second.ID {
		t.Errorf("the same quote, checked against the other plugin again, is Evidence %s and then %s", second.ID, again.ID)
	}
	list, err := k.ListEvidence(ctx, EvidenceQuery{Topic: "c"})
	if err != nil || len(list.Evidence) != 2 {
		t.Errorf("ListEvidence = %+v, %v; want the two pieces of Evidence", list, err)
	}
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00") && len(a) == len(b)
}

// A Topic this version cannot write is refused before its plugin is started:
// a History with an Event of a newer study takes no Evidence, and no program
// is run to find that out.
func TestATopicThatCannotBeWrittenStartsNoPlugin(t *testing.T) {
	ctx := context.Background()
	c, k := studyWithAPlugin(t, t0)
	record := filepath.Join(t.TempDir(), "record.json")
	if _, err := k.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), twoPages)})
	if err != nil {
		t.Fatal(err)
	}
	found := indexedPassages(t, k)
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	appendToHistory(t, filepath.Join(c.StudyHome, "c"), `{"format":2,"id":"zz","time":"2026-10-01T10:00:00Z","kind":"from the future"}`+"\n")
	for _, dry := range []bool{true, false} {
		_, err := k.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "elimination", Source: added.Source.ID,
			Quote: "Elimination produces an upper triangular system.", Passages: []string{found[0].ID}, DryRun: dry})
		if CodeOf(err) != CodeNewerFormat {
			t.Errorf("RecordEvidence (dry run: %v) = %v (%s); want newer_format", dry, err, CodeOf(err))
		}
	}
	if _, err := os.Stat(record); err == nil {
		t.Errorf("the plugin was started for a Topic that cannot be written: it was asked %v", plugintest.Tools(plugintest.ReadCalls(t, record)))
	}
	// A search writes nothing, and is answered.
	if res, err := k.SearchEvidence(ctx, PassageQuery{Topic: "c", Query: "elimination"}); err != nil || len(res.Passages) == 0 {
		t.Errorf("SearchEvidence of the Topic = %+v, %v; want it searched", res, err)
	}
}
