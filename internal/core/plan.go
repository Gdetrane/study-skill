package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A Topic's plan: the Goal's deadline, the Pace, the daily cap on new Cards,
// Tasks, and whether the Topic is active, paused or finished.
//
// The settings live in topic.toml, which the learner can read and edit:
//
//	deadline = "2026-12-01"           the Goal's deadline, optional
//	new_cards_per_day = 10            the daily cap on new Cards
//
//	[[pace]]                          dated periods, in order
//	hours_per_week = 10.0             the first may leave out from: from now on
//
//	[[pace]]
//	from = "2026-11-16"
//	hours_per_week = 3.0
//
// Tasks live in tasks.jsonl, one per line, so two machines adding Tasks
// merge without a conflict:
//
//	{"format":1,"id":"book-the-exam.k3f9a2","title":"Book the exam","by":"2026-11-01","after":"core"}
//
// Status is never stored (ADR-0005): whether the Topic is paused or
// finished, and which Tasks are done, come from replaying the History.

const (
	tasksFile = "tasks.jsonl"

	eventDeadlineSet       = "deadline.set"
	eventPaceSet           = "pace.set"
	eventNewCardsPerDaySet = "new_cards_per_day.set"
	eventTopicStateSet     = "topic_state.set"
	eventTaskAdded         = "task.added"
	eventTaskRemoved       = "task.removed"
	eventTaskDone          = "task.done"
	eventTaskReopened      = "task.reopened"

	maxPacePeriods    = 20
	maxHoursPerWeek   = 168
	maxNewCardsPerDay = 100
	maxTasks          = 50
	maxTaskIDBytes    = 100
)

// Topic states.
const (
	TopicActive   = "active"
	TopicPaused   = "paused"
	TopicFinished = "finished"
)

func init() {
	fileCodecs = append(fileCodecs, codecEntry{pattern: tasksFile, codec: jsonlTolerantCodec})
	eventKinds[eventDeadlineSet] = eventKind{apply: applyDeadlineSet, replay: replayTopicUpdated}
	eventKinds[eventPaceSet] = eventKind{apply: applyPaceSet, replay: replayTopicUpdated}
	eventKinds[eventNewCardsPerDaySet] = eventKind{apply: applyNewCardsPerDaySet, replay: replayTopicUpdated}
	eventKinds[eventTopicStateSet] = eventKind{apply: applyNothing, replay: replayTopicStateSet}
	eventKinds[eventTaskAdded] = eventKind{apply: applyTaskAdded, replay: replayTaskAdded}
	eventKinds[eventTaskRemoved] = eventKind{apply: applyTaskRemoved, replay: replayTaskRemoved}
	eventKinds[eventTaskDone] = eventKind{apply: applyNothing, replay: replayTaskMarked(true)}
	eventKinds[eventTaskReopened] = eventKind{apply: applyNothing, replay: replayTaskMarked(false)}
}

// PacePeriod is a stretch of time with its planned hours a week. A period
// lasts until the next one starts; the first may leave out From to mean
// from the start.
type PacePeriod struct {
	From         string  `json:"from,omitempty" jsonschema:"the period's first day, YYYY-MM-DD; leave it out on the first period to mean from now on"`
	HoursPerWeek float64 `json:"hours_per_week" jsonschema:"hours a week the learner plans for this Topic in the period; 0 for a break"`
}

// Task is a step toward the Goal that is not study, such as booking an
// exam.
type Task struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// By is a date the learner wants it done by, YYYY-MM-DD; shown as is,
	// never counted as late.
	By string `json:"by,omitempty"`
	// After is a Milestone's id: the Task becomes relevant once that
	// Milestone is done.
	After string `json:"after,omitempty"`
	Done  bool   `json:"done,omitempty"`
	// Note explains how the Task is shown, such as when the Milestone it
	// waits for is no longer in the Syllabus.
	Note string `json:"note,omitempty"`
}

// TaskSpec describes a Task to add.
type TaskSpec struct {
	Title string `json:"title" jsonschema:"the step, starting with a verb, such as Book the exam"`
	By    string `json:"by,omitempty" jsonschema:"a date the learner wants it done by, YYYY-MM-DD; optional"`
	After string `json:"after,omitempty" jsonschema:"a Milestone id: the Task is shown once that Milestone is done; optional"`
}

