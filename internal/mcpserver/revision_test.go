package mcpserver_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var firstSyllabus = map[string]any{"milestones": []any{map[string]any{
	"id": "basics", "title": "Basics", "target": "2026-12-01",
	"lessons": []any{map[string]any{"id": "answer", "title": "The answer", "hours": 1}},
}}}

var secondSyllabus = map[string]any{"milestones": []any{map[string]any{
	"id": "basics", "title": "Basics", "target": "2026-12-01",
	"lessons": []any{
		map[string]any{"id": "question", "title": "The question"},
		map[string]any{"id": "answer", "title": "The answer", "hours": 1},
	},
}}}

// learner plays the learner behind an MCP client that can be asked
// directly: it answers each elicitation as told and keeps what it was shown.
type learner struct {
	mu      sync.Mutex
	asked   []string
	answer  *mcp.ElicitResult
	failing bool
}

func (l *learner) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked = append(l.asked, req.Params.Message)
	if l.failing {
		return nil, errors.New("the client has no way to show forms")
	}
	return l.answer, nil
}

func (l *learner) questions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.asked...)
}

func proposeSecond(t *testing.T, session *mcp.ClientSession) core.RevisionProposal {
	t.Helper()
	var p core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "Ask the question first", "syllabus": secondSyllabus}), &p)
	return p
}

func syllabusOf(t *testing.T, session *mcp.ClientSession) core.SyllabusView {
	t.Helper()
	var v core.SyllabusView
	decode(t, call(t, session, "syllabus", map[string]any{"topic": "c"}), &v)
	return v
}

func toolError(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("%s succeeded: %+v", name, res.StructuredContent)
	}
	return text(res)
}

// protocols are the protocol versions Lamplight asks the learner under: the
// latest returns the question in the tool's result (SEP-2322), older ones
// send an elicitation request during the call.
var protocols = map[string]*mcp.ClientSessionOptions{
	"latest":     nil,
	"2025-11-25": {ProtocolVersion: "2025-11-25"},
}

func TestTheLearnerApprovesDirectly(t *testing.T) {
	for name, p := range protocols {
		t.Run(name, func(t *testing.T) { theLearnerApprovesDirectly(t, p) })
	}
}

func TestTheLearnerClosesOrDeclinesTheQuestion(t *testing.T) {
	for name, p := range protocols {
		t.Run(name, func(t *testing.T) { theLearnerClosesOrDeclinesTheQuestion(t, p) })
	}
}

func TestApprovalFallsBackToTheConversation(t *testing.T) {
	for name, p := range protocols {
		t.Run(name, func(t *testing.T) { approvalFallsBackToTheConversation(t, p) })
	}
}

func theLearnerApprovesDirectly(t *testing.T, protocol *mcp.ClientSessionOptions) {
	l := &learner{answer: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve", "comment": "go for it"}}}
	session := connectWith(t, t.TempDir(), &mcp.ClientOptions{ElicitationHandler: l.handle}, protocol)
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &first)
	if !strings.Contains(first.Changes.Text, `Lesson 1.1 "The answer", 1 h`) || !strings.Contains(first.Changes.Text, "by 2026-12-01") {
		t.Errorf("the first Syllabus's changes:\n%s", first.Changes.Text)
	}

	// The agent passes no learner_said: Lamplight asks the learner itself.
	var applied core.RevisionApplied
	decode(t, call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision}), &applied)
	if !applied.Changed || applied.Approval.Via != core.ViaElicitation || applied.Approval.LearnerSaid != "go for it" {
		t.Fatalf("applied = %+v", applied)
	}
	asked := l.questions()
	if len(asked) != 1 || !strings.Contains(asked[0], "A first Syllabus") || !strings.Contains(asked[0], `"The answer"`) ||
		applied.Approval.Shown != asked[0] {
		t.Errorf("the learner was asked %q, the approval records %q", asked, applied.Approval.Shown)
	}

	// Even when the agent claims the learner said yes, Lamplight asks.
	second := proposeSecond(t, session)
	if !strings.Contains(second.Changes.Text, `Renumbers: "The answer" 1.1 → 1.2`) {
		t.Errorf("the second Revision's changes:\n%s", second.Changes.Text)
	}
	l.answer = &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "decline"}}
	decode(t, call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": second.Revision, "learner_said": "yes!"}), &applied)
	if applied.Changed != true || !applied.Declined || applied.Approval.Via != core.ViaElicitation {
		t.Errorf("declined directly = %+v", applied)
	}
	if v := syllabusOf(t, session); len(v.Milestones[0].Lessons) != 1 || len(v.Proposals) != 0 {
		t.Errorf("after the learner declined: %+v", v)
	}

	// Applying the first Revision again changes nothing, and asks nothing.
	decode(t, call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision}), &applied)
	if applied.Changed || len(l.questions()) != 2 {
		t.Errorf("applying again: %+v, asked %d times", applied, len(l.questions()))
	}
}

