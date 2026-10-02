package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// twoMachines sets up Topic c on machine a with the given Syllabus and the
// "answer" Lesson's files, takes a Checkpoint, and clones it to machine b,
// whose clock is a minute ahead: b's Events sort after a's.
func twoMachines(t *testing.T, s Syllabus) (a, b *machine) {
	t.Helper()
	gitIdentity(t)
	ctx := context.Background()
	a = newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	revise(t, a, s)
	writeFile(t, a, "lessons/answer.md", answerLesson)
	writeFile(t, a, "practice/answer/check.sh", answerCheck)
	writeFile(t, a, "practice/answer/answer.txt", "41\n")
	takeCheckpoint(t, a)
	b = newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", filepath.Join(a.home, "c"), "c")
	return a, b
}

// merge takes a Checkpoint on both machines and merges each into the other,
// keeping the other side of any conflicting hunk, as a learner resolving by
// hand might. It returns the conflict flags each machine shows.
func merge(t *testing.T, a, b *machine) (onA, onB []string) {
	t.Helper()
	takeCheckpoint(t, a)
	takeCheckpoint(t, b)
	dirA, dirB := filepath.Join(a.home, "c"), filepath.Join(b.home, "c")
	git(t, dirB, "pull", "-q", "--no-rebase", "--no-edit", "-X", "theirs", dirA, "main")
	git(t, dirA, "pull", "-q", "--no-rebase", "--no-edit", "-X", "theirs", dirB, "main")
	conflicts := func(m *machine) []string {
		topic, err := m.readTopic("c")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range topic.Flags {
			if f.Kind == FlagConflict {
				out = append(out, f.Message)
			}
		}
		return out
	}
	return conflicts(a), conflicts(b)
}

func mentions(flags []string, text string) bool {
	for _, f := range flags {
		if strings.Contains(f, text) {
			return true
		}
	}
	return false
}

// adoptEdit edits syllabus.toml by hand on m and adopts the edit.
func adoptEdit(t *testing.T, m *machine, from, to string) {
	t.Helper()
	ctx := context.Background()
	writeFile(t, m, syllabusFile, strings.Replace(readSyllabusFile(t, m), from, to, 1))
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Keep my edit", FromFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
}

// TestTwoMachinesChangingOneSyllabusAreFlagged: a Revision applied on one
// machine and a hand edit adopted on the other, from one Syllabus version,
// lose one change in the merge. Whether Lamplight rewrites the adopted file
// or keeps it byte for byte, the conflict is flagged on both machines.
func TestTwoMachinesChangingOneSyllabusAreFlagged(t *testing.T) {
	for name, tc := range map[string]struct {
		onA      func(s *Syllabus)
		from, to string
	}{
		"a skip and an adopted rename": {func(s *Syllabus) { s.Milestones[1].Lessons[1].Skipped = true },
			`title = "Maps"`, `title = "Maps and sets"`},
		"a rename and an adopted rename": {func(s *Syllabus) { s.Milestones[1].Lessons[0].Title = "Loops (A)" },
			`title = "Loops"`, `title = "Loops (B)"`},
		"a rename and a rewritten adoption": {func(s *Syllabus) { s.Milestones[1].Lessons[0].Title = "Loops (A)" },
			`title = "Loops"`, `title = "Loops (B)" # mine`},
	} {
		t.Run(name, func(t *testing.T) {
			a, b := twoMachines(t, twoMilestones)
			next := clone(twoMilestones)
			tc.onA(&next)
			revise(t, a, next)
			adoptEdit(t, b, tc.from, tc.to)
			onA, onB := merge(t, a, b)
			for machine, flags := range map[string][]string{"A": onA, "B": onB} {
				if !mentions(flags, "was approved for a Syllabus that Event a") {
					t.Errorf("on %s, the lost change is not flagged: %q", machine, flags)
				}
			}
		})
	}
}

// TestARemovedLessonCompletedOnTheOtherMachine covers both merge orders:
// the removal sorting first, and the completion sorting first. Either way
// the conflict is flagged, and later Revisions are not blocked.
func TestARemovedLessonCompletedOnTheOtherMachine(t *testing.T) {
	ctx := context.Background()
	removed := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Priority: PriorityMust,
		Lessons: []SyllabusLesson{{ID: "question", Title: "The question", Hours: 1}}}}}
	both := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Priority: PriorityMust,
		Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}, {ID: "question", Title: "The question", Hours: 1}}}}}
	for name, tc := range map[string]struct {
		remover, completer func(a, b *machine) *machine
		want               string
	}{
		"the removal first":    {func(a, _ *machine) *machine { return a }, func(_, b *machine) *machine { return b }, "completed although a Revision removed it"},
		"the completion first": {func(_, b *machine) *machine { return b }, func(a, _ *machine) *machine { return a }, "removed Lesson answer, which Event"},
	} {
		t.Run(name, func(t *testing.T) {
			a, b := twoMachines(t, both)
			revise(t, tc.remover(a, b), removed)
			completeAnswer(t, tc.completer(a, b))
			onA, onB := merge(t, a, b)
			for machine, flags := range map[string][]string{"A": onA, "B": onB} {
				if !mentions(flags, tc.want) {
					t.Errorf("on %s: %q, want a flag about %q", machine, flags, tc.want)
				}
			}
			for _, m := range []*machine{a, b} {
				// Once the learner dismisses the flag, a later Revision that
				// still leaves the Lesson out raises no new one.
				topic, err := m.readTopic("c")
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range topic.Flags {
					if _, err := m.DismissFlag(ctx, "c", f.ID, false); err != nil {
						t.Fatal(err)
					}
				}
				next := clone(removed)
				next.Milestones[0].Lessons = append(next.Milestones[0].Lessons, SyllabusLesson{ID: "extra", Title: "Extra"})
				if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add extra", Syllabus: next}); err != nil {
					t.Errorf("a later Revision is refused: %v", err)
					continue
				}
				revise(t, m, next)
				if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
					t.Errorf("flags after a later Revision: %+v, %v; want the dismissal to hold", topic.Flags, err)
				}
				// Adding the done Lesson back is how the learner settles it,
				// but never as skipped.
				back := clone(next)
				back.Milestones[0].Lessons = append(back.Milestones[0].Lessons, SyllabusLesson{ID: "answer", Title: "The answer", Hours: 1, Skipped: true})
				if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Skip the answer", Syllabus: back}); CodeOf(err) != CodeFailedPrecondition {
					t.Errorf("adding a done Lesson back skipped: err = %v, want failed_precondition", err)
				}
			}
		})
	}
}

