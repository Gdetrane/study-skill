package core

import (
	"context"
	"math"
	"sort"
)

// Learning signals: what a future Level suggestion needs, recorded from the
// start (v2.0 makes no suggestions yet). Everything here is derived by
// replaying the History, except hints and Assessments, which have their own
// Events. The signals are for the agent, to adapt how it teaches; they are
// never shown to the learner as counts or scores.

// Signals is a Topic's learning signals.
type Signals struct {
	Topic string     `json:"topic"`
	Level *LevelInfo `json:"level,omitempty"`
	// Assessments are the Topic's Assessments, oldest first.
	Assessments []Assessment `json:"assessments"`
	// Lessons are the Lessons with any activity, in Syllabus order; Lessons
	// no longer in the Syllabus come last.
	Lessons []LessonSignals `json:"lessons"`
	// Reviews are the ratings of every Review in the Topic, Explore Cards
	// included.
	Reviews ReviewSignals `json:"reviews"`
	// Totals sum the Lessons' signals.
	Totals SignalTotals `json:"totals"`
}

// LessonSignals is one Lesson's learning signals.
type LessonSignals struct {
	Lesson string `json:"lesson"`
	Title  string `json:"title,omitempty"`
	Done   bool   `json:"done"`
	// FirstTry is whether the first Attempt measured on the Check shown to
	// the learner passed; absent before one. Attempts the agent ran before
	// showing the Check, and errored ones, are not measured.
	FirstTry *bool `json:"first_try,omitempty"`
	// Attempts counts the Attempts measured on the Check shown.
	Attempts int `json:"attempts"`
	// FeedbackRounds counts the times the Lesson went to feedback.
	FeedbackRounds int `json:"feedback_rounds"`
	// Hints counts the hints recorded, by kind.
	Hints map[string]int `json:"hints,omitempty"`
	// HeldOut compares each held_out criterion's counted run with the run
	// criteria of the same Attempt: the gap between dev and Held-out
	// scores.
	HeldOut []HeldOutGap   `json:"held_out,omitempty"`
	Reviews *ReviewSignals `json:"reviews,omitempty"`
}

// HeldOutGap is the gap between a held_out criterion's counted score and the
// dev score of the run criteria in the same Attempt, both as fractions from
// 0 to 1.
type HeldOutGap struct {
	Criterion string  `json:"criterion"`
	Attempt   string  `json:"attempt"`
	HeldOut   float64 `json:"held_out"`
	// Dev is the run criteria's mean score: each one's results score over
	// its max, or 1 for passed and 0 for failed. Absent when the Attempt
	// had no run criteria.
	Dev *float64 `json:"dev,omitempty"`
	// Gap is Dev minus HeldOut: positive when the work did better on the
	// learner's own tests than on the Held-out data.
	Gap *float64 `json:"gap,omitempty"`
}

// ReviewSignals counts Review ratings.
type ReviewSignals struct {
	Again int `json:"again"`
	Hard  int `json:"hard"`
	Good  int `json:"good"`
	Easy  int `json:"easy"`
}

func (r *ReviewSignals) add(rating string) {
	switch rating {
	case RatingAgain:
		r.Again++
	case RatingHard:
		r.Hard++
	case RatingGood:
		r.Good++
	case RatingEasy:
		r.Easy++
	}
}

func (r ReviewSignals) empty() bool { return r == ReviewSignals{} }

// SignalTotals sums the signals of a Topic's Lessons.
type SignalTotals struct {
	// FirstTryPassed of FirstTryMeasured Lessons passed on the first try.
	FirstTryPassed   int `json:"first_try_passed"`
	FirstTryMeasured int `json:"first_try_measured"`
	FeedbackRounds   int `json:"feedback_rounds"`
	Hints            int `json:"hints"`
}

// SignalsOf returns a Topic's learning signals. It writes nothing.
func (c *Core) SignalsOf(ctx context.Context, topicID string) (Signals, error) {
	if err := checkTopicID(topicID); err != nil {
		return Signals{}, err
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return Signals{}, err
	}
	sig := signalsOf(topicID, s)
	if topic, err := c.readTopic(topicID); err == nil {
		sig.Level = topic.Level
	}
	return sig, nil
}

