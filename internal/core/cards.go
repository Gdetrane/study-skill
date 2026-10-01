package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Cards live in cards.jsonl, one JSON object per line, each an item of its
// own ("cards.jsonl#<card-id>"), kept sorted by ID:
//
//	{"format":1,"id":"pointers.k3x9a2bq","lesson":"pointers","prompt":"…","answer":"…"}
//
// The file holds content only. Whether a Card is a draft, suspended, flagged,
// deleted or due comes from replaying the History: scheduling is
// deterministic (see schedule), so every replay gives the same schedule.
// Card IDs are <lesson-id>.<random suffix>, and Explore Cards, written in an
// Explore Session rather than from a Lesson, are explore.<random suffix>, so
// two machines adding Cards never pick the same ID.
//
// TODO(#29): paused Topics hide their Cards, once Topics have states.

const cardsFile = "cards.jsonl"

// Event types of Cards. A new payload shape gets a new type name.
const (
	eventReviewRecorded  = "review.recorded"
	eventCardAdded       = "card.added"
	eventCardEdited      = "card.edited"
	eventCardSuspended   = "card.suspended"
	eventCardUnsuspended = "card.unsuspended"
	eventCardDeleted     = "card.deleted"
	eventCardFlagged     = "card.flagged"
)

// exploreLesson is the ID prefix of Explore Cards.
const exploreLesson = "explore"

const (
	maxCardRunes     = 1000
	maxCardNoteRunes = 500
	maxCardsPerCall  = 20
	// DefaultDueLimit and MaxDueLimit bound due_cards.
	DefaultDueLimit = 10
	MaxDueLimit     = 100
	// NewCardsPerDay is the daily cap: at most this many draft Cards are
	// decided (kept, edited or dropped) a day, so new Cards never pile up.
	//
	// TODO(#29): a Topic setting, next to Pace.
	NewCardsPerDay = 10
)

// How many Cards a Review session offers for each Energy level.
var dueLimitByEnergy = map[string]int{
	EnergyFull:  20,
	EnergyHalf:  10,
	EnergyFumes: 3,
}

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
	ID string `json:"id"`
	// Number is the Card's display number, from its position among the
	// Topic's Cards in the order they were written. It changes when a Card
	// before it is deleted; the ID never does.
	Number int `json:"number"`
	// Lesson is the Lesson the Card was written from; empty for an Explore
	// Card.
	Lesson string `json:"lesson,omitempty"`
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
	// Draft is true until the Card's first Review.
	Draft bool `json:"draft"`
	// Suspended Cards are never offered for Review until unsuspended.
	Suspended bool `json:"suspended,omitempty"`
	// Flagged is set when the learner flagged the Card during a Review, as
	// wrong or unclear, until it is edited, deleted or the flag dismissed.
	Flagged bool `json:"flagged,omitempty"`
	// Due is when the Card is next due; zero for a draft.
	Due time.Time `json:"due,omitzero"`
}

// cardState is one Card as replay knows it.
type cardState struct {
	id      string
	lesson  string
	created time.Time
	// prompt and answer are the content the History recorded last. The
	// file is authoritative for text, so a hand edit wins when the Card is
	// shown; this is for recognising a Card added twice.
	prompt  string
	answer  string
	reviews []review
	// dropped is set once the learner dropped the draft at its first
	// Review, and deleted once the Card was deleted; by names the Event.
	dropped   bool
	deleted   bool
	droppedBy string
	suspended bool
	// flaggedBy is the card.flagged Event that flagged the Card, and
	// flagNote the learner's note, until the Card is edited.
	flaggedBy string
	flagNote  string
	// decided is when the draft was decided: kept, edited or dropped at
	// its first Review. It counts towards that day's cap on new Cards.
	decided time.Time
}

type review struct {
	at     time.Time
	rating string
}

func (cs *cardState) draft() bool { return len(cs.reviews) == 0 }

// gone reports whether the Card is no longer one: dropped at its first
// Review or deleted.
func (cs *cardState) gone() bool { return cs.dropped || cs.deleted }

// due is when the Card is next due; zero for a draft.
func (cs *cardState) due() time.Time { return schedule(cs.reviews) }

