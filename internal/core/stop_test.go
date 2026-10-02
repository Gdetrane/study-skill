package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoppingMidPracticeSavesTheWork: a Break point reached and a Session
// closed while the learner practises each take a Checkpoint in the learner's
// role, so the work and the stop are saved, not left for the next turn.
func TestStoppingMidPracticeSavesTheWork(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	dir := filepath.Join(m.home, "c")
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull}); err != nil {
		t.Fatal(err)
	}
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")

	reached, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "draft", NextStep: "Check the draft"})
	if err != nil {
		t.Fatal(err)
	}
	if reached.Checkpoint == nil || !reached.Checkpoint.Committed || reached.CheckpointError != "" {
		t.Fatalf("the Break point took no Checkpoint: %+v", reached.TurnCheckpoint)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: stopped at draft" {
		t.Errorf("last Checkpoint = %q", got)
	}
	if got := git(t, dir, "show", "HEAD:practice/answer/answer.txt"); got != "42\n" {
		t.Errorf("the learner's work at HEAD = %q", got)
	}
	if !strings.Contains(git(t, dir, "show", "HEAD:history.jsonl"), `"type":"break_point.reached"`) {
		t.Error("the Checkpoint lacks the Break point's Event")
	}
	// The Checkpoint's own checkpoint.taken Event is written after the commit
	// it names, so the History is the only file left to save.
	if status := strings.TrimSpace(git(t, dir, "status", "--porcelain")); status != "M history.jsonl" {
		t.Errorf("git status after the Break point = %q, want only the History", status)
	}

	writeFile(t, m, "practice/answer/notes.txt", "try 42\n")
	closed, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Run the Check on 42"})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Checkpoint == nil || !closed.Checkpoint.Committed {
		t.Fatalf("closing took no Checkpoint: %+v", closed.TurnCheckpoint)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: Session closed" {
		t.Errorf("last Checkpoint = %q", got)
	}
	if got := git(t, dir, "show", "HEAD:practice/answer/notes.txt"); got != "try 42\n" {
		t.Errorf("the learner's notes at HEAD = %q", got)
	}
	if !strings.Contains(git(t, dir, "show", "HEAD:history.jsonl"), `"type":"session.closed"`) {
		t.Error("the Checkpoint lacks the Session's close")
	}
	if status := strings.TrimSpace(git(t, dir, "status", "--porcelain")); status != "M history.jsonl" {
		t.Errorf("git status after closing = %q, want only the History", status)
	}
	if owedRole(t, m) != "" {
		t.Error("a Checkpoint is still owed after the stop took it")
	}

	// A stop in the agent's turn saves in the agent's role.
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	m.setPhase(t, "answer", PhaseFeedback)
	writeFile(t, m, "teacher/answer.md", "Answer key: 42\n")
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Go over the feedback"}); err != nil {
		t.Fatal(err)
	}
	if got := lastCheckpoint(t, m); got != "[agent] answer: Session closed" {
		t.Errorf("last Checkpoint = %q", got)
	}
}

// TestAStopIsNeverBlockedByItsCheckpoint: when the Checkpoint fails, the stop
// is recorded anyway, the result says why and in which role to save, and the
// Checkpoint stays owed for the next write that takes one.
func TestAStopIsNeverBlockedByItsCheckpoint(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	dir := filepath.Join(m.home, "c")
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	git(t, dir, "checkout", "-q", "--detach")

	closed, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Run the Check on 42"})
	if err != nil {
		t.Fatalf("a failing Checkpoint blocked the close: %v", err)
	}
	if closed.Checkpoint != nil || closed.CheckpointRole != "learner" || !strings.Contains(closed.CheckpointError, "HEAD") {
		t.Fatalf("close on a detached HEAD = %+v", closed.TurnCheckpoint)
	}
	if s := replayFolder(t, dir); s.study.lastSession() == nil || !s.study.lastSession().closed {
		t.Error("the Session is not closed")
	}
	if owedRole(t, m) != "learner" {
		t.Fatal("the failed Checkpoint is not owed")
	}

	git(t, dir, "checkout", "-q", "main")
	cp, err := m.Checkpoint(ctx, CheckpointSpec{Topic: "c", Role: "learner"})
	if err != nil || !cp.Committed {
		t.Fatalf("checkpoint with the role the result named = %+v, %v", cp, err)
	}
	if owedRole(t, m) != "" {
		t.Error("the Checkpoint is still owed after it was taken")
	}
}

