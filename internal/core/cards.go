package core

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Cards live in cards.jsonl, one JSON object per line, each an item of its
// own ("cards.jsonl#<card-id>"), in the order they were written:
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
	// Evidence lists the ids of the Evidence the Card relies on.
	Evidence []string `json:"evidence,omitempty"`
}

// CardDraft is a new Card's content.
type CardDraft struct {
	Prompt   string   `json:"prompt" jsonschema:"the question, without its answer"`
	Answer   string   `json:"answer" jsonschema:"the expected answer"`
	Evidence []string `json:"evidence,omitempty" jsonschema:"ids of the Evidence the Card relies on, from evidence"`
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
	// Evidence lists the ids of the Evidence the Card relies on.
	Evidence []string `json:"evidence,omitempty"`
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
	// decision is that first decision, so a retry is recognised.
	decision *reviewRecordedData
	// dueAt caches due, once dueKnown.
	dueAt    time.Time
	dueKnown bool
}

type review struct {
	at     time.Time
	rating string
}

func (cs *cardState) draft() bool { return len(cs.reviews) == 0 }

// gone reports whether the Card is no longer one: dropped at its first
// Review or deleted.
func (cs *cardState) gone() bool { return cs.dropped || cs.deleted }

// due is when the Card is next due; zero for a draft. Replay is over when
// anyone asks, so it is scheduled once.
func (cs *cardState) due() time.Time {
	if !cs.dueKnown {
		cs.dueAt, cs.dueKnown = schedule(cs.reviews), true
	}
	return cs.dueAt
}

// lessonShown is the Lesson a Card names: empty for an Explore Card.
func (cs *cardState) lessonShown() string {
	if cs.lesson == exploreLesson {
		return ""
	}
	return cs.lesson
}

func cleanDraft(d CardDraft) (CardDraft, error) {
	prompt, err := cardText("Card prompt", d.Prompt)
	if err != nil {
		return d, err
	}
	answer, err := cardText("Card answer", d.Answer)
	if err != nil {
		return d, err
	}
	return CardDraft{Prompt: prompt, Answer: answer, Evidence: d.Evidence}, nil
}

// CheckCardDraft checks a Card's prompt and answer as adding or editing it
// would, and returns them cleaned, so an adapter can ask again before
// recording anything.
func CheckCardDraft(d CardDraft) (CardDraft, error) { return cleanDraft(d) }

// cardText cleans a Card's prompt or answer. Unlike other text, it may span
// lines and hold tabs, as code does; line endings become "\n", and other
// control characters, which could rewrite a terminal, are refused.
func cardText(field, s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the %s is not valid UTF-8 text", field)
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s = strings.TrimSpace(s)
	for _, r := range s {
		if (unicode.IsControl(r) && r != '\n' && r != '\t') || unicode.Is(unicode.Bidi_Control, r) {
			return "", invalidf("the %s contains a control character", field)
		}
	}
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", invalidf("the %s is empty", field)
	case n > maxCardRunes:
		return "", invalidf("the %s is longer than %d characters", field, maxCardRunes)
	}
	return s, nil
}

