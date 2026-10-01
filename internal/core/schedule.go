package core

import (
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v3"
)

// This file is the only one that knows go-fsrs, so moving to another
// version, such as v4 (FSRS-6, which needs Go 1.26), changes only this
// file and its tests. It also changes every replayed schedule, so the move
// is decided before release, while no learner data depends on it.

// schedule replays a Card's Reviews, oldest first, and returns when it is
// next due; zero for a Card never reviewed. Fuzz is off, so the same Reviews
// always give the same schedule.
//
// Review times come from the writers' clocks, which can run backwards
// between machines. Each one is clamped to the previous Review of the Card,
// so the time elapsed between Reviews is never negative.
func schedule(reviews []review) time.Time {
	if len(reviews) == 0 {
		return time.Time{}
	}
	f := fsrs.NewFSRS(fsrs.DefaultParam())
	card := fsrs.NewCard()
	var last time.Time
	for _, r := range reviews {
		at := r.at
		if at.Before(last) {
			at = last
		}
		card = f.Next(card, at, fsrsRating(r.rating)).Card
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
