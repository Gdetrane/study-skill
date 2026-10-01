package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addLearnerLoop adds the tools of the learner loop: the Syllabus and its
// Revisions, Sessions, Phases, Check results, completing a Lesson, and
// Reviews. Checks themselves run only through the CLI (ADR-0009).
func addLearnerLoop(server *mcp.Server, c *core.Core) {
	closedWorld := false
	notDestructive := false
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}
	write := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld}
	idempotent := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "syllabus",
		Title: "The Syllabus",
		Description: "Show a Topic's Syllabus: its Milestones and Lessons in order, each Lesson's status and Phase, " +
			"and any proposed Revision waiting for the learner.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicInput) (*mcp.CallToolResult, core.SyllabusView, error) {
		v, err := c.SyllabusOf(ctx, in.Topic)
		return nil, v, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "revision_propose",
		Title: "Propose a Revision",
		Description: "Propose a change to a Topic's Syllabus, the first Syllabus included: give the whole Syllabus as it " +
			"would be afterwards, and a summary in plain words. Nothing changes until the learner approves it and you " +
			"call revision_apply. Show the learner the change, naming Lessons by title.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in revisionProposeInput) (*mcp.CallToolResult, core.RevisionProposal, error) {
		p, err := c.ProposeRevision(ctx, in.Topic, core.RevisionSpec{Summary: in.Summary, Syllabus: in.Syllabus})
		return nil, p, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "revision_apply",
		Title: "Apply an approved Revision",
		Description: "Apply a proposed Revision once the learner has approved it. Quote the learner's approval in their " +
			"own words in learner_said; never apply a Revision the learner has not approved.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in revisionApplyInput) (*mcp.CallToolResult, core.RevisionApplied, error) {
		r, err := c.ApplyRevision(ctx, in.Topic, in.Revision, core.Approval{Via: "chat", LearnerSaid: in.LearnerSaid}, false)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "session_open",
		Title: "Open a Session",
		Description: "Open a Session on a Topic after the Energy check; it also makes the Topic the most recent one. " +
			"Show the learner the resume point and its Next step word for word. If unclosed is set, the last Session " +
			"ended without a Next step: ask the learner for the missing note.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionOpenInput) (*mcp.CallToolResult, core.SessionOpened, error) {
		r, err := c.OpenSession(ctx, in.Topic, core.SessionSpec{Energy: in.Energy, Focus: in.Focus})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "session_close",
		Title: "Close the Session",
		Description: "Close the open Session with a Next step that starts with a verb (\"Fix the off-by-one in " +
			"parse.go\") and the context needed to take it. The learner sees it first when they come back.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionCloseInput) (*mcp.CallToolResult, core.SessionClosed, error) {
		r, err := c.CloseSession(ctx, in.Topic, in.NextStep, in.Context, false)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "phase_set",
		Title: "Move a Lesson to a Phase",
		Description: "Move a Lesson to teaching, practicing or feedback. Practicing needs the Lesson's Check, which you " +
			"show the learner first. When the turn passes between you and the learner, a Checkpoint is taken. After a " +
			"failed Attempt, go back to practicing with a next_step that names the fix.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in phaseSetInput) (*mcp.CallToolResult, core.PhaseResult, error) {
		r, err := c.SetPhase(ctx, in.Topic, core.PhaseSpec{Lesson: in.Lesson, Phase: in.Phase, NextStep: in.NextStep})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "check_results",
		Title: "Check results",
		Description: "Show a Lesson's Check, its Attempts and whether the Lesson can be completed now. Attempts are " +
			"recorded by running study check in your shell.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lessonInput) (*mcp.CallToolResult, core.CheckResults, error) {
		r, err := c.CheckResultsOf(ctx, in.Topic, in.Lesson)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "lesson_complete",
		Title: "Complete a Lesson",
		Description: "Complete a Lesson once its Check passed on the current work: marks it done, saves its draft Cards " +
			"and takes a Checkpoint. Write Cards that state one fact each, with no answer in the prompt, including " +
			"at least one from the learner's own mistakes. Calling it again changes nothing, so after an error, call it " +
			"again.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lessonCompleteInput) (*mcp.CallToolResult, core.LessonCompletion, error) {
		r, err := c.CompleteLesson(ctx, in.Topic, core.CompleteSpec{Lesson: in.Lesson, Cards: in.Cards})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "due_cards",
		Title: "Cards to review",
		Description: "List Cards to review now: those due, then drafts waiting for their first Review. Never tell the " +
			"learner how many are due.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dueCardsInput) (*mcp.CallToolResult, core.DueCards, error) {
		r, err := c.DueCardsOf(ctx, in.Topic, in.Limit)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "review_record",
		Title: "Record a Review",
		Description: "Record the learner's Review of a Card: again, hard, good or easy. At a draft's first Review, " +
			"the learner also keeps, edits (give the new prompt and answer) or drops it.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in reviewRecordInput) (*mcp.CallToolResult, core.ReviewResult, error) {
		r, err := c.RecordReview(ctx, in.Topic, core.ReviewSpec{Card: in.Card, Rating: in.Rating, Draft: in.Draft,
			Prompt: in.Prompt, Answer: in.Answer})
		return nil, r, toolErr(err)
	})
}

