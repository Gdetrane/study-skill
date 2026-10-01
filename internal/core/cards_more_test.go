package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReFlaggingADismissedCardIsANewFlag(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	if _, err := m.FlagCard(ctx, "c", card.ID, "unclear", false); err != nil {
		t.Fatal(err)
	}
	topic, _ := m.readTopic("c")
	if _, err := m.DismissFlag(ctx, "c", topic.Flags[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if got := cardOn(t, m, card.ID); got.Flagged {
		t.Error("a Card whose flag was dismissed still reads as flagged")
	}
	res, err := m.FlagCard(ctx, "c", card.ID, "unclear", false)
	if err != nil || !res.Changed || !res.Card.Flagged {
		t.Fatalf("flagging it again with the same note = %+v, %v; want a new flag", res, err)
	}
	if topic, _ := m.readTopic("c"); len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagCardFlagged {
		t.Errorf("flags = %+v, want the new flag", topic.Flags)
	}
}

// A delete and a Review made on two machines before syncing are flagged
// whichever replays first (scen4 and scen5).
func TestADeleteAndAReviewOnTwoMachinesAreFlaggedInBothOrders(t *testing.T) {
	for _, reviewFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("review first %v", reviewFirst), func(t *testing.T) {
			ctx := context.Background()
			a, b, card := twoCardMachines(t)
			reviewAt, deleteAt := t0.Add(time.Hour), t0.Add(2*time.Hour)
			if !reviewFirst {
				reviewAt, deleteAt = deleteAt, reviewAt
			}
			b.setClock(reviewAt)
			b.review(t, ReviewSpec{Card: card, Draft: DraftKeep, Rating: RatingGood})
			takeCheckpoint(t, b)
			a.setClock(deleteAt)
			if _, err := a.DeleteCard(ctx, "c", card, false); err != nil {
				t.Fatal(err)
			}
			takeCheckpoint(t, a)
			pull(t, a, b)
			topic, err := a.readTopic("c")
			if err != nil || !hasFlag(topic.Flags, FlagConflict, cardItem(card)) {
				t.Errorf("flags = %+v, %v; want a conflict on the Card", topic.Flags, err)
			}
			if list, _ := a.ListCards(ctx, "c", CardQuery{}); len(list.Cards) != 0 {
				t.Errorf("the deleted Card is listed: %+v", list.Cards)
			}
		})
	}

	// On one machine, reviewing and later deleting is no conflict.
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	m.review(t, ReviewSpec{Card: card.ID, Draft: DraftKeep, Rating: RatingGood})
	if _, err := m.DeleteCard(ctx, "c", card.ID, false); err != nil {
		t.Fatal(err)
	}
	noFlags(t, m)
}

func TestReviewsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	first := m.addCard(t, CardSpec{Prompt: "P1", Answer: "A"})
	second := m.addCard(t, CardSpec{Prompt: "P2", Answer: "A"})
	events := func() int { return len(historyLines(t, filepath.Join(m.home, "c"))) }

	rec := m.review(t, ReviewSpec{Card: first.ID, Draft: DraftKeep, Rating: RatingGood, Request: "r-1"})
	n := events()
	again := m.review(t, ReviewSpec{Card: first.ID, Draft: DraftKeep, Rating: RatingGood, Request: "r-1"})
	if again.Changed || !again.Card.Due.Equal(rec.Card.Due) || again.Rating != RatingGood || events() != n {
		t.Errorf("a retry with the same request = %+v; want the recorded Review and nothing recorded", again)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: second.ID, Draft: DraftKeep, Rating: RatingGood, Request: "r-1"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("the same request for another Card: err = %v, want invalid_argument", err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: second.ID, Rating: RatingGood, Draft: DraftKeep, Request: "no spaces"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a malformed request id: err = %v", err)
	}

	// A retried first decision, without a request id, is recognised too.
	m.review(t, ReviewSpec{Card: second.ID, Draft: DraftDrop})
	n = events()
	dropAgain, err := m.RecordReview(ctx, "c", ReviewSpec{Card: second.ID, Draft: DraftDrop})
	if err != nil || dropAgain.Changed || !dropAgain.Dropped || events() != n {
		t.Errorf("dropping again = %+v, %v; want the recorded drop", dropAgain, err)
	}
	keepAgain, err := m.RecordReview(ctx, "c", ReviewSpec{Card: first.ID, Draft: DraftKeep, Rating: RatingGood})
	if err != nil || keepAgain.Changed {
		t.Errorf("keeping again = %+v, %v; want the recorded decision", keepAgain, err)
	}
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: first.ID, Draft: DraftKeep, Rating: RatingHard}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a different decision on a decided Card: err = %v, want invalid_argument", err)
	}

	// A crash after the Event, then the retry: the Review is recorded once.
	third := m.addCard(t, CardSpec{Prompt: "P3", Answer: "A"})
	m.crash = crashOnce(crashAfterEvent)
	if _, err := m.RecordReview(ctx, "c", ReviewSpec{Card: third.ID, Draft: DraftKeep, Rating: RatingEasy, Request: "r-3"}); !errors.Is(err, errCrash) {
		t.Fatalf("the crash: %v", err)
	}
	m.crash = nil
	retry := m.review(t, ReviewSpec{Card: third.ID, Draft: DraftKeep, Rating: RatingEasy, Request: "r-3"})
	if retry.Changed || retry.Card.Draft || retry.Card.Due.IsZero() {
		t.Errorf("the retry after a crash = %+v; want the Review already recorded", retry)
	}
	if got := strings.Count(strings.Join(historyLines(t, filepath.Join(m.home, "c")), ""), `"request":"r-3"`); got != 1 {
		t.Errorf("the History holds the Review %d times", got)
	}
}

