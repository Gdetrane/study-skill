package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// v1Workspace builds a small synthetic v1 workspace; real ones hold the
// learner's work and are never used in tests.
func v1Workspace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go-concurrency")
	cfg := map[string]any{
		"version": 3, "topic": "Go Concurrency", "template": "go-idiomatic", "approach": "concept",
		"end_goal": "Write a concurrent web crawler", "difficulty": "beginner",
		"lessons": []any{
			map[string]any{"num": 1, "title": "Goroutines", "file": "lessons/01-goroutines.md", "status": "completed"},
			map[string]any{"num": 2, "title": "Channels", "file": "lessons/02-channels.md", "status": "in_progress"},
		},
		"session_state": map[string]any{"phase": "practicing", "pending_action": "review practice/lesson-02 implementation",
			"context": "Half way through the channels exercise.", "energy": "half", "time_budget_minutes": 25},
		// v1 kept a NotebookLM notebook and each source's id in it; the import
		// lists them under Dropped (ADR-0011).
		"sources": []any{map[string]any{"url": "https://go.dev/blog/pipelines",
			"title": "Go Concurrency Patterns: Pipelines", "source_id": "s-1"}},
		"notebooklm": map[string]any{"notebook_id": "nb-1"},
		"review":     map[string]any{"items_due": 1},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		".study-config.json":         string(data) + "\n",
		"lessons/plan.md":            "# Plan\n",
		"lessons/01-goroutines.md":   "# Lesson 1\n",
		"lessons/02-channels.md":     "# Lesson 2\n",
		"practice/lesson-02/main.go": "package main\n",
		".fsrs/cards.json":           `{"cards":[{"id":"lesson-01"}]}` + "\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"commit", "-q", "-m", "[agent] init study workspace"}, {"commit", "-q", "--allow-empty", "-m", "[agent] complete lesson 01"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestImportCommand(t *testing.T) {
	setGitIdentity(t)
	src := v1Workspace(t)
	home := t.TempDir()
	hashes := regexp.MustCompile(`\b[0-9a-f]{12,40}\b`)
	// The copy's size depends on the git version, through .git.
	sizes := regexp.MustCompile(`("files": |"bytes": )\d+`)
	copied := regexp.MustCompile(`Copy: \d+ files, [0-9.]+ [KMGTPE]?i?B`)
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(r result) result {
		for _, s := range []*string{&r.stdout, &r.stderr} {
			*s = strings.ReplaceAll(strings.ReplaceAll(*s, real, "$V1"), src, "$V1")
		}
		r.stdout = sizes.ReplaceAllString(hashes.ReplaceAllString(r.stdout, "<hash>"), "${1}<n>")
		r.stdout = copied.ReplaceAllString(r.stdout, "Copy: <n> files, <size>")
		return r
	}

	dry := norm(run(t, home, "import", src, "--dry-run", "--json"))
	if dry.code != cli.ExitOK {
		t.Fatalf("dry run: exit %d %s %s", dry.code, dry.stdout, dry.stderr)
	}
	golden(t, "import_dry_run.json", dry.stdout)
	golden(t, "import_dry_run.txt", norm(run(t, home, "import", src, "--dry-run")).stdout)
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("the dry run wrote into the Study home: %v", entries)
	}

	kept := run(t, home, "import", src, "--dry-run", "--not-done", "lesson-01")
	if kept.code != cli.ExitOK || !strings.Contains(kept.stdout, "lesson-01  Goroutines (v1: completed (kept open: --not-done))") {
		t.Errorf("--not-done: exit %d\n%s%s", kept.code, kept.stdout, kept.stderr)
	}

	imported := norm(run(t, home, "import", src))
	if imported.code != cli.ExitOK {
		t.Fatalf("import: exit %d %s %s", imported.code, imported.stdout, imported.stderr)
	}
	golden(t, "import.txt", imported.stdout)

	again := norm(run(t, home, "import", src, "--topic", "again", "--json"))
	if again.code != cli.ExitError {
		t.Errorf("importing again: exit %d", again.code)
	}
	golden(t, "import_again.json", again.stdout)

	status := run(t, home, "status", "--json")
	if !strings.Contains(status.stdout, `"action": "adopt"`) || !strings.Contains(status.stdout, `"imported": {`) {
		t.Errorf("status after the import:\n%s", status.stdout)
	}
	// The Level is v1's estimate until an Assessment or the learner sets it,
	// so status must not call it the learner's choice.
	shown := run(t, home, "status")
	if !strings.Contains(shown.stdout, "beginner (v1's estimate)") || strings.Contains(shown.stdout, "your choice") {
		t.Errorf("status after the import should say the Level is v1's estimate:\n%s", shown.stdout)
	}
	if usage := run(t, home, "import", "--json"); usage.code != cli.ExitUsage {
		t.Errorf("import without a folder: exit %d", usage.code)
	}
}
