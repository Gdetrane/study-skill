package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// placement is a placement Assessment that sets the Level.
var placement = AssessmentSpec{
	Kind: AssessmentPlacement,
	Items: []AssessmentItem{
		{Area: "variables", Question: "What does x := 1 do?", Outcome: AnswerCorrect},
		{Area: "pointers", Question: "What is *p?", Outcome: AnswerPartly, Note: "confused * and &"},
		{Area: "pointers", Question: "When is p nil?", Outcome: AnswerIncorrect},
		{Area: "goroutines", Outcome: AnswerNotReached},
	},
	Summary: "Knows the basics; pointers need work",
	Minutes: 14,
	Level:   LevelIntermediate,
}

func readTopicLevel(t *testing.T, m *machine) *LevelInfo {
	t.Helper()
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	return topic.Level
}

func TestAssessmentsSetTheLevelAndTheLearnerCanOverrideIt(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	if l := readTopicLevel(t, m); l != nil {
		t.Fatalf("a new Topic's Level = %+v, want none", l)
	}

	res, err := m.RecordAssessment(ctx, "c", placement)
	if err != nil {
		t.Fatal(err)
	}
	a := res.Assessment
	if !res.Changed || a.ID == "" || a.TimeBox != defaultTimeBoxMinutes || a.Minutes != 14 {
		t.Fatalf("recorded = %+v", res)
	}
	if !slices.Equal(a.Weak, []string{"pointers"}) || !slices.Equal(a.Confirm, []string{"goroutines"}) {
		t.Errorf("weak = %q, confirm = %q", a.Weak, a.Confirm)
	}
	if l := readTopicLevel(t, m); l == nil || l.Level != LevelIntermediate || l.Source != LevelFromAssessment || l.Assessment != a.ID {
		t.Errorf("Level after the placement Assessment = %+v", l)
	}
	if settings := readSettings(t, filepath.Join(m.home, "c")); settings.extra["level"] != LevelIntermediate {
		t.Errorf("topic.toml level = %v", settings.extra["level"])
	}

	again, err := m.RecordAssessment(ctx, "c", placement)
	if err != nil || again.Changed || again.Assessment.ID != a.ID {
		t.Errorf("recording the same Assessment again = %+v, %v; want nothing recorded", again, err)
	}
	if n := countEvents(replayFolder(t, filepath.Join(m.home, "c")), eventAssessmentRecorded); n != 1 {
		t.Errorf("%d assessment.recorded Events, want 1", n)
	}

	// The learner's choice holds until the next Assessment.
	if up := m.update(t, TopicChanges{Level: ptr(LevelAdvanced)}); !up.Changed || up.Topic.Level.Source != LevelFromLearner {
		t.Fatalf("setting the Level = %+v", up)
	}
	if up := m.update(t, TopicChanges{Level: ptr(LevelAdvanced)}); up.Changed {
		t.Error("setting the same Level again recorded a change")
	}
	if l := readTopicLevel(t, m); l.Level != LevelAdvanced || l.Source != LevelFromLearner {
		t.Errorf("Level after the learner chose = %+v", l)
	}

	end := AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Level: LevelBeginner, Summary: "Review pointers",
		Items: []AssessmentItem{{Area: "pointers", Outcome: AnswerIncorrect}}}
	if _, err := m.RecordAssessment(ctx, "c", end); err != nil {
		t.Fatal(err)
	}
	if l := readTopicLevel(t, m); l.Level != LevelBeginner || l.Source != LevelFromAssessment {
		t.Errorf("Level after the Milestone's Assessment = %+v", l)
	}
	list, err := m.ListAssessments(ctx, "c")
	if err != nil || len(list.Assessments) != 2 || list.Assessments[1].Milestone != "basics" {
		t.Errorf("ListAssessments = %+v, %v", list, err)
	}

	// An Assessment without a Level leaves the Level as it is.
	noLevel := AssessmentSpec{Kind: AssessmentPlacement, Summary: "Checked again",
		Items: []AssessmentItem{{Area: "variables", Outcome: AnswerCorrect}}}
	if _, err := m.RecordAssessment(ctx, "c", noLevel); err != nil {
		t.Fatal(err)
	}
	if l := readTopicLevel(t, m); l.Level != LevelBeginner || l.Source != LevelFromAssessment {
		t.Errorf("Level after an Assessment without one = %+v", l)
	}
}

