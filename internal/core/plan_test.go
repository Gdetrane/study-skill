package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

func pacep(p ...PacePeriod) *[]PacePeriod { return &p }

func TestPlanSettings(t *testing.T) {
	m := newTopic(t)
	res := m.update(t, TopicChanges{Deadline: ptr("2026-12-01"),
		Pace: pacep(PacePeriod{HoursPerWeek: 10}, PacePeriod{From: "2026-11-16", HoursPerWeek: 3}), NewCardsPerDay: intp(5)})
	if !res.Changed || res.Topic.Deadline != "2026-12-01" || len(res.Topic.Pace) != 2 || res.Topic.NewCardsPerDay != 5 {
		t.Fatalf("update = %+v", res)
	}
	settings := readSettings(t, filepath.Join(m.home, "c"))
	if p := planOf(settings); p.deadline != "2026-12-01" || p.newCardsPerDay != 5 ||
		!slices.Equal(p.pace, []PacePeriod{{HoursPerWeek: 10}, {From: "2026-11-16", HoursPerWeek: 3}}) || p.problems != nil {
		t.Errorf("topic.toml plan = %+v", p)
	}
	again := m.update(t, TopicChanges{Deadline: ptr("2026-12-01"), Pace: pacep(PacePeriod{HoursPerWeek: 10},
		PacePeriod{From: "2026-11-16", HoursPerWeek: 3}), NewCardsPerDay: intp(5)})
	if again.Changed {
		t.Error("setting the same plan again recorded a change")
	}
	cleared := m.update(t, TopicChanges{Deadline: ptr(""), Pace: pacep()})
	if !cleared.Changed || cleared.Topic.Deadline != "" || cleared.Topic.Pace != nil {
		t.Errorf("clearing = %+v", cleared)
	}
	if data, _ := os.ReadFile(filepath.Join(m.home, "c", topicFile)); strings.Contains(string(data), "deadline") ||
		strings.Contains(string(data), "[[pace]]") {
		t.Errorf("topic.toml after clearing:\n%s", data)
	}
}

func TestPlanSettingsAreValidated(t *testing.T) {
	m := newTopic(t)
	for name, changes := range map[string]TopicChanges{
		"deadline":         {Deadline: ptr("1 Dec")},
		"negative hours":   {Pace: pacep(PacePeriod{HoursPerWeek: -1})},
		"too many hours":   {Pace: pacep(PacePeriod{HoursPerWeek: 200})},
		"undated period":   {Pace: pacep(PacePeriod{HoursPerWeek: 5}, PacePeriod{HoursPerWeek: 3})},
		"periods in order": {Pace: pacep(PacePeriod{From: "2026-11-16", HoursPerWeek: 5}, PacePeriod{From: "2026-11-01", HoursPerWeek: 3})},
		"cap":              {NewCardsPerDay: intp(500)},
		"state":            {State: ptr("sleeping")},
		"task title":       {AddTasks: []TaskSpec{{Title: " "}}},
		"task date":        {AddTasks: []TaskSpec{{Title: "Book", By: "soon"}}},
		"task id":          {RemoveTasks: []string{"../x"}},
	} {
		if _, err := m.UpdateTopic(context.Background(), "c", changes); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
}

// Settings Lamplight does not know survive a plan change, on a Pace period
// that starts the same day too.
func TestPlanKeepsUnknownSettings(t *testing.T) {
	m := newTopic(t)
	path := filepath.Join(m.home, "c", topicFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("level = \"expert\"\n\n[[pace]]\nhours_per_week = 4.0\nmood = \"calm\"\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m.update(t, TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 6}, PacePeriod{From: "2026-12-01", HoursPerWeek: 2})})
	after, _ := os.ReadFile(path)
	for _, want := range []string{`level = "expert"`, `mood = "calm"`, "hours_per_week = 6.0", `from = "2026-12-01"`} {
		if !strings.Contains(string(after), want) {
			t.Errorf("topic.toml lacks %s:\n%s", want, after)
		}
	}
}

func TestTopicStates(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	m.update(t, TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 10})})
	m.addCard(t, CardSpec{Prompt: "Q", Answer: "A"})
	topic, err := m.readTopic("c")
	if err != nil || topic.State != TopicActive || topic.Forecast == nil {
		t.Fatalf("active Topic = %+v, %v", topic, err)
	}

	res := m.update(t, TopicChanges{State: ptr(TopicPaused)})
	if !res.Changed || res.Topic.State != TopicPaused || res.Topic.Forecast != nil {
		t.Errorf("pausing = %+v", res.Topic)
	}
	due, err := m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || !due.Paused || len(due.Cards) != 0 {
		t.Errorf("due Cards of a paused Topic = %+v, %v", due, err)
	}
	if view, err := m.SyllabusOf(ctx, "c"); err != nil || view.Forecast != nil {
		t.Errorf("a paused Topic's Syllabus has a Forecast: %+v, %v", view.Forecast, err)
	}
	if again := m.update(t, TopicChanges{State: ptr(TopicPaused)}); again.Changed {
		t.Error("pausing twice recorded a change")
	}

	m.update(t, TopicChanges{State: ptr(TopicFinished)})
	if due, err := m.DueCardsOf(ctx, "c", DueQuery{}); err != nil || due.Paused || len(due.Cards) != 1 {
		t.Errorf("a finished Topic keeps offering its Cards: %+v, %v", due, err)
	}
	if topic, _ := m.readTopic("c"); topic.State != TopicFinished || topic.Forecast != nil {
		t.Errorf("finished Topic = %+v", topic)
	}
	if topic := m.update(t, TopicChanges{State: ptr(TopicActive)}).Topic; topic.State != TopicActive || topic.Forecast == nil {
		t.Errorf("active again = %+v", topic)
	}
}

