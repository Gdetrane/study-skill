package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

// writeBook writes a file standing in for a book and returns its path.
func writeBook(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func (m *machine) addSource(t *testing.T, spec SourceSpec) Source {
	t.Helper()
	spec.Topic = "c"
	res, err := m.AddSource(context.Background(), spec)
	if err != nil {
		t.Fatalf("AddSource(%+v): %v", spec, err)
	}
	return res.Source
}

func (m *machine) recordEvidence(t *testing.T, spec EvidenceSpec) EvidenceResult {
	t.Helper()
	spec.Topic = "c"
	res, err := m.RecordEvidence(context.Background(), spec)
	if err != nil {
		t.Fatalf("RecordEvidence(%+v): %v", spec, err)
	}
	return res
}

func (m *machine) sources(t *testing.T) SourceList {
	t.Helper()
	list, err := m.ListSources(context.Background(), "c")
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	return list
}

func (m *machine) evidence(t *testing.T, q EvidenceQuery) []Evidence {
	t.Helper()
	q.Topic = "c"
	list, err := m.ListEvidence(context.Background(), q)
	if err != nil {
		t.Fatalf("ListEvidence(%+v): %v", q, err)
	}
	return list.Evidence
}

// appendLine appends text to the file at path.
func appendLine(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func readSourcesFile(t *testing.T, m *machine) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.home, "c", sourcesFile))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func TestKnowledgeWithoutAKnowledgeBase(t *testing.T) {
	m := newTopic(t)
	topic, err := m.readTopic("c")
	if err != nil || topic.KnowledgeBase != nil {
		t.Fatalf("a new Topic's Knowledge base = %+v, %v; want none chosen", topic.KnowledgeBase, err)
	}

	res := m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	if !res.Changed || res.Topic.KnowledgeBase == nil || *res.Topic.KnowledgeBase != (KnowledgeBase{Kind: KnowledgeBaseNone}) {
		t.Fatalf("choosing none = %+v", res)
	}
	if again := m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}}); again.Changed {
		t.Error("choosing the same Knowledge base again recorded a change")
	}
	if settings := readSettings(t, filepath.Join(m.home, "c")); settings.Title != "C" || knowledgeBaseOf(settings) == nil {
		t.Errorf("topic.toml = %+v", settings)
	}

	content := "%PDF-1.4 processes and threads"
	book := writeBook(t, filepath.Join(t.TempDir(), "operating_systems.pdf"), content)
	file := m.addSource(t, SourceSpec{File: book})
	if file.Kind != SourceFile || file.Title != "Operating Systems" || file.FileName != "operating_systems.pdf" ||
		file.TopicPath != "" || file.Size != int64(len(content)) || !strings.HasPrefix(file.Hash, "sha256:") ||
		!sourceIDPattern.MatchString(file.ID) || !strings.HasPrefix(file.ID, "operating-systems.") {
		t.Errorf("file Source = %+v", file)
	}
	if strings.Contains(readSourcesFile(t, m), filepath.Dir(book)) {
		t.Errorf("sources.jsonl holds this computer's path:\n%s", readSourcesFile(t, m))
	}
	page := m.addSource(t, SourceSpec{URL: "HTTPS://Go.dev/blog/context", Title: "Go Concurrency Patterns: Context"})
	if page.Kind != SourceURL || page.URL != "https://go.dev/blog/context" || page.FileName != "" || page.Hash != "" {
		t.Errorf("URL Source = %+v", page)
	}

	first := m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-01", Source: file.ID,
		Quote: "A process is a program in execution.\r\nThreads share its memory.", Location: "p. 42", LocationFrom: LocationFromSource})
	if !first.Changed || first.Evidence.ID == "" || !first.Evidence.Recorded.Equal(t0) ||
		first.Evidence.Quote != "A process is a program in execution.\nThreads share its memory." {
		t.Errorf("first Evidence = %+v", first)
	}
	again := m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-01", Source: file.ID,
		Quote: "A process is a program in execution.\nThreads share its memory.", Location: "p. 42", LocationFrom: LocationFromSource})
	if again.Changed || again.Evidence.ID != first.Evidence.ID {
		t.Errorf("recording the same Evidence twice = %+v, want the first, unchanged", again)
	}
	m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-02", Source: page.ID, Quote: "Context carries deadlines."})

	if all := m.evidence(t, EvidenceQuery{}); len(all) != 2 {
		t.Fatalf("ListEvidence = %+v", all)
	}
	if one := m.evidence(t, EvidenceQuery{Lesson: "lesson-02"}); len(one) != 1 || one[0].Source != page.ID {
		t.Errorf("ListEvidence(lesson-02) = %+v", one)
	}

	list := m.sources(t)
	if list.KnowledgeBase == nil || list.KnowledgeBase.Kind != KnowledgeBaseNone || len(list.Sources) != 2 ||
		list.Sources[0].State != SourceOK || list.Sources[0].Path != book || list.Sources[1].State != "" {
		t.Errorf("ListSources = %+v", list)
	}

	s := replayFolder(t, filepath.Join(m.home, "c"))
	if got := s.lessonsWithoutEvidence([]string{"lesson-01", "lesson-02", "lesson-03"}); !slices.Equal(got, []string{"lesson-03"}) {
		t.Errorf("lessons without Evidence = %v", got)
	}
	if topic, _ := m.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags = %+v", topic.Flags)
	}
	checkChain(t, filepath.Join(m.home, "c"))
}

