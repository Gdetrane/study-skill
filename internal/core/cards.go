package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Cards live in cards.jsonl, one JSON object per line, each an item of its
// own ("cards.jsonl#<card-id>"):
//
//	{"format":1,"id":"pointers.k3x9a2bq","lesson":"pointers","prompt":"…","answer":"…"}
//
// The file holds content only. Whether a Card is a draft, when it is due and
// whether it was dropped come from replaying the History: scheduling is
// deterministic (see schedule), so every replay gives the same schedule.
//
// TODO(#26): Explore Cards, card_add, card_edit, card_suspend and
// card_delete, a daily cap on new Cards, Energy-sized Reviews, paused
// Topics, terminal Reviews, and keeping cards.jsonl sorted by ID.

const cardsFile = "cards.jsonl"

const eventReviewRecorded = "review.recorded"

const (
	maxCardRunes    = 1000
	maxCardsPerCall = 20
	// DefaultDueLimit and MaxDueLimit bound due_cards.
	DefaultDueLimit = 10
	MaxDueLimit     = 100
)

// Ratings of a Review.
const (
	RatingAgain = "again"
	RatingHard  = "hard"
	RatingGood  = "good"
	RatingEasy  = "easy"
)

// What the learner does with a draft Card at its first Review.
const (
	DraftKeep = "keep"
	DraftEdit = "edit"
	DraftDrop = "drop"
)

func cardItem(id string) string { return cardsFile + "#" + id }

// cardLine is one line of cards.jsonl.
type cardLine struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	Lesson string `json:"lesson,omitempty"`
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
}

// CardDraft is a new Card's content.
type CardDraft struct {
	Prompt string `json:"prompt" jsonschema:"the question, without its answer"`
	Answer string `json:"answer" jsonschema:"the expected answer"`
}

// Card is a Card with its state, computed from the History.
type Card struct {
	ID     string `json:"id"`
	Lesson string `json:"lesson,omitempty"`
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
	// Draft is true until the Card's first Review.
	Draft bool `json:"draft"`
	// Due is when the Card is next due; zero for a draft.
	Due time.Time `json:"due,omitzero"`
}

// cardState is one Card as replay knows it.
type cardState struct {
	id      string
	lesson  string
	created time.Time
	reviews []review
	// dropped is set once the learner dropped the draft, by Event
	// droppedBy.
	dropped   bool
	droppedBy string
}

type review struct {
	at     time.Time
	rating string
}

func (cs *cardState) draft() bool { return len(cs.reviews) == 0 }

// due is when the Card is next due; zero for a draft.
func (cs *cardState) due() time.Time { return schedule(cs.reviews) }

func cleanDraft(d CardDraft) (CardDraft, error) {
	prompt, err := requiredText("Card prompt", d.Prompt, maxCardRunes)
	if err != nil {
		return d, err
	}
	answer, err := requiredText("Card answer", d.Answer, maxCardRunes)
	if err != nil {
		return d, err
	}
	return CardDraft{Prompt: prompt, Answer: answer}, nil
}

// newCardID returns a Card ID that cannot collide across machines:
// <lesson-id>.<random suffix>.
func (c *Core) newCardID(s *replayed, lessonID string) string {
	for {
		id := lessonID + "." + randomID()[:8]
		if _, taken := s.study.cards[id]; !taken {
			return id
		}
	}
}

func encodeCard(line cardLine) ([]byte, error) {
	line.Format = FormatVersion
	data, err := json.Marshal(line)
	if err != nil {
		return nil, internalError("encoding a Card", err)
	}
	return data, nil
}

