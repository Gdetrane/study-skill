package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addAssessmentTools adds the tools for Assessments, hints and the learning
// signals. The learner sets the Level through topic_update.
func addAssessmentTools(server *mcp.Server, c *core.Core) {
	closedWorld := false
	notDestructive := false
	idempotent := &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "assessment_record",
		Title: "Record an Assessment",
		Description: "Record an Assessment: the placement Assessment when a Topic is created, before drafting its " +
			"Syllabus, or the one that ends a Milestone. Keep it to its time box, about 15 minutes: items time ran " +
			"out before are not_reached, to confirm during Lessons. Save your notes in the Topic's notes/ folder and " +
			"name the file in notes. With level, the Assessment sets the Topic's Level, replacing any the learner " +
			"chose since the last one. Weak results never block anything: propose a Revision for the weak areas, such " +
			"as a review Lesson. Recording the same Assessment again records nothing.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in assessmentRecordInput) (*mcp.CallToolResult, core.AssessmentRecorded, error) {
		r, err := c.RecordAssessment(ctx, in.Topic, core.AssessmentSpec{Kind: in.Kind, Milestone: in.Milestone,
			Items: in.Items, Summary: in.Summary, Minutes: in.Minutes, TimeBox: in.TimeBox, Level: in.Level,
			Notes: in.Notes})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "hint_record",
		Title: "Record a hint",
		Description: "Record every hint you give while the learner studies a Lesson: nudge for a question or a pointer, " +
			"explanation for explaining a concept again, step for showing part of the way to a solution. Hints are a " +
			"signal for adapting how the Topic is taught. Give request an id of your choosing so a retry records nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hintRecordInput) (*mcp.CallToolResult, core.HintRecorded, error) {
		r, err := c.RecordHint(ctx, in.Topic, core.HintSpec{Lesson: in.Lesson, Kind: in.Kind, Note: in.Note,
			Request: in.Request})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "signals",
		Title: "A Topic's learning signals",
		Description: "Read a Topic's learning signals: the Level and where it came from, the Assessments, and for each " +
			"Lesson whether its Check passed on the first try, its Attempts, feedback rounds, hints, the gap between " +
			"its dev and Held-out scores, and Review ratings. Use them to adapt how you teach: the depth of " +
			"explanations, scaffolding, how soon to offer a hint. They are for you: never show the learner these " +
			"counts or scores; if they ask how they are doing, say it in words.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicInput) (*mcp.CallToolResult, core.Signals, error) {
		r, err := c.SignalsOf(ctx, in.Topic)
		return nil, r, toolErr(err)
	})
}

type assessmentRecordInput struct {
	Topic     string                `json:"topic" jsonschema:"the Topic's id, from status"`
	Kind      string                `json:"kind" jsonschema:"placement, when the Topic is created, or milestone, at the end of a Milestone"`
	Milestone string                `json:"milestone,omitempty" jsonschema:"the Milestone a milestone Assessment ends"`
	Items     []core.AssessmentItem `json:"items" jsonschema:"what was asked and how it went, one item per question"`
	Summary   string                `json:"summary" jsonschema:"what the Assessment found, in plain words"`
	Minutes   int                   `json:"minutes,omitempty" jsonschema:"how many minutes it took"`
	TimeBox   int                   `json:"time_box,omitempty" jsonschema:"how many minutes it was meant to take; 15 when left out"`
	Level     string                `json:"level,omitempty" jsonschema:"the Level the results suggest: beginner, intermediate, advanced or expert; it becomes the Topic's Level"`
	Notes     string                `json:"notes,omitempty" jsonschema:"the file in notes/ where you saved the Assessment, such as notes/placement.md"`
}

type hintRecordInput struct {
	Topic   string `json:"topic" jsonschema:"the Topic's id, from status"`
	Lesson  string `json:"lesson" jsonschema:"the Lesson the learner is studying"`
	Kind    string `json:"kind,omitempty" jsonschema:"nudge, explanation or step; nudge when left out"`
	Note    string `json:"note,omitempty" jsonschema:"what the hint was about, in a few words"`
	Request string `json:"request,omitempty" jsonschema:"an id you choose for this hint, so a retry records nothing"`
}
