package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Event types of Sessions and Phases.
const (
	eventSessionOpened = "session.opened"
	eventSessionClosed = "session.closed"
	eventPhaseSet      = "phase.set"
)

// Energy levels, checked at the start of a Session.
const (
	EnergyFull  = "full"
	EnergyHalf  = "half"
	EnergyFumes = "fumes"
)

// Focuses: what a Session is for.
const (
	FocusLearn    = "learn"
	FocusPractice = "practice"
	FocusReviews  = "reviews"
	FocusExplore  = "explore"
)

// Phases: how far the current Lesson has got.
const (
	PhaseTeaching   = "teaching"
	PhasePracticing = "practicing"
	PhaseFeedback   = "feedback"
)

const (
	maxNextStepRunes = 300
	maxContextRunes  = 2000
)

// sessionState is one Session.
type sessionState struct {
	id     string
	energy string
	focus  string
	opened time.Time
	closed bool
}

// NextStep is the concrete action, starting with a verb, shown first when
// the learner comes back, with the context needed to take it.
type NextStep struct {
	Step    string    `json:"step"`
	Context string    `json:"context,omitempty"`
	Lesson  string    `json:"lesson,omitempty"`
	At      time.Time `json:"at"`
}

// sessionOpenedData is the payload of a session.opened Event. The Session's
// ID is the Event's ID.
type sessionOpenedData struct {
	Energy string `json:"energy,omitempty"`
	Focus  string `json:"focus,omitempty"`
}

// sessionClosedData is the payload of a session.closed Event.
type sessionClosedData struct {
	Session  string `json:"session"`
	NextStep string `json:"next_step"`
	Context  string `json:"context,omitempty"`
}

// phaseSetData is the payload of a phase.set Event.
type phaseSetData struct {
	Lesson string `json:"lesson"`
	Phase  string `json:"phase"`
	// NextStep, when set, is the Next step for the new Phase, such as the
	// fix a failed Attempt calls for.
	NextStep string `json:"next_step,omitempty"`
	// CheckVersion is the version of the Check shown to the learner when
	// practicing starts.
	CheckVersion string `json:"check_version,omitempty"`
}

// SessionSpec describes a Session to open.
type SessionSpec struct {
	// Energy is full, half or fumes. Optional.
	Energy string
	// Focus is learn, practice, reviews or explore. Optional.
	Focus  string
	DryRun bool
}

// SessionOpened is the result of OpenSession.
type SessionOpened struct {
	Topic   string `json:"topic"`
	Session string `json:"session"`
	// Resume is where the learner stopped, to show first.
	Resume ResumePoint `json:"resume"`
	// Unclosed is the previous Session when it ended without a Next step,
	// for instance because the terminal was closed: ask the learner for
	// the missing note and look at what changed since the last Checkpoint.
	Unclosed *SessionInfo `json:"unclosed,omitempty"`
	DryRun   bool         `json:"dry_run,omitempty"`
}

// SessionInfo describes a Session.
type SessionInfo struct {
	ID     string    `json:"id"`
	Opened time.Time `json:"opened"`
	Energy string    `json:"energy,omitempty"`
	Focus  string    `json:"focus,omitempty"`
}

// SessionClosed is the result of CloseSession.
type SessionClosed struct {
	Topic    string   `json:"topic"`
	Session  string   `json:"session"`
	NextStep NextStep `json:"next_step"`
	DryRun   bool     `json:"dry_run,omitempty"`
}

// OpenSession opens a Session on a Topic, which also makes it the most
// recent Topic, and returns where the learner stopped.
func (c *Core) OpenSession(ctx context.Context, topicID string, spec SessionSpec) (SessionOpened, error) {
	switch spec.Energy {
	case "", EnergyFull, EnergyHalf, EnergyFumes:
	default:
		return SessionOpened{}, invalidf("energy must be full, half or fumes, not %q", spec.Energy)
	}
	switch spec.Focus {
	case "", FocusLearn, FocusPractice, FocusReviews, FocusExplore:
	default:
		return SessionOpened{}, invalidf("focus must be learn, practice, reviews or explore, not %q", spec.Focus)
	}
	result := SessionOpened{Topic: topicID, DryRun: spec.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		result.Resume = s.study.resume()
		if last := s.study.lastSession(); last != nil && !last.closed {
			result.Unclosed = last.info()
		}
		return &change{Type: eventSessionOpened, Data: sessionOpenedData{Energy: spec.Energy, Focus: spec.Focus}}, nil
	}, spec.DryRun)
	if err != nil {
		return SessionOpened{}, err
	}
	result.Session = ev.ID
	return result, nil
}

