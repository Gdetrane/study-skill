package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addReadTools adds the read-only lesson and history tools.
func addReadTools(server *mcp.Server, c *core.Core) {
	closedWorld := false
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}

	mcp.AddTool(server, &mcp.Tool{
		Name:  "lesson",
		Title: "A Lesson",
		Description: "Show one Lesson: its number, Milestone, status and Phase, its file, and what its YAML header " +
			"declares: the Check's criteria with their commands and the Break points. check_shown says whether the " +
			"Check is the one shown to the learner when practicing started, the one completion counts; when it is " +
			"false while practicing, show the learner the Check again with phase_set practicing. header_error says " +
			"what to fix when the header cannot be read. For Attempts and grades, use check_results.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lessonInput) (*mcp.CallToolResult, core.LessonDetail, error) {
		d, err := c.LessonOf(ctx, in.Topic, in.Lesson)
		return nil, d, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "history",
		Title: "What happened",
		Description: "Show a Topic's most recent Events, newest first, each with its type, when it happened, a short " +
			"summary and its Lesson: what happened in earlier Sessions, on any machine. Filter by an Event type prefix " +
			"(such as card. or attempt) or a Lesson. It is for you to understand the Topic's past, never a tally to show " +
			"the learner: never count Events, Attempts or Reviews back to them.",
		Annotations: read,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyInput) (*mcp.CallToolResult, core.HistoryView, error) {
		v, err := c.HistoryOf(ctx, in.Topic, core.HistoryQuery{Limit: in.Limit, Type: in.Type, Lesson: in.Lesson})
		return nil, v, toolErr(err)
	})
}

type historyInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id, from status"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many Events to show, newest first; default 10, at most 100"`
	Type   string `json:"type,omitempty" jsonschema:"keep only Events whose type starts with this, such as card. or attempt"`
	Lesson string `json:"lesson,omitempty" jsonschema:"keep only Events about this Lesson"`
}
