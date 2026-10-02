package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Assessments and the Level.
//
// An Assessment is a short, time-boxed quiz: the placement Assessment when a
// Topic is created, and one at the end of each Milestone. It is recorded by
// an assessment.recorded Event with what was asked and how it went, and it
// can set the Topic's Level. The learner can change the Level at any time
// with topic_update (level.set); that choice holds until the next
// Assessment sets it. The Level lives in topic.toml, which the learner can
// read; which Event set it comes from replaying the History. Weak results
// never block anything: they lead the agent to propose a Revision.

const (
	eventAssessmentRecorded = "assessment.recorded"
	eventLevelSet           = "level.set"

	notesFolder = "notes"

	maxAssessmentItems     = 60
	maxAreaRunes           = 100
	maxQuestionRunes       = 500
	maxAssessmentNoteRunes = 1000
	maxAssessmentSummary   = 4000
	maxAssessmentMinutes   = 240
	defaultTimeBoxMinutes  = 15
)

// Levels: how advanced the teaching is for a Topic.
const (
	LevelBeginner     = "beginner"
	LevelIntermediate = "intermediate"
	LevelAdvanced     = "advanced"
	LevelExpert       = "expert"
)

// Where a Topic's Level came from.
const (
	LevelFromAssessment = "assessment"
	LevelFromLearner    = "learner"
)

// Kinds of Assessment.
const (
	AssessmentPlacement = "placement"
	AssessmentMilestone = "milestone"
)

// Outcomes of an Assessment item.
const (
	AnswerCorrect    = "correct"
	AnswerPartly     = "partly"
	AnswerIncorrect  = "incorrect"
	AnswerNotReached = "not_reached"
)

func init() {
	eventKinds[eventAssessmentRecorded] = eventKind{apply: applyAssessmentRecorded, replay: replayAssessmentRecorded}
	eventKinds[eventLevelSet] = eventKind{apply: applyLevelSet, replay: replayLevelSet}
}

// checkLevel validates a Level.
func checkLevel(level string) (string, error) {
	switch level {
	case LevelBeginner, LevelIntermediate, LevelAdvanced, LevelExpert:
		return level, nil
	}
	return "", invalidf("a Level is beginner, intermediate, advanced or expert, not %q", clip(level, 40))
}

// LevelInfo is a Topic's Level and where it came from.
type LevelInfo struct {
	Level string `json:"level"`
	// Source is assessment when an Assessment set it, learner when the
	// learner chose it, through topic_update or by editing topic.toml.
	Source string `json:"source"`
	// Assessment is the Assessment that set it, when it did.
	Assessment string `json:"assessment,omitempty"`
	// At is when the Event that set it was recorded; zero for a Level
	// written into topic.toml by hand.
	At time.Time `json:"at,omitzero"`
}

// AssessmentItem is one question of an Assessment and how it went.
type AssessmentItem struct {
	Area     string `json:"area" jsonschema:"what the question was about, such as pointers"`
	Question string `json:"question,omitempty" jsonschema:"the question asked, in a few words"`
	Outcome  string `json:"outcome" jsonschema:"correct, partly, incorrect, or not_reached when time ran out before it (confirm it during Lessons)"`
	Note     string `json:"note,omitempty" jsonschema:"what the answer showed"`
}

// AssessmentSpec describes an Assessment to record. Its JSON form is what
// study assessment record --file reads.
type AssessmentSpec struct {
	// Kind is placement, at Topic creation, or milestone, at the end of a
	// Milestone.
	Kind string `json:"kind"`
	// Milestone is the Milestone a milestone Assessment ends.
	Milestone string           `json:"milestone,omitempty"`
	Items     []AssessmentItem `json:"items"`
	// Summary says what the Assessment found, in plain words.
	Summary string `json:"summary"`
	// Minutes is how long it took; TimeBox how long it was meant to take,
	// 15 minutes unless set.
	Minutes int `json:"minutes,omitempty"`
	TimeBox int `json:"time_box,omitempty"`
	// Level is the Level the results suggest; when set, it becomes the
	// Topic's Level.
	Level string `json:"level,omitempty"`
	// Notes names the file in notes/ where the agent saved the Assessment,
	// relative to the Topic. Optional; only its path and hash are recorded.
	Notes  string `json:"notes,omitempty"`
	DryRun bool   `json:"-"`
}

