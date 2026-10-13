package core

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// twoPages is a book of two pages, which the test plugin makes two Passages
// of, one for each page.
const twoPages = "Elimination produces an upper triangular system.\f" +
	"Back substitution solves it from the last equation up.\n"

// studyWithAPlugin returns a learner's computer on which the test plugin is
// registered as kb, and a Core for it whose clock reads at. Nothing in it
// waits on a real deadline: a plugin has twenty seconds for everything.
func studyWithAPlugin(t *testing.T, at time.Time) (*plugintest.Computer, *Core) {
	t.Helper()
	c := plugintest.NewComputer(t)
	c.Register(t, "kb", plugintest.Command()...)
	k, err := Open(Options{
		Getenv: c.Getenv,
		Dir:    c.Home,
		Now:    func() time.Time { return at },
		Plugins: PluginOptions{Environ: func() []string { return c.Environ() }, StartTimeout: 20 * time.Second,
			GlanceTimeout: 20 * time.Second, EvidenceTimeout: 20 * time.Second, StopGrace: 300 * time.Millisecond,
			IndexPoll: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

// indexedPassages has the plugin index Topic c's Sources, and returns the
// Passages a search for the book's words finds, in the order of its pages.
func indexedPassages(t *testing.T, k *Core) []FoundPassage {
	t.Helper()
	ctx := context.Background()
	if _, err := k.IndexSources(ctx, IndexSpec{Topic: "c"}); err != nil {
		t.Fatal(err)
	}
	res, err := k.SearchEvidence(ctx, PassageQuery{Topic: "c", Query: "elimination produces an upper triangular system substitution"})
	if err != nil || len(res.Passages) != 2 || res.Passages[0].Location != "p. 1" || res.Passages[1].Location != "p. 2" {
		t.Fatalf("SearchEvidence = %+v, %v; want the book's two Passages, the first page's first", res, err)
	}
	return res.Passages
}

// quotedTopic returns a computer with the test plugin, a Topic c that names
// it, one Source, indexed, and the two Passages a search finds of it.
func quotedTopic(t *testing.T) (*machine, Source, []FoundPassage) {
	t.Helper()
	c, k := studyWithAPlugin(t, t0)
	ctx := context.Background()
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
	return &machine{Core: k, home: c.StudyHome}, added.Source, indexedPassages(t, k)
}

// eventsOfType counts the Events of one type in a Topic's History.
func eventsOfType(t *testing.T, dir, typ string) int {
	t.Helper()
	n := 0
	for _, line := range historyLines(t, dir) {
		if strings.Contains(line, `"type":"`+typ+`"`) {
			n++
		}
	}
	return n
}

// A crash at any point of recording Evidence with its Passage leaves
// something the next write finishes, and a dry run says what the real run
// then does. The Event edits no item, so the point after an item is
// replaced is never reached: the write goes through.
func TestCrashesWhileRecordingEvidenceWithItsPassage(t *testing.T) {
	ctx := context.Background()
	const quote = "Elimination produces an upper triangular system."
	check := func(t *testing.T, m *machine, passage string) {
		t.Helper()
		dir := filepath.Join(m.home, "c")
		if hasIntentFile(t, m) {
			t.Error("the intent marker survived the next write")
		}
		if s := replayFolder(t, dir); len(s.flags) != 0 {
			t.Errorf("flags = %+v", s.flags)
		}
		got := m.evidence(t, EvidenceQuery{})
		if len(got) != 1 || !got[0].Checked || got[0].Quote != quote || !slices.Equal(got[0].Passages, []string{passage}) ||
			got[0].Plugin != "kb" || got[0].Location != "p. 1" || got[0].LocationFrom != LocationFromKnowledgeBase {
			t.Errorf("Evidence = %+v; want the one quote, checked against its Passage", got)
		}
		if n := eventsOfType(t, dir, eventEvidencePassagesRecorded); n != 1 {
			t.Errorf("the History holds %d %s Events, want 1", n, eventEvidencePassagesRecorded)
		}
	}
	for _, tc := range []struct {
		point string
		// stops says that the write is interrupted there, and recorded
		// that its Event is in the History by then.
		stops, recorded bool
	}{
		{crashBeforeLock, true, false},
		{crashAfterIntent, true, false},
		{crashAfterEvent, true, true},
		{crashAfterItem, false, true},
		{crashBeforeClear, true, true},
	} {
		t.Run(tc.point, func(t *testing.T) {
			m, src, found := quotedTopic(t)
			spec := EvidenceSpec{Topic: "c", Lesson: "elimination", Source: src.ID, Quote: quote, Passages: []string{found[0].ID}}
			m.crash = crashOnce(tc.point)
			_, err := m.RecordEvidence(ctx, spec)
			m.crash = nil
			if tc.stops != errors.Is(err, errCrash) || !tc.stops && err != nil {
				t.Fatalf("RecordEvidence = %v; want the crash: %v", err, tc.stops)
			}
			if got := len(m.evidence(t, EvidenceQuery{})); (got == 1) != tc.recorded {
				t.Fatalf("after the crash the History holds %d pieces of Evidence; want it recorded: %v", got, tc.recorded)
			}
			dry := spec
			dry.DryRun = true
			would, dryErr := m.RecordEvidence(ctx, dry)
			did, realErr := m.RecordEvidence(ctx, spec)
			if dryErr != nil || realErr != nil || would.Changed != did.Changed || did.Changed == tc.recorded {
				t.Errorf("the dry run said (%v, %v) and the real run did (%v, %v); want both to record it: %v", would.Changed, dryErr,
					did.Changed, realErr, !tc.recorded)
			}
			check(t, m, found[0].ID)
		})
	}
	// The write is interrupted once the Event is in the History, and the
	// recovery that finishes it is interrupted too. It has no item to
	// replace either, so only its last point is reached.
	for _, tc := range []struct {
		point string
		stops bool
	}{{crashRecoveryAfterItem, false}, {crashRecoveryBeforeClear, true}} {
		t.Run(tc.point, func(t *testing.T) {
			m, src, found := quotedTopic(t)
			spec := EvidenceSpec{Topic: "c", Lesson: "elimination", Source: src.ID, Quote: quote, Passages: []string{found[0].ID}}
			m.crash = crashOnce(crashAfterEvent)
			if _, err := m.RecordEvidence(ctx, spec); !errors.Is(err, errCrash) {
				t.Fatalf("err = %v, want the crash", err)
			}
			m.crash = crashOnce(tc.point)
			_, err := m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("G")})
			if tc.stops != errors.Is(err, errCrash) || !tc.stops && err != nil {
				t.Fatalf("the next write = %v; want the crash during recovery: %v", err, tc.stops)
			}
			if tc.stops && !hasIntentFile(t, m) {
				t.Fatal("a crash during recovery cleared the marker")
			}
			m.crash = nil
			m.update(t, TopicChanges{Goal: ptr("G")})
			check(t, m, found[0].ID)
		})
	}
}

// A History with both kinds of Evidence replays: what was checked and what
// was not, each with what it holds, in whatever order their lines are, and
// one retraction takes back either.
func TestAHistoryWithBothKindsOfEvidenceReplays(t *testing.T) {
	ctx := context.Background()
	m, src, found := quotedTopic(t)
	dir := filepath.Join(m.home, "c")
	plain := m.recordEvidence(t, EvidenceSpec{Lesson: "elimination", Source: src.ID, Quote: "A sentence nobody checked.",
		Location: "p. 46", LocationFrom: LocationFromSource}).Evidence
	one := m.recordEvidence(t, EvidenceSpec{Lesson: "elimination", Source: src.ID,
		Quote: "Elimination produces an upper triangular system.", Passages: []string{found[0].ID}}).Evidence
	two := m.recordEvidence(t, EvidenceSpec{Lesson: "substitution", Source: src.ID,
		Quote: "triangular system. Back substitution solves it", Passages: []string{found[0].ID, found[1].ID}}).Evidence
	if plain.Checked || !one.Checked || !two.Checked || two.Location != "pp. 1–2" {
		t.Fatalf("recorded %+v, %+v and %+v", plain, one, two)
	}
	want := []Evidence{plain, one, two}
	lines := historyLines(t, dir)
	for range 5 {
		s := replayLines(t, lines)
		if len(s.flags) != 0 || !reflect.DeepEqual(s.knowledge().evidence, want) {
			t.Fatalf("replayed flags %+v and Evidence %+v\nwant %+v", s.flags, s.knowledge().evidence, want)
		}
		rand.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	}
	// Each kind is its own Event type, with its own payload.
	if a, b := eventsOfType(t, dir, eventEvidenceRecorded), eventsOfType(t, dir, eventEvidencePassagesRecorded); a != 1 || b != 2 {
		t.Errorf("the History holds %d %s and %d %s Events, want 1 and 2", a, eventEvidenceRecorded, b, eventEvidencePassagesRecorded)
	}
	var payload map[string]any
	if err := json.Unmarshal(lastEvent(t, dir).Data, &payload); err != nil {
		t.Fatal(err)
	}
	if wantPayload := (map[string]any{"id": two.ID, "lesson": "substitution", "source": src.ID,
		"quote": "triangular system. Back substitution solves it", "passages": []any{found[0].ID, found[1].ID}, "plugin": "kb",
		"location": "pp. 1–2"}); !reflect.DeepEqual(payload, wantPayload) {
		t.Errorf("the payload is %v\nwant %v", payload, wantPayload)
	}
	// The History's own view names what happened, and none of its text.
	view, err := m.HistoryOf(ctx, "c", HistoryQuery{Type: "evidence"})
	if err != nil || len(view.Entries) != 3 || view.Entries[0].Summary != "Evidence recorded, checked against its Passages" ||
		view.Entries[0].Lesson != "substitution" || view.Entries[2].Summary != "Evidence recorded" {
		t.Errorf("HistoryOf = %+v, %v", view.Entries, err)
	}

	// A retraction takes back Evidence of either kind, and a Card cites
	// either.
	if _, err := m.AddCard(ctx, "c", CardSpec{Prompt: "What does elimination produce?", Answer: "An upper triangular system",
		Evidence: []string{one.ID, plain.ID}}); err != nil {
		t.Errorf("a Card citing both kinds of Evidence: %v", err)
	}
	for _, e := range []Evidence{one, plain} {
		if res, err := m.RetractEvidence(ctx, "c", e.ID, false); err != nil || !res.Changed || res.Evidence.Checked != e.Checked {
			t.Errorf("RetractEvidence(%s) = %+v, %v", e.ID, res, err)
		}
	}
	if left := m.evidence(t, EvidenceQuery{}); len(left) != 1 || left[0].ID != two.ID {
		t.Errorf("after the retractions the Evidence is %+v", left)
	}
	if s := replayFolder(t, dir); len(s.flags) != 0 {
		t.Errorf("flags = %+v", s.flags)
	}
}

// A line of the History can be written by hand, or by another version. An
// evidence_passages.recorded Event whose payload is not what this version
// writes is held and flagged, never shown: its ids and its plugin's name
// reach a terminal.
func TestEvidenceWithPassagesThatIsNotAsRecordedIsHeld(t *testing.T) {
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	dir := filepath.Join(m.home, "c")
	line := func(id string, data map[string]any) string {
		raw, err := json.Marshal(map[string]any{"format": 1, "id": id, "time": "2026-10-02T10:00:00Z", "wall": "2026-10-02T10:00:00Z",
			"type": eventEvidencePassagesRecorded, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	good := func() map[string]any {
		return map[string]any{"id": "e1", "lesson": "l", "source": src.ID, "quote": "q", "passages": []any{"7f3a9c12"}, "plugin": "kb"}
	}
	history := historyLines(t, dir)
	// The payload this version writes replays, with more Passages than a
	// quote may be recorded with now: that is a rule for whoever records.
	many := good()
	many["passages"] = []any{"p1", "p2", "p3", "p4", "p5", "p6", "p7"}
	if s := replayLines(t, append(slices.Clone(history), line("zz1", many))); len(s.flags) != 0 || len(s.knowledge().evidence) != 1 ||
		len(s.knowledge().evidence[0].Passages) != 7 || !s.knowledge().evidence[0].Checked || s.knowledge().evidence[0].LocationFrom != "" {
		t.Errorf("Evidence with seven Passages: flags %+v, Evidence %+v", s.flags, s.knowledge().evidence)
	}
	for name, change := range map[string]func(map[string]any){
		"no Passage":                           func(d map[string]any) { d["passages"] = []any{} },
		"no list of Passages":                  func(d map[string]any) { delete(d, "passages") },
		"a Passage's id a terminal interprets": func(d map[string]any) { d["passages"] = []any{"7f3a", "p\x1b[2J"} },
		"a Passage's id that is a path":        func(d map[string]any) { d["passages"] = []any{"../7f3a"} },
		"no plugin":                            func(d map[string]any) { delete(d, "plugin") },
		"a plugin's name that is none":         func(d map[string]any) { d["plugin"] = "sh -c 'curl example.com'" },
		"no quote":                             func(d map[string]any) { d["quote"] = "" },
		"no Lesson":                            func(d map[string]any) { delete(d, "lesson") },
		"Passages that are no list":            func(d map[string]any) { d["passages"] = "7f3a9c12" },
	} {
		data := good()
		change(data)
		s := replayLines(t, append(slices.Clone(history), line("zz1", data)))
		if kinds := flagKinds(s.flags); !slices.Equal(kinds, []string{FlagHeldEvent}) || len(s.knowledge().evidence) != 0 {
			t.Errorf("%s: flags %v, Evidence %+v; want the Event held, and no Evidence", name, kinds, s.knowledge().evidence)
		}
	}
	// Evidence whose Source has not arrived is held until it does.
	elsewhere := good()
	elsewhere["source"] = "another-book.a1b2c3"
	if s := replayLines(t, append(slices.Clone(history), line("zz1", elsewhere))); !slices.Equal(flagKinds(s.flags), []string{FlagHeldEvent}) {
		t.Errorf("Evidence before its Source: flags %+v", s.flags)
	}
	// The same id twice is one piece of Evidence, whichever type wrote it.
	twice := append(slices.Clone(history), line("zz1", good()),
		`{"format":1,"id":"zz2","time":"2026-10-02T10:01:00Z","wall":"2026-10-02T10:01:00Z","type":"evidence.recorded",`+
			`"data":{"id":"e1","lesson":"l","source":"`+src.ID+`","quote":"another"}}`+"\n")
	if s := replayLines(t, twice); len(s.flags) != 0 || len(s.knowledge().evidence) != 1 || !s.knowledge().evidence[0].Checked {
		t.Errorf("the same Evidence id in both types: flags %+v, Evidence %+v", s.flags, s.knowledge().evidence)
	}
}

// Two machines that each record Evidence, checked and not, do not conflict:
// their Histories merge by union, replay the same in both directions, and
// hold all of it. A Passage's id is one computer's, and is replayed as it
// was recorded on the other.
func TestTwoMachinesRecordingEvidenceDoNotConflict(t *testing.T) {
	ctx := context.Background()
	ca, a := studyWithAPlugin(t, t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(ca.StudyHome, "c")
	// The book is in the Topic, so every clone has it.
	added, err := a.AddSource(ctx, SourceSpec{Topic: "c", File: writeBook(t, filepath.Join(dirA, "notes", "book.txt"), twoPages)})
	if err != nil {
		t.Fatal(err)
	}
	src := added.Source
	// The second machine has a copy of the Topic, as a clone gives it, and
	// a plugin and an index of its own.
	cb, b := studyWithAPlugin(t, t0.Add(time.Minute))
	dirB := filepath.Join(cb.StudyHome, "c")
	if err := os.CopyFS(dirB, os.DirFS(dirA)); err != nil {
		t.Fatal(err)
	}
	onA, onB := indexedPassages(t, a), indexedPassages(t, b)

	record := func(k *Core, spec EvidenceSpec) Evidence {
		t.Helper()
		spec.Topic, spec.Source = "c", src.ID
		res, err := k.RecordEvidence(ctx, spec)
		if err != nil || !res.Changed {
			t.Fatalf("RecordEvidence(%+v) = %+v, %v", spec, res, err)
		}
		return res.Evidence
	}
	fromA := []Evidence{
		record(a, EvidenceSpec{Lesson: "elimination", Quote: "Elimination produces an upper triangular system.", Passages: []string{onA[0].ID}}),
		record(a, EvidenceSpec{Lesson: "elimination", Quote: "A sentence nobody checked."}),
	}
	fromB := []Evidence{
		record(b, EvidenceSpec{Lesson: "substitution", Quote: "Back substitution solves it", Passages: []string{onB[1].ID}}),
		// The same quote as on the first machine, checked here too.
		record(b, EvidenceSpec{Lesson: "elimination", Quote: "Elimination produces an upper triangular system.", Passages: []string{onB[0].ID}}),
	}

	ours, theirs := historyLines(t, dirA), historyLines(t, dirB)
	aIntoB, bIntoA := replayLines(t, unionMerge(theirs, ours)), replayLines(t, unionMerge(ours, theirs))
	if !reflect.DeepEqual(summarize(aIntoB), summarize(bIntoA)) || len(aIntoB.flags) != 0 {
		t.Fatalf("Evidence from two machines:\n A into B: %+v\n B into A: %+v", summarize(aIntoB), summarize(bIntoA))
	}
	// Under the one-machine-at-a-time contract the same quote recorded on
	// both is two pieces of Evidence, as it always was: nothing is flagged.
	want := append(slices.Clone(fromA), fromB...)
	if got := bIntoA.knowledge().evidence; !reflect.DeepEqual(got, want) || !reflect.DeepEqual(aIntoB.knowledge().evidence, want) {
		t.Errorf("merged Evidence = %+v\nwant %+v", got, want)
	}
	// Each machine reads the merged History, and writes to it.
	for name, k := range map[string]*Core{dirA: a, dirB: b} {
		if err := os.WriteFile(filepath.Join(name, historyFile), []byte(strings.Join(unionMerge(ours, theirs), "")), 0o644); err != nil {
			t.Fatal(err)
		}
		list, err := k.ListEvidence(ctx, EvidenceQuery{Topic: "c"})
		if err != nil || !reflect.DeepEqual(list.Evidence, want) {
			t.Errorf("%s: ListEvidence = %+v, %v", name, list.Evidence, err)
		}
		topic, err := k.readTopic("c")
		if err != nil || len(topic.Flags) != 0 {
			t.Errorf("%s: flags %+v, %v", name, topic.Flags, err)
		}
		if _, err := k.RetractEvidence(ctx, "c", fromB[1].ID, false); err != nil {
			t.Errorf("%s: retracting the other machine's Evidence: %v", name, err)
		}
	}
}