// A Source's title can be changed, and asking for the title it has changes
// nothing. A location the Knowledge base gave is recorded as given.
func TestSourceTitlesAndKnowledgeBaseLocations(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	res := m.update(t, TopicChanges{Goal: ptr("Write a small shell"), KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	if !res.Changed || res.Topic.Goal != "Write a small shell" || res.Topic.KnowledgeBase == nil ||
		*res.Topic.KnowledgeBase != (KnowledgeBase{Kind: KnowledgeBaseNone}) {
		t.Fatalf("goal and Knowledge base = %+v", res)
	}

	book := writeBook(t, filepath.Join(t.TempDir(), "unix.pdf"), "%PDF-1.4 fork and exec")
	src := m.addSource(t, SourceSpec{File: book, Title: "The UNIX Programming Environment"})
	if src.Title != "The UNIX Programming Environment" {
		t.Errorf("Source = %+v", src)
	}
	update := func(changes SourceChanges) SourceResult {
		t.Helper()
		res, err := m.UpdateSource(ctx, "c", src.ID, changes)
		if err != nil {
			t.Fatalf("UpdateSource(%+v): %v", changes, err)
		}
		return res
	}
	if r := update(SourceChanges{Title: ptr("UNIX")}); !r.Changed || r.Source.Title != "UNIX" {
		t.Errorf("new title = %+v", r)
	}
	if r := update(SourceChanges{Title: ptr("UNIX")}); r.Changed {
		t.Error("the same title recorded a change")
	}
	if got := m.sources(t).Sources; len(got) != 1 || got[0].Title != "UNIX" || got[0].Path != book {
		t.Errorf("ListSources = %+v", got)
	}
	cited := m.recordEvidence(t, EvidenceSpec{Lesson: "processes", Source: src.ID, Quote: "fork creates a process.",
		Location: "citation 3", LocationFrom: LocationFromKnowledgeBase})
	if cited.Evidence.Location != "citation 3" || cited.Evidence.LocationFrom != LocationFromKnowledgeBase {
		t.Errorf("Evidence = %+v", cited.Evidence)
	}
	checkChain(t, filepath.Join(m.home, "c"))
}

func TestKnowledgeBaseValidation(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	for _, kb := range []KnowledgeBase{
		{},
		{Kind: "rag"},
		{Kind: "notebooklm"}, // a kind earlier builds had (ADR-0011)
		{Kind: "None"},
		{Kind: "no\xff"},
	} {
		if _, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &kb}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", kb, err)
		}
	}
	before := historyLines(t, filepath.Join(m.home, "c"))
	res, err := m.UpdateTopic(ctx, "c", TopicChanges{DryRun: true, Title: ptr("Systems C"),
		KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	if err != nil || !res.Changed || res.Topic.Title != "Systems C" || res.Topic.KnowledgeBase == nil ||
		res.Topic.KnowledgeBase.Kind != KnowledgeBaseNone {
		t.Errorf("dry run = %+v, %v", res, err)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); !slices.Equal(before, after) {
		t.Error("a dry run recorded Events")
	}
	if settings := readSettings(t, filepath.Join(m.home, "c")); settings.Title != "C" || knowledgeBaseOf(settings) != nil {
		t.Errorf("a dry run changed topic.toml: %+v", settings)
	}
}

// Keys of [knowledge_base] this version does not know stay while the table is
// left as it is: another setting's write keeps them, and choosing the kind
// the Topic has writes nothing. Choosing a kind over another writes the table
// anew, with the kind alone.
func TestKnowledgeBaseKeepsUnknownKeys(t *testing.T) {
	m := newTopic(t)
	none := &KnowledgeBase{Kind: KnowledgeBaseNone}
	m.update(t, TopicChanges{KnowledgeBase: none})
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// [knowledge_base] is the last table, so the key lands inside it.
	if err := os.WriteFile(path, append(data, "  account = \"ada@example.com\"\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Another setting rewrites topic.toml, and choosing the kind the Topic
	// has records nothing.
	m.update(t, TopicChanges{Goal: ptr("Write a small shell")})
	before := historyLines(t, filepath.Join(m.home, "c"))
	if again := m.update(t, TopicChanges{KnowledgeBase: none}); again.Changed {
		t.Error("choosing the same kind again recorded a change")
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); !slices.Equal(before, after) {
		t.Error("choosing the same kind again recorded an Event")
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "ada@example.com") {
		t.Errorf("the table was not left as it is:\n%s", data)
	}

	// A kind this version does not know, as a newer version would write it,
	// with its settings.
	newer := strings.Replace(string(data), `kind = "none"`, `kind = "rag"`, 1)
	if newer == string(data) {
		t.Fatalf("topic.toml has no kind to replace:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := m.update(t, TopicChanges{KnowledgeBase: none}); !res.Changed || *res.Topic.KnowledgeBase != *none {
		t.Errorf("choosing none over another kind = %+v", res)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "ada@example.com") || strings.Contains(string(data), "rag") {
		t.Errorf("choosing a kind over another kept the other's settings:\n%s", data)
	}
}

// A kind this version does not know, one a newer version added or one an
// earlier build had, is shown as topic.toml records it. The Topic works as
// one without a Knowledge base, and the kind cannot be chosen here.
func TestAnUnknownKnowledgeBaseKindIsShownAndBehavesAsNone(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, "[knowledge_base]\n  kind = \"rag\"\n  index = \"books\"\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || topic.KnowledgeBase == nil || topic.KnowledgeBase.Kind != "rag" || len(topic.Flags) != 0 {
		t.Fatalf("Topic = %+v, flags %+v, %v; want the kind as recorded and no flags", topic.KnowledgeBase, topic.Flags, err)
	}
	// As with none, Lessons are marked once the Topic has Sources, not before.
	if topic.LessonsWithoutEvidence != nil {
		t.Errorf("without Sources = %v, want nothing marked", topic.LessonsWithoutEvidence)
	}
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	if topic, err = m.readTopic("c"); err != nil || !slices.Equal(topic.LessonsWithoutEvidence, []string{"answer"}) {
		t.Errorf("with a Source = %v, %v; want [answer]", topic.LessonsWithoutEvidence, err)
	}
	m.recordEvidence(t, EvidenceSpec{Lesson: "answer", Source: src.ID, Quote: "the answer is 42"})
	if list := m.sources(t); list.KnowledgeBase == nil || list.KnowledgeBase.Kind != "rag" || len(list.Sources) != 1 {
		t.Errorf("ListSources = %+v", list)
	}
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: "rag"}}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("choosing the unknown kind: err = %v, want invalid_argument", err)
	}
	// Other settings can change, and the kind and its settings stay.
	m.update(t, TopicChanges{Goal: ptr("G")})
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `kind = "rag"`) || !strings.Contains(string(data), `index = "books"`) {
		t.Errorf("topic.toml after another change:\n%s", data)
	}
}

// earlierBuildTopic returns a machine whose Topic "c" is as an earlier build
// of v2 left it, when a Topic could have a NotebookLM Knowledge base and its
// Sources an id in the notebook (ADR-0011). Its History, topic.toml and
// sources.jsonl are that build's own output, kept in testdata/earlier-build:
// two Sources, one source.updated Event that changed only a NotebookLM id,
// one that changed a title too, and a Knowledge base moved to a second
// notebook.
func earlierBuildTopic(t *testing.T) *machine {
	t.Helper()
	m := newTopic(t)
	for _, name := range []string{historyFile, topicFile, sourcesFile} {
		data, err := os.ReadFile(filepath.Join("testdata", "earlier-build", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m.home, "c", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.setClock(time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)) // after the fixture's Events
	return m
}

// A Topic written by an earlier build still loads: nothing is corrupt or
// flagged, its Knowledge base kind is shown as recorded, and the NotebookLM
// ids in its Events are ignored.
func TestATopicOfAnEarlierBuildStillLoads(t *testing.T) {
	ctx := context.Background()
	m := earlierBuildTopic(t)
	dir := filepath.Join(m.home, "c")
	history, sources := historyLines(t, dir), readSourcesFile(t, m)

	topic, err := m.readTopic("c")
	if err != nil || len(topic.Flags) != 0 || topic.KnowledgeBase == nil || topic.KnowledgeBase.Kind != "notebooklm" {
		t.Fatalf("Topic = %+v, flags %+v, %v; want the kind as recorded and no flags", topic.KnowledgeBase, topic.Flags, err)
	}
	if status, err := m.Status(ctx); err != nil || len(status.Topics) != 1 || len(status.Topics[0].Flags) != 0 {
		t.Errorf("status = %+v, %v", status.Topics, err)
	}
	list := m.sources(t)
	if list.KnowledgeBase == nil || list.KnowledgeBase.Kind != "notebooklm" || len(list.Sources) != 2 {
		t.Fatalf("ListSources = %+v", list)
	}
	// The source.updated that changed only a NotebookLM id changed nothing;
	// the one that changed a title too changed the title.
	page, book := list.Sources[0], list.Sources[1]
	if page.Kind != SourceURL || page.Title != "https://example.com/a" || page.URL != "https://example.com/a" ||
		book.Kind != SourceFile || book.Title != "UNIX" || book.FileName != "unix.pdf" {
		t.Errorf("Sources = %+v", list.Sources)
	}
	if out, err := json.Marshal(list); err != nil || strings.Contains(string(out), "nlm-") || strings.Contains(string(out), "nb-4") {
		t.Errorf("ListSources shows an id of the notebook: %s, %v", out, err)
	}
	view, err := m.HistoryOf(ctx, "c", HistoryQuery{})
	if err != nil || len(view.Entries) != len(history) {
		t.Errorf("HistoryOf = %d entries, %v; want %d", len(view.Entries), err, len(history))
	}
	if !slices.Equal(historyLines(t, dir), history) || readSourcesFile(t, m) != sources {
		t.Error("reading the Topic changed it")
	}

	// The next change to a Source keeps, in its line, the fields this
	// version does not know, and records none of them.
	res, err := m.UpdateSource(ctx, "c", page.ID, SourceChanges{Title: ptr("Example")})
	if err != nil || !res.Changed || res.Source.Title != "Example" {
		t.Fatalf("UpdateSource = %+v, %v", res, err)
	}
	want := `{"id":"` + page.ID + `","kind":"url","title":"Example","url":"https://example.com/a","notebooklm_id":"nlm-2","notebooklm_notebook":"nb-42"}`
	if data := readSourcesFile(t, m); !strings.Contains(data, want+"\n") || strings.Count(data, page.ID) != 1 ||
		!strings.Contains(data, `"title":"UNIX"`) {
		t.Errorf("sources.jsonl after the change, want the line\n%s\nin\n%s", want, data)
	}
	after := historyLines(t, dir)
	if len(after) != len(history)+1 || strings.Contains(after[len(after)-1], "notebook") {
		t.Errorf("the change recorded %d Events, the last %s", len(after)-len(history), after[len(after)-1])
	}
	if got := m.sources(t).Sources[0]; got.Title != "Example" {
		t.Errorf("ListSources after the change = %+v", got)
	}

	// The retired kind cannot be chosen, and choosing none starts a new
	// table, without the notebook.
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: "notebooklm"}}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("choosing notebooklm: err = %v, want invalid_argument", err)
	}
	if res := m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}}); !res.Changed ||
		*res.Topic.KnowledgeBase != (KnowledgeBase{Kind: KnowledgeBaseNone}) {
		t.Errorf("choosing none = %+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, topicFile)); strings.Contains(string(data), "notebook") {
		t.Errorf("topic.toml still names the notebook:\n%s", data)
	}
	if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
		t.Errorf("flags after the changes: %+v, %v", topic.Flags, err)
	}
	checkChain(t, dir)
}

