package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// v1Options shapes a synthetic v1 workspace. Real workspaces are never used
// in tests: they hold the learner's work.
type v1Options struct {
	noGit     bool
	config    map[string]any
	extra     map[string]string // more files, by relative path, committed
	untracked map[string]string // files written after the commits
	links     map[string]string // links, by relative path → target
	commits   []string          // commit subjects after the initial one
}

// v1Workspace builds a v1 workspace like the v1 skill's lifecycle smoke
// test (scripts/e2e/study-lifecycle-smoke.sh): the go-idiomatic template,
// .study-config.json, lessons/, practice/, notes/ and .fsrs/, with
// [agent]/[user]/[session] commits.
func v1Workspace(t *testing.T, opts v1Options) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go-concurrency")
	cfg := map[string]any{
		"version": 3, "topic": "Go Concurrency", "template": "go-idiomatic", "template_mode": "code-as-subject",
		"approach": "concept", "end_goal": "Write a concurrent web crawler", "difficulty": "beginner",
		"difficulty_override": "intermediate", "next_calibration_at_lesson": 4, "mode": "tutorial",
		"created": "2026-06-14T00:00:00Z", "sciagent_skills": []any{}, "sciagent_primary": nil,
		"progress": map[string]any{"current_lesson": 2, "lessons_completed": 2, "session_count": 3},
		"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "completed"},
			map[string]any{"num": 2, "title": "Channels", "file": "lessons/02-channels.md", "status": "completed"},
			map[string]any{"num": 3, "title": "Select", "file": "lessons/03-select.md", "status": "in_progress"},
		},
		"session_state": map[string]any{"phase": "practicing", "pending_action": "review practice/lesson-03 implementation",
			"context": "The learner was writing a select loop.", "energy": "half", "time_budget_minutes": 25},
		"sources":      []any{},
		"review":       map[string]any{"fsrs_data_path": ".fsrs/cards.json", "items_due": 1},
		"catalog_path": "~/.config/study/book-catalog.json",
		"made_up_key":  true,
	}
	for k, v := range opts.config {
		if v == nil {
			delete(cfg, k)
		} else {
			cfg[k] = v
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".study-config.json":          string(data) + "\n",
		"lessons/plan.md":             "# Plan\n\n## Must\n- Goroutines\n- Channels\n\n## Stretch\n- Select\n",
		"lessons/01-goroutines.md":    "# Lesson 1: Goroutines\n\nCreate your implementation in `practice/lesson-01/`.\n",
		"lessons/02-channels.md":      "# Lesson 2: Channels\n",
		"lessons/03-select.md":        "# Lesson 3: Select\n",
		"practice/lesson-01/main.go":  "package main\n",
		"practice/lesson-03/main.go":  "package main // half done\n",
		"notes/session-2026-06-14.md": "# Session 1\n",
		".fsrs/cards.json":            `{"cards":[{"id":"lesson-01","title":"Goroutines"},{"id":"lesson-02","title":"Channels"}]}` + "\n",
		".gitignore":                  "bin/\n*.exe\n.env\nvendor/\n",
		"go.mod":                      "module example.com/concurrency\n",
		"Makefile":                    "run:\n\tgo run ./cmd/study\n",
		"cmd/study/main.go":           "package main\n\nfunc main() {}\n",
		"internal/.gitkeep":           "",
	}
	for k, v := range opts.extra {
		files[k] = v
	}
	writeFiles(t, dir, files)
	for rel, target := range opts.links {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	if !opts.noGit {
		git(t, dir, "init", "-q", "-b", "main")
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "[agent] init study workspace")
		for _, subject := range opts.commits {
			git(t, dir, "commit", "-q", "--allow-empty", "-m", subject)
		}
	}
	writeFiles(t, dir, opts.untracked)
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// treeHash hashes every path, mode, time, link target and file content
// under dir, .git included, to prove an import left the workspace
// untouched.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h.Write([]byte(rel + "\x00" + info.Mode().String() + "\x00" + info.ModTime().String() + "\x00"))
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			h.Write([]byte(target))
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lessonIDs(ls []ImportedLesson) []string {
	var ids []string
	for _, l := range ls {
		ids = append(ids, l.Lesson)
	}
	return ids
}

func notesWhat(ns []ImportNote) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.What)
	}
	return out
}

