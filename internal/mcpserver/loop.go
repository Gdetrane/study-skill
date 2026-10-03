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
			"would be afterwards, and a summary in plain words. Nothing changes until the learner approves it. Show the " +
			"learner changes.text, which names Lessons by title and shows any renumbering. Skip a Lesson by setting " +
			"skipped on it, never by removing it; for each Lesson in changes.skipped_in_progress, go over what was " +
			"already covered with the learner and offer to keep it as Cards with card_add, which accepts a skipped " +
			"Lesson. Done and skipped Lessons keep their title, hours and Milestone. A " +
			"Revision must change something. Milestones can carry " +
			"a target date (YYYY-MM-DD). If status flags syllabus.toml as edited outside Lamplight, set from_file " +
			"instead to propose the learner's edit as it is.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in revisionProposeInput) (*mcp.CallToolResult, core.RevisionProposal, error) {
		spec := core.RevisionSpec{Summary: in.Summary, FromFile: in.FromFile}
		if in.Syllabus != nil {
			spec.Syllabus = *in.Syllabus
		}
		p, err := c.ProposeRevision(ctx, in.Topic, spec)
		return nil, p, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "revision_apply",
		Title: "Apply an approved Revision",
		Description: "Ask the learner to approve a proposed Revision, and apply it if they do. When this client can ask " +
			"the learner directly, Lamplight shows them the change and records their answer itself; the result says " +
			"whether they approved or declined. Otherwise, call it only after the learner approved in the conversation, " +
			"quoting their words in learner_said. Never apply a Revision the learner has not approved.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in revisionApplyInput) (*mcp.CallToolResult, core.RevisionApplied, error) {
		res, r, err := applyRevision(ctx, c, req, in)
		return res, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "revision_decline",
		Title: "Record that the learner declined a Revision",
		Description: "Record that the learner said no to a proposed Revision in the conversation, quoting their words. " +
			"Nothing in the Syllabus changes, and the Revision can no longer be applied; propose a new one if they " +
			"want something else.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in revisionDeclineInput) (*mcp.CallToolResult, core.RevisionDeclined, error) {
		r, err := c.DeclineRevision(ctx, in.Topic, in.Revision, core.Approval{Via: core.ViaChat, LearnerSaid: in.LearnerSaid}, false)
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "session_open",
		Title: "Open a Session",
		Description: "Open a Session on a Topic after the Energy check; it also makes the Topic the most recent one. " +
			"Read the Learner profile and the Topic's additions (paths in status) first. Show the learner the resume " +
			"point: the Lesson, the last Break point reached and the Next step word for word. Give energy; leave focus " +
			"out until the learner chooses: suggested.suggest is learn, practice, reviews or explore (a Focus to " +
			"offer), plan (no Syllabus yet: plan it together) or stop (write tomorrow's first step and end here); " +
			"suggested.reason is English you may rephrase. cards.ready says whether Reviews are possible; never mention " +
			"how many Cards are due. If long_gap is set, start with a short recap and a two-minute warm-up. unclosed " +
			"lists the Sessions that ended without a Next step, newest first: show the learner changes (what changed " +
			"since the last Checkpoint), ask for each missing note, and record it with session_close naming the Session.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionOpenInput) (*mcp.CallToolResult, core.SessionOpened, error) {
		r, err := c.OpenSession(ctx, in.Topic, core.SessionSpec{Energy: in.Energy, Focus: in.Focus})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "session_close",
		Title: "Close the Session",
		Description: "Close the open Session with a Next step that starts with a verb and says what to act on (\"Fix " +
			"the off-by-one in parse.go\", never \"Continue\" or \"The parser is half done\") and the context needed " +
			"to take it. The learner sees it first when they come back.",
		Annotations: write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionCloseInput) (*mcp.CallToolResult, core.SessionClosed, error) {
		r, err := c.CloseSession(ctx, in.Topic, core.CloseSpec{Session: in.Session, NextStep: in.NextStep, Context: in.Context})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "break_point_reached",
		Title: "Reach a Break point",
		Description: "Record that the learner reached one of the Break points declared under break_points: in the " +
			"Lesson's YAML header, with a Next step that starts with a verb and the context needed to take it. Only " +
			"the current Lesson (the Resume point's) has Break points to reach. The Session can stop there, and the " +
			"next one resumes from it; the Session stays open until session_close. Reaching the Break point the Lesson " +
			"is already at, with the same Next step and context, records nothing.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in breakPointInput) (*mcp.CallToolResult, core.BreakPointReached, error) {
		r, err := c.ReachBreakPoint(ctx, in.Topic, core.BreakPointSpec{
			Lesson: in.Lesson, BreakPoint: in.BreakPoint, NextStep: in.NextStep, Context: in.Context,
		})
		return nil, r, toolErr(err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "phase_set",
		Title: "Move a Lesson to a Phase",
		Description: "Move a Lesson to teaching, practicing or feedback. Practicing needs the Lesson's Check, which you " +
			"show the learner first. When the turn passes between you and the learner, a Checkpoint is taken; if the " +
			"result has checkpoint_error, call checkpoint with its checkpoint_role once the problem is fixed. After a " +
			"failed Attempt, go back to practicing with a next_step that names the fix; like every Next step it starts " +
			"with a verb and says what to act on. Asking for the Phase, Next step and Check the Lesson already has " +
			"records nothing.",
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
			"at least one from the learner's own mistakes. Completing a Lesson twice records nothing, so after an error " +
			"or an interruption, call it again with the same arguments. If the result has checkpoint_error, call " +
			"checkpoint with its checkpoint_role once the problem is fixed.",
		Annotations: idempotent,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in lessonCompleteInput) (*mcp.CallToolResult, core.LessonCompletion, error) {
		r, err := c.CompleteLesson(ctx, in.Topic, core.CompleteSpec{Lesson: in.Lesson, Cards: in.Cards})
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
	Topic    string         `json:"topic" jsonschema:"the Topic's id"`
	Summary  string         `json:"summary" jsonschema:"what changes and why, in plain words"`
	Syllabus *core.Syllabus `json:"syllabus,omitempty" jsonschema:"the whole Syllabus as it would be after the Revision; leave out with from_file"`
	FromFile bool           `json:"from_file,omitempty" jsonschema:"propose syllabus.toml as it is on disk, to adopt the learner's hand edit"`
}

type revisionApplyInput struct {
	Topic       string `json:"topic" jsonschema:"the Topic's id"`
	Revision    string `json:"revision" jsonschema:"the Revision's id, from revision_propose"`
	LearnerSaid string `json:"learner_said,omitempty" jsonschema:"the learner's approval in the conversation, quoted in their own words; not used when Lamplight can ask them directly"`
}

type revisionDeclineInput struct {
	Topic       string `json:"topic" jsonschema:"the Topic's id"`
	Revision    string `json:"revision" jsonschema:"the Revision's id, from revision_propose"`
	LearnerSaid string `json:"learner_said" jsonschema:"what the learner said, quoted in their own words"`
}

type sessionOpenInput struct {
	Topic  string `json:"topic" jsonschema:"the Topic's id"`
	Energy string `json:"energy,omitempty" jsonschema:"the learner's Energy: full, half or fumes"`
	Focus  string `json:"focus,omitempty" jsonschema:"what the Session is for: learn, practice, reviews or explore"`
}

type sessionCloseInput struct {
	Topic    string `json:"topic" jsonschema:"the Topic's id"`
	Session  string `json:"session,omitempty" jsonschema:"the Session to close; the latest when left out; name an unclosed older Session to give it its missing note"`
	NextStep string `json:"next_step" jsonschema:"the concrete next action, starting with a verb"`
	Context  string `json:"context,omitempty" jsonschema:"what the learner needs to know to take the Next step"`
}

type breakPointInput struct {
	Topic      string `json:"topic" jsonschema:"the Topic's id"`
	Lesson     string `json:"lesson" jsonschema:"the Lesson's id"`
	BreakPoint string `json:"break_point" jsonschema:"the id of a Break point declared in the Lesson's YAML header"`
	NextStep   string `json:"next_step" jsonschema:"the concrete next action, starting with a verb"`
	Context    string `json:"context,omitempty" jsonschema:"what the learner needs to know to take the Next step"`
}

type phaseSetInput struct {
	Topic    string `json:"topic" jsonschema:"the Topic's id"`
	Lesson   string `json:"lesson" jsonschema:"the Lesson's id"`
	Phase    string `json:"phase" jsonschema:"teaching, practicing or feedback"`
	NextStep string `json:"next_step,omitempty" jsonschema:"a Next step for the new Phase, starting with a verb and saying what to act on, such as the fix a failed Attempt calls for"`
}

type lessonCompleteInput struct {
	Topic  string           `json:"topic" jsonschema:"the Topic's id"`
	Lesson string           `json:"lesson" jsonschema:"the Lesson's id"`
	Cards  []core.CardDraft `json:"cards,omitempty" jsonschema:"draft Cards written from the Lesson"`
}