// reviewRecordedData is the payload of a review.recorded Event.
type reviewRecordedData struct {
	Card string `json:"card"`
	// Rating is empty when a draft is dropped.
	Rating string `json:"rating,omitempty"`
	// Draft is what the learner did with a draft at its first Review.
	Draft  string `json:"draft,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Answer string `json:"answer,omitempty"`
}

// ReviewSpec describes a Review.
type ReviewSpec struct {
	Card string
	// Rating is again, hard, good or easy. Not needed when dropping a
	// draft.
	Rating string
	// Draft is keep, edit or drop, and is required at a draft's first
	// Review.
	Draft string
	// Prompt and Answer replace the Card's content when editing a draft.
	Prompt string
	Answer string
	DryRun bool
}

// ReviewResult is the result of RecordReview.
type ReviewResult struct {
	Topic   string `json:"topic"`
	Card    Card   `json:"card"`
	Rating  string `json:"rating,omitempty"`
	Dropped bool   `json:"dropped"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

func checkRating(r string) error {
	switch r {
	case RatingAgain, RatingHard, RatingGood, RatingEasy:
		return nil
	}
	return invalidf("rating must be again, hard, good or easy, not %q", r)
}

// RecordReview records one Review of a Card. At a draft's first Review the
// learner keeps, edits or drops it; a dropped Card is removed from
// cards.jsonl and never scheduled.
func (c *Core) RecordReview(ctx context.Context, topicID string, spec ReviewSpec) (ReviewResult, error) {
	if spec.Draft != DraftDrop || spec.Rating != "" {
		if err := checkRating(spec.Rating); err != nil {
			return ReviewResult{}, err
		}
	}
	var edit CardDraft
	switch spec.Draft {
	case "", DraftKeep, DraftDrop:
		if spec.Prompt != "" || spec.Answer != "" {
			return ReviewResult{}, invalidf("a new prompt or answer is only taken with draft edit")
		}
	case DraftEdit:
		var err error
		if edit, err = cleanDraft(CardDraft{Prompt: spec.Prompt, Answer: spec.Answer}); err != nil {
			return ReviewResult{}, err
		}
	default:
		return ReviewResult{}, invalidf("draft must be keep, edit or drop, not %q", spec.Draft)
	}
	result := ReviewResult{Topic: topicID, Rating: spec.Rating, Dropped: spec.Draft == DraftDrop, DryRun: spec.DryRun}
	_, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		cs := s.study.cards[spec.Card]
		if cs == nil || cs.dropped {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf("Topic %s has no Card %s", topicID, spec.Card)}
		}
		switch {
		case cs.draft() && spec.Draft == "":
			return nil, invalidf("Card %s is a draft: at its first Review, say whether the learner keeps, edits or drops it", spec.Card)
		case !cs.draft() && spec.Draft != "":
			return nil, invalidf("Card %s is not a draft any more", spec.Card)
		}
		d := reviewRecordedData{Card: spec.Card, Rating: spec.Rating, Draft: spec.Draft, Prompt: edit.Prompt, Answer: edit.Answer}
		ch := &change{Type: eventReviewRecorded, Data: d}
		if spec.Draft == DraftEdit || spec.Draft == DraftDrop {
			ch.Items = []string{cardItem(spec.Card)}
		}
		card, err := readCard(view, cs)
		if err != nil {
			return nil, err
		}
		if spec.Draft == DraftEdit {
			card.Prompt, card.Answer = edit.Prompt, edit.Answer
		}
		result.Card = card
		return ch, nil
	}, spec.DryRun)
	if err != nil {
		return ReviewResult{}, err
	}
	if !result.Dropped {
		s, _, err := c.replayTopic(ctx, topicID)
		if err != nil {
			return ReviewResult{}, err
		}
		if cs := s.study.cards[spec.Card]; cs != nil {
			result.Card.Draft, result.Card.Due = cs.draft(), cs.due()
		}
	}
	return result, nil
}

