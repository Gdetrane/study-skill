package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// maxCardEvidence bounds the Evidence one Card cites.
const maxCardEvidence = 20

// cardAddedData is the payload of a card.added Event: the new Card in full.
type cardAddedData struct {
	Card cardLine `json:"card"`
}

// cardEditedData is the payload of a card.edited Event: the new prompt,
// answer or Evidence, whichever changed. Evidence, when present, replaces
// the Card's list; an empty list removes it.
type cardEditedData struct {
	Card     string    `json:"card"`
	Prompt   string    `json:"prompt,omitempty"`
	Answer   string    `json:"answer,omitempty"`
	Evidence *[]string `json:"evidence,omitempty"`
}

// cardRefData is the payload of the Events that name a Card and nothing
// else: card.suspended and card.unsuspended.
type cardRefData struct {
	Card string `json:"card"`
}

// cardDeletedData is the payload of a card.deleted Event. Reviews is how
// many Reviews of the Card the deleting machine knew, so a Review made on
// another machine before syncing is flagged whichever Event replays first.
type cardDeletedData struct {
	Card    string `json:"card"`
	Reviews int    `json:"reviews"`
}

// cardFlaggedData is the payload of a card.flagged Event.
type cardFlaggedData struct {
	Card string `json:"card"`
	Note string `json:"note,omitempty"`
}

// CardSpec describes a Card to add.
type CardSpec struct {
	// Lesson is the Lesson the Card is written from. Empty makes an
	// Explore Card, written from free questions.
	Lesson string
	Prompt string
	Answer string
	// Evidence lists the ids of the Evidence the Card relies on.
	Evidence []string
	DryRun   bool
}

// CardEdit describes a change to a Card. Empty text and nil Evidence stay
// as they are; an empty, non-nil Evidence removes the Card's.
type CardEdit struct {
	Card     string
	Prompt   string
	Answer   string
	Evidence []string
	DryRun   bool
}

// CardChange is the result of an operation on one Card.
type CardChange struct {
	Topic string `json:"topic"`
	Card  Card   `json:"card"`
	// Changed is false when the Card already was as asked, so nothing was
	// recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// checkCardEvidence checks the Evidence a Card cites against the History:
// each id must be recorded and not retracted. It returns the ids without
// repeats, in the order given.
func checkCardEvidence(s *replayed, topicID string, ids []string) ([]string, error) {
	if len(ids) > maxCardEvidence {
		return nil, invalidf("a Card cites at most %d pieces of Evidence", maxCardEvidence)
	}
	k := s.knowledge()
	out := []string{} // never nil: an empty list removes a Card's Evidence
	for _, id := range ids {
		if slices.Contains(out, id) {
			continue
		}
		i, ok := k.byID[id]
		if !ok {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf("Topic %s has no Evidence %s: study evidence list shows it", topicID, id)}
		}
		if k.evidence[i].Retracted {
			return nil, invalidf("Evidence %s was retracted, so a Card cannot cite it", id)
		}
		out = append(out, id)
	}
	return out, nil
}

