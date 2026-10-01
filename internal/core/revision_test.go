package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// twoMilestones is a Syllabus with two Milestones of two Lessons each.
var twoMilestones = Syllabus{Milestones: []Milestone{
	{ID: "basics", Title: "Basics", Outcome: "Write small programs", Priority: PriorityMust, Target: "2026-11-01",
		Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}, {ID: "values", Title: "Values", Hours: 2}}},
	{ID: "core", Title: "Core", Priority: PriorityIfTime,
		Lessons: []SyllabusLesson{{ID: "loops", Title: "Loops", Hours: 2}, {ID: "maps", Title: "Maps", Hours: 3}}},
}}

// revise proposes a Revision of Topic c and applies it with a chat approval.
func revise(t *testing.T, m *machine, s Syllabus) RevisionProposal {
	t.Helper()
	ctx := context.Background()
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "A change", Syllabus: s})
	if err != nil {
		t.Fatalf("ProposeRevision: %v", err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatalf("ApplyRevision: %v", err)
	}
	return p
}

// clone returns a deep enough copy of a Syllabus to edit in a test.
func clone(s Syllabus) Syllabus {
	out := Syllabus{Milestones: make([]Milestone, len(s.Milestones))}
	for i, m := range s.Milestones {
		m.Lessons = slices.Clone(m.Lessons)
		out.Milestones[i] = m
	}
	return out
}

func lessonNumbers(t *testing.T, m *machine) map[string]string {
	t.Helper()
	v, err := m.SyllabusOf(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, mv := range v.Milestones {
		for _, l := range mv.Lessons {
			out[l.ID] = l.Number + " " + l.Status
		}
	}
	return out
}

// TestRevisionsInsertMoveAndSkipLessons covers the Verification Plan's
// Revisions: each shows its change by title, with the renumbering.
func TestRevisionsInsertMoveAndSkipLessons(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	first := revise(t, m, twoMilestones)
	if c := first.Changes; c.Changes[0].Kind != ChangeFirstSyllabus || !strings.Contains(c.Text, `Lesson 2.2 "Maps", 3 h`) {
		t.Errorf("the first Syllabus's changes:\n%s", c.Text)
	}

	// Insert a Lesson before "Values": Values and nothing else renumbers.
	inserted := clone(twoMilestones)
	inserted.Milestones[0].Lessons = slices.Insert(inserted.Milestones[0].Lessons, 1, SyllabusLesson{ID: "types", Title: "Types", Hours: 1})
	p := revise(t, m, inserted)
	if want := []Renumbering{{Lesson: "values", Title: "Values", From: "1.2", To: "1.3"}}; !slices.Equal(p.Changes.Renumbered, want) {
		t.Errorf("renumbered = %+v, want %+v", p.Changes.Renumbered, want)
	}
	if !strings.Contains(p.Changes.Text, `Adds Lesson 1.2 "Types" to Milestone 1 "Basics", 1 h`) ||
		!strings.Contains(p.Changes.Text, `Renumbers: "Values" 1.2 → 1.3`) {
		t.Errorf("inserting:\n%s", p.Changes.Text)
	}

	// Move "Loops" into Basics.
	moved := clone(inserted)
	moved.Milestones[0].Lessons = append(moved.Milestones[0].Lessons, moved.Milestones[1].Lessons[0])
	moved.Milestones[1].Lessons = moved.Milestones[1].Lessons[1:]
	p = revise(t, m, moved)
	if !strings.Contains(p.Changes.Text, `Moves Lesson "Loops" from Milestone 2 "Core" to Milestone 1 "Basics"`) {
		t.Errorf("moving:\n%s", p.Changes.Text)
	}
	if got := lessonNumbers(t, m); got["loops"] != "1.4 not_started" || got["maps"] != "2.1 not_started" {
		t.Errorf("after moving: %v", got)
	}

	// Skip "Values" while it is in progress: it keeps its number, is left
	// out of the Resume point, and cannot be studied.
	writeFile(t, m, "lessons/values.md", answerLesson)
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "values", Phase: PhaseTeaching}); err != nil {
		t.Fatal(err)
	}
	skipped := clone(moved)
	skipped.Milestones[0].Lessons[2].Skipped = true
	p = revise(t, m, skipped)
	if !slices.Equal(p.Changes.SkippedInProgress, []string{"values"}) || !strings.Contains(p.Changes.Text, `Skips Lesson 1.3 "Values", which was in progress`) {
		t.Errorf("skipping in progress: %+v\n%s", p.Changes.SkippedInProgress, p.Changes.Text)
	}
	if got := lessonNumbers(t, m); got["values"] != "1.3 skipped" {
		t.Errorf("a skipped Lesson = %q", got["values"])
	}
	if _, err := m.SetPhase(ctx, "c", PhaseSpec{Lesson: "values", Phase: PhasePracticing}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("studying a skipped Lesson: err = %v, want failed_precondition", err)
	}
	if !strings.Contains(readSyllabusFile(t, m), "skipped = true") {
		t.Errorf("syllabus.toml does not mark the skip:\n%s", readSyllabusFile(t, m))
	}

	// A skipped Lesson is never rewritten, but the skip can be taken back.
	renamed := clone(skipped)
	renamed.Milestones[0].Lessons[2].Title = "Values and types"
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", Syllabus: renamed}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("renaming a skipped Lesson: err = %v", err)
	}
	removed := clone(skipped)
	removed.Milestones[0].Lessons = slices.Delete(removed.Milestones[0].Lessons, 2, 3)
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", Syllabus: removed}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("removing a skipped Lesson: err = %v", err)
	}
	p = revise(t, m, moved)
	if !strings.Contains(p.Changes.Text, `Takes back the skip of Lesson 1.3 "Values"`) {
		t.Errorf("taking a skip back:\n%s", p.Changes.Text)
	}
	if got := lessonNumbers(t, m); got["values"] != "1.3 in_progress" {
		t.Errorf("after taking the skip back: %q", got["values"])
	}
}

