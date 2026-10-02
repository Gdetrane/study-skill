package core

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Kinds of change a Revision makes to the Syllabus.
const (
	ChangeFileAdopted      = "file_adopted"
	ChangeSettings         = "settings_changed"
	ChangeFirstSyllabus    = "first_syllabus"
	ChangeMilestoneAdded   = "milestone_added"
	ChangeMilestoneRemoved = "milestone_removed"
	ChangeMilestoneEdited  = "milestone_changed"
	ChangeLessonAdded      = "lesson_added"
	ChangeLessonRemoved    = "lesson_removed"
	ChangeLessonRenamed    = "lesson_renamed"
	ChangeLessonMoved      = "lesson_moved"
	ChangeLessonHours      = "lesson_hours"
	ChangeLessonSkipped    = "lesson_skipped"
	ChangeLessonUnskipped  = "lesson_unskipped"
	ChangeLessonsReordered = "lessons_reordered"
)

// RevisionChanges is what a Revision changes in the Syllabus, computed from
// the current Syllabus and the proposed one, so the learner sees a
// before/after that names Lessons by title and shows any renumbering.
type RevisionChanges struct {
	Changes []SyllabusChange `json:"changes"`
	// Renumbered lists the Lessons whose display number changes.
	Renumbered []Renumbering `json:"renumbered"`
	// SkippedInProgress lists Lessons the Revision skips while they are in
	// progress: offer the learner Cards for what was already covered.
	SkippedInProgress []string `json:"skipped_in_progress,omitempty"`
	// Text is the whole change in plain words, one line each.
	Text string `json:"text"`
}

// SyllabusChange is one change, in plain words in Text.
type SyllabusChange struct {
	Kind      string `json:"kind"`
	Milestone string `json:"milestone,omitempty"`
	Lesson    string `json:"lesson,omitempty"`
	Text      string `json:"text"`
}

