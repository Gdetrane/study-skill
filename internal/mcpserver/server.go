// Package mcpserver exposes the core to agents over the Model Context Protocol.
//
// It is a thin adapter: each tool calls one core operation and reports core
// errors as tool errors that carry the error code.
package mcpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Instructions are sent to every agent that connects. They carry the rules
// that must never drift from the binary, so even an outdated skill gets them.
const Instructions = `Lamplight keeps the learner's study state. Follow these rules:
1. Call the status tool at the start of every session, and tell the learner which Topic is active and why, where they stopped, and the one recommended action. Read the Learner profile and the Topic's additions it points to before teaching.
2. Every tool that writes names its Topic explicitly. The Active topic is only a default for reading.
3. Never edit Lamplight's state files yourself (topic.toml, syllabus.toml, history.jsonl, cards.jsonl, sources.jsonl, tasks.jsonl). Write lesson text, notes and exercise files directly.
4. Never show counts of overdue or late work. Show where the learner is and one next action.
5. Every turn switch and every stop gets a Checkpoint: role "learner" when the learner hands their work to you or stops while practicing, "agent" when you hand the turn back or stop in your turn. phase_set, lesson_complete, break_point_reached and session_close take these Checkpoints for you; outside them, call checkpoint. When a result has checkpoint_error, tell the learner, and once the problem is fixed call checkpoint with its checkpoint_role. Never run git commit yourself.
6. Declare a Lesson's Break points under break_points: in its YAML header. When the learner reaches one in the current Lesson, record it with break_point_reached. Whenever a Session stops, record a Next step that starts with a verb and says what to act on with session_close. When a Milestone's Assessment is due, the Next step names the next Lesson's first step, or, when no Lesson follows, taking that Assessment: the Assessment is cued on its own, and at the next Session you offer it first. When a Session opens with unclosed ones, show the learner what changed since the last Checkpoint and ask for each missing note.
7. Run a Lesson's Check only with "study check <lesson> --topic <topic>" in your own shell, never any other way; it records the Attempt. Write the Check in the YAML header of lessons/<lesson-id>.md (run, rubric and held_out criteria) and show it to the learner before practicing starts. A Check needs at least one run criterion or rubric item. Grade rubric items with rubric_record after the learner checks themselves, naming in looked_at only files of their work in practice/<lesson-id>/ that are not ignored. Held-out results are diagnostic and never decide completion: write Held-out data (synthetic or public, never personal) into .heldout/<lesson-id>/, never show the learner that data or a held_out command's output, only its results, and never rename or re-create a held_out criterion to get a fresh first run. Only the first run on the Check shown to the learner counts. After a failed Attempt, give feedback, then go back to practicing with a Next step that names the fix.
8. Change the Syllabus only through Revisions: revision_propose, show the learner the change, then revision_apply, which asks the learner directly when this client can; otherwise call it only after they approved in their own words, and record a no with revision_decline.
9. Review only the Cards due_cards returns: it follows the Session's Energy and a daily cap on new Cards. At a draft's first Review, the learner keeps, edits or drops it. Write Cards with one fact each, no lists, no answer in the prompt, no trivia, and at least one from the learner's own mistakes.
10. Show Forecasts as they are given, when a Milestone ends at the learner's Pace, never how far behind anything is. A Triage is something to consider, never the one next action: offer its options and change nothing until the learner chooses, a Revision to move Lessons, trim Stretch goals or change a target date, or topic_update for the Pace or the Goal's deadline.
11. A paused Topic stays paused until the learner resumes it with topic_update (state active); never resume it yourself. When status recommends resume_topic or session_open says paused, offer to resume it or to pick another Topic, and teach nothing from it until it is resumed. A finished Topic offers only its Reviews.
12. Run the placement Assessment when a Topic is created, before drafting its Syllabus, and one at the end of each Milestone, each kept to about 15 minutes; record them with assessment_record. Weak results never block anything: when next says propose_revision, propose a Revision for the weak areas. The Level is set at an Assessment; the learner can change it at any time with topic_update, and their choice holds until the next Assessment. Teach at the Topic's Level and in its Approach. Record every hint you give with hint_record, saying whether the learner asked for it or you offered it. Read signals to adapt how you teach, never to show the learner counts or scores.
13. A Topic imported from a v1 workspace with "study import" is adopted before anything else: when status recommends adopt or session_open suggests it, go through the adoption with the learner (Goal and deadline, Pace, the Syllabus from notes/v1-plan.md approved as a Revision, Checks for open Lessons, the Knowledge base, Cards for completed Lessons, and the Next step from where v1 stopped). Keep the Lessons the import proved done in the Syllabus, under their v1 ids.`