func TestADryRunReviewShowsTheCardAfterIt(t *testing.T) {
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	dry := m.review(t, ReviewSpec{Card: card.ID, Draft: DraftKeep, Rating: RatingGood, DryRun: true})
	if !dry.Changed || dry.Card.Draft || dry.Card.Due.Sub(t0) < 24*time.Hour {
		t.Errorf("a dry run = %+v; want the Card no longer a draft and due in days", dry)
	}
	real := m.review(t, ReviewSpec{Card: card.ID, Draft: DraftKeep, Rating: RatingGood})
	if !real.Card.Due.Equal(dry.Card.Due) {
		t.Errorf("the dry run said due %v, the Review gives %v", dry.Card.Due, real.Card.Due)
	}
}

func TestExploreIsNotALesson(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	bad := Syllabus{Milestones: []Milestone{{ID: "m", Title: "M", Lessons: []SyllabusLesson{{ID: "explore", Title: "Explore"}}}}}
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", Syllabus: bad}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("a Lesson named explore: err = %v, want invalid_argument", err)
	}
	if _, err := m.AddCard(ctx, "c", CardSpec{Lesson: "explore", Prompt: "P", Answer: "A"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("card_add with lesson explore: err = %v, want invalid_argument", err)
	}
}

func TestCardsCiteEvidence(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	src := m.addSource(t, SourceSpec{URL: "https://example.com/pivots"})
	kept := m.recordEvidence(t, EvidenceSpec{Lesson: "l", Source: src.ID, Quote: "a pivot is the first nonzero entry"}).Evidence
	gone := m.recordEvidence(t, EvidenceSpec{Lesson: "l", Source: src.ID, Quote: "wrong quote"}).Evidence
	if _, err := m.RetractEvidence(ctx, "c", gone.ID, false); err != nil {
		t.Fatal(err)
	}
	card := m.addCard(t, CardSpec{Prompt: "What is a pivot?", Answer: "The first nonzero entry", Evidence: []string{kept.ID, kept.ID}})
	if len(card.Evidence) != 1 || card.Evidence[0] != kept.ID || !strings.Contains(readCardsFile(t, m), `"evidence":["`+kept.ID+`"]`) {
		t.Errorf("the Card = %+v, file:\n%s", card, readCardsFile(t, m))
	}
	for _, tc := range []struct {
		ids  []string
		want ErrorCode
	}{{[]string{"nope"}, CodeNotFound}, {[]string{gone.ID}, CodeInvalidArgument}} {
		if _, err := m.AddCard(ctx, "c", CardSpec{Prompt: "Q " + tc.ids[0], Answer: "A", Evidence: tc.ids}); CodeOf(err) != tc.want {
			t.Errorf("citing %v: err = %v, want %s", tc.ids, err, tc.want)
		}
	}
	res, err := m.EditCard(ctx, "c", CardEdit{Card: card.ID, Evidence: []string{}})
	if err != nil || !res.Changed || len(res.Card.Evidence) != 0 || strings.Contains(readCardsFile(t, m), "evidence") {
		t.Errorf("removing the Evidence = %+v, %v; file:\n%s", res, err, readCardsFile(t, m))
	}
}