// lessonShown is the Lesson a Card names: empty for an Explore Card.
func (cs *cardState) lessonShown() string {
	if cs.lesson == exploreLesson {
		return ""
	}
	return cs.lesson
}

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
// <prefix>.<random suffix>, where prefix is a Lesson's ID or "explore".
func (c *Core) newCardID(s *replayed, prefix string) string {
	for {
		id := prefix + "." + randomID()[:8]
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

// patchCard replaces a Card line's prompt and answer, those given, keeping
// every other field, including fields this version of study does not know.
func patchCard(cardID string, current []byte, prompt, answer string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, corruptf("Card %s in %s is not valid: %v", cardID, cardsFile, err)
	}
	for key, value := range map[string]string{"prompt": prompt, "answer": answer} {
		if value == "" {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, internalError("encoding a Card", err)
		}
		fields[key] = raw
	}
	data, err := json.Marshal(fields)
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

// noSuchCard is the error for a Card the Topic does not have, or no longer
// has.
func noSuchCard(topicID, cardID string) error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf("Topic %s has no Card %s", topicID, cardID)}
}

// liveCard returns a Card of the Topic that is not dropped or deleted.
func liveCard(s *replayed, topicID, cardID string) (*cardState, error) {
	if strings.TrimSpace(cardID) == "" {
		return nil, invalidf("name the Card: due_cards and study card list show their ids")
	}
	cs := s.study.cards[cardID]
	if cs == nil || cs.gone() {
		return nil, noSuchCard(topicID, cardID)
	}
	return cs, nil
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
		cs, err := liveCard(s, topicID, spec.Card)
		if err != nil {
			return nil, err
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
		card, err := readCard(view, s, cs)
		if err != nil {
			return nil, err
		}
		if spec.Draft == DraftEdit {
			card.Prompt, card.Answer, card.Flagged = edit.Prompt, edit.Answer, false
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
// for text: a hand edit wins. Its state comes from replay.
func readCard(view *topicView, s *replayed, cs *cardState) (Card, error) {
	data, exists, err := view.read(cardItem(cs.id))
	if err != nil {
		return Card{}, err
	}
	card := Card{ID: cs.id, Number: s.study.cardNumber(cs.id), Lesson: cs.lessonShown(), Draft: cs.draft(),
		Suspended: cs.suspended, Flagged: cs.flaggedBy != "", Due: cs.due()}
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

// cardNumber is a Card's display number: its position among the Cards not
// dropped or deleted, in the order they were written, from 1.
func (st *studyState) cardNumber(id string) int {
	n := 0
	for _, other := range st.cardOrder {
		cs := st.cards[other]
		if cs.gone() {
			continue
		}
		n++
		if other == id {
			return n
		}
	}
	return 0
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
	data, err := patchCard(d.Card, current, d.Prompt, d.Answer)
	return data, err == nil, err
}

// goneConflict flags a change to a Card that another Event dropped or
// deleted, as a change made on another machine before syncing would be. The
// Card stays gone, and the learner decides.
func goneConflict(s *replayed, cs *cardState, ev event, what string) {
	how := "dropped"
	if cs.deleted {
		how = "deleted"
	}
	s.flag(newFlag(FlagConflict, cardItem(cs.id), []string{cs.droppedBy, ev.ID}, "",
		fmt.Sprintf("Card %s was %s by Event %s and then %s by Event %s, probably on two machines: "+
			"it stays %s; check it with the learner", cs.id, how, cs.droppedBy, what, ev.ID, how)))
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
	if cs.gone() {
		goneConflict(s, cs, ev, "reviewed")
		return nil
	}
	if d.Draft != "" && cs.decided.IsZero() {
		cs.decided = wallOf(ev)
	}
	if d.Draft == DraftDrop {
		cs.dropped, cs.droppedBy = true, ev.ID
		return nil
	}
	if d.Draft == DraftEdit {
		cs.flaggedBy, cs.flagNote = "", ""
		cs.prompt, cs.answer = d.Prompt, d.Answer
	}
	if err := checkRating(d.Rating); err != nil {
		return err
	}
	cs.reviews = append(cs.reviews, review{at: wallOf(ev), rating: d.Rating})
	return nil
}

// DueQuery sizes a list of Cards to review.
type DueQuery struct {
	// Limit is the most Cards to return. Zero sizes the list to Energy.
	Limit int
	// Energy sizes the list when no limit is given: full, half or fumes.
	// Empty means the Energy of the open Session, if any.
	Energy string
}

// DueCards are the Cards to review now: drafts waiting for their first
// Review, and Cards due. It never reports how many more there are.
type DueCards struct {
	Topic string `json:"topic"`
	// Energy is the Energy the list was sized to, when it was.
	Energy string `json:"energy,omitempty"`
	Cards  []Card `json:"cards"`
}

// dueLimit returns how many Cards to offer, and the Energy that decided it.
func dueLimit(s *replayed, q DueQuery) (int, string, error) {
	switch {
	case q.Limit < 0:
		return 0, "", invalidf("the limit must be positive")
	case q.Limit > MaxDueLimit:
		return MaxDueLimit, "", nil
	case q.Limit > 0:
		return q.Limit, "", nil
	}
	energy := q.Energy
	if energy == "" {
		if open := s.study.openSession(); open != nil {
			energy = open.energy
		}
	}
	if energy == "" {
		return DefaultDueLimit, "", nil
	}
	n, ok := dueLimitByEnergy[energy]
	if !ok {
		return 0, "", invalidf("energy must be full, half or fumes, not %q", energy)
	}
	return n, energy, nil
}

// openSession is the latest Session, while it is still open.
func (st *studyState) openSession() *sessionState {
	if n := len(st.sessions); n > 0 && !st.sessions[n-1].closed {
		return st.sessions[n-1]
	}
	return nil
}

// draftsLeftToday is how many more drafts can be decided today under the
// daily cap. A day is a calendar day in the location of now.
func (st *studyState) draftsLeftToday(now time.Time) int {
	y, m, d := now.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 1)
	decided := 0
	for _, cs := range st.cards {
		if !cs.decided.IsZero() && !cs.decided.Before(start) && cs.decided.Before(end) {
			decided++
		}
	}
	return max(0, NewCardsPerDay-decided)
}

// DueCardsOf returns the Cards to review now, sized to the limit or the
// Energy: those due first, earliest first, then drafts in the order they
// were written, as many as today's cap on new Cards allows. Suspended Cards
// are never offered. It never says how many more Cards are due.
func (c *Core) DueCardsOf(ctx context.Context, topicID string, q DueQuery) (DueCards, error) {
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return DueCards{}, err
	}
	limit, energy, err := dueLimit(s, q)
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
		case cs.gone() || cs.suspended:
		case cs.draft():
			drafts = append(drafts, cs)
		case !cs.due().After(now):
			due = append(due, cs)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].due().Before(due[j].due()) })
	if left := s.study.draftsLeftToday(now); len(drafts) > left {
		drafts = drafts[:left]
	}
	out := DueCards{Topic: topicID, Energy: energy, Cards: []Card{}}
	for _, cs := range append(due, drafts...) {
		if len(out.Cards) == limit {
			break
		}
		card, err := readCard(view, s, cs)
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

// CardQuery selects Cards to list.
type CardQuery struct {
	// Lesson keeps only the Cards written from one Lesson; "explore" keeps
	// the Explore Cards.
	Lesson string
}

// CardList lists a Topic's Cards.
type CardList struct {
	Topic string `json:"topic"`
	Cards []Card `json:"cards"`
}

// ListCards lists a Topic's Cards in the order they were written, with
// their state; dropped and deleted Cards are left out.
func (c *Core) ListCards(ctx context.Context, topicID string, q CardQuery) (CardList, error) {
	if q.Lesson != "" && q.Lesson != exploreLesson {
		if err := validateEntityID("Lesson", q.Lesson); err != nil {
			return CardList{}, err
		}
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return CardList{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return CardList{}, err
	}
	defer home.Close()
	defer topic.Close()
	view := &topicView{root: topic}
	out := CardList{Topic: topicID, Cards: []Card{}}
	for _, id := range s.study.cardOrder {
		cs := s.study.cards[id]
		if cs.gone() || (q.Lesson != "" && cs.lesson != q.Lesson) {
			continue
		}
		card, err := readCard(view, s, cs)
		if err != nil {
			c.log.Warn("skipping a Card that cannot be read", "topic", topicID, "card", cs.id, "err", err)
			continue
		}
		out.Cards = append(out.Cards, card)
	}
	return out, nil
}

// cardFlags are the Cards the learner flagged during a Review, as flags in
// status, until each is edited, deleted or its flag dismissed.
func (st *studyState) cardFlags() []Flag {
	var flags []Flag
	for _, id := range st.cardOrder {
		cs := st.cards[id]
		if cs.gone() || cs.flaggedBy == "" {
			continue
		}
		msg := fmt.Sprintf("the learner flagged Card %s during a Review as wrong or unclear", id)
		if cs.flagNote != "" {
			msg += ": " + cs.flagNote
		}
		msg += "; fix it with card_edit, or delete it"
		flags = append(flags, newFlag(FlagCardFlagged, cardItem(id), []string{cs.flaggedBy}, "", msg))
	}
	return flags
}
