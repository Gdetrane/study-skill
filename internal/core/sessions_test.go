package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// breakPointLesson is answerLesson with two Break points.
const breakPointLesson = `---
check:
  - id: answer
    describe: answer.txt holds the answer
    run: [sh, check.sh]
break_points:
  - id: read
    describe: The question is understood
  - id: draft
    describe: A first answer is written
---
# The answer

Write the answer in answer.txt.
`

func TestNextStepsStartWithAVerb(t *testing.T) {
	for _, step := range []string{
		"Fix the off-by-one in parse.go",
		"Write answer.txt",
		"Correggi il parser",     // not English: passes on the language-neutral rules
		"Read section 2.3 again", // digits after the verb are fine
		"Re-run the tests",
		"Next, rerun the Check",
		"Lesson 3: write the swap function",
		// Opening punctuation and symbols are skipped.
		"- fix the parser",
		"`go test ./...` again, then fix the failure",
		"\"Fix\" the parser",
		"**Fix** the parser",
		"¿Puedes revisar el ejercicio 3?",
		"«Rileggi» il capitolo 2",
		// Scripts without spaces between words need four characters.
		"复习第三课的指针练习",
		"ポインタの練習問題を解く",
		"ทบทวนบทเรียนที่สาม",
		"Überarbeite den Parser",
		"Перепиши парсер",
		"مراجعة الدرس الثالث",
	} {
		if got, err := checkNextStep("  " + step + " "); err != nil || got != step {
			t.Errorf("checkNextStep(%q) = %q, %v; want it accepted", step, got, err)
		}
	}
	for _, step := range []string{
		"",
		"Continue",
		"Continue.",
		"1. Fix the parser",
		"The parser is half done",
		"I was on lesson 3",
		"Done with the parser",
		"Maybe fix the parser",
		// The first word is its leading run of letters, so contractions
		// count by their first part.
		"I'm on lesson 3",
		"I’m on lesson 3",
		"It's half done",
		"We're at exercise 2",
		"There's one more exercise",
		"All done for today",
		"Stuck on recursion",
		"复习", // too short to say what to act on
		// Invisible formatting and bidirectional controls are refused.
		"Continue ​",
		"Fix​ the parser",
		"Fix\u202e the parser",
	} {
		if _, err := checkNextStep(step); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("checkNextStep(%q): err = %v, want invalid_argument", step, err)
		}
	}

	// Every operation that records a Next step checks it.
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Continue"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("session_close: %v", err)
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhaseTeaching, NextStep: "The question"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("phase_set: %v", err)
	}
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Done"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("break_point_reached: %v", err)
	}
}

func TestBreakPoints(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)

	reached, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{
		Lesson: "answer", BreakPoint: "draft", NextStep: "Check the draft against the question", Context: "41 is close",
	})
	if err != nil {
		t.Fatalf("ReachBreakPoint: %v", err)
	}
	if !reached.Changed || reached.BreakPoint != (BreakPoint{ID: "draft", Describe: "A first answer is written"}) ||
		reached.NextStep.Step != "Check the draft against the question" || reached.NextStep.Lesson != "answer" {
		t.Errorf("result = %+v", reached)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	r := topic.Resume
	if r == nil || r.Lesson != "answer" || r.BreakPoint == nil || *r.BreakPoint != reached.BreakPoint ||
		r.NextStep == nil || r.NextStep.Step != "Check the draft against the question" || r.NextStep.Context != "41 is close" {
		t.Errorf("resume = %+v", r)
	}
	if again, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{
		Lesson: "answer", BreakPoint: "draft", NextStep: "Check the draft against the question", Context: "41 is close",
	}); err != nil || again.Changed {
		t.Errorf("the same Break point again = %+v, %v; want nothing recorded", again, err)
	}

	for _, tc := range []struct {
		name string
		spec BreakPointSpec
		code ErrorCode
		text string
	}{
		{"unknown Break point", BreakPointSpec{Lesson: "answer", BreakPoint: "done"}, CodeNotFound, "it declares read, draft"},
		{"invalid id", BreakPointSpec{Lesson: "answer", BreakPoint: "../x"}, CodeInvalidArgument, ""},
		{"unknown Lesson", BreakPointSpec{Lesson: "nope", BreakPoint: "read"}, CodeNotFound, ""},
	} {
		tc.spec.NextStep = "Write the answer"
		if _, err := m.ReachBreakPoint(ctx, "c", tc.spec); CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("%s: err = %v, want %s mentioning %q", tc.name, err, tc.code, tc.text)
		}
	}
	writeFile(t, m, "lessons/answer.md", answerLesson)
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Read it"}); CodeOf(err) != CodeNotFound ||
		!strings.Contains(err.Error(), "declares no Break points") {
		t.Errorf("a Lesson without Break points: %v", err)
	}
}

