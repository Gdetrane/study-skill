package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// history builds Events as two machines' Histories, merged, would hold
// them: each with its own ID and time.
type history struct {
	t      *testing.T
	events []event
}

func (h *history) add(id, typ string, data any) {
	h.t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		h.t.Fatal(err)
	}
	at := t0.Add(time.Duration(len(h.events)) * time.Minute)
	h.events = append(h.events, event{Format: 1, ID: id, Time: at, Wall: at, Type: typ, Data: raw,
		raw: fmt.Appendf(nil, "%s %s", id, raw)})
}

func conflicts(s *replayed) []Flag {
	var out []Flag
	for _, f := range s.flags {
		if f.Kind == FlagConflict {
			out = append(out, f)
		}
	}
	return out
}

func withLessonDone(t *testing.T) *history {
	t.Helper()
	h := &history{t: t}
	h.add("a1", eventRevisionProposed, revisionProposedData{Summary: "s", Syllabus: syllabusData{oneLessonSyllabus}})
	h.add("a2", eventRevisionApplied, revisionAppliedData{Revision: "a1", Syllabus: syllabusData{oneLessonSyllabus}})
	h.add("a3", eventLessonCompleted, lessonCompletedData{Lesson: "answer", Attempt: "a0",
		Cards: []cardLine{{ID: "answer.aaaa", Lesson: "answer", Prompt: "A?", Answer: "a"}}})
	return h
}

func TestALessonCompletedOnTwoMachinesIsFlaggedAndKeepsEveryCard(t *testing.T) {
	h := withLessonDone(t)
	h.add("b1", eventLessonCompleted, lessonCompletedData{Lesson: "answer", Attempt: "b0",
		Cards: []cardLine{{ID: "answer.bbbb", Lesson: "answer", Prompt: "B?", Answer: "b"}}})
	s := replay(h.events)
	if c := conflicts(s); len(c) != 1 || !strings.Contains(c[0].Message, "completed twice") {
		t.Errorf("conflicts = %+v", c)
	}
	if len(s.study.cards) != 2 || s.study.lessons["answer"].completed.Attempt != "a0" {
		t.Errorf("cards = %v, completion = %+v; want both Cards and the first completion", s.study.cardOrder,
			s.study.lessons["answer"].completed)
	}
}

func TestAReviewOfADroppedCardIsFlagged(t *testing.T) {
	h := withLessonDone(t)
	h.add("a4", eventReviewRecorded, reviewRecordedData{Card: "answer.aaaa", Draft: DraftDrop})
	h.add("b1", eventReviewRecorded, reviewRecordedData{Card: "answer.aaaa", Rating: RatingGood, Draft: DraftKeep})
	s := replay(h.events)
	if c := conflicts(s); len(c) != 1 || c[0].Item != cardItem("answer.aaaa") {
		t.Errorf("conflicts = %+v", c)
	}
	if cs := s.study.cards["answer.aaaa"]; !cs.dropped || len(cs.reviews) != 0 {
		t.Errorf("Card = %+v, want it still dropped and unreviewed", cs)
	}
}

func TestARevisionRemovingADoneLessonIsFlagged(t *testing.T) {
	h := withLessonDone(t)
	other := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Priority: PriorityMust,
		Lessons: []SyllabusLesson{{ID: "question", Title: "The question"}}}}}
	h.add("b1", eventRevisionProposed, revisionProposedData{Summary: "s", Syllabus: syllabusData{other}})
	h.add("b2", eventRevisionApplied, revisionAppliedData{Revision: "b1", Syllabus: syllabusData{other}})
	if c := conflicts(replay(h.events)); len(c) != 1 || !strings.Contains(c[0].Message, "removed Lesson answer") {
		t.Errorf("conflicts = %+v", c)
	}
}

// TestFSRSNeverSeesTimeRunBackwards is the reviewer's scenario: a Review
// written on a machine whose clock is behind the previous one.
func TestFSRSNeverSeesTimeRunBackwards(t *testing.T) {
	day := func(n int) time.Time { return t0.AddDate(0, 0, n) }
	forward := schedule([]review{{at: day(10), rating: RatingGood}, {at: day(10), rating: RatingGood}})
	backwards := schedule([]review{{at: day(10), rating: RatingGood}, {at: day(0), rating: RatingGood}})
	if !backwards.Equal(forward) {
		t.Errorf("a Review dated before the previous one gives due %v, want %v as if both were on day 10",
			backwards, forward)
	}
	if !backwards.After(day(10)) || backwards.After(day(10).AddDate(1, 0, 0)) {
		t.Errorf("due = %v, want a sane date after day 10", backwards)
	}
}

func TestEditingADraftKeepsCardFieldsLamplightDoesNotKnow(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "practice/answer/answer.txt", "42\n")
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer", Cards: []CardDraft{{Prompt: "Q", Answer: "A"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.home, "c", cardsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withSource := strings.Replace(string(data), `"answer":"A"`, `"answer":"A","source":"notes/day1.md"`, 1)
	if err := os.WriteFile(path, []byte(withSource), 0o644); err != nil {
		t.Fatal(err)
	}
	id := done.Cards[0].ID
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: id, Rating: RatingGood, Draft: DraftEdit,
		Prompt: "Q2", Answer: "A2"}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"source":"notes/day1.md"`) || !strings.Contains(string(data), `"prompt":"Q2"`) {
		t.Errorf("%s = %s, %v", cardsFile, data, err)
	}

	// A union merge that repeats the Card with other content is flagged,
	// and the Card is still readable.
	repeated := string(data) + strings.Replace(strings.TrimSpace(string(data)), "Q2", "Q3", 1) + "\n"
	if err := os.WriteFile(path, []byte(repeated), 0o644); err != nil {
		t.Fatal(err)
	}
	topic, err := m.readTopic("c")
	if err != nil || !hasFlag(topic.Flags, FlagConflict, cardItem(id)) {
		t.Fatalf("flags = %+v, %v; want the repeated Card flagged", topic.Flags, err)
	}
	m.setClock(t0.AddDate(0, 6, 0))
	due, err := m.DueCardsOf(ctx, "c", 0)
	if err != nil || len(due.Cards) != 1 || due.Cards[0].Prompt != "Q2" {
		t.Errorf("due Cards = %+v, %v; want the first line's Card", due, err)
	}
	for _, f := range topic.Flags {
		if f.Kind == FlagConflict {
			if _, err := m.DismissFlag(ctx, "c", f.ID, false); err != nil {
				t.Errorf("dismissing the repeated Card: %v", err)
			}
		}
	}
}
