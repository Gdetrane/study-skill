package mcpserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// checkTopic gives Topic c a one-Lesson Syllabus whose Check has a run
// criterion and a rubric item. Running the Check creates the file marker, so
// a test can tell whether anything ran it.
func checkTopic(t *testing.T, session *mcp.ClientSession, home string) (marker string) {
	t.Helper()
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	var p core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus",
		"syllabus": firstSyllabus}), &p)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": p.Revision, "learner_said": "yes"})
	marker = filepath.Join(home, "ran")
	for name, content := range map[string]string{
		"lessons/answer.md": "---\ncheck:\n  - id: answer\n    run: [touch, " + marker + "]\n" +
			"  - id: explained\n    rubric: The notes say why\n  - id: hidden\n    held_out: [touch, " + marker + "]\n---\n",
		"practice/answer/answer.txt":   "42\n",
		".heldout/answer/expected.txt": "42\n",
	} {
		path := filepath.Join(home, "c", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call(t, session, "phase_set", map[string]any{"topic": "c", "lesson": "answer", "phase": "practicing"})
	return marker
}

func TestRubricRecordAndCheckResults(t *testing.T) {
	withGitIdentity(t)
	home := t.TempDir()
	session := connect(t, home)
	checkTopic(t, session, home)

	var graded core.RubricGraded
	decode(t, call(t, session, "rubric_record", map[string]any{"topic": "c", "lesson": "answer",
		"criterion": "explained", "grade": "met", "note": "The notes say why"}), &graded)
	if !graded.Changed || graded.Grade.Grade != core.GradeMet || graded.Grade.Event == "" {
		t.Errorf("rubric_record = %+v", graded)
	}
	var res core.CheckResults
	decode(t, call(t, session, "check_results", map[string]any{"topic": "c", "lesson": "answer"}), &res)
	if len(res.Rubric) != 1 || res.Rubric[0].Grade == nil || !res.Rubric[0].Current || len(res.HeldOut) != 1 ||
		res.CanComplete {
		t.Errorf("check_results = %+v", res)
	}

	bad, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "rubric_record",
		Arguments: map[string]any{"topic": "c", "lesson": "answer", "criterion": "answer", "grade": "met"}})
	if err != nil || !bad.IsError || !strings.Contains(text(bad), "invalid_argument") {
		t.Errorf("grading a run criterion: %v, %s", err, text(bad))
	}
}

// TestNoToolRunsACheck calls every tool the server offers, with arguments
// aimed at the Lesson whose Check would leave a marker file, and checks that
// nothing ran it: Checks run only through study check (ADR-0009).
func TestNoToolRunsACheck(t *testing.T) {
	withGitIdentity(t)
	home := t.TempDir()
	session := connect(t, home)
	marker := checkTopic(t, session, home)

	values := map[string]any{"topic": "c", "lesson": "answer", "criterion": "answer", "phase": "feedback",
		"role": "learner", "grade": "met", "next_step": "Fix the answer", "energy": "full", "focus": "practice",
		"title": "C", "id": "c", "query": "answer", "summary": "Run it", "revision": "x", "learner_said": "yes"}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		args := map[string]any{}
		if schema, ok := tool.InputSchema.(map[string]any); ok {
			props, _ := schema["properties"].(map[string]any)
			for name := range props {
				if v, ok := values[name]; ok {
					args[name] = v
				}
			}
		}
		// Errors are fine: only whether the Check ran matters.
		_, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("calling %s ran the Check", tool.Name)
		}
	}
	if len(tools.Tools) < 20 {
		t.Errorf("only %d tools were called", len(tools.Tools))
	}
	// The marker is real: running the Check the one supported way makes it.
	c, err := core.Open(core.Options{Getenv: func(key string) string {
		return map[string]string{"STUDY_HOME": home, "HOME": home}[key]
	}, Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunCheck(context.Background(), "c", "answer", core.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("study check did not create the marker, so this test proves nothing: %v", err)
	}
}