// assessmentRecordedData is the payload of an assessment.recorded Event.
type assessmentRecordedData struct {
	Kind      string           `json:"kind"`
	Milestone string           `json:"milestone,omitempty"`
	Items     []AssessmentItem `json:"items"`
	Summary   string           `json:"summary"`
	Minutes   int              `json:"minutes,omitempty"`
	TimeBox   int              `json:"time_box"`
	Level     string           `json:"level,omitempty"`
	Notes     *WorkFile        `json:"notes,omitempty"`
}

// levelSetData is the payload of a level.set Event.
type levelSetData struct {
	Level string `json:"level"`
}

// Assessment is a recorded Assessment.
type Assessment struct {
	// ID is the Event that recorded it.
	ID        string           `json:"id"`
	Kind      string           `json:"kind"`
	Milestone string           `json:"milestone,omitempty"`
	Items     []AssessmentItem `json:"items"`
	Summary   string           `json:"summary"`
	Minutes   int              `json:"minutes,omitempty"`
	TimeBox   int              `json:"time_box"`
	Level     string           `json:"level,omitempty"`
	Notes     *WorkFile        `json:"notes,omitempty"`
	At        time.Time        `json:"at"`
	// Weak lists the areas answered incorrectly or partly: candidates for
	// a Revision, such as a review Lesson. Never a block.
	Weak []string `json:"weak,omitempty"`
	// Confirm lists the areas time ran out before: confirm them during
	// Lessons.
	Confirm []string `json:"confirm,omitempty"`
}

// AssessmentRecorded is the result of RecordAssessment.
type AssessmentRecorded struct {
	Topic      string     `json:"topic"`
	Assessment Assessment `json:"assessment"`
	// Level is the Topic's Level afterwards.
	Level *LevelInfo `json:"level,omitempty"`
	// Changed is false when this Assessment was already recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// assessmentState is what replay knows of a Topic's Assessments, Level
// and hints.
type assessmentState struct {
	assessments []Assessment
	// keys holds each Assessment's content hash, so recording the same
	// Assessment again records nothing.
	keys map[string]string
	// level is the Level the History last set, and by what.
	level *LevelInfo
	// hints are the hints recorded, in replay order, and hintRequests the
	// client's ids for them, so a retry records nothing.
	hints        []Hint
	hintRequests map[string]Hint
}

func (s *replayed) assessing() *assessmentState {
	if s.assess == nil {
		s.assess = &assessmentState{keys: map[string]string{}, hintRequests: map[string]Hint{}}
	}
	return s.assess
}

// RecordAssessment records an Assessment. With a Level, it sets the Topic's
// Level, replacing any the learner chose since the last Assessment.
// Recording the same Assessment again records nothing.
func (c *Core) RecordAssessment(ctx context.Context, topicID string, spec AssessmentSpec) (AssessmentRecorded, error) {
	if err := checkTopicID(topicID); err != nil {
		return AssessmentRecorded{}, err
	}
	d, err := checkAssessment(spec)
	if err != nil {
		return AssessmentRecorded{}, err
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return AssessmentRecorded{}, err
	}
	if d.Kind == AssessmentMilestone {
		if s.study.syllabus == nil {
			return AssessmentRecorded{}, &Error{Code: CodeFailedPrecondition, Message: "Topic " + topicID +
				" has no Syllabus yet, so it has no Milestone to assess: record the placement Assessment instead"}
		}
		if _, ok := s.study.syllabus.milestone(d.Milestone); !ok {
			return AssessmentRecorded{}, &Error{Code: CodeNotFound, Message: "the Syllabus of " + topicID +
				" has no Milestone " + d.Milestone}
		}
	}
	if spec.Notes != "" {
		notes, err := c.notesFile(topicID, spec.Notes)
		if err != nil {
			return AssessmentRecorded{}, err
		}
		d.Notes = &notes
	}
	key := assessmentKey(d)
	var existing string
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		if id, ok := s.assessing().keys[key]; ok {
			existing = id
			return nil, nil
		}
		ch := &change{Type: eventAssessmentRecorded, Data: d}
		if d.Level != "" {
			ch.Items = []string{topicFile}
		}
		return ch, nil
	}, spec.DryRun)
	if err != nil {
		return AssessmentRecorded{}, err
	}
	result := AssessmentRecorded{Topic: topicID, Changed: ev != nil, DryRun: spec.DryRun}
	if ev == nil {
		s, _, err := c.replayTopic(ctx, topicID)
		if err != nil {
			return AssessmentRecorded{}, err
		}
		for _, a := range s.assessing().assessments {
			if a.ID == existing {
				result.Assessment = a
			}
		}
		result.Level = s.assessing().level
		return result, nil
	}
	result.Assessment = assessmentOf(ev.ID, d, ev.Wall)
	if spec.DryRun {
		result.Assessment.ID = ""
	}
	if d.Level != "" {
		result.Level = &LevelInfo{Level: d.Level, Source: LevelFromAssessment, Assessment: result.Assessment.ID, At: ev.Wall}
	} else if !spec.DryRun {
		if topic, err := c.readTopic(topicID); err == nil {
			result.Level = topic.Level
		}
	}
	return result, nil
}

