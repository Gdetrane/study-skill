package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func (m *machine) setPhase(t *testing.T, lesson, phase string) PhaseResult {
	t.Helper()
	r, err := m.SetPhase(context.Background(), "c", PhaseSpec{Lesson: lesson, Phase: phase})
	if err != nil {
		t.Fatalf("SetPhase(%s, %s): %v", lesson, phase, err)
	}
	return r
}

// lastCheckpoint is the subject of the Topic's last commit.
func lastCheckpoint(t *testing.T, m *machine) string {
	t.Helper()
	return strings.TrimSpace(git(t, filepath.Join(m.home, "c"), "log", "-1", "--format=%s"))
}

func owedRole(t *testing.T, m *machine) string {
	t.Helper()
	s, _, err := m.replayTopic(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	if s.study.owed == nil {
		return ""
	}
	return s.study.owed.role
}

// TestAFailedTurnCheckpointIsTakenLater: HEAD is detached when the turn
// passes, so the Checkpoint fails; it stays owed, and the next phase_set,
// even one that changes nothing, takes it once HEAD is back on a branch.
func TestAFailedTurnCheckpointIsTakenLater(t *testing.T) {
	m := learningTopic(t)
	m.setPhase(t, "answer", PhasePracticing)
	dir := filepath.Join(m.home, "c")
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	git(t, dir, "checkout", "-q", "--detach")

	r := m.setPhase(t, "answer", PhaseFeedback)
	if r.Checkpoint != nil || r.CheckpointRole != "learner" || !strings.Contains(r.CheckpointError, "HEAD") {
		t.Fatalf("feedback on a detached HEAD = %+v, want an error naming the learner's role", r)
	}
	if owedRole(t, m) != "learner" {
		t.Fatal("the failed Checkpoint is not owed")
	}

	git(t, dir, "checkout", "-q", "main")
	r = m.setPhase(t, "answer", PhaseFeedback)
	if r.Changed || r.Checkpoint == nil || !r.Checkpoint.Committed || r.CheckpointError != "" {
		t.Fatalf("the retry = %+v, want no change and the owed Checkpoint", r)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: feedback" {
		t.Errorf("last Checkpoint = %q", got)
	}
	if owedRole(t, m) != "" {
		t.Error("the Checkpoint is still owed after it was taken")
	}
}

func TestACrashBeforeATurnCheckpointIsTakenByTheRetry(t *testing.T) {
	m := learningTopic(t)
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.SetPhase(context.Background(), "c", PhaseSpec{Lesson: "answer", Phase: PhaseFeedback}); err == nil {
		t.Fatal("SetPhase survived the crash")
	}
	m.crash = nil
	if owedRole(t, m) != "learner" {
		t.Fatal("the crash lost the owed Checkpoint")
	}
	r := m.setPhase(t, "answer", PhaseFeedback)
	if r.Changed || r.Checkpoint == nil || !r.Checkpoint.Committed {
		t.Fatalf("the retry = %+v, want the owed Checkpoint", r)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: feedback" {
		t.Errorf("last Checkpoint = %q", got)
	}
}

// TestTheTurnFollowsTheLearnerAcrossLessons: practicing Lesson 1 is the
// learner's turn; teaching Lesson 2 next ends it, whatever the Lesson.
func TestTheTurnFollowsTheLearnerAcrossLessons(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newTopic(t)
	two := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Lessons: []SyllabusLesson{
		{ID: "answer", Title: "The answer"}, {ID: "question", Title: "The question"},
	}}}}
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Two Lessons", Syllabus: two})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, m, "lessons/answer.md", answerLesson)
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "41\n")

	r := m.setPhase(t, "question", PhaseTeaching)
	if r.Checkpoint == nil || !r.Checkpoint.Committed {
		t.Fatalf("teaching Lesson 2 after practicing Lesson 1 = %+v, want the learner's Checkpoint", r)
	}
	if got := lastCheckpoint(t, m); got != "[learner] question: teaching" {
		t.Errorf("last Checkpoint = %q", got)
	}
}

func TestCompletingStraightFromPracticingSavesTheLearnersTurn(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	m.setPhase(t, "answer", PhasePracticing)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || done.Checkpoint == nil || !done.Checkpoint.Committed {
		t.Fatalf("completion = %+v, %v", done, err)
	}
	if got := lastCheckpoint(t, m); got != "[learner] answer: completed" {
		t.Errorf("last Checkpoint = %q, want the learner's turn", got)
	}
	again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || again.Changed || again.CheckpointError != "" {
		t.Errorf("completing again = %+v, %v", again, err)
	}
}
