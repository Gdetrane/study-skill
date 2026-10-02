package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func (m *machine) addCard(t *testing.T, spec CardSpec) Card {
	t.Helper()
	res, err := m.AddCard(context.Background(), "c", spec)
	if err != nil {
		t.Fatalf("AddCard(%+v): %v", spec, err)
	}
	return res.Card
}

func (m *machine) due(t *testing.T, q DueQuery) []Card {
	t.Helper()
	res, err := m.DueCardsOf(context.Background(), "c", q)
	if err != nil {
		t.Fatalf("DueCardsOf: %v", err)
	}
	return res.Cards
}

func (m *machine) review(t *testing.T, spec ReviewSpec) ReviewResult {
	t.Helper()
	res, err := m.RecordReview(context.Background(), "c", spec)
	if err != nil {
		t.Fatalf("RecordReview(%+v): %v", spec, err)
	}
	return res
}

func readCardsFile(t *testing.T, m *machine) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.home, "c", cardsFile))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func ids(cards []Card) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.ID
	}
	return out
}

func TestCardsCanBeAddedEditedSuspendedAndDeleted(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)

	if _, err := m.AddCard(ctx, "c", CardSpec{Lesson: "answer", Prompt: "P", Answer: "A"}); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("a Lesson's Card without a Syllabus: err = %v, want failed_precondition", err)
	}
	explore := m.addCard(t, CardSpec{Prompt: "Which header declares malloc?", Answer: "stdlib.h"})
	if !strings.HasPrefix(explore.ID, "explore.") || explore.Lesson != "" || !explore.Draft || explore.Number != 1 {
		t.Errorf("an Explore Card = %+v", explore)
	}
	withSyllabus(t, m)
	if _, err := m.AddCard(ctx, "c", CardSpec{Lesson: "nope", Prompt: "P", Answer: "A"}); CodeOf(err) != CodeNotFound {
		t.Errorf("a Card for a Lesson not in the Syllabus: err = %v, want not_found", err)
	}
	lesson := m.addCard(t, CardSpec{Lesson: "answer", Prompt: "What is the answer?", Answer: "42"})
	if !strings.HasPrefix(lesson.ID, "answer.") || lesson.Lesson != "answer" || lesson.Number != 2 {
		t.Errorf("a Lesson's Card = %+v", lesson)
	}
	again, err := m.AddCard(ctx, "c", CardSpec{Lesson: "answer", Prompt: "What is the answer?", Answer: "42"})
	if err != nil || again.Changed || again.Card.ID != lesson.ID {
		t.Errorf("adding the same Card again = %+v, %v; want the first Card, unchanged", again, err)
	}
	for _, bad := range []CardSpec{{Prompt: "", Answer: "A"}, {Prompt: "P", Answer: " "}, {Prompt: "P\x1b[2J", Answer: "A"}} {
		if _, err := m.AddCard(ctx, "c", bad); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("AddCard(%+v): err = %v, want invalid_argument", bad, err)
		}
	}

	// A field this version does not know survives an edit.
	path := filepath.Join(m.home, "c", cardsFile)
	data := strings.Replace(readCardsFile(t, m), `"answer":"42"`, `"answer":"42","hint":{"source":"kr"}`, 1)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	edited, err := m.EditCard(ctx, "c", CardEdit{Card: lesson.ID, Answer: "Forty-two"})
	if err != nil || !edited.Changed || edited.Card.Answer != "Forty-two" || edited.Card.Prompt != "What is the answer?" {
		t.Fatalf("EditCard = %+v, %v", edited, err)
	}
	if got := readCardsFile(t, m); !strings.Contains(got, `"hint":{"source":"kr"}`) || !strings.Contains(got, "Forty-two") {
		t.Errorf("after the edit, %s =\n%s", cardsFile, got)
	}
	if same, err := m.EditCard(ctx, "c", CardEdit{Card: lesson.ID, Answer: "Forty-two"}); err != nil || same.Changed {
		t.Errorf("editing to the same content = %+v, %v; want no change", same, err)
	}
	if _, err := m.EditCard(ctx, "c", CardEdit{Card: lesson.ID}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an edit with nothing to change: err = %v", err)
	}

	sus, err := m.SuspendCard(ctx, "c", explore.ID, true, false)
	if err != nil || !sus.Changed || !sus.Card.Suspended {
		t.Fatalf("SuspendCard = %+v, %v", sus, err)
	}
	if got := ids(m.due(t, DueQuery{})); !slices.Equal(got, []string{lesson.ID}) {
		t.Errorf("due with a suspended Card = %v, want only %s", got, lesson.ID)
	}
	if res, _ := m.SuspendCard(ctx, "c", explore.ID, true, false); res.Changed {
		t.Error("suspending a suspended Card recorded a change")
	}
	if res, err := m.SuspendCard(ctx, "c", explore.ID, false, false); err != nil || !res.Changed || res.Card.Suspended {
		t.Errorf("unsuspending = %+v, %v", res, err)
	}

	del, err := m.DeleteCard(ctx, "c", explore.ID, false)
	if err != nil || !del.Changed || del.Card.ID != explore.ID {
		t.Fatalf("DeleteCard = %+v, %v", del, err)
	}
	if strings.Contains(readCardsFile(t, m), explore.ID) {
		t.Errorf("the deleted Card is still in %s", cardsFile)
	}
	if res, err := m.DeleteCard(ctx, "c", explore.ID, false); err != nil || res.Changed {
		t.Errorf("deleting it again = %+v, %v; want no change", res, err)
	}
	for _, err := range []error{
		func() error { _, err := m.EditCard(ctx, "c", CardEdit{Card: explore.ID, Prompt: "P"}); return err }(),
		func() error {
			_, err := m.RecordReview(ctx, "c", ReviewSpec{Card: explore.ID, Rating: RatingGood})
			return err
		}(),
		func() error { _, err := m.DeleteCard(ctx, "c", "explore.nope", false); return err }(),
	} {
		if CodeOf(err) != CodeNotFound {
			t.Errorf("an operation on a deleted or unknown Card: err = %v, want not_found", err)
		}
	}
	list, err := m.ListCards(ctx, "c", CardQuery{})
	if err != nil || !slices.Equal(ids(list.Cards), []string{lesson.ID}) || list.Cards[0].Number != 1 {
		t.Errorf("ListCards = %+v, %v; want the Lesson's Card, now Card 1", list, err)
	}
	if list, _ := m.ListCards(ctx, "c", CardQuery{Lesson: "explore"}); len(list.Cards) != 0 {
		t.Errorf("Explore Cards after the delete = %+v", list.Cards)
	}
}

