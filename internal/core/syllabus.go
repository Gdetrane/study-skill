package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const syllabusFile = "syllabus.toml"

// Event types of the Syllabus.
const (
	eventRevisionProposed = "revision.proposed"
	eventRevisionApplied  = "revision.applied"
	eventRevisionDeclined = "revision.declined"
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
	// dateLayout is how a Milestone's target date is written.
	dateLayout = "2006-01-02"
)

// Syllabus is the ordered program of a Topic: Milestones, each with its
// Lessons in order. Display numbers ("Lesson 2.3") come from position; IDs
// never change. Forecasts and Triage, which read the target dates and hour
// estimates, belong to the Goal and the Pace (#29).
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
	Target   string           `json:"target,omitempty" jsonschema:"the date the learner wants this Milestone finished, as YYYY-MM-DD; optional"`
	Lessons  []SyllabusLesson `json:"lessons"`
	Extra    map[string]any   `json:"-"`
}

// SyllabusLesson is one Lesson as the Syllabus lists it. Its text and Check
// live in lessons/<id>.md.
type SyllabusLesson struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Hours float64 `json:"hours,omitempty"`
	// Skipped marks a Lesson the learner chose to skip. It keeps its place
	// and number, and is never rewritten; a later Revision can take it back.
	Skipped bool           `json:"skipped,omitempty" jsonschema:"true for a Lesson the learner chose to skip; it keeps its place and number"`
	Extra   map[string]any `json:"-"`
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
	l, _, ok := s.find(id)
	return l, ok
}

// find returns a Lesson, the Milestone that holds it, and whether it was
// found.
func (s *Syllabus) find(id string) (SyllabusLesson, Milestone, bool) {
	for _, m := range s.Milestones {
		for _, l := range m.Lessons {
			if l.ID == id {
				return l, m, true
			}
		}
	}
	return SyllabusLesson{}, Milestone{}, false
}

// validate checks a Syllabus, whether an agent proposed it or the learner
// edited syllabus.toml, and returns it cleaned up. Errors name the Milestone
// or Lesson by number and id, so a hand edit can be fixed.
func (s Syllabus) validate() (Syllabus, error) {
	if len(s.Milestones) == 0 {
		return s, invalidf("a Syllabus needs at least one Milestone")
	}
	seen := map[string]string{}
	use := func(where, kind, id string) error {
		if err := validateEntityID(kind, id); err != nil {
			return at(where, err)
		}
		if id == exploreLesson {
			return invalidf("the id %q is reserved for Explore Cards; give the %s another id", id, kind)
		}
		if other, ok := seen[id]; ok {
			return invalidf("%s: the id %q is already used by %s", where, id, other)
		}
		seen[id] = where
		return nil
	}
	out := Syllabus{Milestones: make([]Milestone, 0, len(s.Milestones)), Extra: s.Extra}
	for i, m := range s.Milestones {
		where := milestoneName(i, m.ID)
		if err := use(where, "Milestone", m.ID); err != nil {
			return s, err
		}
		title, err := requiredText("title", m.Title, maxTitleRunes)
		if err != nil {
			return s, at(where, err)
		}
		outcome, err := cleanText("outcome", m.Outcome, maxOutcomeRunes)
		if err != nil {
			return s, at(where, err)
		}
		switch m.Priority {
		case PriorityMust, PriorityIfTime, PriorityAfterDeadline:
		case "":
			m.Priority = PriorityMust
		default:
			return s, invalidf("%s: the priority must be must, if_time or after_deadline, not %q", where, m.Priority)
		}
		if m.Target != "" {
			if _, err := time.Parse(dateLayout, m.Target); err != nil {
				return s, invalidf("%s: the target date must be written YYYY-MM-DD, such as 2026-12-01, not %q", where, m.Target)
			}
		}
		if len(m.Lessons) == 0 {
			return s, invalidf("%s has no Lessons", where)
		}
		clean := Milestone{ID: m.ID, Title: title, Outcome: outcome, Priority: m.Priority, Target: m.Target, Extra: m.Extra}
		for j, l := range m.Lessons {
			where := lessonName(i, j, l.ID)
			if err := use(where, "Lesson", l.ID); err != nil {
				return s, err
			}
			lt, err := requiredText("title", l.Title, maxTitleRunes)
			if err != nil {
				return s, at(where, err)
			}
			if math.IsNaN(l.Hours) || math.IsInf(l.Hours, 0) || l.Hours < 0 || l.Hours > maxLessonHours {
				return s, invalidf("%s has %v hours: give an estimate between 0 and %d", where, l.Hours, maxLessonHours)
			}
			clean.Lessons = append(clean.Lessons, SyllabusLesson{ID: l.ID, Title: lt, Hours: l.Hours, Skipped: l.Skipped, Extra: l.Extra})
		}
		out.Milestones = append(out.Milestones, clean)
	}
	return out, nil
}

