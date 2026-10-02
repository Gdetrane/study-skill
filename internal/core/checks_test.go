package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// heldOutLesson has a run criterion and a held-out criterion: eval.sh
// compares answer.txt with the Held-out data, prints that data (which must
// never be shown or recorded) and writes its score to the results file.
const heldOutLesson = `---
check:
  - id: answer
    describe: answer.txt holds the answer
    run: [sh, check.sh]
  - id: hidden
    describe: The answer on the test set
    held_out: [sh, eval.sh]
---
# The answer
`

const heldOutEval = `expected=$(cat "$STUDY_HELDOUT_DIR/expected.txt")
echo "SECRET-$expected"
if [ "$(cat answer.txt)" = "$expected" ]; then
  printf '{"format":1,"passed":true,"score":1,"max":1,"summary":"1 of 1 cases"}' > "$STUDY_RESULTS"
else
  printf '{"format":1,"passed":false,"score":0,"max":1,"summary":"0 of 1 cases"}' > "$STUDY_RESULTS"
fi
`

// rubricLesson has a run criterion and a rubric item.
const rubricLesson = `---
check:
  - id: answer
    run: [sh, check.sh]
  - id: explained
    rubric: The notes explain why the answer is 42
---
`

// writtenLesson is written work: one rubric item, graded from a photo.
const writtenLesson = `---
check:
  - id: working
    rubric: The working shows each step
---
`

func runCheck(t *testing.T, m *machine) Attempt {
	t.Helper()
	a, err := m.RunCheck(context.Background(), "c", "answer", CheckOptions{})
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	return a
}

func criterion(a Attempt, id string) CriterionResult {
	for _, r := range a.Criteria {
		if r.ID == id {
			return r
		}
	}
	return CriterionResult{}
}