// earlierBuildEvent returns the id of the last Event of type typ in the
// Topic's History whose line contains text.
func earlierBuildEvent(t *testing.T, dir, typ, text string) string {
	t.Helper()
	found := ""
	for _, line := range historyLines(t, dir) {
		var ev struct{ ID, Type string }
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == typ && strings.Contains(line, text) {
			found = ev.ID
		}
	}
	if found == "" {
		t.Fatalf("the History has no %s Event containing %s", typ, text)
	}
	return found
}

// leaveIntent leaves the marker of a write to Topic "c" that stopped after
// recording the Event, before changing any content.
func (m *machine) leaveIntent(t *testing.T, eventID, typ string) {
	t.Helper()
	home, err := os.OpenRoot(m.home)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	if err := writeIntent(home, intent{Format: FormatVersion, Topic: "c", Event: eventID, Type: typ}); err != nil {
		t.Fatal(err)
	}
}

func (m *machine) hasIntent(t *testing.T) bool {
	t.Helper()
	home, err := os.OpenRoot(m.home)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	return hasIntent(home, "c")
}

// An interrupted Source change of an earlier build is finished by this one as
// that build would have finished it, when the Source in the Event's payload
// is the version the Event recorded, as it is here: the line is that Source,
// with its NotebookLM ids as fields this version does not know, like every
// other line the earlier build wrote.
func TestRecoveryFinishesAnEarlierBuildsWrite(t *testing.T) {
	m := earlierBuildTopic(t)
	dir := filepath.Join(m.home, "c")
	// The Event that named the book "UNIX" and gave it a NotebookLM id.
	interrupted := earlierBuildEvent(t, dir, eventSourceUpdated, `"title":"UNIX"`)
	// Take its change back out of sources.jsonl and leave its marker, as if
	// the earlier build had stopped between recording the Event and writing.
	sources := readSourcesFile(t, m)
	undone := strings.Replace(sources, `"title":"UNIX"`, `"title":"The UNIX Programming Environment"`, 1)
	undone = strings.Replace(undone, `,"notebooklm_id":"nlm-9","notebooklm_notebook":"nb-42"`, "", 1)
	if err := os.WriteFile(filepath.Join(dir, sourcesFile), []byte(undone), 0o644); err != nil {
		t.Fatal(err)
	}
	m.leaveIntent(t, interrupted, eventSourceUpdated)

	m.update(t, TopicChanges{Goal: ptr("G")}) // the next write finishes the interrupted one
	if data := readSourcesFile(t, m); data != sources {
		t.Errorf("recovery should leave sources.jsonl as the earlier build would have:\n%s\nwant\n%s\nlogs:\n%s", data, sources, m.logs)
	}
	if logs := m.logs.String(); !strings.Contains(logs, "finished an interrupted write") ||
		strings.Contains(logs, "writes differently") || m.hasIntent(t) {
		t.Errorf("the interrupted write should be finished as recorded; logs:\n%s", logs)
	}
	list := m.sources(t)
	if len(list.Sources) != 2 || list.Sources[1].Title != "UNIX" {
		t.Errorf("ListSources = %+v", list.Sources)
	}
	if out, err := json.Marshal(list); err != nil || strings.Contains(string(out), "nlm-") {
		t.Errorf("ListSources shows an id of the notebook: %s, %v", out, err)
	}
	if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
		t.Errorf("flags after recovery: %+v, %v", topic.Flags, err)
	}
}

