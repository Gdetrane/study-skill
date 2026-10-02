package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Hints: help the agent gives while the learner practises. Each hint is a
// hint.recorded Event, one of the signals a future Level suggestion needs.

const (
	eventHintRecorded = "hint.recorded"

	maxHintNoteRunes = 1000
)

// Kinds of hint, from lightest to strongest.
const (
	// HintNudge is a question or a pointer that leaves the learner to find
	// the way.
	HintNudge = "nudge"
	// HintExplanation explains a concept again.
	HintExplanation = "explanation"
	// HintStep shows part of the way to a solution.
	HintStep = "step"
)

func init() {
	eventKinds[eventHintRecorded] = eventKind{apply: applyNothing, replay: replayHintRecorded}
}

// hintRecordedData is the payload of a hint.recorded Event.
type hintRecordedData struct {
	Lesson  string `json:"lesson"`
	Kind    string `json:"kind"`
	Note    string `json:"note,omitempty"`
	Request string `json:"request,omitempty"`
}

// Hint is a recorded hint.
type Hint struct {
	// ID is the Event that recorded it.
	ID     string    `json:"id"`
	Lesson string    `json:"lesson"`
	Kind   string    `json:"kind"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

// HintSpec describes a hint to record.
type HintSpec struct {
	Lesson string
	// Kind is nudge, explanation or step; nudge when empty.
	Kind string
	// Note says what the hint was about, in a few words. Optional.
	Note string
	// Request is an optional id the client chooses for the hint. A retry
	// with the same id records nothing.
	Request string
	DryRun  bool
}

// HintRecorded is the result of RecordHint.
type HintRecorded struct {
	Topic string `json:"topic"`
	Hint  Hint   `json:"hint"`
	// Changed is false when a hint with the same request id was already
	// recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// RecordHint records a hint given for a Lesson the learner is studying.
// Every call records a hint, unless it repeats a request id already
// recorded.
func (c *Core) RecordHint(ctx context.Context, topicID string, spec HintSpec) (HintRecorded, error) {
	if err := checkTopicID(topicID); err != nil {
		return HintRecorded{}, err
	}
	kind := spec.Kind
	switch kind {
	case "":
		kind = HintNudge
	case HintNudge, HintExplanation, HintStep:
	default:
		return HintRecorded{}, invalidf("a hint's kind is nudge, explanation or step, not %q", clip(kind, 40))
	}
	note, err := cleanTextBlock("note", spec.Note, maxHintNoteRunes)
	if err != nil {
		return HintRecorded{}, err
	}
	if spec.Request != "" && !requestPattern.MatchString(spec.Request) {
		return HintRecorded{}, invalidf("the request id must be 1 to 64 letters, digits, dots, colons, hyphens or underscores")
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return HintRecorded{}, err
	}
	if _, err := requireStudiedLesson(s, topicID, spec.Lesson); err != nil {
		return HintRecorded{}, err
	}
	d := hintRecordedData{Lesson: spec.Lesson, Kind: kind, Note: note, Request: spec.Request}
	var repeat *Hint
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		if spec.Request != "" {
			if h, ok := s.assessing().hintRequests[spec.Request]; ok {
				if h.Lesson != spec.Lesson {
					return nil, invalidf("request %s already recorded a hint for Lesson %s, not %s", spec.Request,
						h.Lesson, spec.Lesson)
				}
				repeat = &h
				return nil, nil
			}
		}
		if ls := s.study.lessons[spec.Lesson]; ls != nil && ls.completed != nil {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + spec.Lesson + " is done: hints are " +
				"recorded while the learner studies a Lesson"}
		}
		return &change{Type: eventHintRecorded, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return HintRecorded{}, err
	}
	result := HintRecorded{Topic: topicID, Changed: ev != nil, DryRun: spec.DryRun}
	if ev == nil {
		result.Hint = *repeat
		return result, nil
	}
	result.Hint = Hint{ID: ev.ID, Lesson: d.Lesson, Kind: d.Kind, Note: d.Note, At: ev.Wall}
	if spec.DryRun {
		result.Hint.ID = ""
	}
	return result, nil
}

func replayHintRecorded(s *replayed, ev event) error {
	var d hintRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if err := validateEntityID("Lesson", d.Lesson); err != nil {
		return err
	}
	switch d.Kind {
	case HintNudge, HintExplanation, HintStep:
	default:
		return fmt.Errorf("its kind %q is not nudge, explanation or step", clip(d.Kind, 40))
	}
	st := s.assessing()
	h := Hint{ID: ev.ID, Lesson: d.Lesson, Kind: d.Kind, Note: d.Note, At: wallOf(ev)}
	st.hints = append(st.hints, h)
	if d.Request != "" {
		if _, ok := st.hintRequests[d.Request]; !ok {
			st.hintRequests[d.Request] = h
		}
	}
	return nil
}
