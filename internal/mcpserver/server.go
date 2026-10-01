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
)

// Instructions are sent to every agent that connects. They carry the rules
// that must never drift from the binary, so even an outdated skill gets them.
const Instructions = `Lamplight keeps the learner's study state. Follow these rules:
1. Call the status tool at the start of every session, and tell the learner which Topic is active and why.
2. Every tool that writes names its Topic explicitly. The Active topic is only a default for reading.
3. Never edit Lamplight's state files yourself (topic.toml, syllabus.toml, history.jsonl, cards.jsonl). Write lesson text, notes and exercise files directly.
4. Never show counts of overdue or late work. Show where the learner is and one next action.`

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

	return server
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

type topicCreateInput struct {
	Title string `json:"title" jsonschema:"what the learner is studying, for example Linear algebra"`
	ID    string `json:"id,omitempty" jsonschema:"folder name: lowercase letters, digits and single hyphens; derived from the title when omitted"`
	Goal  string `json:"goal,omitempty" jsonschema:"what the learner wants to be able to do at the end"`
}

// toolError reports a core error to the agent with its stable code.
func toolError(err error) error {
	return fmt.Errorf("%s: %w", core.CodeOf(err), err)
}