// CloseSession closes the open Session with a Next step, starting with a
// verb, and free-text context.
func (c *Core) CloseSession(ctx context.Context, topicID, nextStep, notes string, dryRun bool) (SessionClosed, error) {
	step, err := requiredText("Next step", nextStep, maxNextStepRunes)
	if err != nil {
		return SessionClosed{}, err
	}
	note, err := cleanTextBlock("context", notes, maxContextRunes)
	if err != nil {
		return SessionClosed{}, err
	}
	result := SessionClosed{Topic: topicID, DryRun: dryRun}
	_, err = c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		last := s.study.lastSession()
		if last == nil || last.closed {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "no Session is open in " + topicID + ": open one with session_open"}
		}
		result.Session = last.id
		result.NextStep = NextStep{Step: step, Context: note, Lesson: s.study.currentLesson(), At: c.now().UTC().Truncate(clockTick)}
		return &change{Type: eventSessionClosed, Data: sessionClosedData{Session: last.id, NextStep: step, Context: note}}, nil
	}, dryRun)
	if err != nil {
		return SessionClosed{}, err
	}
	return result, nil
}

// PhaseSpec describes a change of Phase.
type PhaseSpec struct {
	Lesson string
	// Phase is teaching, practicing or feedback.
	Phase string
	// NextStep, optional, is the Next step for the new Phase.
	NextStep string
	DryRun   bool
}

