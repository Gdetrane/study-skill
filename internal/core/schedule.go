package core

import (
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v4"
)

// This file is the only one that knows go-fsrs (v4, FSRS-6), so moving to
// another version changes only this file and its tests. A new version also
// changes every replayed schedule, so moves are decided before release, or
// with a migration once learner data depends on them.

// schedule replays a Card's Reviews, oldest first, and returns when it is
// next due; zero for a Card never reviewed. Fuzz is off, so the same Reviews
// always give the same schedule.
//
// Review times come from the writers' clocks, which can run backwards
// between machines. Each one is clamped to the previous Review of the Card,
// so the time elapsed between Reviews is never negative, which go-fsrs also
// requires. The Card starts at its first Review, never at the current time,
// so replay does not depend on when it runs.
func schedule(reviews []review) time.Time {
	if len(reviews) == 0 {
		return time.Time{}
	}
	params := fsrs.DefaultParam()
	params.EnableFuzz = false
	f := fsrs.NewFSRS(params)
	card := fsrs.NewCard(reviews[0].at)
	var last time.Time
	for _, r := range reviews {
		at := r.at
		if at.Before(last) {
			at = last
		}
		next, err := f.Next(card, at, fsrsRating(r.rating))
		if err != nil {
			// Clamped times and mapped ratings are always valid, so this
			// cannot happen; if it did, the schedule so far stands rather
			// than one built on an invalid step.
			break
		}
		card = next.Card
		last = at
	}
	return card.Due
}

// fsrsRating maps a Review's rating to go-fsrs's.
func fsrsRating(r string) fsrs.Rating {
	switch r {
	case RatingAgain:
		return fsrs.Again
	case RatingHard:
		return fsrs.Hard
	case RatingEasy:
		return fsrs.Easy
	}
	return fsrs.Good
}
