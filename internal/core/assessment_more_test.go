package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// levelFlag returns the flag of a Level changed on two machines, if any.
func levelFlag(t *testing.T, m *machine) *Flag {
	t.Helper()
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range topic.Flags {
		if f.Kind == FlagConflict && strings.Contains(f.Message, "the Level was changed on two machines") {
			return &f
		}
	}
	return nil
}

// levelEvents returns the Events that set the Level, in replay order.
func levelEvents(t *testing.T, m *machine) []string {
	t.Helper()
	var out []string
	for _, w := range replayFolder(t, filepath.Join(m.home, "c")).assessing().levelWrites {
		out = append(out, w.event)
	}
	return out
}

// TestALevelKeptOnAnotherMachineIsFlagged: the learner chooses a Level on
// one machine while the other, not synced, records a later Assessment
// whose Level is the one topic.toml already holds there, so it leaves the
// file as it was. After the merge the file keeps the learner's choice
// although the History's last writer is the Assessment: that is flagged,
// naming both, and never shown as a hand edit.
func TestALevelKeptOnAnotherMachineIsFlagged(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	if _, err := a.RecordAssessment(ctx, "c", AssessmentSpec{Kind: AssessmentPlacement, Level: LevelBeginner,
		Summary: "placement", Items: []AssessmentItem{{Area: "basics", Outcome: AnswerCorrect}}}); err != nil {
		t.Fatal(err)
	}
	merge(t, a, b)
	chose := a.update(t, TopicChanges{Level: ptr(LevelAdvanced)})
	b.setClock(t0.Add(48 * time.Hour))
	end, err := b.RecordAssessment(ctx, "c", AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics",
		Level: LevelBeginner, Summary: "pointers again", Items: []AssessmentItem{{Area: "pointers", Outcome: AnswerIncorrect}}})
	if err != nil {
		t.Fatal(err)
	}
	merge(t, a, b)

	for name, m := range map[string]*machine{"a": a, "b": b} {
		l := readTopicLevel(t, m)
		if l == nil || l.Level != LevelAdvanced || l.Source != LevelFromLearner || l.At.IsZero() {
			t.Errorf("%s: Level after the merge = %+v, want the learner's choice from its Event", name, l)
		}
		events := levelEvents(t, m)
		f := levelFlag(t, m)
		if f == nil {
			t.Fatalf("%s: no flag for the Level changed on two machines", name)
		}
		if f.Item != topicFile || !slices.Equal(f.Events, []string{events[len(events)-2], end.Assessment.ID}) ||
			!strings.Contains(f.Message, "the learner set it to advanced") ||
			!strings.Contains(f.Message, "an Assessment to beginner") {
			t.Errorf("%s: flag = %+v, Level Events %q", name, f, events)
		}
	}
	if chose.Topic.Level == nil || chose.Topic.Level.Level != LevelAdvanced {
		t.Fatalf("the learner's choice = %+v", chose.Topic.Level)
	}

	// Choosing the Level the file holds records it, and the flag goes.
	if up := a.update(t, TopicChanges{Level: ptr(LevelAdvanced)}); !up.Changed {
		t.Error("choosing the Level the file holds recorded nothing")
	}
	if f := levelFlag(t, a); f != nil {
		t.Errorf("the flag survived the learner's choice: %+v", f)
	}
	// Dismissing it on b keeps the file's Level.
	f := levelFlag(t, b)
	if _, err := b.DismissFlag(ctx, "c", f.ID, false); err != nil {
		t.Fatal(err)
	}
	if levelFlag(t, b) != nil {
		t.Error("the dismissed flag is still shown")
	}
	if l := readTopicLevel(t, b); l.Level != LevelAdvanced {
		t.Errorf("Level after dismissing = %+v", l)
	}
}

