package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// twoLessons is a Syllabus with the Lessons answer and question.
var twoLessons = Syllabus{Milestones: []Milestone{{
	ID: "basics", Title: "Basics", Outcome: "Write a program that answers", Priority: PriorityMust,
	Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}, {ID: "question", Title: "The question", Hours: 1}},
}}}

func hasConflict(flags []string, text string) bool {
	for _, f := range flags {
		if strings.Contains(f, text) {
			return true
		}
	}
	return false
}

// A Next step recorded on one machine for a Lesson the other machine
// completed never leads the Resume point after a merge, and is flagged.
func TestAStaleNextStepNeverLeads(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	completeAnswer(t, a)
	writeFile(t, b, "lessons/answer.md", breakPointLesson)
	if _, err := b.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	onA, onB := merge(t, a, b)
	for name, m := range map[string]*machine{"a": a, "b": b} {
		topic, err := m.readTopic("c")
		if err != nil {
			t.Fatal(err)
		}
		if r := topic.Resume; r == nil || !r.SyllabusDone || r.NextStep != nil || r.BreakPoint != nil {
			t.Errorf("on %s: resume = %+v", name, r)
		}
		if rec := recommend(topic); rec.Action == ActionNextStep {
			t.Errorf("on %s: recommended %+v, disagreeing with the Resume point", name, rec)
		}
	}
	if !hasConflict(onA, "Next step") || !hasConflict(onB, "Next step") {
		t.Errorf("conflicts: a %q, b %q", onA, onB)
	}
}

// The same holds for a Session closed with a Next step about a Lesson the
// other machine completed meanwhile.
func TestAStaleSessionNoteNeverLeads(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	completeAnswer(t, a)
	if _, err := b.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseSession(ctx, "c", CloseSpec{NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	_, onB := merge(t, a, b)
	topic, err := b.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if r := topic.Resume; r == nil || r.NextStep != nil || !hasConflict(onB, "Next step") {
		t.Errorf("resume = %+v, conflicts %q", r, onB)
	}
}

// Sessions left unclosed on both machines are all reported, newest first;
// a later Session merged from the other machine hides none.
func TestEveryUnclosedSessionIsReported(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	left, err := a.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseSession(ctx, "c", CloseSpec{NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	merge(t, a, b)
	topic, err := a.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if r := topic.Resume; r == nil || r.OpenSession == nil || r.OpenSession.ID != left.Session {
		t.Errorf("resume = %+v; want a's Session open", topic.Resume)
	}
	opened, err := a.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Unclosed) != 1 || opened.Unclosed[0].ID != left.Session || opened.Changes == nil {
		t.Errorf("unclosed = %+v, changes %+v", opened.Unclosed, opened.Changes)
	}
	// With two left open, the newest comes first.
	again, err := a.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Unclosed) != 2 || again.Unclosed[0].ID != opened.Session || again.Unclosed[1].ID != left.Session {
		t.Errorf("unclosed = %+v", again.Unclosed)
	}
}

// A note given late to an older Session never replaces a Next step recorded
// in a newer one; within one Session, the closing note does.
func TestALateNoteNeverReplacesANewerNextStep(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	old, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(time.Hour))
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write the draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write the new note"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(2 * time.Hour))
	if _, err := m.CloseSession(ctx, "c", CloseSpec{Session: old.Session, NextStep: "Write the old note"}); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if r := topic.Resume; r == nil || r.NextStep == nil || r.NextStep.Step != "Write the new note" {
		t.Errorf("resume = %+v; want the newer Session's note to lead", r.NextStep)
	}
}

// One Session closed on two machines with different notes is flagged; the
// same note twice is not.
func TestASessionClosedOnTwoMachines(t *testing.T) {
	for _, tc := range []struct {
		name     string
		noteB    string
		conflict bool
	}{
		{"different notes", "Write the note from b", true},
		{"the same note", "Write the note from a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, b := twoMachines(t, oneLessonSyllabus)
			opened, err := a.OpenSession(ctx, "c", SessionSpec{})
			if err != nil {
				t.Fatal(err)
			}
			takeCheckpoint(t, a)
			pull(t, b, a)
			if _, err := a.CloseSession(ctx, "c", CloseSpec{NextStep: "Write the note from a"}); err != nil {
				t.Fatal(err)
			}
			if _, err := b.CloseSession(ctx, "c", CloseSpec{Session: opened.Session, NextStep: tc.noteB}); err != nil {
				t.Fatal(err)
			}
			onA, onB := merge(t, a, b)
			if got := hasConflict(onA, "closed twice") && hasConflict(onB, "closed twice"); got != tc.conflict {
				t.Errorf("conflicts: a %q, b %q; want a conflict: %v", onA, onB, tc.conflict)
			}
		})
	}
}