// signalsOf derives the signals from a replayed History.
func signalsOf(topicID string, s *replayed) Signals {
	st := s.assessing()
	sig := Signals{Topic: topicID, Assessments: append([]Assessment{}, st.assessments...), Lessons: []LessonSignals{}}
	byLesson := map[string]*LessonSignals{}
	get := func(id string) *LessonSignals {
		if ls := byLesson[id]; ls != nil {
			return ls
		}
		ls := &LessonSignals{Lesson: id}
		byLesson[id] = ls
		return ls
	}
	for id, l := range s.study.lessons {
		if l.phase == "" && len(l.attempts) == 0 && l.completed == nil {
			continue
		}
		ls := get(id)
		ls.Done = l.completed != nil
		ls.FirstTry = l.firstTry
		ls.Attempts = l.measured
		ls.FeedbackRounds = l.feedbackRounds
		ls.HeldOut = heldOutGaps(l)
	}
	for _, h := range st.hints {
		ls := get(h.Lesson)
		if ls.Hints == nil {
			ls.Hints = map[string]int{}
		}
		ls.Hints[h.Kind]++
		sig.Totals.Hints++
	}
	for _, cs := range s.study.cards {
		for _, r := range cs.reviews {
			sig.Reviews.add(r.rating)
			if lesson := cs.lessonShown(); lesson != "" {
				ls := get(lesson)
				if ls.Reviews == nil {
					ls.Reviews = &ReviewSignals{}
				}
				ls.Reviews.add(r.rating)
			}
		}
	}
	// Syllabus order first, then Lessons no longer in it, by id.
	order := map[string]int{}
	if s.study.syllabus != nil {
		for i, l := range s.study.syllabus.lessons() {
			order[l.ID] = i
			if ls := byLesson[l.ID]; ls != nil {
				ls.Title = l.Title
			}
		}
	}
	for _, ls := range byLesson {
		sig.Lessons = append(sig.Lessons, *ls)
	}
	sort.Slice(sig.Lessons, func(i, j int) bool {
		a, b := sig.Lessons[i], sig.Lessons[j]
		ia, oka := order[a.Lesson]
		ib, okb := order[b.Lesson]
		switch {
		case oka && okb:
			return ia < ib
		case oka != okb:
			return oka
		}
		return a.Lesson < b.Lesson
	})
	for _, ls := range sig.Lessons {
		if ls.FirstTry != nil {
			sig.Totals.FirstTryMeasured++
			if *ls.FirstTry {
				sig.Totals.FirstTryPassed++
			}
		}
		sig.Totals.FeedbackRounds += ls.FeedbackRounds
	}
	return sig
}

// heldOutGaps compares each held_out criterion's counted run with the run
// criteria of the same Attempt.
func heldOutGaps(l *lessonState) []HeldOutGap {
	var criteria []string
	for crit := range l.heldOut {
		criteria = append(criteria, crit)
	}
	sort.Strings(criteria)
	var gaps []HeldOutGap
	for _, crit := range criteria {
		attempt := l.heldOut[crit]
		var a *Attempt
		for i := range l.attempts {
			if l.attempts[i].ID == attempt {
				a = &l.attempts[i]
			}
		}
		if a == nil {
			continue
		}
		var heldOut *float64
		var dev []float64
		for _, r := range a.Criteria {
			switch {
			case r.ID == crit && r.kind() == CriterionHeldOut:
				heldOut = fraction(r)
			case r.kind() == CriterionRun && r.Outcome != OutcomeErrored:
				if f := fraction(r); f != nil {
					dev = append(dev, *f)
				}
			}
		}
		if heldOut == nil {
			continue
		}
		g := HeldOutGap{Criterion: crit, Attempt: attempt, HeldOut: *heldOut}
		if len(dev) > 0 {
			sum := 0.0
			for _, f := range dev {
				sum += f
			}
			mean := round3(sum / float64(len(dev)))
			gap := round3(mean - *heldOut)
			g.Dev, g.Gap = &mean, &gap
		}
		gaps = append(gaps, g)
	}
	return gaps
}

// fraction is a criterion's score from 0 to 1: its results score over its
// max, or 1 for passed and 0 for failed.
func fraction(r CriterionResult) *float64 {
	var f float64
	switch {
	case r.Results != nil && r.Results.Score != nil:
		max := 1.0
		if r.Results.Max != nil && *r.Results.Max > 0 {
			max = *r.Results.Max
		}
		f = *r.Results.Score / max
	case r.Outcome == OutcomePassed:
		f = 1
	case r.Outcome == OutcomeFailed:
		f = 0
	default:
		return nil
	}
	f = round3(math.Max(0, math.Min(1, f)))
	return &f
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
