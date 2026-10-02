package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The tests in this file hold the rules the review of the Checks module
// settled: what counts as a held_out measurement, what a grade can look at,
// and what a Check's command can see.

func TestACheckWithOnlyHeldOutCriteriaIsRefused(t *testing.T) {
	_, err := parseCheck([]byte("---\ncheck:\n  - id: hidden\n    held_out: [sh, eval.sh]\n---\n"))
	if CodeOf(err) != CodeCorrupt || !strings.Contains(err.Error(), "never decide") {
		t.Errorf("a held_out-only Check: err = %v, want corrupt", err)
	}
	// With a rubric item it is accepted; the Attempt then reports the
	// held_out evaluations and is never passed when one failed.
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: names\n    rubric: Names are clear\n"+
		"  - id: hidden\n    held_out: [sh, eval.sh]\n---\n")
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n")
	practicing(t, m)
	if a := runCheck(t, m); a.Outcome != OutcomeFailed {
		t.Errorf("an Attempt whose only held_out evaluation failed is %s, want failed", a.Outcome)
	}
	if res, err := m.CheckResultsOf(context.Background(), "c", "answer"); err != nil || res.Next != nil {
		t.Errorf("a failed held_out evaluation asks for a fix: %+v, %v", res.Next, err)
	}
}

// TestAnErroredAttemptNeverCountsAHeldOutRun covers each way an Attempt, or
// a held_out criterion, can error: the run then never uses up the count.
func TestAnErroredAttemptNeverCountsAHeldOutRun(t *testing.T) {
	t.Run("a run criterion writes into the work", func(t *testing.T) {
		m := heldOutTopic(t)
		writeFile(t, m, "practice/answer/check.sh", "echo built > build.out\n"+answerCheck)
		a := runCheck(t, m)
		if h := criterion(a, "hidden"); a.Outcome != OutcomeErrored || h.Counted == nil || *h.Counted ||
			h.NotCounted != notCountedErrored {
			t.Errorf("Attempt %s, hidden %+v; want errored and not counted", a.Outcome, h)
		}
		if err := os.Remove(filepath.Join(m.home, "c", "practice/answer/build.out")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, m, "practice/answer/check.sh", answerCheck)
		if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || !*h.Counted {
			t.Errorf("the first clean run: %+v, want counted", h)
		}
	})
	t.Run("a run criterion cannot start", func(t *testing.T) {
		m := heldOutTopic(t)
		writeFile(t, m, "lessons/answer.md", strings.Replace(heldOutLesson, "run: [sh, check.sh]", "run: [no-such-program-anywhere]", 1))
		practicing(t, m)
		a := runCheck(t, m)
		if h := criterion(a, "hidden"); a.Outcome != OutcomeErrored || h.Counted == nil || *h.Counted {
			t.Errorf("Attempt %s, hidden %+v; want errored and not counted", a.Outcome, h)
		}
	})
	t.Run("a held_out command writes into the work", func(t *testing.T) {
		ctx := context.Background()
		m := heldOutTopic(t)
		writeFile(t, m, "practice/answer/answer.txt", "42\n")
		writeFile(t, m, "practice/answer/eval.sh", "echo p > predictions.csv\n"+heldOutEval)
		first := runCheck(t, m)
		h := criterion(first, "hidden")
		if first.Outcome != OutcomePassed || h.Outcome != OutcomeErrored || h.Counted != nil ||
			!strings.Contains(h.Reason, "must not write into the work") {
			t.Errorf("Attempt %s, hidden %+v; want the Attempt to stand and the held_out run errored", first.Outcome, h)
		}
		// predictions.csv is now part of the work, so the next Attempt sees
		// no change: the held_out run counts and the Lesson can complete.
		second := runCheck(t, m)
		if h := criterion(second, "hidden"); h.Counted == nil || !*h.Counted {
			t.Errorf("the next run: %+v, want counted", h)
		}
		if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil {
			t.Errorf("completing: %v", err)
		}
	})
}

func TestHeldOutRunsCountOnlyOnTheCheckShown(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", heldOutLesson)
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n")

	// The agent tries its Check on the starter code before showing it, and
	// again once the Lesson has Attempts.
	for i := range 2 {
		if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || *h.Counted || h.NotCounted != notCountedNotShown {
			t.Errorf("run %d before the Check was shown: %+v, want not counted", i+1, h)
		}
	}
	practicing(t, m) // that failure asks for no fix: the learner had not started
	// An edited Check, not shown again, measures nothing either.
	writeFile(t, m, "lessons/answer.md", strings.Replace(heldOutLesson, "on the test set", "on other cases", 1))
	if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || *h.Counted || h.NotCounted != notCountedOtherShow {
		t.Errorf("a Check not shown: %+v, want not counted", h)
	}
	writeFile(t, m, "lessons/answer.md", heldOutLesson)
	if h := criterion(runCheck(t, m), "hidden"); h.Counted == nil || !*h.Counted {
		t.Errorf("the first run of the shown Check: %+v, want counted", h)
	}
}