// cardIDPattern is the shape of every Card ID newCardID makes.
var cardIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]{1,26}$`)

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

// patchCard replaces a Card line's prompt, answer and Evidence, those given,
// keeping every other field, including fields this version of study does
// not know. An empty Evidence list removes the field.
func patchCard(cardID string, current []byte, prompt, answer string, evidence *[]string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, corruptf("Card %s in %s is not valid: %v", cardID, cardsFile, err)
	}
	set := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return internalError("encoding a Card", err)
		}
		fields[key] = raw
		return nil
	}
	for key, value := range map[string]string{"prompt": prompt, "answer": answer} {
		if value != "" {
			if err := set(key, value); err != nil {
				return nil, err
			}
		}
	}
	if evidence != nil {
		if len(*evidence) == 0 {
			delete(fields, "evidence")
		} else if err := set("evidence", *evidence); err != nil {
			return nil, err
		}
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
	// Request is the client's id for the Review, so a retry with it
	// records nothing.
	Request string `json:"request,omitempty"`
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
	// Request is an optional id the client chooses for this Review. A
	// retry with the same id returns the Review already recorded.
	Request string
	DryRun  bool
}

// ReviewResult is the result of RecordReview: the Card as it is after the
// Review, or would be after a dry run.
type ReviewResult struct {
	Topic   string `json:"topic"`
	Card    Card   `json:"card"`
	Rating  string `json:"rating,omitempty"`
	Dropped bool   `json:"dropped"`
	// Changed is false when the Review was already recorded, by a retry
	// with the same request or the same first decision on a draft.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
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

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// sameDecision reports whether spec repeats a draft's recorded first
// decision, as a retry after an error or a crash does.
func sameDecision(d *reviewRecordedData, spec ReviewSpec, edit CardDraft) bool {
	return d != nil && d.Draft == spec.Draft && d.Rating == spec.Rating && d.Prompt == edit.Prompt && d.Answer == edit.Answer
}

// RecordReview records one Review of a Card. At a draft's first Review the
// learner keeps, edits or drops it; a dropped Card is removed from
// cards.jsonl and never scheduled. A retry, with the same request id or
// repeating a draft's first decision, returns what was recorded and records
// nothing.
func (c *Core) RecordReview(ctx context.Context, topicID string, spec ReviewSpec) (ReviewResult, error) {
	if spec.Draft != DraftDrop || spec.Rating != "" {
		if err := checkRating(spec.Rating); err != nil {
			return ReviewResult{}, err
		}
	}
	if spec.Request != "" && !requestPattern.MatchString(spec.Request) {
		return ReviewResult{}, invalidf("the request id must be 1 to 64 letters, digits, dots, colons, hyphens or underscores")
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
	var preview []review // in a dry run, the Card's Reviews with this one
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if r, ok := s.study.requests[spec.Request]; spec.Request != "" && ok {
			if r.Card != spec.Card {
				return nil, invalidf("request %s already recorded a Review of Card %s, not %s", spec.Request, r.Card, spec.Card)
			}
			result.Rating, result.Dropped = r.Rating, r.Draft == DraftDrop
			return nil, nil
		}
		if cs := s.study.cards[spec.Card]; cs != nil && spec.Draft != "" && sameDecision(cs.decision, spec, edit) {
			return nil, nil
		}
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
		d := reviewRecordedData{Card: spec.Card, Rating: spec.Rating, Draft: spec.Draft, Prompt: edit.Prompt,
			Answer: edit.Answer, Request: spec.Request}
		ch := &change{Type: eventReviewRecorded, Data: d}
		if spec.Draft == DraftEdit || spec.Draft == DraftDrop {
			ch.Items = []string{cardItem(spec.Card)}
		}
		preview = append(append([]review{}, cs.reviews...), review{at: c.now(), rating: spec.Rating})
		return ch, nil
	}, spec.DryRun)
	if err != nil {
		return ReviewResult{}, err
	}
	result.Changed = ev != nil
	card, err := c.cardNow(ctx, topicID, spec.Card)
	if err != nil {
		return ReviewResult{}, err
	}
	result.Card = card
	if spec.DryRun && result.Changed && !result.Dropped {
		result.Card.Draft, result.Card.Due = false, schedule(preview)
		if spec.Draft == DraftEdit {
			result.Card.Prompt, result.Card.Answer, result.Card.Flagged = edit.Prompt, edit.Answer, false
		}
	}
	return result, nil
}

// cardNow reads one Card as it is now, dropped and deleted Cards included,
// with the content the History knows once its line is gone.
func (c *Core) cardNow(ctx context.Context, topicID, cardID string) (Card, error) {
	if err := ctx.Err(); err != nil {
		return Card{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return Card{}, err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return Card{}, err
	}
	s := replayHistory(h)
	cs := s.study.cards[cardID]
	switch {
	case cs == nil:
		return Card{ID: cardID}, nil
	case cs.gone():
		return Card{ID: cardID, Lesson: cs.lessonShown(), Prompt: cs.prompt, Answer: cs.answer}, nil
	}
	card, err := readCard(newView(topic, s), s, cs)
	if err != nil && CodeOf(err) != CodeCorrupt {
		return Card{}, err
	}
	return card, nil
}

// readCard reads a Card's content from cards.jsonl, which is authoritative
// for text: a hand edit wins. Its state comes from replay.
func readCard(view *topicView, s *replayed, cs *cardState) (Card, error) {
	data, exists, err := view.read(cardItem(cs.id))
	if err != nil {
		return Card{}, err
	}
	card := Card{ID: cs.id, Number: s.study.cardNumber(cs.id), Lesson: cs.lessonShown(), Draft: cs.draft(),
		Suspended: cs.suspended, Flagged: s.cardFlagged(cs), Due: cs.due()}
	if !exists {
		return card, &Error{Code: CodeCorrupt, Message: fmt.Sprintf("Card %s is missing from %s", cs.id, cardsFile)}
	}
	var line cardLine
	if err := json.Unmarshal(data, &line); err != nil {
		return card, corruptf("Card %s in %s is not valid: %v", cs.id, cardsFile, err)
	}
	card.Prompt, card.Answer, card.Evidence = line.Prompt, line.Answer, line.Evidence
	return card, nil
}

// cardNumber is a Card's display number: its position among the Cards not
// dropped or deleted, in the order they were written, from 1. The numbers
// are counted once per replay.
func (st *studyState) cardNumber(id string) int {
	if st.numbers == nil {
		st.numbers = make(map[string]int, len(st.cardOrder))
		n := 0
		for _, other := range st.cardOrder {
			if !st.cards[other].gone() {
				n++
				st.numbers[other] = n
			}
		}
	}
	return st.numbers[id]
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
	data, err := patchCard(d.Card, current, d.Prompt, d.Answer, nil)
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
	if d.Request != "" {
		if _, ok := s.study.requests[d.Request]; !ok {
			s.study.requests[d.Request] = d
		}
	}
	if cs.gone() {
		goneConflict(s, cs, ev, "reviewed")
		return nil
	}
	if d.Draft != "" {
		if cs.decision != nil {
			// The draft was decided on another machine too, before
			// syncing: the first decision counts, and the learner checks.
			s.flag(newFlag(FlagConflict, cardItem(cs.id), []string{ev.ID}, "",
				fmt.Sprintf("Card %s was decided twice at its first Review, probably on two machines: "+
					"the first decision (%s) counts and Event %s's (%s) was set aside; check it with the learner",
					cs.id, cs.decision.Draft, ev.ID, d.Draft)))
			return nil
		}
		cs.decision, cs.decided = &d, wallOf(ev)
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
	if _, ok := dueLimitByEnergy[q.Energy]; q.Energy != "" && !ok {
		return 0, "", invalidf("energy must be full, half or fumes, not %q", q.Energy)
	}
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
// are never offered. It never says how many more Cards are due. status and
// session_open say only whether Cards are ready (see cardsReady).
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
	view := newView(topic, s)
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
	view := newView(topic, s)
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
		if cs := st.cards[id]; !cs.gone() && cs.flaggedBy != "" {
			flags = append(flags, cardFlag(cs))
		}
	}
	return flags
}

// cardFlag is the flag of a Card the learner flagged. Its ID follows the
// card.flagged Event, so flagging the Card again after a dismissal is a new
// flag.
func cardFlag(cs *cardState) Flag {
	msg := fmt.Sprintf("the learner flagged Card %s during a Review as wrong or unclear", cs.id)
	if cs.flagNote != "" {
		msg += ": " + cs.flagNote
	}
	msg += "; fix it with card_edit, or delete it"
	return newFlag(FlagCardFlagged, cardItem(cs.id), []string{cs.flaggedBy}, "", msg)
}

// cardFlagged reports whether a Card carries a flag the learner has not
// dismissed.
func (s *replayed) cardFlagged(cs *cardState) bool {
	if cs.flaggedBy == "" {
		return false
	}
	_, dismissed := s.dismissed[cardFlag(cs).ID]
	return !dismissed
}
