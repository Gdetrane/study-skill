package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// eventID matches the test Event IDs, which depend on which tests ran first.
var eventID = regexp.MustCompile(`\bid\d{3,}\b`)

// withLesson returns a Study home holding Topic c with a one-Lesson Syllabus,
// the Lesson "answer" with its Check, and its exercise answered wrongly.
func withLesson(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	p, err := c.ProposeRevision(ctx, "c", core.RevisionSpec{Summary: "A first Syllabus", Syllabus: core.Syllabus{
		Milestones: []core.Milestone{{ID: "basics", Title: "Basics", Lessons: []core.SyllabusLesson{{ID: "answer", Title: "The answer"}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ApplyRevision(ctx, "c", p.Revision, core.Approval{Via: "chat", LearnerSaid: "yes"}, false); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"lessons/answer.md":          "---\ncheck:\n  - id: answer\n    describe: answer.txt holds 42\n    run: [sh, check.sh]\n---\n# The answer\n",
		"practice/answer/check.sh":   `test "$(cat answer.txt)" = 42 || { echo "answer.txt holds $(cat answer.txt)"; exit 1; }` + "\n",
		"practice/answer/answer.txt": "41\n",
	} {
		path := filepath.Join(home, "c", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestCheckCommand(t *testing.T) {
	home := withLesson(t)
	topic := filepath.Join(home, "c")
	check := func(dir string, args ...string) result {
		t.Helper()
		r := runIn(t, home, dir, append([]string{"check"}, args...)...)
		r.stdout = eventID.ReplaceAllString(r.stdout, "<id>")
		return r
	}

	failed := check(home, "answer", "--topic", "c", "--json")
	if failed.code != cli.ExitOK {
		t.Errorf("a failed Attempt: exit %d, want 0: the Attempt was recorded", failed.code)
	}
	golden(t, "check_failed.json", failed.stdout)
	golden(t, "check_failed.txt", check(topic, "answer").stdout)

	noTopic := check(home, "answer", "--json")
	if noTopic.code != cli.ExitUsage {
		t.Errorf("outside a Topic's folder without --topic: exit %d, want %d", noTopic.code, cli.ExitUsage)
	}
	golden(t, "check_no_topic.json", noTopic.stdout)
	golden(t, "check_unknown_lesson.json", check(home, "missing", "--topic", "c", "--json").stdout)

	if err := os.WriteFile(filepath.Join(topic, "practice", "answer", "answer.txt"), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	golden(t, "check_passed.txt", check(topic, "answer").stdout)
}

func TestStatusShowsWhereTheLearnerStopped(t *testing.T) {
	home := withLesson(t)
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPhase(context.Background(), "c", core.PhaseSpec{Lesson: "answer", Phase: core.PhaseFeedback,
		NextStep: "Fix the answer in answer.txt"}); err != nil {
		t.Fatal(err)
	}
	golden(t, "status_resume.txt", runIn(t, home, filepath.Join(home, "c"), "status").stdout)
}
