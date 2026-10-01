package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDomainTimeIsTheWritersClock: with this computer's clock behind the
// History, Events sort after the latest one, but what the learner sees, and
// what schedules Cards, is the writer's real time.
func TestDomainTimeIsTheWritersClock(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	behind := t0.Add(-time.Hour)
	m.setClock(behind)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	closed, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write answer.txt"})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || !topic.Resume.NextStep.At.Equal(behind) || !closed.NextStep.At.Equal(behind) {
		t.Errorf("Next step at %v (status), %v (result); want the writer's clock, %v", topic.Resume.NextStep.At,
			closed.NextStep.At, behind)
	}
	a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || !a.At.Equal(behind) {
		t.Errorf("Attempt at %v, %v; want %v", a.At, err, behind)
	}
	res, err := m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || !res.Attempts[0].At.Equal(behind) {
		t.Errorf("replayed Attempt at %+v, %v; want %v", res.Attempts, err, behind)
	}
}

func TestCompletingALessonClearsItsNextStep(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing,
		NextStep: "Write the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || topic.Resume.NextStep == nil {
		t.Fatalf("resume before completing = %+v, %v", topic.Resume, err)
	}
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil {
		t.Fatal(err)
	}
	topic, err = m.readTopic("c")
	if err != nil || topic.Resume.NextStep != nil || !topic.Resume.SyllabusDone {
		t.Errorf("resume after completing = %+v, %v; want no Next step left over", topic.Resume, err)
	}
}

func TestACriterionsOutputIsShownButNeverRecorded(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || !strings.Contains(a.Criteria[0].Output, "answer.txt holds 41") {
		t.Fatalf("Attempt = %+v, %v; want the output shown", a, err)
	}
	history, err := os.ReadFile(filepath.Join(m.home, "c", historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(history), "holds 41") {
		t.Error("the criterion's output was written to the History")
	}
	res, err := m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || res.Attempts[0].Criteria[0].Output != "" {
		t.Errorf("check results = %+v, %v; want no output", res.Attempts, err)
	}
}

func TestAnOlderUnclosedSessionGetsItsNote(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	first, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil || len(second.Unclosed) != 1 || second.Unclosed[0].ID != first.Session {
		t.Fatalf("second Session = %+v, %v", second, err)
	}
	note, err := m.CloseSession(ctx, "c", CloseSpec{Session: first.Session, NextStep: "Finish the exercise"})
	if err != nil || note.Session != first.Session {
		t.Fatalf("closing the first Session = %+v, %v", note, err)
	}
	topic, err := m.readTopic("c")
	if err != nil || topic.Resume.OpenSession == nil || topic.Resume.OpenSession.ID != second.Session {
		t.Errorf("resume = %+v, %v; want the second Session still open", topic.Resume, err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{Session: first.Session, NextStep: "Write the note again"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("closing a closed Session: err = %v, want failed_precondition", err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{Session: "nope", NextStep: "Write the note again"}); CodeOf(err) != CodeNotFound {
		t.Errorf("closing an unknown Session: err = %v, want not_found", err)
	}
}
