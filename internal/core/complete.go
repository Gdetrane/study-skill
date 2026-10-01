package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const eventLessonCompleted = "lesson.completed"

// lessonCompletedData is the payload of a lesson.completed Event. It records
// the Attempt and the Check version completion relied on, so later changes
// to shared code or to the Lesson never reopen it, and the draft Cards it
// saved, in full.
type lessonCompletedData struct {
	Lesson       string     `json:"lesson"`
	Attempt      string     `json:"attempt"`
	CheckVersion string     `json:"check_version"`
	Snapshot     string     `json:"snapshot"`
	Cards        []cardLine `json:"cards,omitempty"`
}

// CompleteSpec describes completing a Lesson.
type CompleteSpec struct {
	Lesson string
	// Cards are the draft Cards written from the Lesson.
	Cards  []CardDraft
	DryRun bool
}

// LessonCompletion is the result of CompleteLesson.
type LessonCompletion struct {
	Topic  string `json:"topic"`
	Lesson string `json:"lesson"`
	// Attempt is the passing Attempt completion relied on.
	Attempt string `json:"attempt"`
	// Cards are the draft Cards the completion saved.
	Cards []Card `json:"cards"`
	// Changed is false when the Lesson was already done.
	Changed    bool              `json:"changed"`
	Checkpoint *CheckpointResult `json:"checkpoint,omitempty"`
	// CheckpointError explains a Checkpoint that could not be taken. The
	// Lesson is done anyway.
	CheckpointError string `json:"checkpoint_error,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
}

// CompleteLesson completes a Lesson in one idempotent operation: it checks
// the completion rule, marks the Lesson done, saves its draft Cards, records
// the Event and takes a Checkpoint. Completing a Lesson that is already done
// changes nothing but still takes the Checkpoint, so calling it again after
// an interruption finishes the job.
func (c *Core) CompleteLesson(ctx context.Context, topicID string, spec CompleteSpec) (LessonCompletion, error) {
	if len(spec.Cards) > maxCardsPerCall {
		return LessonCompletion{}, invalidf("at most %d Cards can be saved with a Lesson", maxCardsPerCall)
	}
	drafts := make([]CardDraft, 0, len(spec.Cards))
	for _, d := range spec.Cards {
		clean, err := cleanDraft(d)
		if err != nil {
			return LessonCompletion{}, err
		}
		drafts = append(drafts, clean)
	}
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return LessonCompletion{}, err
	}
	if _, err := requireLesson(s, topicID, spec.Lesson); err != nil {
		return LessonCompletion{}, err
	}
	// The work and the Check as they are now. They are read before the
	// write takes the lock, because snapshotting the work runs git.
	cur, err := c.currentWork(ctx, topicID, dir, spec.Lesson)
	if err != nil {
		return LessonCompletion{}, err
	}

	result := LessonCompletion{Topic: topicID, Lesson: spec.Lesson, Cards: []Card{}, DryRun: spec.DryRun}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		if ls := s.study.lessons[spec.Lesson]; ls != nil && ls.completed != nil {
			result.Attempt = ls.completed.Attempt
			for _, line := range ls.completed.Cards {
				result.Cards = append(result.Cards, Card{ID: line.ID, Lesson: line.Lesson, Prompt: line.Prompt, Answer: line.Answer, Draft: true})
			}
			return nil, nil
		}
		attempt, err := completionRule(s, spec.Lesson, cur)
		if err != nil {
			return nil, err
		}
		d := lessonCompletedData{Lesson: spec.Lesson, Attempt: attempt.ID, CheckVersion: cur.checkVersion, Snapshot: cur.snapshot}
		var items []string
		for _, draft := range drafts {
			line := cardLine{Format: FormatVersion, ID: c.newCardID(s, spec.Lesson), Lesson: spec.Lesson,
				Prompt: draft.Prompt, Answer: draft.Answer}
			d.Cards = append(d.Cards, line)
			items = append(items, cardItem(line.ID))
			result.Cards = append(result.Cards, Card{ID: line.ID, Lesson: line.Lesson, Prompt: line.Prompt, Answer: line.Answer, Draft: true})
		}
		result.Attempt = attempt.ID
		return &change{Type: eventLessonCompleted, Data: d, Items: items}, nil
	}, spec.DryRun)
	if err != nil {
		return LessonCompletion{}, err
	}
	result.Changed = ev != nil
	if spec.DryRun {
		return result, nil
	}
	cp, err := c.Checkpoint(ctx, CheckpointSpec{Topic: topicID, Role: "agent", Message: spec.Lesson + ": completed"})
	if err != nil {
		result.CheckpointError = err.Error()
		c.log.Warn("could not take a Checkpoint after completing a Lesson", "topic", topicID, "err", err)
		return result, nil
	}
	result.Checkpoint = &cp
	return result, nil
}

func applyLessonCompleted(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d lessonCompletedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	id, ok := strings.CutPrefix(item, cardsFile+"#")
	if ok {
		for _, line := range d.Cards {
			if line.ID == id {
				data, err := encodeCard(line)
				return data, err == nil, err
			}
		}
	}
	return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
}

func replayLessonCompleted(s *replayed, ev event) error {
	var d lessonCompletedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	l := s.study.lesson(d.Lesson)
	if l.completed != nil {
		return nil // completing twice changes nothing
	}
	l.completed = &d
	for _, line := range d.Cards {
		if _, ok := s.study.cards[line.ID]; ok {
			continue
		}
		s.study.cards[line.ID] = &cardState{id: line.ID, lesson: d.Lesson, created: wallOf(ev)}
		s.study.cardOrder = append(s.study.cardOrder, line.ID)
	}
	s.recordVersion(checkItem(d.Lesson), d.CheckVersion, ev.ID)
	if s.study.nextStep != nil && s.study.nextStep.Lesson == d.Lesson {
		s.study.nextStep = nil // its Lesson is done
	}
	return nil
}
