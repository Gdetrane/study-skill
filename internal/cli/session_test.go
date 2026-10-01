package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
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
			Unclosed struct {
				ID string `json:"id"`
			} `json:"unclosed"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw.stdout), &env); err != nil || env.Data.Unclosed.ID == "" {
		t.Fatalf("dry run: %v, %s", err, raw.stdout)
	}
	closed := session("close", "c", "--session", env.Data.Unclosed.ID, "--next-step", "Run the Check on 42")
	if closed.code != cli.ExitOK {
		t.Errorf("closing the unclosed Session: exit %d, %s", closed.code, closed.stderr)
	}
	golden(t, "session_close.txt", closed.stdout)
	golden(t, "status_after_session.txt", runIn(t, home, topic, "status").stdout)
}