// A union merge can leave a stale copy of a Card's line beside the version
// the History recorded; that copy is read past, while two lines that are
// both unknown to the History are a conflict.
func TestAStaleCardLineIsReadPast(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "Old prompt", Answer: "A"})
	stale := strings.TrimSpace(readCardsFile(t, m))
	if _, err := m.EditCard(ctx, "c", CardEdit{Card: card.ID, Prompt: "New prompt"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.home, "c", cardsFile)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The stale line comes first, as a merge can leave it.
	write(stale + "\n" + readCardsFile(t, m))
	topic, err := m.readTopic("c")
	if err != nil || len(topic.Flags) != 0 {
		t.Errorf("with a stale copy: flags %+v, %v", topic.Flags, err)
	}
	if list, _ := m.ListCards(ctx, "c", CardQuery{}); len(list.Cards) != 1 || list.Cards[0].Prompt != "New prompt" {
		t.Errorf("with a stale copy: %+v, want the edited Card", list.Cards)
	}
	if _, err := m.EditCard(ctx, "c", CardEdit{Card: card.ID, Answer: "B"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readCardsFile(t, m), "\n"); n != 1 {
		t.Errorf("the next write left %d lines:\n%s", n, readCardsFile(t, m))
	}

	// Two lines the History knows neither of are flagged.
	write(strings.Replace(readCardsFile(t, m), `"answer":"B"`, `"answer":"hand 1"`, 1) +
		strings.Replace(readCardsFile(t, m), `"answer":"B"`, `"answer":"hand 2"`, 1))
	topic, err = m.readTopic("c")
	if err != nil || len(topic.Flags) != 1 || topic.Flags[0].Kind != FlagConflict {
		t.Errorf("two unknown lines: flags %+v, %v; want one conflict", topic.Flags, err)
	}
}

func TestCardTextMaySpanLines(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "What does this return?\r\nfunc f() int {\r\n\treturn 1\r\n}", Answer: "1"})
	if card.Prompt != "What does this return?\nfunc f() int {\n\treturn 1\n}" {
		t.Errorf("prompt = %q, want lines ending in \\n and the tab kept", card.Prompt)
	}
	if got := m.due(t, DueQuery{}); len(got) != 1 || got[0].Prompt != card.Prompt {
		t.Errorf("read back as %+v", got)
	}
	for _, bad := range []string{"bell\a", "escape \x1b[2J", "reversed \u202e text"} {
		if _, err := m.AddCard(ctx, "c", CardSpec{Prompt: bad, Answer: "A"}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("prompt %q: err = %v, want invalid_argument", bad, err)
		}
		if _, err := m.EditCard(ctx, "c", CardEdit{Card: card.ID, Answer: bad}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("answer %q: err = %v, want invalid_argument", bad, err)
		}
	}
}