func TestHeldOutResultsNeedAScore(t *testing.T) {
	for _, results := range []string{`{}`, `null`, `[1]`, `{"passed":true}`} {
		t.Run(results, func(t *testing.T) {
			m := heldOutTopic(t)
			writeFile(t, m, "practice/answer/eval.sh", "printf '"+results+"' > \"$STUDY_RESULTS\"\n")
			if h := criterion(runCheck(t, m), "hidden"); h.Outcome != OutcomeErrored || h.Counted != nil {
				t.Errorf("results %s: %+v, want errored and never counted", results, h)
			}
		})
	}
}

func TestACheckSeesOnlyItsOwnStudyVariables(t *testing.T) {
	t.Setenv("STUDY_HELDOUT_DIR", "/leaked/heldout")
	t.Setenv("STUDY_RESULTS", "/leaked/results")
	t.Setenv("STUDY_SOMETHING", "leaked")
	m := learningTopic(t)
	writeFile(t, m, "practice/answer/env.sh", `test -z "$STUDY_HELDOUT_DIR" && test -z "$STUDY_SOMETHING" && `+
		`test "$STUDY_RESULTS" != /leaked/results && test "$STUDY_LESSON" = answer`+"\n")
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: env\n    run: [sh, env.sh]\n---\n")
	practicing(t, m)
	if r := criterion(runCheck(t, m), "env"); r.Outcome != OutcomePassed {
		t.Errorf("a run criterion saw inherited STUDY_ variables: %+v", r)
	}
}

func TestResultsFilesAreReadSafely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	if _, present, problem := readResults(path); !present || !strings.Contains(problem, "not a regular file") {
		t.Errorf("a FIFO: present %v, problem %q", present, problem)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", path); err != nil {
		t.Fatal(err)
	}
	if _, _, problem := readResults(path); !strings.Contains(problem, "a link") {
		t.Errorf("a link: problem %q", problem)
	}
	// Text from the file is quoted only in part.
	for name, content := range map[string]string{
		"metric name": `{"metrics":{"` + strings.Repeat("Z", 5000) + `":1}}`,
		"JSON error":  `{"score":1` + strings.Repeat("7", 5000) + `e400}`,
	} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, problem := readResults(path); problem == "" || len([]rune(problem)) > 300 {
			t.Errorf("%s: problem of %d runes", name, len([]rune(problem)))
		}
	}
}

func TestLookedAtOnlyNamesFilesTheSnapshotSees(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", writtenLesson)
	writeFile(t, m, "practice/answer/.gitignore", "*.jpg\npeek\n")
	writeFile(t, m, "practice/answer/page1.jpg", "an ignored photo")
	writeFile(t, m, "practice/answer/answers.md", "1. 42\n")
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n")
	if err := os.Symlink("../../.heldout/answer", filepath.Join(m.home, "c", "practice/answer/peek")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("answers.md", filepath.Join(m.home, "c", "practice/answer/link.md")); err != nil {
		t.Fatal(err)
	}
	practicing(t, m)
	grade := func(files ...string) error {
		_, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "working", Grade: GradeMet,
			LookedAt: files})
		return err
	}
	for name, file := range map[string]string{
		"an ignored photo":                "page1.jpg",
		"an ignored link into .heldout":   "peek/expected.txt",
		"a link inside the folder":        "link.md",
		"a path out of the folder":        "../../.heldout/answer/expected.txt",
		"the Topic's Held-out data":       ".heldout/answer/expected.txt",
		"a file that does not exist":      "missing.md",
		"the practice folder itself":      ".",
		"an absolute path":                "/etc/hostname",
		"a Topic path outside the folder": "practice/other/answers.md",
	} {
		if err := grade(file); err == nil {
			t.Errorf("%s (%s) was accepted", name, file)
		}
	}
	if err := grade("link.md"); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("a link: err = %v, want it named as a link", err)
	}
	if err := grade("answers.md"); err != nil {
		t.Fatalf("a file of the work: %v", err)
	}
	// Changing it makes the grade stale.
	writeFile(t, m, "practice/answer/answers.md", "1. 41\n")
	res, err := m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || res.Rubric[0].Current || res.CanComplete {
		t.Errorf("after the file changed: %+v, can complete %v (%v)", res.Rubric, res.CanComplete, err)
	}
}