// A mistake in the Break points never makes the Check, which gates
// completion, unreadable.
func TestBadBreakPointsLeaveTheCheckReadable(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "---\n# The answer",
		"break_points: not a list\n---\n# The answer", 1))
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); err != nil {
		t.Fatalf("practicing with damaged Break points: %v", err)
	}
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Read it"}); CodeOf(err) != CodeCorrupt {
		t.Errorf("reaching a Break point: %v, want corrupt", err)
	}
	for _, header := range []string{
		"break_points:\n  - id: Bad Id\n",
		"break_points:\n  - id: a\n  - id: a\n",
		"break_points:\n  - id: a\n    describe: \"\\e[2J\"\n",
	} {
		if _, err := parseBreakPoints([]byte("---\n" + header + "---\n")); CodeOf(err) != CodeCorrupt {
			t.Errorf("%q: err = %v, want corrupt", header, err)
		}
	}
}

// A Break point reached on a done Lesson, or a skipped one, is refused.
func TestBreakPointsOnlyForLessonsBeingStudied(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	completeAnswer(t, m)
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Read it"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a done Lesson: %v", err)
	}
}

func TestCrashesWhileReachingABreakPoint(t *testing.T) {
	ctx := context.Background()
	spec := BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write a first answer"}
	for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
		t.Run(point, func(t *testing.T) {
			m := learningTopic(t)
			writeFile(t, m, "lessons/answer.md", breakPointLesson)
			m.crash = crashOnce(point)
			_, err := m.ReachBreakPoint(ctx, "c", spec)
			m.crash = nil
			if err != nil && !errors.Is(err, errCrash) {
				t.Fatalf("the write failed: %v", err)
			}
			dry := spec
			dry.DryRun = true
			dryRes, dryErr := m.ReachBreakPoint(ctx, "c", dry)
			realRes, realErr := m.ReachBreakPoint(ctx, "c", spec)
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
			if n := countEvents(s, eventBreakPointReached); n != 1 {
				t.Errorf("%d break_point.reached Events, want 1", n)
			}
			if r := s.study.resume(); r.BreakPoint == nil || r.BreakPoint.ID != "read" {
				t.Errorf("resume = %+v", r)
			}
		})
	}
}

func countEvents(s *replayed, eventType string) int {
	n := 0
	for _, ev := range s.applied {
		if ev.Type == eventType {
			n++
		}
	}
	return n
}

// gitTree lists the files under .git with their sizes and modification
// times, to show something wrote nothing there.
func gitTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, ".git"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%s %d %s", info.Mode(), info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAnUnclosedSessionShowsWhatChangedSinceTheLastCheckpoint(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	first, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull})
	if err != nil {
		t.Fatal(err)
	}
	// Practicing takes the agent's Checkpoint; then the learner works and
	// closes the terminal without a Next step.
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhasePracticing}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	writeFile(t, m, "practice/answer/notes.md", "tried 41 first\n")
	writeFile(t, m, ".heldout/answer/expected.txt", "42\n") // kept out of the learner's sight
	if err := os.Remove(filepath.Join(m.home, "c", "practice", "answer", "check.sh")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.home, "c")
	before := gitTree(t, dir)

	// Lamplight's own state files change too, and are left out.
	added := m.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	if added.ID == "" {
		t.Fatal("no Card")
	}
	m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}})

	second, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyHalf})
	if err != nil {
		t.Fatal(err)
	}
	ch := second.Changes
	if len(second.Unclosed) != 1 || second.Unclosed[0].ID != first.Session || ch == nil || ch.Since == "" || ch.Error != "" {
		t.Fatalf("unclosed = %+v, changes = %+v", second.Unclosed, ch)
	}
	want := []FileChange{
		{Path: "practice/answer/answer.txt", Change: "modified"},
		{Path: "practice/answer/check.sh", Change: "deleted"},
		{Path: "practice/answer/notes.md", Change: "added"},
	}
	if !equalChanges(ch.Files, want) {
		t.Errorf("changes = %+v, want %+v (the History, cards.jsonl and tasks.jsonl left out)", ch.Files, want)
	}
	if after := gitTree(t, dir); !maps.Equal(before, after) {
		t.Error("listing the changes wrote to .git")
	}
	// The learner's note goes to the Session that missed it.
	if _, err := m.CloseSession(ctx, "c", CloseSpec{Session: first.Session, NextStep: "Run the Check on 42"}); err != nil {
		t.Errorf("closing the unclosed Session: %v", err)
	}
	if third, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil || len(third.Unclosed) != 1 || third.Unclosed[0].ID != second.Session {
		t.Errorf("the next Session = %+v, %v; want the second one reported", third.Unclosed, err)
	}
}

