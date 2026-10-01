package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// twoMachines is a Topic c on machine a, with one Card whose answer was
// edited once, cloned to machine b: the start of the reviewer's merge
// scenarios.
func twoCardMachines(t *testing.T) (a, b *machine, card string) {
	t.Helper()
	gitIdentity(t)
	ctx := context.Background()
	a = newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	card = a.addCard(t, CardSpec{Prompt: "P", Answer: "A"}).ID
	if _, err := a.EditCard(ctx, "c", CardEdit{Card: card, Answer: "B"}); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	b = newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	git(t, b.home, "clone", "-q", filepath.Join(a.home, "c"), "c")
	return a, b, card
}

// pull merges machine from's Topic into machine into's, as git pull does.
func pull(t *testing.T, into, from *machine) {
	t.Helper()
	git(t, filepath.Join(into.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(from.home, "c"), "main")
}

func cardOn(t *testing.T, m *machine, id string) Card {
	t.Helper()
	list, err := m.ListCards(context.Background(), "c", CardQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Cards {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("%s has no Card %s: %+v", m.home, id, list.Cards)
	return Card{}
}

func noFlags(t *testing.T, m *machine) {
	t.Helper()
	topic, err := m.readTopic("c")
	if err != nil || len(topic.Flags) != 0 {
		t.Errorf("%s: flags %+v, %v", m.home, topic.Flags, err)
	}
}

// One machine edits a Card while the other adds one right after it: the
// merge keeps a stale copy of the edited line, which every reader and every
// write must read past (scen1, scen1b, scen1c).
func TestAnEditNextToAnotherMachinesAddSurvivesTheMerge(t *testing.T) {
	for _, then := range []string{"edit", "draft edit", "delete"} {
		t.Run(then, func(t *testing.T) {
			ctx := context.Background()
			a, b, card := twoCardMachines(t)
			if _, err := a.EditCard(ctx, "c", CardEdit{Card: card, Answer: "C"}); err != nil {
				t.Fatal(err)
			}
			takeCheckpoint(t, a)
			b.setClock(t0.Add(2 * time.Minute))
			b.addCard(t, CardSpec{Prompt: "Q", Answer: "Y"})
			takeCheckpoint(t, b)
			pull(t, a, b)
			pull(t, b, a)
			if !strings.Contains(readCardsFile(t, a), `"answer":"B"`) {
				t.Fatalf("the merge left no stale copy, so this test proves nothing:\n%s", readCardsFile(t, a))
			}
			for _, m := range []*machine{a, b} {
				if got := cardOn(t, m, card); got.Answer != "C" {
					t.Errorf("%s reads the edited Card as %+v, want answer C", m.home, got)
				}
				noFlags(t, m)
			}

			a.setClock(t0.Add(time.Hour))
			switch then {
			case "edit":
				res, err := a.EditCard(ctx, "c", CardEdit{Card: card, Prompt: "P2"})
				if err != nil || res.Card.Answer != "C" || res.Card.Prompt != "P2" {
					t.Fatalf("editing the prompt after the merge = %+v, %v; want answer C kept", res, err)
				}
				if got := cardOn(t, a, card); got.Answer != "C" || got.Prompt != "P2" {
					t.Errorf("after the edit: %+v", got)
				}
			case "draft edit":
				a.review(t, ReviewSpec{Card: card, Draft: DraftEdit, Prompt: "P9", Answer: "Z9", Rating: RatingGood})
				if got := cardOn(t, a, card); got.Answer != "Z9" || got.Prompt != "P9" {
					t.Errorf("after the draft edit: %+v", got)
				}
			case "delete":
				if _, err := a.DeleteCard(ctx, "c", card, false); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(readCardsFile(t, a), card) {
					t.Errorf("a copy of the deleted Card survived:\n%s", readCardsFile(t, a))
				}
			}
			if then != "delete" && strings.Count(readCardsFile(t, a), card) != 1 {
				t.Errorf("the write left more than one line for the Card:\n%s", readCardsFile(t, a))
			}
			noFlags(t, a)
		})
	}
}

// A hand edit on one machine wins over the line the other machine left
// unchanged, and survives that merge and the next writes (scen2).
func TestAHandEditSurvivesAnUnrelatedMerge(t *testing.T) {
	ctx := context.Background()
	a, b, card := twoCardMachines(t)
	path := filepath.Join(a.home, "c", cardsFile)
	edited := strings.Replace(readCardsFile(t, a), `"answer":"B"`, `"answer":"B fixed by hand"`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	takeCheckpoint(t, a)
	b.setClock(t0.Add(2 * time.Minute))
	b.addCard(t, CardSpec{Prompt: "Q", Answer: "Y"})
	takeCheckpoint(t, b)
	pull(t, a, b)
	if strings.Count(readCardsFile(t, a), card) < 2 {
		t.Fatalf("the merge left one line for the Card, so this test proves nothing:\n%s", readCardsFile(t, a))
	}
	if got := cardOn(t, a, card); got.Answer != "B fixed by hand" {
		t.Errorf("after the merge the Card reads %+v, want the hand edit", got)
	}
	noFlags(t, a)

	a.setClock(t0.Add(time.Hour))
	a.review(t, ReviewSpec{Card: card, Draft: DraftKeep, Rating: RatingGood})
	if _, err := a.EditCard(ctx, "c", CardEdit{Card: card, Prompt: "P2"}); err != nil {
		t.Fatal(err)
	}
	if got := cardOn(t, a, card); got.Answer != "B fixed by hand" || got.Prompt != "P2" {
		t.Errorf("after the next edit the Card reads %+v, want the hand edit kept", got)
	}
	if strings.Count(readCardsFile(t, a), card) != 1 {
		t.Errorf("the edit left more than one line for the Card:\n%s", readCardsFile(t, a))
	}
	noFlags(t, a)
}