// Lamplight works in sessions, so FSRS's short-term steps, which bring a
// Card back within minutes, are off: every Review schedules in days.
func TestReviewsScheduleInDays(t *testing.T) {
	m := newTopic(t)
	for _, rating := range []string{RatingAgain, RatingHard, RatingGood, RatingEasy} {
		card := m.addCard(t, CardSpec{Prompt: "Rated " + rating, Answer: "A"})
		res := m.review(t, ReviewSpec{Card: card.ID, Draft: DraftKeep, Rating: rating})
		if res.Card.Due.Sub(t0) < 24*time.Hour {
			t.Errorf("a first Review rated %s is due %v, less than a day later", rating, res.Card.Due)
		}
	}
}

func TestTheDailyCapLimitsNewCards(t *testing.T) {
	m := newTopic(t)
	for i := range NewCardsPerDay + 3 {
		m.addCard(t, CardSpec{Prompt: fmt.Sprintf("Prompt %d", i), Answer: "A"})
	}
	drafts := m.due(t, DueQuery{Limit: MaxDueLimit})
	if len(drafts) != NewCardsPerDay {
		t.Fatalf("drafts offered on day 1 = %d, want the cap of %d", len(drafts), NewCardsPerDay)
	}
	for i, card := range drafts[:4] {
		m.setClock(t0.Add(time.Duration(i) * time.Minute))
		m.review(t, ReviewSpec{Card: card.ID, Rating: RatingGood, Draft: DraftKeep})
	}
	m.review(t, ReviewSpec{Card: drafts[4].ID, Draft: DraftDrop})
	if left := m.due(t, DueQuery{Limit: MaxDueLimit}); len(left) != NewCardsPerDay-5 {
		t.Errorf("drafts left on day 1 after deciding 5 = %d, want %d", len(left), NewCardsPerDay-5)
	}
	for _, card := range m.due(t, DueQuery{Limit: MaxDueLimit}) {
		m.review(t, ReviewSpec{Card: card.ID, Rating: RatingGood, Draft: DraftKeep})
	}
	if left := m.due(t, DueQuery{Limit: MaxDueLimit}); len(left) != 0 {
		t.Errorf("after the cap, day 1 still offers %v", ids(left))
	}

	// The next day the remaining drafts come, after the Cards that are due.
	m.setClock(t0.AddDate(0, 0, 1).Add(time.Hour))
	next := m.due(t, DueQuery{Limit: MaxDueLimit})
	var newOnes int
	for _, card := range next {
		if card.Draft {
			newOnes++
		}
	}
	if newOnes != 3 {
		t.Errorf("day 2 offers %d drafts, want the 3 left", newOnes)
	}
}