func TestAssessmentValidation(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	item := []AssessmentItem{{Area: "pointers", Outcome: AnswerCorrect}}
	for name, tc := range map[string]struct {
		spec AssessmentSpec
		want ErrorCode
	}{
		"no kind":                    {AssessmentSpec{Items: item, Summary: "s"}, CodeInvalidArgument},
		"placement with a Milestone": {AssessmentSpec{Kind: AssessmentPlacement, Milestone: "basics", Items: item, Summary: "s"}, CodeInvalidArgument},
		"milestone without one":      {AssessmentSpec{Kind: AssessmentMilestone, Items: item, Summary: "s"}, CodeInvalidArgument},
		"milestone, no Syllabus":     {AssessmentSpec{Kind: AssessmentMilestone, Milestone: "basics", Items: item, Summary: "s"}, CodeFailedPrecondition},
		"no items":                   {AssessmentSpec{Kind: AssessmentPlacement, Summary: "s"}, CodeInvalidArgument},
		"no area":                    {AssessmentSpec{Kind: AssessmentPlacement, Items: []AssessmentItem{{Outcome: AnswerCorrect}}, Summary: "s"}, CodeInvalidArgument},
		"an unknown outcome":         {AssessmentSpec{Kind: AssessmentPlacement, Items: []AssessmentItem{{Area: "a", Outcome: "great"}}, Summary: "s"}, CodeInvalidArgument},
		"no summary":                 {AssessmentSpec{Kind: AssessmentPlacement, Items: item}, CodeInvalidArgument},
		"an unknown Level":           {AssessmentSpec{Kind: AssessmentPlacement, Items: item, Summary: "s", Level: "guru"}, CodeInvalidArgument},
		"too many minutes":           {AssessmentSpec{Kind: AssessmentPlacement, Items: item, Summary: "s", Minutes: 999}, CodeInvalidArgument},
		"a control character":        {AssessmentSpec{Kind: AssessmentPlacement, Items: []AssessmentItem{{Area: "a\x1b[2J", Outcome: AnswerCorrect}}, Summary: "s"}, CodeInvalidArgument},
		"notes outside notes/":       {AssessmentSpec{Kind: AssessmentPlacement, Items: item, Summary: "s", Notes: "topic.toml"}, CodeInvalidArgument},
		"notes not saved yet":        {AssessmentSpec{Kind: AssessmentPlacement, Items: item, Summary: "s", Notes: "notes/placement.md"}, CodeNotFound},
	} {
		if _, err := m.RecordAssessment(ctx, "c", tc.spec); CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}
	many := AssessmentSpec{Kind: AssessmentPlacement, Summary: "s"}
	for range maxAssessmentItems + 1 {
		many.Items = append(many.Items, item[0])
	}
	if _, err := m.RecordAssessment(ctx, "c", many); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("too many items: err = %v", err)
	}

	learn := learningTopic(t)
	if _, err := learn.RecordAssessment(ctx, "c", AssessmentSpec{Kind: AssessmentMilestone, Milestone: "later",
		Items: item, Summary: "s"}); CodeOf(err) != CodeNotFound {
		t.Errorf("an unknown Milestone: err = %v", err)
	}
}