// checkAssessment validates an Assessment and returns its payload, without
// the notes file, which needs the Topic.
func checkAssessment(spec AssessmentSpec) (assessmentRecordedData, error) {
	d := assessmentRecordedData{Kind: spec.Kind, Milestone: strings.TrimSpace(spec.Milestone)}
	switch d.Kind {
	case AssessmentPlacement:
		if d.Milestone != "" {
			return d, invalidf("a placement Assessment is not about a Milestone: leave the Milestone out, or record a " +
				"milestone Assessment")
		}
	case AssessmentMilestone:
		if d.Milestone == "" {
			return d, invalidf("a milestone Assessment names the Milestone it ends")
		}
		if err := validateEntityID("Milestone", d.Milestone); err != nil {
			return d, err
		}
	default:
		return d, invalidf("an Assessment's kind is placement or milestone, not %q", clip(spec.Kind, 40))
	}
	if len(spec.Items) == 0 {
		return d, invalidf("an Assessment needs at least one item: what was asked and how it went")
	}
	if len(spec.Items) > maxAssessmentItems {
		return d, invalidf("an Assessment has at most %d items", maxAssessmentItems)
	}
	for i, it := range spec.Items {
		where := fmt.Sprintf("item %d's ", i+1)
		area, err := cleanText(where+"area", it.Area, maxAreaRunes)
		if err != nil {
			return d, err
		}
		if area == "" {
			return d, invalidf("item %d needs an area: what the question was about", i+1)
		}
		question, err := cleanText(where+"question", it.Question, maxQuestionRunes)
		if err != nil {
			return d, err
		}
		note, err := cleanTextBlock(where+"note", it.Note, maxAssessmentNoteRunes)
		if err != nil {
			return d, err
		}
		switch it.Outcome {
		case AnswerCorrect, AnswerPartly, AnswerIncorrect, AnswerNotReached:
		default:
			return d, invalidf("item %d's outcome is correct, partly, incorrect or not_reached, not %q", i+1,
				clip(it.Outcome, 40))
		}
		d.Items = append(d.Items, AssessmentItem{Area: area, Question: question, Outcome: it.Outcome, Note: note})
	}
	summary, err := cleanTextBlock("summary", spec.Summary, maxAssessmentSummary)
	if err != nil {
		return d, err
	}
	if summary == "" {
		return d, invalidf("an Assessment needs a summary of what it found")
	}
	d.Summary = summary
	if spec.Minutes < 0 || spec.Minutes > maxAssessmentMinutes {
		return d, invalidf("the minutes an Assessment took must be from 0 to %d, not %d", maxAssessmentMinutes, spec.Minutes)
	}
	d.Minutes = spec.Minutes
	d.TimeBox = spec.TimeBox
	switch {
	case d.TimeBox == 0:
		d.TimeBox = defaultTimeBoxMinutes
	case d.TimeBox < 0 || d.TimeBox > maxAssessmentMinutes:
		return d, invalidf("an Assessment's time box must be from 1 to %d minutes, not %d", maxAssessmentMinutes, spec.TimeBox)
	}
	if spec.Level != "" {
		level, err := checkLevel(spec.Level)
		if err != nil {
			return d, err
		}
		d.Level = level
	}
	return d, nil
}

