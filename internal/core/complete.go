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
	Lesson  string `json:"lesson"`
	Attempt string `json:"attempt"`
	// CheckVersion is the version of the Check the passing Attempt ran,
	// and ShownCheck the version shown to the learner when practicing
	// started; completion requires them to be equal.
	CheckVersion string `json:"check_version"`
	ShownCheck   string `json:"shown_check"`
	Snapshot     string `json:"snapshot"`
	// TurnEnded is whose turn completing the Lesson ended: the learner's
	// when completing straight from practicing, otherwise the agent's. A
	// Checkpoint is owed for it.
	TurnEnded string     `json:"turn_ended"`
	Cards     []cardLine `json:"cards,omitempty"`
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
	// Cards are the Cards the completion saved, as they are now: their
	// current content, and whether each is still a draft. Dropped Cards
	// are left out.
	Cards []Card `json:"cards"`
	// Changed is false when the Lesson was already done.
	Changed bool `json:"changed"`
	// Warning says when a repeated call passed Cards that differ from those
	// the completion recorded, which are kept.
	Warning string `json:"warning,omitempty"`
	TurnCheckpoint
	DryRun bool `json:"dry_run,omitempty"`
}

// CompleteLesson completes a Lesson in one idempotent operation: it checks
// the completion rule, marks the Lesson done, saves its draft Cards, records
// the Event and takes a Checkpoint for the turn that ends. Completing a
// Lesson that is already done records nothing but still takes a Checkpoint
// that is owed, so calling it again with the same arguments after an
// interruption finishes the job.
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
	result := LessonCompletion{Topic: topicID, Lesson: spec.Lesson, Cards: []Card{}, DryRun: spec.DryRun}
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return LessonCompletion{}, err
	}
	if _, err := requireLesson(s, topicID, spec.Lesson); err != nil {
		return LessonCompletion{}, err
	}
	// The work and the Check as they are now, unless the Lesson is done.
	// They are read before the write takes the lock, because snapshotting
	// the work runs git.
	var cur work
	if ls := s.study.lessons[spec.Lesson]; ls == nil || ls.completed == nil {
		if cur, err = c.currentWork(ctx, topicID, dir, spec.Lesson); err != nil {
			return LessonCompletion{}, err
		}
	}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		if ls := s.study.lessons[spec.Lesson]; ls != nil && ls.completed != nil {
			c.describeCompletion(s, view, topicID, spec.Lesson, drafts, &result)
			return nil, nil
		}
		// Checked under the lock, so a Revision skipping the Lesson in the
		// meantime is seen.
		if _, err := requireStudiedLesson(s, topicID, spec.Lesson); err != nil {
			return nil, err
		}
		if cur.check == nil {
			return nil, &Error{Code: CodeBusy, Message: "Lesson " + spec.Lesson + " changed while it was being completed: try again"}
		}
		attempt, err := completionRule(s, spec.Lesson, cur)
		if err != nil {
			return nil, err
		}
		d := lessonCompletedData{Lesson: spec.Lesson, Attempt: attempt.ID, CheckVersion: attempt.CheckVersion,
			ShownCheck: s.study.lessons[spec.Lesson].shownCheck, Snapshot: cur.snapshot,
			TurnEnded: s.study.currentTurn()}
		var items []string
		for _, draft := range drafts {
			evidence, err := checkCardEvidence(s, topicID, draft.Evidence)
			if err != nil {
				return nil, err
			}
			line := cardLine{Format: FormatVersion, ID: c.newCardID(s, spec.Lesson), Lesson: spec.Lesson,
				Prompt: draft.Prompt, Answer: draft.Answer, Evidence: evidence}
			d.Cards = append(d.Cards, line)
			items = append(items, cardItem(line.ID))
			result.Cards = append(result.Cards, Card{ID: line.ID, Number: liveCards(&s.study) + len(result.Cards) + 1,
				Lesson: line.Lesson, Prompt: line.Prompt, Answer: line.Answer, Evidence: line.Evidence, Draft: true})
		}
		result.Attempt = attempt.ID
		return &change{Type: eventLessonCompleted, Data: d, Items: items}, nil
	}, spec.DryRun)
	if err != nil {
		return LessonCompletion{}, err
	}
	result.Changed = ev != nil
	if !spec.DryRun {
		result.TurnCheckpoint = c.takeOwedCheckpoint(ctx, topicID)
	}
	return result, nil
}

