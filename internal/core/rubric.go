package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

const eventRubricGraded = "rubric.graded"

func init() {
	eventKinds[eventRubricGraded] = eventKind{apply: applyNothing, replay: replayRubricGraded}
}

// Grades of a rubric item.
const (
	GradeMet    = "met"
	GradePartly = "partly"
	GradeNotMet = "not_met"
)

const (
	maxGradeNoteRunes = 2000
	maxLookedAt       = 10
	maxWorkFileBytes  = 50 << 20
)

// RubricGrade is the agent's grade of one rubric item of a Lesson's Check,
// for the Check shown to the learner and the work as it stood: completion
// needs every rubric item graded for the current Check and work. A grade can
// name the files of the work it looked at, such as typed final answers or a
// photo of paper work, by path and content hash; their bytes are never
// recorded.
type RubricGrade struct {
	// Event is the Event that recorded the grade.
	Event        string     `json:"event"`
	Grade        string     `json:"grade"`
	Note         string     `json:"note,omitempty"`
	CheckVersion string     `json:"check_version"`
	Snapshot     string     `json:"snapshot"`
	LookedAt     []WorkFile `json:"looked_at,omitempty"`
	At           time.Time  `json:"at"`
}

// WorkFile is a file of the learner's work, named by its path in the Topic
// and the hash of its content.
type WorkFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// rubricGradedData is the payload of a rubric.graded Event.
type rubricGradedData struct {
	Lesson       string     `json:"lesson"`
	Criterion    string     `json:"criterion"`
	Grade        string     `json:"grade"`
	Note         string     `json:"note,omitempty"`
	CheckVersion string     `json:"check_version"`
	Snapshot     string     `json:"snapshot"`
	LookedAt     []WorkFile `json:"looked_at,omitempty"`
	// Replaces is the grade this one replaces, empty for an item's first;
	// two grades that replace the same one were given on two machines.
	Replaces string `json:"replaces,omitempty"`
}

// RubricSpec describes grading a rubric item.
type RubricSpec struct {
	Lesson    string
	Criterion string
	// Grade is met, partly or not_met.
	Grade string
	// Note says what the grade is based on, for the learner. Optional.
	Note string
	// LookedAt names files of the work in the Lesson's practice folder that
	// the grade looked at, relative to it or to the Topic, such as typed
	// final answers or a photo of paper work. Optional.
	LookedAt []string
	DryRun   bool
}

// RubricGraded is the result of RecordRubricGrade.
type RubricGraded struct {
	Topic     string      `json:"topic"`
	Lesson    string      `json:"lesson"`
	Criterion string      `json:"criterion"`
	Grade     RubricGrade `json:"grade"`
	// Changed is false when the item already had this grade, note and
	// files, for the same Check and work: nothing was recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// RecordRubricGrade records the agent's grade of a rubric item, for the
// Check shown to the learner and the work as it is now. The learner checks
// their work against the item first; the agent then grades it. Grading the
// same item again replaces its grade.
func (c *Core) RecordRubricGrade(ctx context.Context, topicID string, spec RubricSpec) (RubricGraded, error) {
	switch spec.Grade {
	case GradeMet, GradePartly, GradeNotMet:
	default:
		return RubricGraded{}, invalidf("the grade must be %s, %s or %s, not %q", GradeMet, GradePartly, GradeNotMet, spec.Grade)
	}
	note, err := cleanTextBlock("note", spec.Note, maxGradeNoteRunes)
	if err != nil {
		return RubricGraded{}, err
	}
	if len(spec.LookedAt) > maxLookedAt {
		return RubricGraded{}, invalidf("a grade can name at most %d files", maxLookedAt)
	}
	if err := validateEntityID("criterion", spec.Criterion); err != nil {
		return RubricGraded{}, err
	}
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return RubricGraded{}, err
	}
	if _, err := requireStudiedLesson(s, topicID, spec.Lesson); err != nil {
		return RubricGraded{}, err
	}
	cur, err := c.currentWork(ctx, topicID, dir, spec.Lesson)
	if err != nil {
		return RubricGraded{}, err
	}
	if cur.missing != nil {
		return RubricGraded{}, cur.missing
	}
	if err := gradableCriterion(cur.check, spec.Lesson, spec.Criterion); err != nil {
		return RubricGraded{}, err
	}
	files, err := c.workFiles(topicID, spec.Lesson, spec.LookedAt)
	if err != nil {
		return RubricGraded{}, err
	}
	d := rubricGradedData{Lesson: spec.Lesson, Criterion: spec.Criterion, Grade: spec.Grade, Note: note,
		CheckVersion: cur.checkVersion, Snapshot: cur.snapshot, LookedAt: files}
	result := RubricGraded{Topic: topicID, Lesson: spec.Lesson, Criterion: spec.Criterion, DryRun: spec.DryRun}
	var latest *RubricGrade
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		ls := s.study.lessons[spec.Lesson]
		switch {
		case ls != nil && ls.completed != nil:
			return nil, &Error{Code: CodeFailedPrecondition, Message: "Lesson " + spec.Lesson + " is done"}
		case ls == nil || ls.shownCheck == "":
			return nil, &Error{Code: CodeFailedPrecondition, Message: "the Check of " + spec.Lesson +
				" was never shown to the learner: move the Lesson to practicing with phase_set first"}
		case ls.shownCheck != cur.checkVersion:
			return nil, &Error{Code: CodeFailedPrecondition, Message: "the Check of " + spec.Lesson +
				" changed since it was shown to the learner: show it again with phase_set practicing, then grade"}
		}
		if g := ls.grades[spec.Criterion]; g != nil {
			if g.Grade == d.Grade && g.Note == d.Note && g.CheckVersion == d.CheckVersion &&
				g.Snapshot == d.Snapshot && slices.Equal(g.LookedAt, d.LookedAt) {
				latest = g
				return nil, nil
			}
			d.Replaces = g.Event
		}
		return &change{Type: eventRubricGraded, Data: d}, nil
	}, spec.DryRun)
	if err != nil {
		return RubricGraded{}, err
	}
	if ev == nil {
		result.Grade = *latest
		return result, nil
	}
	result.Changed = true
	result.Grade = RubricGrade{Event: ev.ID, Grade: d.Grade, Note: d.Note, CheckVersion: d.CheckVersion,
		Snapshot: d.Snapshot, LookedAt: d.LookedAt, At: ev.Wall}
	if spec.DryRun {
		result.Grade.Event = ""
	}
	return result, nil
}