func TestDueCardsAreSizedToEnergy(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	withSyllabus(t, m)
	for i := range 8 {
		m.addCard(t, CardSpec{Prompt: fmt.Sprintf("Prompt %d", i), Answer: "A"})
	}
	if got := m.due(t, DueQuery{Energy: EnergyFumes}); len(got) != 3 {
		t.Errorf("at fumes: %d Cards, want 3", len(got))
	}
	if got := m.due(t, DueQuery{}); len(got) != 8 {
		t.Errorf("with no Energy: %d Cards, want all 8 (the default is %d)", len(got), DefaultDueLimit)
	}
	if _, err := m.OpenSession(ctx, "c", SessionSpec{Energy: EnergyFumes}); err != nil {
		t.Fatal(err)
	}
	res, err := m.DueCardsOf(ctx, "c", DueQuery{})
	if err != nil || len(res.Cards) != 3 || res.Energy != EnergyFumes {
		t.Errorf("in a Session at fumes = %+v, %v; want 3 Cards sized to fumes", res, err)
	}
	if got := m.due(t, DueQuery{Limit: 5}); len(got) != 5 {
		t.Errorf("an explicit limit gives %d Cards, want 5", len(got))
	}
	for _, q := range []DueQuery{{Limit: -1}, {Energy: "sleepy"}} {
		if _, err := m.DueCardsOf(ctx, "c", q); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("DueCardsOf(%+v): err = %v, want invalid_argument", q, err)
		}
	}
}