// toolErr is toolError for a possibly nil error.
func toolErr(err error) error {
	if err == nil {
		return nil
	}
	return toolError(err)
}

type topicInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id, from status"`
}

type lessonInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id, from status"`
	Lesson string `json:"lesson" jsonschema:"the Lesson's id, from the Syllabus"`
}

type revisionProposeInput struct {
	Topic    string        `json:"topic" jsonschema:"the Topic's id"`
	Summary  string        `json:"summary" jsonschema:"what changes and why, in plain words"`
	Syllabus core.Syllabus `json:"syllabus" jsonschema:"the whole Syllabus as it would be after the Revision"`
}

type revisionApplyInput struct {
	Topic       string `json:"topic" jsonschema:"the Topic's id"`
	Revision    string `json:"revision" jsonschema:"the Revision's id, from revision_propose"`
	LearnerSaid string `json:"learner_said" jsonschema:"the learner's approval, quoted in their own words"`
}

type sessionOpenInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id"`
	Energy string `json:"energy,omitempty" jsonschema:"the learner's Energy: full, half or fumes"`
	Focus  string `json:"focus,omitempty" jsonschema:"what the Session is for: learn, practice, reviews or explore"`
}

type sessionCloseInput struct {
	Topic    string `json:"topic" jsonschema:"the Topic's id"`
	NextStep string `json:"next_step" jsonschema:"the concrete next action, starting with a verb"`
	Context  string `json:"context,omitempty" jsonschema:"what the learner needs to know to take the Next step"`
}

type phaseSetInput struct {
	Topic    string `json:"topic" jsonschema:"the Topic's id"`
	Lesson   string `json:"lesson" jsonschema:"the Lesson's id"`
	Phase    string `json:"phase" jsonschema:"teaching, practicing or feedback"`
	NextStep string `json:"next_step,omitempty" jsonschema:"a Next step for the new Phase, such as the fix a failed Attempt calls for"`
}

type lessonCompleteInput struct {
	Topic  string           `json:"topic" jsonschema:"the Topic's id"`
	Lesson string           `json:"lesson" jsonschema:"the Lesson's id"`
	Cards  []core.CardDraft `json:"cards,omitempty" jsonschema:"draft Cards written from the Lesson"`
}

type dueCardsInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id"`
	Limit int    `json:"limit,omitempty" jsonschema:"most Cards to return; default 10, at most 100"`
}

type reviewRecordInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id"`
	Card   string `json:"card" jsonschema:"the Card's id, from due_cards"`
	Rating string `json:"rating,omitempty" jsonschema:"again, hard, good or easy; not needed to drop a draft"`
	Draft  string `json:"draft,omitempty" jsonschema:"at a draft's first Review: keep, edit or drop"`
	Prompt string `json:"prompt,omitempty" jsonschema:"the new prompt, when editing a draft"`
	Answer string `json:"answer,omitempty" jsonschema:"the new answer, when editing a draft"`
}
