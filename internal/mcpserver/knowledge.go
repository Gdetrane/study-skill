package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// addKnowledgeTools adds the Knowledge seam's tools (ADR-0007): Sources and
// Evidence. The core never searches a Knowledge base: the agent asks the
// knowledge MCP server it has, such as NotebookLM's, and records here what it
// relied on.
func addKnowledgeTools(server *mcp.Server, c *core.Core) {
	closedWorld := false
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:  "sources",
		Title: "A Topic's Knowledge base and Sources",
		Description: "List a Topic's Knowledge base and Sources before teaching from its material. " +
			"With Knowledge base kind notebooklm, ask the NotebookLM MCP server, if it is installed, about the notebook " +
			"and use its citations; with none, or when no knowledge server is available, read the Sources yourself: " +
			"files by their path, web pages by their URL. A file whose state is moved has a found_at path: record it " +
			"with source_update. A Topic without a Knowledge base yet behaves as none.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicInput) (*mcp.CallToolResult, core.SourceList, error) {
		list, err := c.ListSources(ctx, in.Topic)
		if err != nil {
			return nil, core.SourceList{}, toolError(err)
		}
		return nil, list, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "source_add",
		Title: "Add a Source",
		Description: "Add a document or web page the Topic learns from: a file by its absolute path (library_search " +
			"finds the learner's books) or a URL. Files are hashed, never parsed. When the Topic's Knowledge base is " +
			"notebooklm, also add the Source to the notebook with the NotebookLM MCP server and record its NotebookLM " +
			"id, here or later with source_update.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceAddInput) (*mcp.CallToolResult, core.SourceResult, error) {
		res, err := c.AddSource(ctx, core.SourceSpec{Topic: in.Topic, File: in.File, URL: in.URL,
			Title: in.Title, NotebookLMID: in.NotebookLMID})
		if err != nil {
			return nil, core.SourceResult{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "source_update",
		Title: "Change a Source",
		Description: "Change a Source's title or NotebookLM id (an empty id removes it), or record where a moved file " +
			"is now: the sources tool gives its found_at path. The file at the new path must hold the same content.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceUpdateInput) (*mcp.CallToolResult, core.SourceResult, error) {
		res, err := c.UpdateSource(ctx, in.Topic, in.Source, core.SourceChanges{Title: in.Title, Path: in.Path,
			NotebookLMID: in.NotebookLMID})
		if err != nil {
			return nil, core.SourceResult{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "evidence_record",
		Title: "Record Evidence",
		Description: "Record an exact quote from one of the Topic's Sources that a Lesson relies on: copied word for " +
			"word, never paraphrased. Give its location when you know it, and where the location came from: source " +
			"(read in the Source itself), knowledge_base (such as a NotebookLM citation), learner, or estimate. " +
			"Record Evidence whenever you teach from a Source; Lessons without Evidence are marked, never blocked. " +
			"Recording the same Evidence twice changes nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceRecordInput) (*mcp.CallToolResult, core.EvidenceResult, error) {
		res, err := c.RecordEvidence(ctx, core.EvidenceSpec{Topic: in.Topic, Lesson: in.Lesson, Source: in.Source,
			Quote: in.Quote, Location: in.Location, LocationFrom: in.LocationFrom})
		if err != nil {
			return nil, core.EvidenceResult{}, toolError(err)
		}
		return nil, res, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "evidence",
		Title:       "Evidence in a Topic",
		Description: "List the Evidence recorded in a Topic, in the order it was recorded, or only what one Lesson cites.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceListInput) (*mcp.CallToolResult, core.EvidenceList, error) {
		list, err := c.ListEvidence(ctx, in.Topic, in.Lesson)
		if err != nil {
			return nil, core.EvidenceList{}, toolError(err)
		}
		return nil, list, nil
	})
}

type sourceAddInput struct {
	Topic        string `json:"topic" jsonschema:"the Topic's id, from status"`
	File         string `json:"file,omitempty" jsonschema:"the file's absolute path; give a file or a url"`
	URL          string `json:"url,omitempty" jsonschema:"the web page's http or https address; give a file or a url"`
	Title        string `json:"title,omitempty" jsonschema:"the Source's title; derived from the file or URL when omitted"`
	NotebookLMID string `json:"notebooklm_id,omitempty" jsonschema:"the Source's id in the Topic's NotebookLM notebook"`
}

type sourceUpdateInput struct {
	Topic        string  `json:"topic" jsonschema:"the Topic's id, from status"`
	Source       string  `json:"source" jsonschema:"the Source's id, from the sources tool"`
	Title        *string `json:"title,omitempty" jsonschema:"the new title"`
	Path         *string `json:"path,omitempty" jsonschema:"where the file is now, such as a found_at path"`
	NotebookLMID *string `json:"notebooklm_id,omitempty" jsonschema:"the Source's id in the NotebookLM notebook; empty removes it"`
}

type evidenceRecordInput struct {
	Topic        string `json:"topic" jsonschema:"the Topic's id, from status"`
	Lesson       string `json:"lesson" jsonschema:"the id of the Lesson that cites the Evidence"`
	Source       string `json:"source" jsonschema:"the id of the Source the quote comes from"`
	Quote        string `json:"quote" jsonschema:"the exact quote, word for word as the Source has it"`
	Location     string `json:"location,omitempty" jsonschema:"where the quote is, such as p. 93 or §5.1"`
	LocationFrom string `json:"location_from,omitempty" jsonschema:"where the location came from: source, knowledge_base, learner or estimate"`
}

type evidenceListInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id, from status"`
	Lesson string `json:"lesson,omitempty" jsonschema:"only the Evidence this Lesson cites"`
}

type knowledgeBaseInput struct {
	Kind     string `json:"kind" jsonschema:"notebooklm or none"`
	Notebook string `json:"notebook,omitempty" jsonschema:"the NotebookLM notebook's id, for kind notebooklm"`
}