func TestTheDailyCapIsATopicSetting(t *testing.T) {
	m := newTopic(t)
	for _, q := range []string{"Q1", "Q2", "Q3"} {
		m.addCard(t, CardSpec{Prompt: q, Answer: "A"})
	}
	if got := m.due(t, DueQuery{}); len(got) != 3 {
		t.Fatalf("drafts offered = %d, want 3", len(got))
	}
	m.update(t, TopicChanges{NewCardsPerDay: intp(1)})
	if got := m.due(t, DueQuery{}); len(got) != 1 {
		t.Errorf("drafts offered under a cap of 1 = %d", len(got))
	}
}

func TestTasks(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	res := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam", By: "2026-11-01"},
		{Title: "Ask for feedback", After: "basics"}}})
	if !res.Changed || len(res.AddedTasks) != 2 {
		t.Fatalf("adding = %+v", res)
	}
	book, feedback := res.AddedTasks[0], res.AddedTasks[1]
	if !taskIDPattern.MatchString(book.ID) || !strings.HasPrefix(book.ID, "book-the-exam.") {
		t.Errorf("Task id = %q", book.ID)
	}
	// The feedback Task waits for its Milestone.
	topic, _ := m.readTopic("c")
	if len(topic.Tasks) != 1 || topic.Tasks[0].ID != book.ID {
		t.Errorf("relevant Tasks = %+v", topic.Tasks)
	}
	if again := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}}); again.Changed ||
		again.AddedTasks[0].ID != book.ID {
		t.Errorf("adding the same Task again = %+v", again)
	}
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{AddTasks: []TaskSpec{{Title: "X", After: "nowhere"}}}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a Task after an unknown Milestone: %v", err)
	}

	done, err := m.MarkTask(ctx, "c", book.ID, true, false)
	if err != nil || !done.Changed || !done.Task.Done {
		t.Fatalf("marking done = %+v, %v", done, err)
	}
	if again, err := m.MarkTask(ctx, "c", book.ID, true, false); err != nil || again.Changed {
		t.Errorf("marking done twice = %+v, %v", again, err)
	}
	if topic, _ := m.readTopic("c"); len(topic.Tasks) != 0 {
		t.Errorf("a done Task is still shown: %+v", topic.Tasks)
	}
	list, err := m.ListTasks(ctx, "c", true)
	if err != nil || len(list.Tasks) != 2 || !list.Tasks[0].Done || list.Tasks[1].Done {
		t.Errorf("all Tasks = %+v, %v", list, err)
	}
	if open, _ := m.ListTasks(ctx, "c", false); len(open.Tasks) != 1 || open.Tasks[0].ID != feedback.ID {
		t.Errorf("open Tasks = %+v", open)
	}
	if undo, err := m.MarkTask(ctx, "c", book.ID, false, false); err != nil || !undo.Changed || undo.Task.Done {
		t.Errorf("reopening = %+v, %v", undo, err)
	}

	completeAnswer(t, m) // the first Milestone is done now
	if topic, _ := m.readTopic("c"); len(topic.Tasks) != 2 {
		t.Errorf("relevant Tasks once the Milestone is done = %+v", topic.Tasks)
	}

	if _, err := m.MarkTask(ctx, "c", "missing.abc123", true, false); CodeOf(err) != CodeNotFound {
		t.Errorf("an unknown Task: %v", err)
	}
	removed := m.update(t, TopicChanges{RemoveTasks: []string{book.ID}})
	if !removed.Changed {
		t.Error("removing a Task recorded nothing")
	}
	if again := m.update(t, TopicChanges{RemoveTasks: []string{book.ID}}); again.Changed {
		t.Error("removing it again recorded a change")
	}
	if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 1 {
		t.Errorf("Tasks after removing = %+v", list.Tasks)
	}
}