// At most 50 changed files are listed; More counts the rest.
func TestTheChangesListedAreCapped(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, m)
	for i := range maxChangesShown + 7 {
		writeFile(t, m, fmt.Sprintf("practice/answer/f%02d.txt", i), "x\n")
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if ch := opened.Changes; ch == nil || len(ch.Files) != maxChangesShown || ch.More != 7 ||
		ch.Files[0].Path != "practice/answer/f00.txt" {
		t.Errorf("changes = %d files, more %d", len(ch.Files), ch.More)
	}
}

// Before the first Checkpoint, every file counts as added, and since is
// absent.
func TestChangesBeforeTheFirstCheckpoint(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if ch := opened.Changes; ch == nil || ch.Since != "" || len(ch.Files) == 0 || ch.Files[0].Change != "added" {
		t.Errorf("changes = %+v", ch)
	}
}

func equalChanges(a, b []FileChange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A Topic whose git repository is mid-merge still opens a Session; the
// changes are not listed, and the result says why.
func TestAnUnclosedSessionOpensEvenWhenChangesCannotBeListed(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if _, err := m.OpenSession(ctx, "c", SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, m)
	head := strings.TrimSpace(git(t, filepath.Join(m.home, "c"), "rev-parse", "HEAD"))
	writeFile(t, m, ".git/MERGE_HEAD", head+"\n")
	opened, err := m.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatalf("OpenSession during a merge: %v", err)
	}
	if ch := opened.Changes; len(opened.Unclosed) != 1 || ch == nil || !strings.Contains(ch.Error, "merge") || len(ch.Files) != 0 {
		t.Errorf("unclosed = %+v, changes = %+v", opened.Unclosed, ch)
	}
}

func TestSessionsSuggestAFocusAndNoticeALongGap(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull})
	if err != nil {
		t.Fatal(err)
	}
	if s := opened.Suggested; s == nil || s.Suggest != FocusLearn || opened.LongGap {
		t.Errorf("first Session: suggested %+v, long gap %v", s, opened.LongGap)
	}
	if opened, err = m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull, Focus: FocusExplore}); err != nil || opened.Suggested != nil {
		t.Errorf("a chosen Focus still got a suggestion: %+v, %v", opened.Suggested, err)
	}
	m.setClock(t0.Add(8 * 24 * time.Hour))
	if opened, err = m.OpenSession(ctx, "c", SessionSpec{}); err != nil || !opened.LongGap || opened.Suggested != nil {
		t.Errorf("after eight days: long gap %v, suggested %+v, %v", opened.LongGap, opened.Suggested, err)
	}
	m.setClock(t0.Add(9 * 24 * time.Hour))
	if opened, err = m.OpenSession(ctx, "c", SessionSpec{}); err != nil || opened.LongGap {
		t.Errorf("a day later: long gap %v, %v", opened.LongGap, err)
	}
	// The gap runs from the last activity, not from when the last Session
	// opened: a long Session closed an hour ago is no gap.
	m.setClock(t0.Add(16 * 24 * time.Hour))
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write answer.txt"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(16*24*time.Hour + time.Hour))
	if opened, err = m.OpenSession(ctx, "c", SessionSpec{}); err != nil || opened.LongGap {
		t.Errorf("an hour after closing a week-long Session: long gap %v, %v", opened.LongGap, err)
	}
}

