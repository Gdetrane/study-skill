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
	// TurnEnded is the role whose turn this Phase ended, "agent" or
	// "learner", when the turn passed; a Checkpoint is owed for it.
	TurnEnded string `json:"turn_ended,omitempty"`
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
	// the missing note, and record it with session_close naming that
	// Session.
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

// CloseSpec describes closing a Session.
type CloseSpec struct {
	// Session is the Session to close; the latest one when empty. Naming
	// an older Session gives it the note it never got, when it ended
	// without one.
	Session string
	// NextStep is the concrete next action, starting with a verb.
	NextStep string
	// Context is what the learner needs to know to take it.
	Context string
	DryRun  bool
}

// CloseSession closes a Session with a Next step, starting with a verb, and
// free-text context.
//
// TODO(#28): show what changed since the last Checkpoint when a Session was
// left unclosed.
func (c *Core) CloseSession(ctx context.Context, topicID string, spec CloseSpec) (SessionClosed, error) {
	step, err := requiredText("Next step", spec.NextStep, maxNextStepRunes)
	if err != nil {
		return SessionClosed{}, err
	}
	note, err := cleanTextBlock("context", spec.Context, maxContextRunes)
	if err != nil {
		return SessionClosed{}, err
	}
	result := SessionClosed{Topic: topicID, DryRun: spec.DryRun}
	_, err = c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		session := s.study.lastSession()
		if spec.Session != "" {
			session = s.study.session(spec.Session)
			if session == nil {
				return nil, &Error{Code: CodeNotFound, Message: "Topic " + topicID + " has no Session " + spec.Session}
			}
		}
		if session == nil || session.closed {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "no Session is open in " + topicID + ": open one with session_open"}
		}
		result.Session = session.id
		result.NextStep = NextStep{Step: step, Context: note, Lesson: s.study.currentLesson(), At: c.now().UTC().Truncate(clockTick)}
		return &change{Type: eventSessionClosed, Data: sessionClosedData{Session: session.id, NextStep: step, Context: note}}, nil
	}, spec.DryRun)
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
	// Changed is false when the Lesson already was in that Phase, with the
	// same Next step and Check: nothing was recorded.
	Changed bool `json:"changed"`
	TurnCheckpoint
	DryRun bool `json:"dry_run,omitempty"`
}

// TurnCheckpoint reports the Checkpoint a write took because the turn passed
// between the agent and the learner, or a Lesson was completed.
type TurnCheckpoint struct {
	Checkpoint *CheckpointResult `json:"checkpoint,omitempty"`
	// CheckpointError explains a Checkpoint that could not be taken; the
	// write itself is recorded anyway. The Checkpoint stays owed: once the
	// problem is fixed, call checkpoint with CheckpointRole, or the next
	// phase_set or lesson_complete takes it.
	CheckpointError string `json:"checkpoint_error,omitempty"`
	CheckpointRole  string `json:"checkpoint_role,omitempty"`
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
// its version is recorded, and it is the Check that completion counts. When
// the turn passes between the agent and the learner, whatever the Lesson, a
// Checkpoint is owed for the turn that ended, and taken. A Checkpoint owed
// earlier, because it failed or a crash interrupted it, is taken too.
// Asking for the Phase, Next step and Check the Lesson already has records
// nothing.
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
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if _, err := requireStudiedLesson(s, topicID, spec.Lesson); err != nil {
			return nil, err
		}
		ls := s.study.lessons[spec.Lesson]
		if ls != nil && ls.completed != nil {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + spec.Lesson + " is done"}
		}
		d := phaseSetData{Lesson: spec.Lesson, Phase: spec.Phase, NextStep: step}
		if spec.Phase == PhasePracticing {
			_, version, err := readCheck(view.root, spec.Lesson)
			if err != nil {
				return nil, err
			}
			d.CheckVersion = version
		}
		if ls != nil && ls.phase == spec.Phase && s.study.turn == turnOwner(spec.Phase) &&
			(step == "" || s.study.nextStep != nil && s.study.nextStep.Step == step && s.study.nextStep.Lesson == spec.Lesson) &&
			(spec.Phase != PhasePracticing || d.CheckVersion == ls.shownCheck) {
			return nil, nil
		}
		if before := s.study.currentTurn(); before != turnOwner(spec.Phase) {
			d.TurnEnded = before
		}
		return &change{Type: eventPhaseSet, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return PhaseResult{}, err
	}
	result.Changed = ev != nil
	if !spec.DryRun {
		result.TurnCheckpoint = c.takeOwedCheckpoint(ctx, topicID)
	}
	return result, nil
}

// takeOwedCheckpoint takes the Checkpoint the History says is owed, if any.
// Failing to take it does not fail the write that called for it: the result
// says why, and the Checkpoint stays owed.
func (c *Core) takeOwedCheckpoint(ctx context.Context, topicID string) TurnCheckpoint {
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil || s.study.owed == nil {
		return TurnCheckpoint{}
	}
	owed := s.study.owed
	cp, err := c.Checkpoint(ctx, CheckpointSpec{Topic: topicID, Role: owed.role, Message: owed.message})
	if err != nil {
		c.log.Warn("could not take an owed Checkpoint", "topic", topicID, "role", owed.role, "err", err)
		return TurnCheckpoint{CheckpointError: err.Error(), CheckpointRole: owed.role}
	}
	return TurnCheckpoint{Checkpoint: &cp}
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
	session := s.study.session(d.Session)
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
	s.study.turn = turnOwner(d.Phase)
	if d.TurnEnded != "" {
		s.study.owed = &owedCheckpoint{event: ev.ID, role: d.TurnEnded, message: d.Lesson + ": " + d.Phase}
	}
	if d.NextStep != "" {
		s.study.nextStep = &NextStep{Step: d.NextStep, Lesson: d.Lesson, At: wallOf(ev)}
	}
	if d.CheckVersion != "" {
		// Only showing the Check moves its gating baseline: a Check edited
		// afterwards stays flagged until it is shown again.
		l.shownCheck = d.CheckVersion
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

func (st *studyState) session(id string) *sessionState {
	for _, ss := range st.sessions {
		if ss.id == id {
			return ss
		}
	}
	return nil
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
		if l.Skipped {
			continue
		}
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
	// SyllabusDone is true when every Lesson in the Syllabus is done or
	// skipped.
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
