package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// oneLessonSyllabus is the thin learner loop's Syllabus: one Milestone with
// one Lesson, "answer".
var oneLessonSyllabus = Syllabus{Milestones: []Milestone{{
	ID: "basics", Title: "Basics", Outcome: "Write a program that answers", Priority: PriorityMust,
	Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}},
}}}

// answerLesson is a Lesson file whose Check runs check.sh in the practice
// folder: it passes when answer.txt holds 42.
const answerLesson = `---
check:
  - id: answer
    describe: answer.txt holds the answer
    run: [sh, check.sh]
---
# The answer

Write the answer in answer.txt.
`

const answerCheck = `test "$(cat answer.txt)" = 42 || { echo "answer.txt holds $(cat answer.txt), not 42"; exit 1; }
`

// withSyllabus gives Topic c the one-Lesson Syllabus, approved.
func withSyllabus(t *testing.T, m *machine) {
	t.Helper()
	ctx := context.Background()
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "A first Syllabus", Syllabus: oneLessonSyllabus})
	if err != nil {
		t.Fatalf("ProposeRevision: %v", err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "yes"}, false); err != nil {
		t.Fatalf("ApplyRevision: %v", err)
	}
}

// writeFile writes a file in Topic c, as the agent or the learner would.
func writeFile(t *testing.T, m *machine, rel, content string) {
	t.Helper()
	path := filepath.Join(m.home, "c", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// learningTopic is Topic c with the Syllabus, the Lesson and its exercise,
// answered wrongly.
func learningTopic(t *testing.T) *machine {
	t.Helper()
	gitIdentity(t)
	m := newTopic(t)
	withSyllabus(t, m)
	writeFile(t, m, "lessons/answer.md", answerLesson)
	writeFile(t, m, "practice/answer/check.sh", answerCheck)
	writeFile(t, m, "practice/answer/answer.txt", "41\n")
	return m
}

// practicing moves Lesson answer to practicing, which shows the learner its
// Check.
func practicing(t *testing.T, m *machine) {
	t.Helper()
	if _, err := m.SetPhase(context.Background(), "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); err != nil {
		t.Fatalf("SetPhase(practicing): %v", err)
	}
}

func TestARevisionChangesTheSyllabusOnlyOnceApproved(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "A first Syllabus", Syllabus: oneLessonSyllabus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.home, "c", syllabusFile)); !os.IsNotExist(err) {
		t.Fatalf("a proposed Revision wrote %s (err = %v)", syllabusFile, err)
	}
	view, err := m.SyllabusOf(ctx, "c")
	if err != nil || len(view.Milestones) != 0 || len(view.Proposals) != 1 || view.Proposals[0].Revision != p.Revision {
		t.Fatalf("before approval: %+v, %v", view, err)
	}

	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat"}, false); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an approval without the learner's words: err = %v, want invalid_argument", err)
	}
	applied, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "Looks good"}, false)
	if err != nil || !applied.Changed {
		t.Fatalf("ApplyRevision = %+v, %v", applied, err)
	}
	again, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "Looks good"}, false)
	if err != nil || again.Changed {
		t.Errorf("applying twice = %+v, %v; want no change", again, err)
	}
	view, err = m.SyllabusOf(ctx, "c")
	if err != nil || len(view.Proposals) != 0 || view.Milestones[0].Lessons[0].Status != LessonNotStarted {
		t.Fatalf("after approval: %+v, %v", view, err)
	}

	// A Revision proposed from an older Syllabus cannot be applied.
	renamed := clone(oneLessonSyllabus)
	renamed.Milestones[0].Outcome = "Write a program that answers anything"
	stale, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Rename", Syllabus: renamed})
	if err != nil {
		t.Fatal(err)
	}
	other := oneLessonSyllabus
	other.Milestones = []Milestone{oneLessonSyllabus.Milestones[0]}
	other.Milestones[0].Title = "Foundations"
	newer, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Retitle", Syllabus: other})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", newer.Revision, Approval{Via: "chat", LearnerSaid: "ok"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", stale.Revision, Approval{Via: "chat", LearnerSaid: "ok"}, false); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a stale Revision: err = %v, want failed_precondition", err)
	}

	// The Syllabus gates progress, so a hand edit is flagged.
	writeFile(t, m, syllabusFile, "format = 1\n")
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if !hasFlag(topic.Flags, FlagEditedOutside, syllabusFile) {
		t.Errorf("flags = %+v, want %s edited outside", topic.Flags, syllabusFile)
	}
}

