package core_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// withGitIdentity gives git a fixed identity through a temporary HOME, as a
// learner's global configuration would, and hides the developer's own.
func withGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	config := "[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

// committedFiles lists the files in the Topic's last commit.
func committedFiles(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", "HEAD").Output()
	if err != nil {
		t.Fatalf("git ls-tree: %v", err)
	}
	return strings.Fields(string(out))
}

func TestCheckpoint(t *testing.T) {
	ctx := context.Background()
	withGitIdentity(t)
	c := testCore(t, t.TempDir(), "")
	topic, err := c.CreateTopic(ctx, core.TopicSpec{Title: "Linear algebra"})
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(topic.Path, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", "# Gaussian elimination\n")
	write("matrix.parquet", "data the default .gitignore keeps out")

	first, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "agent", Message: "Lesson 1 notes"})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if !first.Committed || first.Commit == "" || len(first.LargeFiles) != 0 {
		t.Fatalf("first checkpoint = %+v", first)
	}
	got := strings.Join(committedFiles(t, topic.Path), " ")
	if got != ".gitattributes .gitignore history.jsonl notes.md topic.toml" {
		t.Errorf("committed files = %s", got)
	}

	same, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "learner"})
	if err != nil || same.Committed || same.Commit != first.Commit {
		t.Errorf("checkpoint without changes = %+v, %v; want a skip at %s", same, err, first.Commit)
	}

	write("exercise.go", "package main\n")
	dry, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "learner", DryRun: true})
	if err != nil || !dry.Committed || dry.Commit != "" || !dry.DryRun {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	if files := committedFiles(t, topic.Path); len(files) != 5 {
		t.Errorf("the dry run committed: %v", files)
	}
}

func TestCheckpointErrors(t *testing.T) {
	ctx := context.Background()
	withGitIdentity(t)
	home := t.TempDir()
	c := testCore(t, home, "")
	topic, err := c.CreateTopic(ctx, core.TopicSpec{Title: "C"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		spec core.CheckpointSpec
		want core.ErrorCode
	}{
		{"no topic", core.CheckpointSpec{Role: "agent"}, core.CodeInvalidArgument},
		{"unknown topic", core.CheckpointSpec{Topic: "physics", Role: "agent"}, core.CodeNotFound},
		{"path in the id", core.CheckpointSpec{Topic: "../c", Role: "agent"}, core.CodeInvalidArgument},
		{"unknown role", core.CheckpointSpec{Topic: "c", Role: "tutor"}, core.CodeInvalidArgument},
		{"control characters", core.CheckpointSpec{Topic: "c", Role: "agent", Message: "\x1b[2J"}, core.CodeInvalidArgument},
	} {
		if _, err := c.Checkpoint(ctx, tc.spec); core.CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.want)
		}
	}

	if _, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", topic.Path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(topic.Path, ".git", "MERGE_HEAD"), head, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "agent"})
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "merge") {
		t.Errorf("during a merge: err = %v, want failed_precondition mentioning the merge", err)
	}
	if err := os.Remove(filepath.Join(topic.Path, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal(err)
	}

	// Another git program updating the branch is worth retrying.
	if err := os.WriteFile(filepath.Join(topic.Path, "notes.md"), []byte("new work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(topic.Path, ".git", "refs", "heads", "main.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "learner"})
	if core.CodeOf(err) != core.CodeBusy || !errors.Is(err, checkpoint.ErrRefLocked) {
		t.Errorf("while the branch is locked: err = %v, want busy caused by ErrRefLocked", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: topic.ID, Role: "learner"}); err != nil || !res.Committed {
		t.Errorf("once the lock is gone: %+v, %v", res, err)
	}
}
