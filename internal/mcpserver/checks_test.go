package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// checkTopic gives Topic c, in the Study home home, a one-Lesson Syllabus
// whose Check has a run criterion, a rubric item and a held_out criterion,
// and a break point. Each command of the Check adds a line to the file
// marker, so a test can tell whether, and how often, anything ran it.
func checkTopic(t *testing.T, session *mcp.ClientSession, home string) (marker string) {
	t.Helper()
	return checkTopicIn(t, session, home, home)
}

// checkTopicIn is checkTopic for a Study home that is not the folder the
// marker is kept in.
//
// created, when given, is called once the Topic exists and before any other
// tool is called on it.
func checkTopicIn(t *testing.T, session *mcp.ClientSession, home, studyHome string, created ...func()) (marker string) {
	t.Helper()
	call(t, session, "topic_create", map[string]any{"title": "C", "id": "c"})
	for _, f := range created {
		f()
	}
	var p core.RevisionProposal
	decode(t, call(t, session, "revision_propose", map[string]any{"topic": "c", "summary": "A first Syllabus",
		"syllabus": firstSyllabus}), &p)
	call(t, session, "revision_apply", map[string]any{"topic": "c", "revision": p.Revision, "learner_said": "yes"})
	marker = filepath.Join(home, "ran")
	for name, content := range map[string]string{
		"lessons/answer.md": "---\ncheck:\n  - id: answer\n    run: [sh, -c, \"echo run >> '" + marker + "'\"]\n" +
			"  - id: explained\n    rubric: The notes say why\n" +
			"  - id: hidden\n    held_out: [sh, -c, \"echo held_out >> '" + marker + "'\"]\n" +
			"break_points:\n  - id: read\n    describe: The question is understood\n---\n",
		"practice/answer/answer.txt":   "42\n",
		".heldout/answer/expected.txt": "42\n",
	} {
		path := filepath.Join(studyHome, "c", filepath.FromSlash(name))
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

// TestNoToolRunsACheck calls every tool the server offers, each with valid
// input so that it does its work, in the order a study session would, and
// checks that none ran the Check: Checks run only through study check
// (ADR-0009).
func TestNoToolRunsACheck(t *testing.T) { everyTool(t, nil) }

// everyTool is the session of TestNoToolRunsACheck. prepare, if not nil,
// changes the server before the session starts: a test with something to
// check about every tool watches the calls of this one session, which fails
// when the server offers a tool it does not call. It returns the Study home
// and a core opened on it.
func everyTool(t *testing.T, prepare func(*mcp.Server)) (string, *core.Core) {
	home := t.TempDir()
	return home, everyToolOn(t, home, home, everyToolOptions{prepare: prepare})
}

// everyToolOptions says what the session of everyToolOn is given besides
// its folders.
type everyToolOptions struct {
	// prepare, if not nil, changes the server before the session starts.
	prepare func(*mcp.Server)
	// after, if not nil, is called with each tool's name once the tool has
	// answered, for a test to look at what the call left behind, and with
	// "study check" once the Lesson's Check was run through the core, as
	// study check runs it.
	after func(tool string)
	// knowledgeBase is the Knowledge base topic_update gives the Topic: the
	// kind none when it is nil.
	knowledgeBase map[string]any
	// plugins says how the server's core starts Knowledge base plugins.
	plugins core.PluginOptions
}

// everyToolOn is everyTool on a computer whose home folder is home and whose
// Study home is studyHome, which may be another folder: the Knowledge base
// plugin registry is read only when it is.
func everyToolOn(t *testing.T, home, studyHome string, options everyToolOptions) *core.Core {
	t.Helper()
	ctx := context.Background()
	withGitIdentity(t)
	prepare, after := options.prepare, options.after
	if after == nil {
		after = func(string) {}
	}
	knowledgeBase := options.knowledgeBase
	if knowledgeBase == nil {
		knowledgeBase = map[string]any{"kind": "none"}
	}
	book := filepath.Join(home, "Books", "The_C_Programming_Language.pdf")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) string {
		return map[string]string{"STUDY_HOME": studyHome, "HOME": home}[key]
	}
	c, err := core.Open(core.Options{Getenv: getenv, Dir: home, Plugins: options.plugins})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.BuildLibrary(ctx, "Books"); err != nil {
		t.Fatal(err)
	}
	session := connectOn(t, getenv, home, nil, nil, prepare, options.plugins)
	// A Topic that is to name a plugin names it, and has a Source the plugin
	// has not indexed, from before the first tool is called on it: every
	// tool is then called on a Topic a plugin could be asked to index for.
	// Both are done through the core here, not by a tool, so that no tool
	// is left out for coming before the ones that do it.
	marker := checkTopicIn(t, session, home, studyHome, func() {
		name, _ := options.knowledgeBase["plugin"].(string)
		if name == "" {
			return
		}
		if _, err := c.UpdateTopic(ctx, "c", core.TopicChanges{KnowledgeBase: &core.KnowledgeBase{Kind: core.KnowledgeBasePlugin,
			Plugin: name}}); err != nil {
			t.Fatal(err)
		}
		notes := filepath.Join(home, "Books", "notes.txt")
		if err := os.WriteFile(notes, []byte("Notes the plugin has not indexed.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := c.AddSource(ctx, core.SourceSpec{Topic: "c", File: notes}); err != nil {
			t.Fatal(err)
		}
	})
	runs := func() int {
		data, err := os.ReadFile(marker)
		if errors.Is(err, fs.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
	// checkTopic called these, and call fails on a tool error.
	called := map[string]bool{"topic_create": true, "revision_propose": true, "revision_apply": true, "phase_set": true}
	for _, tool := range []string{"topic_create", "revision_propose", "revision_apply", "phase_set"} {
		after(tool)
	}
	// ids holds what earlier calls returned, for later ones to name.
	ids := map[string]string{}
	use := func(tool string, args map[string]any, keep func(json.RawMessage)) {
		t.Helper()
		res := call(t, session, tool, args)
		called[tool] = true
		if keep != nil {
			data, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			keep(data)
		}
		if n := runs(); n != 0 {
			t.Fatalf("calling %s ran the Check %d times", tool, n)
		}
		after(tool)
	}
	// field keeps the string at path in a result as ids[name], taking the
	// first element of any list on the way.
	field := func(name string, path ...string) func(json.RawMessage) {
		return func(data json.RawMessage) {
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			for _, key := range path {
				if list, ok := v.([]any); ok && len(list) > 0 {
					v = list[0]
				}
				m, _ := v.(map[string]any)
				v = m[key]
			}
			s, ok := v.(string)
			if !ok || s == "" {
				t.Fatalf("no %s at %v in %s", name, path, data)
			}
			ids[name] = s
		}
	}
	with := func(base map[string]any, kv ...any) map[string]any {
		out := maps.Clone(base)
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	topic := map[string]any{"topic": "c"}
	lesson := map[string]any{"topic": "c", "lesson": "answer"}

	use("status", map[string]any{}, nil)
	use("syllabus", topic, nil)
	use("topic_update", with(topic, "goal", "Answer the question", "knowledge_base", knowledgeBase,
		"add_tasks", []any{map[string]any{"title": "Book the exam"}}), field("task", "added_tasks", "id"))
	use("tasks", topic, nil)
	use("task_done", with(topic, "task", ids["task"]), nil)
	use("checkpoint", with(topic, "role", "agent"), nil)
	use("session_open", with(topic, "energy", "full"), nil)
	use("break_point_reached", with(lesson, "break_point", "read", "next_step", "Write a first answer"), nil)
	use("card_add", with(lesson, "prompt", "What is the answer?", "answer", "42"), field("card", "card", "id"))
	use("cards", topic, nil)
	use("due_cards", topic, nil)
	use("review_record", with(topic, "card", ids["card"], "draft", "keep", "rating", "good", "request", "review-1"), nil)
	use("card_flag", with(topic, "card", ids["card"], "note", "the answer needs a unit"), nil)
	use("card_edit", with(topic, "card", ids["card"], "answer", "Forty-two"), nil)
	use("card_suspend", with(topic, "card", ids["card"]), nil)
	use("card_delete", with(topic, "card", ids["card"]), nil)
	use("library_search", map[string]any{"query": "C"}, nil)
	use("source_add", with(topic, "file", book), field("source", "source", "id"))
	use("sources", topic, nil)
	use("source_update", with(topic, "source", ids["source"], "title", "K&R"), nil)
	use("evidence_record", with(lesson, "source", ids["source"], "quote", "The answer is 42.",
		"location", "p. 1", "location_from", "source"), field("evidence", "evidence", "id"))
	use("evidence", topic, nil)
	use("evidence_retract", with(topic, "evidence", ids["evidence"]), nil)
	use("revision_propose", with(topic, "summary", "Ask the question first", "syllabus", secondSyllabus),
		field("revision", "revision"))
	use("revision_decline", with(topic, "revision", ids["revision"], "learner_said", "no, keep the order"), nil)
	use("rubric_record", with(lesson, "criterion", "explained", "grade", "met", "note", "It says why",
		"looked_at", []any{"answer.txt"}), nil)
	use("check_results", lesson, nil)
	use("lesson", lesson, nil)
	use("history", with(topic, "limit", 5, "type", "card."), nil)
	use("hint_record", with(lesson, "kind", "nudge", "requested_by", "learner", "note", "What does check.sh compare?"), nil)
	use("signals", topic, nil)
	use("assessment_record", with(topic, "kind", "placement", "summary", "Knows the basics", "level", "beginner",
		"items", []any{map[string]any{"area": "answers", "outcome": "correct"}}), nil)
	use("phase_set", with(lesson, "phase", "feedback"), nil)

	// A damaged History line raises a Flag to dismiss.
	f, err := os.OpenFile(filepath.Join(studyHome, "c", "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"format":1,"id":"zz1","time":"2026-10-01T10:00:00Z","wall":"2026-10-01T10:00:00Z",` +
		`"type":"card.reviewed","data":{}}` + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	use("status", map[string]any{}, field("flag", "topics", "flags", "id"))
	use("flag_dismiss", with(topic, "flag", ids["flag"]), nil)
	use("session_close", with(topic, "next_step", "Run the Check"), nil)
	use("topic_create", map[string]any{"title": "D"}, nil)

	// The marker is real: running the Check the one supported way adds a
	// line for each command.
	if _, err := c.RunCheck(ctx, "c", "answer", core.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := runs(); n != 2 {
		t.Fatalf("study check ran %d commands, want 2, so this test proves nothing", n)
	}
	after("study check")
	var done core.LessonCompletion
	decode(t, call(t, session, "lesson_complete", lesson), &done)
	called["lesson_complete"] = true
	if n := runs(); n != 2 || !done.Changed {
		t.Errorf("lesson_complete: %d runs, completion %+v", n, done)
	}
	after("lesson_complete")

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if !called[tool.Name] {
			t.Errorf("%s was not called: add it to this test", tool.Name)
		}
	}
	return c
}

// TestTheServerCannotRunACheck reads the server's own source: it must not
// call RunCheck, nor start any program. Checks run only through study check
// (ADR-0009).
func TestTheServerCannotRunACheck(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == "os/exec" || p == "syscall" {
				t.Errorf("%s imports %s", name, p)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "RunCheck", "StartProcess":
					t.Errorf("%s calls %s", name, sel.Sel.Name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files were checked")
	}
}
