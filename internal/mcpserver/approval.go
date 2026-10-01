package mcpserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// approvalInput is the id of the question in a multi round-trip request.
const approvalInput = "approval"

// multiRoundTripVersion is the first protocol version in which a server asks
// the learner during a tool call by returning its questions in the result
// (SEP-2322) instead of sending an elicitation request.
const multiRoundTripVersion = "2026-07-28"

// approvalSchema is the form Lamplight shows the learner through MCP
// elicitation when it asks them directly about a Revision.
var approvalSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decision": map[string]any{
			"type":  "string",
			"title": "Apply this change to the Syllabus?",
			"enum":  []string{"approve", "decline"},
		},
		"comment": map[string]any{
			"type":  "string",
			"title": "Anything to add (optional)",
		},
	},
	"required": []string{"decision"},
}

// stateKey signs the request state of multi round-trip questions, so an
// answer counts only for the question this server asked.
var stateKey = func() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand.Read never returns an error.
	return key
}()

// questionState binds a retry to the Revision and the exact question shown.
func questionState(revision, question string) string {
	mac := hmac.New(sha256.New, stateKey)
	mac.Write([]byte(revision + "\n" + question))
	return revision + ":" + hex.EncodeToString(mac.Sum(nil))
}

// canElicit reports whether the client says it can ask the learner directly.
func canElicit(req *mcp.CallToolRequest) bool {
	if req == nil {
		return false
	}
	caps := req.ClientCapabilities()
	return caps != nil && caps.Elicitation != nil
}

// applyRevision asks the learner directly when the client can, and records
// their answer; otherwise it applies the Revision with the approval the
// agent relays from the conversation. The agent never chooses how approval
// was given.
func applyRevision(ctx context.Context, c *core.Core, req *mcp.CallToolRequest, in revisionApplyInput) (*mcp.CallToolResult, core.RevisionApplied, error) {
	chat := core.Approval{Via: core.ViaChat, LearnerSaid: in.LearnerSaid}
	if !canElicit(req) {
		r, err := c.ApplyRevision(ctx, in.Topic, in.Revision, chat, false)
		return nil, r, err
	}
	p, err := c.Revision(ctx, in.Topic, in.Revision)
	if err != nil {
		// Already applied: applying again changes nothing, as without
		// elicitation.
		if r, applyErr := c.ApplyRevision(ctx, in.Topic, in.Revision, core.Approval{}, true); applyErr == nil && !r.Changed {
			r.DryRun = false
			return nil, r, nil
		}
		return nil, core.RevisionApplied{}, err
	}
	question := p.Question()
	var answer *mcp.ElicitResult
	if req.ProtocolVersion() >= multiRoundTripVersion {
		// Ask by returning the question; the client asks the learner and
		// calls again with the answer and the state. An answer to another
		// question, such as one about a Syllabus that has since changed,
		// is asked again.
		resp, ok := req.Params.InputResponses[approvalInput].(*mcp.ElicitResult)
		if !ok || !hmac.Equal([]byte(req.Params.RequestState), []byte(questionState(in.Revision, question))) {
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{approvalInput: &mcp.ElicitParams{Message: question, RequestedSchema: approvalSchema}},
				RequestState:  questionState(in.Revision, question),
			}, core.RevisionApplied{}, nil
		}
		answer = resp
	} else if answer, err = req.Session.Elicit(ctx, &mcp.ElicitParams{Message: question, RequestedSchema: approvalSchema}); err != nil {
		// The client said it could ask, but could not: fall back to the
		// conversation, if the agent has the learner's words.
		if in.LearnerSaid == "" {
			return nil, core.RevisionApplied{}, &core.Error{Code: core.CodeFailedPrecondition, Message: "Lamplight could not ask the " +
				"learner directly (" + err.Error() + "): ask them in the conversation and pass their words in learner_said"}
		}
		r, err := c.ApplyRevision(ctx, in.Topic, in.Revision, chat, false)
		return nil, r, err
	}
	decision, _ := answer.Content["decision"].(string)
	comment, _ := answer.Content["comment"].(string)
	said := core.Approval{Via: core.ViaElicitation, LearnerSaid: strings.TrimSpace(comment), Shown: question}
	switch {
	case answer.Action == "accept" && decision == "approve":
		r, err := c.ApplyRevision(ctx, in.Topic, in.Revision, said, false)
		return nil, r, err
	case answer.Action == "decline" || (answer.Action == "accept" && decision == "decline"):
		r, err := c.DeclineRevision(ctx, in.Topic, in.Revision, said, false)
		return nil, r, err
	}
	return nil, core.RevisionApplied{}, &core.Error{Code: core.CodeFailedPrecondition, Message: "the learner closed the " +
		"question without answering, so nothing changed; ask them again when they are ready"}
}
