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

// Who asked for a hint.
const (
	// HintByLearner: the learner asked for help.
	HintByLearner = "learner"
	// HintByAgent: the agent offered it, unasked.
	HintByAgent = "agent"
)

func init() {
	eventKinds[eventHintRecorded] = eventKind{apply: applyNothing, replay: replayHintRecorded}
}

// hintRecordedData is the payload of a hint.recorded Event.
type hintRecordedData struct {
	Lesson      string `json:"lesson"`
	Kind        string `json:"kind"`
	RequestedBy string `json:"requested_by"`
	Note        string `json:"note,omitempty"`
	Request     string `json:"request,omitempty"`
}

// Hint is a recorded hint.
type Hint struct {
	// ID is the Event that recorded it.
	ID     string `json:"id"`
	Lesson string `json:"lesson"`
	Kind   string `json:"kind"`
	// RequestedBy is learner when the learner asked for it, agent when the
	// agent offered it.
	RequestedBy string    `json:"requested_by"`
	Note        string    `json:"note,omitempty"`
	At          time.Time `json:"at"`
}

// HintSpec describes a hint to record.
type HintSpec struct {
	Lesson string
	// Kind is nudge, explanation or step; nudge when empty.
	Kind string
	// RequestedBy is learner or agent. Required.
	RequestedBy string
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

// checkHint validates a hint, written or read back from the History.
func checkHint(d hintRecordedData) (hintRecordedData, error) {
	if err := validateEntityID("Lesson", d.Lesson); err != nil {
		return d, err
	}
	switch d.Kind {
	case "":
		d.Kind = HintNudge
	case HintNudge, HintExplanation, HintStep:
	default:
		return d, invalidf("a hint's kind is nudge, explanation or step, not %q", clip(d.Kind, 40))
	}
	switch d.RequestedBy {
	case HintByLearner, HintByAgent:
	case "":
		return d, invalidf("say who asked for the hint (requested_by): learner, when the learner asked for help, " +
			"or agent, when it was offered unasked")
	default:
		return d, invalidf("a hint is requested by the learner or the agent, not %q", clip(d.RequestedBy, 40))
	}
	note, err := cleanTextBlock("note", d.Note, maxHintNoteRunes)
	if err != nil {
		return d, err
	}
	d.Note = note
	if d.Request != "" && !requestPattern.MatchString(d.Request) {
		return d, invalidf("the request id must be 1 to 64 letters, digits, dots, colons, hyphens or underscores")
	}
	return d, nil
}

// RecordHint records a hint given for a Lesson the learner is studying.
// Every call records a hint, unless it repeats a request id already
// recorded.
func (c *Core) RecordHint(ctx context.Context, topicID string, spec HintSpec) (HintRecorded, error) {
	if err := checkTopicID(topicID); err != nil {
		return HintRecorded{}, err
	}
	d, err := checkHint(hintRecordedData{Lesson: spec.Lesson, Kind: spec.Kind, RequestedBy: spec.RequestedBy,
		Note: spec.Note, Request: spec.Request})
	if err != nil {
		return HintRecorded{}, err
	}
	var repeat *Hint
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		if d.Request != "" {
			if h, ok := s.assessing().hintRequests[d.Request]; ok {
				if h.Lesson != d.Lesson {
					return nil, invalidf("request %s already recorded a hint for Lesson %s, not %s", d.Request,
						h.Lesson, d.Lesson)
				}
				repeat = &h
				return nil, nil
			}
		}
		if _, err := requireStudiedLesson(s, topicID, d.Lesson); err != nil {
			return nil, err
		}
		if ls := s.study.lessons[d.Lesson]; ls != nil && ls.completed != nil {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + d.Lesson + " is done: hints are " +
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
	result.Hint = Hint{ID: ev.ID, Lesson: d.Lesson, Kind: d.Kind, RequestedBy: d.RequestedBy, Note: d.Note, At: ev.Wall}
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
	if d.Kind == "" {
		return fmt.Errorf("it has no kind")
	}
	d, err := checkHint(d)
	if err != nil {
		return err
	}
	st := s.assessing()
	h := Hint{ID: ev.ID, Lesson: d.Lesson, Kind: d.Kind, RequestedBy: d.RequestedBy, Note: d.Note, At: wallOf(ev)}
	st.hints = append(st.hints, h)
	// The first hint with a request id is the one a retry returns.
	if d.Request != "" {
		if _, ok := st.hintRequests[d.Request]; !ok {
			st.hintRequests[d.Request] = h
		}
	}
	return nil
}
