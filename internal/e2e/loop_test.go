// Package e2e drives Lamplight end to end the way an agent does: over MCP,
// plus study check in the agent's own shell.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/mcpserver"
)

// clock is the learner's clock: it moves on a second at every reading, and
// the test moves it on by days.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(time.Second)
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// agent is a scripted fake agent. It talks to Lamplight only as a real agent
// can: MCP tools, study check in its shell, and the files it writes.
type agent struct {
	t     *testing.T
	home  string
	clock *clock
	// crashAt, when set, makes the next write crash at that point.
	crashMu sync.Mutex
	crashAt string
	session *mcp.ClientSession
}

func (a *agent) options() core.Options {
	return core.Options{
		Getenv: func(key string) string {
			if key == "STUDY_HOME" {
				return a.home
			}
			return os.Getenv(key)
		},
		Dir: a.home,
		Now: a.clock.now,
		Crash: func(point string) error {
			a.crashMu.Lock()
			defer a.crashMu.Unlock()
			if point == a.crashAt {
				a.crashAt = ""
				return errors.New("simulated crash at " + point)
			}
			return nil
		},
	}
}

// connect starts a new MCP session against a fresh server, as a harness
// does at the start of each conversation.
func (a *agent) connect() {
	a.t.Helper()
	ctx := context.Background()
	c, err := core.Open(a.options())
	if err != nil {
		a.t.Fatal(err)
	}
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(c, "e2e", nil).Connect(ctx, serverSide, nil); err != nil {
		a.t.Fatal(err)
	}
	a.session, err = mcp.NewClient(&mcp.Implementation{Name: "scripted-agent", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		a.t.Fatal(err)
	}
}

func (a *agent) disconnect() {
	if err := a.session.Close(); err != nil {
		a.t.Fatal(err)
	}
}

// call calls a tool and decodes its result into out.
func (a *agent) call(tool string, args map[string]any, out any) {
	a.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		a.t.Fatalf("%s returned a tool error: %s", tool, text(res))
	}
	if out == nil {
		return
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		a.t.Fatal(err)
	}
	// Decode into a zero value: json.Unmarshal would keep fields of a reused
	// value that the new result omits.
	v := reflect.ValueOf(out).Elem()
	v.Set(reflect.Zero(v.Type()))
	if err := json.Unmarshal(data, out); err != nil {
		a.t.Fatalf("%s: %s: %v", tool, data, err)
	}
}