// milestoneName and lessonName name a Milestone or a Lesson by display
// number and id, for messages.
func milestoneName(i int, id string) string {
	if id == "" {
		return fmt.Sprintf("Milestone %d", i+1)
	}
	return fmt.Sprintf("Milestone %d (%s)", i+1, id)
}

func lessonName(i, j int, id string) string {
	if id == "" {
		return fmt.Sprintf("Lesson %d.%d", i+1, j+1)
	}
	return fmt.Sprintf("Lesson %d.%d (%s)", i+1, j+1, id)
}

// at prefixes an error's message with where it was found, keeping its code.
func at(where string, err error) error {
	return &Error{Code: CodeOf(err), Message: where + ": " + err.Error(), Err: err}
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

// revisionDeclinedData is the payload of a revision.declined Event: the
// learner said no. The Revision can no longer be applied.
type revisionDeclinedData struct {
	Revision string   `json:"revision"`
	Decision Approval `json:"decision"`
}

// How the learner approved or declined a Revision.
const (
	// ViaChat: the learner answered in the conversation, and the agent
	// relays their words.
	ViaChat = "chat"
	// ViaElicitation: Lamplight asked the learner directly, through the
	// MCP client's elicitation.
	ViaElicitation = "elicitation"
	// ViaTerminal: Lamplight asked the learner directly, on a terminal.
	ViaTerminal = "terminal"
)

// Approval records how the learner approved or declined a Revision.
// Approvals are tamper-evident, not tamper-proof: an agent relays a chat
// approval, and one with a shell could fake a terminal.
type Approval struct {
	// Via is how the learner answered: chat, elicitation or terminal.
	Via string `json:"via"`
	// LearnerSaid quotes the learner's words. Required in chat; when
	// Lamplight asked directly, it is whatever the learner added.
	LearnerSaid string `json:"learner_said,omitempty"`
	// Shown is the question Lamplight showed the learner when it asked
	// directly.
	Shown string `json:"shown,omitempty"`
}

// check validates an Approval, or a decline when declining.
func (a Approval) check(declining bool) (Approval, error) {
	what := "approval"
	if declining {
		what = "answer"
	}
	var err error
	switch a.Via {
	case ViaChat:
		if a.LearnerSaid, err = requiredText("learner's "+what, a.LearnerSaid, maxSummaryRunes); err != nil {
			return a, err
		}
	case ViaElicitation, ViaTerminal:
		// The learner typed this themselves, after answering: it is kept
		// as well as it can be, and never fails their answer.
		a.LearnerSaid = directWords(a.LearnerSaid, maxSummaryRunes)
		if a.Shown == "" {
			return a, invalidf("an %s %s must record what the learner was shown", a.Via, what)
		}
	default:
		return a, invalidf("say how the learner answered: via must be chat, elicitation or terminal, not %q", a.Via)
	}
	return a, nil
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
	// Changes is the change against the current Syllabus, naming Lessons
	// by title, with any renumbering: show it to the learner.
	Changes RevisionChanges `json:"changes"`
	// Stale is true when the Syllabus changed since the Revision was
	// proposed, so it can no longer be applied: propose it again.
	Stale  bool `json:"stale,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
}

// Question is what Lamplight shows the learner when it asks them directly,
// through MCP elicitation or on a terminal, whether to apply the Revision.
func (p RevisionProposal) Question() string {
	return fmt.Sprintf("Lamplight asks: apply this change to the Syllabus of %s?\n\n%s\n\n%s", p.Topic, p.Summary, p.Changes.Text)
}

// RevisionApplied is the result of ApplyRevision.
type RevisionApplied struct {
	Topic    string   `json:"topic"`
	Revision string   `json:"revision"`
	Syllabus Syllabus `json:"syllabus"`
	// Approval is how the learner answered: the approval recorded, also
	// when the Revision was applied before.
	Approval Approval `json:"approval"`
	// Declined is true when Lamplight asked the learner directly through
	// revision_apply and they said no: nothing changed, and the Revision
	// can no longer be applied.
	Declined bool `json:"declined,omitempty"`
	// Changed is false when the answer was already recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// RevisionDeclined is the result of DeclineRevision.
type RevisionDeclined struct {
	Topic    string `json:"topic"`
	Revision string `json:"revision"`
	// Decision is how the learner said no: the one recorded, also when the
	// Revision was declined before.
	Decision Approval `json:"decision"`
	// Changed is false when the learner had already declined it.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// ProposeRevision records a proposed change to the Syllabus, the first
// Syllabus included. Nothing changes until the learner approves it with
// ApplyRevision. Done and skipped Lessons are never rewritten, and a
// Revision that changes nothing is refused, unless it adopts a hand edit.
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
	var changes RevisionChanges
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
		if err := keepsSettledLessons(s, syllabus); err != nil {
			return nil, err
		}
		changes = revisionChanges(s, syllabus, spec.FromFile)
		if len(changes.Changes) == 0 {
			return nil, invalidf("the proposed Syllabus is the Syllabus %s already has: there is nothing to approve", topicID)
		}
		d.Syllabus = syllabusData{syllabus}
		return &change{Type: eventRevisionProposed, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return RevisionProposal{}, err
	}
	return RevisionProposal{Topic: topicID, Revision: ev.ID, Summary: summary, Syllabus: syllabus, Changes: changes,
		DryRun: spec.DryRun}, nil
}

// editedOutside explains why a Syllabus edited by hand blocks Revisions.
func editedOutside(topicID string) error {
	return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
		"%s of %s was changed outside Lamplight since the last approved Revision; to keep that edit, propose it "+
			"as a Revision with from_file and apply it once the learner approves; to drop it, restore %s from the "+
			"Topic's last Checkpoint", syllabusFile, topicID, syllabusFile)}
}

// Revision returns a proposed Revision with its change against the current
// Syllabus, so an adapter can ask the learner whether to apply it. It fails
// when applying it would fail anyway, so the learner is never asked for an
// answer that would be thrown away: already applied or declined, based on a
// Syllabus that has changed since, syllabus.toml edited outside Lamplight,
// or a done or skipped Lesson it would rewrite.
func (c *Core) Revision(ctx context.Context, topicID, revision string) (RevisionProposal, error) {
	p, err := c.ProposedRevision(ctx, topicID, revision)
	if err != nil {
		return RevisionProposal{}, err
	}
	if _, err := c.ApplyRevision(ctx, topicID, revision, Approval{}, true); err != nil {
		return RevisionProposal{}, err
	}
	return p, nil
}

// ProposedRevision returns a Revision still waiting for the learner, stale
// or not, with its change against the current Syllabus: what an adapter
// shows when the learner may decline it.
func (c *Core) ProposedRevision(ctx context.Context, topicID, revision string) (RevisionProposal, error) {
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return RevisionProposal{}, err
	}
	p, ok := s.study.proposals[revision]
	switch {
	case !ok:
		return RevisionProposal{}, noSuchRevision(topicID, revision)
	case s.study.applied[revision]:
		return RevisionProposal{}, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Revision %s of %s is already applied", revision, topicID)}
	case s.study.declined[revision]:
		return RevisionProposal{}, declinedRevision(topicID, revision)
	}
	return RevisionProposal{Topic: topicID, Revision: revision, Summary: p.Summary, Syllabus: p.Syllabus.Syllabus,
		Changes: revisionChanges(s, p.Syllabus.Syllabus, p.FromFile), Stale: p.Base != s.versions[syllabusFile].hash}, nil
}

func noSuchRevision(topicID, revision string) error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf("Topic %s has no proposed Revision %s", topicID, revision)}
}

func declinedRevision(topicID, revision string) error {
	return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
		"the learner declined Revision %s of %s; if they changed their mind, propose it again", revision, topicID)}
}

func staleRevision(topicID, revision string) error {
	return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
		"the Syllabus of %s changed since Revision %s was proposed: propose it again from the current Syllabus",
		topicID, revision)}
}

// ApplyRevision applies a proposed Revision once the learner has approved
// it. It refuses when another Revision was applied since this one was
// proposed, when the learner declined it, or when syllabus.toml was edited
// by hand, unless the Revision adopts exactly that edit. Applying a
// Revision twice changes nothing, and returns the approval recorded the
// first time. A dry run needs no approval.
func (c *Core) ApplyRevision(ctx context.Context, topicID, revision string, approval Approval, dryRun bool) (RevisionApplied, error) {
	if !dryRun {
		var err error
		if approval, err = approval.check(false); err != nil {
			return RevisionApplied{}, err
		}
	}
	var syllabus Syllabus
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		p, ok := s.study.proposals[revision]
		if !ok {
			return nil, noSuchRevision(topicID, revision)
		}
		syllabus = p.Syllabus.Syllabus
		if s.study.applied[revision] {
			approval = s.study.appliedWith[revision]
			return nil, nil
		}
		if s.study.declined[revision] {
			return nil, declinedRevision(topicID, revision)
		}
		recorded := s.versions[syllabusFile].hash
		if p.Base != recorded {
			return nil, staleRevision(topicID, revision)
		}
		current, exists, err := view.read(syllabusFile)
		if err != nil {
			return nil, err
		}
		if onDisk := contentHash(current, exists); onDisk != recorded && !(p.FromFile && onDisk == p.FileVersion) {
			return nil, editedOutside(topicID)
		}
		if err := keepsSettledLessons(s, syllabus); err != nil {
			return nil, err
		}
		return &change{Type: eventRevisionApplied, Items: []string{syllabusFile},
			Data: revisionAppliedData{Revision: revision, Approval: approval, Syllabus: p.Syllabus}}, nil
	}, dryRun)
	if err != nil {
		return RevisionApplied{}, err
	}
	return RevisionApplied{Topic: topicID, Revision: revision, Syllabus: syllabus, Approval: approval,
		Changed: ev != nil, DryRun: dryRun}, nil
}

// DeclineRevision records that the learner said no to a proposed Revision.
// Nothing in the Syllabus changes, and the Revision can no longer be
// applied. Declining twice changes nothing, and returns the decision
// recorded the first time.
func (c *Core) DeclineRevision(ctx context.Context, topicID, revision string, decision Approval, dryRun bool) (RevisionDeclined, error) {
	if !dryRun {
		var err error
		if decision, err = decision.check(true); err != nil {
			return RevisionDeclined{}, err
		}
	}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		if _, ok := s.study.proposals[revision]; !ok {
			return nil, noSuchRevision(topicID, revision)
		}
		switch {
		case s.study.declined[revision]:
			decision = s.study.declinedWith[revision]
			return nil, nil
		case s.study.applied[revision]:
			return nil, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
				"Revision %s of %s is already applied; to undo it, propose a new Revision", revision, topicID)}
		}
		return &change{Type: eventRevisionDeclined, Data: revisionDeclinedData{Revision: revision, Decision: decision}}, nil
	}, dryRun)
	if err != nil {
		return RevisionDeclined{}, err
	}
	return RevisionDeclined{Topic: topicID, Revision: revision, Decision: decision, Changed: ev != nil, DryRun: dryRun}, nil
}

// keepsSettledLessons refuses a Syllabus that rewrites a Lesson already done
// or skipped: such a Lesson keeps its id, title, hours and Milestone, and a
// done Lesson cannot be skipped. A skipped Lesson can be taken back.
//
// A done Lesson the current Syllabus no longer holds was removed on another
// machine; that is flagged, and the learner settles it by adding the Lesson
// back or dismissing the flag, so it does not block other Revisions.
func keepsSettledLessons(s *replayed, next Syllabus) error {
	for id, l := range s.study.lessons {
		if l.completed == nil {
			continue
		}
		if nl, ok := next.lesson(id); ok && nl.Skipped {
			return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lesson %s is done, so it cannot be skipped", id)}
		}
		if s.study.syllabus != nil {
			if _, ok := s.study.syllabus.lesson(id); !ok {
				continue
			}
		}
		if err := keepsLesson(s.study.syllabus, next, id, "done"); err != nil {
			return err
		}
	}
	if s.study.syllabus != nil {
		for _, l := range s.study.syllabus.lessons() {
			if l.Skipped {
				if err := keepsLesson(s.study.syllabus, next, l.ID, "skipped"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// keepsLesson checks that next keeps a settled Lesson as current has it.
func keepsLesson(current *Syllabus, next Syllabus, id, settled string) error {
	refuse := func(what string) error {
		return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lesson %s is %s, so a Revision cannot %s it", id, settled, what)}
	}
	nl, nm, ok := next.find(id)
	if !ok {
		return refuse("remove")
	}
	if current == nil {
		return nil
	}
	ol, om, ok := current.find(id)
	switch {
	case !ok:
		return nil
	case ol.Title != nl.Title:
		return refuse("rename")
	case ol.Hours != nl.Hours:
		return refuse("change the hour estimate of")
	case om.ID != nm.ID:
		return refuse("move")
	}
	return nil
}

// directWords keeps what a learner typed when Lamplight asked them
// directly: one line, printable, valid UTF-8, at most max characters.
func directWords(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
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

// itemAfter is the version an Event left an item at, or "" if it does not
// edit it.
func itemAfter(ev event, item string) string {
	for _, it := range ev.Items {
		if it.Item == item {
			return it.After
		}
	}
	return ""
}

func replayRevisionApplied(s *replayed, ev event) error {
	var d revisionAppliedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	p, ok := s.study.proposals[d.Revision]
	if !ok {
		return fmt.Errorf("%w: Revision %s", errUnknownItem, d.Revision)
	}
	if s.study.applied[d.Revision] {
		// The same Revision approved on two machines: one Syllabus, no
		// conflict, and the second application changes nothing.
		s.repeat = true
		return nil
	}
	syllabus := d.Syllabus.Syllabus
	if s.study.declined[d.Revision] {
		s.flag(newFlag(FlagConflict, syllabusFile, []string{s.study.declinedBy[d.Revision], ev.ID}, d.Revision,
			fmt.Sprintf("Revision %s was both declined and applied, probably on two machines: it stays applied; "+
				"check the Syllabus with the learner", d.Revision)))
	}
	// A Revision approved for a Syllabus that another Event had already
	// changed: two machines changed the Syllabus from one version, and the
	// earlier change is lost unless the learner restores it.
	if recorded := s.versions[syllabusFile]; p.Base != recorded.hash && itemAfter(ev, syllabusFile) != recorded.hash {
		s.flag(newFlag(FlagConflict, syllabusFile, []string{recorded.event, ev.ID}, "",
			fmt.Sprintf("Revision %s was approved for a Syllabus that Event %s had already changed, probably on two "+
				"machines: the Syllabus now follows Revision %s, so check with the learner whether the earlier change "+
				"should be proposed again", d.Revision, recorded.event, d.Revision)))
	}
	// A Revision never rewrites a done Lesson; one that does was proposed
	// on another machine before the Lesson was completed here. A done
	// Lesson the Syllabus before this Revision no longer held was flagged
	// when it went, and keepsSettledLessons lets later Revisions leave it
	// out: flagging it again on every Revision would undo its dismissal.
	for id, l := range s.study.lessons {
		if l.completed == nil || s.study.syllabus == nil {
			continue
		}
		ol, om, held := s.study.syllabus.find(id)
		if !held {
			continue
		}
		problem := ""
		switch nl, nm, ok := syllabus.find(id); {
		case !ok:
			problem = "removed"
		case nl.Skipped:
			problem = "skipped"
		case ol.Title != nl.Title || ol.Hours != nl.Hours || om.ID != nm.ID:
			problem = "rewrote"
		}
		if problem != "" {
			s.flag(newFlag(FlagConflict, syllabusFile, []string{l.completedBy, ev.ID}, id,
				fmt.Sprintf("Revision %s %s Lesson %s, which Event %s had completed, probably on two machines: "+
					"check the Syllabus with the learner", d.Revision, problem, id, l.completedBy)))
		}
	}
	s.study.syllabus = &syllabus
	s.study.applied[d.Revision] = true
	s.study.appliedWith[d.Revision] = d.Approval
	// A Next step of a Lesson the Revision skipped or removed no longer
	// leads the Resume point.
	if ns := s.study.nextStep; ns != nil && ns.Lesson != "" {
		if l, ok := syllabus.lesson(ns.Lesson); !ok || l.Skipped {
			s.study.nextStep = nil
		}
	}
	return nil
}

func replayRevisionDeclined(s *replayed, ev event) error {
	var d revisionDeclinedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if _, ok := s.study.proposals[d.Revision]; !ok {
		return fmt.Errorf("%w: Revision %s", errUnknownItem, d.Revision)
	}
	if s.study.declined[d.Revision] {
		return nil
	}
	if s.study.applied[d.Revision] {
		s.flag(newFlag(FlagConflict, syllabusFile, []string{ev.ID}, d.Revision,
			fmt.Sprintf("Revision %s was both applied and declined, probably on two machines: it stays applied; "+
				"check the Syllabus with the learner", d.Revision)))
	}
	s.study.declined[d.Revision] = true
	s.study.declinedBy[d.Revision] = ev.ID
	s.study.declinedWith[d.Revision] = d.Decision
	return nil
}

// SyllabusView is a Topic's Syllabus with each Lesson's progress, computed
// from the History, and the Revisions waiting for the learner.
type SyllabusView struct {
	Topic      string             `json:"topic"`
	Milestones []MilestoneView    `json:"milestones"`
	Proposals  []RevisionProposal `json:"proposals"`
	// EditedOutside is true when syllabus.toml differs from the version
	// the History recorded; FileError says what is wrong with it, if it
	// cannot be adopted as it is.
	EditedOutside bool   `json:"edited_outside,omitempty"`
	FileError     string `json:"file_error,omitempty"`
}

// MilestoneView is a Milestone with its Lessons' progress.
type MilestoneView struct {
	Number   int          `json:"number"`
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Outcome  string       `json:"outcome,omitempty"`
	Priority string       `json:"priority"`
	Target   string       `json:"target,omitempty"`
	Lessons  []LessonView `json:"lessons"`
}

// Lesson statuses, computed from the History and the Syllabus.
const (
	LessonNotStarted = "not_started"
	LessonInProgress = "in_progress"
	LessonDone       = "done"
	LessonSkipped    = "skipped"
)

// LessonView is one Lesson with its progress.
type LessonView struct {
	// Number is the Lesson's display number, such as "2.3".
	Number string  `json:"number"`
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Hours  float64 `json:"hours,omitempty"`
	Status string  `json:"status"`
	Phase  string  `json:"phase,omitempty"`
	// Evidence is how many pieces of Evidence the Lesson cites.
	Evidence int `json:"evidence"`
}

// SyllabusOf returns a Topic's Syllabus with its progress. A Topic without a
// Syllabus yet has no Milestones.
func (c *Core) SyllabusOf(ctx context.Context, topicID string) (SyllabusView, error) {
	if err := ctx.Err(); err != nil {
		return SyllabusView{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return SyllabusView{}, err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return SyllabusView{}, err
	}
	s := replayHistory(h)
	view := SyllabusView{Topic: topicID, Milestones: []MilestoneView{}, Proposals: []RevisionProposal{}}
	if s.study.syllabus != nil {
		cited := s.citations()
		for i, m := range s.study.syllabus.Milestones {
			mv := MilestoneView{Number: i + 1, ID: m.ID, Title: m.Title, Outcome: m.Outcome, Priority: m.Priority,
				Target: m.Target, Lessons: []LessonView{}}
			for j, l := range m.Lessons {
				lv := s.study.lessonView(l)
				lv.Number, lv.Evidence = fmt.Sprintf("%d.%d", i+1, j+1), cited[l.ID]
				mv.Lessons = append(mv.Lessons, lv)
			}
			view.Milestones = append(view.Milestones, mv)
		}
	}
	recorded := s.versions[syllabusFile].hash
	for _, ev := range s.applied {
		if ev.Type != eventRevisionProposed || s.study.applied[ev.ID] || s.study.declined[ev.ID] {
			continue
		}
		p := s.study.proposals[ev.ID]
		view.Proposals = append(view.Proposals, RevisionProposal{Topic: topicID, Revision: ev.ID, Summary: p.Summary,
			Syllabus: p.Syllabus.Syllabus, Changes: revisionChanges(s, p.Syllabus.Syllabus, p.FromFile), Stale: p.Base != recorded})
	}
	if unfinishedItems(home, topicID, s)[syllabusFile] {
		return view, nil
	}
	if data, exists, err := readItem(topic, syllabusFile); err == nil && contentHash(data, exists) != recorded {
		view.EditedOutside = true
		if err := syllabusFileProblem(data, exists, syllabusFile+" of "+topicID); err != nil {
			view.FileError = err.Error()
		}
	}
	return view, nil
}

// syllabusFileProblem says what keeps syllabus.toml, as found on disk, from
// being adopted as it is, or returns nil when it could be.
func syllabusFileProblem(data []byte, exists bool, where string) error {
	if !exists {
		return &Error{Code: CodeNotFound, Message: where + " is missing"}
	}
	s, err := parseSyllabusFile(data, where)
	if err != nil {
		return err
	}
	if _, err := s.validate(); err != nil {
		return at(where, err)
	}
	return nil
}

// citations counts the Evidence each Lesson cites, retracted Evidence aside.
func (s *replayed) citations() map[string]int {
	out := map[string]int{}
	for _, e := range s.knowledge().evidence {
		if !e.Retracted {
			out[e.Lesson]++
		}
	}
	return out
}

func (st *studyState) lessonView(l SyllabusLesson) LessonView {
	v := LessonView{ID: l.ID, Title: l.Title, Hours: l.Hours, Status: LessonNotStarted}
	ls := st.lessons[l.ID]
	if ls != nil {
		v.Phase = ls.phase
	}
	switch {
	case ls != nil && ls.completed != nil:
		v.Status = LessonDone
	case l.Skipped:
		v.Status = LessonSkipped
	case ls != nil && (ls.phase != "" || len(ls.attempts) > 0):
		v.Status = LessonInProgress
	}
	return v
}