func TestSuggestFocus(t *testing.T) {
	learning := ResumePoint{Lesson: "answer", LessonTitle: "The answer"}
	practicing := ResumePoint{Lesson: "answer", LessonTitle: "The answer", Phase: PhasePracticing}
	done := ResumePoint{SyllabusDone: true}
	noSyllabus := ResumePoint{}
	ready, none := &CardsReady{Ready: true}, (*CardsReady)(nil)
	for _, tc := range []struct {
		energy string
		resume ResumePoint
		cards  *CardsReady
		want   string
	}{
		{EnergyFull, learning, ready, FocusLearn},
		{EnergyFull, practicing, ready, FocusPractice},
		{EnergyFull, done, ready, FocusReviews},
		{EnergyFull, done, none, FocusExplore},
		{EnergyFull, noSyllabus, ready, SuggestPlan},
		{EnergyFull, noSyllabus, none, SuggestPlan},
		{EnergyHalf, practicing, none, FocusPractice},
		{EnergyHalf, learning, ready, FocusReviews},
		{EnergyHalf, learning, none, FocusLearn},
		{EnergyHalf, done, none, FocusExplore},
		{EnergyHalf, noSyllabus, ready, FocusReviews},
		{EnergyHalf, noSyllabus, none, SuggestPlan},
		{EnergyFumes, practicing, ready, FocusReviews},
		{EnergyFumes, practicing, none, SuggestStop},
		{EnergyFumes, noSyllabus, ready, FocusReviews},
		{EnergyFumes, noSyllabus, none, SuggestStop},
	} {
		got := suggestFocus(tc.energy, TopicActive, tc.resume, tc.cards, nil)
		if got == nil || got.Suggest != tc.want || got.Reason == "" {
			t.Errorf("suggestFocus(%s, %+v, %+v) = %+v, want %q", tc.energy, tc.resume, tc.cards, got, tc.want)
		}
	}
	if got := suggestFocus("", TopicActive, learning, ready, nil); got != nil {
		t.Errorf("no Energy: %+v, want no suggestion", got)
	}
	// A paused Topic is never proposed for study, whatever the Energy; a
	// finished one only for its Reviews.
	for _, energy := range []string{EnergyFull, EnergyHalf, EnergyFumes} {
		for _, resume := range []ResumePoint{learning, practicing, done, noSyllabus} {
			if got := suggestFocus(energy, TopicPaused, resume, none, nil); got == nil || got.Suggest != SuggestResumeTopic {
				t.Errorf("paused, %s, %+v: %+v, want %q", energy, resume, got, SuggestResumeTopic)
			}
			if got := suggestFocus(energy, TopicFinished, resume, ready, nil); got == nil || got.Suggest != FocusReviews {
				t.Errorf("finished with Cards ready, %s, %+v: %+v, want %q", energy, resume, got, FocusReviews)
			}
			if got := suggestFocus(energy, TopicFinished, resume, none, nil); got == nil || got.Suggest != SuggestStop {
				t.Errorf("finished with no Card ready, %s, %+v: %+v, want %q", energy, resume, got, SuggestStop)
			}
		}
	}
	if got := suggestFocus("", TopicPaused, learning, none, nil); got != nil {
		t.Errorf("paused with no Energy: %+v, want no suggestion", got)
	}
}

func TestCardsReadyNeverCounts(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	st := replayFolder(t, filepath.Join(m.home, "c")).study
	if got := st.cardsReadyUnder(t0, NewCardsPerDay); got != nil {
		t.Errorf("a Topic without Cards: %+v", got)
	}
	card := m.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	st = replayFolder(t, filepath.Join(m.home, "c")).study
	if got := st.cardsReadyUnder(t0, NewCardsPerDay); got == nil || !got.Ready {
		t.Errorf("a draft within today's cap: %+v", got)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: card.ID, Rating: RatingGood, Draft: DraftKeep}); err != nil {
		t.Fatal(err)
	}
	st = replayFolder(t, filepath.Join(m.home, "c")).study
	got := st.cardsReadyUnder(t0, NewCardsPerDay)
	if got == nil || got.Ready || got.NextDue == nil || !got.NextDue.After(t0) {
		t.Errorf("after its first Review: %+v", got)
	}
	if later := st.cardsReadyUnder(got.NextDue.Add(time.Second), NewCardsPerDay); later == nil || !later.Ready {
		t.Errorf("once due: %+v", later)
	}
	if _, err := m.SuspendCard(ctx, "c", card.ID, true, false); err != nil {
		t.Fatal(err)
	}
	st = replayFolder(t, filepath.Join(m.home, "c")).study
	if got := st.cardsReadyUnder(got.NextDue.Add(time.Second), NewCardsPerDay); got != nil {
		t.Errorf("only a suspended Card: %+v", got)
	}
}

