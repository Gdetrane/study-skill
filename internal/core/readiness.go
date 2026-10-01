package core

import (
	"context"
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// CardsReady says whether a Topic has Cards to review now, never how many:
// the number offered depends on the Session's Energy (see DueCardsOf), and
// counts of what is due are never shown.
type CardsReady struct {
	Ready bool `json:"ready"`
	// NextDue is when the next Card falls due, when none is ready now.
	NextDue *time.Time `json:"next_due,omitempty"`
}

// cardsReady reports whether Cards are ready to review at now: a Card due,
// or a draft today's cap on new Cards still allows. Suspended Cards and
// Cards gone never count. It is nil for a Topic without Cards.
func (st *studyState) cardsReady(now time.Time) *CardsReady {
	var next time.Time
	draftsWaiting, any := false, false
	for _, cs := range st.cards {
		if cs.gone() || cs.suspended {
			continue
		}
		any = true
		if cs.draft() {
			draftsWaiting = true
			continue
		}
		due := cs.due()
		if !due.After(now) {
			return &CardsReady{Ready: true}
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	if draftsWaiting {
		if st.draftsLeftToday(now) > 0 {
			return &CardsReady{Ready: true}
		}
		y, m, d := now.Date()
		tomorrow := time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
		if next.IsZero() || tomorrow.Before(next) {
			next = tomorrow
		}
	}
	if !any {
		return nil
	}
	out := &CardsReady{}
	if !next.IsZero() {
		out.NextDue = &next
	}
	return out
}

// FocusSuggestion is the Focus the learner's Energy suggests, with the
// reason. The learner chooses; a suggestion is never recorded.
type FocusSuggestion struct {
	// Focus is learn, practice, reviews or explore; empty at fumes with
	// nothing due, when the offer is to write tomorrow's first step.
	Focus  string `json:"focus,omitempty"`
	Reason string `json:"reason"`
}

// suggestFocus suggests a Focus from the Energy, where the learner stopped,
// and whether Cards are ready.
func suggestFocus(energy string, r ResumePoint, cards *CardsReady) *FocusSuggestion {
	ready := cards != nil && cards.Ready
	exercise := r.Phase == PhasePracticing || r.Phase == PhaseFeedback
	lesson := "Lesson “" + r.LessonTitle + "”"
	switch energy {
	case EnergyFumes:
		if ready {
			return &FocusSuggestion{Focus: FocusReviews, Reason: "a few Reviews fit low Energy"}
		}
		return &FocusSuggestion{Reason: "nothing is due: write tomorrow's first step as the Next step, and stop here"}
	case EnergyHalf:
		switch {
		case exercise:
			return &FocusSuggestion{Focus: FocusPractice, Reason: "continue the exercise of " + lesson + " from where it stopped"}
		case ready:
			return &FocusSuggestion{Focus: FocusReviews, Reason: "Cards are ready, and Reviews fit half Energy"}
		case r.Lesson != "":
			return &FocusSuggestion{Focus: FocusLearn, Reason: "go on with " + lesson + " at a gentle pace"}
		}
		return &FocusSuggestion{Focus: FocusExplore, Reason: "every Lesson is done: explore what comes next"}
	case EnergyFull:
		switch {
		case exercise:
			return &FocusSuggestion{Focus: FocusPractice, Reason: "continue the exercise of " + lesson + " from where it stopped"}
		case r.Lesson != "":
			return &FocusSuggestion{Focus: FocusLearn, Reason: "full Energy suits " + lesson}
		case ready:
			return &FocusSuggestion{Focus: FocusReviews, Reason: "every Lesson is done, and Cards are ready"}
		}
		return &FocusSuggestion{Focus: FocusExplore, Reason: "every Lesson is done: explore what comes next"}
	}
	return nil
}

// longGap is how long since the previous Session counts as a long gap,
// after which a Session starts with a short recap and a warm-up.
const longGap = 7 * 24 * time.Hour

// maxChangesShown bounds the files listed for an unclosed Session.
const maxChangesShown = 50

// FileChange is a file that changed since the last Checkpoint.
type FileChange struct {
	Path string `json:"path"`
	// Change is added, modified or deleted.
	Change string `json:"change"`
}

// UnclosedSession is a Session that ended without a Next step, for instance
// because the terminal was closed, with what changed in the Topic since its
// last Checkpoint, to help the learner remember where they stopped.
type UnclosedSession struct {
	SessionInfo
	// Since is the last Checkpoint's commit, "" before the first.
	Since string `json:"since,omitempty"`
	// Changes are the files changed since, by path, at most 50; History
	// writes are left out.
	Changes []FileChange `json:"changes"`
	// MoreChanges is how many more files changed than are listed.
	MoreChanges int `json:"more_changes,omitempty"`
	// ChangesError says why the changes could not be listed, such as a
	// git merge in progress. The Session opens anyway.
	ChangesError string `json:"changes_error,omitempty"`
}

// workChanges lists what changed in a Topic since its last Checkpoint,
// writing nothing (ADR-0009: no program the repository names is run).
func (c *Core) workChanges(ctx context.Context, topicID string, u *UnclosedSession) {
	u.Changes = []FileChange{}
	ch, err := checkpoint.ChangesSince(ctx, c.topicDir(topicID))
	if err != nil {
		why := checkpointError(topicID, c.topicDir(topicID), err).Error()
		u.ChangesError = "the changes since the last Checkpoint cannot be listed: " +
			strings.TrimPrefix(why, "cannot checkpoint "+topicID+": ")
		return
	}
	u.Since = ch.Since
	for _, f := range ch.Files {
		if f.Path == historyFile {
			// The History changes with every Event; it is not the
			// learner's work.
			continue
		}
		if len(u.Changes) == maxChangesShown {
			u.MoreChanges++
			continue
		}
		u.Changes = append(u.Changes, FileChange{Path: f.Path, Change: f.Kind})
	}
}