// assessmentKey identifies an Assessment's content, so the same one is not
// recorded twice.
func assessmentKey(d assessmentRecordedData) string {
	data, _ := json.Marshal(d)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// assessmentOf builds the Assessment an Event records.
func assessmentOf(id string, d assessmentRecordedData, at time.Time) Assessment {
	a := Assessment{ID: id, Kind: d.Kind, Milestone: d.Milestone, Items: d.Items, Summary: d.Summary,
		Minutes: d.Minutes, TimeBox: d.TimeBox, Level: d.Level, Notes: d.Notes, At: at}
	for _, it := range d.Items {
		switch it.Outcome {
		case AnswerIncorrect, AnswerPartly:
			a.Weak = appendOnce(a.Weak, it.Area)
		case AnswerNotReached:
			a.Confirm = appendOnce(a.Confirm, it.Area)
		}
	}
	return a
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// notesFile names an Assessment's notes: a regular file under the Topic's
// notes/ folder, recorded by path and content hash.
func (c *Core) notesFile(topicID, name string) (WorkFile, error) {
	if _, err := cleanText("notes file name", name, 1024); err != nil {
		return WorkFile{}, err
	}
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	rel, ok := strings.CutPrefix(clean, notesFolder+"/")
	if !ok || path.IsAbs(clean) || rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return WorkFile{}, invalidf("%s is not in %s/: save the Assessment's notes there and name that file", name, notesFolder)
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return WorkFile{}, err
	}
	defer home.Close()
	defer topic.Close()
	root, err := topic.OpenRoot(notesFolder)
	if errors.Is(err, fs.ErrNotExist) {
		return WorkFile{}, &Error{Code: CodeNotFound, Message: topicID + " has no " + notesFolder + "/ folder yet: save " +
			"the Assessment's notes there first"}
	}
	if err != nil {
		return WorkFile{}, internalError("opening "+notesFolder, err)
	}
	defer root.Close()
	info, err := root.Lstat(rel)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return WorkFile{}, &Error{Code: CodeNotFound, Message: clean + " does not exist"}
	case err != nil:
		return WorkFile{}, invalidf("%s cannot be read inside %s/: %v", clean, notesFolder, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return WorkFile{}, invalidf("%s is a link: name the file itself", clean)
	case !info.Mode().IsRegular():
		return WorkFile{}, invalidf("%s is not a regular file", clean)
	case info.Size() > maxWorkFileBytes:
		return WorkFile{}, invalidf("%s is larger than %d MiB", clean, maxWorkFileBytes>>20)
	}
	f, err := root.Open(rel)
	if err != nil {
		return WorkFile{}, internalError("reading "+clean, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxWorkFileBytes+1)); err != nil {
		return WorkFile{}, internalError("reading "+clean, err)
	}
	return WorkFile{Path: clean, Hash: "sha256:" + hex.EncodeToString(h.Sum(nil))}, nil
}

func applyAssessmentRecorded(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d assessmentRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.Level == "" {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		extra["level"] = d.Level
		return nil
	})
}

func applyLevelSet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d levelSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		extra["level"] = d.Level
		return nil
	})
}

func replayAssessmentRecorded(s *replayed, ev event) error {
	var d assessmentRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if d.Kind != AssessmentPlacement && d.Kind != AssessmentMilestone {
		return fmt.Errorf("its kind %q is not placement or milestone", clip(d.Kind, 40))
	}
	if d.Level != "" {
		if _, err := checkLevel(d.Level); err != nil {
			return err
		}
	}
	st := s.assessing()
	st.keys[assessmentKey(d)] = ev.ID
	st.assessments = append(st.assessments, assessmentOf(ev.ID, d, wallOf(ev)))
	if d.Level != "" {
		st.level = &LevelInfo{Level: d.Level, Source: LevelFromAssessment, Assessment: ev.ID, At: wallOf(ev)}
	}
	return nil
}

