package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addPlanTools adds the tools for a Topic's Tasks. The Goal's deadline, the
// Pace, the daily cap on new Cards and the Topic's state are set through
// topic_update; Forecasts and Triage come with status and syllabus.
func addPlanTools(server *mcp.Server, c *core.Core) {
	closedWorld := false
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:  "tasks",
		Title: "A Topic's Tasks",
		Description: "List a Topic's Tasks, the steps toward its Goal that are not study, with their ids. status shows " +
			"the open ones that matter now; this lists every open one, and the done ones too with all.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tasksInput) (*mcp.CallToolResult, core.TaskList, error) {
		res, err := c.ListTasks(ctx, in.Topic, in.All)
		if err != nil {
			return nil, core.TaskList{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "task_done",
		Title: "Mark a Task done",
		Description: "Mark a Task done when the learner says it is, or not done again with done false. " +
			"Marking it as it already is changes nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskDoneInput) (*mcp.CallToolResult, core.TaskMark, error) {
		done := in.Done == nil || *in.Done
		res, err := c.MarkTask(ctx, in.Topic, in.Task, done, false)
		if err != nil {
			return nil, core.TaskMark{}, toolError(err)
		}
		return nil, res, nil
	})
}

type tasksInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id, from status"`
	All   bool   `json:"all,omitempty" jsonschema:"include the Tasks already done"`
}

type taskDoneInput struct {
	Topic string `json:"topic" jsonschema:"the Topic's id, from status"`
	Task  string `json:"task" jsonschema:"the Task's id, from status or tasks"`
	Done  *bool  `json:"done,omitempty" jsonschema:"false marks the Task not done again; true when left out"`
}
