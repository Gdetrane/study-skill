package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func TestCardTools(t *testing.T) {
	ctx := context.Background()
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C"})

	var added core.CardChange
	decode(t, call(t, session, "card_add", map[string]any{"topic": "c", "prompt": "Which header declares malloc?",
		"answer": "stdlib.h"}), &added)
	if !added.Changed || !strings.HasPrefix(added.Card.ID, "explore.") || !added.Card.Draft {
		t.Fatalf("card_add = %+v", added)
	}
	for i := range 4 {
		call(t, session, "card_add", map[string]any{"topic": "c", "prompt": "Prompt " + string(rune('a'+i)), "answer": "A"})
	}

	var due core.DueCards
	decode(t, call(t, session, "due_cards", map[string]any{"topic": "c", "energy": "fumes"}), &due)
	if len(due.Cards) != 3 || due.Energy != "fumes" {
		t.Errorf("due_cards at fumes = %+v", due)
	}

	var edited core.CardChange
	decode(t, call(t, session, "card_edit", map[string]any{"topic": "c", "card": added.Card.ID, "answer": "<stdlib.h>"}), &edited)
	if !edited.Changed || edited.Card.Answer != "<stdlib.h>" {
		t.Errorf("card_edit = %+v", edited)
	}

	var suspended core.CardChange
	decode(t, call(t, session, "card_suspend", map[string]any{"topic": "c", "card": added.Card.ID}), &suspended)
	if !suspended.Card.Suspended {
		t.Errorf("card_suspend = %+v", suspended)
	}
	var unsuspended core.CardChange
	decode(t, call(t, session, "card_suspend", map[string]any{"topic": "c", "card": added.Card.ID, "suspended": false}), &unsuspended)
	if unsuspended.Card.Suspended || !unsuspended.Changed {
		t.Errorf("card_suspend with suspended false = %+v", unsuspended)
	}

	// A retried Review with the same request records nothing.
	for i, want := range []bool{true, false} {
		var r core.ReviewResult
		decode(t, call(t, session, "review_record", map[string]any{"topic": "c", "card": added.Card.ID, "draft": "keep",
			"rating": "good", "request": "agent-7"}), &r)
		if r.Changed != want || r.Card.Draft {
			t.Errorf("review_record call %d = %+v, want changed %v", i+1, r, want)
		}
	}

	var deleted core.CardChange
	decode(t, call(t, session, "card_delete", map[string]any{"topic": "c", "card": added.Card.ID}), &deleted)
	if !deleted.Changed {
		t.Errorf("card_delete = %+v", deleted)
	}
	var list core.CardList
	decode(t, call(t, session, "cards", map[string]any{"topic": "c"}), &list)
	if len(list.Cards) != 4 || list.Cards[0].Number != 1 {
		t.Errorf("cards after the delete = %+v", list.Cards)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "card_edit",
		Arguments: map[string]any{"topic": "c", "card": added.Card.ID, "prompt": "P"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "not_found") {
		t.Errorf("editing a deleted Card: IsError=%v, %q", res.IsError, text(res))
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "due_cards",
		Arguments: map[string]any{"topic": "c", "energy": "sleepy"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "invalid_argument") {
		t.Errorf("an unknown Energy: IsError=%v, %q", res.IsError, text(res))
	}
}

// TestReviewsNeedARequestID: over MCP a client may retry a call it saw fail,
// so a Review without a request id is refused before anything is recorded.
func TestReviewsNeedARequestID(t *testing.T) {
	ctx := context.Background()
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C"})
	var added core.CardChange
	decode(t, call(t, session, "card_add", map[string]any{"topic": "c", "prompt": "P", "answer": "A"}), &added)
	for name, args := range map[string]map[string]any{
		"no request":    {"topic": "c", "card": added.Card.ID, "rating": "good", "draft": "keep"},
		"empty request": {"topic": "c", "card": added.Card.ID, "rating": "good", "draft": "keep", "request": " "},
	} {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "review_record", Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("%s: review_record succeeded: %+v", name, res.StructuredContent)
		}
	}
	var list core.CardList
	decode(t, call(t, session, "cards", map[string]any{"topic": "c"}), &list)
	if len(list.Cards) != 1 || !list.Cards[0].Draft {
		t.Errorf("a refused Review was recorded: %+v", list.Cards)
	}
}

// TestCardFlag: a Card the learner finds wrong can be flagged over MCP, and
// shows as flagged until it is edited.
func TestCardFlag(t *testing.T) {
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C"})
	var added core.CardChange
	decode(t, call(t, session, "card_add", map[string]any{"topic": "c", "prompt": "P", "answer": "A"}), &added)
	for i, want := range []bool{true, false} {
		var flagged core.CardChange
		decode(t, call(t, session, "card_flag", map[string]any{"topic": "c", "card": added.Card.ID, "note": "the answer is wrong"}), &flagged)
		if flagged.Changed != want || !flagged.Card.Flagged {
			t.Errorf("card_flag call %d = %+v, want changed %v", i+1, flagged, want)
		}
	}
	var edited core.CardChange
	decode(t, call(t, session, "card_edit", map[string]any{"topic": "c", "card": added.Card.ID, "answer": "B"}), &edited)
	if edited.Card.Flagged {
		t.Errorf("the flag outlived an edit: %+v", edited)
	}
}
