package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addCardTools adds the tools for Cards and Reviews.
func addCardTools(server *mcp.Server, c *core.Core) {
	closedWorld := false
	notDestructive := false
	destructive := true
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}
	write := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld}
	idempotent := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "due_cards",
		Title: "Cards to review",
		Description: "List the Cards to review now: those due, then drafts waiting for their first Review, as many as " +
			"today's cap on new Cards allows. Without a limit, the list is sized to the Energy: the one given, else the " +
			"open Session's. Suspended Cards are never listed, and a paused Topic lists none (paused is then true). " +
			"Never tell the learner how many Cards are due.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dueCardsInput) (*mcp.CallToolResult, core.DueCards, error) {
		r, err := c.DueCardsOf(ctx, in.Topic, core.DueQuery{Limit: in.Limit, Energy: in.Energy})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "review_record",
		Title: "Record a Review",
		Description: "Record the learner's Review of a Card: again, hard, good or easy, as the learner rates their own " +
			"recall. At a draft's first Review, the learner also keeps, edits (give the new prompt and answer) or drops it. " +
			"Every Review needs a request id of your own, new for each Review: retrying with the same id after an " +
			"error returns the Review already recorded instead of recording it twice.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reviewRecordInput) (*mcp.CallToolResult, core.ReviewResult, error) {
		// A client may retry a call it saw fail, and without a request id
		// a retried Review of an established Card would be recorded twice.
		if strings.TrimSpace(in.Request) == "" {
			return nil, core.ReviewResult{}, &core.Error{Code: core.CodeInvalidArgument,
				Message: "give the Review a request id of your own, so a retry cannot record it twice"}
		}
		r, err := c.RecordReview(ctx, in.Topic, core.ReviewSpec{Card: in.Card, Rating: in.Rating, Draft: in.Draft,
			Prompt: in.Prompt, Answer: in.Answer, Request: in.Request})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "cards",
		Title: "A Topic's Cards",
		Description: "List a Topic's Cards in the order they were written, with their display numbers, whether each " +
			"is a draft, suspended or flagged by the learner, and when it is due. Use it to find a Card to edit; " +
			"for Reviews use due_cards.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardsInput) (*mcp.CallToolResult, core.CardList, error) {
		r, err := c.ListCards(ctx, in.Topic, core.CardQuery{Lesson: in.Lesson})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "card_add",
		Title: "Add a Card",
		Description: "Add a Card: one fact, a prompt without its answer, and the expected answer. Give the Lesson it " +
			"comes from, or no Lesson for an Explore Card from free questions. Cite the Evidence it relies on when there " +
			"is some. Text may span lines, for code. It is a draft until the learner keeps, edits or drops it at its " +
			"first Review. Cards from a Lesson's completion go with lesson_complete instead.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardAddInput) (*mcp.CallToolResult, core.CardChange, error) {
		r, err := c.AddCard(ctx, in.Topic, core.CardSpec{Lesson: in.Lesson, Prompt: in.Prompt, Answer: in.Answer,
			Evidence: in.Evidence})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "card_edit",
		Title: "Edit a Card",
		Description: "Change a Card's prompt, answer or Evidence, keeping its schedule. Evidence, when given, " +
			"replaces the Card's list; an empty list removes it. Editing a Card the learner flagged settles the flag.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardEditInput) (*mcp.CallToolResult, core.CardChange, error) {
		r, err := c.EditCard(ctx, in.Topic, core.CardEdit{Card: in.Card, Prompt: in.Prompt, Answer: in.Answer,
			Evidence: in.Evidence})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "card_suspend",
		Title: "Suspend a Card",
		Description: "Suspend a Card so it is never offered for Review, or set suspended to false to offer it again. " +
			"Its schedule is kept.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardSuspendInput) (*mcp.CallToolResult, core.CardChange, error) {
		suspend := in.Suspended == nil || *in.Suspended
		r, err := c.SuspendCard(ctx, in.Topic, in.Card, suspend, false)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "card_flag",
		Title: "Flag a Card",
		Description: "Flag a Card the learner finds wrong or unclear, with their note, when it cannot be fixed now: it " +
			"shows in status until the Card is edited or deleted, or the flag dismissed. When you can fix it with the " +
			"learner, use card_edit instead.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardFlagInput) (*mcp.CallToolResult, core.CardChange, error) {
		r, err := c.FlagCard(ctx, in.Topic, in.Card, in.Note, false)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "card_delete",
		Title: "Delete a Card",
		Description: "Delete a Card for good, once the learner agrees: it leaves cards.jsonl and is never scheduled " +
			"again. To drop a draft at its first Review, use review_record with draft drop instead.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cardInput) (*mcp.CallToolResult, core.CardChange, error) {
		r, err := c.DeleteCard(ctx, in.Topic, in.Card, false)
		return nil, r, toolErr(err)
	})
}

type dueCardsInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id"`
	Limit  int    `json:"limit,omitempty" jsonschema:"most Cards to return, at most 100; without it the list is sized to the Energy"`
	Energy string `json:"energy,omitempty" jsonschema:"full, half or fumes; defaults to the open Session's Energy"`
}

type reviewRecordInput struct {
	Topic   string `json:"topic" jsonschema:"the Topic's id"`
	Card    string `json:"card" jsonschema:"the Card's id, from due_cards"`
	Rating  string `json:"rating,omitempty" jsonschema:"again, hard, good or easy; not needed to drop a draft"`
	Draft   string `json:"draft,omitempty" jsonschema:"at a draft's first Review: keep, edit or drop"`
	Prompt  string `json:"prompt,omitempty" jsonschema:"the new prompt, when editing a draft"`
	Answer  string `json:"answer,omitempty" jsonschema:"the new answer, when editing a draft"`
	Request string `json:"request" jsonschema:"your own id for this Review, new for each Review and the same on a retry: letters, digits and . _ : -, up to 64 characters"`
}

type cardsInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id"`
	Lesson string `json:"lesson,omitempty" jsonschema:"only the Cards of this Lesson; explore for Explore Cards"`
}

type cardInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id"`
	Card  string `json:"card" jsonschema:"the Card's id, from cards or due_cards"`
}

type cardAddInput struct {
	Topic    string   `json:"topic" jsonschema:"the Topic's id"`
	Lesson   string   `json:"lesson,omitempty" jsonschema:"the Lesson the Card comes from; leave out for an Explore Card"`
	Prompt   string   `json:"prompt" jsonschema:"the question, without its answer"`
	Answer   string   `json:"answer" jsonschema:"the expected answer"`
	Evidence []string `json:"evidence,omitempty" jsonschema:"ids of the Evidence the Card relies on, from evidence"`
}

type cardEditInput struct {
	Topic    string   `json:"topic" jsonschema:"the Topic's id"`
	Card     string   `json:"card" jsonschema:"the Card's id, from cards or due_cards"`
	Prompt   string   `json:"prompt,omitempty" jsonschema:"the new prompt; leave out to keep it"`
	Answer   string   `json:"answer,omitempty" jsonschema:"the new answer; leave out to keep it"`
	Evidence []string `json:"evidence,omitempty" jsonschema:"the Card's Evidence ids; leave out to keep them, [] removes them"`
}

type cardFlagInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id"`
	Card  string `json:"card" jsonschema:"the Card's id, from cards or due_cards"`
	Note  string `json:"note,omitempty" jsonschema:"what the learner found wrong or unclear, in their words"`
}

type cardSuspendInput struct {
	Topic     string `json:"topic" jsonschema:"the Topic's id"`
	Card      string `json:"card" jsonschema:"the Card's id, from cards or due_cards"`
	Suspended *bool  `json:"suspended,omitempty" jsonschema:"false offers the Card for Review again; default true"`
}