// gradableCriterion checks that a criterion is a rubric item of the Check.
func gradableCriterion(check []Criterion, lessonID, criterion string) error {
	for _, crit := range check {
		if crit.ID != criterion {
			continue
		}
		if crit.Kind != CriterionRubric {
			return invalidf("criterion %s of Lesson %s is a %s criterion, which study check measures; only rubric "+
				"items are graded", criterion, lessonID, crit.Kind)
		}
		return nil
	}
	return &Error{Code: CodeNotFound, Message: "the Check of " + lessonID + " has no criterion " + criterion}
}

// workFiles names files of a Lesson's work by path and content hash. Each
// must be a regular file in the Lesson's practice folder, named relative to
// it or to the Topic.
func (c *Core) workFiles(topicID, lessonID string, names []string) ([]WorkFile, error) {
	if len(names) == 0 {
		return nil, nil
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return nil, err
	}
	defer home.Close()
	defer topic.Close()
	practice := practiceFolder(lessonID)
	var files []WorkFile
	for _, name := range names {
		if _, err := cleanText("file name", name, 1024); err != nil {
			return nil, err
		}
		rel := path.Clean(strings.ReplaceAll(name, "\\", "/"))
		if !strings.HasPrefix(rel, practice+"/") {
			rel = path.Join(practice, rel)
		}
		if strings.HasPrefix(name, "/") || !strings.HasPrefix(rel, practice+"/") {
			return nil, invalidf("%s is not in %s/: name a file of the work there", name, practice)
		}
		info, err := topic.Lstat(rel)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, &Error{Code: CodeNotFound, Message: rel + " does not exist"}
		case err != nil:
			return nil, internalError("reading "+rel, err)
		case !info.Mode().IsRegular():
			return nil, invalidf("%s is not a regular file", rel)
		case info.Size() > maxWorkFileBytes:
			return nil, invalidf("%s is larger than %d MiB", rel, maxWorkFileBytes>>20)
		}
		f, err := topic.Open(rel)
		if err != nil {
			return nil, internalError("reading "+rel, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, io.LimitReader(f, maxWorkFileBytes+1))
		f.Close()
		if err != nil {
			return nil, internalError("reading "+rel, err)
		}
		files = append(files, WorkFile{Path: rel, Hash: "sha256:" + hex.EncodeToString(h.Sum(nil))})
	}
	return files, nil
}

func replayRubricGraded(s *replayed, ev event) error {
	var d rubricGradedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	l := s.study.lesson(d.Lesson)
	if l.grades == nil {
		l.grades = map[string]*RubricGrade{}
	}
	if prev := l.grades[d.Criterion]; prev != nil && d.Replaces != prev.Event {
		s.flag(newFlag(FlagConflict, checkItem(d.Lesson), []string{prev.Event, ev.ID}, d.Criterion,
			fmt.Sprintf("rubric item %s of Lesson %s was graded on two machines, by Events %s and %s: the later "+
				"grade stands; check it with the learner", d.Criterion, d.Lesson, prev.Event, ev.ID)))
	}
	l.grades[d.Criterion] = &RubricGrade{Event: ev.ID, Grade: d.Grade, Note: d.Note, CheckVersion: d.CheckVersion,
		Snapshot: d.Snapshot, LookedAt: d.LookedAt, At: wallOf(ev)}
	return nil
}
