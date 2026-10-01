package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
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
	eventTopicCreated: {apply: applyTopicCreated, replay: replayTopicCreated},
	eventTopicUpdated: {apply: applyTopicUpdated, replay: replayTopicUpdated},
}

// errUnknownItem marks an Event that refers to an item the History does not
// know yet, such as a Review of a Card whose creation hasn't arrived.
var errUnknownItem = errors.New("refers to something not in the History yet")

// Flag kinds. See docs/cli.md.
const (
	// FlagHeldEvent: an Event could not be applied, because it refers to
	// something the History doesn't know or has a type this version of
	// study doesn't understand.
	FlagHeldEvent = "held_event"
	// FlagConflict: two Events changed one item from the same version, or
	// two different Events share an ID; usually changes made on two
	// machines. Never resolved automatically.
	FlagConflict = "conflict"
	// FlagEditedOutside: content that approves or gates progress differs
	// from the version the History recorded.
	FlagEditedOutside = "edited_outside"
	// FlagInterruptedWrite: a write to the Topic was interrupted; the next
	// write finishes it.
	FlagInterruptedWrite = "interrupted_write"
)

// A Flag is something in a Topic that needs the learner's attention. Flags
// are reported, never resolved automatically.
type Flag struct {
	Kind    string   `json:"kind"`
	Message string   `json:"message"`
	Item    string   `json:"item,omitempty"`
	Events  []string `json:"events,omitempty"`
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
	// included. The hybrid logical clock starts after it.
	latest time.Time
	// created is when the Topic was created; zero when its creation is
	// not in the History.
	created time.Time
	// versions holds, for each item, the version recorded by the last
	// Event that changed it.
	versions map[string]version
	flags    []Flag

	seen  map[string][]byte            // Event ID → its line, to apply each ID once
	bases map[string]map[string]string // item → version changed from → Event ID
}

type heldEvent struct {
	ev     event
	reason error
}

// replay applies events, already ordered by time and then ID, applying each
// ID once. Events that refer to something not known yet are held and
// retried after every Event applied; those that never resolve are flagged.
// The same Events always give the same result, whatever order their lines
// were in.
func replay(events []event) *replayed {
	s := &replayed{
		versions: map[string]version{},
		seen:     map[string][]byte{},
		bases:    map[string]map[string]string{},
	}
	var held []heldEvent
	for _, ev := range events {
		if ev.Time.After(s.latest) {
			s.latest = ev.Time
		}
		if raw, ok := s.seen[ev.ID]; ok {
			if !bytes.Equal(raw, ev.raw) {
				s.flag(Flag{Kind: FlagConflict, Events: []string{ev.ID},
					Message: fmt.Sprintf("two different Events share the ID %s; only the first was applied", ev.ID)})
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
		s.flag(Flag{Kind: FlagHeldEvent, Events: []string{h.ev.ID},
			Message: fmt.Sprintf("Event %s (%s) was not applied: %v", h.ev.ID, h.ev.Type, h.reason)})
	}
	return s
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
	for _, it := range ev.Items {
		if it.Before == it.After {
			continue
		}
		if s.bases[it.Item] == nil {
			s.bases[it.Item] = map[string]string{}
		}
		if other, ok := s.bases[it.Item][it.Before]; ok {
			s.flag(Flag{Kind: FlagConflict, Item: it.Item, Events: []string{other, ev.ID},
				Message: fmt.Sprintf("%s was changed twice from the same version, by Events %s and %s, "+
					"probably on two machines: check it by hand", it.Item, other, ev.ID)})
		}
		s.bases[it.Item][it.Before] = ev.ID
		s.versions[it.Item] = version{hash: it.After, event: ev.ID}
	}
	return nil
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

func (s *replayed) flag(f Flag) { s.flags = append(s.flags, f) }

// isGating reports whether an item approves or gates progress, such as the
// Syllabus or a Lesson's Check. Lamplight compares such items with the
// version their last Event recorded, and status flags a difference. The
// Syllabus and Checks add their items here as they arrive; nothing gates
// progress yet.
func isGating(string) bool { return false }

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
		data, exists, err := readItem(topic, item)
		if err == nil && contentHash(data, exists) == recorded.hash {
			continue
		}
		flags = append(flags, Flag{Kind: FlagEditedOutside, Item: item, Events: []string{recorded.event},
			Message: fmt.Sprintf("%s differs from the version Event %s recorded: "+
				"it was changed outside Lamplight, so review the change with the learner", item, recorded.event)})
	}
	return flags
}
