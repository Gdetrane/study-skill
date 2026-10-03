package core

import (
	"context"
	"path/filepath"
	"regexp"
	"time"
)

// studyState is what replaying a Topic's learning Events yields: the
// Syllabus, each Lesson's progress, Sessions, the Next step and Cards.
// Status is never stored; it is computed here from the History (ADR-0005).
type studyState struct {
	// syllabus is the Syllabus of the last Revision applied; nil before
	// the first.
	syllabus *Syllabus
	// proposals are proposed Revisions by ID, and applied the IDs of those
	// applied.
	proposals map[string]revisionProposedData
	applied   map[string]bool
	// declined holds the Revisions the learner declined, and declinedBy
	// the Event that declined each.
	declined   map[string]bool
	declinedBy map[string]string
	// appliedWith and declinedWith are how the learner answered each.
	appliedWith  map[string]Approval
	declinedWith map[string]Approval
	lessons      map[string]*lessonState
	// sessions are the Sessions opened, in order.
	sessions []*sessionState
	// nextStep is the latest Next step recorded, cleared when its Lesson
	// is completed, and nextStepSeq where in replay order it was recorded.
	nextStep    *NextStep
	nextStepSeq int
	cards       map[string]*cardState
	// cardOrder lists Card IDs in the order they were created.
	cardOrder []string
	// numbers caches the Cards' display numbers; see cardNumber.
	numbers map[string]int
	// requests maps the request ids clients gave Reviews to those Reviews,
	// so a retry records nothing.
	requests map[string]reviewRecordedData
	// turn is whose turn it is, across all Lessons: the learner's while a
	// Lesson is practicing, the agent's otherwise. Empty means the agent's.
	turn string
	// owed is the Checkpoint a turn switch or a completion called for and
	// that has not been taken yet, if any. A later one replaces it: one
	// Checkpoint then covers both.
	owed *owedCheckpoint
}

// owedCheckpoint is a Checkpoint the History calls for: the Event that
// called for it, the role of the turn that ended, and the message.
type owedCheckpoint struct {
	event   string
	role    string
	message string
}

// currentTurn is whose turn it is: "agent" or "learner".
func (st *studyState) currentTurn() string {
	if st.turn == "" {
		return "agent"
	}
	return st.turn
}

func newStudyState() studyState {
	return studyState{
		proposals:    map[string]revisionProposedData{},
		applied:      map[string]bool{},
		declined:     map[string]bool{},
		declinedBy:   map[string]string{},
		appliedWith:  map[string]Approval{},
		declinedWith: map[string]Approval{},
		lessons:      map[string]*lessonState{},
		cards:        map[string]*cardState{},
		requests:     map[string]reviewRecordedData{},
	}
}

// lessonState is one Lesson's progress.
type lessonState struct {
	// phase is the Lesson's Phase: teaching, practicing or feedback.
	phase string
	// breakPoint is the id of the last Break point reached in it.
	breakPoint string
	// shownCheck is the version of the Check shown to the learner when
	// practicing last started: the one completion counts.
	shownCheck string
	attempts   []Attempt
	// completed is the completion, once the Lesson is done, and
	// completedBy the Event that recorded it.
	completed   *lessonCompletedData
	completedBy string
}

func (st *studyState) lesson(id string) *lessonState {
	l := st.lessons[id]
	if l == nil {
		l = &lessonState{}
		st.lessons[id] = l
	}
	return l
}

// replayTopic replays a Topic's History without writing anything, for
// reads. It also returns the Topic's folder path.
func (c *Core) replayTopic(ctx context.Context, topicID string) (*replayed, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return nil, "", err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return nil, "", err
	}
	return replayHistory(h), c.topicDir(topicID), nil
}

func (c *Core) topicDir(topicID string) string { return filepath.Join(c.home, topicID) }

// requireLesson returns a Lesson of the Topic's Syllabus, or an error saying
// what is missing.
func requireLesson(s *replayed, topicID, lessonID string) (SyllabusLesson, error) {
	if err := validateEntityID("Lesson", lessonID); err != nil {
		return SyllabusLesson{}, err
	}
	if s.study.syllabus == nil {
		return SyllabusLesson{}, &Error{Code: CodeFailedPrecondition, Message: "Topic " + topicID +
			" has no Syllabus yet: propose one with revision_propose and apply it once the learner approves"}
	}
	l, ok := s.study.syllabus.lesson(lessonID)
	if !ok {
		return SyllabusLesson{}, &Error{Code: CodeNotFound, Message: "the Syllabus of " + topicID + " has no Lesson " + lessonID}
	}
	return l, nil
}

// requireStudiedLesson is requireLesson for studying a Lesson: one the
// learner skipped is not studied until a Revision takes the skip back.
func requireStudiedLesson(s *replayed, topicID, lessonID string) (SyllabusLesson, error) {
	l, err := requireLesson(s, topicID, lessonID)
	if err == nil && l.Skipped {
		return l, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + lessonID + " of " + topicID +
			" was skipped through a Revision; to study it, propose a Revision that takes the skip back"}
	}
	return l, err
}

var entityIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validateEntityID checks the ID of a Milestone, Lesson or Check criterion:
// a short slug that never changes, also used in file names.
func validateEntityID(kind, id string) error {
	if len(id) > 64 || !entityIDPattern.MatchString(id) {
		return invalidf("%q is not a valid %s id: use lowercase letters, digits and single hyphens, up to 64 characters", id, kind)
	}
	return nil
}

// requiredText is cleanText for a field that must not be empty.
func requiredText(field, s string, maxRunes int) (string, error) {
	s, err := cleanText(field, s, maxRunes)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", invalidf("the %s is empty", field)
	}
	return s, nil
}

// wallOf is the real time an Event was written, which domain logic such as
// Card scheduling uses.
func wallOf(ev event) time.Time { return ev.Wall }