// completeAnswer completes Lesson "answer" of a learningTopic.
func completeAnswer(t *testing.T, m *machine) {
	t.Helper()
	ctx := context.Background()
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil || a.Outcome != OutcomePassed {
		t.Fatalf("RunCheck = %+v, %v", a, err)
	}
	if _, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"}); err != nil {
		t.Fatalf("CompleteLesson: %v", err)
	}
}

func TestDoneLessonsAreNeverRewritten(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	completeAnswer(t, m)
	base := Syllabus{Milestones: []Milestone{
		{ID: "basics", Title: "Basics", Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}}},
		{ID: "more", Title: "More", Lessons: []SyllabusLesson{{ID: "question", Title: "The question"}}},
	}}
	revise(t, m, base)
	for name, edit := range map[string]func(*Syllabus){
		"rename": func(s *Syllabus) { s.Milestones[0].Lessons[0].Title = "Another answer" },
		"hours":  func(s *Syllabus) { s.Milestones[0].Lessons[0].Hours = 5 },
		"skip":   func(s *Syllabus) { s.Milestones[0].Lessons[0].Skipped = true },
		"remove": func(s *Syllabus) { s.Milestones[0].Lessons[0] = SyllabusLesson{ID: "other", Title: "Other"} },
		"move to": func(s *Syllabus) {
			s.Milestones[0].Lessons, s.Milestones[1].Lessons = s.Milestones[1].Lessons, s.Milestones[0].Lessons
		},
	} {
		next := clone(base)
		edit(&next)
		if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: name, Syllabus: next}); CodeOf(err) != CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "Lesson answer is done") {
			t.Errorf("%s a done Lesson: err = %v, want failed_precondition", name, err)
		}
	}
	// Inserting before a done Lesson renumbers it, which is allowed.
	inserted := clone(base)
	inserted.Milestones[0].Lessons = slices.Insert(inserted.Milestones[0].Lessons, 0, SyllabusLesson{ID: "warmup", Title: "Warm-up"})
	p := revise(t, m, inserted)
	if len(p.Changes.Renumbered) != 1 || p.Changes.Renumbered[0].Lesson != "answer" {
		t.Errorf("renumbered = %+v", p.Changes.Renumbered)
	}
}

// TestAStaleRevisionIsRejected: two Revisions from the same Syllabus; once
// one is applied, the other is stale.
func TestAStaleRevisionIsRejected(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	a, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Drop maps", Syllabus: Syllabus{Milestones: twoMilestones.Milestones[:1]}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add a Milestone", Syllabus: Syllabus{Milestones: append(slices.Clone(twoMilestones.Milestones),
		Milestone{ID: "stretch", Title: "Stretch", Priority: PriorityAfterDeadline, Lessons: []SyllabusLesson{{ID: "generics", Title: "Generics"}}})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", a.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", b.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "changed since") {
		t.Errorf("applying a stale Revision: err = %v", err)
	}
	if _, err := m.Revision(ctx, "c", b.Revision); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("asking about a stale Revision: err = %v", err)
	}
	v, err := m.SyllabusOf(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Proposals) != 1 || v.Proposals[0].Revision != b.Revision || !v.Proposals[0].Stale {
		t.Errorf("proposals = %+v, want the stale one marked", v.Proposals)
	}
}

