package core

import (
	"os"
	"path/filepath"
)

// learnerFile is the Learner profile in the Study home, and a Topic's
// additions to it in the Topic's folder. The skill reads both at the start
// of every Session; Lamplight only points to them.
const learnerFile = "learner.md"

// Actions that status recommends.
const (
	ActionNextStep = "next_step"
	ActionPlan     = "plan"
	ActionLearn    = "learn"
	ActionPractice = "practice"
	ActionFeedback = "feedback"
	ActionReview   = "review"
	ActionExplore  = "explore"
)

// Recommendation is the one action status recommends for the Active topic.
type Recommendation struct {
	Topic string `json:"topic"`
	// Action is next_step, plan, learn, practice, feedback, review or
	// explore.
	Action string `json:"action"`
	// Text says what to do, for the learner; for next_step it is the Next
	// step word for word.
	Text string `json:"text"`
}

// recommend picks the one action to take next on a Topic: its Next step
// when there is one, otherwise the next move in its Syllabus, otherwise
// Reviews or exploring once every Lesson is done. It never counts anything.
func recommend(t Topic) *Recommendation {
	r := &Recommendation{Topic: t.ID}
	resume := t.Resume
	ready := t.Cards != nil && t.Cards.Ready
	lesson := ""
	if resume != nil {
		lesson = "Lesson “" + resume.LessonTitle + "”"
	}
	switch {
	case resume != nil && resume.NextStep != nil:
		r.Action, r.Text = ActionNextStep, resume.NextStep.Step
	case resume == nil || resume.Lesson == "" && !resume.SyllabusDone:
		r.Action, r.Text = ActionPlan, "Plan the Syllabus together, and approve it"
	case resume.SyllabusDone && ready:
		r.Action, r.Text = ActionReview, "Review the Cards that are ready"
	case resume.SyllabusDone:
		r.Action, r.Text = ActionExplore, "Every Lesson is done: decide together what comes next"
	case resume.Phase == PhasePracticing:
		r.Action, r.Text = ActionPractice, "Continue practicing "+lesson
	case resume.Phase == PhaseFeedback:
		r.Action, r.Text = ActionFeedback, "Go over the feedback on "+lesson
	case resume.Phase == PhaseTeaching:
		r.Action, r.Text = ActionLearn, "Continue "+lesson
	default:
		r.Action, r.Text = ActionLearn, "Start "+lesson
	}
	return r
}

// addTopicGuidance fills in what status shows of a Topic beyond its
// settings: whether Cards are ready and where its Learner profile additions
// are.
func (c *Core) addTopicGuidance(topic *os.Root, s *replayed, t *Topic) {
	t.Cards = s.study.cardsReady(c.now())
	if isRegularFile(topic, learnerFile) {
		t.LearnerAdditions = filepath.Join(t.Path, learnerFile)
	}
}

// addGuidance fills in what status shows beyond the Topics: the Learner
// profile and the one recommended action for the Active topic.
func (c *Core) addGuidance(home *os.Root, status *Status) {
	if isRegularFile(home, learnerFile) {
		status.LearnerProfile = filepath.Join(c.home, learnerFile)
	}
	if status.ActiveTopic == nil {
		return
	}
	for _, t := range status.Topics {
		if t.ID == status.ActiveTopic.ID {
			status.Recommended = recommend(t)
		}
	}
}

// isRegularFile reports whether name in root is a regular file, never
// following a symbolic link.
func isRegularFile(root *os.Root, name string) bool {
	info, err := root.Lstat(name)
	return err == nil && info.Mode().IsRegular()
}