func TestAFlaggedCardShowsInStatusUntilFixed(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	card := m.addCard(t, CardSpec{Prompt: "P", Answer: "A"})
	res, err := m.FlagCard(ctx, "c", card.ID, "the answer is ambiguous", false)
	if err != nil || !res.Changed || !res.Card.Flagged {
		t.Fatalf("FlagCard = %+v, %v", res, err)
	}
	flagged := func() []Flag {
		t.Helper()
		topic, err := m.readTopic("c")
		if err != nil {
			t.Fatal(err)
		}
		var out []Flag
		for _, f := range topic.Flags {
			if f.Kind == FlagCardFlagged {
				out = append(out, f)
			}
		}
		return out
	}
	if f := flagged(); len(f) != 1 || f[0].Item != cardItem(card.ID) || !strings.Contains(f[0].Message, "ambiguous") {
		t.Fatalf("flags = %+v", f)
	}
	if again, _ := m.FlagCard(ctx, "c", card.ID, "the answer is ambiguous", false); again.Changed {
		t.Error("flagging again with the same note recorded a change")
	}
	if _, err := m.EditCard(ctx, "c", CardEdit{Card: card.ID, Answer: "B"}); err != nil {
		t.Fatal(err)
	}
	if f := flagged(); len(f) != 0 {
		t.Errorf("after the edit, flags = %+v", f)
	}

	// A flag can also be dismissed, and deleting the Card settles it.
	if _, err := m.FlagCard(ctx, "c", card.ID, "", false); err != nil {
		t.Fatal(err)
	}
	f := flagged()
	if len(f) != 1 {
		t.Fatalf("flags = %+v", f)
	}
	if _, err := m.DismissFlag(ctx, "c", f[0].ID, false); err != nil {
		t.Fatalf("DismissFlag: %v", err)
	}
	if f := flagged(); len(f) != 0 {
		t.Errorf("after dismissing, flags = %+v", f)
	}
	if _, err := m.FlagCard(ctx, "c", card.ID, "still wrong", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteCard(ctx, "c", card.ID, false); err != nil {
		t.Fatal(err)
	}
	if f := flagged(); len(f) != 0 {
		t.Errorf("after the delete, flags = %+v", f)
	}
}

func TestCardChangesFromTwoMachinesAreFlagged(t *testing.T) {
	added := cardLine{ID: "explore.aaaa", Prompt: "P?", Answer: "a"}
	for _, tc := range []struct {
		name   string
		second func(h *history)
	}{
		{"deleted and reviewed", func(h *history) {
			h.add("b1", eventReviewRecorded, reviewRecordedData{Card: "explore.aaaa", Rating: RatingGood, Draft: DraftKeep})
		}},
		{"deleted and edited", func(h *history) {
			h.add("b1", eventCardEdited, cardEditedData{Card: "explore.aaaa", Prompt: "Q?"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &history{t: t}
			h.add("a1", eventCardAdded, cardAddedData{Card: added})
			h.add("a2", eventCardDeleted, cardRefData{Card: "explore.aaaa"})
			tc.second(h)
			s := replay(h.events)
			if c := conflicts(s); len(c) != 1 || c[0].Item != cardItem("explore.aaaa") {
				t.Errorf("conflicts = %+v", c)
			}
			if cs := s.study.cards["explore.aaaa"]; !cs.deleted || len(cs.reviews) != 0 {
				t.Errorf("Card = %+v, want it still deleted and unreviewed", cs)
			}
		})
	}

	// A change to a Card whose creation hasn't arrived waits for it.
	h := &history{t: t}
	h.add("b1", eventCardSuspended, cardRefData{Card: "explore.aaaa"})
	s := replay(h.events)
	if len(s.flags) != 1 || s.flags[0].Kind != FlagHeldEvent {
		t.Errorf("a suspension before its Card: flags = %+v", s.flags)
	}
	h.add("a1", eventCardAdded, cardAddedData{Card: added})
	slices.SortFunc(h.events, func(x, y event) int { return strings.Compare(x.ID, y.ID) })
	if s := replay(h.events); len(s.flags) != 0 || !s.study.cards["explore.aaaa"].suspended {
		t.Errorf("once the Card arrives: flags = %+v, suspended = %v", s.flags, s.study.cards["explore.aaaa"].suspended)
	}
}

func TestCrashesInCardWrites(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		setup func(t *testing.T, m *machine) (card string)
		run   func(m *machine, card string, dryRun bool) (changed bool, err error)
		check func(t *testing.T, m *machine, card string)
	}
	add := func(t *testing.T, m *machine) string { return m.addCard(t, CardSpec{Prompt: "P", Answer: "A"}).ID }
	ops := []op{
		{"card.added", func(*testing.T, *machine) string { return "" },
			func(m *machine, _ string, dry bool) (bool, error) {
				res, err := m.AddCard(ctx, "c", CardSpec{Prompt: "P", Answer: "A", DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine, _ string) {
				if n := strings.Count(readCardsFile(t, m), "\n"); n != 1 || len(m.due(t, DueQuery{})) != 1 {
					t.Errorf("%s =\n%s", cardsFile, readCardsFile(t, m))
				}
			}},
		{"card.edited", add,
			func(m *machine, card string, dry bool) (bool, error) {
				res, err := m.EditCard(ctx, "c", CardEdit{Card: card, Prompt: "New", DryRun: dry})
				return res.Changed, err
			},
			func(t *testing.T, m *machine, _ string) {
				if got := readCardsFile(t, m); !strings.Contains(got, `"prompt":"New"`) {
					t.Errorf("%s =\n%s", cardsFile, got)
				}
			}},
		{"card.deleted", add,
			func(m *machine, card string, dry bool) (bool, error) {
				res, err := m.DeleteCard(ctx, "c", card, dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine, _ string) {
				if got := readCardsFile(t, m); got != "" || len(m.due(t, DueQuery{})) != 0 {
					t.Errorf("%s =\n%s", cardsFile, got)
				}
			}},
		{"card.suspended", add,
			func(m *machine, card string, dry bool) (bool, error) {
				res, err := m.SuspendCard(ctx, "c", card, true, dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine, _ string) {
				if len(m.due(t, DueQuery{})) != 0 {
					t.Error("the suspended Card is still offered")
				}
			}},
		{"card.flagged", add,
			func(m *machine, card string, dry bool) (bool, error) {
				res, err := m.FlagCard(ctx, "c", card, "unclear", dry)
				return res.Changed, err
			},
			func(t *testing.T, m *machine, _ string) {
				if cards := m.due(t, DueQuery{}); len(cards) != 1 || !cards[0].Flagged {
					t.Errorf("due = %+v", cards)
				}
			}},
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				card := o.setup(t, m)
				m.crash = crashOnce(point)
				_, err := o.run(m, card, false)
				m.crash = nil
				if err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				dryChanged, dryErr := o.run(m, card, true)
				realChanged, realErr := o.run(m, card, false)
				if CodeOf(dryErr) != CodeOf(realErr) || (dryErr == nil) != (realErr == nil) || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				if realErr != nil {
					t.Errorf("the repeated write failed: %v", realErr)
				}
				if hasIntentFile(t, m) {
					t.Error("the intent marker survived the next write")
				}
				if s := replayFolder(t, filepath.Join(m.home, "c")); len(s.flags) != 0 {
					t.Errorf("flags = %+v", s.flags)
				}
				o.check(t, m, card)
			})
		}
	}
}

func TestCardsSyncThroughGit(t *testing.T) {
	gitIdentity(t)
	ctx := context.Background()
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(a.home, "c")
	shared := a.addCard(t, CardSpec{Prompt: "Shared", Answer: "S"})
	takeCheckpoint(t, a)

	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", dirA, "c")
	dirB := filepath.Join(b.home, "c")

	// Each machine adds Cards and reviews the shared one, before syncing.
	for i := range 3 {
		a.addCard(t, CardSpec{Prompt: fmt.Sprintf("From A %d", i), Answer: "A"})
		b.addCard(t, CardSpec{Prompt: fmt.Sprintf("From B %d", i), Answer: "B"})
	}
	a.review(t, ReviewSpec{Card: shared.ID, Rating: RatingGood, Draft: DraftKeep})
	takeCheckpoint(t, a)
	takeCheckpoint(t, b)
	git(t, dirB, "pull", "-q", "--no-rebase", "--no-edit", dirA, "main")
	git(t, dirA, "pull", "-q", "--no-rebase", "--no-edit", dirB, "main")

	settled := func(want int) {
		t.Helper()
		for _, m := range []*machine{a, b} {
			list, err := m.ListCards(ctx, "c", CardQuery{})
			if err != nil || len(list.Cards) != want {
				t.Errorf("after the merge, %s has %+v, %v; want %d Cards", m.home, list.Cards, err, want)
			}
			if topic, err := m.readTopic("c"); err != nil || len(topic.Flags) != 0 {
				t.Errorf("after the merge: flags %+v, %v", topic.Flags, err)
			}
		}
	}
	settled(7)
	// Both machines replay the same schedule.
	sa := replayFolder(t, dirA).study.cards[shared.ID].due()
	sb := replayFolder(t, dirB).study.cards[shared.ID].due()
	if sa.IsZero() || !sa.Equal(sb) {
		t.Errorf("the shared Card is due %v on A and %v on B", sa, sb)
	}

	// One machine edits a Card while the other adds Cards next to it: the
	// merge can keep a stale copy of the edited line, which is read past.
	if _, err := a.EditCard(ctx, "c", CardEdit{Card: shared.ID, Prompt: "Shared, edited on A"}); err != nil {
		t.Fatal(err)
	}
	b.addCard(t, CardSpec{Prompt: "From B 3", Answer: "B"})
	takeCheckpoint(t, a)
	takeCheckpoint(t, b)
	git(t, dirB, "pull", "-q", "--no-rebase", "--no-edit", dirA, "main")
	git(t, dirA, "pull", "-q", "--no-rebase", "--no-edit", dirB, "main")
	settled(8)
	for _, m := range []*machine{a, b} {
		list, _ := m.ListCards(ctx, "c", CardQuery{})
		if len(list.Cards) == 0 || list.Cards[0].Prompt != "Shared, edited on A" {
			t.Errorf("%s reads the shared Card as %+v", m.home, list.Cards)
		}
	}

	// Editing one Card on both machines from the same version is flagged.
	a.setClock(t0.Add(time.Hour))
	b.setClock(t0.Add(time.Hour + time.Minute))
	if _, err := a.EditCard(ctx, "c", CardEdit{Card: shared.ID, Answer: "edited on A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EditCard(ctx, "c", CardEdit{Card: shared.ID, Answer: "edited on B"}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	takeCheckpoint(t, b)
	git(t, dirB, "pull", "-q", "--no-rebase", "--no-edit", dirA, "main")
	topic, err := b.readTopic("c")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range topic.Flags {
		found = found || (f.Kind == FlagConflict && f.Item == cardItem(shared.ID))
	}
	if !found {
		t.Errorf("two edits of one Card: flags = %+v, want a conflict on %s", topic.Flags, shared.ID)
	}
}