// TestConflictsInEachReplayOrder covers the flags that depend on which of
// two machines' Events sorts first.
func TestConflictsInEachReplayOrder(t *testing.T) {
	ctx := context.Background()
	more := clone(oneLessonSyllabus)
	more.Milestones[0].Lessons = append(more.Milestones[0].Lessons, SyllabusLesson{ID: "question", Title: "The question"})
	for name, tc := range map[string]struct {
		play func(t *testing.T, a, b *machine)
		want string
	}{
		"applied, then declined": {func(t *testing.T, a, b *machine) {
			p, err := a.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add the question", Syllabus: more})
			if err != nil {
				t.Fatal(err)
			}
			takeCheckpoint(t, a)
			git(t, filepath.Join(b.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(a.home, "c"), "main")
			if _, err := a.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := b.DeclineRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "no"}, false); err != nil {
				t.Fatal(err)
			}
		}, "both applied and declined"},
		"completed, then rewritten": {func(t *testing.T, a, b *machine) {
			completeAnswer(t, a)
			renamed := clone(oneLessonSyllabus)
			renamed.Milestones[0].Lessons[0].Title = "Another answer"
			revise(t, b, renamed)
		}, "rewrote Lesson answer"},
		"skipped, then completed": {func(t *testing.T, a, b *machine) {
			skipped := clone(more)
			skipped.Milestones[0].Lessons[0].Skipped = true
			revise(t, a, skipped)
			completeAnswer(t, b)
		}, "completed although a Revision skipped it"},
	} {
		t.Run(name, func(t *testing.T) {
			a, b := twoMachines(t, oneLessonSyllabus)
			tc.play(t, a, b)
			onA, onB := merge(t, a, b)
			if !mentions(onA, tc.want) || !mentions(onB, tc.want) {
				t.Errorf("want a flag about %q\nA: %q\nB: %q", tc.want, onA, onB)
			}
		})
	}
}

// TestOneRevisionApprovedOnTwoMachinesIsNoConflict: both machines made the
// same change, so there is nothing for the learner to settle.
func TestOneRevisionApprovedOnTwoMachinesIsNoConflict(t *testing.T) {
	ctx := context.Background()
	more := clone(twoMilestones)
	more.Milestones[1].Lessons = append(more.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
	a, b := twoMachines(t, twoMilestones)
	p, err := a.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add generics", Syllabus: more})
	if err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	git(t, filepath.Join(b.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(a.home, "c"), "main")
	for _, m := range []*machine{a, b} {
		if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
			t.Fatal(err)
		}
	}
	onA, onB := merge(t, a, b)
	if len(onA) != 0 || len(onB) != 0 {
		t.Errorf("one Revision approved on both machines is flagged:\nA: %q\nB: %q", onA, onB)
	}
	for _, m := range []*machine{a, b} {
		if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
			t.Errorf("flags = %+v, %v", topic.Flags, err)
		}
	}
}

// TestARevisionApprovedAgainChangesNothing: the same Revision approved on a
// machine that missed a later one leaves the later Syllabus in place.
func TestARevisionApprovedAgainChangesNothing(t *testing.T) {
	ctx := context.Background()
	a, b := twoMachines(t, twoMilestones)
	first := clone(twoMilestones)
	first.Milestones[1].Lessons = append(first.Milestones[1].Lessons, SyllabusLesson{ID: "generics", Title: "Generics"})
	p, err := a.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add generics", Syllabus: first})
	if err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	git(t, filepath.Join(b.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(a.home, "c"), "main")
	if _, err := a.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	later := clone(first)
	later.Milestones[0].Title = "Foundations"
	revise(t, a, later)
	if _, err := b.ApplyRevision(ctx, "c", p.Revision, Approval{Via: ViaChat, LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	merge(t, a, b)
	for name, m := range map[string]*machine{"A": a, "B": b} {
		topic, err := m.readTopic("c")
		if err != nil || len(topic.Flags) != 0 {
			t.Errorf("on %s: flags %+v, %v", name, topic.Flags, err)
		}
		if v, err := m.SyllabusOf(ctx, "c"); err != nil || v.Milestones[0].Title != "Foundations" {
			t.Errorf("on %s the later Revision was undone: %+v, %v", name, v.Milestones[0], err)
		}
	}
}

// TestTheSameChangeOnTwoMachinesIsNoConflict: both machines changed an item
// from one version to the same new version, so they agree.
func TestTheSameChangeOnTwoMachinesIsNoConflict(t *testing.T) {
	a, b := twoMachines(t, oneLessonSyllabus)
	for _, m := range []*machine{a, b} {
		m.update(t, TopicChanges{Title: ptr("C programming")})
	}
	onA, onB := merge(t, a, b)
	if len(onA) != 0 || len(onB) != 0 {
		t.Errorf("the same change made on both machines is flagged:\nA: %q\nB: %q", onA, onB)
	}
}