// A Task the learner wrote into topic.toml by hand can be marked done: the
// text is the file's, the status the History's.
func TestAHandWrittenTaskCanBeDone(t *testing.T) {
	m := newTopic(t)
	path := filepath.Join(m.home, "c", topicFile)
	data, _ := os.ReadFile(path)
	data = append(data, []byte("\n[[tasks]]\nid = \"call-the-school.abc123\"\ntitle = \"Call the school\"\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if topic, _ := m.readTopic("c"); len(topic.Tasks) != 1 || topic.Tasks[0].Title != "Call the school" {
		t.Fatalf("Tasks = %+v", topic.Tasks)
	}
	if res, err := m.MarkTask(context.Background(), "c", "call-the-school.abc123", true, false); err != nil || !res.Changed {
		t.Fatalf("marking it done = %+v, %v", res, err)
	}
	if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
		t.Errorf("flags = %+v", s.flags)
	}
}

// A hand edit gone wrong in the plan is reported; the rest of the Topic
// stands.
func TestABrokenPlanSettingIsReported(t *testing.T) {
	m := newTopic(t)
	path := filepath.Join(m.home, "c", topicFile)
	data, _ := os.ReadFile(path)
	data = append(data, []byte("deadline = \"soon\"\n\n[[pace]]\nhours_per_week = -3.0\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || len(topic.SettingsProblems) != 2 || topic.Deadline != "" || topic.Pace != nil {
		t.Fatalf("Topic = %+v, %v", topic, err)
	}
	// Setting a new Pace replaces the broken one.
	if res := m.update(t, TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 5})}); len(res.Topic.SettingsProblems) != 1 {
		t.Errorf("after a new Pace: %+v", res.Topic.SettingsProblems)
	}
}

// A dry run shows the Topic as it would be, Forecast included, and writes
// nothing.
func TestPlanDryRun(t *testing.T) {
	m := learningTopic(t)
	before := historyLines(t, filepath.Join(m.home, "c"))
	res, err := m.UpdateTopic(context.Background(), "c", TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 10}),
		Deadline: ptr("2026-12-01"), State: ptr(TopicActive), AddTasks: []TaskSpec{{Title: "Book the exam"}}, DryRun: true})
	if err != nil || !res.Changed {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if res.Topic.Deadline != "2026-12-01" || res.Topic.Forecast == nil || res.Topic.Forecast.Pace != "10 h/week" ||
		len(res.Topic.Tasks) != 1 {
		t.Errorf("dry-run Topic = %+v", res.Topic)
	}
	if after := historyLines(t, filepath.Join(m.home, "c")); len(after) != len(before) {
		t.Errorf("the dry run wrote %d Events", len(after)-len(before))
	}
	if topic, _ := m.readTopic("c"); topic.Deadline != "" || len(topic.Pace) != 0 {
		t.Errorf("the dry run changed topic.toml: %+v", topic)
	}
	paused, err := m.UpdateTopic(context.Background(), "c", TopicChanges{State: ptr(TopicPaused), DryRun: true})
	if err != nil || paused.Topic.State != TopicPaused || paused.Topic.Forecast != nil {
		t.Errorf("dry-run pause = %+v, %v", paused.Topic, err)
	}
}