func TestAnInvalidSyllabusIsRefused(t *testing.T) {
	m := newTopic(t)
	for _, s := range []Syllabus{
		{},
		{Milestones: []Milestone{{ID: "m", Title: "M"}}},
		{Milestones: []Milestone{{ID: "m", Title: "M", Lessons: []SyllabusLesson{{ID: "Bad Id", Title: "L"}}}}},
		{Milestones: []Milestone{{ID: "m", Title: "M", Lessons: []SyllabusLesson{{ID: "m", Title: "L"}}}}},
		{Milestones: []Milestone{{ID: "m", Title: "M", Priority: "urgent", Lessons: []SyllabusLesson{{ID: "l", Title: "L"}}}}},
	} {
		if _, err := m.ProposeRevision(context.Background(), "c", RevisionSpec{Summary: "s", Syllabus: s}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", s, err)
		}
	}
}

func hasFlag(flags []Flag, kind, item string) bool {
	for _, f := range flags {
		if f.Kind == kind && f.Item == item {
			return true
		}
	}
	return false
}

func TestPhasesTakeACheckpointWhenTheTurnPasses(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	set := func(phase string) PhaseResult {
		t.Helper()
		r, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: phase})
		if err != nil {
			t.Fatalf("SetPhase(%s): %v", phase, err)
		}
		if r.CheckpointError != "" {
			t.Fatalf("SetPhase(%s): checkpoint error %s", phase, r.CheckpointError)
		}
		return r
	}
	if r := set(PhaseTeaching); r.Checkpoint != nil || !r.Changed {
		t.Errorf("teaching = %+v, want no Checkpoint: it is still the agent's turn", r)
	}
	if r := set(PhaseTeaching); r.Changed {
		t.Errorf("the same Phase again = %+v, want no change", r)
	}
	if r := set(PhasePracticing); r.Checkpoint == nil || !r.Checkpoint.Committed {
		t.Errorf("practicing = %+v, want a Checkpoint of the agent's turn", r)
	}
	writeFile(t, m, "practice/answer/answer.txt", "40\n")
	if r := set(PhaseFeedback); r.Checkpoint == nil || !r.Checkpoint.Committed {
		t.Errorf("feedback = %+v, want a Checkpoint of the learner's turn", r)
	}
	log := git(t, filepath.Join(m.home, "c"), "log", "--format=%s")
	if !strings.HasPrefix(log, "[learner] answer: feedback\n[agent] answer: practicing\n") {
		t.Errorf("git log =\n%s", log)
	}

	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "missing", Phase: PhaseTeaching}); CodeOf(err) != CodeNotFound {
		t.Errorf("an unknown Lesson: err = %v, want not_found", err)
	}
	if err := os.Remove(filepath.Join(m.home, "c", "lessons", "answer.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("practicing without a Check: err = %v, want failed_precondition", err)
	}
}

