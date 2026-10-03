package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// An eventKind gives one type of Event its meaning.
type eventKind struct {
	// apply returns an item's content after the Event, given its current
	// content, and whether the item exists afterwards. It reads only the
	// Event's Data and must be deterministic: recovery runs it again on the
	// same content and expects the version the Event recorded.
	apply func(ev event, item string, current []byte, exists bool) (next []byte, keep bool, err error)
	// replay folds the Event into the replayed state. It returns an error
	// wrapping errUnknownItem when the Event refers to something not known
	// yet, which holds the Event until it is.
	replay func(s *replayed, ev event) error
}

// eventKinds lists every Event type the core records.
var eventKinds = map[string]eventKind{
	eventTopicCreated:     {apply: applyTopicCreated, replay: replayTopicCreated},
	eventTopicUpdated:     {apply: applyTopicUpdated, replay: replayTopicUpdated},
	eventFlagDismissed:    {apply: applyNothing, replay: replayFlagDismissed},
	eventRevisionProposed: {apply: applyNothing, replay: replayRevisionProposed},
	eventRevisionApplied:  {apply: applyRevisionApplied, replay: replayRevisionApplied},
	eventRevisionDeclined: {apply: applyNothing, replay: replayRevisionDeclined},
	eventSessionOpened:    {apply: applyNothing, replay: replaySessionOpened},
	eventSessionClosed:    {apply: applyNothing, replay: replaySessionClosed},
	eventSessionFocused:   {apply: applyNothing, replay: replaySessionFocused},
	eventPhaseSet:         {apply: applyNothing, replay: replayPhaseSet},
	eventAttemptRecorded:  {apply: applyNothing, replay: replayAttemptRecorded},
	eventLessonCompleted:  {apply: applyLessonCompleted, replay: replayLessonCompleted},
	eventReviewRecorded:   {apply: applyReviewRecorded, replay: replayReviewRecorded},
	eventCardAdded:        {apply: applyCardAdded, replay: replayCardAdded},
	eventCardEdited:       {apply: applyCardEdited, replay: replayCardEdited},
	eventCardSuspended:    {apply: applyNothing, replay: replayCardSuspension(true)},
	eventCardUnsuspended:  {apply: applyNothing, replay: replayCardSuspension(false)},
	eventCardDeleted:      {apply: applyCardDeleted, replay: replayCardDeleted},
	eventCardFlagged:      {apply: applyNothing, replay: replayCardFlagged},
	eventCheckpointTaken:  {apply: applyNothing, replay: replayCheckpointTaken},
}

// errUnknownItem marks an Event that refers to an item the History does not
// know yet, such as a Review of a Card whose creation hasn't arrived.
var errUnknownItem = errors.New("refers to something not in the History yet")

// Flag kinds. See docs/cli.md.
const (
	// FlagHeldEvent: an Event could not be applied, because it refers to
	// something the History doesn't know, or was written by a version of
	// study that is newer or knows Event types this one doesn't.
	FlagHeldEvent = "held_event"
	// FlagConflict: two Events changed one item from the same version, or
	// two different Events share an ID; usually changes made on two
	// machines. Never resolved automatically.
	FlagConflict = "conflict"
	// FlagDamagedLine: a line of the History is not an Event, such as a
	// line cut off by an interrupted write that a merge moved into the
	// middle of the file. It is skipped, never deleted.
	FlagDamagedLine = "damaged_line"
	// FlagClockAhead: the History holds an Event dated more than a day
	// after this computer's clock, so some machine's clock was wrong.
	FlagClockAhead = "clock_ahead"
	// FlagEditedOutside: content that approves or gates progress differs
	// from the version the History recorded. The next change Lamplight
	// records to it clears the flag.
	FlagEditedOutside = "edited_outside"
	// FlagInterruptedWrite: a write to the Topic was interrupted; the next
	// write finishes it.
	FlagInterruptedWrite = "interrupted_write"
	// FlagCardFlagged: the learner flagged a Card during a Review as wrong
	// or unclear. Editing or deleting the Card settles it.
	FlagCardFlagged = "card_flagged"
)