func TestAssessmentNotes(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	writeFile(t, m, "notes/placement.md", "# Placement\n")
	if err := os.Symlink(filepath.Join(m.home, "c", "topic.toml"), filepath.Join(m.home, "c", "notes", "link.md")); err != nil {
		t.Fatal(err)
	}
	spec := AssessmentSpec{Kind: AssessmentPlacement, Summary: "s", Items: []AssessmentItem{{Area: "a", Outcome: AnswerCorrect}}}

	spec.Notes = "notes/link.md"
	if _, err := m.RecordAssessment(ctx, "c", spec); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("notes that are a link: err = %v", err)
	}
	spec.Notes = "notes/../topic.toml"
	if _, err := m.RecordAssessment(ctx, "c", spec); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("notes leading out of notes/: err = %v", err)
	}
	spec.Notes = "notes/placement.md"
	res, err := m.RecordAssessment(ctx, "c", spec)
	if err != nil {
		t.Fatal(err)
	}
	if n := res.Assessment.Notes; n == nil || n.Path != "notes/placement.md" || !strings.HasPrefix(n.Hash, "sha256:") {
		t.Errorf("notes = %+v", n)
	}
	history, err := os.ReadFile(filepath.Join(m.home, "c", historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(history), "# Placement") {
		t.Error("the notes' content reached the History")
	}
}

func TestALevelEditedByHandIsTheLearners(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	if _, err := m.RecordAssessment(ctx, "c", placement); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), `level = "intermediate"`, `level = "expert"`, 1)
	if edited == string(data) {
		t.Fatalf("topic.toml has no level line:\n%s", data)
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := readTopicLevel(t, m); l == nil || l.Level != LevelExpert || l.Source != LevelFromLearner {
		t.Errorf("a hand-edited Level = %+v, want expert from the learner", l)
	}

	broken := strings.Replace(edited, `level = "expert"`, `level = "guru"`, 1)
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	if topic.Level != nil || !mentions(topic.SettingsProblems, "level in topic.toml") {
		t.Errorf("a broken Level = %+v, problems %q", topic.Level, topic.SettingsProblems)
	}
	// Setting the Level fixes it, even to the one it reads as.
	if up := m.update(t, TopicChanges{Level: ptr(LevelAdvanced)}); !up.Changed || up.Topic.Level == nil ||
		len(up.Topic.SettingsProblems) != 0 {
		t.Errorf("setting the Level over a broken one = %+v", up)
	}
}

func TestLevelUpdateValidationAndDryRun(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Level: ptr("")}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an empty Level: err = %v", err)
	}
	dry, err := m.UpdateTopic(ctx, "c", TopicChanges{Level: ptr(LevelExpert), DryRun: true})
	if err != nil || !dry.Changed || dry.Topic.Level == nil || dry.Topic.Level.Level != LevelExpert ||
		dry.Topic.Level.Source != LevelFromLearner {
		t.Errorf("a dry run = %+v, %v", dry, err)
	}
	if l := readTopicLevel(t, m); l != nil {
		t.Errorf("the dry run set the Level: %+v", l)
	}
	dryA, err := m.RecordAssessment(ctx, "c", AssessmentSpec{Kind: placement.Kind, Items: placement.Items,
		Summary: placement.Summary, Level: placement.Level, DryRun: true})
	if err != nil || !dryA.Changed || dryA.Assessment.ID != "" {
		t.Errorf("a dry-run Assessment = %+v, %v", dryA, err)
	}
	if s := replayFolder(t, filepath.Join(m.home, "c")); countEvents(s, eventAssessmentRecorded) != 0 {
		t.Error("the dry-run Assessment was recorded")
	}
}