func TestUnsuspendingOffersTheCardAgain(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	if _, err := m.SuspendCard(ctx, "c", card.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if !replayFolder(t, filepath.Join(m.home, "c")).study.cards[card.ID].suspended {
		t.Fatal("the suspension was not replayed")
	}
	if _, err := m.SuspendCard(ctx, "c", card.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if replayFolder(t, filepath.Join(m.home, "c")).study.cards[card.ID].suspended {
		t.Error("the Card is still suspended after unsuspending")
	}
	if due := m.due(t, DueQuery{}); len(due) != 1 {
		t.Errorf("due after unsuspending = %+v", due)
	}
}

// The daily cap counts the learner's own day, not UTC's.
func TestTheDailyCapFollowsTheLocalDay(t *testing.T) {
	east := time.FixedZone("UTC+10", 10*60*60)
	m := newMachine(t, t.TempDir(), "id", time.Date(2026, 10, 1, 23, 0, 0, 0, east))
	if _, err := m.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	for i := range NewCardsPerDay + 1 {
		m.addCard(t, CardSpec{Prompt: fmt.Sprintf("P%d", i), Answer: "A"})
	}
	for _, card := range m.due(t, DueQuery{Limit: MaxDueLimit}) {
		m.review(t, ReviewSpec{Card: card.ID, Draft: DraftKeep, Rating: RatingGood})
	}
	// 00:30 on 2 October in UTC+10 is still 1 October in UTC: the learner's
	// new day has begun, so the last draft is offered.
	m.setClock(time.Date(2026, 10, 2, 0, 30, 0, 0, east))
	var drafts int
	for _, card := range m.due(t, DueQuery{Limit: MaxDueLimit}) {
		if card.Draft {
			drafts++
		}
	}
	if drafts != 1 {
		t.Errorf("on the learner's new day %d drafts are offered, want 1", drafts)
	}
}

func TestTheSameTextInAnotherLessonIsAnotherCard(t *testing.T) {
	m := newTopic(t)
	withSyllabus(t, m)
	explore := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	lesson := m.addCard(t, CardSpec{Lesson: "answer", Prompt: "P", Answer: "A"})
	if explore.ID == lesson.ID {
		t.Errorf("the same text from a Lesson returned the Explore Card %s", explore.ID)
	}
}

// Listing and offering Cards parse cards.jsonl once and schedule each Card
// once, so a large Topic stays quick.
func TestTwoThousandCardsListQuickly(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	var history, cards strings.Builder
	for i := range 2000 {
		line := cardLine{Format: 1, ID: fmt.Sprintf("explore.c%07d", i), Prompt: fmt.Sprintf("Prompt %d", i), Answer: "A"}
		data, err := json.Marshal(cardAddedData{Card: line})
		if err != nil {
			t.Fatal(err)
		}
		at := t0.Add(time.Duration(i+1) * time.Second).Format(time.RFC3339Nano)
		fmt.Fprintf(&history, `{"format":1,"id":"bulk%04d","time":%q,"wall":%q,"type":"card.added","data":%s}`+"\n", i, at, at, data)
		encoded, _ := encodeCard(line)
		cards.Write(encoded)
		cards.WriteByte('\n')
	}
	appendToHistory(t, dir, history.String())
	if err := os.WriteFile(filepath.Join(dir, cardsFile), []byte(cards.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(time.Hour))
	start := time.Now()
	list, err := m.ListCards(context.Background(), "c", CardQuery{})
	if err != nil || len(list.Cards) != 2000 || list.Cards[1999].Number != 2000 {
		t.Fatalf("ListCards = %d Cards, %v", len(list.Cards), err)
	}
	if _, err := m.DueCardsOf(context.Background(), "c", DueQuery{Limit: MaxDueLimit}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("listing and offering 2000 Cards took %v", took)
	}
}

func BenchmarkListCards(b *testing.B) {
	m := newMachine(&testing.T{}, b.TempDir(), "id", t0)
	ctx := context.Background()
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		b.Fatal(err)
	}
	for i := range 200 {
		if _, err := m.AddCard(ctx, "c", CardSpec{Prompt: fmt.Sprintf("P%d", i), Answer: "A"}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := m.ListCards(ctx, "c", CardQuery{}); err != nil {
			b.Fatal(err)
		}
	}
}
