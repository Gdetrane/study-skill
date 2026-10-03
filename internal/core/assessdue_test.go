package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func recommended(t *testing.T, m *machine) *Recommendation {
	t.Helper()
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return status.Recommended
}

// finishAnswer passes the Check of Lesson answer and completes it,
// returning the completion.
func finishAnswer(t *testing.T, m *machine) LessonCompletion {
	t.Helper()
	ctx := context.Background()
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil || a.Outcome != OutcomePassed {
		t.Fatalf("RunCheck = %+v, %v", a, err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil {
		t.Fatalf("CompleteLesson: %v", err)
	}
	return done
}

func TestTheMilestoneAssessmentIsNextOnceItsLessonsAreDone(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if r := recommended(t, m); r == nil || r.Action == ActionAssess {
		t.Fatalf("before any Lesson is done: %+v", r)
	}

	done := finishAnswer(t, m)
	if done.Next == nil || done.Next.Code != NextAssessMilestone || done.Next.Milestone == nil || done.Next.Milestone.ID != "basics" {
		t.Fatalf("the completion that finishes the Milestone returned next = %+v", done.Next)
	}
	r := recommended(t, m)
	if r == nil || r.Action != ActionAssess || r.Milestone == nil || r.Milestone.ID != "basics" || r.Milestone.Number != 1 {
		t.Fatalf("status recommends %+v, want assess Milestone basics", r)
	}
	if strings.Contains(strings.ToLower(r.Text), "overdue") || strings.Contains(strings.ToLower(r.Text), "late") {
		t.Errorf("the cue must be a next action, never late: %q", r.Text)
	}
	again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || again.Changed || again.Next == nil || again.Next.Code != NextAssessMilestone {
		t.Errorf("completing again = %+v, %v; want the same cue", again, err)
	}

	end := AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Summary: "Answers well",
		Items: []AssessmentItem{{Area: "answers", Outcome: AnswerCorrect}}}
	if _, err := m.RecordAssessment(ctx, "c", end); err != nil {
		t.Fatal(err)
	}
	if r := recommended(t, m); r == nil || r.Action == ActionAssess {
		t.Errorf("after the Milestone's Assessment, status still recommends %+v", r)
	}
	if again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil || again.Next != nil {
		t.Errorf("after the Assessment, completing again = %+v, %v; want no cue", again.Next, err)
	}
}

// finishNext writes Lesson next with a Check like answer's, passes it and
// completes it.
func finishNext(t *testing.T, m *machine) LessonCompletion {
	t.Helper()
	ctx := context.Background()
	writeFile(t, m, "lessons/next.md", strings.Replace(answerLesson, "# The answer", "# The next one", 1))
	writeFile(t, m, "practice/next/check.sh", answerCheck)
	writeFile(t, m, "practice/next/answer.txt", "42\n")
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "next", Phase: PhasePracticing}); err != nil {
		t.Fatal(err)
	}
	if a, err := m.RunCheck(ctx, "c", "next", CheckOptions{}); err != nil || a.Outcome != OutcomePassed {
		t.Fatalf("RunCheck(next) = %+v, %v", a, err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "next"})
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func recordMilestoneAssessment(t *testing.T, m *machine, milestone string) {
	t.Helper()
	end := AssessmentSpec{Kind: AssessmentMilestone, Milestone: milestone, Summary: "Answers well",
		Items: []AssessmentItem{{Area: "answers", Outcome: AnswerCorrect}}}
	if _, err := m.RecordAssessment(context.Background(), "c", end); err != nil {
		t.Fatal(err)
	}
}