// TestAGradeIsStaleWhenAFileItLookedAtChanged checks the completion rule's
// own re-hash of looked_at files, apart from the snapshot that also covers
// them.
func TestAGradeIsStaleWhenAFileItLookedAtChanged(t *testing.T) {
	ls := &lessonState{shownCheck: "v1", grades: map[string]*RubricGrade{"working": {
		Event: "e1", Grade: GradeMet, CheckVersion: "v1", Snapshot: "s1",
		LookedAt: []WorkFile{{Path: "practice/answer/answers.md", Hash: "sha256:old"}},
	}}}
	w := work{checkVersion: "v1", snapshot: "s1", lookedAt: map[string]string{"practice/answer/answers.md": "sha256:old"}}
	if why := staleGrade(ls, "working", w); why != "" {
		t.Errorf("an unchanged file: %q", why)
	}
	w.lookedAt["practice/answer/answers.md"] = "sha256:new"
	if why := staleGrade(ls, "working", w); !strings.Contains(why, "changed") {
		t.Errorf("a changed file: %q", why)
	}
}

func TestARubricGradeNeedsWork(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", writtenLesson)
	for _, name := range []string{"check.sh", "answer.txt"} {
		if err := os.Remove(filepath.Join(m.home, "c", "practice/answer", name)); err != nil {
			t.Fatal(err)
		}
	}
	practicing(t, m)
	if _, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "working", Grade: GradeNotMet}); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "no work") {
		t.Errorf("grading an empty practice folder: err = %v", err)
	}
}

func TestARubricGradeNeedsTheCheckShown(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", rubricLesson)
	practicing(t, m)
	writeFile(t, m, "lessons/answer.md", strings.Replace(rubricLesson, "explain why", "say why", 1))
	if _, err := m.RecordRubricGrade(ctx, "c", RubricSpec{Lesson: "answer", Criterion: "explained", Grade: GradeMet}); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "changed since it was shown") {
		t.Errorf("grading a Check edited after it was shown: err = %v", err)
	}
}

// TestAFailedAttemptAsksForTheFixOnce: whatever Phases the Lesson goes
// through, practicing resumes only with a Next step that names the fix, and
// once one is recorded nothing more is asked until an Attempt fails again.
func TestAFailedAttemptAsksForTheFixOnce(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	runCheck(t, m) // fails: answer.txt holds 41
	m.setPhase(t, "answer", PhaseFeedback)
	m.setPhase(t, "answer", PhaseTeaching)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("practicing through teaching without a Next step: err = %v", err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing,
		NextStep: "Fix the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}
	m.setPhase(t, "answer", PhaseFeedback)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); err != nil {
		t.Errorf("practicing again with no new Attempt: %v", err)
	}
	runCheck(t, m) // fails again
	m.setPhase(t, "answer", PhaseFeedback)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("after a new failed Attempt: err = %v, want a Next step asked for again", err)
	}
}

func TestHeldOutDataIsCommittedAndNeverNamed(t *testing.T) {
	m := heldOutTopic(t)
	writeFile(t, m, ".heldout/answer/test.parquet", "PAR1 rows")
	writeFile(t, m, ".heldout/answer/build/cases.bin", "cases")
	writeFile(t, m, ".heldout/answer/large.csv", strings.Repeat("x", 11<<20))
	writeFile(t, m, "practice/answer/large.bin", strings.Repeat("y", 11<<20))
	writeFile(t, m, "practice/answer/coverage.out", "mode: set\n")
	cp := takeCheckpoint(t, m)
	for _, f := range cp.LargeFiles {
		if strings.HasPrefix(f.Path, ".heldout/") {
			t.Errorf("a Checkpoint named Held-out data: %+v", f)
		}
	}
	if len(cp.LargeFiles) != 1 || cp.LargeFiles[0].Path != "practice/answer/large.bin" {
		t.Errorf("large files = %+v, want only the practice file", cp.LargeFiles)
	}
	files := git(t, filepath.Join(m.home, "c"), "ls-files")
	for _, want := range []string{".heldout/answer/test.parquet", ".heldout/answer/build/cases.bin", ".heldout/answer/expected.txt"} {
		if !strings.Contains(files, want+"\n") {
			t.Errorf("%s is not committed:\n%s", want, files)
		}
	}
	if strings.Contains(files, "coverage.out") {
		t.Error("coverage.out was committed")
	}
}

func TestAReplayedAttemptIsValidated(t *testing.T) {
	m := heldOutTopic(t)
	runCheck(t, m)
	path := filepath.Join(m.home, "c", historyFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	last := strings.Replace(lines[len(lines)-1], `"outcome":"failed"`, `"outcome":"great"`, 1)
	lines[len(lines)-1] = last
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := replayFolder(t, filepath.Join(m.home, "c"))
	held := false
	for _, f := range s.flags {
		if f.Kind == FlagHeldEvent && strings.Contains(f.Message, "great") {
			held = true
		}
	}
	if !held {
		t.Errorf("an Attempt with an unknown outcome was not held: flags %+v", s.flags)
	}
}