func TestCheckCriteriaKinds(t *testing.T) {
	check, err := parseCheck([]byte(`---
check:
  - id: tests
    describe: The tests pass
    run: [sleep, 1]
  - id: names
    rubric: Every name says what it does
  - id: accuracy
    held_out: [python3, eval.py]
---
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Criterion{
		{ID: "tests", Kind: CriterionRun, Describe: "The tests pass", Command: []string{"sleep", "1"}},
		{ID: "names", Kind: CriterionRubric, Rubric: "Every name says what it does"},
		{ID: "accuracy", Kind: CriterionHeldOut, Command: []string{"python3", "eval.py"}},
	}
	if fmt.Sprint(check) != fmt.Sprint(want) {
		t.Errorf("criteria = %+v\nwant %+v", check, want)
	}

	many := "---\ncheck:\n"
	for i := range maxCriteria + 1 {
		many += fmt.Sprintf("  - id: c%d\n    run: [true]\n", i)
	}
	for name, header := range map[string]string{
		"no kind":            "  - id: a\n    describe: nothing to do\n",
		"two kinds":          "  - id: a\n    run: [true]\n    rubric: Also this\n",
		"rubric not text":    "  - id: a\n    rubric: [one, two]\n",
		"empty rubric":       "  - id: a\n    rubric: \"\"\n",
		"run as a string":    "  - id: a\n    run: go test ./...\n",
		"empty held-out":     "  - id: a\n    held_out: []\n",
		"too many criteria":  strings.TrimPrefix(many, "---\ncheck:\n"),
		"no program":         "  - id: a\n    run: [\"\", x]\n",
		"control character":  "  - id: a\n    rubric: \"bell\\a\"\n",
		"duplicate criteria": "  - id: a\n    run: [true]\n  - id: a\n    run: [true]\n",
	} {
		if _, err := parseCheck([]byte("---\ncheck:\n" + header + "---\n")); CodeOf(err) != CodeCorrupt {
			t.Errorf("%s: err = %v, want corrupt", name, err)
		}
	}
}

func TestTheRubricTextIsPartOfTheCheckVersion(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", rubricLesson)
	home, topic, err := m.openTopicFolder("c")
	if err != nil {
		t.Fatal(err)
	}
	_, before, err := readCheck(topic, "answer")
	topic.Close()
	home.Close()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, m, "lessons/answer.md", strings.Replace(rubricLesson, "explain why", "say why", 1))
	home, topic, _ = m.openTopicFolder("c")
	_, after, err := readCheck(topic, "answer")
	topic.Close()
	home.Close()
	if err != nil || before == after {
		t.Errorf("editing a rubric item kept the version %s (%v)", after, err)
	}
}

func TestResultsFiles(t *testing.T) {
	ctx := context.Background()
	write := func(json string) string {
		return fmt.Sprintf("[sh, -c, 'printf %%s %q > \"$STUDY_RESULTS\"']", json)
	}
	for _, tc := range []struct {
		name, run, outcome, reason string
		check                      func(t *testing.T, r CriterionResult)
	}{
		{name: "no results file", run: "[sh, check.sh]", outcome: OutcomeFailed},
		{name: "a score", run: write(`{"format":1,"score":17,"max":20,"metrics":{"precision":0.9},"summary":"17 of 20"}`),
			outcome: OutcomePassed, check: func(t *testing.T, r CriterionResult) {
				s := r.Results
				if s == nil || *s.Score != 17 || *s.Max != 20 || s.Metrics["precision"] != 0.9 || s.Summary != "17 of 20" {
					t.Errorf("results = %+v", s)
				}
			}},
		{name: "a score out of 1 by default", run: write(`{"score":0.75}`), outcome: OutcomePassed,
			check: func(t *testing.T, r CriterionResult) {
				if r.Results == nil || *r.Results.Max != 1 {
					t.Errorf("results = %+v", r.Results)
				}
			}},
		{name: "passed false", run: write(`{"passed":false}`), outcome: OutcomeFailed},
		{name: "not JSON", run: write(`{"score":`), outcome: OutcomeErrored, reason: "not valid"},
		{name: "score above max", run: write(`{"score":3,"max":2}`), outcome: OutcomeErrored, reason: "more than its max"},
		{name: "negative score", run: write(`{"score":-1}`), outcome: OutcomeErrored, reason: "negative"},
		{name: "newer format", run: write(`{"format":2}`), outcome: OutcomeErrored, reason: "upgrade study"},
		{name: "bad metric name", run: write(`{"metrics":{"Has Spaces":1}}`), outcome: OutcomeErrored, reason: "metric name"},
		{name: "too large", run: `[sh, -c, 'head -c 70000 /dev/zero | tr "\\000" a > "$STUDY_RESULTS"']`,
			outcome: OutcomeErrored, reason: "larger than"},
		{name: "a link", run: `[sh, -c, 'ln -s /etc/hostname "$STUDY_RESULTS"']`, outcome: OutcomeErrored, reason: "not a regular file"},
		{name: "no Held-out data for a run", run: `[sh, -c, 'test -z "$STUDY_HELDOUT_DIR"']`, outcome: OutcomePassed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := learningTopic(t)
			writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: answer\n    run: "+tc.run+"\n---\n")
			practicing(t, m)
			a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
			if err != nil {
				t.Fatal(err)
			}
			r := criterion(a, "answer")
			if r.Outcome != tc.outcome || a.Outcome != tc.outcome || !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("criterion = %+v (Attempt %s), want %s with %q", r, a.Outcome, tc.outcome, tc.reason)
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

// heldOutTopic is Topic c with the held-out Lesson, its Held-out data and
// its exercise answered wrongly, moved to practicing.
func heldOutTopic(t *testing.T) *machine {
	t.Helper()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", heldOutLesson)
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n")
	practicing(t, m)
	return m
}

// TestTheHeldOutJourney follows the issue's journey: a first failure, which
// is the counted measurement, feedback, a fix, then completion with the
// original score kept. Held-out results never block completion, and the
// Held-out data never shows up in output or in the History.
func TestTheHeldOutJourney(t *testing.T) {
	ctx := context.Background()
	m := heldOutTopic(t)

	first := runCheck(t, m)
	hidden := criterion(first, "hidden")
	if first.Outcome != OutcomeFailed || hidden.Kind != CriterionHeldOut || hidden.Outcome != OutcomeFailed ||
		hidden.Counted == nil || !*hidden.Counted || hidden.Results == nil || *hidden.Results.Score != 0 {
		t.Fatalf("first Attempt = %+v, hidden = %+v", first, hidden)
	}
	if hidden.Output != "" {
		t.Errorf("a held-out criterion's output was shown: %q", hidden.Output)
	}

	// Feedback, then back to practicing: the failed Attempt calls for a Next
	// step that names the fix.
	m.setPhase(t, "answer", PhaseFeedback)
	res, err := m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || res.Next == nil || res.Next.Code != NextFixStep || !strings.Contains(res.Next.Text, "Next step that names the fix") {
		t.Errorf("check results next = %+v, %v", res.Next, err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); CodeOf(err) != CodeInvalidArgument ||
		!strings.Contains(err.Error(), "names the fix") {
		t.Errorf("back to practicing without a Next step: err = %v", err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing,
		NextStep: "Fix the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	second := runCheck(t, m)
	if h := criterion(second, "hidden"); second.Outcome != OutcomePassed || h.Counted == nil || *h.Counted ||
		*h.Results.Score != 1 {
		t.Errorf("second Attempt = %+v, hidden = %+v; want passed, not counted", second, h)
	}
	res, err = m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || len(res.HeldOut) != 1 {
		t.Fatalf("check results = %+v, %v", res, err)
	}
	if h := res.HeldOut[0]; h.CountedAttempt != first.ID || *h.Counted.Score != 0 || h.LatestAttempt != second.ID ||
		*h.Latest.Score != 1 {
		t.Errorf("held-out status = %+v; want the first score counted and kept", h)
	}
	if !res.CanComplete {
		t.Fatalf("cannot complete: %s", res.Reason)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || !done.Changed || done.Attempt != second.ID {
		t.Errorf("completion = %+v, %v", done, err)
	}

	history, err := os.ReadFile(filepath.Join(m.home, "c", historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(history), "SECRET") {
		t.Error("the Held-out data reached the History")
	}
}

func TestAnErroredHeldOutRunLeavesTheCountUnused(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", heldOutLesson)
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	practicing(t, m)

	// No Held-out data yet: errored, which uses nothing up.
	first := runCheck(t, m)
	if h := criterion(first, "hidden"); h.Outcome != OutcomeErrored || h.Counted != nil ||
		!strings.Contains(h.Reason, "no Held-out data") {
		t.Errorf("without Held-out data: %+v", h)
	}
	if first.Outcome != OutcomeFailed {
		t.Errorf("an errored held-out run changed the Attempt's outcome: %s", first.Outcome)
	}
	// A command that writes no results file is errored too.
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n")
	writeFile(t, m, "practice/answer/eval.sh", "echo no results\n")
	if h := criterion(runCheck(t, m), "hidden"); h.Outcome != OutcomeErrored || h.Counted != nil ||
		!strings.Contains(h.Reason, "no results file") {
		t.Errorf("without a results file: %+v", h)
	}
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || !*h.Counted {
		t.Errorf("the first run that produced results: %+v, want counted", h)
	}
}

func TestEditingTheCheckCannotMakeAFreshFirstRun(t *testing.T) {
	m := heldOutTopic(t)
	first := runCheck(t, m)
	writeFile(t, m, "lessons/answer.md", strings.Replace(heldOutLesson, "on the test set", "on fresh cases", 1))
	if _, err := m.SetPhase(context.Background(), "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing,
		NextStep: "Fix the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}
	if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || *h.Counted {
		t.Errorf("after editing the Check: %+v, want not counted", h)
	}
	// The History agrees: one counted measurement, the first.
	res, err := m.CheckResultsOf(context.Background(), "c", "answer")
	if err != nil {
		t.Fatal(err)
	}
	if h := res.HeldOut[0]; h.CountedAttempt != first.ID {
		t.Errorf("held-out status = %+v; want the first run counted", h)
	}
	for _, at := range res.Attempts[1:] {
		if h := criterion(at, "hidden"); h.Counted == nil || *h.Counted {
			t.Errorf("Attempt %s = %+v, want not counted", at.ID, h)
		}
	}
}

func TestRubricItemsNeedAGradeForTheCurrentWork(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", rubricLesson)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	grade := func(g string) (RubricGraded, error) {
		return m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "explained", Grade: g,
			Note: "The notes say why"})
	}
	if _, err := grade(GradeMet); CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "never shown") {
		t.Errorf("grading before practicing: err = %v", err)
	}
	practicing(t, m)
	runCheck(t, m)
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
	canComplete(false, "rubric item explained has no grade")

	for _, bad := range []RubricSpec{
		{Lesson: "answer", Criterion: "explained", Grade: "excellent"},
		{Lesson: "answer", Criterion: "answer", Grade: GradeMet},
		{Lesson: "answer", Criterion: "missing", Grade: GradeMet},
		{Lesson: "answer", Criterion: "explained", Grade: GradeMet, Note: "\x1b[2J"},
	} {
		if _, err := m.RecordRubricGrade(ctx, "c", bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}

	dry, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "explained", Grade: GradeNotMet, DryRun: true})
	if err != nil || !dry.Changed || dry.Grade.Grade != GradeNotMet {
		t.Errorf("dry run = %+v, %v", dry, err)
	}
	canComplete(false, "has no grade")

	// Any grade counts: rubric items are graded, not passed.
	g, err := grade(GradeNotMet)
	if err != nil || !g.Changed || g.Grade.Event == "" {
		t.Fatalf("grade = %+v, %v", g, err)
	}
	if again, err := grade(GradeNotMet); err != nil || again.Changed || again.Grade.Event != g.Grade.Event {
		t.Errorf("the same grade again = %+v, %v; want nothing recorded", again, err)
	}
	canComplete(true, "")

	// Changing the work means running the Check, and grading, again.
	writeFile(t, m, "practice/answer/notes.md", "42 is the answer\n")
	runCheck(t, m)
	canComplete(false, "the work changed since rubric item explained was graded")
	if _, err := grade(GradeMet); err != nil {
		t.Fatal(err)
	}
	canComplete(true, "")

	// So does changing the Check: once it is shown again, a grade for the
	// old Check no longer counts.
	writeFile(t, m, "lessons/answer.md", strings.Replace(rubricLesson, "explain why", "say why", 1))
	practicing(t, m)
	runCheck(t, m)
	canComplete(false, "rubric item explained was graded for another version of the Check")
	met, err := grade(GradeMet)
	if err != nil {
		t.Fatal(err)
	}
	canComplete(true, "")
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || len(done.Grades) != 1 || done.Grades[0] != met.Grade.Event {
		t.Fatalf("completion = %+v, %v; want it to rely on the latest grade", done, err)
	}
	if _, err := grade(GradePartly); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("grading a done Lesson: err = %v", err)
	}
}

func TestWrittenWorkIsGradedFromItsFiles(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", writtenLesson)
	writeFile(t, m, "practice/answer/page1.jpg", "\xff\xd8\xff photo bytes")
	writeFile(t, m, "practice/answer/answers.md", "1. 42\n")
	writeFile(t, m, "notes/outside.md", "not the work\n")
	practicing(t, m)

	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("study check on a Check with only rubric items: err = %v", err)
	}
	for _, name := range []string{"../../notes/outside.md", "/etc/hostname", "missing.jpg", "."} {
		if _, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "working",
			Grade: GradeMet, LookedAt: []string{name}}); err == nil {
			t.Errorf("looking at %q was accepted", name)
		}
	}
	g, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "working", Grade: GradeMet,
		LookedAt: []string{"page1.jpg", "practice/answer/answers.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if files := g.Grade.LookedAt; len(files) != 2 || files[0].Path != "practice/answer/page1.jpg" ||
		!strings.HasPrefix(files[0].Hash, "sha256:") || files[1].Path != "practice/answer/answers.md" {
		t.Errorf("looked at = %+v", files)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || !done.Changed || done.Attempt != "" || len(done.Grades) != 1 {
		t.Fatalf("completing written work = %+v, %v", done, err)
	}
	history, err := os.ReadFile(filepath.Join(m.home, "c", historyFile))
	if err != nil || strings.Contains(string(history), "photo bytes") {
		t.Errorf("the photo's bytes reached the History (%v)", err)
	}
}

func TestACompletedLessonStaysDone(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	practicing(t, m)
	runCheck(t, m)
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil {
		t.Fatal(err)
	}
	// Later changes to the shared code, the work or the Check never reopen it.
	writeFile(t, m, "practice/answer/check.sh", "exit 1\n")
	writeFile(t, m, "lessons/answer.md", rubricLesson)
	again, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || again.Changed {
		t.Errorf("completing again = %+v, %v; want nothing recorded", again, err)
	}
	if res, err := m.CheckResultsOf(ctx, "c", "answer"); err != nil || !res.Done || res.CanComplete {
		t.Errorf("check results = %+v, %v", res, err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("reopening by phase_set: err = %v", err)
	}
}

func TestACheckReportsItsProgress(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: slow\n    run: [sleep, '0.3']\n  - id: quick\n    run: [true]\n---\n")
	practicing(t, m)
	var mu sync.Mutex
	var seen []string
	_, err := m.RunCheck(context.Background(), "c", "answer", CheckOptions{ProgressEvery: 50 * time.Millisecond,
		Progress: func(p CheckProgress) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, fmt.Sprintf("%s %d/%d %s", p.Criterion, p.Index, p.Total, p.State))
		}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(seen, "; ")
	for _, want := range []string{"slow 1/2 started", "slow 1/2 running", "slow 1/2 finished", "quick 2/2 started", "quick 2/2 finished"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress %q lacks %q", got, want)
		}
	}
}

func TestCrashesWhileGradingARubricItem(t *testing.T) {
	ctx := context.Background()
	spec := RubricSpec{Lesson: "answer", Criterion: "explained", Grade: GradeMet}
	for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
		t.Run(point, func(t *testing.T) {
			m := learningTopic(t)
			writeFile(t, m, "lessons/answer.md", rubricLesson)
			practicing(t, m)
			m.crash = crashOnce(point)
			_, err := m.RecordRubricGrade(ctx, "c", spec)
			m.crash = nil
			if err != nil && !errors.Is(err, errCrash) {
				t.Fatalf("the write failed: %v", err)
			}
			dry := spec
			dry.DryRun = true
			dryRes, dryErr := m.RecordRubricGrade(ctx, "c", dry)
			realRes, realErr := m.RecordRubricGrade(ctx, "c", spec)
			if dryErr != nil || realErr != nil || dryRes.Changed != realRes.Changed {
				t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryRes.Changed, dryErr, realRes.Changed, realErr)
			}
			if hasIntentFile(t, m) {
				t.Error("the intent marker survived the next write")
			}
			s := replayFolder(t, filepath.Join(m.home, "c"))
			if len(s.flags) != 0 {
				t.Errorf("flags = %+v", s.flags)
			}
			if n := countEvents(s, eventRubricGraded); n != 1 {
				t.Errorf("%d rubric.graded Events, want 1", n)
			}
		})
	}
}

// TestChecksOnTwoMachines: a rubric item graded on both machines, and a
// held-out criterion measured first on both, are flagged after a merge.
func TestChecksOnTwoMachines(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	writeFile(t, a, "lessons/answer.md", strings.Replace(heldOutLesson, "---\n# The answer",
		"  - id: explained\n    rubric: The notes explain why\n---\n# The answer", 1))
	writeFile(t, a, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, a, ".heldout/answer/expected.txt", "42\n")
	practicing(t, a)
	takeCheckpoint(t, a)
	pull(t, b, a)

	for _, m := range []*machine{a, b} {
		if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || !*h.Counted {
			t.Fatalf("each machine's first run claims the count: %+v", h)
		}
		if _, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "explained", Grade: GradeMet}); err != nil {
			t.Fatal(err)
		}
	}
	onA, onB := merge(t, a, b)
	for name, flags := range map[string][]string{"a": onA, "b": onB} {
		if !mentions(flags, "measured first twice") || !mentions(flags, "graded on two machines") {
			t.Errorf("flags on %s = %q", name, flags)
		}
	}
	res, err := b.CheckResultsOf(ctx, "c", "answer")
	if err != nil {
		t.Fatal(err)
	}
	counted := 0
	for _, at := range res.Attempts {
		if h := criterion(at, "hidden"); h.Counted != nil && *h.Counted {
			counted++
		}
	}
	if counted != 1 {
		t.Errorf("%d counted measurements after the merge, want 1", counted)
	}
}