// Renumbering is a Lesson whose display number changes.
type Renumbering struct {
	Lesson string `json:"lesson"`
	Title  string `json:"title"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// numbered is a Lesson with its display number and Milestone.
type numbered struct {
	lesson    SyllabusLesson
	number    string
	milestone Milestone
	mnumber   int
}

func numberLessons(s *Syllabus) (map[string]numbered, []string) {
	out := map[string]numbered{}
	var order []string
	if s == nil {
		return out, nil
	}
	for i, m := range s.Milestones {
		for j, l := range m.Lessons {
			out[l.ID] = numbered{lesson: l, number: fmt.Sprintf("%d.%d", i+1, j+1), milestone: m, mnumber: i + 1}
			order = append(order, l.ID)
		}
	}
	return out, order
}

// revisionChanges compares the replayed Syllabus with a proposed one;
// fromFile marks a Revision that adopts a hand edit of syllabus.toml. A
// Revision that changes nothing has no changes, unless it adopts the file.
func revisionChanges(s *replayed, next Syllabus, fromFile bool) RevisionChanges {
	out := RevisionChanges{Changes: []SyllabusChange{}, Renumbered: []Renumbering{}}
	add := func(kind, milestone, lesson, format string, args ...any) {
		out.Changes = append(out.Changes, SyllabusChange{Kind: kind, Milestone: milestone, Lesson: lesson,
			Text: fmt.Sprintf(format, args...)})
	}
	if fromFile {
		add(ChangeFileAdopted, "", "", "Adopts %s as edited by hand", syllabusFile)
	}
	current := s.study.syllabus
	if current == nil {
		lessons := 0
		for _, m := range next.Milestones {
			lessons += len(m.Lessons)
		}
		add(ChangeFirstSyllabus, "", "", "Sets the first Syllabus: %s and %s", count(len(next.Milestones), "Milestone"), count(lessons, "Lesson"))
		for i, m := range next.Milestones {
			add(ChangeMilestoneAdded, m.ID, "", "Milestone %d %q%s", i+1, m.Title, milestoneDetails(m))
			for j, l := range m.Lessons {
				add(ChangeLessonAdded, m.ID, l.ID, "  Lesson %d.%d %q%s", i+1, j+1, l.Title, hours(l.Hours))
			}
		}
		out.Text = joinChanges(out.Changes)
		return out
	}

	before, _ := numberLessons(current)
	after, order := numberLessons(&next)
	oldMilestones := map[string]int{}
	for i, m := range current.Milestones {
		oldMilestones[m.ID] = i
	}
	newMilestones := map[string]bool{}
	for i, m := range next.Milestones {
		newMilestones[m.ID] = true
		oi, ok := oldMilestones[m.ID]
		if !ok {
			add(ChangeMilestoneAdded, m.ID, "", "Adds Milestone %d %q%s", i+1, m.Title, milestoneDetails(m))
			continue
		}
		om := current.Milestones[oi]
		var edits []string
		if om.Title != m.Title {
			edits = append(edits, fmt.Sprintf("renamed to %q", m.Title))
		}
		if om.Outcome != m.Outcome {
			edits = append(edits, fmt.Sprintf("outcome %q", m.Outcome))
		}
		if om.Priority != m.Priority {
			edits = append(edits, fmt.Sprintf("priority %s instead of %s", priorityWords(m.Priority), priorityWords(om.Priority)))
		}
		if om.Target != m.Target {
			switch {
			case m.Target == "":
				edits = append(edits, "no target date")
			case om.Target == "":
				edits = append(edits, "target date "+m.Target)
			default:
				edits = append(edits, fmt.Sprintf("target date %s instead of %s", m.Target, om.Target))
			}
		}
		if oi != i {
			edits = append(edits, fmt.Sprintf("now Milestone %d", i+1))
		}
		if len(edits) > 0 {
			add(ChangeMilestoneEdited, m.ID, "", "Changes Milestone %d %q: %s", oi+1, om.Title, strings.Join(edits, "; "))
		}
	}
	for i, m := range current.Milestones {
		if !newMilestones[m.ID] {
			add(ChangeMilestoneRemoved, m.ID, "", "Removes Milestone %d %q", i+1, m.Title)
		}
	}

	for _, id := range order {
		n := after[id]
		o, existed := before[id]
		if !existed {
			add(ChangeLessonAdded, n.milestone.ID, id, "Adds Lesson %s %q to Milestone %d %q%s",
				n.number, n.lesson.Title, n.mnumber, n.milestone.Title, hours(n.lesson.Hours))
			continue
		}
		if o.lesson.Title != n.lesson.Title {
			add(ChangeLessonRenamed, n.milestone.ID, id, "Renames Lesson %s %q to %q", o.number, o.lesson.Title, n.lesson.Title)
		}
		if o.milestone.ID != n.milestone.ID {
			add(ChangeLessonMoved, n.milestone.ID, id, "Moves Lesson %q from Milestone %d %q to Milestone %d %q",
				n.lesson.Title, o.mnumber, o.milestone.Title, n.mnumber, n.milestone.Title)
		}
		if o.lesson.Hours != n.lesson.Hours {
			add(ChangeLessonHours, n.milestone.ID, id, "Changes the estimate of Lesson %q from %s to %s",
				n.lesson.Title, hoursText(o.lesson.Hours), hoursText(n.lesson.Hours))
		}
		switch {
		case n.lesson.Skipped && !o.lesson.Skipped:
			text := fmt.Sprintf("Skips Lesson %s %q", n.number, n.lesson.Title)
			if ls := s.study.lessons[id]; ls != nil && ls.completed == nil && (ls.phase != "" || len(ls.attempts) > 0) {
				// TODO(#26): Cards for what was already covered, through
				// card_add, which accepts a skipped Lesson.
				out.SkippedInProgress = append(out.SkippedInProgress, id)
				text += ", which was in progress"
			}
			add(ChangeLessonSkipped, n.milestone.ID, id, "%s", text)
		case !n.lesson.Skipped && o.lesson.Skipped:
			add(ChangeLessonUnskipped, n.milestone.ID, id, "Takes back the skip of Lesson %s %q", n.number, n.lesson.Title)
		}
		if o.number != n.number {
			out.Renumbered = append(out.Renumbered, Renumbering{Lesson: id, Title: n.lesson.Title, From: o.number, To: n.number})
		}
	}
	// Lessons that stay in a Milestone but change places within it: no
	// change above names that, and it is what the learner approves.
	for i, m := range next.Milestones {
		oi, ok := oldMilestones[m.ID]
		if !ok {
			continue
		}
		var was, now, titles []string
		for _, l := range current.Milestones[oi].Lessons {
			if n, kept := after[l.ID]; kept && n.milestone.ID == m.ID {
				was = append(was, l.ID)
			}
		}
		for _, l := range m.Lessons {
			if o, existed := before[l.ID]; existed && o.milestone.ID == m.ID {
				now = append(now, l.ID)
				titles = append(titles, strconv.Quote(l.Title))
			}
		}
		if !slices.Equal(was, now) {
			add(ChangeLessonsReordered, m.ID, "", "Reorders the Lessons of Milestone %d %q: now %s", i+1, m.Title, strings.Join(titles, ", "))
		}
	}
	_, oldOrder := numberLessons(current)
	for _, id := range oldOrder {
		if _, kept := after[id]; !kept {
			o := before[id]
			add(ChangeLessonRemoved, o.milestone.ID, id, "Removes Lesson %s %q", o.number, o.lesson.Title)
		}
	}
	if visible := len(out.Changes) > 0 && !(fromFile && len(out.Changes) == 1); !visible {
		a, errA := encodeSyllabus(*current)
		b, errB := encodeSyllabus(next)
		if errA == nil && errB == nil && !bytes.Equal(a, b) {
			add(ChangeSettings, "", "", "Changes settings Lamplight does not know")
		}
	}
	out.Text = joinChanges(out.Changes)
	if len(out.Renumbered) > 0 {
		var parts []string
		for _, r := range out.Renumbered {
			parts = append(parts, fmt.Sprintf("%q %s → %s", r.Title, r.From, r.To))
		}
		out.Text += "\nRenumbers: " + strings.Join(parts, ", ")
	}
	return out
}

func joinChanges(changes []SyllabusChange) string {
	lines := make([]string, len(changes))
	for i, c := range changes {
		lines[i] = c.Text
	}
	return strings.Join(lines, "\n")
}

func milestoneDetails(m Milestone) string {
	parts := []string{priorityWords(m.Priority)}
	if m.Target != "" {
		parts = append(parts, "by "+m.Target)
	}
	if m.Outcome != "" {
		parts = append(parts, "outcome: "+m.Outcome)
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func priorityWords(p string) string {
	switch p {
	case PriorityIfTime:
		return "if time allows"
	case PriorityAfterDeadline:
		return "after the deadline"
	}
	return "must"
}

func hours(h float64) string {
	if h == 0 {
		return ""
	}
	return ", " + hoursText(h)
}

func hoursText(h float64) string {
	if h == 0 {
		return "no estimate"
	}
	return strconv.FormatFloat(h, 'f', -1, 64) + " h"
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
