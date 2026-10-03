package core

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// marker is free text with a terminal escape: it must never reach the
// history or lesson views.
const marker = "Secret MARKER \x1b[2J text"

func historyEntries(t *testing.T, m *machine, q HistoryQuery) []HistoryEntry {
	t.Helper()
	q.Limit = MaxHistoryLimit
	v, err := m.HistoryOf(context.Background(), "c", q)
	if err != nil {
		t.Fatal(err)
	}
	return v.Entries
}

func TestHistoryShowsEachEventAsReplayAppliedIt(t *testing.T) {
	m := learningTopic(t)
	practicing(t, m)
	path := filepath.Join(m.home, "c", historyFile)
	// The same id twice with different content: replay applies the first.
	appendLine(t, path, `{"format":1,"id":"zz1","time":"2026-10-02T09:30:00Z","wall":"2026-10-02T09:30:00Z",`+
		`"type":"phase.set","data":{"lesson":"answer","phase":"feedback"}}`+"\n")
	appendLine(t, path, `{"format":1,"id":"zz1","time":"2026-10-02T09:30:00Z","wall":"2026-10-02T09:30:00Z",`+
		`"type":"phase.set","data":{"lesson":"answer","phase":"teaching"}}`+"\n")
	// A later Event from a machine whose wall clock was behind.
	appendLine(t, path, `{"format":1,"id":"zz2","time":"2026-10-02T09:31:00Z","wall":"2026-09-30T08:00:00Z",`+
		`"type":"phase.set","data":{"lesson":"answer","phase":"practicing"}}`+"\n")
	// An Event replay holds: its Phase is unknown.
	appendLine(t, path, `{"format":1,"id":"zz3","time":"2026-10-02T09:32:00Z","wall":"2026-10-02T09:32:00Z",`+
		`"type":"phase.set","data":{"lesson":"answer","phase":"dancing"}}`+"\n")

	entries := historyEntries(t, m, HistoryQuery{Type: "phase."})
	byID := map[string]HistoryEntry{}
	for _, e := range entries {
		if _, twice := byID[e.ID]; twice {
			t.Errorf("Event %s is listed twice", e.ID)
		}
		byID[e.ID] = e
	}
	if e := byID["zz1"]; e.Summary != "Phase set: feedback" {
		t.Errorf("the repeated Event shows %q, want the copy replay applied (feedback)", e.Summary)
	}
	if e := byID["zz2"]; !e.ClockBehind || byID["zz1"].ClockBehind {
		t.Errorf("clock behind: zz2 %v, zz1 %v; want only zz2 marked", e.ClockBehind, byID["zz1"].ClockBehind)
	}
	if e := byID["zz3"]; !e.Held || e.Summary != "Phase set" || byID["zz2"].Held {
		t.Errorf("the held Event = %+v", e)
	}
	status, err := m.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasFlag(status.Topics[0].Flags, FlagHeldEvent, "") {
		t.Errorf("an unknown Phase is not held: %+v", status.Topics[0].Flags)
	}
}

func TestHistoryTiesCardEventsToTheirLesson(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	added, err := m.AddCard(ctx, "c", CardSpec{Lesson: "answer", Prompt: "What is the answer?", Answer: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: added.Card.ID, Rating: RatingGood, Draft: DraftKeep}); err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, e := range historyEntries(t, m, HistoryQuery{Lesson: "answer"}) {
		types = append(types, e.Type)
	}
	got := strings.Join(types, ",")
	if !strings.Contains(got, eventReviewRecorded) || !strings.Contains(got, eventCardAdded) {
		t.Errorf("the Lesson's History lists %s, want its Card Events too", got)
	}
}

// TestHistoryAndLessonNeverEchoText puts a marker in every field of an Event
// of every type, nested Card lines and approvals included, and checks that
// neither the history view nor the lesson view shows it. Ids and fixed
// words would be shown, but a marker with spaces and an escape is neither.
func TestHistoryAndLessonNeverEchoText(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	var types []string
	for typ := range eventKinds {
		types = append(types, typ)
	}
	sort.Strings(types)
	keys := []string{"title", "goal", "summary", "next_step", "context", "note", "quote", "location", "prompt",
		"answer", "learner_said", "question", "area", "describe", "rubric", "kind", "level", "approach", "energy",
		"focus", "state", "rating", "draft", "role", "phase", "outcome", "grade", "criterion", "break_point",
		"milestone", "card", "lesson", "task", "deadline", "reason", "id", "source", "flag", "session", "revision",
		"requested_by", "commit", "for", "notebook", "url", "path"}
	path := filepath.Join(m.home, "c", historyFile)
	for i, typ := range types {
		data := map[string]any{}
		for _, k := range keys {
			data[k] = marker
		}
		line := map[string]any{"id": marker, "lesson": marker, "prompt": marker, "answer": marker}
		data["card"] = line
		data["cards"] = []any{line}
		data["tasks"] = []any{map[string]any{"id": marker, "title": marker}}
		data["approval"] = map[string]any{"via": marker, "learner_said": marker}
		data["items"] = []any{map[string]any{"area": marker, "question": marker, "note": marker}}
		raw, err := json.Marshal(map[string]any{"format": 1, "id": fmt.Sprintf("sweep%02d", i),
			"time": fmt.Sprintf("2026-10-02T10:%02d:00Z", i), "wall": fmt.Sprintf("2026-10-02T10:%02d:00Z", i),
			"type": typ, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		appendLine(t, path, string(raw)+"\n")
	}
	// Free text the learner and the agent wrote through the tools, about
	// Lesson answer.
	practicing(t, m)
	if _, err := m.RecordHint(ctx, "c", HintSpec{Lesson: "answer", RequestedBy: "learner", Note: "Secret MARKER note"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: "Write the Secret MARKER step", Context: "Secret MARKER context"}); err != nil {
		t.Fatal(err)
	}

	h, err := m.HistoryOf(ctx, "c", HistoryQuery{Limit: MaxHistoryLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Entries) < len(types) {
		t.Fatalf("only %d entries for %d swept Event types", len(h.Entries), len(types))
	}
	d, err := m.LessonOf(ctx, "c", "answer")
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"history": h, "lesson": d} {
		data, _ := json.Marshal(v)
		if strings.Contains(string(data), "MARKER") || strings.Contains(string(data), "\\u001b") {
			t.Errorf("the %s view echoes text someone wrote: %s", name, data)
		}
	}
}