func theLearnerClosesOrDeclinesTheQuestion(t *testing.T, protocol *mcp.ClientSessionOptions) {
	l := &learner{answer: &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}}
	session := connectWith(t, t.TempDir(), &mcp.ClientOptions{ElicitationHandler: l.handle}, protocol)
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &first)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision})
	second := proposeSecond(t, session)

	l.answer = &mcp.ElicitResult{Action: "cancel"}
	if msg := toolError(t, session, "revision_apply", map[string]any{"topic": "c", "revision": second.Revision}); !strings.Contains(msg, "failed_precondition") ||
		!strings.Contains(msg, "without answering") {
		t.Errorf("a closed question: %s", msg)
	}
	if v := syllabusOf(t, session); len(v.Proposals) != 1 {
		t.Errorf("a closed question recorded something: %+v", v.Proposals)
	}

	l.answer = &mcp.ElicitResult{Action: "decline"}
	var declined core.RevisionApplied
	decode(t, call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": second.Revision}), &declined)
	if !declined.Declined {
		t.Errorf("declining the form = %+v", declined)
	}
}

func approvalFallsBackToTheConversation(t *testing.T, protocol *mcp.ClientSessionOptions) {
	// A client that cannot ask: the agent relays the learner's words.
	session := connect(t, t.TempDir())
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &first)
	if msg := toolError(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision}); !strings.Contains(msg, "invalid_argument") {
		t.Errorf("a chat approval without the learner's words: %s", msg)
	}
	var applied core.RevisionApplied
	decode(t, call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision, "learner_said": "yes, that looks right"}), &applied)
	if !applied.Changed || applied.Approval.Via != core.ViaChat {
		t.Errorf("a chat approval = %+v", applied)
	}
	second := proposeSecond(t, session)
	var declined core.RevisionApplied
	decode(t, call(t, session, "revision_decline", map[string]any{"topic": "c", "revision": second.Revision, "learner_said": "no, keep the order"}), &declined)
	if !declined.Declined || declined.Approval.LearnerSaid != "no, keep the order" {
		t.Errorf("declined in chat = %+v", declined)
	}

	// A client that says it can ask but fails falls back too, under the
	// older protocols. Under the latest, the client fulfils the question
	// itself and fails the call on its side, before the server sees it.
	if protocol == nil {
		return
	}
	l := &learner{failing: true}
	broken := connectWith(t, t.TempDir(), &mcp.ClientOptions{ElicitationHandler: l.handle}, protocol)
	call(t, broken, "topic_create", map[string]any{"title": "C", "id": "c"})
	decode(t, call(t, broken, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &first)
	if msg := toolError(t, broken, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision}); !strings.Contains(msg, "could not ask") {
		t.Errorf("a failed elicitation without the learner's words: %s", msg)
	}
	decode(t, call(t, broken, "revision_apply", map[string]any{"topic": "c", "revision": first.Revision, "learner_said": "yes"}), &applied)
	if !applied.Changed || applied.Approval.Via != core.ViaChat {
		t.Errorf("after a failed elicitation = %+v", applied)
	}
}

// TestAnAnswerCountsOnlyForTheQuestionAsked drives the latest protocol's
// round trip by hand: an answer with a forged state, or to a question about
// a Syllabus that has since changed, is asked again.
func TestAnAnswerCountsOnlyForTheQuestionAsked(t *testing.T) {
	ctx := context.Background()
	l := &learner{}
	session := connectWith(t, t.TempDir(), &mcp.ClientOptions{ElicitationHandler: l.handle,
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}}, nil)
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var first core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus", "syllabus": firstSyllabus}), &first)

	args := map[string]any{"topic": "c", "revision": first.Revision}
	asked, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "revision_apply", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	q, ok := asked.InputRequests["approval"].(*mcp.ElicitParams)
	if !asked.NeedsInput() || !ok || !strings.Contains(q.Message, "A first Syllabus") || asked.RequestState == "" {
		t.Fatalf("the first call should ask the learner: %+v", asked)
	}
	yes := mcp.InputResponseMap{"approval": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "approve"}}}

	forged, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "revision_apply", Arguments: args, InputResponses: yes,
		RequestState: first.Revision + ":forged"})
	if err != nil {
		t.Fatal(err)
	}
	if !forged.NeedsInput() {
		t.Errorf("an answer with a forged state was taken: %+v", forged.StructuredContent)
	}

	done, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "revision_apply", Arguments: args, InputResponses: yes,
		RequestState: asked.RequestState})
	if err != nil || done.NeedsInput() || done.IsError {
		t.Fatalf("the real answer: %+v, %v", done, err)
	}
	var applied core.RevisionApplied
	decode(t, done, &applied)
	if !applied.Changed || applied.Approval.Via != core.ViaElicitation || applied.Approval.Shown != q.Message {
		t.Errorf("applied = %+v", applied)
	}
}
