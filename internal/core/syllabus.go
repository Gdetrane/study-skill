package core

import (
	"context"
	"encoding/json"
	"fmt"
)

const syllabusFile = "syllabus.toml"

// Event types of the Syllabus.
const (
	eventRevisionProposed = "revision.proposed"
	eventRevisionApplied  = "revision.applied"
)

// Milestone priorities.
const (
	PriorityMust          = "must"
	PriorityIfTime        = "if_time"
	PriorityAfterDeadline = "after_deadline"
)

const (
	maxSummaryRunes = 500
	maxOutcomeRunes = 300
	maxLessonHours  = 100
)

// Syllabus is the ordered program of a Topic: Milestones, each with its
// Lessons in order. Display numbers come from position; IDs never change.
//
// TODO(#25): target dates, Evidence per Lesson, Forecasts and Triage.
type Syllabus struct {
	Milestones []Milestone `json:"milestones"`
	// Extra holds settings this version of study does not know, kept as
	// they are (see syllabus_file.go). Likewise in Milestones and Lessons.
	Extra map[string]any `json:"-"`
}

// Milestone is a finish line within a Syllabus.
type Milestone struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Outcome  string           `json:"outcome,omitempty"`
	Priority string           `json:"priority,omitempty" jsonschema:"must, if_time or after_deadline; must when left out"`
	Lessons  []SyllabusLesson `json:"lessons"`
	Extra    map[string]any   `json:"-"`
}

// SyllabusLesson is one Lesson as the Syllabus lists it. Its text and Check
// live in lessons/<id>.md.
type SyllabusLesson struct {
	ID    string         `json:"id"`
	Title string         `json:"title"`
	Hours float64        `json:"hours,omitempty"`
	Extra map[string]any `json:"-"`
}

// lessons lists the Syllabus's Lessons in order.
func (s *Syllabus) lessons() []SyllabusLesson {
	var out []SyllabusLesson
	for _, m := range s.Milestones {
		out = append(out, m.Lessons...)
	}
	return out
}

func (s *Syllabus) lesson(id string) (SyllabusLesson, bool) {
	for _, l := range s.lessons() {
		if l.ID == id {
			return l, true
		}
	}
	return SyllabusLesson{}, false
}

// validate checks a Syllabus an agent proposed and returns it cleaned up.
func (s Syllabus) validate() (Syllabus, error) {
	if len(s.Milestones) == 0 {
		return s, invalidf("a Syllabus needs at least one Milestone")
	}
	seen := map[string]string{}
	use := func(kind, id string) error {
		if err := validateEntityID(kind, id); err != nil {
			return err
		}
		if other, ok := seen[id]; ok {
			return invalidf("the id %q is used by two %ss", id, other)
		}
		seen[id] = kind
		return nil
	}
	out := Syllabus{Milestones: make([]Milestone, 0, len(s.Milestones)), Extra: s.Extra}
	for _, m := range s.Milestones {
		if err := use("Milestone", m.ID); err != nil {
			return s, err
		}
		title, err := requiredText("Milestone title", m.Title, maxTitleRunes)
		if err != nil {
			return s, err
		}
		outcome, err := cleanText("Milestone outcome", m.Outcome, maxOutcomeRunes)
		if err != nil {
			return s, err
		}
		switch m.Priority {
		case PriorityMust, PriorityIfTime, PriorityAfterDeadline:
		case "":
			m.Priority = PriorityMust
		default:
			return s, invalidf("the priority of Milestone %s must be must, if_time or after_deadline, not %q", m.ID, m.Priority)
		}
		if len(m.Lessons) == 0 {
			return s, invalidf("Milestone %s has no Lessons", m.ID)
		}
		clean := Milestone{ID: m.ID, Title: title, Outcome: outcome, Priority: m.Priority, Extra: m.Extra}
		for _, l := range m.Lessons {
			if err := use("Lesson", l.ID); err != nil {
				return s, err
			}
			lt, err := requiredText("Lesson title", l.Title, maxTitleRunes)
			if err != nil {
				return s, err
			}
			if l.Hours < 0 || l.Hours > maxLessonHours {
				return s, invalidf("Lesson %s has %v hours: give an estimate between 0 and %d", l.ID, l.Hours, maxLessonHours)
			}
			clean.Lessons = append(clean.Lessons, SyllabusLesson{ID: l.ID, Title: lt, Hours: l.Hours, Extra: l.Extra})
		}
		out.Milestones = append(out.Milestones, clean)
	}
	return out, nil
}

