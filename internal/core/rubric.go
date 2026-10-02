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
	if len(cur.files) == 0 {
		return RubricGraded{}, &Error{Code: CodeFailedPrecondition, Message: practiceFolder(spec.Lesson) +
			"/ holds no work to grade: the learner's work, such as typed final answers or a photo of paper work, goes there"}
	}
	files, err := c.workFiles(topicID, spec.Lesson, spec.LookedAt, cur.files)
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

// workFiles names files of a Lesson's work by their path in the Topic and
// their content hash. Each is named relative to the practice folder or to
// the Topic, and must be a file the work's snapshot sees (snapshot holds
// them, relative to the practice folder): never an ignored file, nor a link.
func (c *Core) workFiles(topicID, lessonID string, names []string, snapshot map[string]string) ([]WorkFile, error) {
	var files []WorkFile
	for _, name := range names {
		p, err := workFilePath(lessonID, name)
		if err != nil {
			return nil, err
		}
		hash, err := c.hashWorkFile(topicID, lessonID, p, snapshot)
		if err != nil {
			return nil, err
		}
		files = append(files, WorkFile{Path: p, Hash: hash})
	}
	return files, nil
}

// workFilePath turns a file name given relative to a Lesson's practice
// folder, or to the Topic, into its path in the Topic, refusing anything
// outside the practice folder.
func workFilePath(lessonID, name string) (string, error) {
	if _, err := cleanText("file name", name, 1024); err != nil {
		return "", err
	}
	practice := practiceFolder(lessonID)
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	rel := strings.TrimPrefix(clean, practice+"/")
	if name == "" || path.IsAbs(clean) || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", invalidf("%s is not in %s/: name a file of the work there", name, practice)
	}
	return practice + "/" + rel, nil
}

// hashWorkFile hashes one file of a Lesson's work, named by its path in the
// Topic. The file is opened inside the practice folder itself, so no link
// can lead out of it, and it must be one the work's snapshot sees.
func (c *Core) hashWorkFile(topicID, lessonID, topicPath string, snapshot map[string]string) (string, error) {
	practice := practiceFolder(lessonID)
	rel, ok := strings.CutPrefix(topicPath, practice+"/")
	if !ok {
		return "", invalidf("%s is not in %s/", topicPath, practice)
	}
	if _, seen := snapshot[rel]; !seen {
		return "", invalidf("%s is ignored, or is not a file of the work: a grade can only look at the files the "+
			"work's snapshot sees", topicPath)
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return "", err
	}
	defer home.Close()
	defer topic.Close()
	root, err := topic.OpenRoot(practice)
	if err != nil {
		return "", internalError("opening "+practice, err)
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", &Error{Code: CodeNotFound, Message: topicPath + " does not exist"}
	case err != nil:
		return "", invalidf("%s cannot be read inside %s/: %v", topicPath, practice, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", invalidf("%s is a link: name the file itself", topicPath)
	case !info.Mode().IsRegular():
		return "", invalidf("%s is not a regular file", topicPath)
	case info.Size() > maxWorkFileBytes:
		return "", invalidf("%s is larger than %d MiB", topicPath, maxWorkFileBytes>>20)
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", internalError("reading "+topicPath, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", invalidf("%s is not a regular file", topicPath)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxWorkFileBytes+1)); err != nil {
		return "", internalError("reading "+topicPath, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func replayRubricGraded(s *replayed, ev event) error {
	var d rubricGradedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if err := validateEntityID("Lesson", d.Lesson); err != nil {
		return err
	}
	if err := validateEntityID("criterion", d.Criterion); err != nil {
		return err
	}
	switch d.Grade {
	case GradeMet, GradePartly, GradeNotMet:
	default:
		return fmt.Errorf("its grade %q is not met, partly or not_met", clip(d.Grade, 40))
	}
	for _, f := range d.LookedAt {
		if !strings.HasPrefix(f.Path, practiceFolder(d.Lesson)+"/") {
			return fmt.Errorf("it names %q, which is not in %s/", clip(f.Path, 200), practiceFolder(d.Lesson))
		}
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