// note returns the detail of the note about what, failing the test if
// there is none.
func note(t *testing.T, ns []ImportNote, what string) string {
	t.Helper()
	for _, n := range ns {
		if n.What == what {
			return n.Detail
		}
	}
	t.Errorf("no note about %s in %v", what, notesWhat(ns))
	return ""
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestImportAV1Workspace(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{commits: []string{"[user] lesson 1 work", "[agent] complete lesson 01", "[session] end session 1"}})
	before := treeHash(t, src)
	m := newMachine(t, t.TempDir(), "id", t0)

	dry, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if entries, _ := os.ReadDir(m.home); len(entries) != 0 {
		t.Fatalf("the dry run wrote into the Study home: %v", entries)
	}
	if !dry.DryRun || dry.Topic.ID != "go-concurrency" || dry.Topic.Title != "Go Concurrency" || dry.Topic.Goal != "Write a concurrent web crawler" {
		t.Errorf("dry run Topic = %+v", dry.Topic)
	}
	if dry.Copy.Files < 10 || dry.Copy.Bytes < 100 {
		t.Errorf("the dry run's copy = %+v", dry.Copy)
	}
	if !strings.Contains(note(t, dry.Converted, ".gitignore"), "Lamplight's") {
		t.Errorf("the dry run does not say how .gitignore is merged: %+v", dry.Converted)
	}

	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatalf("ImportV1: %v", err)
	}
	if treeHash(t, src) != before {
		t.Error("the import changed the v1 workspace")
	}

	// Lesson 1 is proven by its completion commit, Lesson 2 by its card;
	// Lesson 3 was in progress.
	if ids := lessonIDs(got.Completed); !slices.Equal(ids, []string{"lesson-01", "lesson-02"}) {
		t.Errorf("completed = %+v", got.Completed)
	}
	if !strings.HasPrefix(got.Completed[0].Proof, "commit ") || !strings.HasSuffix(got.Completed[0].Proof, ": [agent] complete lesson 01") ||
		got.Completed[1].Proof != "v1's card lesson-02" {
		t.Errorf("proofs = %q, %q", got.Completed[0].Proof, got.Completed[1].Proof)
	}
	if ids := lessonIDs(got.Open); !slices.Equal(ids, []string{"lesson-03"}) {
		t.Errorf("open = %+v", got.Open)
	}
	if got.Level != "intermediate" || got.Approach != ApproachConcepts {
		t.Errorf("level %q, approach %q", got.Level, got.Approach)
	}
	if got.NextStep == nil || got.NextStep.PendingAction != "review practice/lesson-03 implementation" {
		t.Errorf("v1 next step = %+v", got.NextStep)
	}
	for what, want := range map[string]string{
		".fsrs": "cards (2)", "template": "templates", "progress": "History", "review": "cards",
		"catalog_path": "Library", "made_up_key": "not known", "created": "import", "difficulty": "difficulty_override",
		"next_calibration_at_lesson": "Assessment", "sciagent_skills": "companion",
	} {
		if detail := note(t, got.Dropped, what); !strings.Contains(detail, want) {
			t.Errorf("dropped %s: %q, want it to mention %q", what, detail, want)
		}
	}
	if got.Checkpoint == "" {
		t.Errorf("no Checkpoint: %s", got.CheckpointError)
	}
	if !slices.Equal(dry.Moved, got.Moved) || !slices.Equal(dry.Dropped, got.Dropped) || !slices.Equal(dry.Converted, got.Converted) ||
		dry.Copy != got.Copy {
		t.Errorf("the dry run reported differently:\n dry %+v\n real %+v", dry, got)
	}

	topic := filepath.Join(m.home, "go-concurrency")
	for _, want := range []string{"lessons/lesson-01.md", "lessons/lesson-02.md", "lessons/lesson-03.md",
		"notes/v1-plan.md", "notes/v1-config.json", "practice/lesson-01/main.go", "go.mod", "cmd/study/main.go",
		"internal/.gitkeep", "topic.toml", "history.jsonl"} {
		if !exists(filepath.Join(topic, want)) {
			t.Errorf("%s is missing", want)
		}
	}
	for _, gone := range []string{".study-config.json", ".fsrs", "lessons/plan.md", "lessons/01-goroutines.md"} {
		if exists(filepath.Join(topic, gone)) {
			t.Errorf("%s is still there", gone)
		}
	}
	ignore, _ := os.ReadFile(filepath.Join(topic, ".gitignore"))
	if !strings.HasPrefix(string(ignore), "bin/\n*.exe\n.env\nvendor/\n") || !strings.Contains(string(ignore), "*.parquet") ||
		!strings.HasSuffix(string(ignore), "!/.gitattributes\n!/.gitignore\n") || strings.Count(string(ignore), "\n.env\n") != 1 {
		t.Errorf(".gitignore = %q", ignore)
	}

	// The git history is kept, and the Checkpoint saved the import.
	log := git(t, topic, "log", "--format=%s")
	for _, want := range []string{"[agent] init study workspace", "[agent] complete lesson 01", "Imported from v1"} {
		if !strings.Contains(log, want) {
			t.Errorf("git log lacks %q:\n%s", want, log)
		}
	}
	if out := git(t, topic, "status", "--porcelain"); strings.TrimSpace(out) != "M history.jsonl" && strings.TrimSpace(out) != "" {
		t.Errorf("git status after the import:\n%s", out)
	}

	// status recommends adopting it from the v1 plan, the proven Lessons
	// are done, and the Level is the import's.
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 1 || status.Topics[0].Imported == nil || status.Topics[0].Imported.Adopted ||
		status.Topics[0].Level == nil || status.Topics[0].Level.Level != "intermediate" {
		t.Fatalf("status = %+v", status.Topics)
	}
	if source := status.Topics[0].Level.Source; source != LevelFromImport {
		t.Errorf("the Level's source = %q, want %q", source, LevelFromImport)
	}
	if status.Recommended == nil || status.Recommended.Action != ActionAdopt || !strings.Contains(status.Recommended.Text, "the v1 plan in notes/v1-plan.md") {
		t.Errorf("recommended = %+v", status.Recommended)
	}
	imp := status.Topics[0].Imported
	if imp.From != got.From || imp.Plan != v1PlanTarget || imp.Config != v1ConfigTarget || !slices.Equal(imp.Completed, got.Completed) ||
		!slices.Equal(imp.Open, got.Open) || !slices.Equal(imp.Dropped, got.Dropped) || imp.More != 0 {
		t.Errorf("status imported = %+v", imp)
	}
	s, _, err := m.replayTopic(ctx, "go-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	if s.study.lessons["lesson-01"].completed == nil || s.study.lessons["lesson-03"] != nil && s.study.lessons["lesson-03"].completed != nil {
		t.Error("the replayed Lessons do not match the import")
	}
	if len(status.Topics[0].Flags) != 0 {
		t.Errorf("flags = %+v", status.Topics[0].Flags)
	}

	// The learner choosing a Level makes it theirs.
	if _, err := m.UpdateTopic(ctx, "go-concurrency", TopicChanges{Level: ptr(LevelAdvanced)}); err != nil {
		t.Fatal(err)
	}
	if topic, err := m.readTopic("go-concurrency"); err != nil || topic.Level == nil || topic.Level.Source != LevelFromLearner {
		t.Errorf("after the learner's choice: %+v, %v", topic.Level, err)
	}
}

func TestAnImportedTopicIsAdoptedThroughARevision(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{commits: []string{"[agent] complete lesson 01"},
		config: map[string]any{"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "completed"},
			map[string]any{"num": 2, "title": "Channels", "file": "lessons/02-channels.md", "status": "planned"},
		}}})
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
		t.Fatal(err)
	}
	opened, err := m.OpenSession(ctx, "go-concurrency", SessionSpec{Energy: EnergyFull, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Suggested == nil || opened.Suggested.Suggest != SuggestAdopt {
		t.Errorf("session_open suggests %+v, want adopt", opened.Suggested)
	}

	// The Syllabus must keep the Lesson the import proved done.
	without := Syllabus{Milestones: []Milestone{{ID: "core", Title: "Core", Lessons: []SyllabusLesson{
		{ID: "lesson-02", Title: "Channels", Hours: 2}}}}}
	if _, err := m.ProposeRevision(ctx, "go-concurrency", RevisionSpec{Summary: "From the v1 plan", Syllabus: without}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a Syllabus without the done Lesson: %v", err)
	}
	with := Syllabus{Milestones: []Milestone{{ID: "core", Title: "Core", Priority: "must", Lessons: []SyllabusLesson{
		{ID: "lesson-01", Title: "Goroutines", Hours: 2}, {ID: "lesson-02", Title: "Channels", Hours: 2}}}}}
	p, err := m.ProposeRevision(ctx, "go-concurrency", RevisionSpec{Summary: "From the v1 plan", Syllabus: with})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "go-concurrency", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	topic := status.Topics[0]
	if topic.Imported == nil || !topic.Imported.Adopted {
		t.Errorf("imported = %+v, want adopted", topic.Imported)
	}
	if status.Recommended == nil || status.Recommended.Action == ActionAdopt || topic.Resume == nil || topic.Resume.Lesson != "lesson-02" {
		t.Errorf("after adoption: recommended %+v, resume %+v", status.Recommended, topic.Resume)
	}
}

// The Lessons an import proved done are v1's work: once adopted, a
// Milestone made only of them does not cue its Assessment, and the history
// view names the import.
func TestAnImportsCompletionsDoNotCueAnAssessment(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{commits: []string{"[agent] complete lesson 01"},
		config: map[string]any{"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "completed"},
			map[string]any{"num": 2, "title": "Channels", "file": "lessons/02-channels.md", "status": "planned"},
		}}})
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
		t.Fatal(err)
	}
	view, err := m.HistoryOf(ctx, "go-concurrency", HistoryQuery{Limit: MaxHistoryLimit})
	if err != nil {
		t.Fatal(err)
	}
	var summaries []string
	for _, e := range view.Entries {
		summaries = append(summaries, e.Summary)
	}
	for _, want := range []string{"Imported from a v1 workspace", "Level set: intermediate, from the v1 workspace"} {
		if !slices.Contains(summaries, want) {
			t.Errorf("the history view lacks %q: %q", want, summaries)
		}
	}

	syllabus := Syllabus{Milestones: []Milestone{
		{ID: "basics", Title: "Basics", Priority: "must", Lessons: []SyllabusLesson{{ID: "lesson-01", Title: "Goroutines", Hours: 2}}},
		{ID: "core", Title: "Core", Priority: "must", Lessons: []SyllabusLesson{{ID: "lesson-02", Title: "Channels", Hours: 2}}},
	}}
	p, err := m.ProposeRevision(ctx, "go-concurrency", RevisionSpec{Summary: "From the v1 plan", Syllabus: syllabus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "go-concurrency", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if topic := status.Topics[0]; topic.AssessmentDue != nil || status.Recommended == nil ||
		status.Recommended.Action == ActionAssess || status.Recommended.Action == ActionAdopt {
		t.Errorf("after adoption: assessment due %+v, recommended %+v", topic.AssessmentDue, status.Recommended)
	}
}

// Without a v1 plan, which only v1's project approach writes, the adoption
// starts from v1's lesson list and the learner's notes.
func TestAnImportWithoutAPlanIsAdoptedFromTheLessonList(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{})
	if err := os.Remove(filepath.Join(src, "lessons", "plan.md")); err != nil {
		t.Fatal(err)
	}
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if imp := status.Topics[0].Imported; imp == nil || imp.Plan != "" || imp.Config != v1ConfigTarget {
		t.Errorf("imported = %+v", imp)
	}
	if r := status.Recommended; r == nil || r.Action != ActionAdopt || !strings.Contains(r.Text, "v1's lesson list in notes/v1-config.json") {
		t.Errorf("recommended = %+v", r)
	}
}