// AddCard adds a draft Card, from a Lesson or, without one, as an Explore
// Card. It is a draft until its first Review, and the daily cap limits how
// many new Cards are offered. Adding a Card with the same Lesson, prompt and
// answer as one the Topic has returns that Card and records nothing, so a
// retry never duplicates a Card.
//
// An Explore Card needs no open Explore Session: a useful answer comes up in
// any Session, and the learner may add Cards from the command line with no
// Session at all.
func (c *Core) AddCard(ctx context.Context, topicID string, spec CardSpec) (CardChange, error) {
	draft, err := cleanDraft(CardDraft{Prompt: spec.Prompt, Answer: spec.Answer})
	if err != nil {
		return CardChange{}, err
	}
	if spec.Lesson == exploreLesson {
		return CardChange{}, invalidf("%q is not a Lesson: leave the Lesson out for an Explore Card", exploreLesson)
	}
	result := CardChange{Topic: topicID, DryRun: spec.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		prefix := exploreLesson
		if spec.Lesson != "" {
			if _, err := requireLesson(s, topicID, spec.Lesson); err != nil {
				return nil, err
			}
			prefix = spec.Lesson
		}
		evidence, err := checkCardEvidence(s, topicID, spec.Evidence)
		if err != nil {
			return nil, err
		}
		for _, id := range s.study.cardOrder {
			cs := s.study.cards[id]
			if !cs.gone() && cs.lesson == prefix && cs.prompt == draft.Prompt && cs.answer == draft.Answer {
				card, err := readCard(view, s, cs)
				if err != nil && CodeOf(err) != CodeCorrupt {
					return nil, err
				}
				result.Card = card
				return nil, nil
			}
		}
		line := cardLine{Format: FormatVersion, ID: c.newCardID(s, prefix), Lesson: spec.Lesson,
			Prompt: draft.Prompt, Answer: draft.Answer, Evidence: evidence}
		result.Card = Card{ID: line.ID, Number: liveCards(&s.study) + 1, Lesson: line.Lesson,
			Prompt: line.Prompt, Answer: line.Answer, Evidence: line.Evidence, Draft: true}
		return &change{Type: eventCardAdded, Data: cardAddedData{Card: line}, Items: []string{cardItem(line.ID)}}, nil
	}, spec.DryRun)
	if err != nil {
		return CardChange{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

// liveCards counts the Cards not dropped or deleted.
func liveCards(st *studyState) int {
	n := 0
	for _, cs := range st.cards {
		if !cs.gone() {
			n++
		}
	}
	return n
}

// EditCard changes a Card's prompt, answer or Evidence. Editing a Card the
// learner flagged settles the flag. Asking for what the Card already has
// records nothing.
func (c *Core) EditCard(ctx context.Context, topicID string, edit CardEdit) (CardChange, error) {
	if edit.Prompt == "" && edit.Answer == "" && edit.Evidence == nil {
		return CardChange{}, invalidf("give the Card's new prompt, answer or Evidence")
	}
	var err error
	if edit.Prompt != "" {
		if edit.Prompt, err = cardText("Card prompt", edit.Prompt); err != nil {
			return CardChange{}, err
		}
	}
	if edit.Answer != "" {
		if edit.Answer, err = cardText("Card answer", edit.Answer); err != nil {
			return CardChange{}, err
		}
	}
	result := CardChange{Topic: topicID, DryRun: edit.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		cs, err := liveCard(s, topicID, edit.Card)
		if err != nil {
			return nil, err
		}
		card, err := readCard(view, s, cs)
		if err != nil {
			return nil, err
		}
		d := cardEditedData{Card: edit.Card}
		if edit.Prompt != "" && edit.Prompt != card.Prompt {
			d.Prompt, card.Prompt = edit.Prompt, edit.Prompt
		}
		if edit.Answer != "" && edit.Answer != card.Answer {
			d.Answer, card.Answer = edit.Answer, edit.Answer
		}
		if edit.Evidence != nil {
			evidence, err := checkCardEvidence(s, topicID, edit.Evidence)
			if err != nil {
				return nil, err
			}
			if !slices.Equal(evidence, card.Evidence) {
				d.Evidence, card.Evidence = &evidence, evidence
			}
		}
		result.Card = card
		if d.Prompt == "" && d.Answer == "" && d.Evidence == nil {
			return nil, nil
		}
		result.Card.Flagged = false
		return &change{Type: eventCardEdited, Data: d, Items: []string{cardItem(edit.Card)}}, nil
	}, edit.DryRun)
	if err != nil {
		return CardChange{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

// SuspendCard suspends a Card, so it is never offered for Review, or with
// suspend false makes it reviewable again. Its schedule is kept.
func (c *Core) SuspendCard(ctx context.Context, topicID, cardID string, suspend, dryRun bool) (CardChange, error) {
	typ := eventCardSuspended
	if !suspend {
		typ = eventCardUnsuspended
	}
	return c.changeCard(ctx, topicID, cardID, dryRun, func(_ *replayed, cs *cardState, card *Card) *change {
		if cs.suspended == suspend {
			return nil
		}
		card.Suspended = suspend
		return &change{Type: typ, Data: cardRefData{Card: cardID}}
	})
}

// DeleteCard deletes a Card: its line leaves cards.jsonl and it is never
// scheduled again. The History keeps its Reviews. Deleting a Card already
// deleted or dropped records nothing.
func (c *Core) DeleteCard(ctx context.Context, topicID, cardID string, dryRun bool) (CardChange, error) {
	result := CardChange{Topic: topicID, DryRun: dryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if cs := s.study.cards[cardID]; cs != nil && cs.gone() {
			result.Card = Card{ID: cardID, Lesson: cs.lessonShown(), Prompt: cs.prompt, Answer: cs.answer}
			return nil, nil
		}
		cs, err := liveCard(s, topicID, cardID)
		if err != nil {
			return nil, err
		}
		card, err := readCard(view, s, cs)
		if err != nil && CodeOf(err) != CodeCorrupt {
			return nil, err
		}
		result.Card = card
		return &change{Type: eventCardDeleted, Data: cardDeletedData{Card: cardID, Reviews: len(cs.reviews)},
			Items: []string{cardItem(cardID)}}, nil
	}, dryRun)
	if err != nil {
		return CardChange{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

// FlagCard records that the learner flagged a Card during a Review as wrong
// or unclear, with an optional note. status shows it until the Card is
// edited or deleted, or the flag dismissed. Flagging a Card again with the
// same note records nothing, unless its flag was dismissed: then it is a new
// flag.
func (c *Core) FlagCard(ctx context.Context, topicID, cardID, note string, dryRun bool) (CardChange, error) {
	note, err := cleanText("note", note, maxCardNoteRunes)
	if err != nil {
		return CardChange{}, err
	}
	return c.changeCard(ctx, topicID, cardID, dryRun, func(s *replayed, cs *cardState, card *Card) *change {
		if cs.flaggedBy != "" && cs.flagNote == note {
			if _, dismissed := s.dismissed[cardFlag(cs).ID]; !dismissed {
				return nil
			}
		}
		card.Flagged = true
		return &change{Type: eventCardFlagged, Data: cardFlaggedData{Card: cardID, Note: note}}
	})
}

// changeCard records the change decide returns for a live Card, or nothing
// when decide returns nil.
func (c *Core) changeCard(ctx context.Context, topicID, cardID string, dryRun bool,
	decide func(s *replayed, cs *cardState, card *Card) *change) (CardChange, error) {
	result := CardChange{Topic: topicID, DryRun: dryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		cs, err := liveCard(s, topicID, cardID)
		if err != nil {
			return nil, err
		}
		card, err := readCard(view, s, cs)
		if err != nil && CodeOf(err) != CodeCorrupt {
			return nil, err
		}
		ch := decide(s, cs, &card)
		result.Card = card
		return ch, nil
	}, dryRun)
	if err != nil {
		return CardChange{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

func applyCardAdded(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d cardAddedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	if item != cardItem(d.Card.ID) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	data, err := encodeCard(d.Card)
	return data, err == nil, err
}

func applyCardEdited(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d cardEditedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	if item != cardItem(d.Card) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("Card %s is missing from %s", d.Card, cardsFile)
	}
	data, err := patchCard(d.Card, current, d.Prompt, d.Answer, d.Evidence)
	return data, err == nil, err
}

func applyCardDeleted(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d cardDeletedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	if item != cardItem(d.Card) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	return nil, false, nil
}

func replayCardAdded(s *replayed, ev event) error {
	var d cardAddedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.Card.ID == "" {
		return fmt.Errorf("its payload names no Card")
	}
	// Card and Lesson ids reach terminals as they are, and a History can
	// arrive from another machine: an Event with ids study never makes is
	// not a Card.
	if !cardIDPattern.MatchString(d.Card.ID) {
		return fmt.Errorf("its Card id %q is not one study makes", d.Card.ID)
	}
	if d.Card.Lesson != "" && d.Card.Lesson != exploreLesson {
		if err := validateEntityID("Lesson", d.Card.Lesson); err != nil {
			return err
		}
	}
	if _, ok := s.study.cards[d.Card.ID]; ok {
		// Card IDs carry a random suffix, so this is a forged or repeated
		// Event rather than a collision; the first Card counts.
		s.flag(newFlag(FlagConflict, cardItem(d.Card.ID), []string{ev.ID}, "",
			fmt.Sprintf("Event %s adds Card %s, which already exists; the first Card counts", ev.ID, d.Card.ID)))
		return nil
	}
	lesson := d.Card.Lesson
	if lesson == "" {
		lesson = exploreLesson
	}
	s.study.cards[d.Card.ID] = &cardState{id: d.Card.ID, lesson: lesson, created: wallOf(ev),
		prompt: d.Card.Prompt, answer: d.Card.Answer}
	s.study.cardOrder = append(s.study.cardOrder, d.Card.ID)
	return nil
}

// cardOf returns the Card an Event names, or an error that holds the Event
// until the Card is known.
func cardOf(s *replayed, ev event, cardID string) (*cardState, error) {
	if cardID == "" {
		return nil, fmt.Errorf("its payload names no Card")
	}
	cs := s.study.cards[cardID]
	if cs == nil {
		return nil, fmt.Errorf("%w: Card %s", errUnknownItem, cardID)
	}
	return cs, nil
}

func replayCardEdited(s *replayed, ev event) error {
	var d cardEditedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	cs, err := cardOf(s, ev, d.Card)
	if err != nil {
		return err
	}
	if cs.gone() {
		goneConflict(s, cs, ev, "edited")
		return nil
	}
	cs.flaggedBy, cs.flagNote = "", ""
	if d.Prompt != "" {
		cs.prompt = d.Prompt
	}
	if d.Answer != "" {
		cs.answer = d.Answer
	}
	return nil
}

func replayCardSuspension(suspended bool) func(*replayed, event) error {
	return func(s *replayed, ev event) error {
		var d cardRefData
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return fmt.Errorf("its payload is unreadable: %v", err)
		}
		cs, err := cardOf(s, ev, d.Card)
		if err != nil {
			return err
		}
		if !cs.gone() {
			cs.suspended = suspended
		}
		return nil
	}
}

func replayCardDeleted(s *replayed, ev event) error {
	var d cardDeletedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	cs, err := cardOf(s, ev, d.Card)
	if err != nil {
		return err
	}
	if cs.gone() {
		// Deleted on two machines, or deleted after a drop: both agree
		// the Card is gone.
		return nil
	}
	if missed := len(cs.reviews) - d.Reviews; missed > 0 {
		// Reviewed on another machine that the deleting one had not synced
		// with: the Card stays deleted, and the learner decides.
		s.flag(newFlag(FlagConflict, cardItem(cs.id), []string{ev.ID}, "",
			fmt.Sprintf("Card %s was deleted by Event %s on a machine that had not seen its %d latest %s, probably "+
				"made on another machine: it stays deleted; check it with the learner",
				cs.id, ev.ID, missed, pluralWord(missed, "Review", "Reviews"))))
	}
	cs.deleted, cs.droppedBy = true, ev.ID
	cs.flaggedBy, cs.flagNote = "", ""
	return nil
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func replayCardFlagged(s *replayed, ev event) error {
	var d cardFlaggedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	cs, err := cardOf(s, ev, d.Card)
	if err != nil {
		return err
	}
	if !cs.gone() {
		cs.flaggedBy, cs.flagNote = ev.ID, d.Note
	}
	return nil
}
