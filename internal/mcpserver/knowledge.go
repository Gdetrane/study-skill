package mcpserver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
			"With Knowledge base kind notebooklm, ask the NotebookLM MCP server, if it is installed, about the notebook; " +
			"with none, or when no knowledge server is available, read the Sources yourself: files at their path on this " +
			"computer, web pages at their URL. Files are found automatically, inside the Topic or in the learner's " +
			"Library, by their content. State missing means the file is not on this computer: ask the learner where it " +
			"is and record it with source_update. State untracked is a line someone added to sources.jsonl by hand: " +
			"add the Source with source_add instead. notebooklm_stale means the Source's NotebookLM id belongs to " +
			"another notebook. A Topic without a Knowledge base yet behaves as none.",
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
		Description: "Add a document or web page the Topic learns from: a file by its absolute path or a path " +
			"starting with ~/ (library_search finds the learner's books), or an http or https URL. Files are hashed, " +
			"never parsed, and found again on any machine by their content. When the Topic's Knowledge base is " +
			"notebooklm, also add the Source to the notebook with the NotebookLM MCP server and record its NotebookLM " +
			"id, here or later with source_update.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceAddInput) (*mcp.CallToolResult, core.SourceResult, error) {
		if err := absolutePath(in.File); err != nil {
			return nil, core.SourceResult{}, toolError(err)
		}
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
		Description: "Change a Source's title or NotebookLM id (an empty id removes it), or record where its file is " +
			"on this computer when the sources tool says missing. The file there must hold the same content. A path is " +
			"remembered on this computer only and records nothing in the History.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sourceUpdateInput) (*mcp.CallToolResult, core.SourceResult, error) {
		if in.Path != nil {
			if err := absolutePath(*in.Path); err != nil {
				return nil, core.SourceResult{}, toolError(err)
			}
		}
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
		Description: "Record an exact quote from one of the Topic's Sources that a Lesson relies on: the Source's own " +
			"words, copied word for word, never paraphrased. With NotebookLM, quote the cited passage of the Source, " +
			"never NotebookLM's answer. Give the quote's location when you know it, and where it came from: source " +
			"(read in the Source itself, such as a printed page number), knowledge_base (a citation as the Knowledge " +
			"base gave it; NotebookLM citations carry no page numbers, so record the citation as given), learner, or " +
			"estimate. Record Evidence whenever you teach from a Source; Lessons without Evidence are marked, never " +
			"blocked. Recording the same Evidence twice changes nothing; evidence_retract takes back a mistake.",
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
		Name:  "evidence",
		Title: "Evidence in a Topic",
		Description: "List the Evidence recorded in a Topic, in the order it was recorded, or only what one Lesson " +
			"cites. Retracted Evidence is left out unless all is set.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceListInput) (*mcp.CallToolResult, core.EvidenceList, error) {
		list, err := c.ListEvidence(ctx, core.EvidenceQuery{Topic: in.Topic, Lesson: in.Lesson, All: in.All})
		if err != nil {
			return nil, core.EvidenceList{}, toolError(err)
		}
		return nil, list, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "evidence_retract",
		Title: "Retract Evidence",
		Description: "Take back Evidence recorded by mistake, such as a wrong quote or the wrong Lesson. The " +
			"retraction is recorded, never deleted; retracting it again changes nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in evidenceRetractInput) (*mcp.CallToolResult, core.EvidenceResult, error) {
		res, err := c.RetractEvidence(ctx, in.Topic, in.Evidence, false)
		if err != nil {
			return nil, core.EvidenceResult{}, toolError(err)
		}
		return nil, res, nil
	})
}

type sourceAddInput struct {
	Topic        string `json:"topic" jsonschema:"the Topic's id, from status"`
	File         string `json:"file,omitempty" jsonschema:"the file's absolute path, or a path starting with ~/; give a file or a url"`
	URL          string `json:"url,omitempty" jsonschema:"the web page's http or https address; give a file or a url"`
	Title        string `json:"title,omitempty" jsonschema:"the Source's title; derived from the file or URL when omitted"`
	NotebookLMID string `json:"notebooklm_id,omitempty" jsonschema:"the Source's id in the Topic's NotebookLM notebook"`
}

type sourceUpdateInput struct {
	Topic        string  `json:"topic" jsonschema:"the Topic's id, from status"`
	Source       string  `json:"source" jsonschema:"the Source's id, from the sources tool"`
	Title        *string `json:"title,omitempty" jsonschema:"the new title"`
	Path         *string `json:"path,omitempty" jsonschema:"where the file is on this computer: an absolute path, or one starting with ~/"`
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
	All    bool   `json:"all,omitempty" jsonschema:"include retracted Evidence"`
}

type evidenceRetractInput struct {
	Topic    string `json:"topic" jsonschema:"the Topic's id, from status"`
	Evidence string `json:"evidence" jsonschema:"the Evidence's id, from the evidence tool"`
}

// absolutePath refuses a relative file path: the MCP server's folder is not
// the agent's, so a relative path would name the wrong file.
func absolutePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/") {
		return nil
	}
	return &core.Error{Code: core.CodeInvalidArgument,
		Message: fmt.Sprintf("%q is a relative path: give the file's absolute path, or one starting with ~/", path)}
}

type knowledgeBaseInput struct {
	Kind     string `json:"kind,omitempty" jsonschema:"notebooklm or none; a notebook alone means notebooklm"`
	Notebook string `json:"notebook,omitempty" jsonschema:"the NotebookLM notebook's id, for kind notebooklm"`
}
