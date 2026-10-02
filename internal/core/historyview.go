package core

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// History view limits, as for the Library search.
const (
	DefaultHistoryLimit = 10
	MaxHistoryLimit     = 100
)

// HistoryQuery selects the Events HistoryOf returns.
type HistoryQuery struct {
	// Limit is how many Events to return, newest first; zero means
	// DefaultHistoryLimit, and it is capped at MaxHistoryLimit.
	Limit int
	// Type keeps the Events whose type starts with it, such as "card." or
	// "attempt".
	Type string
	// Lesson keeps the Events about one Lesson, Card Events included.
	Lesson string
}

// HistoryView is a Topic's recent Events, summarised for the agent to see
// what happened. It is never a tally to show the learner.
type HistoryView struct {
	Topic   string         `json:"topic"`
	Entries []HistoryEntry `json:"entries"`
	// More is true when older Events match too.
	More bool `json:"more"`
}

// HistoryEntry is one Event, summarised. It never carries an Event's full
// payload: its summary is built only from the kind of thing that happened
// and ids, never from text anyone wrote, so no Held-out results, notes,
// quotes or Next steps.
type HistoryEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// At is when the Event was written, by its writer's clock. Entries are
	// ordered by the History's own clock, so a machine whose clock was
	// behind can make At go backwards: ClockBehind marks those entries.
	At          time.Time `json:"at"`
	ClockBehind bool      `json:"clock_behind,omitempty"`
	Summary     string    `json:"summary"`
	Lesson      string    `json:"lesson,omitempty"`
	// Item is the first item the Event edited, such as "topic.toml" or
	// "cards.jsonl#<card-id>".
	Item string `json:"item,omitempty"`
	// Held is true for an Event that was not applied, for example because
	// it refers to something the History does not hold; status flags it.
	Held bool `json:"held,omitempty"`
}

var historyTypePattern = regexp.MustCompile(`^[a-z_.]{1,64}$`)