func TestStatusRecommendsOneAction(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Topic != "c" || r.Action != ActionPlan {
		t.Errorf("a Topic without a Syllabus: %+v", r)
	}
	if status.LearnerProfile != "" || status.Topics[0].LearnerAdditions != "" {
		t.Errorf("Learner profile paths without the files: %q, %q", status.LearnerProfile, status.Topics[0].LearnerAdditions)
	}

	gitIdentity(t)
	withSyllabus(t, m)
	writeFile(t, m, "lessons/answer.md", breakPointLesson)
	writeFile(t, m, "practice/answer/check.sh", answerCheck)
	writeFile(t, m, "learner.md", "Explain with examples first.\n")
	if err := os.WriteFile(filepath.Join(m.home, "learner.md"), []byte("Short sessions.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err = m.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionLearn || r.Text != "Start Lesson \u201cThe answer\u201d" {
		t.Errorf("a Lesson not started: %+v", r)
	}
	if status.LearnerProfile != filepath.Join(m.home, "learner.md") ||
		status.Topics[0].LearnerAdditions != filepath.Join(m.home, "c", "learner.md") {
		t.Errorf("Learner profile paths: %q, %q", status.LearnerProfile, status.Topics[0].LearnerAdditions)
	}

	practicing(t, m)
	if status, _ = m.Status(ctx); status.Recommended.Action != ActionPractice {
		t.Errorf("practicing: %+v", status.Recommended)
	}
	if _, err := m.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	if status, _ = m.Status(ctx); status.Recommended.Action != ActionNextStep || status.Recommended.Text != "Write a first answer" {
		t.Errorf("with a Next step: %+v", status.Recommended)
	}

	for _, tc := range []struct {
		resume *ResumePoint
		cards  *CardsReady
		want   string
	}{
		{&ResumePoint{SyllabusDone: true}, &CardsReady{Ready: true}, ActionReviews},
		{&ResumePoint{SyllabusDone: true}, nil, ActionExplore},
		{&ResumePoint{Lesson: "a", LessonTitle: "A", Phase: PhaseFeedback}, nil, ActionFeedback},
		{&ResumePoint{Lesson: "a", LessonTitle: "A", Phase: PhaseTeaching}, nil, ActionLearn},
	} {
		if got := recommend(Topic{ID: "c", Resume: tc.resume, Cards: tc.cards}); got.Action != tc.want || got.Text == "" {
			t.Errorf("recommend(%+v, %+v) = %+v, want %s", tc.resume, tc.cards, got, tc.want)
		}
	}
}

// A Session left open on one machine is reported on the other once the
// Topic is synced, and the other machine can give it its note.
func TestAnUnclosedSessionTravelsBetweenMachines(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	left, err := a.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, a, "lessons/answer.md", breakPointLesson)
	if _, err := a.ReachBreakPoint(ctx, "c", BreakPointSpec{Lesson: "answer", BreakPoint: "read", NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	pull(t, b, a)

	opened, err := b.OpenSession(ctx, "c", SessionSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Unclosed) != 1 || opened.Unclosed[0].ID != left.Session ||
		opened.Resume.BreakPoint == nil || opened.Resume.BreakPoint.Describe != "The question is understood" ||
		opened.Resume.NextStep == nil || opened.Resume.NextStep.Step != "Write a first answer" {
		t.Fatalf("on b: unclosed %+v, resume %+v", opened.Unclosed, opened.Resume)
	}
	if _, err := b.CloseSession(ctx, "c", CloseSpec{Session: left.Session, NextStep: "Write a first answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CloseSession(ctx, "c", CloseSpec{NextStep: "Compare the answer with the question"}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, b)
	pull(t, a, b)
	topic, err := a.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if r := topic.Resume; r == nil || r.OpenSession != nil || r.NextStep == nil || r.NextStep.Step != "Compare the answer with the question" {
		t.Errorf("back on a: resume %+v", r)
	}
	if len(topic.Flags) != 0 {
		t.Errorf("flags = %+v", topic.Flags)
	}
}
