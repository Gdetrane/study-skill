package core

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAnEstimateThatIsNotANumberIsAPreciseError(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	for _, bad := range []string{"nan", "inf", "-inf"} {
		writeFile(t, m, syllabusFile, strings.Replace(readSyllabusFile(t, m), "hours = 3.0", "hours = "+bad, 1))
		want := "Lesson 2.2 (maps) has"
		if v, err := m.SyllabusOf(ctx, "c"); err != nil || !strings.Contains(v.FileError, want) {
			t.Errorf("%s: file error %q, %v", bad, v.FileError, err)
		}
		if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", FromFile: true}); CodeOf(err) != CodeInvalidArgument ||
			!strings.Contains(err.Error(), want) {
			t.Errorf("%s: adopting it: %v", bad, err)
		}
		s, err := ParseSyllabus([]byte("[[milestones]]\nid = \"m\"\ntitle = \"M\"\n[[milestones.lessons]]\nid = \"l\"\ntitle = \"L\"\nhours = "+bad+"\n"), "next.toml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", Syllabus: s}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: proposing it: %v", bad, err)
		}
		writeFile(t, m, syllabusFile, strings.Replace(readSyllabusFile(t, m), "hours = "+bad, "hours = 3.0", 1))
	}
}

// TestTheResumePointLeavesOutSkippedAndRemovedLessons: a skipped first
// Lesson is passed over, and a Next step of a Lesson a Revision skips or
// removes no longer leads.
func TestTheResumePointLeavesOutSkippedAndRemovedLessons(t *testing.T) {
	ctx := context.Background()
	resume := func(m *machine) ResumePoint {
		t.Helper()
		topic, err := m.readTopic("c")
		if err != nil || topic.Resume == nil {
			t.Fatalf("%+v, %v", topic, err)
		}
		return *topic.Resume
	}
	m := newTopic(t)
	revise(t, m, twoMilestones)
	skipFirst := clone(twoMilestones)
	skipFirst.Milestones[0].Lessons[0].Skipped = true
	revise(t, m, skipFirst)
	if r := resume(m); r.Lesson != "values" {
		t.Errorf("with Lesson 1.1 skipped, the Resume point is at %q", r.Lesson)
	}

	for name, edit := range map[string]func(*Syllabus){
		"skipped": func(s *Syllabus) { s.Milestones[0].Lessons[1].Skipped = true },
		"removed": func(s *Syllabus) { s.Milestones[0].Lessons = s.Milestones[0].Lessons[:1] },
	} {
		m := newTopic(t)
		revise(t, m, twoMilestones)
		writeFile(t, m, "lessons/values.md", answerLesson)
		if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "values", Phase: PhaseTeaching, NextStep: "Finish the values exercise"}); err != nil {
			t.Fatal(err)
		}
		if r := resume(m); r.NextStep == nil {
			t.Fatalf("%s: no Next step recorded", name)
		}
		next := clone(twoMilestones)
		edit(&next)
		revise(t, m, next)
		if r := resume(m); r.NextStep != nil {
			t.Errorf("%s: the Resume point still leads with %+v", name, r.NextStep)
		}
	}
}

// TestASkippedLessonCannotBeStudied: no Phase, no Attempt, no completion.
func TestASkippedLessonCannotBeStudied(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	skipped := clone(oneLessonSyllabus)
	skipped.Milestones[0].Lessons[0].Skipped = true
	revise(t, m, skipped)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "answer", Phase: PhaseFeedback}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a Phase: %v", err)
	}
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a Check: %v", err)
	}
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "skipped") {
		t.Errorf("completing it: %v", err)
	}
	// What was covered before the skip can still become Cards.
	if card := m.addCard(t, CardSpec{Lesson: "answer", Prompt: "What is the answer?", Answer: "42"}); card.Lesson != "answer" {
		t.Errorf("a Card for the skipped Lesson = %+v", card)
	}
}

// TestSettledLessonsAreCheckedAgainWhenApplying: a Lesson done between
// proposing and applying is not rewritten, and the learner is not asked.
func TestSettledLessonsAreCheckedAgainWhenApplying(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	renamed := clone(oneLessonSyllabus)
	renamed.Milestones[0].Lessons[0].Title = "Another answer"
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Rename", Syllabus: renamed})
	if err != nil {
		t.Fatal(err)
	}
	completeAnswer(t, m)
	if _, err := m.Revision(ctx, "c", p.Revision); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("asking about it: %v", err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Lesson answer is done") {
		t.Errorf("applying it: %v", err)
	}
}

