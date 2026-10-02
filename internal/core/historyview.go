package core

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Lesson keeps the Events about one Lesson.
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
// payload: no Held-out results, notes or quotes.
type HistoryEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// At is when the Event was written, by its writer's clock.
	At      time.Time `json:"at"`
	Summary string    `json:"summary"`
	Lesson  string    `json:"lesson,omitempty"`
	// Item is the first item the Event edited, such as "topic.toml" or
	// "cards.jsonl#<card-id>".
	Item string `json:"item,omitempty"`
}

var historyTypePattern = regexp.MustCompile(`^[a-z_.]{1,64}$`)

// HistoryOf returns a Topic's most recent Events, newest first, summarised.
// It reads the History and writes nothing.
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
	view := HistoryView{Topic: topicID, Entries: []HistoryEntry{}}
	seen := map[string]bool{}
	for i := len(h.events) - 1; i >= 0; i-- {
		ev := h.events[i]
		if seen[ev.ID] {
			continue // a union merge can repeat an Event
		}
		seen[ev.ID] = true
		entry := summarizeEvent(ev)
		if q.Type != "" && !strings.HasPrefix(ev.Type, q.Type) || q.Lesson != "" && entry.Lesson != q.Lesson {
			continue
		}
		if len(view.Entries) == q.Limit {
			view.More = true
			break
		}
		view.Entries = append(view.Entries, entry)
	}
	return view, nil
}

// summarizeEvent describes an Event in a short sentence, from the few
// fields that say what happened. Unknown types are named as they are.
func summarizeEvent(ev event) HistoryEntry {
	var d map[string]any
	_ = json.Unmarshal(ev.Data, &d)
	str := func(key string) string {
		if v, ok := d[key].(string); ok {
			return clip(v, 80)
		}
		return ""
	}
	// A Card is named by its id, or by the id inside a Card line.
	card := str("card")
	if m, ok := d["card"].(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			card = clip(id, 80)
		}
	}
	entry := HistoryEntry{ID: ev.ID, Type: ev.Type, At: wallOf(ev), Lesson: str("lesson")}
	if len(ev.Items) > 0 {
		entry.Item = ev.Items[0].Item
	}
	with := func(text, detail string) string {
		if detail == "" {
			return text
		}
		return text + ": " + detail
	}
	quoted := func(s string) string {
		if s == "" {
			return ""
		}
		return "“" + s + "”"
	}
	var text string
	switch ev.Type {
	case eventTopicCreated:
		text = with("Topic created", quoted(str("title")))
	case eventTopicUpdated:
		text = "Title or goal changed"
	case eventTopicStateSet:
		text = with("Topic state set", str("state"))
	case eventKnowledgeBaseSet:
		text = with("Knowledge base set", str("kind"))
	case eventDeadlineSet:
		if dl := str("deadline"); dl != "" {
			text = "Deadline set to " + dl
		} else {
			text = "Deadline removed"
		}
	case eventPaceSet:
		text = "Pace changed"
	case eventNewCardsPerDaySet:
		text = "Daily cap on new Cards changed"
	case eventLevelSet:
		text = with("Level set", str("level"))
	case eventApproachSet:
		text = with("Approach set", str("approach"))
	case eventTaskAdded:
		text = "Task added"
	case eventTaskRemoved:
		text = "Task removed"
	case eventTaskDone:
		text = with("Task done", str("task"))
	case eventTaskReopened:
		text = with("Task reopened", str("task"))
	case eventSourceAdded:
		text = "Source added"
	case eventSourceUpdated:
		text = "Source updated"
	case eventEvidenceRecorded:
		text = "Evidence recorded"
	case eventEvidenceRetracted:
		text = "Evidence retracted"
	case eventRevisionProposed:
		text = with("Revision proposed", quoted(str("summary")))
	case eventRevisionApplied:
		text = "Revision applied"
		if a, ok := d["approval"].(map[string]any); ok {
			if via, ok := a["via"].(string); ok && via != "" {
				text += ", approved by " + clip(via, 20)
			}
		}
	case eventRevisionDeclined:
		text = "Revision declined"
	case eventSessionOpened:
		text = "Session opened"
		if e := str("energy"); e != "" {
			text += ", Energy " + e
		}
		if f := str("focus"); f != "" {
			text += ", Focus " + f
		}
	case eventSessionFocused:
		text = with("Focus chosen", str("focus"))
	case eventSessionClosed:
		text = with("Session closed with the Next step", quoted(str("next_step")))
	case eventPhaseSet:
		text = with("Phase set", str("phase"))
	case eventBreakPointReached:
		text = with("Break point reached", str("break_point"))
	case eventAttemptRecorded:
		text = with("Attempt", str("outcome"))
	case eventRubricGraded:
		text = fmt.Sprintf("Rubric item %s graded %s", str("criterion"), str("grade"))
	case eventLessonCompleted:
		text = "Lesson completed"
	case eventHintRecorded:
		text = with("Hint given", str("kind"))
		switch str("requested_by") {
		case "learner":
			text += ", asked for by the learner"
		case "agent":
			text += ", offered by the agent"
		}
	case eventAssessmentRecorded:
		text = "Assessment recorded"
		if kind := str("kind"); kind != "" {
			text = with(text, kind)
		}
		if m := str("milestone"); m != "" {
			text += " for Milestone " + m
		}
		if l := str("level"); l != "" {
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
		if r := str("rating"); r != "" {
			text += ", rated " + r
		}
		if dr := str("draft"); dr != "" {
			text += ", draft " + dr
		}
	case eventCheckpointTaken:
		if committed, _ := d["committed"].(bool); committed {
			text = "Checkpoint saved"
			if role := str("role"); role != "" {
				text += " for the " + role + "’s turn"
			}
		} else {
			text = "Checkpoint: nothing new to save"
		}
	case eventFlagDismissed:
		text = with("Flag dismissed", str("kind"))
	default:
		text = clip(ev.Type, 64)
	}
	entry.Summary = text
	return entry
}