func TestTheCompletionRule(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	check := func(want string) Attempt {
		t.Helper()
		a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
		if err != nil {
			t.Fatalf("RunCheck: %v", err)
		}
		if a.Outcome != want {
			t.Fatalf("Attempt = %+v, want %s", a, want)
		}
		return a
	}
	canComplete := func(want bool, reason string) {
		t.Helper()
		res, err := m.CheckResultsOf(ctx, "c", "answer")
		if err != nil {
			t.Fatal(err)
		}
		if res.CanComplete != want || !strings.Contains(res.Reason, reason) {
			t.Errorf("can complete = %v (%s), want %v (%s)", res.CanComplete, res.Reason, want, reason)
		}
	}

	canComplete(false, "never shown")
	practicing(t, m)
	canComplete(false, "has never run")
	failed := check(OutcomeFailed)
	if c := failed.Criteria[0]; c.ExitCode != 1 || !strings.Contains(c.Output, "not 42") {
		t.Errorf("the failed criterion = %+v", c)
	}
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); CodeOf(err) != CodeFailedPrecondition {
		t.Fatalf("completing after a failed Attempt: err = %v, want failed_precondition", err)
	}

	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	canComplete(false, "the last Attempt failed")
	passed := check(OutcomePassed)
	canComplete(true, "")

	// Changing the work or the Check after a pass means running it again.
	writeFile(t, m, "practice/answer/notes.md", "scratch\n")
	canComplete(false, "the work changed")
	if err := os.Remove(filepath.Join(m.home, "c", "practice", "answer", "notes.md")); err != nil {
		t.Fatal(err)
	}
	canComplete(true, "")
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "holds the answer", "holds the right answer", 1))
	canComplete(false, "the Check changed since it was shown")
	writeFile(t, m, "lessons/answer.md", answerLesson+"\nMore text never changes the Check.\n")
	canComplete(true, "")

	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer", Cards: []CardDraft{
		{Prompt: "What is the answer?", Answer: "42"},
	}})
	if err != nil {
		t.Fatalf("CompleteLesson: %v", err)
	}
	if !done.Changed || done.Attempt != passed.ID || len(done.Cards) != 1 || done.Checkpoint == nil || done.CheckpointError != "" {
		t.Fatalf("completion = %+v", done)
	}
	if !strings.HasPrefix(done.Cards[0].ID, "answer.") {
		t.Errorf("Card id = %s, want answer.<suffix>", done.Cards[0].ID)
	}
	cards, err := os.ReadFile(filepath.Join(m.home, "c", cardsFile))
	if err != nil || !strings.Contains(string(cards), `"prompt":"What is the answer?"`) {
		t.Errorf("%s = %s, %v", cardsFile, cards, err)
	}
	again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || again.Changed || again.Attempt != passed.ID || len(again.Cards) != 1 || again.Cards[0].ID != done.Cards[0].ID {
		t.Errorf("completing twice = %+v, %v; want the first completion, unchanged", again, err)
	}

	// A done Lesson stays done when the shared work changes later.
	writeFile(t, m, "practice/answer/answer.txt", "0\n")
	view, err := m.SyllabusOf(ctx, "c")
	if err != nil || view.Milestones[0].Lessons[0].Status != LessonDone {
		t.Errorf("after changing the work: %+v, %v", view, err)
	}
}

func TestAnAttemptErrorsWhenTheCheckCannotRun(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "[sh, check.sh]", "[no-such-program-anywhere]", 1))
	a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || a.Outcome != OutcomeErrored || !strings.Contains(a.Criteria[0].Reason, "could not start") {
		t.Errorf("a missing program: %+v, %v", a, err)
	}
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "[sh, check.sh]", "[sleep, '5']", 1))
	a, err = m.RunCheck(ctx, "c", "answer", CheckOptions{Timeout: 50 * time.Millisecond})
	if err != nil || a.Outcome != OutcomeErrored || !strings.Contains(a.Criteria[0].Reason, "longer than") {
		t.Errorf("a timeout: %+v, %v", a, err)
	}
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "[sh, check.sh]", "[sh, -c, 'echo changed >> answer.txt']", 1))
	a, err = m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || a.Outcome != OutcomeErrored || !strings.Contains(a.Reason, "changed answer.txt in practice/answer/") ||
		!strings.Contains(a.Reason, ".gitignore") {
		t.Errorf("work changed by the Check: %+v, %v", a, err)
	}
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: style\n    rubric: Names are clear\n---\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); CodeOf(err) != CodeCorrupt || !strings.Contains(err.Error(), "rubric") {
		t.Errorf("a rubric criterion: err = %v, want a clear corrupt error", err)
	}
}

func TestACrashDuringCompletionIsFinishedByTheNextCall(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	practicing(t, m)
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer", Cards: []CardDraft{{Prompt: "Q", Answer: "A"}}}); err == nil {
		t.Fatal("CompleteLesson survived the crash")
	}
	m.crash = nil
	if _, err := os.Stat(filepath.Join(m.home, "c", cardsFile)); !os.IsNotExist(err) {
		t.Fatalf("the crash happened after the Cards were written (err = %v)", err)
	}
	topic, err := m.readTopic("c")
	if err != nil || !hasFlag(topic.Flags, FlagInterruptedWrite, "") {
		t.Fatalf("status after the crash: %+v, %v", topic.Flags, err)
	}

	res, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || res.Changed || len(res.Cards) != 1 || res.Checkpoint == nil {
		t.Fatalf("completing again = %+v, %v; want the recorded completion and a Checkpoint", res, err)
	}
	due, err := m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || len(due.Cards) != 1 || due.Cards[0].Prompt != "Q" || !due.Cards[0].Draft {
		t.Errorf("due Cards after recovery = %+v, %v", due, err)
	}
	committed := git(t, filepath.Join(m.home, "c"), "show", "HEAD:"+cardsFile)
	if !strings.Contains(committed, `"prompt":"Q"`) {
		t.Errorf("the Checkpoint lacks the Card: %s", committed)
	}
}

