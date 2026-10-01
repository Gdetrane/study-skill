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
	lessons   map[string]*lessonState
	// sessions are the Sessions opened, in order.
	sessions []*sessionState
	// nextStep is the latest Next step recorded, cleared when its Lesson
	// is completed.
	nextStep *NextStep
	cards    map[string]*cardState
	// cardOrder lists Card IDs in the order they were created.
	cardOrder []string
}

func newStudyState() studyState {
	return studyState{
		proposals: map[string]revisionProposedData{},
		applied:   map[string]bool{},
		lessons:   map[string]*lessonState{},
		cards:     map[string]*cardState{},
	}
}

// lessonState is one Lesson's progress.
type lessonState struct {
	// phase is the Lesson's Phase: teaching, practicing or feedback.
	phase    string
	attempts []Attempt
	// completed is the completion, once the Lesson is done.
	completed *lessonCompletedData
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