// Break points are reached in the current Lesson only, never in a skipped
// one, so the Resume point's Lesson, Break point and Next step belong
// together.
func TestBreakPointsOnlyInTheCurrentLesson(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	gitIdentity(t)
	revise(t, m, twoLessons)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	writeFile(t, m, "lessons/question.md", breakPointLesson)
	_, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "question", BreakPoint: "read", NextStep: "Write the question"})
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "not the current Lesson, answer") {
		t.Errorf("a later Lesson: %v", err)
	}
	skipped := clone(twoLessons)
	skipped.Milestones[0].Lessons[1].Skipped = true
	revise(t, m, skipped)
	_, err = m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "question", BreakPoint: "read", NextStep: "Write the question"})
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "skipped") {
		t.Errorf("a skipped Lesson: %v", err)
	}
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write a first answer"}); err != nil {
		t.Errorf("the current Lesson: %v", err)
	}
}

// A Lesson removed and added again starts without its old Break point.
func TestARemovedLessonLosesItsBreakPoint(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	gitIdentity(t)
	revise(t, m, twoLessons)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "draft", NextStep: "Check the draft"}); err != nil {
		t.Fatal(err)
	}
	removed := clone(twoLessons)
	removed.Milestones[0].Lessons = removed.Milestones[0].Lessons[1:]
	revise(t, m, removed)
	revise(t, m, twoLessons)
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if r := topic.Resume; r == nil || r.Lesson != "answer" || r.BreakPoint != nil {
		t.Errorf("resume = %+v; want answer without its old Break point", r)
	}
}

// status flags a current Lesson whose header cannot be read, instead of
// hiding its Break points; fixing the header clears the flag.
func TestStatusFlagsUnreadableBreakPoints(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", strings.Replace(breakPointLesson,
		"describe: The question is understood", "describe: The question: understood", 1))
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if !hasFlag(topic.Flags, FlagLessonHeader, "lessons/answer.md") {
		t.Errorf("flags = %+v; want the header flagged", topic.Flags)
	}
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	if topic, _ = m.readTopic("c"); hasFlag(topic.Flags, FlagLessonHeader, "lessons/answer.md") {
		t.Errorf("the flag stayed after the fix: %+v", topic.Flags)
	}
}

// A learner.md that is a symbolic link is not pointed to: status never sends
// an agent outside the Study home.
func TestALearnerProfileSymlinkIsNotFollowed(t *testing.T) {
	m := newTopic(t)
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte("profile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(m.home, "learner.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(m.home, "c", "learner.md")); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LearnerProfile != "" || status.Topics[0].LearnerAdditions != "" {
		t.Errorf("symlinked learner.md pointed to: %q, %q", status.LearnerProfile, status.Topics[0].LearnerAdditions)
	}
}

// Once today's cap on new Cards is used, waiting drafts are not ready, and
// the next falls due at the start of tomorrow in the learner's time zone.
func TestCardsReadyWhenTheDailyCapIsUsed(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	for i := range NewCardsPerDay + 1 {
		card := m.addCard(t, CardSpec{Prompt: "Question " + strings.Repeat("?", i+1), Answer: "Answer"})
		if i < NewCardsPerDay {
			if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: card.ID, Rating: RatingGood, Draft: DraftKeep}); err != nil {
				t.Fatal(err)
			}
		}
	}
	st := replayFolder(t, filepath.Join(m.home, "c")).study
	kiritimati := time.FixedZone("UTC+14", 14*60*60)
	now := t0.In(kiritimati) // 23:30 on 1 October there
	got := st.cardsReady(now)
	tomorrow := time.Date(2026, 10, 2, 0, 0, 0, 0, kiritimati)
	if got == nil || got.Ready || got.NextDue == nil || !got.NextDue.Equal(tomorrow) {
		t.Errorf("with the cap used: %+v, want ready tomorrow at %s", got, tomorrow)
	}
	if later := st.cardsReady(tomorrow); later == nil || !later.Ready {
		t.Errorf("tomorrow: %+v", later)
	}
}
