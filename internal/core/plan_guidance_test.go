package core

import (
	"context"
	"testing"
	"time"
)

// startOfTomorrow is the start of the day after now, in now's location.
func startOfTomorrow(now time.Time) time.Time {
	y, mo, d := now.Date()
	return time.Date(y, mo, d+1, 0, 0, 0, 0, now.Location())
}

// withNextStep opens and closes a Session on Topic c with a Next step.
func withNextStep(t *testing.T, m *machine, step string) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CloseSession(ctx, "c", CloseSpec{NextStep: step}); err != nil {
		t.Fatal(err)
	}
}

// A paused Active topic is resumed or left for another, never studied: status
// recommends resume_topic over its Next step, session_open suggests the same
// whatever the Energy, and its Cards are never ready in either.
func TestAPausedTopicIsResumedOrLeftNeverStudied(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	m.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	withNextStep(t, m, "Write answer.txt")
	m.update(t, TopicChanges{State: ptr(TopicPaused)})

	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionResumeTopic || r.Text == "" {
		t.Errorf("recommended for a paused Topic = %+v", r)
	}
	if c := status.Topics[0].Cards; c == nil || c.Ready || !c.Paused || c.NextDue != nil {
		t.Errorf("status's Cards of a paused Topic = %+v", c)
	}
	for _, energy := range []string{EnergyFull, EnergyHalf, EnergyFumes} {
		opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: energy, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !opened.Paused || opened.Cards == nil || opened.Cards.Ready || !opened.Cards.Paused {
			t.Errorf("session_open with %s Energy on a paused Topic = %+v", energy, opened)
		}
		if s := opened.Suggested; s == nil || s.Suggest != SuggestResumeTopic {
			t.Errorf("suggested with %s Energy on a paused Topic = %+v", energy, s)
		}
	}
	if due := m.due(t, DueQuery{Limit: MaxDueLimit}); len(due) != 0 {
		t.Errorf("due_cards on a paused Topic = %d Cards", len(due))
	}

	// Resumed, it is studied from its Next step again, and its Cards are
	// ready.
	m.update(t, TopicChanges{State: ptr(TopicActive)})
	if status, err = m.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionNextStep || r.Text != "Write answer.txt" {
		t.Errorf("recommended once resumed = %+v", r)
	}
	if c := status.Topics[0].Cards; c == nil || !c.Ready || c.Paused {
		t.Errorf("status's Cards once resumed = %+v", c)
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull, DryRun: true})
	if err != nil || opened.Paused || opened.Cards == nil || !opened.Cards.Ready {
		t.Errorf("session_open once resumed = %+v, %v", opened, err)
	}
	if s := opened.Suggested; s == nil || s.Suggest != FocusLearn {
		t.Errorf("suggested once resumed = %+v", s)
	}
}

// A finished Topic offers only its Reviews: status recommends them while
// Cards are ready, and stop once none is, over its Next step and its
// Syllabus; session_open suggests the same.
func TestAFinishedTopicOffersOnlyItsReviews(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	withNextStep(t, m, "Write answer.txt")
	m.update(t, TopicChanges{State: ptr(TopicFinished)})

	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionReviews {
		t.Errorf("recommended for a finished Topic with Cards ready = %+v", r)
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull, DryRun: true})
	if err != nil || opened.Paused || opened.Cards == nil || !opened.Cards.Ready {
		t.Fatalf("session_open on a finished Topic = %+v, %v", opened, err)
	}
	if s := opened.Suggested; s == nil || s.Suggest != FocusReviews {
		t.Errorf("suggested on a finished Topic with Cards ready = %+v", s)
	}

	m.review(t, ReviewSpec{Card: card.ID, Rating: RatingGood, Draft: DraftKeep})
	if status, err = m.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if r := status.Recommended; r == nil || r.Action != ActionStop {
		t.Errorf("recommended for a finished Topic with no Card ready = %+v", r)
	}
	if c := status.Topics[0].Cards; c == nil || c.Ready || c.NextDue == nil {
		t.Errorf("status's Cards of a finished Topic = %+v", c)
	}
	if opened, err = m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if s := opened.Suggested; s == nil || s.Suggest != SuggestStop {
		t.Errorf("suggested on a finished Topic with no Card ready = %+v", s)
	}
}

// Whether drafts are ready follows the Topic's own daily cap on new Cards,
// in status, session_open and due_cards alike.
func TestCardsReadyRespectsTheTopicCap(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	m.update(t, TopicChanges{NewCardsPerDay: intp(1)})
	first := m.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	m.addCard(t, CardSpec{Prompt: "What is 6 × 9?", Answer: "54"})
	m.review(t, ReviewSpec{Card: first.ID, Rating: RatingGood, Draft: DraftKeep})

	// One draft decided today uses the cap of 1, so the other waits for
	// tomorrow, though the default cap would allow it.
	tomorrow := startOfTomorrow(m.now())
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := status.Topics[0].Cards; c == nil || c.Ready || c.NextDue == nil || !c.NextDue.Equal(tomorrow) {
		t.Errorf("status's Cards with the Topic's cap used = %+v, want ready at %s", c, tomorrow)
	}
	opened, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFull, DryRun: true})
	if err != nil || opened.Cards == nil || opened.Cards.Ready {
		t.Errorf("session_open with the Topic's cap used = %+v, %v", opened, err)
	}
	if due := m.due(t, DueQuery{Limit: MaxDueLimit}); len(due) != 0 {
		t.Errorf("due_cards with the Topic's cap used = %d Cards", len(due))
	}

	m.update(t, TopicChanges{NewCardsPerDay: intp(2)})
	if status, err = m.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if c := status.Topics[0].Cards; c == nil || !c.Ready {
		t.Errorf("status's Cards under a raised cap = %+v", c)
	}

	// A cap of 0 takes no new Cards: drafts alone are never ready, and no
	// date is promised.
	none := newTopic(t)
	none.update(t, TopicChanges{NewCardsPerDay: intp(0)})
	none.addCard(t, CardSpec{Prompt: "What is 6 × 7?", Answer: "42"})
	if status, err = none.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if c := status.Topics[0].Cards; c == nil || c.Ready || c.NextDue != nil {
		t.Errorf("status's Cards under a cap of 0 = %+v", c)
	}
}
