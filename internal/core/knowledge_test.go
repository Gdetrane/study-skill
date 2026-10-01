package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestKnowledgeWithoutAKnowledgeBase(t *testing.T) {
	ctx := context.Background()
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

	book := writeBook(t, filepath.Join(t.TempDir(), "operating_systems.pdf"), "%PDF-1.4 processes and threads")
	file := m.addSource(t, SourceSpec{File: book})
	if file.Kind != SourceFile || file.Title != "Operating Systems" || file.Path != book ||
		file.Size != int64(len("%PDF-1.4 processes and threads")) || !strings.HasPrefix(file.Hash, "sha256:") ||
		!sourceIDPattern.MatchString(file.ID) || !strings.HasPrefix(file.ID, "operating-systems.") {
		t.Errorf("file Source = %+v", file)
	}
	page := m.addSource(t, SourceSpec{URL: "HTTPS://Go.dev/blog/context", Title: "Go Concurrency Patterns: Context"})
	if page.Kind != SourceURL || page.URL != "https://go.dev/blog/context" || page.Path != "" || page.Hash != "" {
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

	all, err := m.ListEvidence(ctx, "c", "")
	if err != nil || len(all.Evidence) != 2 {
		t.Fatalf("ListEvidence = %+v, %v", all, err)
	}
	one, err := m.ListEvidence(ctx, "c", "lesson-02")
	if err != nil || len(one.Evidence) != 1 || one.Evidence[0].Source != page.ID || one.Lesson != "lesson-02" {
		t.Errorf("ListEvidence(lesson-02) = %+v, %v", one, err)
	}

	list := m.sources(t)
	if list.KnowledgeBase == nil || list.KnowledgeBase.Kind != KnowledgeBaseNone || len(list.Sources) != 2 ||
		list.Sources[0].State != SourceOK || list.Sources[1].State != "" {
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

func TestNotebookLMKnowledgeBase(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	res := m.update(t, TopicChanges{Goal: ptr("Write a small shell"),
		KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNotebookLM, Notebook: "nb-123"}})
	want := KnowledgeBase{Kind: KnowledgeBaseNotebookLM, Notebook: "nb-123"}
	if !res.Changed || res.Topic.Goal != "Write a small shell" || res.Topic.KnowledgeBase == nil || *res.Topic.KnowledgeBase != want {
		t.Fatalf("goal and Knowledge base = %+v", res)
	}
	if n := len(historyLines(t, filepath.Join(m.home, "c"))); n != 3 {
		t.Errorf("History has %d lines, want creation plus two Events", n)
	}

	book := writeBook(t, filepath.Join(t.TempDir(), "unix.pdf"), "%PDF-1.4 fork and exec")
	src := m.addSource(t, SourceSpec{File: book, Title: "The UNIX Programming Environment", NotebookLMID: "nlm-src-1"})
	if src.NotebookLMID != "nlm-src-1" || src.Title != "The UNIX Programming Environment" {
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
	if r := update(SourceChanges{NotebookLMID: ptr("nlm-src-2")}); !r.Changed || r.Source.NotebookLMID != "nlm-src-2" {
		t.Errorf("new NotebookLM id = %+v", r)
	}
	if r := update(SourceChanges{NotebookLMID: ptr("nlm-src-2")}); r.Changed {
		t.Error("the same NotebookLM id recorded a change")
	}
	if r := update(SourceChanges{NotebookLMID: ptr(""), Title: ptr("UNIX")}); !r.Changed || r.Source.NotebookLMID != "" || r.Source.Title != "UNIX" {
		t.Errorf("removing the NotebookLM id = %+v", r)
	}
	if got := m.sources(t).Sources[0]; got.NotebookLMID != "" || got.Title != "UNIX" || got.Hash != src.Hash {
		t.Errorf("listed Source = %+v", got)
	}
	m.recordEvidence(t, EvidenceSpec{Lesson: "processes", Source: src.ID, Quote: "fork creates a process.",
		Location: "Source 1, citation 3", LocationFrom: LocationFromKnowledgeBase})

	// Switching to none drops the notebook along with its kind.
	res = m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	if !res.Changed || *res.Topic.KnowledgeBase != (KnowledgeBase{Kind: KnowledgeBaseNone}) {
		t.Errorf("switching to none = %+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(m.home, "c", topicFile)); strings.Contains(string(data), "nb-123") {
		t.Errorf("topic.toml still names the notebook:\n%s", data)
	}
	checkChain(t, filepath.Join(m.home, "c"))
}

func TestKnowledgeBaseValidation(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	for _, kb := range []KnowledgeBase{
		{Kind: KnowledgeBaseNotebookLM},
		{Kind: KnowledgeBaseNone, Notebook: "nb"},
		{Kind: "rag"},
		{Kind: KnowledgeBaseNotebookLM, Notebook: "two words"},
	} {
		if _, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &kb}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", kb, err)
		}
	}
	before := historyLines(t, filepath.Join(m.home, "c"))
	res, err := m.UpdateTopic(ctx, "c", TopicChanges{DryRun: true, Title: ptr("Systems C"),
		KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNotebookLM, Notebook: "nb"}})
	if err != nil || !res.Changed || res.Topic.Title != "Systems C" || res.Topic.KnowledgeBase.Notebook != "nb" {
		t.Errorf("dry run = %+v, %v", res, err)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); !slices.Equal(before, after) {
		t.Error("a dry run recorded Events")
	}
	if settings := readSettings(t, filepath.Join(m.home, "c")); settings.Title != "C" || knowledgeBaseOf(settings) != nil {
		t.Errorf("a dry run changed topic.toml: %+v", settings)
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
		{"same content", SourceSpec{Topic: "c", File: copyOfBook}, CodeAlreadyExists},
		{"same URL", SourceSpec{Topic: "c", URL: "https://EXAMPLE.com/page"}, CodeAlreadyExists},
		{"unknown Topic", SourceSpec{Topic: "go", URL: "https://example.com/other"}, CodeNotFound},
		{"bad NotebookLM id", SourceSpec{Topic: "c", URL: "https://example.com/other", NotebookLMID: "a b"}, CodeInvalidArgument},
	} {
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
		{"nothing", src.ID, SourceChanges{}, CodeInvalidArgument},
		{"empty title", src.ID, SourceChanges{Title: ptr(" ")}, CodeInvalidArgument},
		{"path of a URL", page.ID, SourceChanges{Path: &book}, CodeInvalidArgument},
	} {
		if _, err := m.UpdateSource(ctx, "c", tc.id, tc.changes); CodeOf(err) != tc.want {
			t.Errorf("update %s: err = %v, want %s", tc.name, err, tc.want)
		}
	}
}

func TestAMovedSourceIsFoundAgainByItsContent(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	books := filepath.Join(t.TempDir(), "Books")
	original := writeBook(t, filepath.Join(books, "OS", "modern_operating_systems.pdf"), "%PDF-1.4 Tanenbaum")
	if _, err := m.BuildLibrary(ctx, books); err != nil {
		t.Fatal(err)
	}
	src := m.addSource(t, SourceSpec{File: original})
	if src.Title != "Modern Operating Systems" {
		t.Errorf("title = %q, want the Library's", src.Title)
	}

	moved := filepath.Join(books, "Systems", "tanenbaum.pdf")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceMissing {
		t.Errorf("before the Library is rebuilt: %+v, want missing", got)
	}
	if _, err := m.BuildLibrary(ctx, books); err != nil {
		t.Fatal(err)
	}
	got := m.sources(t).Sources[0]
	if got.State != SourceMoved || got.FoundAt != moved {
		t.Fatalf("after the Library is rebuilt: %+v, want moved to %s", got, moved)
	}

	other := writeBook(t, filepath.Join(books, "other.pdf"), "%PDF-1.4 another book")
	if _, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Path: &other}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("recording a different file: err = %v, want invalid_argument", err)
	}
	res, err := m.UpdateSource(ctx, "c", src.ID, SourceChanges{Path: &got.FoundAt})
	if err != nil || !res.Changed || res.Source.Path != moved {
		t.Fatalf("recording the move = %+v, %v", res, err)
	}
	if got := m.sources(t).Sources[0]; got.State != SourceOK || got.Path != moved {
		t.Errorf("after the move is recorded: %+v", got)
	}

	writeBook(t, moved, "%PDF-1.4 Tanenbaum, second edition")
	if got := m.sources(t).Sources[0]; got.State != SourceChanged {
		t.Errorf("after the file changed: %+v, want changed", got)
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
	if _, err := m.ListEvidence(ctx, "c", "Lesson 1"); CodeOf(err) != CodeInvalidArgument {
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

// Fields of sources.jsonl that this version does not know survive an update.
func TestUnknownSourceFieldsAreKept(t *testing.T) {
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/page"})
	path := filepath.Join(m.home, "c", sourcesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(data), "}\n") + `,"pages":312}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateSource(context.Background(), "c", src.ID, SourceChanges{Title: ptr("Example")}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"pages":312`) || !strings.Contains(string(data), `"title":"Example"`) {
		t.Errorf("sources.jsonl = %s", data)
	}
}