// revisionProposedData is the payload of a revision.proposed Event. The
// Revision's ID is the Event's ID.
type revisionProposedData struct {
	Summary string `json:"summary"`
	// Base is the version of syllabus.toml the History recorded when the
	// Revision was proposed, or empty for a Topic's first Syllabus.
	Base     string       `json:"base,omitempty"`
	Syllabus syllabusData `json:"syllabus"`
	// FromFile marks a Revision that adopts a hand edit of syllabus.toml,
	// and FileVersion is the version of the file it adopts.
	FromFile    bool   `json:"from_file,omitempty"`
	FileVersion string `json:"file_version,omitempty"`
}

// revisionAppliedData is the payload of a revision.applied Event. It
// carries the whole Syllabus, so the Event can be applied from the History
// alone.
type revisionAppliedData struct {
	Revision string       `json:"revision"`
	Approval Approval     `json:"approval"`
	Syllabus syllabusData `json:"syllabus"`
}

// Approval records how the learner approved a Revision. Approvals are
// tamper-evident, not tamper-proof: the agent relays them.
//
// TODO(#25): ask the learner directly through MCP elicitation or on a
// terminal, and record that instead.
type Approval struct {
	// Via is how the approval was given: "chat" when the learner approved
	// in the conversation and the agent relays it.
	Via string `json:"via"`
	// LearnerSaid quotes the learner's approval.
	LearnerSaid string `json:"learner_said"`
}

// RevisionSpec describes a Revision to propose.
type RevisionSpec struct {
	// Summary says in plain words what changes and why.
	Summary string
	// Syllabus is the whole Syllabus as it would be after the Revision.
	Syllabus Syllabus
	// FromFile proposes syllabus.toml as it is on disk instead, so a hand
	// edit becomes an approved Revision rather than being overwritten.
	FromFile bool
	DryRun   bool
}

// RevisionProposal is a proposed Revision, waiting for the learner.
type RevisionProposal struct {
	Topic    string   `json:"topic"`
	Revision string   `json:"revision"`
	Summary  string   `json:"summary"`
	Syllabus Syllabus `json:"syllabus"`
	DryRun   bool     `json:"dry_run,omitempty"`
}