// refused calls a tool that must fail with code.
func (a *agent) refused(tool string, args map[string]any, code core.ErrorCode) string {
	a.t.Helper()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", tool, err)
	}
	if !res.IsError || !strings.HasPrefix(text(res), string(code)+":") {
		a.t.Fatalf("%s: IsError=%v, %q; want a %s error", tool, res.IsError, text(res), code)
	}
	return text(res)
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// shellCheck runs study check in the agent's shell, as ADR-0009 requires.
func (a *agent) shellCheck(lesson string) core.Attempt {
	a.t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"check", lesson, "--topic", "c", "--json"},
		strings.NewReader(""), &stdout, &stderr, a.options())
	if code != cli.ExitOK {
		a.t.Fatalf("study check: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	var env struct {
		OK   bool         `json:"ok"`
		Data core.Attempt `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || !env.OK {
		a.t.Fatalf("study check printed %s (%v)", stdout.String(), err)
	}
	return env.Data
}

// write writes a file in the Topic, as the agent or the learner does.
func (a *agent) write(rel, content string) {
	a.t.Helper()
	path := filepath.Join(a.home, "c", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		a.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		a.t.Fatal(err)
	}
}

func (a *agent) git(args ...string) string {
	a.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", filepath.Join(a.home, "c")}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		a.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// withGitIdentity gives git an identity through a temporary HOME, as a
// learner's global configuration would, and hides the developer's own.
func withGitIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config := "[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

const lesson = `---
check:
  - id: answer
    describe: answer.txt holds the answer to everything
    run: [sh, check.sh]
---
# The answer

Write the answer to everything in answer.txt.
`

const checkScript = `answer=$(cat answer.txt)
test "$answer" = 42 || { echo "answer.txt holds $answer, not the answer"; exit 1; }
`

// TestTheLearnerLoop is the thin learner loop through every layer: a
// Syllabus approved, a Session, teaching, an Attempt that fails, feedback, a
// fix, a passing Attempt, a crash while completing the Lesson, the next
// day's resume, a Card Review and a Next step. It asserts on the History and
// the replayed state, never on transcripts.
func TestTheLearnerLoop(t *testing.T) {
	withGitIdentity(t)
	a := &agent{t: t, home: t.TempDir(), clock: &clock{at: time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)}}
	a.connect()

	// Day 1. The Topic and its Syllabus, approved by the learner.
	var status core.Status
	a.call("status", map[string]any{}, &status)
	if len(status.Topics) != 0 {
		t.Fatalf("a new Study home has Topics: %+v", status.Topics)
	}
	a.call("topic_create", map[string]any{"title": "C", "id": "c", "goal": "Write small C programs"}, nil)
	var proposal core.RevisionProposal
	a.call("revision_propose", map[string]any{"topic": "c", "summary": "One Milestone with one Lesson to start",
		"syllabus": map[string]any{"milestones": []any{map[string]any{
			"id": "basics", "title": "Basics", "outcome": "Answer questions with a program", "priority": "must",
			"lessons": []any{map[string]any{"id": "answer", "title": "The answer", "hours": 1}},
		}}}}, &proposal)
	a.refused("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "teaching"}, core.CodeFailedPrecondition)
	a.call("revision_apply", map[string]any{"topic": "c", "revision": proposal.Revision, "learner_said": "Looks good, let's go"}, nil)

	// A Session: teaching, then the learner practices.
	var opened core.SessionOpened
	a.call("session_open", map[string]any{"topic": "c", "energy": "full", "focus": "learn"}, &opened)
	if opened.Resume.Lesson != "answer" || opened.Unclosed != nil {
		t.Fatalf("first Session = %+v", opened)
	}
	a.call("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "teaching"}, nil)
	a.write("lessons/answer.md", lesson)
	a.write("practice/answer/check.sh", checkScript)
	var phase core.PhaseResult
	a.call("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "practicing"}, &phase)
	if phase.Checkpoint == nil || !phase.Checkpoint.Committed {
		t.Fatalf("practicing = %+v, want a Checkpoint of the agent's turn", phase)
	}

	// An Attempt that fails, and feedback.
	a.write("practice/answer/answer.txt", "41\n")
	a.call("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "feedback"}, &phase)
	if phase.Checkpoint == nil || !phase.Checkpoint.Committed {
		t.Fatalf("feedback = %+v, want a Checkpoint of the learner's turn", phase)
	}
	first := a.shellCheck("answer")
	if first.Outcome != core.OutcomeFailed || !strings.Contains(first.Criteria[0].Output, "holds 41") {
		t.Fatalf("first Attempt = %+v", first)
	}
	a.refused("lesson_complete", map[string]any{"topic": "c", "lesson": "answer"}, core.CodeFailedPrecondition)
	a.call("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "practicing",
		"next_step": "Fix the answer in answer.txt"}, nil)

	// The fix, and a passing Attempt.
	a.write("practice/answer/answer.txt", "42\n")
	a.call("phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "feedback"}, nil)
	second := a.shellCheck("answer")
	if second.Outcome != core.OutcomePassed {
		t.Fatalf("second Attempt = %+v", second)
	}
	var results core.CheckResults
	a.call("check_results", map[string]any{"topic": "c", "lesson": "answer"}, &results)
	if !results.CanComplete || len(results.Attempts) != 2 {
		t.Fatalf("check results = %+v", results)
	}

	// The process dies while completing the Lesson: the Event is in the
	// History, the Cards and the Checkpoint are not.
	a.crashMu.Lock()
	a.crashAt = core.CrashAfterEvent
	a.crashMu.Unlock()
	res, err := a.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "lesson_complete", Arguments: map[string]any{
		"topic": "c", "lesson": "answer",
		"cards": []any{map[string]any{"prompt": "What does answer.txt hold when the Check passes?", "answer": "42"}},
	}})
	if err != nil || !res.IsError {
		t.Fatalf("lesson_complete survived the crash: %v %+v", err, res)
	}
	a.disconnect()
	if _, err := os.Stat(filepath.Join(a.home, "c", "cards.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the crash came after the Cards were written (err = %v)", err)
	}

	// Day 2. A new conversation resumes where the learner stopped.
	a.clock.advance(24 * time.Hour)
	a.connect()
	a.call("status", map[string]any{}, &status)
	topic := status.Topics[0]
	if status.ActiveTopic == nil || status.ActiveTopic.ID != "c" || !hasFlag(topic.Flags, core.FlagInterruptedWrite) {
		t.Fatalf("status after the crash = %+v", status)
	}
	if topic.Resume == nil || !topic.Resume.SyllabusDone || topic.Resume.OpenSession == nil {
		t.Fatalf("resume after the crash = %+v", topic.Resume)
	}
	a.call("session_open", map[string]any{"topic": "c", "energy": "half", "focus": "reviews"}, &opened)
	if opened.Unclosed == nil {
		t.Errorf("the second Session does not report the first, left unclosed")
	}
	var done core.LessonCompletion
	a.call("lesson_complete", map[string]any{"topic": "c", "lesson": "answer"}, &done)
	if done.Changed || done.Attempt != second.ID || len(done.Cards) != 1 || done.Checkpoint == nil {
		t.Fatalf("completing again = %+v, want the recorded completion and its Checkpoint", done)
	}

	// The draft Card's first Review.
	var due core.DueCards
	a.call("due_cards", map[string]any{"topic": "c"}, &due)
	if len(due.Cards) != 1 || !due.Cards[0].Draft || due.Cards[0].ID != done.Cards[0].ID {
		t.Fatalf("due Cards = %+v", due)
	}
	var reviewed core.ReviewResult
	a.call("review_record", map[string]any{"topic": "c", "card": due.Cards[0].ID, "rating": "good", "draft": "keep"}, &reviewed)
	if reviewed.Card.Draft || !reviewed.Card.Due.After(a.clock.now()) {
		t.Fatalf("review = %+v", reviewed)
	}
	a.call("due_cards", map[string]any{"topic": "c"}, &due)
	if len(due.Cards) != 0 {
		t.Fatalf("right after its Review, the Card is due again: %+v", due)
	}
	a.call("session_close", map[string]any{"topic": "c", "next_step": "Plan the next Milestone with the learner",
		"context": "The Basics Milestone is done."}, nil)
	a.disconnect()

	// The History holds the whole story, once each.
	types := historyTypes(t, filepath.Join(a.home, "c", "history.jsonl"))
	want := []string{"topic.created", "revision.proposed", "revision.applied", "session.opened",
		"phase.set", "phase.set", "phase.set", "attempt.recorded", "phase.set", "phase.set", "attempt.recorded",
		"lesson.completed", "session.opened", "review.recorded", "session.closed"}
	if !slices.Equal(types, want) {
		t.Errorf("History =\n%v\nwant\n%v", types, want)
	}

	// Checkpoints at every turn switch keep the learner's work apart.
	log := strings.Split(a.git("log", "--reverse", "--format=%s"), "\n")
	wantLog := []string{"[agent] answer: practicing", "[learner] answer: feedback", "[agent] answer: practicing",
		"[learner] answer: feedback", "[agent] answer: completed"}
	if !slices.Equal(log, wantLog) {
		t.Errorf("Checkpoints =\n%v\nwant\n%v", log, wantLog)
	}
	learnerFix := a.git("diff", "--name-only", "HEAD~2", "HEAD~1")
	if !strings.Contains(learnerFix, "practice/answer/answer.txt") || strings.Contains(learnerFix, "lessons/") {
		t.Errorf("the learner's fix Checkpoint changed:\n%s", learnerFix)
	}
	if !strings.Contains(a.git("show", "HEAD:cards.jsonl"), "What does answer.txt hold") {
		t.Errorf("the completion Checkpoint lacks the Card")
	}

	// The replayed state: the Lesson is done, the Next step is the last
	// one recorded, nothing is flagged, and the same History gives the same
	// state whatever the order of its lines.
	a.connect()
	defer a.disconnect()
	a.call("status", map[string]any{}, &status)
	topic = status.Topics[0]
	if len(topic.Flags) != 0 || topic.Resume == nil || topic.Resume.OpenSession != nil ||
		topic.Resume.NextStep == nil || topic.Resume.NextStep.Step != "Plan the next Milestone with the learner" {
		t.Errorf("final status = %+v", topic)
	}
	var syllabus core.SyllabusView
	a.call("syllabus", map[string]any{"topic": "c"}, &syllabus)
	if syllabus.Milestones[0].Lessons[0].Status != core.LessonDone {
		t.Errorf("Syllabus = %+v", syllabus)
	}
	assertSameStateInAnyOrder(t, a)
}

func hasFlag(flags []core.Flag, kind string) bool {
	for _, f := range flags {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func historyTypes(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("a History line is not an Event: %s", line)
		}
		types = append(types, ev.Type)
	}
	return types
}

// assertSameStateInAnyOrder copies the Topic with its History's lines
// reversed, as a union merge could leave them, and compares what replaying
// both gives.
func assertSameStateInAnyOrder(t *testing.T, a *agent) {
	t.Helper()
	ctx := context.Background()
	other := t.TempDir()
	if out, err := exec.Command("cp", "-R", filepath.Join(a.home, "c"), filepath.Join(other, "c")).CombinedOutput(); err != nil {
		t.Fatalf("copying the Topic: %v\n%s", err, out)
	}
	history := filepath.Join(other, "c", "history.jsonl")
	data, err := os.ReadFile(history)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	slices.Reverse(lines)
	if err := os.WriteFile(history, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := func(home string) string {
		c, err := core.Open(core.Options{Getenv: func(k string) string {
			if k == "STUDY_HOME" {
				return home
			}
			return os.Getenv(k)
		}, Dir: home, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }})
		if err != nil {
			t.Fatal(err)
		}
		syllabus, err := c.SyllabusOf(ctx, "c")
		if err != nil {
			t.Fatal(err)
		}
		results, err := c.CheckResultsOf(ctx, "c", "answer")
		if err != nil {
			t.Fatal(err)
		}
		due, err := c.DueCardsOf(ctx, "c", 0)
		if err != nil {
			t.Fatal(err)
		}
		status, err := c.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		status.StudyHome, status.Topics[0].Path = "", ""
		data, err := json.MarshalIndent([]any{syllabus, results, due, status.Topics[0]}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got, want := state(other), state(a.home); got != want {
		t.Errorf("the History in reverse replays to\n%s\nwant\n%s", got, want)
	}
}