// TestAPendingCheckpointIsTakenByAStop: a stop while another Checkpoint is
// owed takes that one, which also saves the stop.
func TestAPendingCheckpointIsTakenByAStop(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	dir := filepath.Join(m.home, "c")
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	git(t, dir, "checkout", "-q", "--detach")
	m.setPhase(t, "answer", PhaseFeedback) // its learner Checkpoint fails and stays owed
	git(t, dir, "checkout", "-q", "main")

	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Go over the feedback"}); err != nil {
		t.Fatal(err)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: feedback" {
		t.Errorf("last Checkpoint = %q, want the one the turn switch owed", got)
	}
	if !strings.Contains(git(t, dir, "show", "HEAD:history.jsonl"), `"type":"session.closed"`) {
		t.Error("the Checkpoint lacks the Session's close")
	}
	if owedRole(t, m) != "" {
		t.Error("a Checkpoint is still owed")
	}
}

// TestTheChosenFocusIsRecordedOnTheOpenSession: session_open with the open
// Session's id and a Focus records the learner's choice and opens nothing.
func TestTheChosenFocusIsRecordedOnTheOpenSession(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyHalf})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Suggested == nil {
		t.Fatal("no Focus suggested")
	}
	focused, err := m.OpenSession(ctx, "c", SessionSpec{Session: opened.Session, Focus: FocusPractice})
	if err != nil {
		t.Fatal(err)
	}
	if focused.Session != opened.Session || focused.Focus != FocusPractice || focused.Suggested != nil {
		t.Errorf("result = %+v", focused)
	}
	if again, err := m.OpenSession(ctx, "c", SessionSpec{Session: opened.Session, Focus: FocusPractice}); err != nil || again.Session != opened.Session {
		t.Errorf("the same Focus again = %+v, %v", again, err)
	}
	s := replayFolder(t, filepath.Join(m.home, "c"))
	if n := countEvents(s, eventSessionOpened); n != 1 {
		t.Errorf("%d Sessions opened, want 1", n)
	}
	if n := countEvents(s, eventSessionFocused); n != 1 {
		t.Errorf("%d session.focused Events, want 1: the same Focus again records nothing", n)
	}
	if got := s.study.lastSession(); got.focus != FocusPractice || got.energy != EnergyHalf {
		t.Errorf("replayed Session = %+v", got)
	}

	for _, tc := range []struct {
		name string
		spec SessionSpec
		code ErrorCode
	}{
		{"no Focus", SessionSpec{Session: opened.Session}, CodeInvalidArgument},
		{"an Energy too", SessionSpec{Session: opened.Session, Focus: FocusLearn, Energy: EnergyFull}, CodeInvalidArgument},
		{"an unknown Focus", SessionSpec{Session: opened.Session, Focus: "nap"}, CodeInvalidArgument},
		{"an unknown Session", SessionSpec{Session: "nosuch", Focus: FocusLearn}, CodeNotFound},
	} {
		if _, err := m.OpenSession(ctx, "c", tc.spec); CodeOf(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.name, err, tc.code)
		}
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write the answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Session: opened.Session, Focus: FocusLearn}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a closed Session: err = %v, want failed_precondition", err)
	}
}

func TestCrashesWhileRecordingAFocus(t *testing.T) {
	ctx := context.Background()
	for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
		t.Run(point, func(t *testing.T) {
			m := learningTopic(t)
			opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull})
			if err != nil {
				t.Fatal(err)
			}
			spec := SessionSpec{Session: opened.Session, Focus: FocusLearn}
			m.crash = crashOnce(point)
			_, err = m.OpenSession(ctx, "c", spec)
			m.crash = nil
			if err != nil && !errors.Is(err, errCrash) {
				t.Fatalf("the write failed: %v", err)
			}
			dry := spec
			dry.DryRun = true
			if _, err := m.OpenSession(ctx, "c", dry); err != nil {
				t.Errorf("dry run: %v", err)
			}
			if _, err := m.OpenSession(ctx, "c", spec); err != nil {
				t.Errorf("the retry: %v", err)
			}
			if hasIntentFile(t, m) {
				t.Error("the intent marker survived the next write")
			}
			s := replayFolder(t, filepath.Join(m.home, "c"))
			if len(s.flags) != 0 {
				t.Errorf("flags = %+v", s.flags)
			}
			if n := countEvents(s, eventSessionFocused); n != 1 {
				t.Errorf("%d session.focused Events, want 1", n)
			}
			if got := s.study.lastSession().focus; got != FocusLearn {
				t.Errorf("replayed Focus = %q", got)
			}
		})
	}
}

// A session.focused Event that arrives before its Session, as a merge can
// order them, is held until the Session is known.
func TestAFocusForAnUnknownSessionIsHeld(t *testing.T) {
	s := replay(nil)
	ev := event{ID: "f1", Type: eventSessionFocused, Data: []byte(`{"session":"nosuch","focus":"learn"}`)}
	if err := replaySessionFocused(s, ev); !errors.Is(err, errUnknownItem) {
		t.Errorf("err = %v, want it held as unknown", err)
	}
}