func TestTheAssessmentCueSurvivesAStop(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	revise(t, m, answerThenNext)
	finishAnswer(t, m)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Start the next Lesson by reading its notes"}); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionAssess || r.Milestone == nil || r.Milestone.ID != "basics" {
		t.Fatalf("after a stop, status recommends %+v, want the Assessment of basics", r)
	}
	if ns := status.Topics[0].Resume.NextStep; ns == nil || ns.Step != "Start the next Lesson by reading its notes" {
		t.Errorf("the Resume point no longer shows the Next step word for word: %+v", ns)
	}

	recordMilestoneAssessment(t, m, "basics")
	if r := recommended(t, m); r == nil || r.Action != ActionNextStep {
		t.Errorf("after the Assessment, status recommends %+v, want the Next step again", r)
	}
}

func TestTheAssessmentCueEndsWhenAnotherMilestoneStarts(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	revise(t, m, answerThenNext)
	finishAnswer(t, m)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "next", Phase: PhaseTeaching}); err != nil {
		t.Fatal(err)
	}
	if r := recommended(t, m); r == nil || r.Action == ActionAssess {
		t.Errorf("after starting a Lesson of the next Milestone, status still recommends %+v", r)
	}
}

func TestAPhaseInsideTheFinishedMilestoneKeepsTheCue(t *testing.T) {
	m := learningTopic(t)
	revise(t, m, answerThenNext)
	finishAnswer(t, m)
	// A Phase for Lesson answer arriving later, from another machine: it is
	// not a step towards another Milestone.
	appendLine(t, filepath.Join(m.home, "c", historyFile), `{"format":1,"id":"zz-phase","time":"2026-10-02T09:30:00Z",`+
		`"wall":"2026-10-02T09:30:00Z","type":"phase.set","data":{"lesson":"answer","phase":"feedback"}}`+"\n")
	if r := recommended(t, m); r == nil || r.Action != ActionAssess {
		t.Errorf("a Phase inside the finished Milestone ended the cue: %+v", r)
	}
}

func TestTheMostRecentlyFinishedMilestoneIsAssessed(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	revise(t, m, answerThenNext)
	if done := finishNext(t, m); done.Next == nil || done.Next.Milestone.ID != "more" {
		t.Fatalf("finishing Milestone more first: next = %+v", done.Next)
	}
	finishAnswer(t, m) // its Phases start a Lesson of another Milestone, so more is left
	r := recommended(t, m)
	if r == nil || r.Action != ActionAssess || r.Milestone.ID != "basics" {
		t.Fatalf("status recommends %+v, want the Assessment of basics, finished last", r)
	}
	again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "next"})
	if err != nil || again.Next != nil {
		t.Errorf("completing next again names another Milestone's Assessment: %+v, %v", again.Next, err)
	}
}

func TestTheAssessmentCueNeedsAnActiveTopic(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	finishAnswer(t, m)
	paused := TopicPaused
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{State: &paused}); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if due := status.Topics[0].AssessmentDue; due != nil {
		t.Errorf("a paused Topic has assessment_due %+v", due)
	}
	if r := status.Recommended; r == nil || r.Action != ActionResumeTopic {
		t.Errorf("a paused Topic recommends %+v", r)
	}
	if again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil || again.Next != nil {
		t.Errorf("a paused Topic's completion returns next = %+v, %v", again.Next, err)
	}

	// The first completion, on a Topic paused after its Check passed.
	m = learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil || a.Outcome != OutcomePassed {
		t.Fatalf("RunCheck = %+v, %v", a, err)
	}
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{State: &paused}); err != nil {
		t.Fatal(err)
	}
	if done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil || done.Next != nil {
		t.Errorf("completing a paused Topic's last Lesson returns next = %+v, %v", done.Next, err)
	}
}

func TestOnlyAFinishedMilestoneIsAssessed(t *testing.T) {
	t.Run("a Milestone with a Lesson still to do", func(t *testing.T) {
		m := learningTopic(t)
		revise(t, m, twoMilestones) // Basics holds answer and values
		if done := finishAnswer(t, m); done.Next != nil {
			t.Errorf("completing answer, with values still to do, returned next = %+v", done.Next)
		}
		if r := recommended(t, m); r == nil || r.Action == ActionAssess {
			t.Errorf("Basics still has a Lesson to do, yet status recommends %+v", r)
		}
	})
	t.Run("a Milestone of skipped Lessons only", func(t *testing.T) {
		m := learningTopic(t)
		skipped := clone(answerThenNext)
		skipped.Milestones[0].Lessons[0].Skipped = true
		revise(t, m, skipped)
		if r := recommended(t, m); r == nil || r.Action == ActionAssess {
			t.Errorf("a Milestone with every Lesson skipped has nothing to assess: %+v", r)
		}
	})
}