func TestReviewsDecideDraftsAndScheduleCards(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	practicing(t, m)
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer", Cards: []CardDraft{
		{Prompt: "Keep me", Answer: "1"}, {Prompt: "Edit me", Answer: "2"}, {Prompt: "Drop me", Answer: "3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	keep, edit, drop := done.Cards[0].ID, done.Cards[1].ID, done.Cards[2].ID

	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: keep, Rating: RatingGood}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("reviewing a draft without deciding: err = %v, want invalid_argument", err)
	}
	r, err := m.RecordReview(ctx, "c", ReviewSpec{Card: keep, Rating: RatingGood, Draft: DraftKeep})
	if err != nil || r.Card.Draft || !r.Card.Due.After(t0) {
		t.Fatalf("keeping a draft = %+v, %v", r, err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: edit, Rating: RatingHard, Draft: DraftEdit,
		Prompt: "Edited", Answer: "2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: drop, Draft: DraftDrop}); err != nil {
		t.Fatal(err)
	}
	cards, err := os.ReadFile(filepath.Join(m.home, "c", cardsFile))
	if err != nil || !strings.Contains(string(cards), `"prompt":"Edited"`) || strings.Contains(string(cards), "Drop me") {
		t.Errorf("%s =\n%s", cardsFile, cards)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: drop, Rating: RatingGood}); CodeOf(err) != CodeNotFound {
		t.Errorf("reviewing a dropped Card: err = %v, want not_found", err)
	}

	due, err := m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || len(due.Cards) != 0 {
		t.Fatalf("right after the Reviews: %+v, %v; want nothing due", due, err)
	}
	m.setClock(t0.AddDate(0, 1, 0))
	due, err = m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || len(due.Cards) != 2 || due.Cards[0].ID != edit {
		t.Fatalf("a month later: %+v, %v; want both kept Cards, the harder one first", due, err)
	}
	// The schedule is replayed, so it is the same every time.
	again, err := m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || !again.Cards[0].Due.Equal(due.Cards[0].Due) || !again.Cards[1].Due.Equal(due.Cards[1].Due) {
		t.Errorf("replaying twice gave %+v, then %+v", due, again)
	}
}

func TestSessionsRecordTheNextStep(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	withSyllabus(t, m)
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Read the Lesson"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("closing without a Session: err = %v, want failed_precondition", err)
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyHalf, Focus: FocusLearn})
	if err != nil || opened.Resume.Lesson != "answer" || opened.Unclosed != nil {
		t.Fatalf("OpenSession = %+v, %v", opened, err)
	}
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: "tired"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an unknown Energy: err = %v, want invalid_argument", err)
	}
	closed, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write answer.txt", Context: "We stopped before the exercise."})
	if err != nil || closed.Session != opened.Session || closed.NextStep.Lesson != "answer" {
		t.Fatalf("CloseSession = %+v, %v", closed, err)
	}

	// The terminal closes without a Next step: the next Session says so.
	second, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil || second.Resume.NextStep == nil || second.Resume.NextStep.Step != "Write answer.txt" {
		t.Fatalf("second Session = %+v, %v", second, err)
	}
	third, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil || third.Unclosed == nil || third.Unclosed.ID != second.Session {
		t.Fatalf("third Session = %+v, %v; want the second reported as unclosed", third, err)
	}
	topic, err := m.readTopic("c")
	if err != nil || topic.Resume == nil || topic.Resume.NextStep.Context != "We stopped before the exercise." ||
		topic.Resume.OpenSession == nil || topic.Resume.OpenSession.ID != third.Session {
		t.Errorf("status resume = %+v, %v", topic.Resume, err)
	}
}
