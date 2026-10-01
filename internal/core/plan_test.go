package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		"task id":          {RemoveTasks: []string{"bad\x01id"}},
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
	if !newTaskIDPattern.MatchString(book.ID) || !strings.HasPrefix(book.ID, "book-the-exam.") {
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

func writeTasksFile(t *testing.T, m *machine, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(m.home, "c", tasksFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Task the learner wrote into tasks.jsonl by hand, with any id, can be
// marked done and removed: the text is the file's, the status the
// History's. A line that is not a Task is reported, and the others work.
func TestAHandWrittenTaskCanBeDone(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	writeTasksFile(t, m, `{"id":"Call School","title":"Call the school"}`+"\nnot a task\n"+`{"id":"no-title"}`+"\n")
	topic, _ := m.readTopic("c")
	if len(topic.Tasks) != 1 || topic.Tasks[0].ID != "Call School" || len(topic.SettingsProblems) != 2 {
		t.Fatalf("Tasks = %+v, problems %v", topic.Tasks, topic.SettingsProblems)
	}
	if res, err := m.MarkTask(ctx, "c", "Call School", true, false); err != nil || !res.Changed {
		t.Fatalf("marking it done = %+v, %v", res, err)
	}
	// Adding a Task keeps the broken lines as they are, and stays
	// idempotent.
	added := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}})
	if again := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}}); again.Changed ||
		again.AddedTasks[0].ID != added.AddedTasks[0].ID {
		t.Errorf("adding it again = %+v", again)
	}
	if data, _ := os.ReadFile(filepath.Join(m.home, "c", tasksFile)); !strings.Contains(string(data), "not a task\n") {
		t.Errorf("tasks.jsonl lost the line it could not read:\n%s", data)
	}
	// Re-adding a done Task's title reports it done.
	if again := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Call the school"}}}); again.Changed ||
		!again.AddedTasks[0].Done {
		t.Errorf("re-adding a done Task = %+v", again)
	}
	if res := m.update(t, TopicChanges{RemoveTasks: []string{"Call School"}}); !res.Changed {
		t.Error("removing the hand-written Task recorded nothing")
	}
	if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
		t.Errorf("flags = %+v", s.flags)
	}
	if _, err := m.MarkTask(ctx, "c", "with#hash", true, false); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an id that cannot name an item: %v", err)
	}
}

// Removing a Task that never existed is refused; one removed already
// changes nothing.
func TestRemovingTasks(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	id := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}}).AddedTasks[0].ID
	if _, err := m.UpdateTopic(ctx, "c", TopicChanges{RemoveTasks: []string{"never.abcdef"}}); CodeOf(err) != CodeNotFound {
		t.Errorf("removing an unknown Task: %v", err)
	}
	m.update(t, TopicChanges{RemoveTasks: []string{id}})
	if again := m.update(t, TopicChanges{RemoveTasks: []string{id}}); again.Changed {
		t.Error("removing a Task twice recorded a change")
	}
}

// A dry run invents no Task id; the real run picks one.
func TestADryRunAddShowsNoID(t *testing.T) {
	m := newTopic(t)
	res, err := m.UpdateTopic(context.Background(), "c", TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}, DryRun: true})
	if err != nil || !res.DryRun || len(res.AddedTasks) != 1 || res.AddedTasks[0].ID != "" ||
		len(res.Topic.Tasks) != 1 || res.Topic.Tasks[0].ID != "" {
		t.Errorf("dry run = %+v, %v", res, err)
	}
}

// A Task can only wait for a Milestone the Syllabus has; once a Revision
// drops it, the Task is shown anyway, with a note, never hidden forever.
func TestATaskWaitingForAMilestone(t *testing.T) {
	ctx := context.Background()
	bare := newTopic(t)
	if _, err := bare.UpdateTopic(ctx, "c", TopicChanges{AddTasks: []TaskSpec{{Title: "X", After: "basics"}}}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a Task after a Milestone without a Syllabus: %v", err)
	}
	m := learningTopic(t)
	id := m.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Ask for feedback", After: "basics"}}}).AddedTasks[0].ID
	if topic, _ := m.readTopic("c"); len(topic.Tasks) != 0 {
		t.Fatalf("a Task waiting for its Milestone is shown: %+v", topic.Tasks)
	}
	renamed := clone(oneLessonSyllabus)
	renamed.Milestones[0].ID = "first"
	revise(t, m, renamed)
	topic, _ := m.readTopic("c")
	if len(topic.Tasks) != 1 || topic.Tasks[0].ID != id || !strings.Contains(topic.Tasks[0].Note, "no longer in the Syllabus") {
		t.Errorf("Tasks = %+v", topic.Tasks)
	}
}

