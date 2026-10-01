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

// The two suggestions that are not a Focus.
const (
	// SuggestPlan: the Topic has no Syllabus yet; plan it together first.
	SuggestPlan = "plan"
	// SuggestStop: low Energy and nothing due; write tomorrow's first step
	// as the Next step and end here.
	SuggestStop = "stop"
)

// FocusSuggestion is what the learner's Energy suggests for the Session.
// The learner chooses; a suggestion is never recorded.
type FocusSuggestion struct {
	// Suggest is a Focus the learner may choose (learn, practice, reviews
	// or explore), or plan or stop, which are not Focuses.
	Suggest string `json:"suggest"`
	// Reason is English prose for the learner; the skill may rephrase it.
	Reason string `json:"reason"`
}

// suggestFocus suggests a Focus from the Energy, where the learner stopped,
// and whether Cards are ready. Without a Syllabus it suggests planning one,
// as status recommends.
func suggestFocus(energy string, r ResumePoint, cards *CardsReady) *FocusSuggestion {
	ready := cards != nil && cards.Ready
	exercise := r.Phase == PhasePracticing || r.Phase == PhaseFeedback
	noSyllabus := r.Lesson == "" && !r.SyllabusDone
	lesson := "Lesson “" + r.LessonTitle + "”"
	practice := &FocusSuggestion{Suggest: FocusPractice, Reason: "continue the exercise of " + lesson + " from where it stopped"}
	plan := &FocusSuggestion{Suggest: SuggestPlan, Reason: "there is no Syllabus yet: plan it together first"}
	explore := &FocusSuggestion{Suggest: FocusExplore, Reason: "every Lesson is done: explore what comes next"}
	switch energy {
	case EnergyFumes:
		if ready {
			return &FocusSuggestion{Suggest: FocusReviews, Reason: "a few Reviews fit low Energy"}
		}
		return &FocusSuggestion{Suggest: SuggestStop, Reason: "nothing is due: write tomorrow's first step as the Next step, and stop here"}
	case EnergyHalf:
		switch {
		case exercise:
			return practice
		case ready:
			return &FocusSuggestion{Suggest: FocusReviews, Reason: "Cards are ready, and Reviews fit half Energy"}
		case noSyllabus:
			return plan
		case r.Lesson != "":
			return &FocusSuggestion{Suggest: FocusLearn, Reason: "go on with " + lesson + " at a gentle pace"}
		}
		return explore
	case EnergyFull:
		switch {
		case exercise:
			return practice
		case noSyllabus:
			return plan
		case r.Lesson != "":
			return &FocusSuggestion{Suggest: FocusLearn, Reason: "full Energy suits " + lesson}
		case ready:
			return &FocusSuggestion{Suggest: FocusReviews, Reason: "every Lesson is done, and Cards are ready"}
		}
		return explore
	}
	return nil
}

// longGap is how long since the Topic's last activity counts as a long gap,
// after which a Session starts with a short recap and a warm-up.
const longGap = 7 * 24 * time.Hour

// maxChangesShown bounds the files listed for an unclosed Session.
const maxChangesShown = 50

// stateFiles are the files Lamplight itself writes as Events happen. They
// are left out of the changes behind an unclosed Session, which are meant to
// show the learner's work.
var stateFiles = map[string]bool{
	topicFile: true, syllabusFile: true, historyFile: true, cardsFile: true, sourcesFile: true,
}

// FileChange is a file that changed since the last Checkpoint.
type FileChange struct {
	Path string `json:"path"`
	// Change is added, modified or deleted.
	Change string `json:"change"`
}

// WorkChanges is what changed in a Topic's files since its last
// Checkpoint, to help the learner remember where an unclosed Session
// stopped. Lamplight's own state files are left out.
type WorkChanges struct {
	// Since is the last Checkpoint's commit; absent when the Topic has no
	// Checkpoint yet, so every file counts as added.
	Since string `json:"since,omitempty"`
	// Files are the changed files, by path, at most 50.
	Files []FileChange `json:"files"`
	// More is how many more files changed than are listed.
	More int `json:"more,omitempty"`
	// Error says why the changes could not be listed, such as a git merge
	// in progress. The Session opens anyway.
	Error string `json:"error,omitempty"`
}

// workChanges lists what changed in a Topic since its last Checkpoint,
// writing nothing (ADR-0009: no program the repository names is run).
func (c *Core) workChanges(ctx context.Context, topicID string) *WorkChanges {
	out := &WorkChanges{Files: []FileChange{}}
	ch, err := checkpoint.ChangesSince(ctx, c.topicDir(topicID))
	if err != nil {
		why := checkpointError(topicID, c.topicDir(topicID), err).Error()
		out.Error = "the changes since the last Checkpoint cannot be listed: " +
			strings.TrimPrefix(why, "cannot checkpoint "+topicID+": ")
		return out
	}
	out.Since = ch.Since
	for _, f := range ch.Files {
		if stateFiles[f.Path] {
			continue
		}
		if len(out.Files) == maxChangesShown {
			out.More++
			continue
		}
		out.Files = append(out.Files, FileChange{Path: f.Path, Change: f.Kind})
	}
	return out
}

// latestActivity is when the Topic was last worked on: the latest real time
// an Event was written.
func latestActivity(s *replayed) time.Time {
	var latest time.Time
	for _, ev := range s.applied {
		if w := wallOf(ev); w.After(latest) {
			latest = w
		}
	}
	return latest
}