// RevisionApplied is the result of ApplyRevision.
type RevisionApplied struct {
	Topic    string   `json:"topic"`
	Revision string   `json:"revision"`
	Syllabus Syllabus `json:"syllabus"`
	// Changed is false when the Revision was already applied.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// ProposeRevision records a proposed change to the Syllabus, the first
// Syllabus included. Nothing changes until the learner approves it with
// ApplyRevision. Lessons already done must keep their place and title.
//
// While syllabus.toml differs from the version the History recorded, it was
// edited outside Lamplight: only a Revision that adopts the edit, with
// FromFile, can be proposed, so the edit is never silently overwritten.
func (c *Core) ProposeRevision(ctx context.Context, topicID string, spec RevisionSpec) (RevisionProposal, error) {
	summary, err := requiredText("summary", spec.Summary, maxSummaryRunes)
	if err != nil {
		return RevisionProposal{}, err
	}
	var syllabus Syllabus
	if !spec.FromFile {
		if syllabus, err = spec.Syllabus.validate(); err != nil {
			return RevisionProposal{}, err
		}
	}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		current, exists, err := view.read(syllabusFile)
		if err != nil {
			return nil, err
		}
		recorded := s.versions[syllabusFile].hash
		edited := contentHash(current, exists) != recorded
		d := revisionProposedData{Summary: summary, Base: recorded}
		switch {
		case spec.FromFile:
			if !exists {
				return nil, &Error{Code: CodeNotFound, Message: syllabusFile + " of " + topicID + " does not exist"}
			}
			onDisk, err := parseSyllabusFile(current, syllabusFile+" of "+topicID)
			if err != nil {
				return nil, err
			}
			if syllabus, err = onDisk.validate(); err != nil {
				return nil, invalidf("%s of %s cannot be adopted as it is: %v", syllabusFile, topicID, err)
			}
			d.FromFile, d.FileVersion = true, contentHash(current, exists)
		case edited:
			return nil, editedOutside(topicID)
		default:
			syllabus = keepExtras(syllabus, s.study.syllabus)
		}
		if err := keepsDoneLessons(s, syllabus); err != nil {
			return nil, err
		}
		d.Syllabus = syllabusData{syllabus}
		return &change{Type: eventRevisionProposed, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return RevisionProposal{}, err
	}
	return RevisionProposal{Topic: topicID, Revision: ev.ID, Summary: summary, Syllabus: syllabus, DryRun: spec.DryRun}, nil
}

// editedOutside explains why a Syllabus edited by hand blocks Revisions.
func editedOutside(topicID string) error {
	return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
		"%s of %s was changed outside Lamplight since the last approved Revision; to keep that edit, propose it "+
			"as a Revision with from_file and apply it once the learner approves; to drop it, restore %s from the "+
			"Topic's last Checkpoint", syllabusFile, topicID, syllabusFile)}
}

// ApplyRevision applies a proposed Revision once the learner has approved
// it. It refuses when another Revision was applied since this one was
// proposed, or when syllabus.toml was edited by hand, unless the Revision
// adopts exactly that edit. Applying a Revision twice changes nothing.
func (c *Core) ApplyRevision(ctx context.Context, topicID, revision string, approval Approval, dryRun bool) (RevisionApplied, error) {
	if approval.Via != "chat" {
		return RevisionApplied{}, invalidf("say how the learner approved the Revision: via must be \"chat\", not %q", approval.Via)
	}
	said, err := requiredText("learner's approval", approval.LearnerSaid, maxSummaryRunes)
	if err != nil {
		return RevisionApplied{}, err
	}
	approval.LearnerSaid = said
	var syllabus Syllabus
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		p, ok := s.study.proposals[revision]
		if !ok {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf("Topic %s has no proposed Revision %s", topicID, revision)}
		}
		syllabus = p.Syllabus.Syllabus
		if s.study.applied[revision] {
			return nil, nil
		}
		recorded := s.versions[syllabusFile].hash
		if p.Base != recorded {
			return nil, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
				"the Syllabus of %s changed since Revision %s was proposed: propose it again from the current Syllabus",
				topicID, revision)}
		}
		current, exists, err := view.read(syllabusFile)
		if err != nil {
			return nil, err
		}
		if onDisk := contentHash(current, exists); onDisk != recorded && !(p.FromFile && onDisk == p.FileVersion) {
			return nil, editedOutside(topicID)
		}
		if err := keepsDoneLessons(s, syllabus); err != nil {
			return nil, err
		}
		return &change{Type: eventRevisionApplied, Items: []string{syllabusFile},
			Data: revisionAppliedData{Revision: revision, Approval: approval, Syllabus: p.Syllabus}}, nil
	}, dryRun)
	if err != nil {
		return RevisionApplied{}, err
	}
	return RevisionApplied{Topic: topicID, Revision: revision, Syllabus: syllabus, Changed: ev != nil, DryRun: dryRun}, nil
}

// keepsDoneLessons refuses a Syllabus that drops or renames a Lesson already
// done: done Lessons are never rewritten.
func keepsDoneLessons(s *replayed, next Syllabus) error {
	for id, l := range s.study.lessons {
		if l.completed == nil {
			continue
		}
		nl, ok := next.lesson(id)
		if !ok {
			return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lesson %s is done, so a Revision cannot remove it", id)}
		}
		if s.study.syllabus != nil {
			if old, ok := s.study.syllabus.lesson(id); ok && old.Title != nl.Title {
				return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lesson %s is done, so a Revision cannot rename it", id)}
			}
		}
	}
	return nil
}

