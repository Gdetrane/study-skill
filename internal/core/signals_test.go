package core

import (
	"context"
	"testing"
)

func signals(t *testing.T, m *machine) Signals {
	t.Helper()
	sig, err := m.SignalsOf(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func lessonSignals(t *testing.T, sig Signals, id string) LessonSignals {
	t.Helper()
	for _, ls := range sig.Lessons {
		if ls.Lesson == id {
			return ls
		}
	}
	t.Fatalf("no signals for Lesson %s in %+v", id, sig.Lessons)
	return LessonSignals{}
}

// TestSignalsFollowTheLearner walks one Lesson: a try by the agent before
// the Check is shown, a failed first Attempt, feedback, hints, a fix, a
// pass, completion with a Card, and a Review.
func TestSignalsFollowTheLearner(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if sig := signals(t, m); len(sig.Lessons) != 0 || sig.Totals != (SignalTotals{}) {
		t.Fatalf("signals before any work = %+v", sig)
	}

	// The agent tries its Check before showing it: not measured.
	runCheck(t, m)
	practicing(t, m)
	if a := runCheck(t, m); a.Outcome != OutcomeFailed {
		t.Fatalf("first Attempt = %+v", a)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhaseFeedback}); err != nil {
		t.Fatal(err)
	}
	// The learner asks for a nudge; the agent offers a step.
	for kind, by := range map[string]string{HintNudge: HintByLearner, HintStep: HintByAgent} {
		if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: kind, RequestedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing,
		NextStep: "Fix the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if a := runCheck(t, m); a.Outcome != OutcomePassed {
		t.Fatalf("second Attempt = %+v", a)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer",
		Cards: []CardDraft{{Prompt: "What is the answer?", Answer: "42"}}})
	if err != nil || len(done.Cards) != 1 {
		t.Fatal(done, err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: done.Cards[0].ID, Draft: DraftKeep, Rating: RatingGood}); err != nil {
		t.Fatal(err)
	}

	sig := signals(t, m)
	ls := lessonSignals(t, sig, "answer")
	if ls.FirstTry == nil || *ls.FirstTry || ls.Attempts != 2 || ls.FeedbackRounds != 1 || !ls.Done ||
		ls.Title != "The answer" {
		t.Errorf("Lesson signals = %+v", ls)
	}
	if ls.Hints[HintNudge] != 1 || ls.Hints[HintStep] != 1 || ls.HintsRequested != 1 || ls.HintsOffered != 1 {
		t.Errorf("hints = %v, %d requested, %d offered", ls.Hints, ls.HintsRequested, ls.HintsOffered)
	}
	if ls.Reviews == nil || ls.Reviews.Good != 1 || sig.Reviews.Good != 1 {
		t.Errorf("Reviews = %+v, %+v", ls.Reviews, sig.Reviews)
	}
	if sig.Totals != (SignalTotals{FirstTryPassed: 0, FirstTryMeasured: 1, FeedbackRounds: 1, Hints: 2, HintsRequested: 1}) {
		t.Errorf("totals = %+v", sig.Totals)
	}
}

// TestAnErroredAttemptIsNotMeasured: a Check that cannot run says nothing
// about the learner, so it is neither the first try nor an Attempt.
func TestAnErroredAttemptIsNotMeasured(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: answer\n    run: [no-such-program-for-lamplight]\n---\n")
	practicing(t, m)
	if a := runCheck(t, m); a.Outcome != OutcomeErrored {
		t.Fatalf("Attempt = %+v, want errored", a)
	}
	if ls := lessonSignals(t, signals(t, m), "answer"); ls.FirstTry != nil || ls.Attempts != 0 {
		t.Errorf("Lesson signals after an errored Attempt = %+v", ls)
	}
}

func TestAFirstTryPass(t *testing.T) {
	m := learningTopic(t)
	completeAnswer(t, m)
	sig := signals(t, m)
	if ls := lessonSignals(t, sig, "answer"); ls.FirstTry == nil || !*ls.FirstTry || ls.Attempts != 1 {
		t.Errorf("Lesson signals = %+v", ls)
	}
	if sig.Totals.FirstTryPassed != 1 || sig.Totals.FirstTryMeasured != 1 {
		t.Errorf("totals = %+v", sig.Totals)
	}
}

// TestTheGapBetweenDevAndHeldOutScores: the work passes the learner's own
// test but not the Held-out data, a gap of 1.
func TestTheGapBetweenDevAndHeldOutScores(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", heldOutLesson)
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, m, ".heldout/answer/expected.txt", "43\n")
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	practicing(t, m)
	runCheck(t, m)
	ls := lessonSignals(t, signals(t, m), "answer")
	if len(ls.HeldOut) != 1 {
		t.Fatalf("held_out = %+v", ls.HeldOut)
	}
	g := ls.HeldOut[0]
	if g.Criterion != "hidden" || g.HeldOut != 0 || g.Dev == nil || *g.Dev != 1 || g.Gap == nil || *g.Gap != 1 {
		t.Errorf("gap = %+v (dev %v, gap %v)", g, deref(g.Dev), deref(g.Gap))
	}
}

func deref(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func TestSignalsIncludeAssessmentsAndTheLevel(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.RecordAssessment(ctx, "c", placement); err != nil {
		t.Fatal(err)
	}
	sig := signals(t, m)
	if len(sig.Assessments) != 1 || sig.Level == nil || sig.Level.Level != LevelIntermediate {
		t.Errorf("signals = %+v", sig)
	}
}

func TestFraction(t *testing.T) {
	score, max := 3.0, 4.0
	over := 9.0
	for name, tc := range map[string]struct {
		r    CriterionResult
		want any
	}{
		"a score":          {CriterionResult{Outcome: OutcomeFailed, Results: &CriterionScores{Score: &score, Max: &max}}, 0.75},
		"passed":           {CriterionResult{Outcome: OutcomePassed}, 1.0},
		"failed":           {CriterionResult{Outcome: OutcomeFailed}, 0.0},
		"errored":          {CriterionResult{Outcome: OutcomeErrored}, nil},
		"over the maximum": {CriterionResult{Outcome: OutcomePassed, Results: &CriterionScores{Score: &over, Max: &max}}, 1.0},
	} {
		if got := deref(fraction(tc.r)); got != tc.want {
			t.Errorf("%s: fraction = %v, want %v", name, got, tc.want)
		}
	}
}