func TestRecommendRanksTheAssessment(t *testing.T) {
	due := &MilestoneRef{Number: 1, ID: "basics", Title: "Basics"}
	resume := &ResumePoint{Lesson: "next", NextStep: &NextStep{Step: "Read the notes"}}
	if r := recommend(Topic{ID: "c", State: TopicPaused, AssessmentDue: due, Resume: resume}); r.Action != ActionResumeTopic {
		t.Errorf("a paused Topic recommends %s, want resume_topic first", r.Action)
	}
	if r := recommend(Topic{ID: "c", State: TopicActive, AssessmentDue: due, Resume: resume}); r.Action != ActionAssess {
		t.Errorf("with a Next step and an Assessment due, status recommends %s, want assess", r.Action)
	}
}

func TestSessionOpenSuggestsTheAssessment(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	finishAnswer(t, m)
	for _, tc := range []struct{ energy, want string }{
		{EnergyFull, SuggestAssess}, {EnergyHalf, SuggestAssess}, {EnergyFumes, SuggestStop},
	} {
		opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: tc.energy, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if opened.Suggested == nil || opened.Suggested.Suggest != tc.want {
			t.Errorf("%s Energy suggests %+v, want %s", tc.energy, opened.Suggested, tc.want)
		}
	}
}

func TestACompletionFromAnotherMachineDoesNotBringTheCueBack(t *testing.T) {
	a, b := twoMachines(t, oneLessonSyllabus)
	finishAnswer(t, a)
	recordMilestoneAssessment(t, a, "basics")
	takeCheckpoint(t, a)
	finishAnswer(t, b) // the same Lesson, completed later on the other machine
	takeCheckpoint(t, b)
	pull(t, a, b)
	if r := recommended(t, a); r == nil || r.Action == ActionAssess {
		t.Errorf("a second completion merged in brought the cue back: %+v", r)
	}
}

// An Assessment recorded before the Milestone's last Lesson was completed did
// not assess the finished Milestone: the same Assessment recorded after it,
// without a request id, is recorded, and ends the cue.
func TestAnEarlyAssessmentIsNoRetryOnceTheMilestoneEnds(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	both := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Outcome: "Write a program that answers",
		Priority: PriorityMust, Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1},
			{ID: "next", Title: "The next one", Hours: 1}}}}}
	revise(t, m, both)
	finishAnswer(t, m)
	end := AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Summary: "Answers well",
		Items: []AssessmentItem{{Area: "answers", Outcome: AnswerCorrect}}}
	if r, err := m.RecordAssessment(ctx, "c", end); err != nil || !r.Changed {
		t.Fatalf("the early Assessment = %+v, %v", r, err)
	}
	if again, err := m.RecordAssessment(ctx, "c", end); err != nil || again.Changed {
		t.Fatalf("a retry before the Milestone ends = %+v, %v; want nothing recorded", again, err)
	}
	finishNext(t, m)
	if r := recommended(t, m); r == nil || r.Action != ActionAssess {
		t.Fatalf("after the Milestone's last Lesson, status recommends %+v, want assess", r)
	}
	after, err := m.RecordAssessment(ctx, "c", end)
	if err != nil || !after.Changed {
		t.Fatalf("the same Assessment after the Milestone ended = %+v, %v; want it recorded", after, err)
	}
	if r := recommended(t, m); r != nil && r.Action == ActionAssess {
		t.Errorf("after the Assessment, status still recommends %+v", r)
	}
}