func TestCrashesInPlanWrites(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		setup func(t *testing.T, m *machine) (state string)
		run   func(m *machine, state string, dryRun bool) (changed bool, err error)
		check func(t *testing.T, m *machine)
	}
	update := func(changes TopicChanges) func(*machine, string, bool) (bool, error) {
		return func(m *machine, _ string, dry bool) (bool, error) {
			changes.DryRun = dry
			res, err := m.UpdateTopic(ctx, "c", changes)
			return res.Changed, err
		}
	}
	nothing := func(*testing.T, *machine) string { return "" }
	addTask := func(t *testing.T, m *machine) string {
		return m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}}).AddedTasks[0].ID
	}
	topic := func(t *testing.T, m *machine) Topic {
		topic, err := m.readTopic("c")
		if err != nil {
			t.Fatal(err)
		}
		return topic
	}
	ops := []op{
		{"deadline.set", nothing, update(TopicChanges{Deadline: ptr("2026-12-01")}),
			func(t *testing.T, m *machine) {
				if d := topic(t, m).Deadline; d != "2026-12-01" {
					t.Errorf("deadline = %q", d)
				}
			}},
		{"pace.set", nothing, update(TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 10})}),
			func(t *testing.T, m *machine) {
				if p := topic(t, m).Pace; len(p) != 1 || p[0].HoursPerWeek != 10 {
					t.Errorf("Pace = %+v", p)
				}
			}},
		{"new_cards_per_day.set", nothing, update(TopicChanges{NewCardsPerDay: intp(3)}),
			func(t *testing.T, m *machine) {
				if n := topic(t, m).NewCardsPerDay; n != 3 {
					t.Errorf("cap = %d", n)
				}
			}},
		{"topic_state.set", nothing, update(TopicChanges{State: ptr(TopicPaused)}),
			func(t *testing.T, m *machine) {
				if s := topic(t, m).State; s != TopicPaused {
					t.Errorf("state = %q", s)
				}
			}},
		{"task.added", nothing, update(TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}}),
			func(t *testing.T, m *machine) {
				if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 1 {
					t.Errorf("Tasks = %+v", list.Tasks)
				}
			}},
		{"task.removed", addTask, func(m *machine, id string, dry bool) (bool, error) {
			res, err := m.UpdateTopic(ctx, "c", TopicChanges{RemoveTasks: []string{id}, DryRun: dry})
			return res.Changed, err
		}, func(t *testing.T, m *machine) {
			if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 0 {
				t.Errorf("Tasks = %+v", list.Tasks)
			}
		}},
		{"task.done", addTask, func(m *machine, id string, dry bool) (bool, error) {
			res, err := m.MarkTask(ctx, "c", id, true, dry)
			return res.Changed, err
		}, func(t *testing.T, m *machine) {
			if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 1 || !list.Tasks[0].Done {
				t.Errorf("Tasks = %+v", list.Tasks)
			}
		}},
		{"task.reopened", func(t *testing.T, m *machine) string {
			id := addTask(t, m)
			if _, err := m.MarkTask(ctx, "c", id, true, false); err != nil {
				t.Fatal(err)
			}
			return id
		}, func(m *machine, id string, dry bool) (bool, error) {
			res, err := m.MarkTask(ctx, "c", id, false, dry)
			return res.Changed, err
		}, func(t *testing.T, m *machine) {
			if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 1 || list.Tasks[0].Done {
				t.Errorf("Tasks = %+v", list.Tasks)
			}
		}},
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				state := o.setup(t, m)
				m.crash = crashOnce(point)
				_, err := o.run(m, state, false)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				dryChanged, dryErr := o.run(m, state, true)
				realChanged, realErr := o.run(m, state, false)
				if CodeOf(dryErr) != CodeOf(realErr) || (dryErr == nil) != (realErr == nil) || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				if hasIntentFile(t, m) {
					t.Error("the intent marker survived the next write")
				}
				if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				o.check(t, m)
			})
		}
	}
}

// The plan lives in topic.toml, one item: two machines changing it from one
// version are flagged, never resolved silently.
func TestPlanChangesOnTwoMachinesAreFlagged(t *testing.T) {
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	b := newMachine(t, t.TempDir(), "b", t0)
	syncTopic(t, a, b)
	a.update(t, TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 10})})
	b.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}})
	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	s := replayLines(t, unionMerge(ours, theirs))
	if len(s.flags) != 1 || s.flags[0].Kind != FlagConflict || s.flags[0].Item != topicFile {
		t.Errorf("flags = %+v, want one conflict on topic.toml", s.flags)
	}
}