// TestHandEditsAreFlaggedPrecisely: an edit of syllabus.toml outside
// Lamplight is flagged, and what keeps it from being adopted is named
// precisely, by line or by Milestone and Lesson.
func TestHandEditsAreFlaggedPrecisely(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	original := readSyllabusFile(t, m)
	for _, tc := range []struct {
		name, edit, want string
	}{
		{"valid", strings.Replace(original, `title = "Loops"`, `title = "Loops and ranges"`, 1), "propose it as a Revision from the file"},
		{"TOML", strings.Replace(original, `title = "Loops"`, `title = "Loops`, 1), "line "},
		{"empty title", strings.Replace(original, `title = "Values"`, `title = ""`, 1), "Lesson 1.2 (values): the title is empty"},
		{"duplicate id", strings.Replace(original, `id = "maps"`, `id = "loops"`, 1), `Lesson 2.2 (loops): the id "loops" is already used by Lesson 2.1 (loops)`},
		{"priority", strings.Replace(original, `priority = "if_time"`, `priority = "someday"`, 1), "Milestone 2 (core): the priority must be"},
		{"target", strings.Replace(original, `target = "2026-11-01"`, `target = "next month"`, 1), "Milestone 1 (basics): the target date must be written YYYY-MM-DD"},
		{"hours", strings.Replace(original, `hours = 3.0`, `hours = "three"`, 1), "hours of Lesson 2.2 is not a number"},
		{"no lessons", original[:strings.Index(original, "[[milestones.lessons]]")], "Milestone 1 (basics) has no Lessons"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, m, syllabusFile, tc.edit)
			topic, err := m.readTopic("c")
			if err != nil {
				t.Fatal(err)
			}
			var flag *Flag
			for i := range topic.Flags {
				if topic.Flags[i].Kind == FlagEditedOutside && topic.Flags[i].Item == syllabusFile {
					flag = &topic.Flags[i]
				}
			}
			if flag == nil || !strings.Contains(flag.Message, tc.want) {
				t.Errorf("flag = %+v, want a message containing %q", flag, tc.want)
			}
			v, err := m.SyllabusOf(ctx, "c")
			if err != nil || !v.EditedOutside {
				t.Fatalf("the Syllabus view = %+v, %v", v, err)
			}
			if tc.name != "valid" && !strings.Contains(v.FileError, tc.want) {
				t.Errorf("file error = %q, want %q", v.FileError, tc.want)
			}
		})
	}
	writeFile(t, m, syllabusFile, original)
	if topic, _ := m.readTopic("c"); hasFlag(topic.Flags, FlagEditedOutside, syllabusFile) {
		t.Error("restoring the file did not clear the flag")
	}
}

func TestTargetDates(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	bad := clone(twoMilestones)
	bad.Milestones[1].Target = "2026-13-01"
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", Syllabus: bad}); CodeOf(err) != CodeInvalidArgument ||
		!strings.Contains(err.Error(), "Milestone 2 (core)") {
		t.Errorf("an impossible date: err = %v", err)
	}
	revise(t, m, twoMilestones)
	if !strings.Contains(readSyllabusFile(t, m), `target = "2026-11-01"`) {
		t.Errorf("the target date is not written as a date:\n%s", readSyllabusFile(t, m))
	}

	// A learner writing native TOML dates, for the target and for a key of
	// their own, gets them back as text, the same on every machine.
	edited := strings.Replace(readSyllabusFile(t, m), `target = "2026-11-01"`, "target = 2026-12-24\ndeadline = 2027-01-15", 1)
	writeFile(t, m, syllabusFile, edited)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Keep the dates", FromFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Syllabus.Milestones[0].Target; got != "2026-12-24" {
		t.Errorf("a native TOML date read as %q", got)
	}
	if !strings.Contains(p.Changes.Text, "target date 2026-12-24 instead of 2026-11-01") {
		t.Errorf("changes:\n%s", p.Changes.Text)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	after := readSyllabusFile(t, m)
	if !strings.Contains(after, `target = "2026-12-24"`) || !strings.Contains(after, `deadline = "2027-01-15"`) {
		t.Errorf("dates after adopting:\n%s", after)
	}
	v, err := m.SyllabusOf(ctx, "c")
	if err != nil || v.Milestones[0].Target != "2026-12-24" || v.EditedOutside {
		t.Errorf("the Syllabus view = %+v, %v", v.Milestones[0], err)
	}
}

