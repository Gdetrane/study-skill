//go:build linux

package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// TestTheLearnerAnswersOnTheTerminal runs study revision apply and decline
// with stdin on a terminal, where study asks the learner directly and
// records that, with what it showed them.
func TestTheLearnerAnswersOnTheTerminal(t *testing.T) {
	home := withTopic(t)
	writeHomeFile(t, home, "first.toml", firstSyllabusTOML)
	writeHomeFile(t, home, "next.json", nextSyllabusJSON)
	history := filepath.Join(home, "linear-algebra", "history.jsonl")

	answer := func(typed string, args ...string) (result, string) {
		t.Helper()
		master, slave := openPTY(t)
		if _, err := master.WriteString(typed); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := cli.Run(context.Background(), args, slave, &stdout, &stderr, options(home, home))
		data, err := os.ReadFile(history)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		return result{code: code, stdout: stdout.String(), stderr: stderr.String()}, lines[len(lines)-1]
	}

	first := proposed(t, home, "--summary", "A first Syllabus", "--syllabus", "first.toml")
	r, last := answer("y\nLooks right\n", "revision", "apply", "linear-algebra", first)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "Applied Revision") {
		t.Fatalf("approving: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "A first Syllabus") || !strings.Contains(r.stderr, `Lesson 1.1 "Gaussian elimination"`) {
		t.Errorf("the learner was not shown the change: %q", r.stderr)
	}
	if !strings.Contains(last, `"via":"terminal"`) || !strings.Contains(last, `"learner_said":"Looks right"`) ||
		!strings.Contains(last, `"shown":"Lamplight asks: apply this change to the Syllabus of linear-algebra?`) {
		t.Errorf("the History does not record the terminal approval: %s", last)
	}

	next := proposed(t, home, "--summary", "Start with vectors", "--syllabus", "next.json")
	r, _ = answer("\n", "revision", "apply", "linear-algebra", next)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "still waiting") {
		t.Errorf("deciding later: exit %d, stdout %q", r.code, r.stdout)
	}
	if out := run(t, home, "syllabus", "linear-algebra", "--json"); !strings.Contains(out.stdout, `"revision": "`) {
		t.Errorf("deciding later recorded something: %s", out.stdout)
	}

	r, last = answer("n\nnot this week\n", "revision", "apply", "linear-algebra", next)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "declined") ||
		!strings.Contains(last, `"type":"revision.declined"`) || !strings.Contains(last, `"via":"terminal"`) {
		t.Errorf("declining at the prompt: exit %d, stdout %q, last Event %s", r.code, r.stdout, last)
	}

	third := proposed(t, home, "--summary", "Start with vectors again", "--syllabus", "next.json")
	r, last = answer("y\n\n", "revision", "decline", "linear-algebra", third)
	if r.code != cli.ExitOK || !strings.Contains(last, `"type":"revision.declined"`) || !strings.Contains(last, `"via":"terminal"`) {
		t.Errorf("study revision decline on a terminal: exit %d, stdout %q, last Event %s", r.code, r.stdout, last)
	}

	// Answering again asks nothing and reports the answer recorded, as
	// with --learner-said.
	r, _ = answer("y\n\n", "revision", "apply", "linear-algebra", first)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "was already applied") || r.stderr != "" {
		t.Errorf("applying again: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	r, _ = answer("y\n\n", "revision", "decline", "linear-algebra", third)
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, "had already declined") || r.stderr != "" {
		t.Errorf("declining again: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}

	// With --json, study never asks, even on a terminal.
	fourth := proposed(t, home, "--summary", "Start with vectors once more", "--syllabus", "next.json")
	r, _ = answer("y\n\n", "revision", "apply", "linear-algebra", fourth, "--json")
	if r.code != cli.ExitUsage || !strings.Contains(r.stdout, `"code": "usage"`) || r.stderr != "" {
		t.Errorf("--json on a terminal: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}

	// A Revision that cannot be applied is not asked about.
	path := filepath.Join(home, "linear-algebra", "syllabus.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, "linear-algebra/syllabus.toml", strings.Replace(string(data), `title = "Matrices"`, `title = "Matrices!"`, 1))
	r, _ = answer("y\n\n", "revision", "apply", "linear-algebra", fourth)
	if r.code == cli.ExitOK || strings.Contains(r.stderr, "Lamplight asks") || !strings.Contains(r.stderr, "outside Lamplight") {
		t.Errorf("asking about a Revision that cannot be applied: exit %d, stderr %q", r.code, r.stderr)
	}
}