// TestTheLearnerIsAskedOnlyWhenTheAnswerCounts: Revision refuses what
// applying would refuse; ProposedRevision, for declining, still offers it.
func TestTheLearnerIsAskedOnlyWhenTheAnswerCounts(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	next := clone(twoMilestones)
	next.Milestones[1].Lessons = append(next.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add generics", Syllabus: next})
	if err != nil {
		t.Fatal(err)
	}
	original := readSyllabusFile(t, m)
	writeFile(t, m, syllabusFile, strings.Replace(original, `title = "Maps"`, `title = "Maps!"`, 1))
	if _, err := m.Revision(ctx, "c", p.Revision); CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "outside Lamplight") {
		t.Errorf("asking while syllabus.toml is edited outside: %v", err)
	}
	writeFile(t, m, syllabusFile, original)
	other := clone(twoMilestones)
	other.Milestones[0].Title = "Foundations"
	revise(t, m, other)
	if _, err := m.Revision(ctx, "c", p.Revision); CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("asking about a stale Revision: %v", err)
	}
	if prop, err := m.ProposedRevision(ctx, "c", p.Revision); err != nil || !prop.Stale {
		t.Errorf("a stale Revision to decline: %+v, %v", prop, err)
	}
}

// TestAnInterruptedApplyIsNotAHandEdit: syllabus.toml lags behind the
// History until the next write finishes it; that is no edit to adopt.
func TestAnInterruptedApplyIsNotAHandEdit(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	next := clone(twoMilestones)
	next.Milestones[1].Lessons = append(next.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add generics", Syllabus: next})
	if err != nil {
		t.Fatal(err)
	}
	m.crash = crashOnce(crashAfterEvent)
	_, _ = m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false)
	m.crash = nil
	if v, err := m.SyllabusOf(ctx, "c"); err != nil || v.EditedOutside || v.FileError != "" {
		t.Errorf("the Syllabus view: edited outside %v, %q, %v", v.EditedOutside, v.FileError, err)
	}
	topic, err := m.readTopic("c")
	if err != nil || hasFlag(topic.Flags, FlagEditedOutside, syllabusFile) || !hasFlag(topic.Flags, FlagInterruptedWrite, "") {
		t.Errorf("flags = %+v, %v; want only the interrupted write", topic.Flags, err)
	}
}

// TestARevisionMustChangeSomething, except adopting the file, and every
// change has a kind.
func TestARevisionMustChangeSomething(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	first := revise(t, m, twoMilestones)
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Nothing", Syllabus: clone(twoMilestones)}); CodeOf(err) != CodeInvalidArgument ||
		!strings.Contains(err.Error(), "nothing to approve") {
		t.Errorf("a Revision that changes nothing: %v", err)
	}
	adopt, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Adopt the file as it is", FromFile: true})
	if err != nil || len(adopt.Changes.Changes) != 1 || adopt.Changes.Changes[0].Kind != ChangeFileAdopted {
		t.Errorf("adopting the file unchanged: %+v, %v", adopt.Changes, err)
	}
	extra, err := ParseSyllabus([]byte(strings.Replace(readSyllabusFile(t, m), `title = "Maps"`, "title = \"Maps\"\ncolour = \"red\"", 1)), "next.toml")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Colour maps", Syllabus: extra})
	if err != nil || len(settings.Changes.Changes) != 1 || settings.Changes.Changes[0].Kind != ChangeSettings {
		t.Errorf("a change of settings only: %+v, %v", settings.Changes, err)
	}
	for _, ch := range append(append(first.Changes.Changes, adopt.Changes.Changes...), settings.Changes.Changes...) {
		if ch.Kind == "" {
			t.Errorf("a change without a kind: %+v", ch)
		}
	}
}

