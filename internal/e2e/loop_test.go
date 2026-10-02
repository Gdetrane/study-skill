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
	a.call("review_record", map[string]any{"topic": "c", "card": due.Cards[0].ID, "rating": "good", "draft": "keep",
		"request": "review-1"}, &reviewed)
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
		"phase.set", "phase.set", "checkpoint.taken", "phase.set", "checkpoint.taken", "attempt.recorded",
		"phase.set", "checkpoint.taken", "phase.set", "checkpoint.taken", "attempt.recorded",
		// The crash interrupted the completion before its Checkpoint; the
		// next day's retry takes it.
		"lesson.completed", "session.opened", "checkpoint.taken",
		"review.recorded", "session.closed"}
	if !slices.Equal(types, want) {
		t.Errorf("History =\n%v\nwant\n%v", types, want)
	}

	// The Attempts and the completion record what they relied on, and
	// never a criterion's output.
	var attempts []attemptPayload
	var completion completionPayload
	for _, ev := range historyEvents(t, filepath.Join(a.home, "c", "history.jsonl")) {
		switch ev.Type {
		case "attempt.recorded":
			if bytes.Contains(ev.Data, []byte("output")) || bytes.Contains(ev.Data, []byte("holds 41")) {
				t.Errorf("an Attempt recorded output: %s", ev.Data)
			}
			var p attemptPayload
			decodePayload(t, ev, &p)
			p.ID = ev.ID
			attempts = append(attempts, p)
		case "lesson.completed":
			decodePayload(t, ev, &completion)
		}
	}
	if len(attempts) != 2 || attempts[0].Outcome != "failed" || attempts[1].Outcome != "passed" ||
		attempts[0].Criteria[0].ExitCode != 1 || attempts[1].Criteria[0].ExitCode != 0 ||
		attempts[0].Snapshot == attempts[1].Snapshot || attempts[0].CheckVersion != attempts[1].CheckVersion ||
		!strings.HasPrefix(attempts[1].Snapshot, "sha256:") || attempts[1].ID != second.ID {
		t.Errorf("Attempts = %+v", attempts)
	}
	if completion.Lesson != "answer" || completion.Attempt != second.ID || completion.Snapshot != second.Snapshot ||
		completion.CheckVersion != second.CheckVersion || completion.ShownCheck != second.CheckVersion ||
		completion.TurnEnded != "agent" || len(completion.Cards) != 1 || completion.Cards[0].ID != done.Cards[0].ID {
		t.Errorf("completion = %+v", completion)
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
	// one recorded, and nothing is flagged.
	a.connect()
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
	a.call("checkpoint", map[string]any{"topic": "c", "role": "agent", "message": "end of day 2"}, nil)
	a.disconnect()
	assertTwoMachinesMerge(t, a)
}

type attemptPayload struct {
	ID           string `json:"-"`
	Lesson       string `json:"lesson"`
	CheckVersion string `json:"check_version"`
	Snapshot     string `json:"snapshot"`
	Outcome      string `json:"outcome"`
	Criteria     []struct {
		ID       string `json:"id"`
		Outcome  string `json:"outcome"`
		ExitCode int    `json:"exit_code"`
	} `json:"criteria"`
}

type completionPayload struct {
	Lesson       string `json:"lesson"`
	Attempt      string `json:"attempt"`
	CheckVersion string `json:"check_version"`
	ShownCheck   string `json:"shown_check"`
	Snapshot     string `json:"snapshot"`
	TurnEnded    string `json:"turn_ended"`
	Cards        []struct {
		ID     string `json:"id"`
		Prompt string `json:"prompt"`
	} `json:"cards"`
}

type historyEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func historyEvents(t *testing.T, path string) []historyEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []historyEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev historyEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("a History line is not an Event: %s", line)
		}
		events = append(events, ev)
	}
	return events
}

func decodePayload(t *testing.T, ev historyEvent, v any) {
	t.Helper()
	if err := json.Unmarshal(ev.Data, v); err != nil {
		t.Fatalf("%s payload %s: %v", ev.Type, ev.Data, err)
	}
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

// assertTwoMachinesMerge plays the v2.0 sync contract with real git: the
// Topic is cloned to a second machine, each machine has a Session and takes
// a Checkpoint, and each pulls the other's work, the History merging by
// union. Both machines must then replay to the same state, with every
// Event of both and nothing flagged.
func assertTwoMachinesMerge(t *testing.T, a *agent) {
	t.Helper()
	ctx := context.Background()
	b := &agent{t: t, home: t.TempDir(), clock: a.clock}
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitIn(b.home, "clone", "-q", filepath.Join(a.home, "c"), "c")

	for _, m := range []*agent{a, b} {
		c, err := core.Open(m.options())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.OpenSession(ctx, "c", core.SessionSpec{Focus: core.FocusExplore}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.CloseSession(ctx, "c", core.CloseSpec{NextStep: "Read about the question on " + m.home}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Checkpoint(ctx, core.CheckpointSpec{Topic: "c", Role: "agent", Message: "explore"}); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(filepath.Join(a.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(b.home, "c"), "main")
	gitIn(filepath.Join(b.home, "c"), "pull", "-q", "--no-rebase", "--no-edit", filepath.Join(a.home, "c"), "main")

	state := func(m *agent) (string, []string) {
		c, err := core.Open(m.options())
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
		due, err := c.DueCardsOf(ctx, "c", core.DueQuery{})
		if err != nil {
			t.Fatal(err)
		}
		status, err := c.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		topic := status.Topics[0]
		topic.Path = ""
		if len(topic.Flags) != 0 {
			t.Errorf("flags after the merge on %s: %+v", m.home, topic.Flags)
		}
		data, err := json.MarshalIndent([]any{syllabus, results, due, topic}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, ev := range historyEvents(t, filepath.Join(m.home, "c", "history.jsonl")) {
			types = append(types, ev.Type)
		}
		return string(data), types
	}
	stateA, typesA := state(a)
	stateB, typesB := state(b)
	if stateA != stateB {
		t.Errorf("after merging, machine A replays to\n%s\nand machine B to\n%s", stateA, stateB)
	}
	closed := 0
	for _, typ := range typesA {
		if typ == "session.closed" {
			closed++
		}
	}
	if closed != 3 || len(typesA) != len(typesB) {
		t.Errorf("the merged History has %d session.closed Events (want both machines', 3 in all), "+
			"and %d lines on A, %d on B", closed, len(typesA), len(typesB))
	}
}
