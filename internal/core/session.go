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
	// seq is the position of its opening in replay order.
	seq    int
	closed bool
	// closedBy is the Event that closed it, and note its Next step.
	closedBy string
	note     string
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
	// Lesson is the Lesson the Next step is about: the current Lesson when
	// it was written, so replay can tell a Next step that a merge made
	// stale.
	Lesson string `json:"lesson,omitempty"`
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
	// Unclosed are the Sessions that ended without a Next step, newest
	// first, for instance because the terminal was closed; a merge can
	// leave one from each machine. Show the learner Changes, ask for each
	// missing note, and record it with session_close naming the Session.
	Unclosed []SessionInfo `json:"unclosed,omitempty"`
	// Changes is what changed since the last Checkpoint, when a Session
	// was left unclosed.
	Changes *WorkChanges `json:"changes,omitempty"`
	// Suggested is what the Energy suggests, when an Energy is given and no
	// Focus was chosen yet. The learner chooses.
	Suggested *FocusSuggestion `json:"suggested,omitempty"`
	// Cards says whether Cards are ready to review, never how many.
	Cards *CardsReady `json:"cards,omitempty"`
	// LongGap is true when the Topic was last worked on more than a week
	// ago: start with a short recap of where the Topic stands and a
	// two-minute warm-up, never with the size of any backlog.
	LongGap bool `json:"long_gap,omitempty"`
	// Paused is set when the Topic is paused. The Session opens anyway,
	// and the Topic stays paused until the learner resumes it through
	// topic_update: offer to resume it or to pick another Topic.
	Paused bool `json:"paused,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
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
// recent Topic, and returns where the learner stopped: the Resume point, a
// Session left unclosed with what changed since the last Checkpoint, the
// Focus the Energy suggests, and whether Cards are ready.
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
	now := c.now()
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		result.Resume = s.study.resume()
		state := s.topicState()
		result.Paused = state == TopicPaused
		describeBreakPoint(view.root, &result.Resume)
		result.Cards = cardsReady(s, view, topicID, now)
		if open := s.study.unclosedSessions(); len(open) > 0 {
			result.Unclosed = open
			// Unclosed reports them, with what changed; this Session is
			// the open one now.
			result.Resume.OpenSession = nil
		}
		if last := latestActivity(s); !last.IsZero() {
			result.LongGap = now.Sub(last) > longGap
		}
		if spec.Focus == "" {
			result.Suggested = suggestFocus(spec.Energy, state, result.Resume, result.Cards)
		}
		return &change{Type: eventSessionOpened, Data: sessionOpenedData{Energy: spec.Energy, Focus: spec.Focus}}, nil
	}, spec.DryRun)
	if err != nil {
		return SessionOpened{}, err
	}
	result.Session = ev.ID
	if len(result.Unclosed) > 0 {
		result.Changes = c.workChanges(ctx, topicID)
	}
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
// free-text context. Naming an older Session that was left unclosed gives it
// the note it never got; OpenSession reports such a Session, with what
// changed since the last Checkpoint.
func (c *Core) CloseSession(ctx context.Context, topicID string, spec CloseSpec) (SessionClosed, error) {
	step, err := checkNextStep(spec.NextStep)
	if err != nil {
		return SessionClosed{}, err
	}
	note, err := cleanTextBlock("context", spec.Context, maxContextRunes)
	if err != nil {
		return SessionClosed{}, err
	}
	result := SessionClosed{Topic: topicID, DryRun: spec.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		session := s.study.lastSession()
		if spec.Session != "" {
			session = s.study.session(spec.Session)
			if session == nil {
				return nil, &Error{Code: CodeNotFound, Message: "Topic " + topicID + " has no Session " + spec.Session}
			}
		}
		switch {
		case session == nil:
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Topic " + topicID + " has no Session yet: open one first"}
		case session.closed:
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Session " + session.id + " of " + topicID +
				" is already closed; to give an older Session its missing note, name that Session"}
		}
		result.Session = session.id
		lesson := s.study.currentLesson()
		result.NextStep = NextStep{Step: step, Context: note, Lesson: lesson}
		return &change{Type: eventSessionClosed, Data: sessionClosedData{
			Session: session.id, NextStep: step, Context: note, Lesson: lesson,
		}}, nil
	}, spec.DryRun)
	if err != nil {
		return SessionClosed{}, err
	}
	result.NextStep.At = ev.Wall
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
	if step != "" {
		if step, err = checkNextStep(step); err != nil {
			return PhaseResult{}, err
		}
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
		// A failed Attempt sends the Lesson back to practicing with a Next
		// step that names the fix, whatever Phases it went through; once
		// one is recorded, no other is asked for until an Attempt fails
		// again.
		if spec.Phase == PhasePracticing && step == "" && ls != nil && ls.fixPending {
			return nil, invalidf("the last Attempt of %s failed, so going back to practicing needs a Next step that "+
				"names the fix, starting with a verb, such as \"Fix the off-by-one in count()\"", spec.Lesson)
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
	s.study.sessions = append(s.study.sessions, &sessionState{id: ev.ID, energy: d.Energy, focus: d.Focus,
		opened: wallOf(ev), seq: len(s.applied)})
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
	if session.closed {
		// Closed on two machines: the first note counts.
		if session.note != d.NextStep {
			s.flag(newFlag(FlagConflict, "", []string{session.closedBy, ev.ID}, "session "+d.Session,
				fmt.Sprintf("Session %s was closed twice with different Next steps, by Events %s and %s, probably on "+
					"two machines: the first counts (%q); check it with the learner", d.Session, session.closedBy, ev.ID,
					session.note)))
		}
		return nil
	}
	session.closed, session.closedBy, session.note = true, ev.ID, d.NextStep
	lesson := d.Lesson
	if lesson == "" {
		lesson = s.study.currentLesson()
	}
	step := &NextStep{Step: d.NextStep, Context: d.Context, Lesson: lesson, At: wallOf(ev)}
	if s.staleNextStep(ev, step) || s.study.superseded(session) {
		return nil
	}
	s.setNextStep(step)
	return nil
}

// setNextStep makes step the latest Next step, noting where in replay order
// it was recorded.
func (s *replayed) setNextStep(step *NextStep) {
	s.study.nextStep, s.study.nextStepSeq = step, len(s.applied)
	if ls := s.study.lessons[step.Lesson]; step.Lesson != "" && ls != nil {
		ls.fixPending = false // the Next step names what to do now
	}
}

// superseded reports whether a late note for session must leave the current
// Next step alone: the current one was recorded after a newer Session
// opened, so it is newer than anything the late note says.
func (st *studyState) superseded(session *sessionState) bool {
	if st.nextStep == nil {
		return false
	}
	for i, ss := range st.sessions {
		if ss == session && i+1 < len(st.sessions) {
			return st.nextStepSeq > st.sessions[i+1].seq
		}
	}
	return false
}

// staleReason says why a Next step for lessonID can no longer lead the
// Resume point: its Lesson is done, was skipped, or is no longer in the
// Syllabus. It is "" while the Lesson is still to study, and for a Next
// step about no Lesson.
func (st *studyState) staleReason(lessonID string) string {
	if lessonID == "" {
		return ""
	}
	if l := st.lessons[lessonID]; l != nil && l.completed != nil {
		return "is done"
	}
	if st.syllabus == nil {
		return ""
	}
	switch sl, ok := st.syllabus.lesson(lessonID); {
	case !ok:
		return "is no longer in the Syllabus"
	case sl.Skipped:
		return "was skipped"
	}
	return ""
}

// staleNextStep flags a Next step that arrives, through a merge, for a
// Lesson that is already done, skipped or removed, and reports it so the
// caller leaves the Resume point alone.
func (s *replayed) staleNextStep(ev event, step *NextStep) bool {
	why := s.study.staleReason(step.Lesson)
	if why == "" {
		return false
	}
	s.flag(newFlag(FlagConflict, lessonFile(step.Lesson), []string{ev.ID}, "next step",
		fmt.Sprintf("Event %s records the Next step %q for Lesson %s, which %s, probably on two machines: it is "+
			"not shown; record a new Next step, or dismiss this flag", ev.ID, step.Step, step.Lesson, why)))
	return true
}

func replayPhaseSet(s *replayed, ev event) error {
	var d phaseSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	l := s.study.lesson(d.Lesson)
	if d.Phase == PhaseFeedback && l.phase != PhaseFeedback {
		l.feedbackRounds++
	}
	l.phase = d.Phase
	s.study.turn = turnOwner(d.Phase)
	if d.TurnEnded != "" {
		s.study.owed = &owedCheckpoint{event: ev.ID, role: d.TurnEnded, message: d.Lesson + ": " + d.Phase}
	}
	if d.NextStep != "" {
		if step := (&NextStep{Step: d.NextStep, Lesson: d.Lesson, At: wallOf(ev)}); !s.staleNextStep(ev, step) {
			s.setNextStep(step)
		}
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

// ResumePoint is where the learner stopped: the current Lesson, its Phase
// and the last Break point reached in it, the Next step word for word, and
// any Session left open.
type ResumePoint struct {
	Lesson      string      `json:"lesson,omitempty"`
	LessonTitle string      `json:"lesson_title,omitempty"`
	Phase       string      `json:"phase,omitempty"`
	BreakPoint  *BreakPoint `json:"break_point,omitempty"`
	NextStep    *NextStep   `json:"next_step,omitempty"`
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
			if ls.breakPoint != "" {
				r.BreakPoint = &BreakPoint{ID: ls.breakPoint}
			}
		}
	} else if st.syllabus != nil {
		r.SyllabusDone = true
	}
	// A Next step whose Lesson is done, skipped or gone never leads, in
	// whatever order a merge left the Events.
	if ns := st.nextStep; ns != nil && st.staleReason(ns.Lesson) == "" {
		r.NextStep = ns
	}
	if open := st.unclosedSessions(); len(open) > 0 {
		r.OpenSession = &open[0]
	}
	return r
}

// unclosedSessions are the Sessions not closed yet, newest first: in
// progress, or ended without a Next step. A merge can leave several, one
// from each machine.
func (st *studyState) unclosedSessions() []SessionInfo {
	var out []SessionInfo
	for i := len(st.sessions) - 1; i >= 0; i-- {
		if ss := st.sessions[i]; !ss.closed {
			out = append(out, *ss.info())
		}
	}
	return out
}

// empty reports whether there is nothing to resume yet: no Syllabus and no
// Session.
func (r ResumePoint) empty() bool {
	return r.Lesson == "" && !r.SyllabusDone && r.NextStep == nil && r.OpenSession == nil
}