// TestARetakeAfterTheLearnerChoseALevelIsRecorded: the same Assessment
// recorded a month later, after the learner chose a Level, is the next
// Assessment, and sets the Level again.
func TestARetakeAfterTheLearnerChoseALevelIsRecorded(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	end := AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Level: LevelBeginner, Summary: "All fine",
		Items: []AssessmentItem{{Area: "pointers", Outcome: AnswerCorrect}}}
	first, err := m.RecordAssessment(ctx, "c", end)
	if err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(30 * 24 * time.Hour))
	m.update(t, TopicChanges{Level: ptr(LevelAdvanced)})
	m.setClock(t0.Add(60 * 24 * time.Hour))
	again, err := m.RecordAssessment(ctx, "c", end)
	if err != nil || !again.Changed || again.Assessment.ID == first.Assessment.ID {
		t.Fatalf("the retake = %+v, %v; want it recorded", again, err)
	}
	if l := readTopicLevel(t, m); l.Level != LevelBeginner || l.Source != LevelFromAssessment || l.Assessment != again.Assessment.ID {
		t.Errorf("Level after the retake = %+v", l)
	}
	// Right after, the same one is a retry.
	if retry, err := m.RecordAssessment(ctx, "c", end); err != nil || retry.Changed || retry.Assessment.ID != again.Assessment.ID {
		t.Errorf("a retry = %+v, %v", retry, err)
	}
	// A Level edited into topic.toml by hand is a change too.
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `level = "beginner"`, `level = "expert"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	afterEdit, err := m.RecordAssessment(ctx, "c", end)
	if err != nil || !afterEdit.Changed {
		t.Errorf("the same Assessment after a hand edit = %+v, %v; want it recorded", afterEdit, err)
	}
	if l := readTopicLevel(t, m); l.Level != LevelBeginner || l.Assessment != afterEdit.Assessment.ID {
		t.Errorf("Level after the retake = %+v", l)
	}
	// Without a request id, only the latest counts: an older one is new.
	other := AssessmentSpec{Kind: AssessmentPlacement, Summary: "Another", Items: end.Items}
	if _, err := m.RecordAssessment(ctx, "c", other); err != nil {
		t.Fatal(err)
	}
	if r, err := m.RecordAssessment(ctx, "c", end); err != nil || !r.Changed {
		t.Errorf("an earlier Assessment after another = %+v, %v; want it recorded", r, err)
	}
	if list, _ := m.ListAssessments(ctx, "c"); len(list.Assessments) != 5 {
		t.Errorf("%d Assessments, want 5", len(list.Assessments))
	}
}

func TestAssessmentRequestIDs(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	spec := placement
	spec.Request = "placement-1"
	first, err := m.RecordAssessment(ctx, "c", spec)
	if err != nil || !first.Changed {
		t.Fatal(first, err)
	}
	m.update(t, TopicChanges{Level: ptr(LevelExpert)})
	// A retry with the same id records nothing, even after the Level
	// changed.
	retry, err := m.RecordAssessment(ctx, "c", spec)
	if err != nil || retry.Changed || retry.Assessment.ID != first.Assessment.ID {
		t.Errorf("a retry = %+v, %v", retry, err)
	}
	if l := readTopicLevel(t, m); l.Level != LevelExpert {
		t.Errorf("the retry changed the Level: %+v", l)
	}
	changed := spec
	changed.Summary = "Something else"
	if _, err := m.RecordAssessment(ctx, "c", changed); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("the id of a different Assessment: err = %v", err)
	}
	// Another id is another Assessment, whatever it holds.
	spec.Request = "placement-2"
	if r, err := m.RecordAssessment(ctx, "c", spec); err != nil || !r.Changed {
		t.Errorf("another id = %+v, %v", r, err)
	}
	if n := countEvents(replayFolder(t, filepath.Join(m.home, "c")), eventAssessmentRecorded); n != 2 {
		t.Errorf("%d assessment.recorded Events, want 2", n)
	}
}

// TestWeakResultsProposeARevision: a milestone Assessment with weak areas
// says to propose a Revision, and nothing is blocked.
func TestWeakResultsProposeARevision(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	weak := AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Summary: "Pointers need work",
		Items: []AssessmentItem{{Area: "pointers", Outcome: AnswerIncorrect}, {Area: "slices", Outcome: AnswerPartly},
			{Area: "maps", Outcome: AnswerNotReached}}}
	r, err := m.RecordAssessment(ctx, "c", weak)
	if err != nil {
		t.Fatal(err)
	}
	if r.Next == nil || r.Next.Code != NextProposeRevision || !strings.Contains(r.Next.Text, "pointers, slices") ||
		strings.Contains(r.Next.Text, "maps") {
		t.Errorf("next after weak results = %+v", r.Next)
	}
	if retry, err := m.RecordAssessment(ctx, "c", weak); err != nil || retry.Next == nil {
		t.Errorf("a retry's next = %+v, %v", retry.Next, err)
	}
	for name, spec := range map[string]AssessmentSpec{
		"a placement Assessment": {Kind: AssessmentPlacement, Summary: "s", Items: weak.Items},
		"no weak areas": {Kind: AssessmentMilestone, Milestone: "basics", Summary: "s",
			Items: []AssessmentItem{{Area: "maps", Outcome: AnswerNotReached}}},
	} {
		if r, err := m.RecordAssessment(ctx, "c", spec); err != nil || r.Next != nil {
			t.Errorf("%s: next = %+v, %v", name, r.Next, err)
		}
	}
	// Nothing waits on the Revision.
	if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", RequestedBy: HintByLearner}); err != nil {
		t.Errorf("a hint after weak results: %v", err)
	}
	completeAnswer(t, m)
}

// TestReplayChecksAssessmentLevelAndHintEvents: Events from another machine
// or a hand edit are checked as a write would check them; one that fails
// is held and flagged.
func TestReplayChecksAssessmentLevelAndHintEvents(t *testing.T) {
	m := newTopic(t)
	good := `"kind":"placement","items":[{"area":"a","outcome":"correct"}],"summary":"s","time_box":15`
	bad := map[string]string{
		eventAssessmentRecorded + "/fields":      `{"kind":"milestone","milestone":"Not A Slug!","items":[{"area":"x\u001b[2J","outcome":"great"}],"summary":"","time_box":-5}`,
		eventAssessmentRecorded + "/no time box": `{"kind":"placement","items":[{"area":"a","outcome":"correct"}],"summary":"s"}`,
		eventAssessmentRecorded + "/minutes":     `{` + good + `,"minutes":16}`,
		eventAssessmentRecorded + "/notes path":  `{` + good + `,"notes":{"path":"../topic.toml","hash":"sha256:` + strings.Repeat("0", 64) + `"}}`,
		eventAssessmentRecorded + "/notes hash":  `{` + good + `,"notes":{"path":"notes/p.md","hash":"md5:00"}}`,
		eventAssessmentRecorded + "/level":       `{` + good + `,"level":"guru"}`,
		eventAssessmentRecorded + "/request":     `{` + good + `,"request":"has space"}`,
		eventLevelSet + "/level":                 `{"level":"guru"}`,
		eventHintRecorded + "/kind":              `{"lesson":"answer","kind":"shout","requested_by":"agent"}`,
		eventHintRecorded + "/no kind":           `{"lesson":"answer","requested_by":"agent"}`,
		eventHintRecorded + "/asker":             `{"lesson":"answer","kind":"nudge","requested_by":"teacher"}`,
		eventHintRecorded + "/no asker":          `{"lesson":"answer","kind":"nudge"}`,
		eventHintRecorded + "/lesson":            `{"lesson":"Not A Slug","kind":"nudge","requested_by":"agent"}`,
		eventHintRecorded + "/note":              `{"lesson":"answer","kind":"nudge","requested_by":"agent","note":"\u001b[2J"}`,
	}
	f, err := os.OpenFile(filepath.Join(m.home, "c", historyFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for name, data := range bad {
		i++
		typ, _, _ := strings.Cut(name, "/")
		at := t0.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		fmt.Fprintf(f, `{"format":1,"id":"zz%02d","time":"%s","wall":"%s","type":"%s","data":%s}`+"\n", i, at, at, typ, data)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s := replayFolder(t, filepath.Join(m.home, "c"))
	held := 0
	for _, fl := range s.flags {
		if fl.Kind == FlagHeldEvent {
			held++
		}
	}
	st := s.assessing()
	if held != len(bad) || len(st.assessments) != 0 || len(st.hints) != 0 || st.level != nil {
		t.Errorf("%d of %d Events held; %d Assessments, %d hints, Level %+v; flags %+v", held, len(bad),
			len(st.assessments), len(st.hints), st.level, s.flags)
	}
}

func TestParseAssessmentTakesOneObject(t *testing.T) {
	one := `{"kind":"placement","items":[{"area":"a","outcome":"correct"}],"summary":"s"}`
	if _, err := ParseAssessment([]byte(one+"\n"), "a.json"); err != nil {
		t.Errorf("one Assessment: %v", err)
	}
	for name, data := range map[string]string{
		"two objects":   one + one,
		"trailing text": one + " and more",
		"a second line": one + "\n{}",
	} {
		if _, err := ParseAssessment([]byte(data), "a.json"); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestFeedbackOnTwoMachinesIsOneRound: the Lesson went to feedback on both
// machines from the same Attempt; after the merge that is one round.
func TestFeedbackOnTwoMachinesIsOneRound(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	practicing(t, a)
	runCheck(t, a)
	merge(t, a, b)
	for _, m := range []*machine{a, b} {
		if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhaseFeedback}); err != nil {
			t.Fatal(err)
		}
	}
	merge(t, a, b)
	if n := countEvents(replayFolder(t, filepath.Join(a.home, "c")), eventPhaseSet); n < 3 {
		t.Fatalf("%d phase.set Events, want the feedback from both machines", n)
	}
	if ls := lessonSignals(t, signals(t, a), "answer"); ls.FeedbackRounds != 1 {
		t.Errorf("feedback rounds = %d, want 1", ls.FeedbackRounds)
	}
}

// TestAHintRetryAfterAMergeReturnsTheFirst: two machines recorded a hint
// with the same request id; a retry returns the first in the History.
func TestAHintRetryAfterAMergeReturnsTheFirst(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	onA, err := a.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: HintNudge, RequestedBy: HintByLearner, Request: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: HintStep, RequestedBy: HintByAgent, Request: "h1"}); err != nil {
		t.Fatal(err)
	}
	merge(t, a, b)
	for name, m := range map[string]*machine{"a": a, "b": b} {
		retry, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", RequestedBy: HintByLearner, Request: "h1"})
		if err != nil || retry.Changed || retry.Hint.ID != onA.Hint.ID || retry.Hint.Kind != HintNudge {
			t.Errorf("%s: a retry = %+v, %v; want the first hint, %s", name, retry, err, onA.Hint.ID)
		}
	}
}

// TestACheckWithoutRunCriteriaHasNoFirstTry: a rubric item and a held_out
// criterion say nothing about a first try, so the Lesson has none.
func TestACheckWithoutRunCriteriaHasNoFirstTry(t *testing.T) {
	m := learningTopic(t)
	writeFile(t, m, "lessons/answer.md", "---\ncheck:\n  - id: notes\n    rubric: The notes explain it\n"+
		"  - id: hidden\n    held_out: [sh, eval.sh]\n---\n")
	writeFile(t, m, "practice/answer/eval.sh", heldOutEval)
	writeFile(t, m, ".heldout/answer/expected.txt", "43\n")
	writeFile(t, m, "practice/answer/answer.txt", "43\n")
	practicing(t, m)
	runCheck(t, m)
	sig := signals(t, m)
	ls := lessonSignals(t, sig, "answer")
	if ls.FirstTry != nil || ls.Attempts != 0 || sig.Totals.FirstTryMeasured != 0 {
		t.Errorf("Lesson signals = %+v, totals %+v; want no first try", ls, sig.Totals)
	}
	if len(ls.HeldOut) != 1 || ls.HeldOut[0].HeldOut != 1 || ls.HeldOut[0].Dev != nil {
		t.Errorf("held_out = %+v", ls.HeldOut)
	}
}

// TestExploreReviewsAreNotALessons: an Explore Card's Reviews count for the
// Topic, never for a Lesson.
func TestExploreReviewsAreNotALessons(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	explore := m.addCard(t, CardSpec{Prompt: "Which header declares malloc?", Answer: "stdlib.h"})
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: explore.ID, Draft: DraftKeep, Rating: RatingHard}); err != nil {
		t.Fatal(err)
	}
	sig := signals(t, m)
	if sig.Reviews.Hard != 1 || len(sig.Lessons) != 0 {
		t.Errorf("signals = %+v; want the Review for the Topic only", sig)
	}
}

// TestHintsAskedForAndOffered: signals tell hints the learner asked for
// from those the agent offered.
func TestHintsAskedForAndOffered(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	for _, by := range []string{HintByLearner, HintByLearner, HintByAgent} {
		if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", RequestedBy: by}); err != nil {
			t.Fatal(err)
		}
	}
	sig := signals(t, m)
	ls := lessonSignals(t, sig, "answer")
	if ls.HintsRequested != 2 || ls.HintsOffered != 1 || ls.Hints[HintNudge] != 3 ||
		sig.Totals.Hints != 3 || sig.Totals.HintsRequested != 2 {
		t.Errorf("Lesson signals = %+v, totals %+v", ls, sig.Totals)
	}
}

func TestApproach(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	if topic, err := m.readTopic("c"); err != nil || topic.Approach != "" {
		t.Fatalf("a new Topic's Approach = %q, %v", topic.Approach, err)
	}
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keys study doesn't know survive.
	if err := os.WriteFile(path, append(data, []byte("mine = \"kept\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	dry, err := m.UpdateTopic(ctx, "c", TopicChanges{Approach: ptr(ApproachProject), DryRun: true})
	if err != nil || !dry.Changed || dry.Topic.Approach != ApproachProject {
		t.Errorf("a dry run = %+v, %v", dry, err)
	}
	if topic, _ := m.readTopic("c"); topic.Approach != "" {
		t.Errorf("the dry run set the Approach: %q", topic.Approach)
	}
	if up := m.update(t, TopicChanges{Approach: ptr(ApproachProject)}); !up.Changed || up.Topic.Approach != ApproachProject {
		t.Errorf("setting the Approach = %+v", up)
	}
	settings := readSettings(t, filepath.Join(m.home, "c"))
	if settings.extra["approach"] != ApproachProject || settings.extra["mine"] != "kept" {
		t.Errorf("topic.toml = %+v", settings.extra)
	}
	if n := countEvents(replayFolder(t, filepath.Join(m.home, "c")), eventApproachSet); n != 1 {
		t.Errorf("%d approach.set Events, want 1", n)
	}
	if up := m.update(t, TopicChanges{Approach: ptr(ApproachProject)}); up.Changed {
		t.Error("setting the same Approach again recorded a change")
	}
	if up := m.update(t, TopicChanges{Approach: ptr(ApproachChallenges)}); up.Topic.Approach != ApproachChallenges {
		t.Errorf("changing the Approach = %+v", up)
	}
	for _, bad := range []string{"", "lectures", "Project"} {
		if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Approach: ptr(bad)}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("Approach %q: err = %v", bad, err)
		}
	}

	// A hand edit that breaks it is a problem status shows; setting it
	// fixes the file.
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), `approach = "challenges"`, `approach = 3`, 1)
	if broken == string(data) {
		t.Fatalf("topic.toml has no approach line:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || topic.Approach != "" || !mentions(topic.SettingsProblems, "approach in topic.toml") {
		t.Errorf("a broken Approach = %q, problems %q, %v", topic.Approach, topic.SettingsProblems, err)
	}
	if up := m.update(t, TopicChanges{Approach: ptr(ApproachConcepts)}); !up.Changed || up.Topic.Approach != ApproachConcepts ||
		len(up.Topic.SettingsProblems) != 0 {
		t.Errorf("setting the Approach over a broken one = %+v", up)
	}
}