func TestApprovalsRecordHowTheLearnerAnswered(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "First", Syllabus: twoMilestones})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Approval{
		{Via: ViaChat},
		{Via: "agent", LearnerSaid: "sure"},
		{Via: ViaElicitation, LearnerSaid: "yes"},
		{Via: ViaTerminal},
	} {
		if _, err := m.ApplyRevision(ctx, "c", p.Revision, bad, false); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("approval %+v: err = %v, want invalid_argument", bad, err)
		}
	}
	if res, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{}, true); err != nil || !res.DryRun || !res.Changed {
		t.Errorf("a dry run needs no approval: %+v, %v", res, err)
	}
	shown := "Revision: First\nSets the first Syllabus"
	res, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaElicitation, Shown: shown}, false)
	if err != nil || !res.Changed || res.Approval.Via != ViaElicitation {
		t.Fatalf("an elicited approval: %+v, %v", res, err)
	}
	lines := historyLines(t, filepath.Join(m.home, "c"))
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"via":"elicitation"`) || !strings.Contains(last, `"shown":"Revision: First\nSets the first Syllabus"`) {
		t.Errorf("the History does not record how the learner approved: %s", last)
	}
}

func TestADeclinedRevisionIsNeverApplied(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Drop Core", Syllabus: Syllabus{Milestones: twoMilestones.Milestones[:1]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeclineRevision(ctx, "c", p.Revision, Approval{Via: ViaChat}, false); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a chat decline without the learner's words: err = %v", err)
	}
	res, err := m.DeclineRevision(ctx, "c", p.Revision, Approval{Via: ViaTerminal, Shown: "Apply?", LearnerSaid: "not yet"}, false)
	if err != nil || !res.Changed || res.Decision.Via != ViaTerminal {
		t.Fatalf("declining: %+v, %v", res, err)
	}
	// Declining again changes nothing and reports the decision recorded.
	if again, err := m.DeclineRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "no"}, false); err != nil || again.Changed ||
		again.Decision.LearnerSaid != "not yet" {
		t.Errorf("declining twice: %+v, %v", again, err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes after all"}, false); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "declined") {
		t.Errorf("applying a declined Revision: err = %v", err)
	}
	if v, err := m.SyllabusOf(ctx, "c"); err != nil || len(v.Proposals) != 0 || len(v.Milestones) != 2 {
		t.Errorf("after declining: %d proposals, %d Milestones, %v", len(v.Proposals), len(v.Milestones), err)
	}
	more := clone(twoMilestones)
	more.Milestones[1].Lessons = append(more.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
	applied := revise(t, m, more)
	if _, err := m.DeclineRevision(ctx, "c", applied.Revision, Approval{Via: ViaChat, LearnerSaid: "no"}, false); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("declining an applied Revision: err = %v", err)
	}
}

func TestCrashesInRevisionWrites(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		run   func(m *machine, revision string, dryRun bool) (changed bool, err error)
		check func(t *testing.T, m *machine)
	}
	ops := []op{
		{"revision.applied",
			func(m *machine, rev string, dry bool) (bool, error) {
				res, err := m.ApplyRevision(ctx, "c", rev, Approval{Via: ViaTerminal, Shown: "Apply?"}, dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if !strings.Contains(readSyllabusFile(t, m), `id = "generics"`) {
					t.Errorf("syllabus.toml = %s", readSyllabusFile(t, m))
				}
			}},
		{"revision.declined",
			func(m *machine, rev string, dry bool) (bool, error) {
				res, err := m.DeclineRevision(ctx, "c", rev, Approval{Via: ViaChat, LearnerSaid: "no"}, dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine) {
				if v, err := m.SyllabusOf(ctx, "c"); err != nil || len(v.Proposals) != 0 {
					t.Errorf("proposals after declining = %+v, %v", v.Proposals, err)
				}
			}},
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				revise(t, m, twoMilestones)
				next := clone(twoMilestones)
				next.Milestones[1].Lessons = append(next.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
				p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add generics", Syllabus: next})
				if err != nil {
					t.Fatal(err)
				}
				m.crash = crashOnce(point)
				_, err = o.run(m, p.Revision, false)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				dryChanged, dryErr := o.run(m, p.Revision, true)
				realChanged, realErr := o.run(m, p.Revision, false)
				if CodeOf(dryErr) != CodeOf(realErr) || (dryErr == nil) != (realErr == nil) || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				if hasIntentFile(t, m) {
					t.Error("the intent marker survived the next write")
				}
				if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				o.check(t, m)
			})
		}
	}
}

// TestRevisionConflictsBetweenMachines plays two machines that change one
// Topic without syncing in between, merged with real git: a Lesson
// completed on one and skipped on the other, and a Revision applied on one
// and declined on the other, are flagged, never resolved silently.
func TestRevisionConflictsBetweenMachines(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	revise(t, a, oneLessonSyllabus)
	writeFile(t, a, "lessons/answer.md", answerLesson)
	writeFile(t, a, "practice/answer/check.sh", answerCheck)
	writeFile(t, a, "practice/answer/answer.txt", "41\n")
	next := clone(oneLessonSyllabus)
	next.Milestones[0].Lessons = append(next.Milestones[0].Lessons, SyllabusLesson{ID: "question", Title: "The question"})
	pending, err := a.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add the question", Syllabus: next})
	if err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	dirA := filepath.Join(a.home, "c")

	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", dirA, "c")
	dirB := filepath.Join(b.home, "c")

	// A completes the Lesson and declines the pending Revision; B skips the
	// Lesson and applies the pending Revision.
	completeAnswer(t, a)
	if _, err := a.DeclineRevision(ctx, "c", pending.Revision, Approval{Via: ViaChat, LearnerSaid: "no"}, false); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	if _, err := b.ApplyRevision(ctx, "c", pending.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	skip := clone(next)
	skip.Milestones[0].Lessons[0].Skipped = true
	revise(t, b, skip)
	takeCheckpoint(t, b)

	flagsAfterPull := func(dir, from string, m *machine) []Flag {
		t.Helper()
		out := git(t, dir, "pull", "-q", "--no-rebase", "--no-edit", "-X", "theirs", from, "main")
		_ = out
		topic, err := m.readTopic("c")
		if err != nil {
			t.Fatal(err)
		}
		var conflicts []Flag
		for _, f := range topic.Flags {
			if f.Kind == FlagConflict {
				conflicts = append(conflicts, f)
			}
		}
		return conflicts
	}
	onB := flagsAfterPull(dirB, dirA, b)
	onA := flagsAfterPull(dirA, dirB, a)
	describe := func(flags []Flag) string {
		var parts []string
		for _, f := range flags {
			parts = append(parts, f.Message)
		}
		return strings.Join(parts, "\n")
	}
	for name, flags := range map[string][]Flag{"B": onB, "A": onA} {
		text := describe(flags)
		if !strings.Contains(text, "both declined and applied") && !strings.Contains(text, "both applied and declined") {
			t.Errorf("on %s, the Revision applied and declined is not flagged:\n%s", name, text)
		}
		if !strings.Contains(text, "skipped Lesson answer") && !strings.Contains(text, "although a Revision skipped it") {
			t.Errorf("on %s, the done Lesson skipped is not flagged:\n%s", name, text)
		}
	}
	if describe(onA) != describe(onB) {
		t.Errorf("the two machines flag different things:\nA:\n%s\nB:\n%s", describe(onA), describe(onB))
	}
	if _, err := os.Stat(filepath.Join(dirB, ".git")); err != nil {
		t.Fatal(err)
	}
}

// TestAdoptingAnEditLamplightWouldWriteClearsTheFlag: a careful hand edit
// can be exactly what Lamplight writes, so adopting it leaves the file as it
// is. The Event still records that version, so the flag clears.
func TestAdoptingAnEditLamplightWouldWriteClearsTheFlag(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	revise(t, m, twoMilestones)
	writeFile(t, m, syllabusFile, strings.Replace(readSyllabusFile(t, m), `title = "Loops"`, `title = "Loops and ranges"`, 1))
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Keep my edit", FromFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || hasFlag(topic.Flags, FlagEditedOutside, syllabusFile) {
		t.Errorf("after adopting an edit Lamplight would write: %+v, %v", topic.Flags, err)
	}
	if lessonNumbers(t, m)["loops"] != "2.1 not_started" {
		t.Errorf("the adopted Syllabus: %v", lessonNumbers(t, m))
	}
	v, err := m.SyllabusOf(ctx, "c")
	if err != nil || v.Milestones[1].Lessons[0].Title != "Loops and ranges" {
		t.Errorf("the adopted title: %+v, %v", v.Milestones[1].Lessons, err)
	}
}
