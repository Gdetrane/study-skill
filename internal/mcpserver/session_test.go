package mcpserver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

const breakPointLesson = "---\ncheck:\n  - id: answer\n    run: [sh, check.sh]\n" +
	"break_points:\n  - id: read\n    describe: The question is understood\n---\n# The answer\n"

func TestSessionsAndBreakPoints(t *testing.T) {
	withGitIdentity(t)
	home := t.TempDir()
	session := connect(t, home)
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var proposal core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &proposal)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": proposal.Revision, "learner_said": "yes"})
	for name, content := range map[string]string{
		"lessons/answer.md":          breakPointLesson,
		"practice/answer/check.sh":   "test \"$(cat answer.txt)\" = 42\n",
		"practice/answer/answer.txt": "41\n",
	} {
		path := filepath.Join(home, "c", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var opened core.SessionOpened
	decode(t, call(t, session, "session_open", map[string]any{"topic": "c", "energy": "full"}), &opened)
	if opened.Suggested == nil || opened.Suggested.Suggest != core.FocusLearn || len(opened.Unclosed) != 0 {
		t.Errorf("first Session = %+v", opened)
	}
	// The learner chooses the suggested Focus: it is recorded on this
	// Session, and no other opens.
	var focused core.SessionOpened
	decode(t, call(t, session, "session_open", map[string]any{"topic": "c", "session": opened.Session, "focus": "learn"}), &focused)
	if focused.Session != opened.Session || focused.Focus != core.FocusLearn {
		t.Errorf("recording the Focus = %+v", focused)
	}
	var reached core.BreakPointReached
	decode(t, call(t, session, "break_point_reached", map[string]any{
		"topic": "c", "lesson": "answer", "break_point": "read", "next_step": "Write a first answer",
	}), &reached)
	if !reached.Changed || reached.BreakPoint.Describe != "The question is understood" {
		t.Errorf("break_point_reached = %+v", reached)
	}
	if reached.Checkpoint == nil || !reached.Checkpoint.Committed {
		t.Errorf("the Break point saved nothing: %+v", reached.TurnCheckpoint)
	}
	if msg := toolError(t, session, "session_close", map[string]any{"topic": "c", "next_step": "Continue"}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("a Next step that is not an action: %q", msg)
	}

	// The terminal closes after the learner worked past the last
	// Checkpoint: the next Session shows what changed.
	call(t, session, "checkpoint", map[string]any{"topic": "c", "role": "agent"})
	if err := os.WriteFile(filepath.Join(home, "c", "practice", "answer", "answer.txt"), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened = core.SessionOpened{}
	decode(t, call(t, session, "session_open", map[string]any{"topic": "c", "energy": "fumes"}), &opened)
	ch := opened.Changes
	if len(opened.Unclosed) != 1 || ch == nil || len(ch.Files) != 1 ||
		ch.Files[0] != (core.FileChange{Path: "practice/answer/answer.txt", Change: "modified"}) {
		t.Fatalf("unclosed = %+v, changes = %+v", opened.Unclosed, ch)
	}
	if opened.Resume.BreakPoint == nil || opened.Resume.BreakPoint.ID != "read" || opened.Suggested == nil ||
		opened.Suggested.Suggest != core.SuggestStop {
		t.Errorf("second Session: resume %+v, suggested %+v", opened.Resume, opened.Suggested)
	}
	call(t, session, "session_close", map[string]any{"topic": "c", "session": opened.Unclosed[0].ID, "next_step": "Run the Check on 42"})

	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if r := status.Recommended; r == nil || r.Action != core.ActionNextStep || r.Text != "Run the Check on 42" {
		t.Errorf("recommended = %+v", status.Recommended)
	}
}