func replayLevelSet(s *replayed, ev event) error {
	var d levelSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	if _, err := checkLevel(d.Level); err != nil {
		return err
	}
	s.assessing().level = &LevelInfo{Level: d.Level, Source: LevelFromLearner, At: wallOf(ev)}
	return nil
}

// levelOf reads the Level from topic.toml's settings: empty when unset, and
// a problem when the value is not a Level.
func levelOf(settings topicSettings) (level string, problem string) {
	v, ok := settings.extra["level"]
	if !ok {
		return "", ""
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Sprintf("level in %s must be text: beginner, intermediate, advanced or expert", topicFile)
	}
	if _, err := checkLevel(s); err != nil {
		return "", fmt.Sprintf("level in %s must be beginner, intermediate, advanced or expert, not %q", topicFile, clip(s, 40))
	}
	return s, ""
}

// levelInfo is the Topic's Level as topic.toml holds it, with where it came
// from: the Event that last set it when the file agrees, otherwise the
// learner, who edited the file.
func levelInfo(s *replayed, settings topicSettings) (*LevelInfo, string) {
	level, problem := levelOf(settings)
	if level == "" {
		return nil, problem
	}
	if l := s.assessing().level; l != nil && l.Level == level {
		info := *l
		return &info, ""
	}
	return &LevelInfo{Level: level, Source: LevelFromLearner}, ""
}

// addLevel fills a Topic's Level for status, and reports a Level a hand
// edit broke.
func addLevel(topic *Topic, s *replayed, settings topicSettings) {
	info, problem := levelInfo(s, settings)
	topic.Level = info
	if problem != "" {
		topic.SettingsProblems = append(topic.SettingsProblems, problem)
	}
}

// planLevel plans the learner setting the Level. A Level present in
// topic.toml but unreadable is rewritten even when it reads as the one
// asked for.
func planLevel(topicID, level string) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		data, exists, err := view.read(topicFile)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, corruptf("%s of %s is missing", topicFile, topicID)
		}
		settings, err := parseTopicSettings(data, path.Join(topicID, topicFile))
		if err != nil {
			return nil, err
		}
		if current, problem := levelOf(settings); current == level && problem == "" {
			return nil, nil
		}
		return &change{Type: eventLevelSet, Data: levelSetData{Level: level}, Items: []string{topicFile}}, nil
	}
}

// maxAssessmentFileBytes bounds an Assessment file.
const maxAssessmentFileBytes = 1 << 20

// ReadAssessmentFile reads an Assessment written as JSON, in the shape the
// assessment_record tool takes; a relative path counts from the folder study
// started in.
func (c *Core) ReadAssessmentFile(path string) (AssessmentSpec, error) {
	full := c.expandPath(path)
	data, err := os.ReadFile(full)
	if err != nil {
		return AssessmentSpec{}, &Error{Code: CodeNotFound, Message: "cannot read " + full + ": " + err.Error(), Err: err}
	}
	return ParseAssessment(data, path)
}

// ParseAssessment reads an Assessment written as JSON; name names it in
// errors. Fields it doesn't know are refused, so a misspelt one is never
// silently lost.
func ParseAssessment(data []byte, name string) (AssessmentSpec, error) {
	if len(data) > maxAssessmentFileBytes {
		return AssessmentSpec{}, invalidf("%s is larger than %d KiB", name, maxAssessmentFileBytes>>10)
	}
	var spec AssessmentSpec
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return AssessmentSpec{}, invalidf("%s is not an Assessment in JSON: %v", name, err)
	}
	return spec, nil
}

// AssessmentList is a Topic's Assessments, oldest first.
type AssessmentList struct {
	Topic       string       `json:"topic"`
	Assessments []Assessment `json:"assessments"`
	Level       *LevelInfo   `json:"level,omitempty"`
}

// ListAssessments returns a Topic's Assessments and its Level.
func (c *Core) ListAssessments(ctx context.Context, topicID string) (AssessmentList, error) {
	if err := checkTopicID(topicID); err != nil {
		return AssessmentList{}, err
	}
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return AssessmentList{}, err
	}
	list := AssessmentList{Topic: topicID, Assessments: append([]Assessment{}, s.assessing().assessments...)}
	if topic, err := c.readTopic(topicID); err == nil {
		list.Level = topic.Level
	}
	return list, nil
}