// PhaseResult is the result of SetPhase.
type PhaseResult struct {
	Topic  string `json:"topic"`
	Lesson string `json:"lesson"`
	Phase  string `json:"phase"`
	// Changed is false when the Lesson already was in that Phase.
	Changed bool `json:"changed"`
	// Checkpoint is the Checkpoint taken because the turn passed between
	// the agent and the learner.
	Checkpoint *CheckpointResult `json:"checkpoint,omitempty"`
	// CheckpointError explains a Checkpoint that could not be taken. The
	// Phase is recorded anyway.
	CheckpointError string `json:"checkpoint_error,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
}

// turnOwner is whose turn a Phase is: the learner's while practicing, the
// agent's otherwise.
func turnOwner(phase string) string {
	if phase == PhasePracticing {
		return "learner"
	}
	return "agent"
}

// SetPhase moves a Lesson to a Phase: teaching, practicing or feedback.
// Practicing needs the Lesson's Check, which is shown to the learner first;
// its version is recorded. When the turn passes between the agent and the
// learner, a Checkpoint saves the work of the one whose turn ended.
func (c *Core) SetPhase(ctx context.Context, topicID string, spec PhaseSpec) (PhaseResult, error) {
	switch spec.Phase {
	case PhaseTeaching, PhasePracticing, PhaseFeedback:
	default:
		return PhaseResult{}, invalidf("phase must be teaching, practicing or feedback, not %q", spec.Phase)
	}
	step, err := cleanText("Next step", spec.NextStep, maxNextStepRunes)
	if err != nil {
		return PhaseResult{}, err
	}
	result := PhaseResult{Topic: topicID, Lesson: spec.Lesson, Phase: spec.Phase, DryRun: spec.DryRun}
	previous := ""
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if _, err := requireLesson(s, topicID, spec.Lesson); err != nil {
			return nil, err
		}
		ls := s.study.lessons[spec.Lesson]
		if ls != nil && ls.completed != nil {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + spec.Lesson + " is done"}
		}
		if ls != nil {
			previous = ls.phase
		}
		if previous == spec.Phase && step == "" {
			return nil, nil
		}
		d := phaseSetData{Lesson: spec.Lesson, Phase: spec.Phase, NextStep: step}
		if spec.Phase == PhasePracticing {
			_, version, err := readCheck(view.root, spec.Lesson)
			if err != nil {
				return nil, err
			}
			d.CheckVersion = version
		}
		return &change{Type: eventPhaseSet, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return PhaseResult{}, err
	}
	result.Changed = ev != nil
	before := turnOwner(previous)
	if previous == "" {
		before = "agent" // a Session starts with the agent's turn
	}
	if ev == nil || spec.DryRun || before == turnOwner(spec.Phase) {
		return result, nil
	}
	cp, err := c.Checkpoint(ctx, CheckpointSpec{Topic: topicID, Role: before,
		Message: fmt.Sprintf("%s: %s", spec.Lesson, spec.Phase)})
	if err != nil {
		result.CheckpointError = err.Error()
		c.log.Warn("could not take a Checkpoint at a turn switch", "topic", topicID, "err", err)
		return result, nil
	}
	result.Checkpoint = &cp
	return result, nil
}

func replaySessionOpened(s *replayed, ev event) error {
	var d sessionOpenedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	s.study.sessions = append(s.study.sessions, &sessionState{id: ev.ID, energy: d.Energy, focus: d.Focus, opened: wallOf(ev)})
	return nil
}

func replaySessionClosed(s *replayed, ev event) error {
	var d sessionClosedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	var session *sessionState
	for _, ss := range s.study.sessions {
		if ss.id == d.Session {
			session = ss
		}
	}
	if session == nil {
		return fmt.Errorf("%w: Session %s", errUnknownItem, d.Session)
	}
	session.closed = true
	s.study.nextStep = &NextStep{Step: d.NextStep, Context: d.Context, Lesson: s.study.currentLesson(), At: wallOf(ev)}
	return nil
}

func replayPhaseSet(s *replayed, ev event) error {
	var d phaseSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	l := s.study.lesson(d.Lesson)
	l.phase = d.Phase
	if d.NextStep != "" {
		s.study.nextStep = &NextStep{Step: d.NextStep, Lesson: d.Lesson, At: wallOf(ev)}
	}
	if d.CheckVersion != "" {
		s.recordVersion(checkItem(d.Lesson), d.CheckVersion, ev.ID)
	}
	return nil
}

// recordVersion notes the version of a gating item that an Event relied on
// without changing it, such as the Check shown when practicing starts, so
// status can flag later edits made outside Lamplight.
func (s *replayed) recordVersion(item, hash, eventID string) {
	s.versions[item] = version{hash: hash, event: eventID}
}

func (st *studyState) lastSession() *sessionState {
	if len(st.sessions) == 0 {
		return nil
	}
	return st.sessions[len(st.sessions)-1]
}

func (ss *sessionState) info() *SessionInfo {
	return &SessionInfo{ID: ss.id, Opened: ss.opened, Energy: ss.energy, Focus: ss.focus}
}

// currentLesson is the Lesson the learner is on: the first in Syllabus order
// that is not done. It is empty without a Syllabus or once every Lesson is
// done.
func (st *studyState) currentLesson() string {
	if st.syllabus == nil {
		return ""
	}
	for _, l := range st.syllabus.lessons() {
		if ls := st.lessons[l.ID]; ls == nil || ls.completed == nil {
			return l.ID
		}
	}
	return ""
}

// ResumePoint is where the learner stopped: the current Lesson and its
// Phase, the Next step word for word, and any Session left open.
type ResumePoint struct {
	Lesson      string    `json:"lesson,omitempty"`
	LessonTitle string    `json:"lesson_title,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	NextStep    *NextStep `json:"next_step,omitempty"`
	// OpenSession is a Session not closed yet: in progress, or ended
	// without a Next step.
	OpenSession *SessionInfo `json:"open_session,omitempty"`
	// SyllabusDone is true when every Lesson in the Syllabus is done.
	SyllabusDone bool `json:"syllabus_done,omitempty"`
}

func (st *studyState) resume() ResumePoint {
	var r ResumePoint
	if id := st.currentLesson(); id != "" {
		l, _ := st.syllabus.lesson(id)
		r.Lesson, r.LessonTitle = l.ID, l.Title
		if ls := st.lessons[id]; ls != nil {
			r.Phase = ls.phase
		}
	} else if st.syllabus != nil {
		r.SyllabusDone = true
	}
	r.NextStep = st.nextStep
	if last := st.lastSession(); last != nil && !last.closed {
		r.OpenSession = last.info()
	}
	return r
}

// empty reports whether there is nothing to resume yet: no Syllabus and no
// Session.
func (r ResumePoint) empty() bool {
	return r.Lesson == "" && !r.SyllabusDone && r.NextStep == nil && r.OpenSession == nil
}
