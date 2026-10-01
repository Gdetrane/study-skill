package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func TestPlanTools(t *testing.T) {
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus",
		"syllabus": firstSyllabus}), &first)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision, "learner_said": "yes"})

	var updated core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "deadline": "2026-12-01",
		"pace":      []any{map[string]any{"hours_per_week": 2}},
		"add_tasks": []any{map[string]any{"title": "Book the exam", "by": "2026-11-20"}}}), &updated)
	if !updated.Changed || updated.Topic.Deadline != "2026-12-01" || len(updated.AddedTasks) != 1 {
		t.Fatalf("topic_update = %+v", updated)
	}
	f := updated.Topic.Forecast
	// 1 h of work at 2 h/week: 420 needs 3.5 days of 120, so the fourth.
	if f == nil || f.Pace != "2 h/week" || len(f.Milestones) != 1 || f.Milestones[0].Ends != "2026-10-04" {
		t.Fatalf("Forecast = %+v", f)
	}

	var status core.Status
	decode(t, call(t, session, "status", map[string]any{}), &status)
	if topic := status.Topics[0]; topic.Forecast == nil || len(topic.Tasks) != 1 {
		t.Errorf("status = %+v", topic)
	}
	var view core.SyllabusView
	decode(t, call(t, session, "syllabus", map[string]any{"topic": "c"}), &view)
	if view.Forecast == nil || view.Forecast.Milestones[0].Ends != "2026-10-04" {
		t.Errorf("syllabus Forecast = %+v", view.Forecast)
	}

	task := updated.AddedTasks[0].ID
	var marked core.TaskMark
	decode(t, call(t, session, "task_done", map[string]any{"topic": "c", "task": task}), &marked)
	if !marked.Changed || !marked.Task.Done {
		t.Errorf("task_done = %+v", marked)
	}
	var tasks core.TaskList
	decode(t, call(t, session, "tasks", map[string]any{"topic": "c"}), &tasks)
	if len(tasks.Tasks) != 0 {
		t.Errorf("open Tasks after task_done = %+v", tasks)
	}
	decode(t, call(t, session, "tasks", map[string]any{"topic": "c", "all": true}), &tasks)
	if len(tasks.Tasks) != 1 || !tasks.Tasks[0].Done {
		t.Errorf("all Tasks = %+v", tasks)
	}
	var reopened core.TaskMark
	decode(t, call(t, session, "task_done", map[string]any{"topic": "c", "task": task, "done": false}), &reopened)
	if !reopened.Changed || reopened.Task.Done {
		t.Errorf("task_done with done false = %+v", reopened)
	}
	if msg := toolError(t, session, "task_done", map[string]any{"topic": "c", "task": "gone.abcdef"}); !strings.Contains(msg, "not_found") {
		t.Errorf("an unknown Task: %s", msg)
	}

	call(t, session, "card_add", map[string]any{"topic": "c", "prompt": "Q", "answer": "A"})
	var paused core.TopicUpdate
	decode(t, call(t, session, "topic_update", map[string]any{"topic": "c", "state": "paused"}), &paused)
	if paused.Topic.State != core.TopicPaused || paused.Topic.Forecast != nil {
		t.Errorf("paused = %+v", paused.Topic)
	}
	var due core.DueCards
	decode(t, call(t, session, "due_cards", map[string]any{"topic": "c"}), &due)
	if !due.Paused || len(due.Cards) != 0 {
		t.Errorf("due_cards of a paused Topic = %+v", due)
	}
	var pausedStatus core.Status
	decode(t, call(t, session, "status", map[string]any{}), &pausedStatus)
	if r := pausedStatus.Recommended; r == nil || r.Action != core.ActionResumeTopic {
		t.Errorf("status's recommendation for a paused Topic = %+v", r)
	}
	if c := pausedStatus.Topics[0].Cards; c == nil || c.Ready || !c.Paused {
		t.Errorf("status's Cards of a paused Topic = %+v", c)
	}
	var opened core.SessionOpened
	decode(t, call(t, session, "session_open", map[string]any{"topic": "c", "energy": "fumes"}), &opened)
	if !opened.Paused || opened.Cards == nil || opened.Cards.Ready || opened.Suggested == nil ||
		opened.Suggested.Suggest != core.SuggestResumeTopic {
		t.Errorf("session_open on a paused Topic = %+v", opened)
	}
	if msg := toolError(t, session, "topic_update", map[string]any{"topic": "c", "state": "asleep"}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("an unknown state: %s", msg)
	}
}