func applyRevisionApplied(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	if item != syllabusFile {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	var d revisionAppliedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	data, err := encodeSyllabus(d.Syllabus.Syllabus)
	return data, err == nil, err
}

func replayRevisionProposed(s *replayed, ev event) error {
	var d revisionProposedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	s.study.proposals[ev.ID] = d
	return nil
}

func replayRevisionApplied(s *replayed, ev event) error {
	var d revisionAppliedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if _, ok := s.study.proposals[d.Revision]; !ok {
		return fmt.Errorf("%w: Revision %s", errUnknownItem, d.Revision)
	}
	syllabus := d.Syllabus.Syllabus
	// A Revision never removes a done Lesson; one that does was proposed on
	// another machine before the Lesson was completed here.
	for id, l := range s.study.lessons {
		if l.completed == nil {
			continue
		}
		if _, ok := syllabus.lesson(id); !ok {
			s.flag(newFlag(FlagConflict, syllabusFile, []string{l.completedBy, ev.ID}, id,
				fmt.Sprintf("Revision %s removed Lesson %s, which Event %s had completed, probably on two machines: "+
					"check the Syllabus with the learner", d.Revision, id, l.completedBy)))
		}
	}
	s.study.syllabus = &syllabus
	s.study.applied[d.Revision] = true
	return nil
}

// SyllabusView is a Topic's Syllabus with each Lesson's progress, computed
// from the History, and the Revisions waiting for the learner.
type SyllabusView struct {
	Topic      string             `json:"topic"`
	Milestones []MilestoneView    `json:"milestones"`
	Proposals  []RevisionProposal `json:"proposals"`
}

// MilestoneView is a Milestone with its Lessons' progress.
type MilestoneView struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Outcome  string       `json:"outcome,omitempty"`
	Priority string       `json:"priority"`
	Lessons  []LessonView `json:"lessons"`
}

// Lesson statuses, computed from the History.
const (
	LessonNotStarted = "not_started"
	LessonInProgress = "in_progress"
	LessonDone       = "done"
)

// LessonView is one Lesson with its progress.
type LessonView struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Hours  float64 `json:"hours,omitempty"`
	Status string  `json:"status"`
	Phase  string  `json:"phase,omitempty"`
}

// SyllabusOf returns a Topic's Syllabus with its progress. A Topic without a
// Syllabus yet has no Milestones.
func (c *Core) SyllabusOf(ctx context.Context, topicID string) (SyllabusView, error) {
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return SyllabusView{}, err
	}
	view := SyllabusView{Topic: topicID, Milestones: []MilestoneView{}, Proposals: []RevisionProposal{}}
	if s.study.syllabus != nil {
		for _, m := range s.study.syllabus.Milestones {
			mv := MilestoneView{ID: m.ID, Title: m.Title, Outcome: m.Outcome, Priority: m.Priority, Lessons: []LessonView{}}
			for _, l := range m.Lessons {
				mv.Lessons = append(mv.Lessons, s.study.lessonView(l))
			}
			view.Milestones = append(view.Milestones, mv)
		}
	}
	for _, ev := range s.applied {
		if ev.Type != eventRevisionProposed || s.study.applied[ev.ID] {
			continue
		}
		p := s.study.proposals[ev.ID]
		view.Proposals = append(view.Proposals, RevisionProposal{Topic: topicID, Revision: ev.ID, Summary: p.Summary,
			Syllabus: p.Syllabus.Syllabus})
	}
	return view, nil
}

func (st *studyState) lessonView(l SyllabusLesson) LessonView {
	v := LessonView{ID: l.ID, Title: l.Title, Hours: l.Hours, Status: LessonNotStarted}
	if ls := st.lessons[l.ID]; ls != nil {
		v.Phase = ls.phase
		switch {
		case ls.completed != nil:
			v.Status = LessonDone
		case ls.phase != "" || len(ls.attempts) > 0:
			v.Status = LessonInProgress
		}
	}
	return v
}