// readCard reads a Card's content from cards.jsonl, which is authoritative
// for text: a hand edit wins.
func readCard(view *topicView, cs *cardState) (Card, error) {
	data, exists, err := view.read(cardItem(cs.id))
	if err != nil {
		return Card{}, err
	}
	card := Card{ID: cs.id, Lesson: cs.lesson, Draft: cs.draft(), Due: cs.due()}
	if !exists {
		return card, &Error{Code: CodeCorrupt, Message: fmt.Sprintf("Card %s is missing from %s", cs.id, cardsFile)}
	}
	var line cardLine
	if err := json.Unmarshal(data, &line); err != nil {
		return card, corruptf("Card %s in %s is not valid: %v", cs.id, cardsFile, err)
	}
	card.Prompt, card.Answer = line.Prompt, line.Answer
	return card, nil
}

func applyReviewRecorded(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d reviewRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	if item != cardItem(d.Card) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if d.Draft == DraftDrop {
		return nil, false, nil
	}
	if !exists {
		return nil, false, corruptf("Card %s is missing from %s", d.Card, cardsFile)
	}
	// Only the prompt and the answer change; fields this version of study
	// does not know are kept.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, false, corruptf("Card %s in %s is not valid: %v", d.Card, cardsFile, err)
	}
	for key, value := range map[string]string{"prompt": d.Prompt, "answer": d.Answer} {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false, internalError("encoding a Card", err)
		}
		fields[key] = raw
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, false, internalError("encoding a Card", err)
	}
	return data, true, nil
}

func replayReviewRecorded(s *replayed, ev event) error {
	var d reviewRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	cs := s.study.cards[d.Card]
	if cs == nil {
		return fmt.Errorf("%w: Card %s", errUnknownItem, d.Card)
	}
	if cs.dropped {
		// Dropped on one machine, reviewed on another: the Card stays
		// dropped, and the learner decides.
		s.flag(newFlag(FlagConflict, cardItem(d.Card), []string{cs.droppedBy, ev.ID}, "",
			fmt.Sprintf("Card %s was dropped by Event %s and then reviewed by Event %s, probably on two machines: "+
				"it stays dropped; check it with the learner", d.Card, cs.droppedBy, ev.ID)))
		return nil
	}
	if d.Draft == DraftDrop {
		cs.dropped, cs.droppedBy = true, ev.ID
		return nil
	}
	if err := checkRating(d.Rating); err != nil {
		return err
	}
	cs.reviews = append(cs.reviews, review{at: wallOf(ev), rating: d.Rating})
	return nil
}

// DueCards are the Cards to review now: drafts waiting for their first
// Review, and Cards due. It never reports how many more there are.
type DueCards struct {
	Topic string `json:"topic"`
	Cards []Card `json:"cards"`
}

// DueCardsOf returns up to limit Cards to review now, those due first,
// earliest first, then drafts in the order they were written.
func (c *Core) DueCardsOf(ctx context.Context, topicID string, limit int) (DueCards, error) {
	switch {
	case limit < 0:
		return DueCards{}, invalidf("the limit must be positive")
	case limit == 0:
		limit = DefaultDueLimit
	case limit > MaxDueLimit:
		limit = MaxDueLimit
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return DueCards{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return DueCards{}, err
	}
	defer home.Close()
	defer topic.Close()
	view := &topicView{root: topic}
	now := c.now()
	var due, drafts []*cardState
	for _, id := range s.study.cardOrder {
		cs := s.study.cards[id]
		switch {
		case cs.dropped:
		case cs.draft():
			drafts = append(drafts, cs)
		case !cs.due().After(now):
			due = append(due, cs)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].due().Before(due[j].due()) })
	out := DueCards{Topic: topicID, Cards: []Card{}}
	for _, cs := range append(due, drafts...) {
		if len(out.Cards) == limit {
			break
		}
		card, err := readCard(view, cs)
		if err != nil {
			// A Card missing from cards.jsonl, removed by hand, is not
			// offered; status's flags are where damage is reported.
			c.log.Warn("skipping a Card that cannot be read", "topic", topicID, "card", cs.id, "err", err)
			continue
		}
		out.Cards = append(out.Cards, card)
	}
	return out, nil
}
