package core

import (
	"os"
	"path/filepath"
)

// learnerFile is the Learner profile in the Study home, and a Topic's
// additions to it in the Topic's folder. The skill reads both at the start
// of every Session; Lamplight only points to them.
const learnerFile = "learner.md"

// Actions that status recommends. The words match the Focuses where they
// mean the same thing (learn, practice, reviews, explore) and the
// suggestion plan.
const (
	ActionNextStep = "next_step"
	ActionPlan     = SuggestPlan
	ActionLearn    = FocusLearn
	ActionPractice = FocusPractice
	ActionFeedback = "feedback"
	ActionReviews  = FocusReviews
	ActionExplore  = FocusExplore
)

// FlagLessonHeader: the current Lesson's YAML header cannot be read, so its
// Break points are unknown. Fixing the header clears it.
const FlagLessonHeader = "lesson_header"

// Recommendation is the one action status recommends for the Active topic.
type Recommendation struct {
	Topic string `json:"topic"`
	// Action is next_step, plan, learn, practice, feedback, reviews or
	// explore: an enumeration skills can rely on.
	Action string `json:"action"`
	// Text says what to do in English prose, which the skill may rephrase;
	// for next_step it is the Next step word for word.
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
		r.Action, r.Text = ActionReviews, "Review the Cards that are ready"
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
// settings: whether Cards are ready, where its Learner profile additions
// are, and a flag when the current Lesson's Break points cannot be read.
func (c *Core) addTopicGuidance(topic *os.Root, s *replayed, t *Topic) {
	t.Cards = s.study.cardsReady(c.now())
	if isRegularFile(topic, learnerFile) {
		t.LearnerAdditions = filepath.Join(t.Path, learnerFile)
	}
	if r := t.Resume; r != nil && r.Lesson != "" {
		if _, err := readBreakPoints(topic, r.Lesson); err != nil {
			t.Flags = append(t.Flags, newFlag(FlagLessonHeader, lessonFile(r.Lesson), nil, "",
				err.Error()+"; fix the YAML header to see the Lesson's Break points"))
		}
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
// following a symbolic link: a learner.md that is a symlink is not pointed
// to, so status never sends an agent outside the Study home.
func isRegularFile(root *os.Root, name string) bool {
	info, err := root.Lstat(name)
	return err == nil && info.Mode().IsRegular()
}
