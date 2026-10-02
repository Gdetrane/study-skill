package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// v1Options shapes a synthetic v1 workspace. Real workspaces are never used
// in tests: they hold the learner's work.
type v1Options struct {
	noGit   bool
	config  map[string]any
	extra   map[string]string // more files, by relative path
	links   map[string]string // links, by relative path → target
	commits []string          // commit subjects after the initial one
}

// v1Workspace builds a v1 workspace like the v1 skill's lifecycle smoke
// test: .study-config.json, lessons/, practice/, notes/ and .fsrs/, with
// [agent]/[user] commits.
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
		".gitignore":                  "bin/\n",
		"go.mod":                      "module example.com/concurrency\n",
	}
	for k, v := range opts.extra {
		files[k] = v
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range opts.links {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if opts.noGit {
		return dir
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "[agent] init study workspace")
	for _, subject := range opts.commits {
		git(t, dir, "commit", "-q", "--allow-empty", "-m", subject)
	}
	return dir
}

// treeHash hashes every path, mode, link target and file content under dir,
// .git included, to prove an import left the workspace untouched.
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
	if !strings.HasPrefix(got.Completed[0].Proof, "commit ") || got.Completed[1].Proof != "v1's card lesson-02" {
		t.Errorf("proofs = %q, %q", got.Completed[0].Proof, got.Completed[1].Proof)
	}
	if ids := lessonIDs(got.Open); !slices.Equal(ids, []string{"lesson-03"}) {
		t.Errorf("open = %+v", got.Open)
	}
	if got.Level != "intermediate" || got.Approach != ApproachProject && got.Approach != ApproachConcepts {
		t.Errorf("level %q, approach %q", got.Level, got.Approach)
	}
	if got.NextStep == nil || got.NextStep.PendingAction != "review practice/lesson-03 implementation" {
		t.Errorf("v1 next step = %+v", got.NextStep)
	}
	dropped := strings.Join(notesWhat(got.Dropped), ",")
	for _, want := range []string{".fsrs", "template", "progress", "review", "catalog_path", "made_up_key", "created"} {
		if !strings.Contains(dropped, want) {
			t.Errorf("dropped %s does not mention %s", dropped, want)
		}
	}
	if got.Checkpoint == "" {
		t.Errorf("no Checkpoint: %s", got.CheckpointError)
	}
	if dry.Converted == nil || len(dry.Moved) != len(got.Moved) || len(dry.Dropped) != len(got.Dropped) {
		t.Errorf("the dry run reported differently:\n dry %+v\n real %+v", dry, got)
	}

	topic := filepath.Join(m.home, "go-concurrency")
	for _, want := range []string{"lessons/lesson-01.md", "lessons/lesson-02.md", "lessons/lesson-03.md",
		"notes/v1-plan.md", "notes/v1-config.json", "practice/lesson-01/main.go", "go.mod", "topic.toml", "history.jsonl"} {
		if _, err := os.Stat(filepath.Join(topic, want)); err != nil {
			t.Errorf("%s: %v", want, err)
		}
	}
	for _, gone := range []string{".study-config.json", ".fsrs", "lessons/plan.md", "lessons/01-goroutines.md"} {
		if _, err := os.Lstat(filepath.Join(topic, gone)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", gone)
		}
	}
	if ignore, _ := os.ReadFile(filepath.Join(topic, ".gitignore")); !strings.HasPrefix(string(ignore), "bin/\n") ||
		!strings.Contains(string(ignore), "*.parquet") {
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

	// status recommends adopting it, and the proven Lessons are done.
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 1 || status.Topics[0].Imported == nil || status.Topics[0].Imported.Adopted ||
		status.Topics[0].Level == nil || status.Topics[0].Level.Level != "intermediate" {
		t.Fatalf("status = %+v", status.Topics)
	}
	if status.Recommended == nil || status.Recommended.Action != ActionAdopt {
		t.Errorf("recommended = %+v", status.Recommended)
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
				map[string]any{"url": "https://go.dev/doc/effective_go"},
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
	if got.Sources[0].TopicPath != "sources/book.pdf" || got.Sources[1].FileName != "Strang Linear Algebra.pdf" ||
		got.Sources[1].NotebookLMID != "s-9" || got.Sources[2].URL != "https://go.dev/doc/effective_go" {
		t.Errorf("sources = %+v", got.Sources)
	}
	dropped := strings.Join(notesWhat(got.Dropped), ",")
	if !strings.Contains(dropped, "sources[3]") || !strings.Contains(dropped, "sources[4]") || !strings.Contains(dropped, "NotebookLM notebook nb-1") {
		t.Errorf("dropped = %s", dropped)
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

func TestImportRefusals(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)

	escaping := v1Workspace(t, v1Options{links: map[string]string{"notes/secrets": "/etc"}})
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: escaping}); CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "notes/secrets") {
		t.Errorf("an escaping link: %v", err)
	}
	upward := v1Workspace(t, v1Options{links: map[string]string{"notes/up": "../../elsewhere"}})
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: upward}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a link out through ..: %v", err)
	}

	notV1 := t.TempDir()
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: notV1}); CodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "not a v1 workspace") {
		t.Errorf("a folder without .study-config.json: %v", err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: filepath.Join(t.TempDir(), "missing")}); CodeOf(err) != CodeNotFound {
		t.Errorf("a missing folder: %v", err)
	}

	src := v1Workspace(t, v1Options{})
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src}); CodeOf(err) != CodeAlreadyExists {
		t.Errorf("the same workspace again: %v", err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: src, ID: "again"}); CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "imported already") {
		t.Errorf("the same workspace under another id: %v", err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: filepath.Join(m.home, "go-concurrency")}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a folder inside the Study home: %v", err)
	}

	worktree := v1Workspace(t, v1Options{noGit: true, extra: map[string]string{".git": "gitdir: /elsewhere\n"}})
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: worktree, ID: "worktree"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a linked worktree: %v", err)
	}

	newer := v1Workspace(t, v1Options{config: map[string]any{"version": 9}})
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: newer, ID: "newer"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an unknown config version: %v", err)
	}
	if _, err := m.ImportV1(ctx, ImportSpec{Dir: v1Workspace(t, v1Options{}), ID: "Not An Id"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an invalid id: %v", err)
	}
}

func TestImportWithoutGitOrLessons(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	src := v1Workspace(t, v1Options{noGit: true, config: map[string]any{"lessons": []any{}, "session_state": nil,
		"end_goal": nil, "difficulty_override": nil, "approach": "challenge"}})
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
	if _, err := os.Stat(filepath.Join(m.home, "minimal", ".git", "HEAD")); err != nil {
		t.Errorf("the imported Topic has no git repository: %v", err)
	}
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
	if _, err := os.Lstat(filepath.Join(topic, "notes", "pipe")); !os.IsNotExist(err) {
		t.Error("the FIFO was copied")
	}
	if !strings.Contains(strings.Join(notesWhat(got.Dropped), ","), "notes/pipe") {
		t.Errorf("dropped = %+v", got.Dropped)
	}
}

func TestACrashedImportLeavesNoTopic(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	for _, point := range []string{crashImportCopied, crashImportStaged} {
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
			if _, err := os.Lstat(filepath.Join(m.home, "go-concurrency")); !os.IsNotExist(err) {
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