// Two machines adding Tasks merge without a conflict.
func TestTasksAddedOnTwoMachinesMerge(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoCardMachines(t)
	a.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Book the exam"}}})
	takeCheckpoint(t, a)
	b.setClock(t0.Add(2 * time.Minute))
	b.update(t, TopicChanges{AddTasks: []TaskSpec{{Title: "Ask for feedback"}}})
	takeCheckpoint(t, b)
	pull(t, a, b)
	pull(t, b, a)
	for _, m := range []*machine{a, b} {
		list, err := m.ListTasks(ctx, "c", true)
		if err != nil || len(list.Tasks) != 2 {
			t.Errorf("%s: Tasks = %+v, %v", m.home, list.Tasks, err)
		}
		noFlags(t, m)
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

// Each documented removal fixes a setting present but broken, even when it
// reads as what is asked: the broken key goes, and so does the problem.
func TestRemovingABrokenSettingFixesIt(t *testing.T) {
	for name, tc := range map[string]struct {
		line    string
		changes TopicChanges
	}{
		"deadline":           {`deadline = "soon"`, TopicChanges{Deadline: ptr("")}},
		"pace":               {"[[pace]]\nhours_per_week = -3.0", TopicChanges{Pace: pacep()}},
		"pace without hours": {"[[pace]]\nfrom = \"2026-10-01\"", TopicChanges{Pace: pacep()}},
		"cap":                {`new_cards_per_day = "ten"`, TopicChanges{NewCardsPerDay: intp(NewCardsPerDay)}},
	} {
		t.Run(name, func(t *testing.T) {
			m := newTopic(t)
			path := filepath.Join(m.home, "c", topicFile)
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, append(data, []byte(tc.line+"\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			if topic, _ := m.readTopic("c"); len(topic.SettingsProblems) != 1 {
				t.Fatalf("problems = %v", topic.SettingsProblems)
			}
			res := m.update(t, tc.changes)
			if !res.Changed || len(res.Topic.SettingsProblems) != 0 {
				t.Errorf("after the fix: changed %v, problems %v", res.Changed, res.Topic.SettingsProblems)
			}
		})
	}
}

// Setting the cap to the default writes it, so it is explicit.
func TestSettingTheCapToTheDefault(t *testing.T) {
	m := newTopic(t)
	if res := m.update(t, TopicChanges{NewCardsPerDay: intp(NewCardsPerDay)}); !res.Changed {
		t.Error("setting the cap to the default recorded nothing")
	}
	if again := m.update(t, TopicChanges{NewCardsPerDay: intp(NewCardsPerDay)}); again.Changed {
		t.Error("setting it again recorded a change")
	}
}

// A topic.toml left with git's conflict markers says how to fix it.
func TestAConflictInTopicSettingsSaysHowToFixIt(t *testing.T) {
	m := newTopic(t)
	path := filepath.Join(m.home, "c", topicFile)
	data, _ := os.ReadFile(path)
	conflicted := strings.Replace(string(data), `title = "C"`,
		"<<<<<<< HEAD\ntitle = \"C\"\n=======\ntitle = \"C on B\"\n>>>>>>> b", 1)
	if err := os.WriteFile(path, []byte(conflicted), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := m.readTopic("c")
	if CodeOf(err) != CodeCorrupt || !strings.Contains(err.Error(), "resolve the merge conflict in topic.toml") {
		t.Errorf("err = %v", err)
	}
}

// Making a Topic paused on one machine and finished on another, from the
// same state, is flagged; agreeing is not.
func TestStatesChangedOnTwoMachines(t *testing.T) {
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	syncTopic(t, a, b)
	a.update(t, TopicChanges{State: ptr(TopicPaused)})
	b.update(t, TopicChanges{State: ptr(TopicFinished)})
	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	s := replayLines(t, unionMerge(ours, theirs))
	if len(s.flags) != 1 || s.flags[0].Kind != FlagConflict || s.topicState() != TopicFinished {
		t.Errorf("flags = %+v, state %s", s.flags, s.topicState())
	}
	c := newMachine(t, t.TempDir(), "c", t0.Add(2*time.Minute))
	syncTopic(t, a, c)
	c.update(t, TopicChanges{State: ptr(TopicPaused)})
	if s := replayLines(t, unionMerge(historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(c.home, "c")))); len(s.flags) != 0 {
		t.Errorf("two machines agreeing were flagged: %+v", s.flags)
	}
}

// A paused Topic stays paused until the learner resumes it: opening a
// Session says so, and its Cards are not available.
func TestAPausedTopicStaysPaused(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	m.addCard(t, CardSpec{Prompt: "Q", Answer: "A"})
	m.update(t, TopicChanges{State: ptr(TopicPaused)})
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull})
	if err != nil || !opened.Paused {
		t.Errorf("session_open on a paused Topic = %+v, %v", opened, err)
	}
	if topic, _ := m.readTopic("c"); topic.State != TopicPaused {
		t.Errorf("state after a Session = %s", topic.State)
	}
	s, _, err := m.replayTopic(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	home, root, err := m.openTopicFolder("c")
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	defer root.Close()
	if got := cardsAvailable(s, newView(root, s), "c", m.now()); !got.Paused || got.Ready {
		t.Errorf("Cards of a paused Topic = %+v", got)
	}
	m.update(t, TopicChanges{State: ptr(TopicActive)})
	s, _, _ = m.replayTopic(ctx, "c")
	if got := cardsAvailable(s, newView(root, s), "c", m.now()); got.Paused || !got.Ready {
		t.Errorf("Cards of an active Topic = %+v", got)
	}
}

// A finished Topic's Syllabus has no Forecast.
func TestAFinishedTopicHasNoForecast(t *testing.T) {
	m := learningTopic(t)
	m.update(t, TopicChanges{Pace: pacep(PacePeriod{HoursPerWeek: 10}), State: ptr(TopicFinished)})
	view, err := m.SyllabusOf(context.Background(), "c")
	if err != nil || view.Forecast != nil {
		t.Errorf("Syllabus of a finished Topic: Forecast %+v, %v", view.Forecast, err)
	}
}

// A write that crashes part way through an update of several settings is
// finished by recovery, and the retry agrees with its dry run.
func TestACrashAcrossSeveralSettings(t *testing.T) {
	ctx := context.Background()
	crashNth := func(point string, n int64) func(string) error {
		var seen atomic.Int64
		return func(p string) error {
			if p == point && seen.Add(1) == n {
				return errCrash
			}
			return nil
		}
	}
	for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
		for n := int64(1); n <= 4; n++ {
			t.Run(fmt.Sprintf("%s/%d", point, n), func(t *testing.T) {
				m := learningTopic(t)
				changes := TopicChanges{Deadline: ptr("2026-12-01"), Pace: pacep(PacePeriod{HoursPerWeek: 10}),
					AddTasks: []TaskSpec{{Title: "Book the exam"}}, State: ptr(TopicPaused)}
				m.crash = crashNth(point, n)
				_, err := m.UpdateTopic(ctx, "c", changes)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("first run: %v", err)
				}
				if err != nil && n > 1 && point != crashAfterIntent && !strings.Contains(err.Error(), "recorded but not finished") {
					t.Errorf("the error does not say the step was recorded: %v", err)
				}
				dry := changes
				dry.DryRun = true
				d, derr := m.UpdateTopic(ctx, "c", dry)
				r, rerr := m.UpdateTopic(ctx, "c", changes)
				if (derr == nil) != (rerr == nil) || d.Changed != r.Changed || d.Topic.State != r.Topic.State ||
					d.Topic.Deadline != r.Topic.Deadline || len(d.Topic.Pace) != len(r.Topic.Pace) {
					t.Errorf("dry run (%+v, %v) disagrees with the real run (%+v, %v)", d, derr, r, rerr)
				}
				if list, _ := m.ListTasks(ctx, "c", true); len(list.Tasks) != 1 {
					t.Errorf("Tasks = %+v", list.Tasks)
				}
				if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				if topic, _ := m.readTopic("c"); topic.State != TopicPaused || topic.Deadline != "2026-12-01" || len(topic.Pace) != 1 {
					t.Errorf("Topic = %+v", topic)
				}
			})
		}
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
	b.update(t, TopicChanges{Deadline: ptr("2026-12-01")})
	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	s := replayLines(t, unionMerge(ours, theirs))
	if len(s.flags) != 1 || s.flags[0].Kind != FlagConflict || s.flags[0].Item != topicFile {
		t.Errorf("flags = %+v, want one conflict on topic.toml", s.flags)
	}
}
