// Package mcpserver exposes the core to agents over the Model Context Protocol.
//
// It is a thin adapter: each tool calls one core operation and reports core
// errors as tool errors that carry the error code.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Instructions are sent to every agent that connects. They carry the rules
// that must never drift from the binary, so even an outdated skill gets them.
const Instructions = `Lamplight keeps the learner's study state. Follow these rules:
1. Call the status tool at the start of every session, and tell the learner which Topic is active and why.
2. Every tool that writes names its Topic explicitly. The Active topic is only a default for reading.
3. Never edit Lamplight's state files yourself (topic.toml, syllabus.toml, history.jsonl, cards.jsonl). Write lesson text, notes and exercise files directly.
4. Never show counts of overdue or late work. Show where the learner is and one next action.
5. Call checkpoint at every turn switch: role "learner" when the learner hands their work to you, "agent" when you hand the turn back. Never run git commit yourself.`

// More rules join Instructions as their features land: recording a Next step
// when a Session stops (#28) and running Checks through the CLI (#30).

// New returns an MCP server whose tools call c.
func New(c *core.Core, version string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "lamplight", Title: "Lamplight", Version: version},
		&mcp.ServerOptions{Instructions: Instructions},
	)
	closedWorld := false
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:  "status",
		Title: "Where the learner is",
		Description: "Show the Study home, the Active topic and why it was chosen, and every Topic. " +
			"A Topic's flags name anything that needs the learner's attention; tell the learner about them. " +
			"Call this first in every session.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, core.Status, error) {
		status, err := c.Status(ctx)
		if err != nil {
			return nil, core.Status{}, toolError(err)
		}
		return nil, status, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "flag_dismiss",
		Title: "Dismiss a flag",
		Description: "Dismiss one of a Topic's flags, by the id status gives it. Only call this after the learner has " +
			"seen the flag and accepted it; never dismiss a flag on your own. Dismissing records the decision and never " +
			"changes content.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flagDismissInput) (*mcp.CallToolResult, core.FlagDismissal, error) {
		res, err := c.DismissFlag(ctx, in.Topic, in.Flag, false)
		if err != nil {
			return nil, core.FlagDismissal{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "topic_create",
		Title: "Create a Topic",
		Description: "Create a Topic: a folder in the Study home with its settings, History and git repository. " +
			"Use it after brainstorming the subject with the learner.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicCreateInput) (*mcp.CallToolResult, core.Topic, error) {
		topic, err := c.CreateTopic(ctx, core.TopicSpec{Title: in.Title, ID: in.ID, Goal: in.Goal})
		if err != nil {
			return nil, core.Topic{}, toolError(err)
		}
		return nil, topic, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "topic_update",
		Title: "Change a Topic",
		Description: "Change a Topic's title or goal. Fields left out stay as they are; an empty goal removes it. " +
			"Asking for the values the Topic already has changes nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicUpdateInput) (*mcp.CallToolResult, core.TopicUpdate, error) {
		updated, err := c.UpdateTopic(ctx, in.Topic, core.TopicChanges{Title: in.Title, Goal: in.Goal})
		if err != nil {
			return nil, core.TopicUpdate{}, toolError(err)
		}
		return nil, updated, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "checkpoint",
		Title: "Save the work in a Topic",
		Description: "Save the work in a Topic as a git commit at every turn switch: role \"learner\" when the learner's turn " +
			"ended, \"agent\" when yours did. Nothing is committed when nothing changed. Mention any large_files to the " +
			"learner: data and model files usually belong in the Topic's .gitignore.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in checkpointInput) (*mcp.CallToolResult, core.CheckpointResult, error) {
		res, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: in.Topic, Role: in.Role, Message: in.Message})
		if err != nil {
			return nil, core.CheckpointResult{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "library_search",
		Title: "Search the Library",
		Description: "Find books in the learner's Library, ranked by relevance, with their absolute paths. " +
			"The learner builds the index with `study library build <folder>`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in librarySearchInput) (*mcp.CallToolResult, librarySearchOutput, error) {
		results, err := c.SearchLibrary(ctx, in.Query, in.Limit)
		if err != nil {
			return nil, librarySearchOutput{}, toolError(err)
		}
		return nil, librarySearchOutput{Results: results}, nil
	})

	return server
}

type checkpointInput struct {
	Topic   string `json:"topic" jsonschema:"the Topic's id, from status"`
	Role    string `json:"role" jsonschema:"whose turn ended: agent or learner"`
	Message string `json:"message,omitempty" jsonschema:"what happened in the turn, in a few words"`
}

type librarySearchInput struct {
	Query string `json:"query" jsonschema:"what to look for, such as a subject, a title or a language"`
	Limit int    `json:"limit,omitempty" jsonschema:"most results to return; default 10, at most 100"`
}

type librarySearchOutput struct {
	Results []library.Result `json:"results"`
}

// shutdownGrace is how long Serve waits, once ctx is cancelled, for the
// session to finish on its own, and again after closing the streams.
var shutdownGrace = 2 * time.Second

// Serve runs the server over in and out (stdin and stdout for study mcp)
// until the client disconnects or ctx is cancelled.
//
// A cancelled session waits for responses already being written, so a client
// that stopped reading could block it forever. Serve therefore gives the
// session a grace period, then closes the streams (which interrupts blocked
// pipe I/O), and returns after a second grace period even if a write is still
// stuck.
func Serve(ctx context.Context, c *core.Core, version string, in io.Reader, out io.Writer) error {
	rc, ok := in.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(in)
	}
	wc, ok := out.(io.WriteCloser)
	if !ok {
		wc = nopWriteCloser{out}
	}
	done := make(chan error, 1)
	go func() { done <- New(c, version).Run(ctx, &mcp.IOTransport{Reader: rc, Writer: wc}) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	select {
	case err := <-done:
		return err
	case <-time.After(shutdownGrace):
	}
	_ = rc.Close()
	_ = wc.Close()
	select {
	case err := <-done:
		return err
	case <-time.After(shutdownGrace):
		return ctx.Err()
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type flagDismissInput struct {
	Topic string `json:"topic" jsonschema:"the id of the Topic the flag belongs to"`
	Flag  string `json:"flag" jsonschema:"the flag's id, from status"`
}

type topicUpdateInput struct {
	Topic string  `json:"topic" jsonschema:"the id of the Topic to change"`
	Title *string `json:"title,omitempty" jsonschema:"the new title"`
	Goal  *string `json:"goal,omitempty" jsonschema:"the new goal; an empty string removes it"`
}

type topicCreateInput struct {
	Title string `json:"title" jsonschema:"what the learner is studying, for example Linear algebra"`
	ID    string `json:"id,omitempty" jsonschema:"folder name: lowercase letters, digits and single hyphens; derived from the title when omitted"`
	Goal  string `json:"goal,omitempty" jsonschema:"what the learner wants to be able to do at the end"`
}

// toolError reports a core error to the agent with its stable code.
func toolError(err error) error {
	return fmt.Errorf("%s: %w", core.CodeOf(err), err)
}
