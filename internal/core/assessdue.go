package core

import (
	"encoding/json"
	"fmt"
)

// NextAssessMilestone is the code of the NextAction a completion returns
// when it finishes its Milestone: the end-of-Milestone Assessment is next.
const NextAssessMilestone = "assess_milestone"

// MilestoneRef names a Milestone.
type MilestoneRef struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	Title  string `json:"title"`
}

// assessmentDue returns the Milestone whose end-of-Milestone Assessment is
// the next thing to do, or nil. It is the Milestone finished most recently,
// in replay order: every Lesson done or skipped, at least one done. It is
// due while no milestone Assessment for it was recorded after its last
// Lesson was completed, and nothing since shows the learner chose to move
// on: no Lesson outside it was started, and no Next step was recorded.
func (s *replayed) assessmentDue() *MilestoneRef {
	syl := s.study.syllabus
	if syl == nil {
		return nil
	}
	milestoneOf := map[string]int{}
	for i, m := range syl.Milestones {
		for _, l := range m.Lessons {
			milestoneOf[l.ID] = i
		}
	}
	lastDone, lastAssessed := map[int]int{}, map[int]int{}
	var phases []struct{ at, milestone int }
	for at, ev := range s.applied {
		var d struct {
			Lesson    string `json:"lesson"`
			Kind      string `json:"kind"`
			Milestone string `json:"milestone"`
		}
		switch ev.Type {
		case eventLessonCompleted, eventPhaseSet, eventAssessmentRecorded:
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
		default:
			continue
		}
		switch ev.Type {
		case eventLessonCompleted:
			if m, ok := milestoneOf[d.Lesson]; ok {
				lastDone[m] = at
			}
		case eventPhaseSet:
			if m, ok := milestoneOf[d.Lesson]; ok {
				phases = append(phases, struct{ at, milestone int }{at, m})
			}
		case eventAssessmentRecorded:
			if d.Kind != AssessmentMilestone {
				continue
			}
			for i, m := range syl.Milestones {
				if m.ID == d.Milestone {
					lastAssessed[i] = at
				}
			}
		}
	}
	due, dueAt := -1, -1
	for i, m := range syl.Milestones {
		at, ok := lastDone[i]
		if !ok || !s.study.milestoneSettled(m) || at < dueAt {
			continue
		}
		due, dueAt = i, at
	}
	if due < 0 {
		return nil
	}
	if at, ok := lastAssessed[due]; ok && at > dueAt {
		return nil
	}
	for _, p := range phases {
		if p.at > dueAt && p.milestone != due {
			return nil // a Lesson outside it started: the learner moved on
		}
	}
	if ns := s.study.nextStep; ns != nil && s.study.staleReason(ns.Lesson) == "" && s.study.nextStepSeq > dueAt {
		return nil // a Next step was recorded since: it leads
	}
	m := syl.Milestones[due]
	return &MilestoneRef{Number: due + 1, ID: m.ID, Title: m.Title}
}

// milestoneSettled reports whether every Lesson of m is done or skipped. A
// Milestone is a candidate only once one of its Lessons was completed, so a
// Milestone of skipped Lessons only is never assessed.
func (st *studyState) milestoneSettled(m Milestone) bool {
	for _, l := range m.Lessons {
		if ls := st.lessons[l.ID]; !l.Skipped && (ls == nil || ls.completed == nil) {
			return false
		}
	}
	return true
}

// milestoneOfLesson returns the Milestone that holds a Lesson.
func (s *Syllabus) milestoneOfLesson(lessonID string) (MilestoneRef, Milestone, bool) {
	for i, m := range s.Milestones {
		for _, l := range m.Lessons {
			if l.ID == lessonID {
				return MilestoneRef{Number: i + 1, ID: m.ID, Title: m.Title}, m, true
			}
		}
	}
	return MilestoneRef{}, Milestone{}, false
}

// finishesMilestone reports whether completing lessonID finishes m: every
// other Lesson of m is already done or skipped.
func (st *studyState) finishesMilestone(m Milestone, lessonID string) bool {
	for _, l := range m.Lessons {
		if l.ID == lessonID || l.Skipped {
			continue
		}
		if ls := st.lessons[l.ID]; ls == nil || ls.completed == nil {
			return false
		}
	}
	return true
}

// assessNext is the NextAction that sends the agent to a Milestone's
// Assessment.
func assessNext(m *MilestoneRef) *NextAction {
	return &NextAction{Code: NextAssessMilestone, Milestone: m,
		Text: fmt.Sprintf("Milestone %d “%s” is finished: assess it together with the learner, then record it "+
			"with assessment_record", m.Number, m.Title)}
}
