package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// CardsReady says whether a Topic has Cards to review now, never how many:
// the number offered depends on the Session's Energy (see DueCardsOf), and
// counts of what is due are never shown.
type CardsReady struct {
	Ready bool `json:"ready"`
	// Paused is set when the Topic is paused: its Cards wait until the
	// learner resumes it, so none is ever ready.
	Paused bool `json:"paused,omitempty"`
	// NextDue is when the next Card falls due, when none is ready now.
	NextDue *time.Time `json:"next_due,omitempty"`
}

// cardsReady reports whether a Topic's Cards are ready to review at now,
// knowing its state and its daily cap on new Cards (see topicCap): a paused
// Topic's Cards are never ready. Everything that asks whether Cards are
// ready goes through it. It is nil for a Topic without Cards.
func cardsReady(s *replayed, view *topicView, topicID string, now time.Time) *CardsReady {
	out := s.study.cardsReadyUnder(now, topicCap(view, topicID))
	if out != nil && s.topicState() == TopicPaused {
		return &CardsReady{Paused: true}
	}
	return out
}

// cardsReadyUnder reports whether Cards are ready to review at now: a Card
// due, or a draft that capPerDay, the daily cap on new Cards, still allows
// today. Suspended Cards and Cards gone never count. It is nil for a Topic
// without Cards.
func (st *studyState) cardsReadyUnder(now time.Time, capPerDay int) *CardsReady {
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
	// A cap of 0 takes no new Cards: its drafts are never ready.
	if draftsWaiting && capPerDay > 0 {
		if st.draftsLeftToday(now, capPerDay) > 0 {
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

// The suggestions that are not a Focus.
const (
	// SuggestPlan: the Topic has no Syllabus yet; plan it together first.
	SuggestPlan = "plan"
	// SuggestStop: low Energy and nothing due, so write tomorrow's first
	// step as the Next step and end here; or a finished Topic with no Card
	// ready, so there is nothing to study on it now.
	SuggestStop = "stop"
	// SuggestResumeTopic: the Topic is paused. It stays paused until the
	// learner resumes it through topic_update, or picks another Topic.
	SuggestResumeTopic = "resume_topic"
	// SuggestAssess: every Lesson of a Milestone is done or skipped, and its
	// end-of-Milestone Assessment comes next. It is suggested at full and
	// half Energy only: at fumes, an Assessment waits for a better day.
	SuggestAssess = "assess"
)

// FocusSuggestion is what the learner's Energy suggests for the Session.
// The learner chooses; a suggestion is never recorded.
type FocusSuggestion struct {
	// Suggest is a Focus the learner may choose (learn, practice, reviews
	// or explore), or plan, stop, resume_topic or assess, which are not
	// Focuses.
	Suggest string `json:"suggest"`
	// Reason is English prose for the learner; the skill may rephrase it.
	Reason string `json:"reason"`
}

// suggestFocus suggests a Focus from the Energy, the Topic's state, where
// the learner stopped, and whether Cards are ready. Like status's
// recommendation, it never proposes studying a paused Topic, suggests only
// Reviews on a finished one, and suggests planning a Syllabus first.
func suggestFocus(energy, state string, r ResumePoint, cards *CardsReady, assess *MilestoneRef) *FocusSuggestion {
	switch energy {
	case EnergyFull, EnergyHalf, EnergyFumes:
	default:
		return nil
	}
	ready := cards != nil && cards.Ready
	switch state {
	case TopicPaused:
		return &FocusSuggestion{Suggest: SuggestResumeTopic, Reason: "the Topic is paused, and stays paused until you resume it"}
	case TopicFinished:
		if ready {
			return &FocusSuggestion{Suggest: FocusReviews, Reason: "the Topic is finished, and Cards are ready"}
		}
		return &FocusSuggestion{Suggest: SuggestStop, Reason: "the Topic is finished, and no Card is ready: nothing to study on it now"}
	}
	if assess != nil && (energy == EnergyFull || energy == EnergyHalf) {
		return &FocusSuggestion{Suggest: SuggestAssess, Reason: fmt.Sprintf("every Lesson of Milestone %d “%s” is "+
			"done: its Assessment comes next", assess.Number, assess.Title)}
	}
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
	topicFile: true, syllabusFile: true, historyFile: true, cardsFile: true, sourcesFile: true, tasksFile: true,
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
		// Held-out data is kept out of the learner's sight, names included.
		if stateFiles[f.Path] || strings.HasPrefix(f.Path, ".heldout/") {
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