// dismissible reports whether the learner can dismiss a kind of flag. The
// others go away by themselves when the next write records the item or
// finishes the interrupted write.
func dismissible(kind string) bool {
	switch kind {
	case FlagHeldEvent, FlagConflict, FlagDamagedLine, FlagClockAhead, FlagCardFlagged:
		return true
	}
	return false
}

// clockAheadLimit is how far the History's latest Event may be ahead of this
// computer's clock before status flags it.
const clockAheadLimit = 24 * time.Hour

// A Flag is something in a Topic that needs the learner's attention. Flags
// are reported, never resolved automatically; the learner can dismiss some
// kinds once they have looked at them.
type Flag struct {
	// ID identifies the flag across runs and machines, for dismissing it.
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Message string   `json:"message"`
	Item    string   `json:"item,omitempty"`
	Events  []string `json:"events,omitempty"`
}

// newFlag builds a Flag whose ID is a short hash of what identifies it: its
// kind, item and Events, and detail for flags that have neither.
func newFlag(kind, item string, events []string, detail, message string) Flag {
	sum := sha256.Sum256([]byte(kind + "\x00" + item + "\x00" + strings.Join(events, "\x00") + "\x00" + detail))
	return Flag{ID: hex.EncodeToString(sum[:5]), Kind: kind, Message: message, Item: item, Events: events}
}

// version is the hash an Event recorded for an item.
type version struct {
	hash  string
	event string
}

// replayed is what replaying a Topic's History yields. Status is never
// stored: it is computed here from the Events (ADR-0005).
type replayed struct {
	// applied are the Events applied, in replay order.
	applied []event
	// latest is the latest Event time in the History, held Events
	// included, and latestEvent the Event at that time. The hybrid logical
	// clock starts after it.
	latest      time.Time
	latestEvent string
	// newer counts Events written by a newer version of study; while there
	// are any, the Topic is read but never written.
	newer int
	// created is when the Topic was created; zero when its creation is
	// not in the History.
	created time.Time
	// versions holds, for each item, the version recorded by the last
	// Event that changed it.
	versions map[string]version
	// recorded holds, for each item, every version any Event recorded for
	// it, before or after, with the position of the last Event that did:
	// a line a merge leaves at one of these versions is debris.
	recorded map[string]map[string]int
	// flags are what replay found, before dismissals are taken out.
	flags []Flag
	// dismissed holds the flags the learner dismissed: ID → kind.
	dismissed map[string]string
	// study is the learning state: Syllabus, Lessons, Sessions and Cards.
	study studyState
	// know is the Topic's Sources and Evidence; see knowledge().
	know *knowledgeState
	// plans is the Topic's state and its Tasks done; see planning().
	plans *planningState
	// assess is the Topic's Assessments, Level and hints; see assessing().
	assess *assessmentState
	// imported is the Topic's import from a v1 workspace, if any.
	imported *importState

	// repeat is set by an Event's replay when the Event repeats a change
	// already replayed, such as one Revision approved on two machines: its
	// items then change nothing.
	repeat bool

	seen  map[string][]byte                // Event ID → its line, to apply each ID once
	bases map[string]map[string]baseChange // item → version changed from → the change
}

// baseChange is an Event that changed an item from a version, and the
// version it left.
type baseChange struct {
	event string
	after string
}

type heldEvent struct {
	ev     event
	reason error
}

// replayHistory replays a History read from disk: its Events, plus flags for
// the lines it could not use.
func replayHistory(h historyLog) *replayed {
	s := replay(h.events)
	for _, ev := range h.newer {
		s.newer++
		s.see(ev)
		s.flag(newFlag(FlagHeldEvent, "", []string{ev.ID}, string(ev.raw),
			fmt.Sprintf("Event %s was written by a newer version of study (format %d), so this Topic can be read "+
				"but not changed: upgrade study", ev.ID, ev.Format)))
	}
	for _, d := range h.damaged {
		s.flag(newFlag(FlagDamagedLine, historyFile, nil, string(d.text),
			fmt.Sprintf("line %d of %s was skipped, because %s; fix or delete the line by hand, "+
				"perhaps comparing it with the Topic's last Checkpoint", d.line, historyFile, d.reason)))
	}
	return s
}