func TestImportMapsSourcesAndTheNotebook(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	outside := filepath.Join(t.TempDir(), "Strang Linear Algebra.pdf")
	if err := os.WriteFile(outside, []byte("%PDF-1.4 outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := v1Workspace(t, v1Options{
		extra: map[string]string{"sources/book.pdf": "%PDF-1.4 inside"},
		config: map[string]any{
			"sources": []any{
				"sources/book.pdf",
				map[string]any{"path": outside, "title": "Linear Algebra", "notebook_id": "nb-1", "source_id": "s-9"},
				map[string]any{"url": "https://go.dev/doc/effective_go", "source_id": "s-10"},
				map[string]any{"path": "missing.pdf"},
				map[string]any{"url": "https://go.dev/doc/effective_go"},
			},
			"notebooklm": map[string]any{"notebook_id": "nb-2"},
		}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	if got.KnowledgeBase == nil || got.KnowledgeBase.Kind != KnowledgeBaseNotebookLM || got.KnowledgeBase.Notebook != "nb-2" {
		t.Errorf("knowledge base = %+v", got.KnowledgeBase)
	}
	if len(got.Sources) != 3 {
		t.Fatalf("sources = %+v", got.Sources)
	}
	// A Source keeps the notebook it declared; one that declared none
	// takes the Knowledge base's.
	if got.Sources[0].TopicPath != "sources/book.pdf" || got.Sources[1].FileName != "Strang Linear Algebra.pdf" ||
		got.Sources[1].NotebookLMID != "s-9" || got.Sources[1].NotebookLMNotebook != "nb-1" ||
		got.Sources[2].URL != "https://go.dev/doc/effective_go" || got.Sources[2].NotebookLMNotebook != "nb-2" {
		t.Errorf("sources = %+v", got.Sources)
	}
	dropped := strings.Join(notesWhat(got.Dropped), ",")
	if !strings.Contains(dropped, "sources[3]") || !strings.Contains(dropped, "sources[4]") || !strings.Contains(dropped, "NotebookLM notebook nb-1") {
		t.Errorf("dropped = %s", dropped)
	}
	stored, err := os.ReadFile(filepath.Join(m.home, "go-concurrency", sourcesFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"notebooklm_id":"s-9","notebooklm_notebook":"nb-1"`) ||
		!strings.Contains(string(stored), `"notebooklm_id":"s-10","notebooklm_notebook":"nb-2"`) {
		t.Errorf("%s = %s", sourcesFile, stored)
	}
	list, err := m.ListSources(ctx, "go-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sources) != 3 {
		t.Fatalf("listed = %+v", list.Sources)
	}
	for _, s := range list.Sources {
		if s.Kind == SourceFile && s.State != SourceOK {
			t.Errorf("Source %s is %s, want ok", s.ID, s.State)
		}
	}
}

// v1 wrote NotebookLM notebooks in several shapes: each is read, or listed
// under Dropped with why.
func TestImportReadsEveryShapeOfNotebookLM(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	for _, tc := range []struct {
		name       string
		notebooklm any
		kb         string
		dropped    map[string]string
	}{
		{"an id", "nb-1", "nb-1", nil},
		{"an address", "https://notebooklm.google.com/notebook/0a1b-2c3d?authuser=1", "0a1b-2c3d", nil},
		{"an object with an address", map[string]any{"url": "https://notebooklm.google.com/notebook/nb-u/"}, "nb-u", nil},
		{"a list of notebooks", map[string]any{"notebooks": []any{map[string]any{"id": "nb-q"},
			"https://notebooklm.google.com/notebook/nb-r", map[string]any{"name": "Notes"}}}, "nb-q",
			map[string]string{"NotebookLM notebook nb-r": "one Knowledge base", "notebooklm.notebooks[2]": "names no notebook"}},
		{"another site", "https://example.com/notebook/x", "", map[string]string{"notebooklm": "not a NotebookLM address"}},
		{"an address without a notebook", map[string]any{"url": "https://notebooklm.google.com/"}, "",
			map[string]string{"notebooklm": "names no notebook"}},
		{"an object without one", map[string]any{"title": "Go"}, "", map[string]string{"notebooklm": "names no notebook"}},
		{"a number", 7, "", map[string]string{"notebooklm": "neither"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := v1Workspace(t, v1Options{noGit: true, config: map[string]any{"notebooklm": tc.notebooklm}})
			got, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.kb == "" && got.KnowledgeBase != nil:
				t.Errorf("knowledge base = %+v, want none", got.KnowledgeBase)
			case tc.kb != "" && (got.KnowledgeBase == nil || got.KnowledgeBase.Notebook != tc.kb):
				t.Errorf("knowledge base = %+v, want %s", got.KnowledgeBase, tc.kb)
			}
			for what, want := range tc.dropped {
				if detail := note(t, got.Dropped, what); !strings.Contains(detail, want) {
					t.Errorf("dropped %s: %q, want %q", what, detail, want)
				}
			}
		})
	}
}

func TestImportRefusals(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	refused := func(t *testing.T, spec ImportSpec, code ErrorCode, says string) {
		t.Helper()
		there := spec.ID == "" || exists(filepath.Join(m.home, spec.ID))
		_, err := m.ImportV1(ctx, spec)
		if CodeOf(err) != code || !strings.Contains(fmt.Sprint(err), says) {
			t.Errorf("got %v (%s), want %s saying %q", err, CodeOf(err), code, says)
		}
		if !there && exists(filepath.Join(m.home, spec.ID)) {
			t.Errorf("a refused import left Topic %s", spec.ID)
		}
	}

	t.Run("not v1", func(t *testing.T) {
		refused(t, ImportSpec{Dir: t.TempDir()}, CodeInvalidArgument, "not a v1 workspace")
	})
	t.Run("missing", func(t *testing.T) {
		refused(t, ImportSpec{Dir: filepath.Join(t.TempDir(), "missing")}, CodeNotFound, "does not exist")
	})
	t.Run("a Topic already", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true, extra: map[string]string{"topic.toml": "title = \"x\"\n"}})
		refused(t, ImportSpec{Dir: src, ID: "topic-already"}, CodeInvalidArgument, "a Lamplight Topic already")
	})
	t.Run("inside the Study home", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true})
		inHome := filepath.Join(m.home, "old-v1", "go-concurrency")
		if err := os.MkdirAll(filepath.Dir(inHome), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(src, inHome); err != nil {
			t.Fatal(err)
		}
		refused(t, ImportSpec{Dir: inHome, ID: "in-home"}, CodeInvalidArgument, "inside the Study home already")
	})
	t.Run("holding the Study home", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true})
		holder := newMachine(t, filepath.Join(src, "study"), "id", t0)
		_, err := holder.ImportV1(ctx, ImportSpec{Dir: src})
		if CodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "the Study home") || !strings.Contains(err.Error(), "is inside") {
			t.Errorf("a workspace holding the Study home: %v", err)
		}
	})
	t.Run("again", func(t *testing.T) {
		src := v1Workspace(t, v1Options{})
		if _, err := m.ImportV1(ctx, ImportSpec{Dir: src, ID: "first"}); err != nil {
			t.Fatal(err)
		}
		refused(t, ImportSpec{Dir: src, ID: "first"}, CodeAlreadyExists, "already exists")
		refused(t, ImportSpec{Dir: src, ID: "again"}, CodeAlreadyExists, "imported already, as Topic first")
		// The same workspace through a link to it is the same workspace.
		link := filepath.Join(t.TempDir(), "via-link")
		if err := os.Symlink(src, link); err != nil {
			t.Fatal(err)
		}
		refused(t, ImportSpec{Dir: link, ID: "via-link"}, CodeAlreadyExists, "imported already")
	})
	t.Run("a linked worktree", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true, extra: map[string]string{".git": "gitdir: /elsewhere\n"}})
		refused(t, ImportSpec{Dir: src, ID: "worktree"}, CodeFailedPrecondition, "linked worktree")
	})
	t.Run("a newer config", func(t *testing.T) {
		refused(t, ImportSpec{Dir: v1Workspace(t, v1Options{config: map[string]any{"version": 9}}), ID: "newer"},
			CodeInvalidArgument, "version 9")
	})
	t.Run("an invalid id", func(t *testing.T) {
		refused(t, ImportSpec{Dir: v1Workspace(t, v1Options{noGit: true}), ID: "Not An Id"}, CodeInvalidArgument, "")
	})
	t.Run("an unknown Lesson to keep open", func(t *testing.T) {
		refused(t, ImportSpec{Dir: v1Workspace(t, v1Options{noGit: true}), ID: "not-done", NotDone: []string{"lesson-09"}},
			CodeInvalidArgument, "--not-done names lesson-09")
	})
	t.Run("a git command running", func(t *testing.T) {
		for _, lock := range []string{".git/index.lock", ".git/HEAD.lock", ".git/packed-refs.lock", ".git/refs/heads/main.lock"} {
			src := v1Workspace(t, v1Options{})
			writeFiles(t, src, map[string]string{lock: ""})
			refused(t, ImportSpec{Dir: src, ID: "busy"}, CodeBusy, lock+" exists")
		}
	})
	t.Run("too many Lessons", func(t *testing.T) {
		lessons := make([]any, maxV1Lessons+1)
		for i := range lessons {
			lessons[i] = map[string]any{"num": i + 1, "title": "L", "status": "planned"}
		}
		refused(t, ImportSpec{Dir: v1Workspace(t, v1Options{noGit: true, config: map[string]any{"lessons": lessons}}), ID: "many"},
			CodeInvalidArgument, "the importer takes at most")
	})
	t.Run("a config too big", func(t *testing.T) {
		big := v1Workspace(t, v1Options{noGit: true, config: map[string]any{"made_up_key": strings.Repeat("x", maxV1ConfigBytes)}})
		refused(t, ImportSpec{Dir: big, ID: "big"}, CodeInvalidArgument, "bigger than")
	})
	t.Run("a config that is a FIFO", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true})
		config := filepath.Join(src, v1ConfigFile)
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(config, 0o644); err != nil {
			t.Skipf("no FIFOs here: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := m.ImportV1(ctx, ImportSpec{Dir: src, ID: "fifo"})
			done <- err
		}()
		select {
		case err := <-done:
			if CodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("a FIFO config: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the import waits on a FIFO")
		}
	})
	t.Run("a .gitignore folder", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true})
		if err := os.Remove(filepath.Join(src, ".gitignore")); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, src, map[string]string{".gitignore/x": "x"})
		refused(t, ImportSpec{Dir: src, ID: "ignore-folder"}, CodeFailedPrecondition, ".gitignore in the workspace is a folder")
	})
	t.Run("a clashing file with nowhere to go", func(t *testing.T) {
		src := v1Workspace(t, v1Options{noGit: true, extra: map[string]string{"cards.jsonl": "{}\n", "notes/v1-cards.jsonl": "{}\n"}})
		refused(t, ImportSpec{Dir: src, ID: "clash"}, CodeFailedPrecondition, "notes/v1-cards.jsonl is taken")
	})
}

// A repository that borrows objects (git clone --shared) is refused, and
// the advice given makes it importable.
func TestImportRefusesBorrowedObjectsAndTheAdviceWorks(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	origin := v1Workspace(t, v1Options{commits: []string{"[agent] complete lesson 01"}})
	shared := filepath.Join(t.TempDir(), "shared-workspace")
	git(t, filepath.Dir(shared), "clone", "-q", "--shared", origin, shared)
	m := newMachine(t, t.TempDir(), "id", t0)
	_, err := m.ImportV1(ctx, ImportSpec{Dir: shared})
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "git repack -a -d") ||
		!strings.Contains(err.Error(), "delete .git/objects/info/alternates") {
		t.Fatalf("a repository with alternates: %v", err)
	}

	// The advice, as given.
	git(t, shared, "repack", "-a", "-d")
	if err := os.Remove(filepath.Join(shared, ".git", "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}
	git(t, shared, "fsck", "--full")
	got, err := m.ImportV1(ctx, ImportSpec{Dir: shared})
	if err != nil {
		t.Fatal(err)
	}
	topic := filepath.Join(m.home, got.Topic.ID)
	git(t, topic, "fsck", "--full")
	if log := git(t, topic, "log", "--format=%s"); !strings.Contains(log, "[agent] complete lesson 01") {
		t.Errorf("the imported history:\n%s", log)
	}
}

// Links leading outside the workspace, directly or through another link,
// are left out and listed; links inside it are copied as links.
func TestImportDropsLinksLeadingOut(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{links: map[string]string{
		"notes/secrets":   "/etc",
		"notes/up":        "../../elsewhere",
		"notes/latest.md": "session-2026-06-14.md",
		"notes/dangling":  "not-written-yet.md",
		"x/root":          "..",
		"y":               "x/root/../elsewhere",
	}})
	elsewhere := filepath.Join(filepath.Dir(src), "elsewhere")
	writeFiles(t, elsewhere, map[string]string{"secret.txt": "not the learner's to copy"})
	if err := os.Symlink(filepath.Join(src, "go.mod"), filepath.Join(src, "absolute-inside")); err != nil {
		t.Fatal(err)
	}
	m := newMachine(t, t.TempDir(), "id", t0)
	// A link that appears after the scan is checked as it is copied.
	m.crash = func(p string) error {
		if p == crashImportCopying {
			return os.Symlink("/etc", filepath.Join(src, "notes", "late"))
		}
		return nil
	}
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatalf("links refused the import: %v", err)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	if exists(filepath.Join(topic, "notes", "late")) {
		t.Error("a link leading out, made after the scan, was copied")
	}
	for link, want := range map[string]string{"notes/secrets": "outside the workspace", "notes/up": "outside the workspace",
		"y": "through another link", "absolute-inside": "outside the workspace"} {
		if !strings.Contains(note(t, got.Dropped, link), want) {
			t.Errorf("dropped %s: %q", link, note(t, got.Dropped, link))
		}
		if exists(filepath.Join(topic, link)) {
			t.Errorf("%s was copied", link)
		}
	}
	for link, target := range map[string]string{"notes/latest.md": "session-2026-06-14.md", "notes/dangling": "not-written-yet.md", "x/root": ".."} {
		if got, err := os.Readlink(filepath.Join(topic, link)); err != nil || got != target {
			t.Errorf("%s = %q, %v; want a link to %s", link, got, err, target)
		}
	}
}

// Folders the language's tools rebuild are left out unless the learner
// committed them; every other file is copied, git-ignored or not.
func TestImportSkipsRebuildableFolders(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{
		extra: map[string]string{
			".gitignore":                  "*.parquet\n__pycache__/\n",
			"web/node_modules/pad/x.js":   "committed\n",
			"practice/rust/Cargo.toml":    "[package]\n",
			"notes/target/goal.md":        "my target\n",
			"practice/lesson-01/venv/a.r": "not python\n",
		},
		untracked: map[string]string{
			"node_modules/left-pad/index.js":           "module.exports = 1\n",
			"practice/lesson-01/__pycache__/m.pyc":     "pyc",
			"practice/rust/target/debug/app":           "binary",
			"env/pyvenv.cfg":                           "home = /usr/bin\n",
			"env/lib/site.py":                          "x",
			".pytest_cache/v/cache/lastfailed":         "{}",
			"data/prices.parquet":                      "learner data",
			"practice/lesson-01/.mypy_cache/3.12/x.db": "x",
		},
	})
	if python, err := exec.LookPath("python3"); err == nil {
		cmd := exec.Command(python, "-m", "venv", "--without-pip", filepath.Join(src, ".venv"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("python3 -m venv: %v\n%s", err, out)
		}
	} else {
		writeFiles(t, src, map[string]string{".venv/pyvenv.cfg": "home = /usr/bin\n", ".venv/lib/python3.12/site-packages/x.py": "x"})
		if err := os.Symlink("/usr/bin/python3", filepath.Join(src, ".venv", "python")); err != nil {
			t.Fatal(err)
		}
	}
	m := newMachine(t, t.TempDir(), "id", t0)
	dry, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dry.Dropped, got.Dropped) || dry.Copy != got.Copy {
		t.Errorf("the dry run and the import disagree:\n %+v\n %+v", dry.Dropped, got.Dropped)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	for _, skipped := range []string{".venv", "node_modules", "practice/lesson-01/__pycache__", "practice/rust/target", "env",
		".pytest_cache", "practice/lesson-01/.mypy_cache"} {
		if !strings.Contains(note(t, got.Dropped, skipped), "rebuild it with the language's tools") {
			t.Errorf("dropped %s: %q", skipped, note(t, got.Dropped, skipped))
		}
		if exists(filepath.Join(topic, skipped)) {
			t.Errorf("%s was copied", skipped)
		}
	}
	for _, kept := range []string{"web/node_modules/pad/x.js", "notes/target/goal.md", "data/prices.parquet",
		"practice/lesson-01/venv/a.r", "practice/rust/Cargo.toml"} {
		if !exists(filepath.Join(topic, kept)) {
			t.Errorf("%s was not copied", kept)
		}
	}
	// The committed node_modules is still committed.
	if out := git(t, topic, "ls-files", "web/node_modules"); !strings.Contains(out, "web/node_modules/pad/x.js") {
		t.Errorf("the committed node_modules: %q", out)
	}
}

// Every move is planned up front: a file moves once, nothing under .git
// moves, only regular files under lessons/ move, and no move replaces a
// file. The dry run and the import agree.
func TestImportMovesOnlyLessonFilesItCanMove(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{
		extra: map[string]string{"lessons/05-taken.md": "# Lesson 5\n", "lessons/lesson-05.md": "# Not lesson 5\n"},
		links: map[string]string{"lessons/06-link.md": "05-taken.md"},
		config: map[string]any{"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "in_progress"},
			map[string]any{"num": 2, "title": "Goroutines part 2", "file": "lessons/01-goroutines.md", "status": "planned"},
			map[string]any{"num": 3, "title": "Sneaky", "file": ".git/HEAD", "status": "completed"},
			map[string]any{"num": 4, "title": "Module", "file": "go.mod", "status": "planned"},
			map[string]any{"num": 5, "title": "Taken", "file": "lessons/05-taken.md", "status": "planned"},
			map[string]any{"num": 6, "title": "Link", "file": "lessons/06-link.md", "status": "planned"},
			map[string]any{"num": 7, "title": "Up", "file": "lessons/../../x.md", "status": "planned"},
		}},
		commits: []string{"[agent] complete lesson 03"},
	})
	m := newMachine(t, t.TempDir(), "id", t0)
	dry, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	want := []ImportMove{{From: "lessons/01-goroutines.md", To: "lessons/lesson-01.md"},
		{From: v1PlanFile, To: v1PlanTarget}, {From: v1ConfigFile, To: v1ConfigTarget}}
	if !slices.Equal(got.Moved, want) || !slices.Equal(dry.Moved, got.Moved) {
		t.Errorf("moved = %+v, dry run %+v", got.Moved, dry.Moved)
	}
	for what, says := range map[string]string{
		"lesson file of lesson-02":   "shared with lesson-01",
		"lesson file of lesson-03":   "not under lessons/",
		"lesson file of lesson-04":   "not under lessons/",
		"moving lessons/05-taken.md": "lessons/lesson-05.md is taken",
		"lesson file of lesson-06":   "not a file",
		"lesson file of lesson-07":   "outside the workspace",
	} {
		if detail := note(t, got.Dropped, what); !strings.Contains(detail, says) {
			t.Errorf("%s: %q, want %q", what, detail, says)
		}
	}
	// .git/HEAD proves nothing as a Lesson file, and stays where it is.
	if ids := lessonIDs(got.Completed); len(ids) != 0 {
		t.Errorf("completed = %v", ids)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	for _, kept := range []string{"go.mod", "lessons/05-taken.md", "lessons/lesson-05.md", "lessons/lesson-01.md"} {
		if !exists(filepath.Join(topic, kept)) {
			t.Errorf("%s is missing", kept)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(topic, "lessons", "lesson-05.md")); string(data) != "# Not lesson 5\n" {
		t.Errorf("lessons/lesson-05.md was replaced: %q", data)
	}
	git(t, topic, "fsck")
	if log := git(t, topic, "log", "-1", "--format=%s"); !strings.Contains(log, "Imported from v1") {
		t.Errorf("the repository after the import: %q", log)
	}
}

// When notes is a file, the plan and the config stay where they are, and
// the import still succeeds.
func TestImportKeepsFilesWhoseTargetFolderIsAFile(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{noGit: true})
	if err := os.RemoveAll(filepath.Join(src, "notes")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, src, map[string]string{"notes": "my notes, in one file\n"})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	if detail := note(t, got.Dropped, "moving "+v1PlanFile); !strings.Contains(detail, "notes is not a folder") {
		t.Errorf("the plan: %q", detail)
	}
	if detail := note(t, got.Dropped, "moving "+v1ConfigFile); !strings.Contains(detail, "notes is not a folder") {
		t.Errorf("the config: %q", detail)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	for _, kept := range []string{v1PlanFile, v1ConfigFile, "notes"} {
		if !exists(filepath.Join(topic, kept)) {
			t.Errorf("%s is missing", kept)
		}
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if imp := status.Topics[0].Imported; imp.Plan != v1PlanFile || imp.Config != v1ConfigFile {
		t.Errorf("imported = %+v", imp)
	}
}

// v1 files named like Lamplight's own move to notes/v1-<name>.
func TestImportMovesFilesNamedLikeLamplights(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{extra: map[string]string{
		"cards.jsonl": "v1 export\n", "syllabus.toml": "not a Syllabus\n", "tasks.jsonl/x": "a folder\n",
	}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	for name, content := range map[string]string{"cards.jsonl": "v1 export\n", "syllabus.toml": "not a Syllabus\n", "tasks.jsonl/x": "a folder\n"} {
		if data, err := os.ReadFile(filepath.Join(topic, "notes", "v1-"+name)); err != nil || string(data) != content {
			t.Errorf("notes/v1-%s = %q, %v", name, data, err)
		}
		if root := strings.Split(name, "/")[0]; exists(filepath.Join(topic, root)) {
			t.Errorf("%s is still at the top of the Topic", root)
		}
		if detail := note(t, got.Converted, strings.Split(name, "/")[0]); !strings.Contains(detail, "kept as notes/v1-") {
			t.Errorf("converted %s: %q", name, detail)
		}
	}
	status, err := m.Status(ctx)
	if err != nil || len(status.Topics) != 1 || len(status.Topics[0].Flags) != 0 {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

// A completion is proven only by the commit v1 writes, "[agent] complete
// lesson NN", matched whole and not reverted since, or by v1's card.
func TestImportProvesLessonsByV1sCompletionCommit(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	lessons := []any{}
	for _, n := range []int{1, 2, 3, 4, 5, 6, 10} {
		file := fmt.Sprintf("lessons/%02d-lesson.md", n)
		lessons = append(lessons, map[string]any{"num": n, "title": fmt.Sprintf("Lesson %d", n), "file": file, "status": "completed"})
	}
	extra := map[string]string{".fsrs/cards.json": "[]\n"}
	for _, n := range []int{1, 2, 3, 4, 5, 6, 10} {
		extra[fmt.Sprintf("lessons/%02d-lesson.md", n)] = "# Lesson\n"
	}
	src := v1Workspace(t, v1Options{extra: extra, config: map[string]any{"lessons": lessons}, commits: []string{
		"[agent] complete lesson 10",
		"[user] I did not complete lesson 2",
		"[agent] complete lesson 03",
		`Revert "[agent] complete lesson 03"`,
		"[agent] complete lesson 4.5",
		"[agent] completed lesson 5",
		"fixup! [agent] complete lesson 06",
	}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if ids := lessonIDs(got.Completed); !slices.Equal(ids, []string{"lesson-05", "lesson-10"}) {
		t.Errorf("completed = %v", ids)
	}
	if ids := lessonIDs(got.Open); !slices.Equal(ids, []string{"lesson-01", "lesson-02", "lesson-03", "lesson-04", "lesson-06"}) {
		t.Errorf("open = %v", ids)
	}
	for _, l := range got.Completed {
		if want := fmt.Sprintf(": [agent] complete%s lesson %s", map[bool]string{true: "d"}[l.Lesson == "lesson-05"],
			strings.TrimPrefix(strings.TrimPrefix(l.Lesson, "lesson-"), "0")); !strings.HasSuffix(l.Proof, want) {
			t.Errorf("%s's proof = %q, want it to end %q", l.Lesson, l.Proof, want)
		}
	}
	for _, l := range got.Open {
		if !strings.Contains(l.V1Status, `not proven: no "[agent] complete lesson `) {
			t.Errorf("%s: %q", l.Lesson, l.V1Status)
		}
	}

	// The learner keeps a Lesson open that the records prove done.
	kept, err := m.ImportV1(ctx, ImportSpec{Dir: src, NotDone: []string{"lesson-10"}})
	if err != nil {
		t.Fatal(err)
	}
	if ids := lessonIDs(kept.Completed); !slices.Equal(ids, []string{"lesson-05"}) {
		t.Errorf("completed with --not-done = %v", ids)
	}
	if i := slices.Index(lessonIDs(kept.Open), "lesson-10"); i < 0 || !strings.Contains(kept.Open[i].V1Status, "kept open: --not-done") {
		t.Errorf("open with --not-done = %+v", kept.Open)
	}
	s, _, err := m.replayTopic(ctx, "go-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	if l := s.study.lessons["lesson-10"]; l != nil && l.completed != nil {
		t.Error("lesson-10 was completed despite --not-done")
	}
}

func TestLessonsV1CalledDoneAreProvenOrLeftOpen(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{
		commits: []string{"[agent] complete lesson 3", "[agent] complete lesson 05"},
		extra:   map[string]string{".fsrs/cards.json": "[]\n"},
		config: map[string]any{"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "completed"},
			map[string]any{"num": 3, "title": "Select", "file": "lessons/03-select.md", "status": "done"},
			map[string]any{"num": 4, "title": "No file", "file": "lessons/04-nothing.md", "status": "completed"},
			map[string]any{"num": 5, "title": "Proven but no file", "file": "lessons/05-gone.md", "status": "completed"},
			map[string]any{"title": "No number", "file": "lessons/notes.md", "status": "planned"},
			map[string]any{"num": 3, "title": "Twice", "file": "lessons/03-select.md", "status": "planned"},
		}}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if ids := lessonIDs(got.Completed); !slices.Equal(ids, []string{"lesson-03"}) {
		t.Errorf("completed = %+v", got.Completed)
	}
	if ids := lessonIDs(got.Open); !slices.Equal(ids, []string{"lesson-01", "lesson-04", "lesson-05"}) {
		t.Errorf("open = %+v", got.Open)
	}
	for _, l := range got.Open {
		if !strings.Contains(l.V1Status, "not proven") {
			t.Errorf("open Lesson %s says %q", l.Lesson, l.V1Status)
		}
	}
	dropped := strings.Join(notesWhat(got.Dropped), ",")
	for _, want := range []string{"lessons[4]", "lessons[5]", "lesson file of lesson-04"} {
		if !strings.Contains(dropped, want) {
			t.Errorf("dropped %s lacks %s", dropped, want)
		}
	}
}

// Each Lesson entry is read on its own: a bad one is dropped with why, and
// whole numbers written another way are read.
func TestImportReadsLessonsOneAtATime(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{noGit: true, config: map[string]any{
		"difficulty_override_at_lesson": 5,
		"lessons": []any{
			map[string]any{"num": "1", "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "planned",
				"metrics": map[string]any{"review_rounds": 2}},
			map[string]any{"num": 2.0, "title": "Channels", "file": "lessons/02-channels.md", "status": "planned",
				"metrics": map[string]any{"hints_requested": 1}},
			map[string]any{"num": 1.5, "title": "Half"},
			map[string]any{"num": "two", "title": "Words"},
			map[string]any{"num": true, "title": "Yes"},
			"lesson 4",
			map[string]any{"num": 3, "title": 7, "file": "lessons/03-select.md", "status": "planned", "mood": "good"},
		}}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if ids := lessonIDs(got.Open); !slices.Equal(ids, []string{"lesson-01", "lesson-02", "lesson-03"}) {
		t.Errorf("open = %+v", got.Open)
	}
	if got.Open[2].Title != "lesson-03" {
		t.Errorf("a Lesson whose title is not text = %+v", got.Open[2])
	}
	for what, want := range map[string]string{
		"lessons[2]":                    "1.5, is not a whole number",
		"lessons[3]":                    `"two", is not a whole number`,
		"lessons[4]":                    "true, is not a whole number",
		"lessons[5]":                    "not an object",
		"lessons[6].title":              "not text",
		"lessons[].metrics":             "performance metrics (review rounds, hints, ratings): v2 records Attempts, Hints and Assessments instead (in 2 Lessons)",
		"lessons[].mood":                "not known to the importer",
		"difficulty_override_at_lesson": "the History records when a Level is set",
	} {
		if detail := note(t, got.Dropped, what); !strings.Contains(detail, want) {
			t.Errorf("dropped %s: %q, want %q", what, detail, want)
		}
	}
}

// v1's .gitignore and .gitattributes come first, Lamplight's lines last, so
// v1 rules cannot leave the state files out of Checkpoints or change how
// they merge; the History records the merged .gitattributes.
func TestImportMergesTheGitFilesWithLamplightsLast(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{
		extra: map[string]string{
			".gitignore":       "*.jsonl\n*.toml\n.*\n!.study-config.json\n!.gitignore\n!.gitattributes\n!.fsrs/\n",
			".gitattributes":   "*.jsonl filter=lfs diff=lfs merge=lfs -text\n*.toml text eol=crlf\n",
			"sources/book.pdf": "%PDF-1.4",
		},
		config: map[string]any{"sources": []any{"sources/book.pdf"}},
	})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	if got.Checkpoint == "" {
		t.Fatalf("no Checkpoint: %s", got.CheckpointError)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	tracked := git(t, topic, "ls-files")
	for _, f := range []string{topicFile, historyFile, sourcesFile, gitattributes, gitignore} {
		if !slices.Contains(strings.Split(tracked, "\n"), f) {
			t.Errorf("%s is not tracked:\n%s", f, tracked)
		}
	}
	attrs := git(t, topic, "check-attr", "merge", "filter", "text", "eol", "--", historyFile, topicFile)
	for _, want := range []string{"history.jsonl: merge: union", "history.jsonl: filter: unspecified", "history.jsonl: text: unspecified",
		"topic.toml: eol: unspecified", "topic.toml: merge: unspecified"} {
		if !strings.Contains(attrs, want) {
			t.Errorf("git check-attr lacks %q:\n%s", want, attrs)
		}
	}
	data, err := os.ReadFile(filepath.Join(topic, gitattributes))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "*.jsonl filter=lfs") {
		t.Errorf("the learner's lines are not first:\n%s", data)
	}
	ignore, _ := os.ReadFile(filepath.Join(topic, gitignore))
	if !strings.HasPrefix(string(ignore), "*.jsonl\n") || !strings.HasSuffix(string(ignore), "!/"+gitignore+"\n") {
		t.Errorf(".gitignore:\n%s", ignore)
	}

	// The History recorded the .gitattributes as it is.
	f, err := os.Open(filepath.Join(topic, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recorded := ""
	for sc := bufio.NewScanner(f); sc.Scan(); {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		for _, it := range ev.Items {
			if it.Item == gitattributes {
				recorded = it.After
			}
		}
	}
	if recorded != contentHash(data, true) {
		t.Errorf("the History records %s for .gitattributes, the file hashes to %s", recorded, contentHash(data, true))
	}
}

// A Topic whose History holds two imports, as a merge of two machines'
// could, is flagged, and the first import counts.
func TestImportedTwiceIsFlagged(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: v1Workspace(t, v1Options{})}); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(m.home, "go-concurrency", historyFile)
	data, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	var again event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == eventTopicImported {
			again = ev
		}
	}
	first := again.ID
	again.ID, again.Time, again.Items = "second-import", again.Time.Add(time.Hour), nil
	line, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, append(data, append(line, '\n')...), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("go-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagConflict || !strings.Contains(topic.Flags[0].Message, "imported twice") ||
		!slices.Equal(topic.Flags[0].Events, []string{first, "second-import"}) {
		t.Errorf("flags = %+v", topic.Flags)
	}
}

// status shows an import's report, each list cut to its first 100 entries.
func TestImportStatusCarriesTheBoundedReport(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	lessons := make([]any, 120)
	for i := range lessons {
		lessons[i] = map[string]any{"num": i + 1, "title": fmt.Sprintf("Lesson %d", i+1), "status": "planned"}
	}
	src := v1Workspace(t, v1Options{noGit: true, config: map[string]any{"lessons": lessons}})
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("go-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	imp := topic.Imported
	if len(imp.Open) != maxImportedShown || len(imp.Dropped) != maxImportedShown ||
		imp.More != len(got.Open)-maxImportedShown+len(got.Dropped)-maxImportedShown {
		t.Errorf("imported shows %d open, %d dropped, %d more; the import had %d and %d",
			len(imp.Open), len(imp.Dropped), imp.More, len(got.Open), len(got.Dropped))
	}
}

func TestImportWithoutGitOrLessons(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{noGit: true, config: map[string]any{"lessons": []any{}, "session_state": nil,
		"end_goal": nil, "difficulty_override": nil, "approach": "challenge"}})
	if err := os.Remove(filepath.Join(src, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src, ID: "minimal"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Approach != ApproachChallenges || got.Level != "beginner" || len(got.Completed) != 0 || got.NextStep != nil {
		t.Errorf("import = %+v", got)
	}
	if !strings.Contains(strings.Join(notesWhat(got.Dropped), ","), "git history") {
		t.Errorf("dropped = %+v", got.Dropped)
	}
	topic := filepath.Join(m.home, "minimal")
	if !exists(filepath.Join(topic, ".git", "HEAD")) {
		t.Error("the imported Topic has no git repository")
	}
	if ignore, _ := os.ReadFile(filepath.Join(topic, gitignore)); string(ignore) != defaultGitignoreForTest() {
		t.Errorf("without a v1 .gitignore, the Topic's = %q", ignore)
	}
}

func defaultGitignoreForTest() string {
	data, _ := os.ReadFile(filepath.Join("..", "checkpoint", "gitignore.go"))
	_, rest, _ := strings.Cut(string(data), "const defaultGitignore = `")
	body, _, _ := strings.Cut(rest, "`")
	return body
}

func TestImportCopiesLinksAndSkipsSpecialFiles(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{links: map[string]string{"notes/latest.md": "session-2026-06-14.md"}})
	if err := syscall.Mkfifo(filepath.Join(src, "notes", "pipe"), 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	m := newMachine(t, t.TempDir(), "id", t0)
	got, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if err != nil {
		t.Fatal(err)
	}
	topic := filepath.Join(m.home, "go-concurrency")
	if target, err := os.Readlink(filepath.Join(topic, "notes", "latest.md")); err != nil || target != "session-2026-06-14.md" {
		t.Errorf("the link = %q, %v", target, err)
	}
	if exists(filepath.Join(topic, "notes", "pipe")) {
		t.Error("the FIFO was copied")
	}
	if !strings.Contains(note(t, got.Dropped, "notes/pipe"), "not copied") {
		t.Errorf("dropped = %+v", got.Dropped)
	}
}

func TestACrashedImportLeavesNoTopic(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	for _, point := range []string{crashImportCopying, crashImportCopied, crashImportStaged} {
		t.Run(point, func(t *testing.T) {
			src := v1Workspace(t, v1Options{})
			m := newMachine(t, t.TempDir(), "id", t0)
			m.crash = func(p string) error {
				if p == point {
					return errCrash
				}
				return nil
			}
			if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err == nil {
				t.Fatal("the import did not crash")
			}
			m.crash = nil
			if exists(filepath.Join(m.home, "go-concurrency")) {
				t.Fatal("a crashed import left a Topic")
			}
			if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
				t.Fatalf("importing again after the crash: %v", err)
			}
		})
	}
	t.Run(crashImportInPlace, func(t *testing.T) {
		src := v1Workspace(t, v1Options{})
		m := newMachine(t, t.TempDir(), "id", t0)
		m.crash = func(p string) error {
			if p == crashImportInPlace {
				return errCrash
			}
			return nil
		}
		if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err == nil {
			t.Fatal("the import did not crash")
		}
		m.crash = nil
		status, err := m.Status(ctx)
		if err != nil || len(status.Topics) != 1 || status.Topics[0].Imported == nil {
			t.Fatalf("after the crash: %+v, %v", status, err)
		}
		if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); CodeOf(err) != CodeAlreadyExists {
			t.Errorf("importing again: %v", err)
		}
		if res, err := m.Checkpoint(ctx, CheckpointSpec{Topic: "go-concurrency", Role: "agent"}); err != nil || !res.Committed {
			t.Errorf("the next Checkpoint saves the import: %+v, %v", res, err)
		}
	})
}

// A failed import removes its staging folder, and one stopped during the
// copy says it was canceled.
func TestAnImportCanceledDuringTheCopyCleansUp(t *testing.T) {
	gitIdentity(t)
	src := v1Workspace(t, v1Options{})
	m := newMachine(t, t.TempDir(), "id", t0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.crash = func(p string) error {
		if p == crashImportCopying {
			cancel()
		}
		return nil
	}
	_, err := m.ImportV1(ctx, ImportSpec{Dir: src})
	if CodeOf(err) != CodeCanceled {
		t.Fatalf("a canceled import: %v (%s)", err, CodeOf(err))
	}
	if entries, err := os.ReadDir(filepath.Join(m.home, localDir, "tmp")); err != nil || len(entries) != 0 {
		t.Errorf("the staging folder is left: %v, %v", entries, err)
	}
	if exists(filepath.Join(m.home, "go-concurrency")) {
		t.Error("a canceled import left a Topic")
	}
}

// Staging folders an interrupted import left are reported by doctor, and
// removed by the next import once they are an hour old and unlocked.
func TestImportSweepsStaleStagingFolders(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	m.crash = func(p string) error {
		if p == crashImportCopying {
			return errCrash
		}
		return nil
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: v1Workspace(t, v1Options{}), ID: "crashed"}); err == nil {
		t.Fatal("the import did not crash")
	}
	m.crash = nil
	tmp := filepath.Join(m.home, localDir, "tmp")
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging after the crash: %v, %v", entries, err)
	}
	stale := entries[0].Name()
	old := time.Now().Add(-2 * time.Hour)
	writeFiles(t, tmp, map[string]string{"young.abc/x": "x", "held.abc/x": "x"})
	for _, name := range []string{stale, "held.abc"} {
		if err := os.Chtimes(filepath.Join(tmp, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	home, err := os.OpenRoot(m.home)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	unlock, err := lockTopic(ctx, home, "held")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	opts := Options{Getenv: func(key string) string { return map[string]string{"STUDY_HOME": m.home, "HOME": m.home}[key] }}
	var finding *Finding
	for _, f := range Diagnose(ctx, opts).Findings {
		if f.Name == "staging" {
			finding = &f
		}
	}
	if finding == nil || finding.Status != FindingWarn || !strings.Contains(finding.Message, stale) ||
		strings.Contains(finding.Message, "young") || strings.Contains(finding.Message, "held") || !strings.Contains(finding.Fix, "study import") {
		t.Errorf("doctor's staging finding = %+v", finding)
	}

	if _, err := m.ImportV1(ctx, ImportSpec{Dir: v1Workspace(t, v1Options{}), ID: "next"}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(tmp, stale)) {
		t.Error("the stale staging folder is still there")
	}
	for _, kept := range []string{"young.abc", "held.abc"} {
		if !exists(filepath.Join(tmp, kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	for _, f := range Diagnose(ctx, opts).Findings {
		if f.Name == "staging" {
			t.Errorf("doctor still warns: %+v", f)
		}
	}
}

// Two imports of the same workspace at once: one succeeds, the other is
// told it was imported already.
func TestConcurrentImportsOfOneWorkspace(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{})
	m := newMachine(t, t.TempDir(), "id", t0)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"one", "two"} {
		wg.Go(func() {
			_, errs[i] = m.ImportV1(ctx, ImportSpec{Dir: src, ID: id})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "imported already"):
			t.Errorf("the other import: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d imports succeeded: %v", ok, errs)
	}
}