// taskLine is one line of tasks.jsonl.
type taskLine struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	By     string `json:"by,omitempty"`
	After  string `json:"after,omitempty"`
}

var newTaskIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]{1,26}$`)

func taskItem(id string) string { return tasksFile + "#" + id }

// checkTaskID accepts any id a learner may have written by hand, short of
// one that cannot name an item: control characters, "#", or too long.
func checkTaskID(id string) error {
	if id == "" || len(id) > maxTaskIDBytes || !utf8.ValidString(id) || strings.ContainsRune(id, '#') ||
		strings.ContainsFunc(id, unicode.IsControl) {
		return invalidf("%q is not a Task id: study status and study task list show them", id)
	}
	return nil
}

// planningState is what replay knows of a Topic's plan.
type planningState struct {
	// state is the Topic's state, empty meaning active, and stateBy the
	// Event that set it.
	state   string
	stateBy string
	// done holds the Tasks marked done. The last mark wins: marking a Task
	// done on one machine and not done on another is not flagged.
	done map[string]bool
	// removed holds the Tasks removed, so removing one twice records
	// nothing.
	removed map[string]bool
}

func (s *replayed) planning() *planningState {
	if s.plans == nil {
		s.plans = &planningState{done: map[string]bool{}, removed: map[string]bool{}}
	}
	return s.plans
}

// topicState is the Topic's state: active, paused or finished.
func (s *replayed) topicState() string {
	if st := s.planning().state; st != "" {
		return st
	}
	return TopicActive
}

// topicSettingsPlan is the plan topic.toml holds.
type topicSettingsPlan struct {
	deadline       string
	pace           []PacePeriod
	newCardsPerDay int
	// capSet is set when topic.toml sets the cap at all.
	capSet bool
	// broken names the settings present but unreadable, so a request for
	// the value they would default to still rewrites them.
	broken map[string]bool
	// problems says what in topic.toml could not be read; the rest stands.
	problems []string
}

// planOf reads the plan from topic.toml's settings. A setting that cannot
// be read is left out and named in problems, so a hand edit gone wrong
// never makes the Topic unreadable.
func planOf(settings topicSettings) topicSettingsPlan {
	p := topicSettingsPlan{newCardsPerDay: NewCardsPerDay, broken: map[string]bool{}}
	fail := func(key, problem string) {
		p.broken[key] = true
		p.problems = append(p.problems, problem)
	}
	if d, err := date(settings.extra, "deadline", topicFile); err != nil {
		fail("deadline", err.Error())
	} else if d != "" {
		if _, ok := parseDay(d); !ok {
			fail("deadline", fmt.Sprintf("the deadline in %s must be written YYYY-MM-DD, not %q", topicFile, d))
		} else {
			p.deadline = d
		}
	}
	if pace, err := paceFromSettings(settings.extra["pace"]); err != nil {
		fail("pace", err.Error())
	} else {
		p.pace = pace
	}
	if _, set := settings.extra["new_cards_per_day"]; set {
		p.capSet = true
		n, err := number(settings.extra, "new_cards_per_day", topicFile)
		switch {
		case err != nil:
			fail("new_cards_per_day", err.Error())
		case n != math.Trunc(n) || n < 0 || n > maxNewCardsPerDay:
			fail("new_cards_per_day", fmt.Sprintf("new_cards_per_day in %s must be a whole number from 0 to %d, not %v",
				topicFile, maxNewCardsPerDay, n))
		default:
			p.newCardsPerDay = int(n)
		}
	}
	return p
}

func paceFromSettings(v any) ([]PacePeriod, error) {
	rows, err := tables(v, "pace in "+topicFile)
	if err != nil {
		return nil, err
	}
	var periods []PacePeriod
	for i, row := range rows {
		where := fmt.Sprintf("Pace period %d in %s", i+1, topicFile)
		from, err := date(row, "from", where)
		if err != nil {
			return nil, err
		}
		if _, ok := row["hours_per_week"]; !ok {
			return nil, fmt.Errorf("%s has no hours_per_week: give the hours a week, 0 for a break", where)
		}
		hours, err := number(row, "hours_per_week", where)
		if err != nil {
			return nil, err
		}
		periods = append(periods, PacePeriod{From: from, HoursPerWeek: hours})
	}
	if _, err := checkPace(periods); err != nil {
		return nil, fmt.Errorf("the Pace in %s: %s", topicFile, err.Error())
	}
	return periods, nil
}

// checkPace validates Pace periods: whole dates, in order, each from a later
// day than the one before, and hours a week from 0 to 168.
func checkPace(periods []PacePeriod) ([]PacePeriod, error) {
	if len(periods) > maxPacePeriods {
		return nil, invalidf("a Pace has at most %d periods", maxPacePeriods)
	}
	var last time.Time
	out := make([]PacePeriod, 0, len(periods))
	for i, p := range periods {
		h := p.HoursPerWeek
		if math.IsNaN(h) || math.IsInf(h, 0) || h < 0 || h > maxHoursPerWeek {
			return nil, invalidf("Pace period %d plans %v hours a week: give 0 to %d", i+1, h, maxHoursPerWeek)
		}
		from := strings.TrimSpace(p.From)
		switch {
		case from == "" && i > 0:
			return nil, invalidf("Pace period %d needs the day it starts: only the first period can go without one", i+1)
		case from != "":
			d, ok := parseDay(from)
			if !ok {
				return nil, invalidf("Pace period %d starts %q: write the day as YYYY-MM-DD, such as 2026-11-16", i+1, from)
			}
			if i > 0 && !d.After(last) {
				return nil, invalidf("Pace period %d starts %s, not after the period before it", i+1, from)
			}
			last = d
		}
		out = append(out, PacePeriod{From: from, HoursPerWeek: h})
	}
	return out, nil
}

// checkDate validates an optional date written YYYY-MM-DD.
func checkDate(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, ok := parseDay(s); !ok {
		return "", invalidf("the %s must be written YYYY-MM-DD, such as 2026-12-01, not %q", field, s)
	}
	return s, nil
}

// checkTopicState validates a Topic state.
func checkTopicState(state string) (string, error) {
	switch state {
	case TopicActive, TopicPaused, TopicFinished:
		return state, nil
	}
	return "", invalidf("a Topic's state is active, paused or finished, not %q", state)
}

// Event payloads.
type deadlineSetData struct {
	Deadline string `json:"deadline"`
}

type paceSetData struct {
	Periods []PacePeriod `json:"periods"`
}

type newCardsPerDaySetData struct {
	NewCardsPerDay int `json:"new_cards_per_day"`
}

// topicStateSetData is the payload of topic_state.set: the new state and
// the one the writer saw, so a disagreement between machines is flagged.
type topicStateSetData struct {
	State string `json:"state"`
	From  string `json:"from"`
}

type taskAddedData struct {
	Tasks []taskLine `json:"tasks"`
}

type taskRemovedData struct {
	Tasks []string `json:"tasks"`
}

type taskMarkedData struct {
	Task string `json:"task"`
}

// editSettings applies a change to topic.toml's settings and encodes them.
func editSettings(ev event, item string, current []byte, exists bool, edit func(extra map[string]any) error) ([]byte, bool, error) {
	if item != topicFile {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("%s is missing", topicFile)
	}
	settings, err := parseTopicSettings(current, topicFile)
	if err != nil {
		return nil, false, err
	}
	if settings.extra == nil {
		settings.extra = map[string]any{}
	}
	if err := edit(settings.extra); err != nil {
		return nil, false, err
	}
	data, err := encodeTopicSettings(settings)
	return data, err == nil, err
}

func unreadable(ev event) error {
	return corruptf("Event %s has an unreadable payload", ev.ID)
}

func applyDeadlineSet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d deadlineSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		if d.Deadline == "" {
			delete(extra, "deadline")
		} else {
			extra["deadline"] = d.Deadline
		}
		return nil
	})
}

// applyPaceSet replaces the [[pace]] periods. Keys this version does not
// know stay on the period that starts the same day.
func applyPaceSet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d paceSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		old, _ := tables(extra["pace"], "pace")
		if len(d.Periods) == 0 {
			delete(extra, "pace")
			return nil
		}
		rows := make([]map[string]any, 0, len(d.Periods))
		for _, p := range d.Periods {
			row := map[string]any{}
			for _, o := range old {
				if from, _ := date(o, "from", "pace"); from == p.From {
					for k, v := range o {
						row[k] = v
					}
				}
			}
			delete(row, "from")
			if p.From != "" {
				row["from"] = p.From
			}
			row["hours_per_week"] = p.HoursPerWeek
			rows = append(rows, row)
		}
		extra["pace"] = rows
		return nil
	})
}

func applyNewCardsPerDaySet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d newCardsPerDaySetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	return editSettings(ev, item, current, exists, func(extra map[string]any) error {
		extra["new_cards_per_day"] = int64(d.NewCardsPerDay)
		return nil
	})
}

// applyTaskAdded writes one added Task's line of tasks.jsonl.
func applyTaskAdded(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d taskAddedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	for _, t := range d.Tasks {
		if taskItem(t.ID) == item {
			data, err := json.Marshal(t)
			return data, err == nil, err
		}
	}
	return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
}

// applyTaskRemoved removes one Task's line from tasks.jsonl.
func applyTaskRemoved(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d taskRemovedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, unreadable(ev)
	}
	for _, id := range d.Tasks {
		if taskItem(id) == item {
			return nil, false, nil
		}
	}
	return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
}

func replayTaskAdded(s *replayed, ev event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	var d taskAddedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return unreadable(ev)
	}
	for _, t := range d.Tasks {
		delete(s.planning().removed, t.ID)
	}
	return nil
}

func replayTaskRemoved(s *replayed, ev event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	var d taskRemovedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return unreadable(ev)
	}
	for _, id := range d.Tasks {
		s.planning().removed[id] = true
	}
	return nil
}

// replayTopicStateSet records the Topic's state. A change made without
// seeing the latest one, such as pausing on one machine while finishing on
// another, is flagged when the two disagree.
func replayTopicStateSet(s *replayed, ev event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	var d topicStateSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return unreadable(ev)
	}
	if _, err := checkTopicState(d.State); err != nil {
		return corruptf("Event %s sets the unknown state %q", ev.ID, d.State)
	}
	p := s.planning()
	current := s.topicState()
	if d.From != "" && d.From != current && d.State != current && p.stateBy != "" {
		s.flag(newFlag(FlagConflict, "", []string{p.stateBy, ev.ID}, "",
			fmt.Sprintf("the Topic was made %s by Event %s and %s by Event %s, probably on two machines: "+
				"it is %s now; change it with topic_update if that is not what the learner wants",
				current, p.stateBy, d.State, ev.ID, d.State)))
	}
	p.state, p.stateBy = d.State, ev.ID
	return nil
}

// replayTaskMarked records a Task as done or not. It never waits for the
// Task's creation: a Task the learner added to tasks.jsonl by hand can be
// marked done too, and its text comes from the file anyway.
func replayTaskMarked(done bool) func(*replayed, event) error {
	return func(s *replayed, ev event) error {
		if s.created.IsZero() {
			return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
		}
		var d taskMarkedData
		if err := json.Unmarshal(ev.Data, &d); err != nil || d.Task == "" {
			return unreadable(ev)
		}
		if done {
			s.planning().done[d.Task] = true
		} else {
			delete(s.planning().done, d.Task)
		}
		return nil
	}
}

// topicPlanSettings reads the plan from topic.toml through view.
func topicPlanSettings(view *topicView, topicID string) (topicSettings, topicSettingsPlan, error) {
	data, exists, err := view.read(topicFile)
	if err != nil {
		return topicSettings{}, topicSettingsPlan{}, err
	}
	if !exists {
		return topicSettings{}, topicSettingsPlan{}, corruptf("%s of %s is missing", topicFile, topicID)
	}
	settings, err := parseTopicSettings(data, filepath.Join(topicID, topicFile))
	if err != nil {
		return topicSettings{}, topicSettingsPlan{}, err
	}
	return settings, planOf(settings), nil
}

// readTasks reads tasks.jsonl through view, in file order: each Task once,
// as the History's resolution picks it, and a problem for each line that
// is not a Task, which never keeps the others from being read. Tasks the
// view holds but the file does not yet, as recovery or a dry run would
// write them, follow in id order.
func readTasks(view *topicView) ([]Task, []string, error) {
	data, _, err := readFile(view.root, tasksFile)
	if err != nil {
		return nil, nil, err
	}
	var tasks []Task
	var problems []string
	seen := map[string]bool{}
	read := func(id string) error {
		content, exists, err := view.read(taskItem(id))
		if err != nil || !exists {
			return err
		}
		var tl taskLine
		if err := json.Unmarshal(content, &tl); err != nil || strings.TrimSpace(tl.Title) == "" {
			problems = append(problems, fmt.Sprintf("Task %s in %s has no title: fix or delete its line", id, tasksFile))
			return nil
		}
		tasks = append(tasks, Task{ID: id, Title: tl.Title, By: tl.By, After: tl.After})
		return nil
	}
	for n, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil || head.ID == "" {
			problems = append(problems, fmt.Sprintf("line %d of %s is not a Task with an id: fix or delete it", n+1, tasksFile))
			continue
		}
		if seen[head.ID] {
			continue
		}
		seen[head.ID] = true
		if err := checkTaskID(head.ID); err != nil {
			problems = append(problems, fmt.Sprintf("line %d of %s: %v", n+1, tasksFile, err))
			continue
		}
		if err := read(head.ID); err != nil {
			return nil, nil, err
		}
	}
	var pending []string
	for item := range view.pending {
		if id, ok := strings.CutPrefix(item, tasksFile+"#"); ok && !seen[id] {
			pending = append(pending, id)
		}
	}
	slices.Sort(pending)
	for _, id := range pending {
		if err := read(id); err != nil {
			return nil, nil, err
		}
	}
	return tasks, problems, nil
}

// planDeadline plans setting the Goal's deadline; "" removes it. A deadline
// present but unreadable is rewritten even when the request matches what it
// reads as.
func planDeadline(topicID, deadline string) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		_, p, err := topicPlanSettings(view, topicID)
		if err != nil {
			return nil, err
		}
		if p.deadline == deadline && !p.broken["deadline"] {
			return nil, nil
		}
		return &change{Type: eventDeadlineSet, Data: deadlineSetData{Deadline: deadline}, Items: []string{topicFile}}, nil
	}
}

// planPace plans replacing the Pace periods; none removes the Pace.
func planPace(topicID string, periods []PacePeriod) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		_, p, err := topicPlanSettings(view, topicID)
		if err != nil {
			return nil, err
		}
		if slices.Equal(p.pace, periods) && !p.broken["pace"] {
			return nil, nil
		}
		return &change{Type: eventPaceSet, Data: paceSetData{Periods: periods}, Items: []string{topicFile}}, nil
	}
}

// planNewCardsPerDay plans setting the daily cap on new Cards. Setting it
// to the default writes it too, so the setting is explicit.
func planNewCardsPerDay(topicID string, n int) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		_, p, err := topicPlanSettings(view, topicID)
		if err != nil {
			return nil, err
		}
		if p.capSet && !p.broken["new_cards_per_day"] && p.newCardsPerDay == n {
			return nil, nil
		}
		return &change{Type: eventNewCardsPerDaySet, Data: newCardsPerDaySetData{NewCardsPerDay: n}, Items: []string{topicFile}}, nil
	}
}

// planTopicState plans setting the Topic's state.
func planTopicState(state string) plan {
	return func(s *replayed, _ *topicView) (*change, error) {
		current := s.topicState()
		if current == state {
			return nil, nil
		}
		return &change{Type: eventTopicStateSet, Data: topicStateSetData{State: state, From: current}}, nil
	}
}

// planTasksAdded plans adding Tasks; one with the title of a Task already
// there is that Task, and is not added again.
func (c *Core) planTasksAdded(topicID string, specs []TaskSpec, added *[]Task) plan {
	return func(s *replayed, view *topicView) (*change, error) {
		tasks, _, err := readTasks(view)
		if err != nil {
			return nil, err
		}
		*added = (*added)[:0]
		var fresh []taskLine
		for _, spec := range specs {
			if i := slices.IndexFunc(tasks, func(t Task) bool { return t.Title == spec.Title }); i >= 0 {
				t := tasks[i]
				t.Done = s.planning().done[t.ID]
				*added = append(*added, t)
				continue
			}
			if spec.After != "" {
				if s.study.syllabus == nil {
					return nil, invalidf("Topic %s has no Syllabus yet, so Task %q cannot wait for a Milestone", topicID, spec.Title)
				}
				if _, ok := s.study.syllabus.milestone(spec.After); !ok {
					return nil, invalidf("the Syllabus of %s has no Milestone %q to put Task %q after", topicID, spec.After, spec.Title)
				}
			}
			tl := taskLine{Format: FormatVersion, ID: c.newTaskID(spec.Title, tasks, fresh), Title: spec.Title,
				By: spec.By, After: spec.After}
			fresh = append(fresh, tl)
			*added = append(*added, Task{ID: tl.ID, Title: tl.Title, By: tl.By, After: tl.After})
		}
		if len(tasks)+len(fresh) > maxTasks {
			return nil, invalidf("a Topic holds at most %d Tasks: remove some that no longer matter", maxTasks)
		}
		if len(fresh) == 0 {
			return nil, nil
		}
		items := make([]string, len(fresh))
		for i, t := range fresh {
			items[i] = taskItem(t.ID)
		}
		return &change{Type: eventTaskAdded, Data: taskAddedData{Tasks: fresh}, Items: items}, nil
	}
}

// planTasksRemoved plans removing Tasks. A Task removed already changes
// nothing; one that never existed is refused.
func planTasksRemoved(topicID string, ids []string) plan {
	return func(s *replayed, view *topicView) (*change, error) {
		tasks, _, err := readTasks(view)
		if err != nil {
			return nil, err
		}
		var gone []string
		for _, id := range ids {
			if !slices.ContainsFunc(tasks, func(t Task) bool { return t.ID == id }) {
				if s.planning().removed[id] {
					continue
				}
				return nil, &Error{Code: CodeNotFound, Message: "Topic " + topicID + " has no Task " + id +
					": study task list shows its Tasks"}
			}
			if !slices.Contains(gone, id) {
				gone = append(gone, id)
			}
		}
		if len(gone) == 0 {
			return nil, nil
		}
		items := make([]string, len(gone))
		for i, id := range gone {
			items[i] = taskItem(id)
		}
		return &change{Type: eventTaskRemoved, Data: taskRemovedData{Tasks: gone}, Items: items}, nil
	}
}

// newTaskID returns an id for a Task: a slug of its title and a random
// suffix, so two machines never pick the same one.
func (c *Core) newTaskID(title string, existing []Task, fresh []taskLine) string {
	slug := slugify(title)
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		slug = "task"
	}
	for {
		id := slug + "." + randomID()[:6]
		if !slices.ContainsFunc(existing, func(t Task) bool { return t.ID == id }) &&
			!slices.ContainsFunc(fresh, func(t taskLine) bool { return t.ID == id }) {
			return id
		}
	}
}

// checkTaskSpecs validates Tasks to add.
func checkTaskSpecs(specs []TaskSpec) ([]TaskSpec, error) {
	out := make([]TaskSpec, 0, len(specs))
	for _, spec := range specs {
		title, err := requiredText("Task's title", spec.Title, maxTitleRunes)
		if err != nil {
			return nil, err
		}
		by, err := checkDate("Task's date", spec.By)
		if err != nil {
			return nil, err
		}
		after := strings.TrimSpace(spec.After)
		if after != "" {
			if err := validateEntityID("Milestone", after); err != nil {
				return nil, err
			}
		}
		out = append(out, TaskSpec{Title: title, By: by, After: after})
	}
	return out, nil
}

// TaskMark is the result of MarkTask.
type TaskMark struct {
	Topic string `json:"topic"`
	Task  Task   `json:"task"`
	// Changed is false when the Task already was as asked.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// MarkTask marks a Task done, or not done when done is false. Marking it
// again records nothing. Any Task in tasks.jsonl can be marked, one the
// learner wrote there by hand included.
func (c *Core) MarkTask(ctx context.Context, topicID, taskID string, done, dryRun bool) (TaskMark, error) {
	if err := checkTopicID(topicID); err != nil {
		return TaskMark{}, err
	}
	if err := checkTaskID(taskID); err != nil {
		return TaskMark{}, err
	}
	var task Task
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, view *topicView) (*change, error) {
		tasks, _, err := readTasks(view)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(tasks, func(t Task) bool { return t.ID == taskID })
		if i < 0 {
			return nil, &Error{Code: CodeNotFound, Message: "Topic " + topicID + " has no Task " + taskID +
				": study task list shows its Tasks"}
		}
		task = tasks[i]
		task.Done = s.planning().done[taskID]
		if task.Done == done {
			return nil, nil
		}
		kind := eventTaskDone
		if !done {
			kind = eventTaskReopened
		}
		return &change{Type: kind, Data: taskMarkedData{Task: taskID}}, nil
	}, dryRun)
	if err != nil {
		return TaskMark{}, err
	}
	if ev != nil {
		task.Done = done
	}
	return TaskMark{Topic: topicID, Task: task, Changed: ev != nil, DryRun: dryRun}, nil
}

// TaskList lists a Topic's Tasks.
type TaskList struct {
	Topic string `json:"topic"`
	Tasks []Task `json:"tasks"`
	// Problems names the lines of tasks.jsonl that are not Tasks.
	Problems []string `json:"problems,omitempty"`
}

// ListTasks lists a Topic's Tasks in order, the done ones only with all.
func (c *Core) ListTasks(ctx context.Context, topicID string, all bool) (TaskList, error) {
	s, _, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return TaskList{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return TaskList{}, err
	}
	defer home.Close()
	defer topic.Close()
	tasks, problems, err := readTasks(newView(topic, s))
	if err != nil {
		return TaskList{}, err
	}
	out := TaskList{Topic: topicID, Tasks: []Task{}, Problems: problems}
	for _, t := range tasks {
		t.Done = s.planning().done[t.ID]
		if all || !t.Done {
			out.Tasks = append(out.Tasks, t)
		}
	}
	return out, nil
}

// relevantTasks are the open Tasks to show now: those without a Milestone,
// those whose Milestone is done, and those whose Milestone a Revision
// removed, with a note, so a Task never waits forever.
func relevantTasks(s *replayed, tasks []Task) []Task {
	var out []Task
	for _, t := range tasks {
		if s.planning().done[t.ID] {
			continue
		}
		if t.After != "" {
			if s.study.syllabus == nil {
				t.Note = "it waits for Milestone " + t.After + ", and the Topic has no Syllabus"
			} else if _, ok := s.study.syllabus.milestone(t.After); !ok {
				t.Note = "it waited for Milestone " + t.After + ", which is no longer in the Syllabus"
			} else if !s.study.milestoneDone(t.After) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// milestoneDone reports whether every Lesson of a Milestone is done or
// skipped.
func (st *studyState) milestoneDone(id string) bool {
	if st.syllabus == nil {
		return false
	}
	m, ok := st.syllabus.milestone(id)
	if !ok {
		return false
	}
	for _, l := range m.Lessons {
		if v := st.lessonView(l); v.Status != LessonDone && v.Status != LessonSkipped {
			return false
		}
	}
	return true
}

// topicCap is the Topic's daily cap on new Cards decided: its setting, or
// the default when it sets none or one that cannot be read.
func topicCap(view *topicView, topicID string) int {
	if _, p, err := topicPlanSettings(view, topicID); err == nil {
		return p.newCardsPerDay
	}
	return NewCardsPerDay
}

// addPlan fills a Topic's plan for status: its state, the Goal's deadline,
// the Pace, the daily cap on new Cards, the Forecast and the relevant
// Tasks. A paused or finished Topic has no Forecast.
func (c *Core) addPlan(topic *Topic, s *replayed, settings topicSettings, view *topicView) {
	p := planOf(settings)
	topic.State = s.topicState()
	topic.Deadline = p.deadline
	topic.Pace = p.pace
	topic.NewCardsPerDay = p.newCardsPerDay
	topic.SettingsProblems = p.problems
	tasks, problems, err := readTasks(view)
	if err != nil {
		problems = append(problems, fmt.Sprintf("%s cannot be read: %v", tasksFile, err))
	}
	topic.Tasks = relevantTasks(s, tasks)
	topic.SettingsProblems = append(topic.SettingsProblems, problems...)
	topic.Forecast = nil
	if topic.State == TopicActive {
		topic.Forecast = forecastOf(&s.study, p.pace, p.deadline, c.now())
	}
}
