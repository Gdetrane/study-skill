package core

import (
	"context"
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

func TestTheAssessmentCueGivesWayWhenTheLearnerMovesOn(t *testing.T) {
	ctx := context.Background()
	t.Run("a Lesson of the next Milestone starts", func(t *testing.T) {
		m := learningTopic(t)
		revise(t, m, answerThenNext)
		finishAnswer(t, m)
		if r := recommended(t, m); r == nil || r.Action != ActionAssess {
			t.Fatalf("after finishing Basics: %+v", r)
		}
		if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "next", Phase: PhaseTeaching}); err != nil {
			t.Fatal(err)
		}
		if r := recommended(t, m); r == nil || r.Action == ActionAssess {
			t.Errorf("after starting the next Lesson, status still recommends %+v", r)
		}
	})
	t.Run("a Next step is recorded", func(t *testing.T) {
		m := learningTopic(t)
		revise(t, m, answerThenNext)
		finishAnswer(t, m)
		if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Start the next Milestone tomorrow"}); err != nil {
			t.Fatal(err)
		}
		if r := recommended(t, m); r == nil || r.Action != ActionNextStep {
			t.Errorf("after a Next step, status recommends %+v, want the Next step", r)
		}
	})
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