// replay applies events, already ordered by time and then ID, applying each
// ID once. Events that refer to something not known yet are held and
// retried after every Event applied; those that never resolve are flagged.
// The same Events always give the same result, whatever order their lines
// were in.
func replay(events []event) *replayed {
	s := &replayed{
		versions:  map[string]version{},
		recorded:  map[string]map[string]int{},
		dismissed: map[string]string{},
		study:     newStudyState(),
		seen:      map[string][]byte{},
		bases:     map[string]map[string]baseChange{},
	}
	var held []heldEvent
	for _, ev := range events {
		s.see(ev)
		if raw, ok := s.seen[ev.ID]; ok {
			if !bytes.Equal(raw, ev.raw) {
				s.flag(newFlag(FlagConflict, "", []string{ev.ID}, string(ev.raw),
					fmt.Sprintf("two different Events share the ID %s; only the first was applied", ev.ID)))
			}
			continue
		}
		s.seen[ev.ID] = ev.raw
		if err := s.apply(ev); err != nil {
			held = append(held, heldEvent{ev: ev, reason: err})
			continue
		}
		held = s.retry(held)
	}
	for _, h := range held {
		s.flag(newFlag(FlagHeldEvent, "", []string{h.ev.ID}, "",
			fmt.Sprintf("Event %s (%s) was not applied: %v", h.ev.ID, h.ev.Type, h.reason)))
	}
	return s
}

// see advances the latest time in the History.
func (s *replayed) see(ev event) {
	if ev.Time.After(s.latest) || (ev.Time.Equal(s.latest) && ev.ID > s.latestEvent) {
		s.latest, s.latestEvent = ev.Time, ev.ID
	}
}

func (s *replayed) apply(ev event) error {
	kind, ok := eventKinds[ev.Type]
	if !ok {
		return errors.New("this version of study doesn't know this type of Event; upgrade study")
	}
	if err := kind.replay(s, ev); err != nil {
		return err
	}
	s.applied = append(s.applied, ev)
	if s.repeat {
		s.repeat = false
		return nil
	}
	for _, it := range ev.Items {
		if s.recorded[it.Item] == nil {
			s.recorded[it.Item] = map[string]int{}
		}
		for _, hash := range []string{it.Before, it.After} {
			if hash != "" {
				s.recorded[it.Item][hash] = len(s.applied)
			}
		}
		if it.Before == it.After {
			// The Event left the item as it was, but it is still the
			// version this Event recorded: adopting a hand edit that
			// Lamplight would write byte for byte must clear the flag.
			s.versions[it.Item] = version{hash: it.After, event: ev.ID}
			continue
		}
		if s.bases[it.Item] == nil {
			s.bases[it.Item] = map[string]baseChange{}
		}
		// Two Events that made the same change from one version, such as
		// one Revision approved on two machines, agree: no conflict.
		if other, ok := s.bases[it.Item][it.Before]; ok && other.after != it.After {
			s.flag(newFlag(FlagConflict, it.Item, []string{other.event, ev.ID}, "",
				fmt.Sprintf("%s was changed twice from the same version, by Events %s and %s, "+
					"probably on two machines: check it by hand", it.Item, other.event, ev.ID)))
		}
		s.bases[it.Item][it.Before] = baseChange{event: ev.ID, after: it.After}
		// The item is at a new version, so a later change from it starts
		// a new run: a version that comes back (A, B, then A again) is not
		// a conflict with the Event that first changed it.
		delete(s.bases[it.Item], it.After)
		s.versions[it.Item] = version{hash: it.After, event: ev.ID}
	}
	return nil
}

// itemVersions returns what the History recorded for an item, for the
// resolver that picks among the lines a merge leaves (see resolveEntity).
func (s *replayed) itemVersions(item string) itemVersions {
	if s == nil {
		return itemVersions{}
	}
	v, ok := s.versions[item]
	return itemVersions{latest: v.hash, recorded: ok, known: s.recorded[item]}
}