// word is a value a summary may show: an id, a date or one of the fixed
// words of the domain (a Phase, an outcome, a grade). Anything else, such as
// text someone wrote, is left out.
var word = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// HistoryOf returns a Topic's most recent Events, newest first, summarised.
// Each Event appears once, as replay applied it; Events written by a newer
// version of study are not shown. It reads the History and writes nothing.
func (c *Core) HistoryOf(ctx context.Context, topicID string, q HistoryQuery) (HistoryView, error) {
	switch {
	case q.Limit < 0:
		return HistoryView{}, invalidf("the limit must be positive")
	case q.Limit == 0:
		q.Limit = DefaultHistoryLimit
	case q.Limit > MaxHistoryLimit:
		q.Limit = MaxHistoryLimit
	}
	if q.Type != "" && !historyTypePattern.MatchString(q.Type) {
		return HistoryView{}, invalidf("%q is not an Event type: use lowercase letters, dots and underscores, such as card. or attempt", q.Type)
	}
	if q.Lesson != "" {
		if err := validateEntityID("Lesson", q.Lesson); err != nil {
			return HistoryView{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return HistoryView{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return HistoryView{}, err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return HistoryView{}, err
	}
	held := map[string]bool{}
	for _, f := range replayHistory(h).flags {
		if f.Kind == FlagHeldEvent {
			for _, id := range f.Events {
				held[id] = true
			}
		}
	}

	// A forward pass, in the order replay applies Events: the first copy of
	// an ID is the one applied, Cards are tied to their Lessons, and a wall
	// clock that went backwards is noticed.
	var kept []HistoryEntry
	seen := map[string]bool{}
	cardLesson := map[string]string{}
	var lastWall time.Time
	for _, ev := range h.events {
		if seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		noteCardLessons(ev, cardLesson)
		wall := wallOf(ev)
		behind := !lastWall.IsZero() && wall.Before(lastWall)
		if wall.After(lastWall) {
			lastWall = wall
		}
		if q.Type != "" && !strings.HasPrefix(ev.Type, q.Type) {
			continue
		}
		entry := summarizeEvent(ev, cardLesson)
		entry.ClockBehind, entry.Held = behind, held[ev.ID]
		if q.Lesson != "" && entry.Lesson != q.Lesson {
			continue
		}
		kept = append(kept, entry)
	}
	view := HistoryView{Topic: topicID, Entries: []HistoryEntry{}}
	for i := len(kept) - 1; i >= 0; i-- {
		if len(view.Entries) == q.Limit {
			view.More = true
			break
		}
		view.Entries = append(view.Entries, kept[i])
	}
	return view, nil
}

// noteCardLessons records the Lesson of each Card an Event creates.
func noteCardLessons(ev event, cardLesson map[string]string) {
	type line struct {
		ID     string `json:"id"`
		Lesson string `json:"lesson"`
	}
	switch ev.Type {
	case eventCardAdded:
		var d struct {
			Card line `json:"card"`
		}
		if json.Unmarshal(ev.Data, &d) == nil && d.Card.ID != "" {
			cardLesson[d.Card.ID] = d.Card.Lesson
		}
	case eventLessonCompleted:
		var d struct {
			Cards []line `json:"cards"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			for _, c := range d.Cards {
				cardLesson[c.ID] = c.Lesson
			}
		}
	}
}

// summarizeEvent describes an Event in a short sentence made only of the kind
// of thing that happened and ids, never of text anyone wrote. Unknown types
// are named as they are.
func summarizeEvent(ev event, cardLesson map[string]string) HistoryEntry {
	var d map[string]any
	_ = json.Unmarshal(ev.Data, &d)
	// w returns a field only when it is a word: an id, a date or a fixed
	// value. Free text never reaches a summary.
	w := func(key string) string {
		if v, ok := d[key].(string); ok && word.MatchString(v) {
			return v
		}
		return ""
	}
	// A Card is named by its id, or by the id inside a Card line.
	card := w("card")
	if m, ok := d["card"].(map[string]any); ok {
		if id, ok := m["id"].(string); ok && word.MatchString(id) {
			card = id
		}
	}
	entry := HistoryEntry{ID: ev.ID, Type: ev.Type, At: wallOf(ev)}
	if l, _ := d["lesson"].(string); l != "" && validateEntityID("Lesson", l) == nil {
		entry.Lesson = l
	} else if l := cardLesson[card]; card != "" && l != "" && validateEntityID("Lesson", l) == nil {
		entry.Lesson = l
	}
	if len(ev.Items) > 0 {
		entry.Item = ev.Items[0].Item
	}
	with := func(text, detail string) string {
		if detail == "" {
			return text
		}
		return text + ": " + detail
	}
	var text string
	switch ev.Type {
	case eventTopicCreated:
		text = "Topic created"
	case eventTopicUpdated:
		text = "Title or goal changed"
	case eventTopicStateSet:
		text = with("Topic state set", w("state"))
	case eventKnowledgeBaseSet:
		text = with("Knowledge base set", w("kind"))
	case eventDeadlineSet:
		if dl := w("deadline"); dl != "" {
			text = "Deadline set to " + dl
		} else {
			text = "Deadline changed"
		}
	case eventPaceSet:
		text = "Pace changed"
	case eventNewCardsPerDaySet:
		text = "Daily cap on new Cards changed"
	case eventLevelSet:
		text = with("Level set", w("level"))
	case eventApproachSet:
		text = with("Approach set", w("approach"))
	case eventTaskAdded:
		text = "Task added"
	case eventTaskRemoved:
		text = "Task removed"
	case eventTaskDone:
		text = "Task done"
	case eventTaskReopened:
		text = "Task reopened"
	case eventSourceAdded:
		text = "Source added"
	case eventSourceUpdated:
		text = "Source updated"
	case eventEvidenceRecorded:
		text = "Evidence recorded"
	case eventEvidenceRetracted:
		text = "Evidence retracted"
	case eventRevisionProposed:
		text = "Revision proposed"
	case eventRevisionApplied:
		text = "Revision applied"
		if a, ok := d["approval"].(map[string]any); ok {
			if via, ok := a["via"].(string); ok && word.MatchString(via) {
				text += ", approved by " + via
			}
		}
	case eventRevisionDeclined:
		text = "Revision declined"
	case eventSessionOpened:
		text = "Session opened"
		if e := w("energy"); e != "" {
			text += ", Energy " + e
		}
		if f := w("focus"); f != "" {
			text += ", Focus " + f
		}
	case eventSessionFocused:
		text = with("Focus chosen", w("focus"))
	case eventSessionClosed:
		text = "Session closed with a Next step"
	case eventPhaseSet:
		switch phase := w("phase"); phase {
		case PhaseTeaching, PhasePracticing, PhaseFeedback:
			text = with("Phase set", phase)
		default:
			text = "Phase set"
		}
	case eventBreakPointReached:
		text = with("Break point reached", w("break_point"))
	case eventAttemptRecorded:
		text = with("Attempt", w("outcome"))
	case eventRubricGraded:
		text = "Rubric item graded"
		if crit, grade := w("criterion"), w("grade"); crit != "" && grade != "" {
			text = "Rubric item " + crit + " graded " + grade
		}
	case eventLessonCompleted:
		text = "Lesson completed"
	case eventHintRecorded:
		text = with("Hint given", w("kind"))
		switch w("requested_by") {
		case "learner":
			text += ", asked for by the learner"
		case "agent":
			text += ", offered by the agent"
		}
	case eventAssessmentRecorded:
		text = with("Assessment recorded", w("kind"))
		if m := w("milestone"); m != "" {
			text += " for Milestone " + m
		}
		if l := w("level"); l != "" {
			text += ", Level " + l
		}
	case eventCardAdded:
		text = with("Card added", card)
	case eventCardEdited:
		text = with("Card edited", card)
	case eventCardSuspended:
		text = with("Card suspended", card)
	case eventCardUnsuspended:
		text = with("Card unsuspended", card)
	case eventCardDeleted:
		text = with("Card deleted", card)
	case eventCardFlagged:
		text = with("Card flagged", card)
	case eventReviewRecorded:
		text = with("Card reviewed", card)
		if r := w("rating"); r != "" {
			text += ", rated " + r
		}
		if dr := w("draft"); dr != "" {
			text += ", draft " + dr
		}
	case eventCheckpointTaken:
		if committed, _ := d["committed"].(bool); committed {
			text = "Checkpoint saved"
			if role := w("role"); role != "" {
				text += " for the " + role + "’s turn"
			}
		} else {
			text = "Checkpoint: nothing new to save"
		}
	case eventFlagDismissed:
		text = with("Flag dismissed", w("kind"))
	default:
		if historyTypePattern.MatchString(ev.Type) {
			text = ev.Type
		} else {
			text = "An Event of an unknown type"
		}
	}
	entry.Summary = text
	return entry
}