// After this version finishes an earlier build's interrupted Source change
// that touched only a NotebookLM id, the next change to that Source is no
// conflict when the Source in the Event's payload is the version the Event
// recorded: only one machine ever wrote. Recovery must then leave the line at
// that version, not at the one it started from, or the next Event would
// change the Source from the same version a second time.
// TestRecoveryOfAnEarlierBuildsIDChangeOnAHandEditedLineCanFlagAConflict
// covers the case in which the payload's Source is not that version.
func TestAChangeAfterRecoveringAnEarlierBuildsIDChangeIsNoConflict(t *testing.T) {
	const page = `{"id":"example-com-a.qpm4lo","kind":"url","title":"%s","url":"https://example.com/a"%s}` + "\n"
	for _, tc := range []struct {
		name string
		// interrupt leaves the Topic as the earlier build would have, had it
		// stopped after recording the Event, and returns the Event's id.
		interrupt func(t *testing.T, m *machine, dir string) string
		// ids are the NotebookLM fields the line carries once the write is
		// finished.
		ids string
	}{
		{"an id changed", func(t *testing.T, m *machine, dir string) string {
			// The fixture's own Event, from nlm-1 to nlm-2: put the line back
			// as it was when the Source was added.
			sources := readSourcesFile(t, m)
			undone := strings.Replace(sources, `"notebooklm_id":"nlm-2"`, `"notebooklm_id":"nlm-1"`, 1)
			if undone == sources {
				t.Fatal("the fixture's line has no nlm-2 to undo")
			}
			if err := os.WriteFile(filepath.Join(dir, sourcesFile), []byte(undone), 0o644); err != nil {
				t.Fatal(err)
			}
			return earlierBuildEvent(t, dir, eventSourceUpdated, `"notebooklm_id":"nlm-2"`)
		}, `,"notebooklm_id":"nlm-2","notebooklm_notebook":"nb-42"`},
		{"an id removed", func(t *testing.T, m *machine, dir string) string {
			// The Event the earlier build recorded for
			// study source update --notebooklm-id "" on the fixture's Topic,
			// with the line still as that Event found it.
			removal, err := os.ReadFile(filepath.Join("testdata", "earlier-build", "id-removed.event.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			appendLine(t, filepath.Join(dir, historyFile), string(removal))
			return earlierBuildEvent(t, dir, eventSourceUpdated, `"notebooklm_id":""`)
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m := earlierBuildTopic(t)
			dir := filepath.Join(m.home, "c")
			m.leaveIntent(t, tc.interrupt(t, m, dir), eventSourceUpdated)

			res, err := m.UpdateSource(ctx, "c", "example-com-a.qpm4lo", SourceChanges{Title: ptr("Example")})
			if err != nil || !res.Changed {
				t.Fatalf("UpdateSource = %+v, %v", res, err)
			}
			topic, err := m.readTopic("c")
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range topic.Flags {
				t.Errorf("flag after one machine's recovery and one change: %s: %s", f.Kind, f.Message)
			}
			if data, want := readSourcesFile(t, m), fmt.Sprintf(page, "Example", tc.ids); !strings.HasPrefix(data, want) {
				t.Errorf("sources.jsonl after the change:\n%s\nwant its first line to be\n%s", data, want)
			}
			if logs := m.logs.String(); strings.Contains(logs, "writes differently") || m.hasIntent(t) {
				t.Errorf("the interrupted write should be finished as recorded; logs:\n%s", logs)
			}
		})
	}
}

// An earlier build that stopped after recording a source.added Event, before
// writing the line, is finished by this version as that build would have
// finished it: the line is the Source the Event recorded, with its NotebookLM
// ids as fields this version does not know. A later change to that Source
// keeps them and is no conflict.
func TestRecoveryFinishesAnEarlierBuildsAddedSource(t *testing.T) {
	ctx := context.Background()
	m := earlierBuildTopic(t)
	dir := filepath.Join(m.home, "c")
	// The Topic as it was while the fixture's first Source was being added:
	// the History up to that Event, the first notebook in topic.toml, and no
	// sources.jsonl yet.
	const added = `{"id":"example-com-a.qpm4lo","kind":"url","title":"https://example.com/a","url":"https://example.com/a","notebooklm_id":"nlm-1","notebooklm_notebook":"nb-42"}`
	lines := historyLines(t, dir)
	at := slices.IndexFunc(lines, func(line string) bool {
		return strings.Contains(line, `"type":"`+eventSourceAdded+`","data":`+added)
	})
	if at < 0 {
		t.Fatal("the fixture has no source.added Event carrying NotebookLM ids")
	}
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(strings.Join(lines[:at+1], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, topicFile), []byte(strings.Replace(string(settings), `"nb-43"`, `"nb-42"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, sourcesFile)); err != nil {
		t.Fatal(err)
	}
	m.leaveIntent(t, earlierBuildEvent(t, dir, eventSourceAdded, `"notebooklm_id":"nlm-1"`), eventSourceAdded)

	m.update(t, TopicChanges{Goal: ptr("G")}) // the next write finishes the interrupted one
	if data := readSourcesFile(t, m); data != added+"\n" {
		t.Errorf("recovery should write the line the earlier build would have:\n%s\nwant\n%s\nlogs:\n%s", data, added, m.logs)
	}
	if logs := m.logs.String(); !strings.Contains(logs, "finished an interrupted write") ||
		strings.Contains(logs, "writes differently") || m.hasIntent(t) {
		t.Errorf("the interrupted write should be finished as recorded; logs:\n%s", logs)
	}
	list := m.sources(t)
	if len(list.Sources) != 1 || list.Sources[0].ID != "example-com-a.qpm4lo" || list.Sources[0].Title != "https://example.com/a" {
		t.Errorf("ListSources = %+v", list.Sources)
	}
	if out, err := json.Marshal(list); err != nil || strings.Contains(string(out), "nlm-") {
		t.Errorf("ListSources shows an id of the notebook: %s, %v", out, err)
	}

	if res, err := m.UpdateSource(ctx, "c", "example-com-a.qpm4lo", SourceChanges{Title: ptr("Example")}); err != nil || !res.Changed {
		t.Fatalf("UpdateSource = %+v, %v", res, err)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range topic.Flags {
		t.Errorf("flag after one machine's recovery and one change: %s: %s", f.Kind, f.Message)
	}
	want := strings.Replace(added, `"title":"https://example.com/a"`, `"title":"Example"`, 1) + "\n"
	if data := readSourcesFile(t, m); data != want {
		t.Errorf("sources.jsonl after the change:\n%s\nwant\n%s", data, want)
	}
}

// A limit of that rule, kept on purpose (ADR-0011). The Source in an Event's
// payload is the version the Event recorded only when the line held nothing
// else. If the line also held a field the earlier build did not know, such as
// one added by hand, the recorded version had that field too and the payload
// does not, so recovery writes as it always did. An id-only change then
// leaves the line as it was, and the next change to the Source is flagged as
// a conflict, which the learner dismisses. Nothing is lost.
func TestRecoveryOfAnEarlierBuildsIDChangeOnAHandEditedLineCanFlagAConflict(t *testing.T) {
	ctx := context.Background()
	m := earlierBuildTopic(t)
	dir := filepath.Join(m.home, "c")
	const (
		source   = `"id":"example-com-a.qpm4lo","kind":"url","title":"https://example.com/a","url":"https://example.com/a"`
		before   = `{` + source + `,"notebooklm_id":"nlm-2","notebooklm_notebook":"nb-42","pages":312}`
		recorded = `{` + source + `,"notebooklm_id":"nlm-3","notebooklm_notebook":"nb-42","pages":312}`
	)
	// The learner added "pages" to the line by hand.
	sources := readSourcesFile(t, m)
	edited := strings.Replace(sources, `"notebooklm_notebook":"nb-42"}`, `"notebooklm_notebook":"nb-42","pages":312}`, 1)
	if !strings.HasPrefix(edited, before+"\n") {
		t.Fatalf("the fixture's first line is not the one this test edits:\n%s", sources)
	}
	if err := os.WriteFile(filepath.Join(dir, sourcesFile), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	// Then the earlier build recorded a change of the id alone, from nlm-2 to
	// nlm-3, and stopped before writing: its Event, as that build wrote them,
	// with the line it meant to leave, "pages" included, as the version after.
	appendLine(t, filepath.Join(dir, historyFile), `{"format":1,"id":"earlierbuild01","time":"2026-10-08T19:00:00Z",`+
		`"wall":"2026-10-08T19:00:00Z","type":"source.updated","data":{"id":"example-com-a.qpm4lo",`+
		`"notebooklm_id":"nlm-3","notebooklm_notebook":"nb-42","source":{`+source+`,"notebooklm_id":"nlm-3","notebooklm_notebook":"nb-42"}},`+
		`"items":[{"item":"sources.jsonl#example-com-a.qpm4lo","before":"`+contentHash([]byte(before), true)+
		`","after":"`+contentHash([]byte(recorded), true)+`"}]}`+"\n")
	m.leaveIntent(t, "earlierbuild01", eventSourceUpdated)

	if res, err := m.UpdateSource(ctx, "c", "example-com-a.qpm4lo", SourceChanges{Title: ptr("Example")}); err != nil || !res.Changed {
		t.Fatalf("UpdateSource = %+v, %v", res, err)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagConflict || topic.Flags[0].Item != sourceItem("example-com-a.qpm4lo") {
		t.Errorf("flags = %+v, want one conflict on the Source", topic.Flags)
	}
	// The title change and the hand-added field are both there; the id is the
	// one the line had.
	want := strings.Replace(before, `"title":"https://example.com/a"`, `"title":"Example"`, 1) + "\n"
	if data := readSourcesFile(t, m); !strings.HasPrefix(data, want) {
		t.Errorf("sources.jsonl after the change:\n%s\nwant its first line to be\n%s", data, want)
	}
	if !strings.Contains(m.logs.String(), "writes differently") || m.hasIntent(t) {
		t.Errorf("recovery should finish the write, warning that it writes the line differently; logs:\n%s", m.logs)
	}
}

// An earlier build's interrupted move from one notebook to another is
// finished by this version, which writes the table anew with the kind alone.
// topic.toml is then at a version of its own, so the next change to it is no
// conflict: only one machine ever wrote.
func TestAChangeAfterRecoveringAnEarlierBuildsNotebookChangeIsNoConflict(t *testing.T) {
	m := earlierBuildTopic(t)
	dir := filepath.Join(m.home, "c")
	interrupted := earlierBuildEvent(t, dir, eventKnowledgeBaseSet, `"notebook":"nb-43"`)
	// topic.toml as that Event found it: with the first notebook.
	path := filepath.Join(dir, topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	undone := strings.Replace(string(data), `"nb-43"`, `"nb-42"`, 1)
	if undone == string(data) {
		t.Fatalf("the fixture's topic.toml has no nb-43 to undo:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(undone), 0o644); err != nil {
		t.Fatal(err)
	}
	m.leaveIntent(t, interrupted, eventKnowledgeBaseSet)

	res := m.update(t, TopicChanges{Title: ptr("Systems C")})
	if !res.Changed || res.Topic.Title != "Systems C" || res.Topic.KnowledgeBase == nil || res.Topic.KnowledgeBase.Kind != "notebooklm" {
		t.Errorf("the title change = %+v", res)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range topic.Flags {
		t.Errorf("flag after one machine's recovery and one change: %s: %s", f.Kind, f.Message)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `kind = "notebooklm"`) || strings.Contains(string(data), "nb-4") {
		t.Errorf("topic.toml should keep the kind and no notebook:\n%s", data)
	}
	if logs := m.logs.String(); !strings.Contains(logs, "finished an interrupted write") || m.hasIntent(t) {
		t.Errorf("the interrupted write was not finished; logs:\n%s", logs)
	}
}

func TestSourceValidation(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := t.TempDir()
	book := writeBook(t, filepath.Join(dir, "a.pdf"), "%PDF-1.4 A")
	copyOfBook := writeBook(t, filepath.Join(dir, "copy.pdf"), "%PDF-1.4 A")
	src := m.addSource(t, SourceSpec{File: book})
	page := m.addSource(t, SourceSpec{URL: "https://example.com/page"})

	for _, tc := range []struct {
		name string
		spec SourceSpec
		want ErrorCode
	}{
		{"neither", SourceSpec{Topic: "c"}, CodeInvalidArgument},
		{"both", SourceSpec{Topic: "c", File: book, URL: "https://example.com"}, CodeInvalidArgument},
		{"missing file", SourceSpec{Topic: "c", File: filepath.Join(dir, "nope.pdf")}, CodeNotFound},
		{"folder", SourceSpec{Topic: "c", File: dir}, CodeInvalidArgument},
		{"ftp", SourceSpec{Topic: "c", URL: "ftp://example.com/book.pdf"}, CodeInvalidArgument},
		{"no scheme", SourceSpec{Topic: "c", URL: "example.com/page"}, CodeInvalidArgument},
		{"space", SourceSpec{Topic: "c", URL: "https://example.com/a page"}, CodeInvalidArgument},
		{"credentials", SourceSpec{Topic: "c", URL: "https://user:secret@example.com/x"}, CodeInvalidArgument},
		{"invalid UTF-8 title", SourceSpec{Topic: "c", URL: "https://example.com/u", Title: "bad \xff"}, CodeInvalidArgument},
		{"same content", SourceSpec{Topic: "c", File: copyOfBook}, CodeAlreadyExists},
		{"same URL", SourceSpec{Topic: "c", URL: "https://EXAMPLE.com:443/page"}, CodeAlreadyExists},
		{"unknown Topic", SourceSpec{Topic: "go", URL: "https://example.com/other"}, CodeNotFound},
		{"inside .git", SourceSpec{Topic: "c", File: filepath.Join(m.home, "c", ".git", "HEAD")}, CodeInvalidArgument},
	} {
		if tc.name == "inside .git" {
			writeBook(t, tc.spec.File, "ref: refs/heads/main\n")
		}
		_, err := m.AddSource(ctx, tc.spec)
		if CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.want)
		}
		if tc.want == CodeAlreadyExists && err != nil && !strings.Contains(err.Error(), src.ID) && !strings.Contains(err.Error(), page.ID) {
			t.Errorf("%s: %v does not name the existing Source", tc.name, err)
		}
	}

	before := historyLines(t, filepath.Join(m.home, "c"))
	dry, err := m.AddSource(ctx, SourceSpec{Topic: "c", URL: "https://example.com/new", DryRun: true})
	if err != nil || !dry.DryRun || dry.Source.ID == "" {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); !slices.Equal(before, after) || len(m.sources(t).Sources) != 2 {
		t.Error("a dry run added a Source")
	}

	for _, tc := range []struct {
		name    string
		id      string
		changes SourceChanges
		want    ErrorCode
	}{
		{"bad id", "Not An Id", SourceChanges{Title: ptr("x")}, CodeInvalidArgument},
		{"unknown", "nothing.abc123", SourceChanges{Title: ptr("x")}, CodeNotFound},
		{"unknown, path only", "nothing.abc123", SourceChanges{Path: &book}, CodeNotFound},
		{"nothing", src.ID, SourceChanges{}, CodeInvalidArgument},
		{"empty title", src.ID, SourceChanges{Title: ptr(" ")}, CodeInvalidArgument},
		{"path of a URL", page.ID, SourceChanges{Path: &book}, CodeInvalidArgument},
		{"another file", src.ID, SourceChanges{Path: ptr(writeBook(t, filepath.Join(dir, "b.pdf"), "%PDF-1.4 B"))}, CodeInvalidArgument},
	} {
		if _, err := m.UpdateSource(ctx, "c", tc.id, tc.changes); CodeOf(err) != tc.want {
			t.Errorf("update %s: err = %v, want %s", tc.name, err, tc.want)
		}
	}
}

// Paths starting with ~/ are in the home folder, as in STUDY_HOME.
func TestSourcePathsExpandTheHomeFolder(t *testing.T) {
	m := newTopic(t) // its HOME is the Study home
	book := writeBook(t, filepath.Join(m.home, "Books", "sicp.pdf"), "%PDF-1.4 SICP")
	res, err := m.AddSource(context.Background(), SourceSpec{Topic: "c", File: "~/Books/sicp.pdf"})
	if err != nil || res.Path != book {
		t.Errorf("AddSource(~/Books/sicp.pdf) = %+v, %v; want %s", res, err, book)
	}
}

// The History, not sources.jsonl, decides which Sources exist.
func TestTheHistoryDecidesWhichSourcesExist(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	path := filepath.Join(m.home, "c", sourcesFile)
	appendLine(t, path, `{"id":"hand.abc123","kind":"url","title":"Hand\u001b[2J","url":"https://example.com/hand"}`+"\n")

	list := m.sources(t)
	if len(list.Sources) != 2 || list.Sources[1].State != SourceUntracked || list.Sources[1].ID != "hand.abc123" ||
		strings.ContainsFunc(list.Sources[1].Title, unicode.IsControl) {
		t.Fatalf("ListSources = %+v, want the hand-added line untracked", list.Sources)
	}
	if _, err := m.UpdateSource(ctx, "c", "hand.abc123", SourceChanges{Title: ptr("Renamed")}); CodeOf(err) != CodeNotFound ||
		!strings.Contains(err.Error(), "by hand") {
		t.Errorf("updating an untracked line: err = %v, want not_found explaining it", err)
	}
	if _, err := m.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "l", Source: "hand.abc123", Quote: "q"}); CodeOf(err) != CodeNotFound {
		t.Errorf("Evidence from an untracked line: err = %v, want not_found", err)
	}
	// Adding the same page records it as a Source of its own.
	added := m.addSource(t, SourceSpec{URL: "https://example.com/hand"})
	if list := m.sources(t); len(list.Sources) != 3 || list.Sources[1].ID != added.ID || list.Sources[1].State != "" {
		t.Errorf("after adding it properly: %+v", list.Sources)
	}
}

// Recovery finishes an Event that replay holds, such as an update whose
// Source has not arrived yet: it has no recorded version to be superseded
// by.
func TestRecoveryFinishesAHeldEvent(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Title: ptr("Renamed")}); !errors.Is(err, errCrash) {
		t.Fatalf("err = %v", err)
	}
	m.crash = nil
	// Without its source.added, replay holds the source.updated.
	dir := filepath.Join(m.home, "c")
	var kept []string
	for _, line := range historyLines(t, dir) {
		if !strings.Contains(line, `"`+eventSourceAdded+`"`) {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(strings.Join(kept, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if kinds := flagKinds(replayFolder(t, dir).flags); !slices.Equal(kinds, []string{FlagHeldEvent}) {
		t.Fatalf("flags = %v, want the update held", kinds)
	}
	m.update(t, TopicChanges{Goal: ptr("G")}) // the next write finishes the interrupted one
	if data := readSourcesFile(t, m); !strings.Contains(data, `"title":"Renamed"`) {
		t.Errorf("recovery dropped the held update:\n%s\nlogs:\n%s", data, m.logs)
	}
}

// A crash at any point of any Knowledge Event is finished by the next
// write, and a dry run agrees with the real run in the meantime.
func TestCrashesInKnowledgeWrites(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		setup func(t *testing.T, m *machine) (state string)
		run   func(m *machine, state string, dryRun bool) (changed bool, err error)
		check func(t *testing.T, m *machine)
	}
	ops := []op{
		{"knowledge_base.set", func(*testing.T, *machine) string { return "" },
			func(m *machine, _ string, dry bool) (bool, error) {
				res, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}, DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if kb := knowledgeBaseOf(readSettings(t, filepath.Join(m.home, "c"))); kb == nil || kb.Kind != KnowledgeBaseNone {
					t.Errorf("Knowledge base = %+v", kb)
				}
			}},
		{"source.added", func(*testing.T, *machine) string { return "" },
			func(m *machine, _ string, dry bool) (bool, error) {
				res, err := m.AddSource(ctx, SourceSpec{Topic: "c", URL: "https://example.com/a", DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if n := strings.Count(readSourcesFile(t, m), "\n"); n != 1 || len(m.sources(t).Sources) != 1 {
					t.Errorf("sources.jsonl has %d lines, the History %d Sources", n, len(m.sources(t).Sources))
				}
			}},
		{"source.updated", func(t *testing.T, m *machine) string {
			return m.addSource(t, SourceSpec{URL: "https://example.com/a"}).ID
		},
			func(m *machine, id string, dry bool) (bool, error) {
				res, err := m.UpdateSource(ctx, "c", id, SourceChanges{Title: ptr("New"), DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if !strings.Contains(readSourcesFile(t, m), `"title":"New"`) || m.sources(t).Sources[0].Title != "New" {
					t.Errorf("sources.jsonl = %s", readSourcesFile(t, m))
				}
			}},
		{"evidence.recorded", func(t *testing.T, m *machine) string {
			return m.addSource(t, SourceSpec{URL: "https://example.com/a"}).ID
		},
			func(m *machine, id string, dry bool) (bool, error) {
				res, err := m.RecordEvidence(ctx, EvidenceSpec{Topic: "c", Lesson: "l", Source: id, Quote: "q", DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if got := m.evidence(t, EvidenceQuery{}); len(got) != 1 {
					t.Errorf("Evidence = %+v", got)
				}
			}},
		{"evidence.retracted", func(t *testing.T, m *machine) string {
			id := m.addSource(t, SourceSpec{URL: "https://example.com/a"}).ID
			return m.recordEvidence(t, EvidenceSpec{Lesson: "l", Source: id, Quote: "q"}).Evidence.ID
		},
			func(m *machine, id string, dry bool) (bool, error) {
				res, err := m.RetractEvidence(ctx, "c", id, dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if got := m.evidence(t, EvidenceQuery{}); len(got) != 0 {
					t.Errorf("Evidence after retracting = %+v", got)
				}
			}},
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				state := o.setup(t, m)
				m.crash = crashOnce(point)
				_, err := o.run(m, state, false)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				dryChanged, dryErr := o.run(m, state, true)
				realChanged, realErr := o.run(m, state, false)
				if CodeOf(dryErr) != CodeOf(realErr) || (dryErr == nil) != (realErr == nil) || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				if hasIntentFile(t, m) {
					t.Error("the intent marker survived the next write")
				}
				if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				o.check(t, m)
			})
		}
	}
}

// A file Source is found on this computer, inside the Topic, where it was
// last found, or in the Library by its content; finding it records nothing
// in the History.
func TestFileSourcesAreFoundOnThisComputer(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	books := filepath.Join(t.TempDir(), "Books")
	original := writeBook(t, filepath.Join(books, "OS", "modern_operating_systems.pdf"), "%PDF-1.4 Tanenbaum")
	if _, err := m.BuildLibrary(ctx, books); err != nil {
		t.Fatal(err)
	}
	src := m.addSource(t, SourceSpec{File: original})
	if src.Title != "Modern Operating Systems" || src.FileName != "modern_operating_systems.pdf" {
		t.Errorf("Source = %+v, want the Library's title", src)
	}
	events := len(historyLines(t, filepath.Join(m.home, "c")))

	moved := filepath.Join(books, "Systems", "tanenbaum.pdf")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceMissing || got.Path != "" {
		t.Errorf("before the Library is rebuilt: %+v, want missing", got)
	}
	if _, err := m.BuildLibrary(ctx, books); err != nil {
		t.Fatal(err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceOK || got.Path != moved {
		t.Fatalf("after the Library is rebuilt: %+v, want ok at %s", got, moved)
	}
	if n := len(historyLines(t, filepath.Join(m.home, "c"))); n != events {
		t.Errorf("finding the file recorded %d Events", n-events)
	}
	// The place found is remembered: the Library no longer needs to know it.
	if err := os.Remove(filepath.Join(m.home, libraryIndex)); err != nil {
		t.Fatal(err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceOK || got.Path != moved {
		t.Errorf("without the Library: %+v, want ok where it was last found", got)
	}

	// A rewrite of the same size is noticed by its modification time.
	writeBook(t, moved, "%PDF-1.4 Tanenbauz")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(moved, later, later); err != nil {
		t.Fatal(err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceChanged || got.Path != moved {
		t.Errorf("after a same-size rewrite: %+v, want changed", got)
	}

	// Saying where it is records it on this computer only.
	elsewhere := writeBook(t, filepath.Join(t.TempDir(), "kept.pdf"), "%PDF-1.4 Tanenbaum")
	res, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Path: &elsewhere})
	if err != nil || !res.Changed || res.Path != elsewhere {
		t.Fatalf("recording the path = %+v, %v", res, err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceOK || got.Path != elsewhere {
		t.Errorf("after recording the path: %+v", got)
	}
	if n := len(historyLines(t, filepath.Join(m.home, "c"))); n != events {
		t.Errorf("recording a path recorded %d Events", n-events)
	}
	if strings.Contains(readSourcesFile(t, m), elsewhere) {
		t.Error("sources.jsonl holds this computer's path")
	}
}

// Two machines adding Sources and Evidence merge without conflicts, and
// Evidence that arrives before its Source is held until the Source does.
func TestSourcesAndEvidenceFromTwoMachines(t *testing.T) {
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	syncTopic(t, a, b)

	fromA := a.addSource(t, SourceSpec{File: writeBook(t, filepath.Join(t.TempDir(), "k_and_r.pdf"), "%PDF-1.4 K&R")})
	a.recordEvidence(t, EvidenceSpec{Lesson: "pointers", Source: fromA.ID, Quote: "A pointer is a variable."})
	fromB := b.addSource(t, SourceSpec{URL: "https://example.com/c-tutorial"})
	b.recordEvidence(t, EvidenceSpec{Lesson: "pointers", Source: fromB.ID, Quote: "Pointers hold addresses."})

	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	aIntoB, bIntoA := replayLines(t, unionMerge(theirs, ours)), replayLines(t, unionMerge(ours, theirs))
	if !reflect.DeepEqual(summarize(aIntoB), summarize(bIntoA)) || len(aIntoB.flags) != 0 {
		t.Fatalf("Sources from two machines:\n A into B: %+v\n B into A: %+v", summarize(aIntoB), summarize(bIntoA))
	}
	if !reflect.DeepEqual(aIntoB.knowledge().evidence, bIntoA.knowledge().evidence) || len(aIntoB.knowledge().evidence) != 2 {
		t.Errorf("Evidence differs between merge directions: %+v vs %+v", aIntoB.knowledge().evidence, bIntoA.knowledge().evidence)
	}

	// B's Evidence arrives without B's Source: it is held and flagged...
	var evidenceOnly []string
	for _, line := range theirs {
		if strings.Contains(line, `"`+eventEvidenceRecorded+`"`) {
			evidenceOnly = append(evidenceOnly, line)
		}
	}
	held := replayLines(t, unionMerge(ours, evidenceOnly))
	if kinds := flagKinds(held.flags); !slices.Equal(kinds, []string{FlagHeldEvent}) || len(held.knowledge().evidence) != 1 {
		t.Errorf("Evidence before its Source: flags %v, Evidence %+v", kinds, held.knowledge().evidence)
	}
	// ...until the Source arrives too.
	if resolved := replayLines(t, unionMerge(unionMerge(ours, evidenceOnly), theirs)); len(resolved.flags) != 0 ||
		len(resolved.knowledge().evidence) != 2 {
		t.Errorf("after the Source arrives: flags %+v, Evidence %+v", resolved.flags, resolved.knowledge().evidence)
	}
}

// Sources travel between machines through git: the Topic's files and the
// History merge, a Source inside the Topic is found in the clone, one
// outside is found in the other machine's Library, and finding files
// records no Events, so nothing conflicts.
func TestSourcesSyncThroughGit(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(a.home, "c")
	paper := writeBook(t, filepath.Join(dirA, "notes", "paper.pdf"), "%PDF-1.4 paper")
	inTopic := a.addSource(t, SourceSpec{File: paper})
	if inTopic.TopicPath != "notes/paper.pdf" || inTopic.FileName != "" {
		t.Fatalf("a Source inside the Topic = %+v", inTopic)
	}
	booksA := filepath.Join(t.TempDir(), "Books")
	kr := writeBook(t, filepath.Join(booksA, "kr.pdf"), "%PDF-1.4 K&R")
	if _, err := a.BuildLibrary(ctx, booksA); err != nil {
		t.Fatal(err)
	}
	outside := a.addSource(t, SourceSpec{File: kr})
	takeCheckpoint(t, a)

	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", dirA, "c")
	dirB := filepath.Join(b.home, "c")
	library := filepath.Join(t.TempDir(), "Library")
	onB := writeBook(t, filepath.Join(library, "C", "the_c_book.pdf"), "%PDF-1.4 K&R")
	if _, err := b.BuildLibrary(ctx, library); err != nil {
		t.Fatal(err)
	}
	before := historyLines(t, dirB)
	list := b.sources(t)
	if got := list.Sources[0]; got.ID != inTopic.ID || got.State != SourceOK || got.Path != filepath.Join(dirB, "notes", "paper.pdf") {
		t.Errorf("the Source inside the Topic on B: %+v", got)
	}
	if got := list.Sources[1]; got.ID != outside.ID || got.State != SourceOK || got.Path != onB {
		t.Errorf("the Source in B's Library: %+v", got)
	}
	if after := historyLines(t, dirB); !slices.Equal(before, after) || strings.TrimSpace(git(t, dirB, "status", "--porcelain")) != "" {
		t.Error("finding Sources on B changed the Topic")
	}
	a.sources(t) // A finds its files too, recording nothing

	a.addSource(t, SourceSpec{URL: "https://example.com/from-a"})
	takeCheckpoint(t, a)
	b.addSource(t, SourceSpec{URL: "https://example.com/from-b"})
	takeCheckpoint(t, b)
	git(t, dirB, "pull", "-q", "--no-rebase", "--no-edit", dirA, "main")
	if list := b.sources(t); len(list.Sources) != 4 {
		t.Errorf("after the merge B has %d Sources: %+v", len(list.Sources), list.Sources)
	}
	if n := strings.Count(readSourcesFile(t, b), "\n"); n != 4 {
		t.Errorf("the merged sources.jsonl has %d lines:\n%s", n, readSourcesFile(t, b))
	}
	if topic, err := b.readTopic("c"); err != nil || len(topic.Flags) != 0 {
		t.Errorf("after the merge: %+v, %v", topic.Flags, err)
	}
}

// A union merge of one Source edited on two machines leaves two lines for
// it: the Topic stays usable, the lines are flagged, and the next change to
// the Source leaves one line.
func TestDuplicateSourceLinesAreFlagged(t *testing.T) {
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	appendLine(t, filepath.Join(m.home, "c", sourcesFile), `{"id":"`+src.ID+`","kind":"url","title":"Edited elsewhere","url":"https://example.com/a"}`+"\n")
	topic, err := m.readTopic("c")
	if err != nil || len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagConflict || topic.Flags[0].Item != sourceItem(src.ID) {
		t.Fatalf("flags = %+v, %v; want a conflict on the Source", topic.Flags, err)
	}
	if got := m.sources(t).Sources; len(got) != 1 || got[0].Title != src.Title {
		t.Errorf("ListSources = %+v, want the History's version", got)
	}
	if _, err := m.UpdateSource(context.Background(), "c", src.ID, SourceChanges{Title: ptr("Settled")}); err != nil {
		t.Fatal(err)
	}
	if data := readSourcesFile(t, m); strings.Count(data, src.ID) != 1 || !strings.Contains(data, "Settled") {
		t.Errorf("sources.jsonl after the next change:\n%s", data)
	}
	if topic, _ := m.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags after the next change: %+v", topic.Flags)
	}
}

// A line whose id is not a Source id is not a Source: it is neither listed
// as untracked nor flagged, so its id never reaches a terminal.
func TestLinesWithMalformedIDsAreNotSources(t *testing.T) {
	m := newTopic(t)
	m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	path := filepath.Join(m.home, "c", sourcesFile)
	bad := `{"id":"x\u001b]0;pwned\u0007","kind":"url","title":"t","url":"https://example.com/x"}` + "\n"
	appendLine(t, path, bad+bad+`{"id":"hand.abc123","kind":"url","title":"Hand","url":"https://example.com/hand"}`+"\n")
	list := m.sources(t)
	if len(list.Sources) != 2 || list.Sources[1].ID != "hand.abc123" {
		t.Errorf("ListSources = %+v, want only the well-formed hand-added line untracked", list.Sources)
	}
	if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
		t.Errorf("flags = %+v, %v; want none for lines that are not Sources", topic.Flags, err)
	}
}

// The flag for a Source's duplicate lines has the same id whichever order a
// union merge left the lines in, so dismissing it on one machine dismisses
// it on the other.
func TestTheDuplicateLinesFlagIsTheSameInEitherOrder(t *testing.T) {
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	path := filepath.Join(m.home, "c", sourcesFile)
	ours := strings.TrimSuffix(readSourcesFile(t, m), "\n")
	theirs := `{"id":"` + src.ID + `","kind":"url","title":"Edited elsewhere","url":"https://example.com/a"}`
	var ids []string
	for _, lines := range [][]string{{ours, theirs}, {theirs, ours}} {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		topic, err := m.readTopic("c")
		if err != nil || len(topic.Flags) != 1 {
			t.Fatalf("flags = %+v, %v", topic.Flags, err)
		}
		ids = append(ids, topic.Flags[0].ID)
	}
	if ids[0] != ids[1] {
		t.Errorf("the flag's id depends on the order of the lines: %s and %s", ids[0], ids[1])
	}
}

// After a union merge leaves two lines for a Source, the next change writes
// the History's version of every field, even when the other copy is the one
// read first.
func TestTheNextChangeWritesTheHistorysVersion(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/a"})
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Title: ptr("Your title")}); err != nil {
		t.Fatal(err)
	}
	// The other machine's copy sorts before the History's version.
	appendLine(t, filepath.Join(m.home, "c", sourcesFile),
		`{"id":"`+src.ID+`","kind":"url","title":"A stray title","url":"https://example.com/stray"}`+"\n")
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Title: ptr("Your new title")}); err != nil {
		t.Fatal(err)
	}
	data := readSourcesFile(t, m)
	if strings.Count(data, src.ID) != 1 || !strings.Contains(data, `"title":"Your new title"`) ||
		!strings.Contains(data, `"url":"https://example.com/a"`) {
		t.Errorf("sources.jsonl after the next change, want the new title and the History's address:\n%s", data)
	}
	if got := m.sources(t).Sources; len(got) != 1 || got[0].Title != "Your new title" || got[0].URL != "https://example.com/a" {
		t.Errorf("ListSources = %+v", got)
	}
	if topic, _ := m.readTopic("c"); len(topic.Flags) != 0 {
		t.Errorf("flags after the next change: %+v", topic.Flags)
	}
}

func TestEvidenceValidation(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/page"})
	ok := EvidenceSpec{Topic: "c", Lesson: "lesson-01", Source: src.ID, Quote: "A quote."}
	with := func(edit func(*EvidenceSpec)) EvidenceSpec {
		spec := ok
		edit(&spec)
		return spec
	}
	for _, tc := range []struct {
		name string
		spec EvidenceSpec
		want ErrorCode
	}{
		{"no Lesson", with(func(s *EvidenceSpec) { s.Lesson = "" }), CodeInvalidArgument},
		{"bad Lesson", with(func(s *EvidenceSpec) { s.Lesson = "Lesson 1" }), CodeInvalidArgument},
		{"bad Source id", with(func(s *EvidenceSpec) { s.Source = "x" }), CodeInvalidArgument},
		{"unknown Source", with(func(s *EvidenceSpec) { s.Source = "nothing.abc123" }), CodeNotFound},
		{"no quote", with(func(s *EvidenceSpec) { s.Quote = "  \n " }), CodeInvalidArgument},
		{"control character", with(func(s *EvidenceSpec) { s.Quote = "a\x1b[2Jb" }), CodeInvalidArgument},
		{"invalid UTF-8", with(func(s *EvidenceSpec) { s.Quote = "caf\xe9" }), CodeInvalidArgument},
		{"long quote", with(func(s *EvidenceSpec) { s.Quote = strings.Repeat("a", maxQuoteRunes+1) }), CodeInvalidArgument},
		{"location without its origin", with(func(s *EvidenceSpec) { s.Location = "p. 1" }), CodeInvalidArgument},
		{"origin without a location", with(func(s *EvidenceSpec) { s.LocationFrom = LocationFromSource }), CodeInvalidArgument},
		{"unknown origin", with(func(s *EvidenceSpec) { s.Location, s.LocationFrom = "p. 1", "memory" }), CodeInvalidArgument},
		{"unknown Topic", with(func(s *EvidenceSpec) { s.Topic = "go" }), CodeNotFound},
	} {
		if _, err := m.RecordEvidence(ctx, tc.spec); CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.want)
		}
	}
	if _, err := m.ListEvidence(ctx, EvidenceQuery{Topic: "c", Lesson: "Lesson 1"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("listing a bad Lesson id: err = %v", err)
	}

	before := historyLines(t, filepath.Join(m.home, "c"))
	dry, err := m.RecordEvidence(ctx, with(func(s *EvidenceSpec) { s.DryRun = true }))
	if err != nil || !dry.Changed || !dry.DryRun || dry.Evidence.Quote != "A quote." {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); !slices.Equal(before, after) {
		t.Error("a dry run recorded Evidence")
	}
}

func TestEvidenceRetraction(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/page"})
	wrong := m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-01", Source: src.ID, Quote: "A misquote."}).Evidence
	m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-02", Source: src.ID, Quote: "A quote."})

	before := historyLines(t, filepath.Join(m.home, "c"))
	dry, err := m.RetractEvidence(ctx, "c", wrong.ID, true)
	if err != nil || !dry.Changed || !dry.Evidence.Retracted || !slices.Equal(before, historyLines(t, filepath.Join(m.home, "c"))) {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	res, err := m.RetractEvidence(ctx, "c", wrong.ID, false)
	if err != nil || !res.Changed || !res.Evidence.Retracted || res.Evidence.Quote != "A misquote." {
		t.Fatalf("RetractEvidence = %+v, %v", res, err)
	}
	if again, err := m.RetractEvidence(ctx, "c", wrong.ID, false); err != nil || again.Changed {
		t.Errorf("retracting twice = %+v, %v", again, err)
	}
	if got := m.evidence(t, EvidenceQuery{}); len(got) != 1 || got[0].Lesson != "lesson-02" {
		t.Errorf("listed Evidence = %+v", got)
	}
	if got := m.evidence(t, EvidenceQuery{All: true}); len(got) != 2 || !got[0].Retracted {
		t.Errorf("all Evidence = %+v", got)
	}
	s := replayFolder(t, filepath.Join(m.home, "c"))
	if got := s.lessonsWithoutEvidence([]string{"lesson-01", "lesson-02"}); !slices.Equal(got, []string{"lesson-01"}) {
		t.Errorf("lessons without Evidence = %v", got)
	}
	// The same quote can be recorded again after its retraction.
	if again := m.recordEvidence(t, EvidenceSpec{Lesson: "lesson-01", Source: src.ID, Quote: "A misquote."}); !again.Changed ||
		again.Evidence.ID == wrong.ID {
		t.Errorf("recording a retracted quote again = %+v", again)
	}
	for _, tc := range []struct {
		id   string
		want ErrorCode
	}{{"Not An Id", CodeInvalidArgument}, {"nothing", CodeNotFound}} {
		if _, err := m.RetractEvidence(ctx, "c", tc.id, false); CodeOf(err) != tc.want {
			t.Errorf("retracting %q: err = %v, want %s", tc.id, err, tc.want)
		}
	}
}

// Fields of sources.jsonl that this version does not know survive an update,
// and an interrupted update that recovery finishes: the Event's payload does
// not hold them, so recovery takes them from the line, as the write would
// have.
func TestUnknownSourceFieldsAreKept(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/page"})
	path := filepath.Join(m.home, "c", sourcesFile)
	line := strings.TrimSuffix(readSourcesFile(t, m), "}\n") + `,"pages":312}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Title: ptr("Example")}); err != nil {
		t.Fatal(err)
	}
	if data := readSourcesFile(t, m); !strings.Contains(data, `"pages":312`) || !strings.Contains(data, `"title":"Example"`) {
		t.Errorf("sources.jsonl = %s", data)
	}

	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Title: ptr("Example, again")}); !errors.Is(err, errCrash) {
		t.Fatalf("err = %v", err)
	}
	m.crash = nil
	m.update(t, TopicChanges{Goal: ptr("G")}) // the next write finishes the interrupted one
	if data := readSourcesFile(t, m); !strings.Contains(data, `"pages":312`) || !strings.Contains(data, `"title":"Example, again"`) {
		t.Errorf("sources.jsonl after recovery = %s", data)
	}
	if logs := m.logs.String(); !strings.Contains(logs, "finished an interrupted write") || strings.Contains(logs, "writes differently") {
		t.Errorf("recovery should write the version the Event recorded; logs:\n%s", logs)
	}
	if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
		t.Errorf("flags after recovery: %+v, %v", topic.Flags, err)
	}
}

func TestURLsAreNormalised(t *testing.T) {
	for raw, want := range map[string]string{
		"HTTPS://Example.COM/A":                                   "https://example.com/A",
		"https://example.com:443/page":                            "https://example.com/page",
		"http://example.com:80/":                                  "http://example.com/",
		"http://example.com:8080/x?y=1#part":                      "http://example.com:8080/x?y=1#part",
		"https://[::1]:443/":                                      "https://[::1]/",
		"http://例え.JP/パス":                                         "http://例え.jp/%E3%83%91%E3%82%B9",
		"https://example.com/\u202egnp.exe":                       "",
		"https://user:secret@example.com/x":                       "",
		"https://exa\u202emple.com/":                              "",
		"javascript:alert(1)":                                     "",
		"https:example.com":                                       "",
		"file:///etc/passwd":                                      "",
		"https://example.com/a\x00b":                              "",
		"https://example.com/" + "\xff":                           "",
		"https://example.com/" + strings.Repeat("a", maxURLRunes): "",
	} {
		got, err := cleanURL(raw)
		if want == "" && CodeOf(err) != CodeInvalidArgument || want != "" && (err != nil || got != want) {
			t.Errorf("cleanURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}

	// A title derived from a long URL fits the title limit, and can be set
	// again as it is.
	m := newTopic(t)
	res, err := m.AddSource(context.Background(), SourceSpec{Topic: "c", URL: "https://example.com/" + strings.Repeat("a", 1900)})
	if err != nil || utf8.RuneCountInString(res.Source.Title) > maxTitleRunes {
		t.Fatalf("long URL Source = %+v, %v", res.Source, err)
	}
	if _, err := m.UpdateSource(context.Background(), "c", res.Source.ID, SourceChanges{Title: &res.Source.Title}); err != nil {
		t.Errorf("setting the derived title again: %v", err)
	}
}

// Titles and names Lamplight derives are safe to print.
func TestDerivedTitlesAreSafe(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := t.TempDir()
	odd := writeBook(t, filepath.Join(dir, "red\x1b[31m_book.pdf"), "%PDF-1.4 red")
	if _, err := m.AddSource(ctx, SourceSpec{Topic: "c", File: odd}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a path with an escape sequence: err = %v, want invalid_argument", err)
	}
	// The Library titles the book from its name; reached through a plain
	// link, that title is cleaned before it is stored.
	if _, err := m.BuildLibrary(ctx, dir); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "plain.pdf")
	if err := os.Symlink(odd, plain); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	escape := m.addSource(t, SourceSpec{File: plain})
	if strings.ContainsFunc(escape.Title, unicode.IsControl) || !strings.Contains(escape.Title, "Book") {
		t.Errorf("derived %q from a Library title with an escape sequence", escape.Title)
	}
	if got := derivedText("a\x1b[2J\u202eb\xff", 10); got != "a [2J b" {
		t.Errorf("derivedText = %q", got)
	}
	symbols := m.addSource(t, SourceSpec{File: writeBook(t, filepath.Join(dir, "___.pdf"), "%PDF-1.4 ___")})
	if symbols.Title == "" || !sourceIDPattern.MatchString(symbols.ID) {
		t.Errorf("a name that slugs to nothing gave %+v", symbols)
	}
}

// Hashing stops when the request is cancelled.
func TestHashingHonoursCancellation(t *testing.T) {
	m := newTopic(t)
	big := filepath.Join(t.TempDir(), "big.pdf")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(3 << 30); err != nil { // 3 GiB, sparse
		t.Skipf("cannot make a sparse file: %v", err)
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = m.AddSource(ctx, SourceSpec{Topic: "c", File: big})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Errorf("AddSource with a 20ms deadline: %v after %v", err, time.Since(start))
	}
}

func TestUpdateTopicSaysWhatChangedBeforeAFailure(t *testing.T) {
	m := newTopic(t)
	var n int
	m.crash = func(p string) error {
		if p == crashAfterIntent {
			if n++; n == 2 {
				return errCrash
			}
		}
		return nil
	}
	_, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Title: ptr("New title"), Goal: ptr(""),
		KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	m.crash = nil
	if err == nil || !strings.Contains(err.Error(), "the title of c was changed, but not its Knowledge base") {
		t.Errorf("err = %v, want it to say only the title changed", err)
	}
}

func TestStatusMarksLessonsWithoutEvidence(t *testing.T) {
	m := learningTopic(t)
	practicing(t, m)
	if topic, err := m.readTopic("c"); err != nil || topic.LessonsWithoutEvidence != nil {
		t.Fatalf("without Sources = %v, %v; want nothing marked", topic.LessonsWithoutEvidence, err)
	}

	book := m.addSource(t, SourceSpec{File: writeBook(t, filepath.Join(t.TempDir(), "answers.pdf"), "%PDF-1.4 the answer is 42")})
	topic, err := m.readTopic("c")
	if err != nil || !slices.Equal(topic.LessonsWithoutEvidence, []string{"answer"}) {
		t.Fatalf("a started Lesson citing nothing = %v, %v; want [answer]", topic.LessonsWithoutEvidence, err)
	}

	m.recordEvidence(t, EvidenceSpec{Lesson: "answer", Source: book.ID, Quote: "the answer is 42"})
	if topic, err = m.readTopic("c"); err != nil || topic.LessonsWithoutEvidence != nil {
		t.Errorf("after citing Evidence = %v, %v; want nothing marked", topic.LessonsWithoutEvidence, err)
	}
}
