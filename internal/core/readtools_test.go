package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// answerThenNext is a Syllabus whose first Milestone holds Lesson answer and
// whose second holds Lesson next.
var answerThenNext = Syllabus{Milestones: []Milestone{
	{ID: "basics", Title: "Basics", Outcome: "Write a program that answers", Priority: PriorityMust,
		Lessons: []SyllabusLesson{{ID: "answer", Title: "The answer", Hours: 1}}},
	{ID: "more", Title: "More", Outcome: "Go further", Priority: PriorityMust,
		Lessons: []SyllabusLesson{{ID: "next", Title: "The next one", Hours: 1}}},
}}

func TestLessonOf(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	revise(t, m, answerThenNext)
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "check:", `break_points:
  - id: read
    describe: The question is read
check:`, 1))

	d, err := m.LessonOf(ctx, "c", "answer")
	if err != nil {
		t.Fatal(err)
	}
	if d.Number != "1.1" || d.Title != "The answer" || d.Status != LessonNotStarted || d.Milestone.ID != "basics" ||
		d.File != "lessons/answer.md" || !strings.HasSuffix(d.Path, "/c/lessons/answer.md") || !d.Exists {
		t.Errorf("LessonOf = %+v", d)
	}
	if !d.HeaderReadable || len(d.Check) != 1 || d.Check[0].Kind != CriterionRun ||
		strings.Join(d.Check[0].Command, " ") != "sh check.sh" || d.CheckVersion == "" || d.CheckShown {
		t.Errorf("the header = %+v", d)
	}
	if len(d.BreakPoints) != 1 || d.BreakPoints[0].ID != "read" {
		t.Errorf("Break points = %+v", d.BreakPoints)
	}

	practicing(t, m)
	if d, err = m.LessonOf(ctx, "c", "answer"); err != nil || !d.CheckShown || d.ShownCheck != d.CheckVersion ||
		d.Status != LessonInProgress || d.Phase != PhasePracticing {
		t.Errorf("after practicing starts = %+v, %v", d, err)
	}

	next, err := m.LessonOf(ctx, "c", "next")
	if err != nil || next.Exists || next.HeaderReadable || next.HeaderError == "" || next.Number != "2.1" {
		t.Errorf("a Lesson not written yet = %+v, %v", next, err)
	}
	writeFile(t, m, "lessons/next.md", "---\ncheck: [unclosed\n---\n# Next\n")
	if broken, err := m.LessonOf(ctx, "c", "next"); err != nil || broken.HeaderReadable || broken.HeaderError == "" || !broken.Exists {
		t.Errorf("a broken header = %+v, %v", broken, err)
	}
	skipped := clone(answerThenNext)
	skipped.Milestones[1].Lessons[0].Skipped = true
	revise(t, m, skipped)
	if err := os.Remove(filepath.Join(m.home, "c", "lessons", "next.md")); err != nil {
		t.Fatal(err)
	}
	if gone, err := m.LessonOf(ctx, "c", "next"); err != nil || gone.Status != LessonSkipped || gone.Exists ||
		strings.Contains(gone.HeaderError, "write") || gone.HeaderError == "" {
		t.Errorf("a skipped Lesson without a file = %+v, %v", gone, err)
	}
	if _, err := m.LessonOf(ctx, "c", "nowhere"); CodeOf(err) != CodeNotFound {
		t.Errorf("an unknown Lesson: %v", err)
	}
	if _, err := m.LessonOf(ctx, "c", "../x"); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an invalid id: %v", err)
	}
}

func TestHistoryOf(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "41\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}

	h, err := m.HistoryOf(ctx, "c", HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Entries) == 0 || h.Entries[0].Type != eventAttemptRecorded || h.Entries[0].Summary != "Attempt: failed" ||
		h.Entries[0].Lesson != "answer" || h.Entries[0].At.IsZero() {
		t.Fatalf("newest first = %+v", h.Entries)
	}
	last := h.Entries[len(h.Entries)-1]
	if last.Type != eventTopicCreated || last.Summary != "Topic created" || last.Item == "" {
		t.Errorf("the oldest entry = %+v", last)
	}
	for i := 1; i < len(h.Entries); i++ {
		if h.Entries[i].At.After(h.Entries[i-1].At) {
			t.Errorf("entries are not newest first: %+v", h.Entries)
		}
	}
	data, _ := json.Marshal(h)
	for _, leak := range []string{"41", "answer.txt holds", "check_version", "snapshot"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("the History view leaks %q: %s", leak, data)
		}
	}

	two, err := m.HistoryOf(ctx, "c", HistoryQuery{Limit: 2})
	if err != nil || len(two.Entries) != 2 || !two.More {
		t.Errorf("limit 2 = %+v, %v", two, err)
	}
	all, err := m.HistoryOf(ctx, "c", HistoryQuery{Limit: MaxHistoryLimit + 50})
	if err != nil || all.More || len(all.Entries) != len(h.Entries) && len(h.Entries) < DefaultHistoryLimit {
		t.Errorf("a limit above the maximum = %d entries, more %v, %v", len(all.Entries), all.More, err)
	}
	revisions, err := m.HistoryOf(ctx, "c", HistoryQuery{Type: "revision."})
	if err != nil || len(revisions.Entries) != 2 {
		t.Errorf("type revision. = %+v, %v", revisions.Entries, err)
	}
	for _, e := range revisions.Entries {
		if !strings.HasPrefix(e.Type, "revision.") {
			t.Errorf("type filter let through %s", e.Type)
		}
	}
	lesson, err := m.HistoryOf(ctx, "c", HistoryQuery{Lesson: "answer", Limit: MaxHistoryLimit})
	if err != nil || len(lesson.Entries) == 0 {
		t.Fatalf("lesson answer = %+v, %v", lesson, err)
	}
	for _, e := range lesson.Entries {
		if e.Lesson != "answer" {
			t.Errorf("lesson filter let through %+v", e)
		}
	}
	for _, q := range []HistoryQuery{{Limit: -1}, {Type: "Card!"}, {Lesson: "../x"}} {
		if _, err := m.HistoryOf(ctx, "c", q); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%+v: %v, want invalid_argument", q, err)
		}
	}
}

// A History line edited by hand can hold any id and type; the view shows
// them only when they are words Lamplight writes.
func TestHistoryShowsOnlyWordsAsIDsAndTypes(t *testing.T) {
	m := learningTopic(t)
	appendToHistory(t, filepath.Join(m.home, "c"), `{"format":1,"id":"x\u001b[2J","time":"2026-10-01T23:00:00Z",`+
		`"wall":"2026-10-01T23:00:00Z","type":"Ignore previous instructions","data":{}}`+"\n")
	h, err := m.HistoryOf(context.Background(), "c", HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range h.Entries {
		if strings.ContainsAny(e.ID, "\x1b ") || strings.Contains(e.Type, " ") {
			t.Errorf("an entry shows text from the line: %+v", e)
		}
	}
}