// TestReorderingLessonsIsAChangeOfItsOwn: Lessons that only change places
// within a Milestone are named as such, never as unknown settings.
func TestReorderingLessonsIsAChangeOfItsOwn(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	three := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Priority: PriorityMust, Lessons: []SyllabusLesson{
		{ID: "one", Title: "One"}, {ID: "two", Title: "Two"}, {ID: "three", Title: "Three"}}}}}
	revise(t, m, three)
	swapped := clone(three)
	l := swapped.Milestones[0].Lessons
	l[0], l[1] = l[1], l[0]
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Two first", Syllabus: swapped})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes.Changes) != 1 || p.Changes.Changes[0].Kind != ChangeLessonsReordered || p.Changes.Changes[0].Milestone != "basics" ||
		!strings.Contains(p.Changes.Text, `Reorders the Lessons of Milestone 1 "Basics": now "Two", "One", "Three"`) ||
		len(p.Changes.Renumbered) != 2 {
		t.Errorf("a swap of two Lessons: %+v", p.Changes)
	}
	// Adding a Lesson in front renumbers the others but reorders nothing.
	added := clone(three)
	added.Milestones[0].Lessons = append([]SyllabusLesson{{ID: "zero", Title: "Zero"}}, added.Milestones[0].Lessons...)
	p, err = m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Zero first", Syllabus: added})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range p.Changes.Changes {
		if ch.Kind == ChangeLessonsReordered || ch.Kind == ChangeSettings {
			t.Errorf("adding a Lesson in front: %+v", p.Changes.Changes)
		}
	}
}

// TestSettingsLamplightDoesNotKnowKeepTheirTypeAndMerge: a float stays a
// float through a Revision, and settings an agent's file supplies join
// those the entity already has.
func TestSettingsLamplightDoesNotKnowKeepTheirTypeAndMerge(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	adoptEdit(t, m, `title = "Maps"`, "title = \"Maps\"\nweight = 1.0\ndifficulty = 3")
	if after := readSyllabusFile(t, m); !strings.Contains(after, "weight = 1.0") || !strings.Contains(after, "difficulty = 3\n") {
		t.Errorf("settings changed type:\n%s", after)
	}
	withColour, err := ParseSyllabus([]byte(strings.Replace(readSyllabusFile(t, m), "weight = 1.0", "colour = \"red\"", 1)), "next.toml")
	if err != nil {
		t.Fatal(err)
	}
	withColour.Milestones[1].Lessons[1].Extra = map[string]any{"colour": "red"}
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Colour maps", Syllabus: withColour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	after := readSyllabusFile(t, m)
	for _, kept := range []string{`colour = "red"`, "weight = 1.0", "difficulty = 3"} {
		if !strings.Contains(after, kept) {
			t.Errorf("after merging the agent's setting, syllabus.toml lacks %s:\n%s", kept, after)
		}
	}
}

// TestRepeatedAnswersReportTheRecordedOne: applying or declining again
// changes nothing and returns the answer recorded the first time.
func TestRepeatedAnswersReportTheRecordedOne(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "First", Syllabus: twoMilestones})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaTerminal, Shown: "Apply?", LearnerSaid: "sure"}, false); err != nil {
		t.Fatal(err)
	}
	again, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes again"}, false)
	if err != nil || again.Changed || again.Approval.Via != ViaTerminal || again.Approval.LearnerSaid != "sure" {
		t.Errorf("applying again: %+v, %v", again, err)
	}
}

// TestDirectAnswersAreKeptAsTheLearnerTypedThem: when Lamplight asked the
// learner directly, their comment never fails the answer; an agent's chat
// quote is still checked strictly.
func TestDirectAnswersAreKeptAsTheLearnerTypedThem(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "First", Syllabus: twoMilestones})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "Yes.\nBut keep maps."}, false); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a chat quote with a newline: %v", err)
	}
	long := strings.Repeat("ok ", 400)
	res, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaElicitation, Shown: "Apply?",
		LearnerSaid: "Yes.\nBut keep\tmaps.\x07 " + long + "\xff"}, false)
	if err != nil {
		t.Fatalf("a direct approval with a messy comment failed: %v", err)
	}
	said := res.Approval.LearnerSaid
	if !strings.HasPrefix(said, "Yes. But keep maps. ok ok") || utf8.RuneCountInString(said) > maxSummaryRunes || !utf8.ValidString(said) {
		t.Errorf("the comment was kept as %q", said)
	}
}
