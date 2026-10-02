package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func TestLessonAndHistoryTools(t *testing.T) {
	withGitIdentity(t)
	home := t.TempDir()
	session := connect(t, home)
	checkTopic(t, session, home)

	res := call(t, session, "lesson", map[string]any{"topic": "c", "lesson": "answer"})
	var d core.LessonDetail
	decode(t, res, &d)
	kinds := []string{}
	for _, c := range d.Check {
		kinds = append(kinds, c.Kind)
	}
	if d.Number != "1.1" || d.Status != core.LessonInProgress || d.Phase != core.PhasePracticing || !d.Exists ||
		!d.HeaderReadable || !d.CheckShown || strings.Join(kinds, ",") != "run,rubric,held_out" ||
		len(d.BreakPoints) != 1 || d.BreakPoints[0].ID != "read" {
		t.Errorf("lesson = %+v", d)
	}

	var h core.HistoryView
	decode(t, call(t, session, "history", map[string]any{"topic": "c"}), &h)
	if len(h.Entries) == 0 || h.Entries[0].Type != "checkpoint.taken" && h.Entries[0].Type != "phase.set" {
		t.Fatalf("history = %+v", h.Entries)
	}
	var phases core.HistoryView
	decode(t, call(t, session, "history", map[string]any{"topic": "c", "type": "phase.", "lesson": "answer"}), &phases)
	if len(phases.Entries) != 1 || phases.Entries[0].Summary != "Phase set: practicing" {
		t.Errorf("phase history = %+v", phases.Entries)
	}

	for name, out := range map[string]any{"lesson": d, "history": h} {
		data, _ := json.Marshal(out)
		if strings.Contains(string(data), "expected.txt") || strings.Contains(string(data), ".heldout") {
			t.Errorf("%s shows Held-out data: %s", name, data)
		}
	}
	if msg := toolError(t, session, "lesson", map[string]any{"topic": "c", "lesson": "nowhere"}); !strings.Contains(msg, "not_found") {
		t.Errorf("an unknown Lesson: %s", msg)
	}
	if msg := toolError(t, session, "history", map[string]any{"topic": "c", "limit": -1}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("history with a negative limit: %s", msg)
	}
}
