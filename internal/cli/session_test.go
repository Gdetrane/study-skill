package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var commitHash = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

func TestSessionCommands(t *testing.T) {
	gitHome := t.TempDir()
	t.Setenv("HOME", gitHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(gitHome, ".config"))
	if err := os.WriteFile(filepath.Join(gitHome, ".gitconfig"), []byte("[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := withLesson(t)
	topic := filepath.Join(home, "c")
	lesson := "---\ncheck:\n  - id: answer\n    describe: answer.txt holds 42\n    run: [sh, check.sh]\n" +
		"break_points:\n  - id: read\n    describe: The question is understood\n---\n# The answer\n"
	if err := os.WriteFile(filepath.Join(topic, "lessons", "answer.md"), []byte(lesson), 0o644); err != nil {
		t.Fatal(err)
	}
	session := func(args ...string) result {
		t.Helper()
		r := run(t, home, append([]string{"session"}, args...)...)
		r.stdout = eventID.ReplaceAllString(r.stdout, "<id>")
		r.stdout = commitHash.ReplaceAllString(r.stdout, "<commit>")
		return r
	}

	first := session("open", "c", "--energy", "full", "--json")
	if first.code != cli.ExitOK {
		t.Fatalf("session open: exit %d, %s", first.code, first.stdout)
	}
	golden(t, "session_open.json", first.stdout)
	golden(t, "session_break_point.txt", session("break-point", "c", "answer", "read", "--next-step", "Write a first answer").stdout)

	bad := session("close", "c", "--next-step", "Continue", "--json")
	if bad.code != cli.ExitUsage {
		t.Errorf("a Next step that is not an action: exit %d, want %d", bad.code, cli.ExitUsage)
	}
	golden(t, "session_close_not_an_action.json", bad.stdout)
	golden(t, "session_unknown_break_point.json", session("break-point", "c", "answer", "done", "--next-step", "Write it", "--json").stdout)

	// The learner works after the last Checkpoint, then closes the
	// terminal: the next Session shows what changed.
	if r := run(t, home, "checkpoint", "--topic", "c", "--role", "agent"); r.code != cli.ExitOK {
		t.Fatalf("checkpoint: %s", r.stderr)
	}
	if err := os.WriteFile(filepath.Join(topic, "practice", "answer", "answer.txt"), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := session("open", "c", "--energy", "half")
	golden(t, "session_open_unclosed.txt", second.stdout)

	raw := run(t, home, "session", "open", "c", "--dry-run", "--json")
	var env struct {
		Data struct {
			Unclosed []struct {
				ID string `json:"id"`
			} `json:"unclosed"`
		} `json:"data"`
	}
	// Both Sessions were left unclosed: the first, whose close was refused
	// above, and the second. Each gets its note, the older first.
	if err := json.Unmarshal([]byte(raw.stdout), &env); err != nil || len(env.Data.Unclosed) != 2 {
		t.Fatalf("dry run: %v, %s", err, raw.stdout)
	}
	if r := session("close", "c", "--session", env.Data.Unclosed[1].ID, "--next-step", "Read the question again"); r.code != cli.ExitOK {
		t.Errorf("closing the older Session: exit %d, %s", r.code, r.stderr)
	}
	closed := session("close", "c", "--session", env.Data.Unclosed[0].ID, "--next-step", "Run the Check on 42")
	if closed.code != cli.ExitOK {
		t.Errorf("closing the unclosed Session: exit %d, %s", closed.code, closed.stderr)
	}
	golden(t, "session_close.txt", closed.stdout)
	golden(t, "status_after_session.txt", runIn(t, home, topic, "status").stdout)
}

// runAt runs the command line like runIn, with the clock at now.
func runAt(t *testing.T, home string, now time.Time, args ...string) result {
	t.Helper()
	opts := options(home, home)
	opts.Now = func() time.Time { return now }
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, opts)
	out := strings.ReplaceAll(stdout.String(), home, "$STUDY_HOME")
	out = eventID.ReplaceAllString(out, "<id>")
	return result{code: code, stdout: out, stderr: stderr.String()}
}

// After a week away, with a Session left unclosed and many files changed:
// a welcome back, every unclosed Session, and the changes capped.
func TestSessionOpenAfterALongGap(t *testing.T) {
	setGitIdentity(t)
	home := withLesson(t)
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenSession(context.Background(), "c", core.SessionSpec{}); err != nil {
		t.Fatal(err)
	}
	if r := run(t, home, "checkpoint", "--topic", "c", "--role", "agent"); r.code != cli.ExitOK {
		t.Fatalf("checkpoint: %s", r.stderr)
	}
	for i := range 55 {
		path := filepath.Join(home, "c", "practice", "answer", fmt.Sprintf("try%02d.txt", i))
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden(t, "session_open_long_gap.txt", runAt(t, home, fixedNow.Add(8*24*time.Hour), "session", "open", "c", "--energy", "fumes").stdout)
}

// status shows the Learner profile, the Topic's additions and whether Cards
// are ready.
func TestStatusShowsGuidance(t *testing.T) {
	home := withLesson(t)
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddCard(context.Background(), "c", core.CardSpec{Prompt: "What is 6 × 7?", Answer: "42"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, "learner.md"), filepath.Join(home, "c", "learner.md")} {
		if err := os.WriteFile(path, []byte("Examples first.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	golden(t, "status_guidance.txt", runIn(t, home, filepath.Join(home, "c"), "status").stdout)
}

func setGitIdentity(t *testing.T) {
	t.Helper()
	gitHome := t.TempDir()
	t.Setenv("HOME", gitHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(gitHome, ".config"))
	config := "[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"
	if err := os.WriteFile(filepath.Join(gitHome, ".gitconfig"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The learner chooses a Focus after the suggestion: it is recorded on the
// open Session, and no other Session opens.
func TestSessionFocus(t *testing.T) {
	setGitIdentity(t)
	home := withLesson(t)
	var opened struct {
		Data struct {
			Session string `json:"session"`
		} `json:"data"`
	}
	r := run(t, home, "session", "open", "c", "--energy", "full", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &opened); err != nil || opened.Data.Session == "" {
		t.Fatalf("session open: %v, %s", err, r.stdout)
	}
	id := opened.Data.Session

	focused := run(t, home, "session", "open", "c", "--session", id, "--focus", "learn")
	if focused.code != cli.ExitOK {
		t.Fatalf("recording the Focus: exit %d, %s", focused.code, focused.stderr)
	}
	golden(t, "session_focus.txt", strings.ReplaceAll(focused.stdout, id, "<id>"))
	history, err := os.ReadFile(filepath.Join(home, "c", "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(history), `"type":"session.opened"`); got != 1 {
		t.Errorf("%d Sessions opened, want 1: recording the Focus must not open another", got)
	}
	if !strings.Contains(string(history), `"type":"session.focused"`) {
		t.Error("the Focus was not recorded")
	}

	if r := run(t, home, "session", "open", "c", "--session", id, "--json"); r.code != cli.ExitUsage {
		t.Errorf("--session without --focus: exit %d, want %d", r.code, cli.ExitUsage)
	}
	if r := run(t, home, "session", "close", "c", "--next-step", "Read the question again"); r.code != cli.ExitOK {
		t.Fatalf("session close: %s", r.stderr)
	}
	if r := run(t, home, "session", "open", "c", "--session", id, "--focus", "practice", "--json"); r.code != cli.ExitError ||
		!strings.Contains(r.stdout, "failed_precondition") {
		t.Errorf("a closed Session: exit %d, %s", r.code, r.stdout)
	}
}
