package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const eventBreakPointReached = "break_point.reached"

func init() {
	eventKinds[eventBreakPointReached] = eventKind{apply: applyNothing, replay: replayBreakPointReached}
}

// breakPointReachedData is the payload of a break_point.reached Event.
type breakPointReachedData struct {
	Lesson     string `json:"lesson"`
	BreakPoint string `json:"break_point"`
	NextStep   string `json:"next_step"`
	Context    string `json:"context,omitempty"`
}

// BreakPointSpec describes reaching a Break point.
type BreakPointSpec struct {
	Lesson string
	// BreakPoint is the id of one of the Break points the Lesson's header
	// declares.
	BreakPoint string
	// NextStep is the concrete next action, starting with a verb.
	NextStep string
	// Context is what the learner needs to know to take it.
	Context string
	DryRun  bool
}

// BreakPointReached is the result of ReachBreakPoint.
type BreakPointReached struct {
	Topic      string     `json:"topic"`
	Lesson     string     `json:"lesson"`
	BreakPoint BreakPoint `json:"break_point"`
	NextStep   NextStep   `json:"next_step"`
	// Changed is false when the Lesson was already at that Break point
	// with the same Next step: nothing was recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// ReachBreakPoint records that the learner reached one of a Lesson's Break
// points, with a Next step, starting with a verb, and free-text context. The
// Session can stop there, and the next one resumes from it. The Session stays
// open: closing it is session_close's job.
func (c *Core) ReachBreakPoint(ctx context.Context, topicID string, spec BreakPointSpec) (BreakPointReached, error) {
	step, err := checkNextStep(spec.NextStep)
	if err != nil {
		return BreakPointReached{}, err
	}
	note, err := cleanTextBlock("context", spec.Context, maxContextRunes)
	if err != nil {
		return BreakPointReached{}, err
	}
	result := BreakPointReached{Topic: topicID, Lesson: spec.Lesson, DryRun: spec.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if _, err := requireStudiedLesson(s, topicID, spec.Lesson); err != nil {
			return nil, err
		}
		ls := s.study.lessons[spec.Lesson]
		if ls != nil && ls.completed != nil {
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + spec.Lesson + " is done"}
		}
		point, err := findBreakPoint(view.root, spec.Lesson, spec.BreakPoint)
		if err != nil {
			return nil, err
		}
		result.BreakPoint = point
		result.NextStep = NextStep{Step: step, Context: note, Lesson: spec.Lesson, At: c.now().UTC().Truncate(clockTick)}
		if ls != nil && ls.breakPoint == point.ID && s.study.nextStep != nil &&
			*s.study.nextStep == (NextStep{Step: step, Context: note, Lesson: spec.Lesson, At: s.study.nextStep.At}) {
			result.NextStep = *s.study.nextStep
			return nil, nil
		}
		return &change{Type: eventBreakPointReached, Data: breakPointReachedData{
			Lesson: spec.Lesson, BreakPoint: point.ID, NextStep: step, Context: note,
		}}, nil
	}, spec.DryRun)
	if err != nil {
		return BreakPointReached{}, err
	}
	result.Changed = ev != nil
	return result, nil
}

// findBreakPoint returns the Break point a Lesson's header declares with the
// given id.
func findBreakPoint(topic *os.Root, lessonID, id string) (BreakPoint, error) {
	if err := validateEntityID("Break point", id); err != nil {
		return BreakPoint{}, err
	}
	points, err := readBreakPoints(topic, lessonID)
	if err != nil {
		return BreakPoint{}, err
	}
	var ids []string
	for _, p := range points {
		if p.ID == id {
			return p, nil
		}
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return BreakPoint{}, &Error{Code: CodeNotFound, Message: lessonFile(lessonID) +
			" declares no Break points: list them under break_points: in its YAML header"}
	}
	return BreakPoint{}, &Error{Code: CodeNotFound, Message: fmt.Sprintf("%s has no Break point %s; it declares %s",
		lessonFile(lessonID), id, strings.Join(ids, ", "))}
}

func replayBreakPointReached(s *replayed, ev event) error {
	var d breakPointReachedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	s.study.lesson(d.Lesson).breakPoint = d.BreakPoint
	s.study.nextStep = &NextStep{Step: d.NextStep, Context: d.Context, Lesson: d.Lesson, At: wallOf(ev)}
	return nil
}

// describeBreakPoint fills in the description of the Resume point's Break
// point from the Lesson's header. A Lesson file that cannot be read leaves
// it empty: the id is still shown.
func describeBreakPoint(topic *os.Root, r *ResumePoint) {
	if r.BreakPoint == nil || r.Lesson == "" {
		return
	}
	points, err := readBreakPoints(topic, r.Lesson)
	if err != nil {
		return
	}
	for _, p := range points {
		if p.ID == r.BreakPoint.ID {
			r.BreakPoint.Describe = p.Describe
		}
	}
}