func TestHints(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	h, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Note: "Look at what check.sh compares"})
	if err != nil || !h.Changed || h.Hint.Kind != HintNudge || h.Hint.ID == "" {
		t.Fatalf("a hint = %+v, %v", h, err)
	}
	if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: HintStep}); err != nil {
		t.Fatal(err)
	}
	// A retry with the same request id records nothing.
	first, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: HintExplanation, Request: "r1"})
	if err != nil || !first.Changed {
		t.Fatal(first, err)
	}
	retry, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Kind: HintExplanation, Request: "r1"})
	if err != nil || retry.Changed || retry.Hint.ID != first.Hint.ID {
		t.Errorf("a retry = %+v, %v", retry, err)
	}
	if n := countEvents(replayFolder(t, filepath.Join(m.home, "c")), eventHintRecorded); n != 3 {
		t.Errorf("%d hint.recorded Events, want 3", n)
	}
	for name, tc := range map[string]struct {
		spec HintSpec
		want ErrorCode
	}{
		"an unknown kind":   {HintSpec{Lesson: "answer", Kind: "answer"}, CodeInvalidArgument},
		"an unknown Lesson": {HintSpec{Lesson: "later"}, CodeNotFound},
		"a bad request id":  {HintSpec{Lesson: "answer", Request: "has space"}, CodeInvalidArgument},
	} {
		if _, err := m.RecordHint(ctx, "c", tc.spec); CodeOf(err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}
	dry, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", DryRun: true})
	if err != nil || !dry.Changed || dry.Hint.ID != "" {
		t.Errorf("a dry run = %+v, %v", dry, err)
	}
	completeAnswer(t, m)
	if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a hint for a done Lesson: err = %v", err)
	}
}

func TestCrashesInAssessmentWrites(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		event string
		run   func(m *machine, dry bool) (bool, error)
	}
	ops := []op{
		{"assessment.recorded", eventAssessmentRecorded, func(m *machine, dry bool) (bool, error) {
			spec := placement
			spec.DryRun = dry
			r, err := m.RecordAssessment(ctx, "c", spec)
			return r.Changed, err
		}},
		{"level.set", eventLevelSet, func(m *machine, dry bool) (bool, error) {
			r, err := m.UpdateTopic(ctx, "c", TopicChanges{Level: ptr(LevelExpert), DryRun: dry})
			return r.Changed, err
		}},
		{"hint.recorded", eventHintRecorded, func(m *machine, dry bool) (bool, error) {
			r, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", Request: "same", DryRun: dry})
			return r.Changed, err
		}},
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := learningTopic(t)
				m.crash = crashOnce(point)
				_, err := o.run(m, false)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				dryChanged, dryErr := o.run(m, true)
				realChanged, realErr := o.run(m, false)
				if dryErr != nil || realErr != nil || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				if hasIntentFile(t, m) {
					t.Error("the intent marker survived the next write")
				}
				s := replayFolder(t, filepath.Join(m.home, "c"))
				if len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				if n := countEvents(s, o.event); n != 1 {
					t.Errorf("%d %s Events, want 1", n, o.event)
				}
			})
		}
	}
}

// TestLevelOnTwoMachines: the Level set on both machines from the same
// version is flagged after a merge; Assessments recorded on both are both
// kept.
func TestLevelOnTwoMachines(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, oneLessonSyllabus)
	a.update(t, TopicChanges{Level: ptr(LevelAdvanced)})
	b.update(t, TopicChanges{Level: ptr(LevelBeginner)})
	noLevel := func(area string) AssessmentSpec {
		return AssessmentSpec{Kind: AssessmentPlacement, Summary: "on " + area,
			Items: []AssessmentItem{{Area: area, Outcome: AnswerCorrect}}}
	}
	if _, err := a.RecordAssessment(ctx, "c", noLevel("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RecordAssessment(ctx, "c", noLevel("b")); err != nil {
		t.Fatal(err)
	}
	onA, onB := merge(t, a, b)
	for name, flags := range map[string][]string{"a": onA, "b": onB} {
		if !mentions(flags, topicFile) {
			t.Errorf("flags on %s = %q, want the Level changed on two machines flagged", name, flags)
		}
	}
	for _, m := range []*machine{a, b} {
		list, err := m.ListAssessments(ctx, "c")
		if err != nil || len(list.Assessments) != 2 {
			t.Errorf("Assessments after the merge = %+v, %v", list.Assessments, err)
		}
	}
}