// describeCompletion fills in a completion already recorded: the Attempt it
// relied on and its Cards as they are now, read through view, which a write
// has already brought up to date. It warns when drafts differ from the
// Cards the completion recorded.
func (c *Core) describeCompletion(s *replayed, view *topicView, topicID, lessonID string, drafts []CardDraft, result *LessonCompletion) {
	done := s.study.lessons[lessonID].completed
	result.Attempt = done.Attempt
	for _, id := range s.study.cardOrder {
		cs := s.study.cards[id]
		if cs.lesson != lessonID || cs.gone() {
			continue
		}
		card, err := readCard(view, s, cs)
		if err != nil {
			c.log.Warn("skipping a Card that cannot be read", "topic", topicID, "card", id, "err", err)
			continue
		}
		result.Cards = append(result.Cards, card)
	}
	if len(drafts) > 0 && !sameDrafts(drafts, done.Cards) {
		result.Warning = fmt.Sprintf("Lesson %s was already completed with other Cards, which are kept; "+
			"these Cards were not saved", lessonID)
	}
}

func sameDrafts(drafts []CardDraft, recorded []cardLine) bool {
	if len(drafts) != len(recorded) {
		return false
	}
	for i, d := range drafts {
		if d.Prompt != recorded[i].Prompt || d.Answer != recorded[i].Answer {
			return false
		}
	}
	return true
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
	// Cards are registered whichever completion saved them, so none is
	// lost when two machines completed the Lesson.
	for _, line := range d.Cards {
		if _, ok := s.study.cards[line.ID]; ok {
			continue
		}
		s.study.cards[line.ID] = &cardState{id: line.ID, lesson: d.Lesson, created: wallOf(ev),
			prompt: line.Prompt, answer: line.Answer}
		s.study.cardOrder = append(s.study.cardOrder, line.ID)
	}
	if l.completed != nil {
		s.flag(newFlag(FlagConflict, lessonFile(d.Lesson), []string{l.completedBy, ev.ID}, "",
			fmt.Sprintf("Lesson %s was completed twice, by Events %s and %s, probably on two machines: "+
				"the first completion counts and the Cards of both are kept; check them with the learner",
				d.Lesson, l.completedBy, ev.ID)))
		return nil
	}
	if s.study.syllabus != nil {
		switch sl, ok := s.study.syllabus.lesson(d.Lesson); {
		case !ok:
			s.flag(newFlag(FlagConflict, syllabusFile, []string{ev.ID}, d.Lesson,
				fmt.Sprintf("Lesson %s was completed although a Revision removed it, probably on two machines: "+
					"it counts as done; add it back through a Revision, or dismiss this flag", d.Lesson)))
		case sl.Skipped:
			s.flag(newFlag(FlagConflict, syllabusFile, []string{ev.ID}, d.Lesson,
				fmt.Sprintf("Lesson %s was completed although a Revision skipped it, probably on two machines: "+
					"it counts as done; check the Syllabus with the learner", d.Lesson)))
		}
	}
	l.completed, l.completedBy = &d, ev.ID
	turnEnded := d.TurnEnded
	if turnEnded == "" {
		turnEnded = "agent"
	}
	s.study.owed = &owedCheckpoint{event: ev.ID, role: turnEnded, message: d.Lesson + ": completed"}
	s.study.turn = "agent" // the next Lesson starts with teaching
	if s.study.nextStep != nil && s.study.nextStep.Lesson == d.Lesson {
		s.study.nextStep = nil // its Lesson is done
	}
	return nil
}