// retry applies held Events that have become applicable, until none does.
func (s *replayed) retry(held []heldEvent) []heldEvent {
	for progress := true; progress && len(held) > 0; {
		progress = false
		for i := 0; i < len(held); i++ {
			if err := s.apply(held[i].ev); err != nil {
				held[i].reason = err
				continue
			}
			held = append(held[:i], held[i+1:]...)
			progress = true
			i--
		}
	}
	return held
}

// flag records a flag, once: a conflict that two checks find has one ID,
// and the first message is kept.
func (s *replayed) flag(f Flag) {
	for _, g := range s.flags {
		if g.ID == f.ID {
			return
		}
	}
	s.flags = append(s.flags, f)
}

// isGating reports whether an item approves or gates progress: the Syllabus,
// which only approved Revisions change, and each Lesson's Check. Lamplight
// compares such items with the version their last Event recorded, and status
// flags a difference.
func isGating(item string) bool {
	if item == syllabusFile {
		return true
	}
	file, key, _ := strings.Cut(item, "#")
	ok, _ := path.Match("lessons/*.md", file)
	return ok && key == checkKey
}

// topicFlags returns the flags of a replayed Topic that the learner has not
// dismissed: what replay found, gating items edited outside Lamplight, and a
// History dated ahead of this computer's clock.
func (c *Core) topicFlags(topic *os.Root, s *replayed) []Flag {
	all := append(append([]Flag{}, s.flags...), c.gatingFlags(topic, s)...)
	all = append(all, s.study.cardFlags()...)
	all = append(all, entityConflicts(topic, s)...)
	all = append(all, levelFlags(topic, s)...)
	if ahead := s.latest.Sub(c.now()); ahead > clockAheadLimit {
		// The flag is named after the earliest Event dated ahead, which
		// later Events, the dismissal included, never change.
		first := s.latestEvent
		for _, ev := range s.applied {
			if ev.Time.Sub(c.now()) > clockAheadLimit {
				first = ev.ID
				break
			}
		}
		all = append(all, newFlag(FlagClockAhead, "", []string{first}, "",
			fmt.Sprintf("the latest Event in the History is dated %s, %s ahead of this computer's clock, "+
				"so some machine's clock was wrong; new Events still sort after it, and keep their real "+
				"time separately", s.latest.Format(time.RFC3339), ahead.Round(time.Hour))))
	}
	visible := make([]Flag, 0, len(all))
	for _, f := range all {
		if _, gone := s.dismissed[f.ID]; !gone {
			visible = append(visible, f)
		}
	}
	return visible
}

// gatingFlags compares the gating items the History recorded with the
// content on disk.
func (c *Core) gatingFlags(topic *os.Root, s *replayed) []Flag {
	items := make([]string, 0, len(s.versions))
	for item := range s.versions {
		if c.gating(item) {
			items = append(items, item)
		}
	}
	sort.Strings(items)
	var flags []Flag
	for _, item := range items {
		recorded := s.versions[item]
		data, exists, err := readItemWith(topic, item, s.itemVersions(item))
		if err == nil && contentHash(data, exists) == recorded.hash {
			continue
		}
		message := fmt.Sprintf("%s differs from the version Event %s recorded: "+
			"it was changed outside Lamplight, so review the change with the learner", item, recorded.event)
		if item == syllabusFile && err == nil {
			// A hand edit of the Syllabus is adopted through a Revision, so
			// say precisely what would stop that.
			if problem := syllabusFileProblem(data, exists, syllabusFile); problem != nil {
				message += "; it cannot be adopted as it is: " + problem.Error()
			} else {
				message += "; to keep the edit, propose it as a Revision from the file"
			}
		}
		flags = append(flags, newFlag(FlagEditedOutside, item, []string{recorded.event}, "", message))
	}
	return flags
}

// applyNothing is the applier of Events that edit no items.
func applyNothing(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
}