// New returns an MCP server whose tools call c. logger, if not nil, receives
// the server's Log; it must never write to the transport's stdout.
func New(c *core.Core, version string, logger *slog.Logger) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "lamplight", Title: "Lamplight", Version: version},
		&mcp.ServerOptions{Instructions: Instructions, Logger: logger},
	)
	closedWorld := false
	notDestructive := false
	destructive := true

	mcp.AddTool(server, &mcp.Tool{
		Name:  "status",
		Title: "Where the learner is",
		Description: "Show the Study home, the Active topic and why it was chosen, and every Topic. Call this first " +
			"in every session. Tell the learner, for the Active topic: its resume point (Lesson, Break point, and the " +
			"Next step word for word), the one recommended action, and whether Cards are ready (cards.ready; never a " +
			"count). When recommended.action is assess, every Lesson of recommended.milestone is done: offer its " +
			"Assessment first, before the Next step, which the resume point still shows. learner_profile and a Topic's learner_additions are files " +
			"to read before teaching. A Topic's flags name anything that needs the learner's attention; tell the " +
			"learner about them.",
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
		Description: "Change a Topic's settings: title, goal, Knowledge base, the Goal's deadline, the Pace, the daily " +
			"cap on new Cards, Tasks, the Level, the Approach, or its state (paused, finished or active again). Fields left out stay as they " +
			"are; an empty goal or deadline removes it, and an empty pace removes the Pace. The Knowledge base is kind " +
			"notebooklm, with the notebook's id, or none; choose it with the learner when creating the Topic. A Pace is " +
			"dated periods of hours a week, such as 10 until a deadline and then 3; it gives each Milestone a Forecast. " +
			"A paused Topic offers no Cards and no Forecasts. remove_tasks deletes Tasks for good. level is the " +
			"learner's choice of Level, which holds until the next Assessment; set it only when they ask. The Approach " +
			"(concepts, project or challenges) is chosen with the learner when creating the Topic. Asking for the values " +
			"the Topic already has changes nothing.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in topicUpdateInput) (*mcp.CallToolResult, core.TopicUpdate, error) {
		changes := core.TopicChanges{Title: in.Title, Goal: in.Goal, Deadline: in.Deadline, Pace: in.Pace,
			NewCardsPerDay: in.NewCardsPerDay, State: in.State, AddTasks: in.AddTasks, RemoveTasks: in.RemoveTasks,
			Level: in.Level, Approach: in.Approach}
		if in.KnowledgeBase != nil {
			changes.KnowledgeBase = &core.KnowledgeBase{Kind: in.KnowledgeBase.Kind, Notebook: in.KnowledgeBase.Notebook}
		}
		updated, err := c.UpdateTopic(ctx, in.Topic, changes)
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

	addLearnerLoop(server, c)
	addCardTools(server, c)

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

	addKnowledgeTools(server, c)
	addPlanTools(server, c)
	addAssessmentTools(server, c)
	addReadTools(server, c)
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
func Serve(ctx context.Context, c *core.Core, version string, in io.Reader, out io.Writer, logger *slog.Logger) error {
	rc, ok := in.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(in)
	}
	wc, ok := out.(io.WriteCloser)
	if !ok {
		wc = nopWriteCloser{out}
	}
	done := make(chan error, 1)
	go func() { done <- New(c, version, logger).Run(ctx, &mcp.IOTransport{Reader: rc, Writer: wc}) }()

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
	// KnowledgeBase chooses where the Topic's Sources are searched.
	KnowledgeBase *knowledgeBaseInput `json:"knowledge_base,omitempty" jsonschema:"the Knowledge base: kind notebooklm with the notebook's id, or none"`
	Deadline      *string             `json:"deadline,omitempty" jsonschema:"the Goal's deadline, YYYY-MM-DD; an empty string removes it"`
	// Pace replaces the Pace periods.
	Pace           *[]core.PacePeriod `json:"pace,omitempty" jsonschema:"the Pace as dated periods, replacing the current ones; an empty list removes the Pace"`
	NewCardsPerDay *int               `json:"new_cards_per_day,omitempty" jsonschema:"the daily cap on new Cards decided; 10 unless set"`
	State          *string            `json:"state,omitempty" jsonschema:"active, paused or finished"`
	Level          *string            `json:"level,omitempty" jsonschema:"the learner's choice of Level: beginner, intermediate, advanced or expert; it holds until the next Assessment"`
	Approach       *string            `json:"approach,omitempty" jsonschema:"how the Lessons relate: concepts (standalone concepts), project (one project built step by step) or challenges (a run of challenges)"`
	AddTasks       []core.TaskSpec    `json:"add_tasks,omitempty" jsonschema:"Tasks to add: steps toward the Goal that are not study"`
	RemoveTasks    []string           `json:"remove_tasks,omitempty" jsonschema:"ids of Tasks to remove"`
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
