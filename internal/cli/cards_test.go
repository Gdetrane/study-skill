package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// cardIDs matches the random suffix of Card IDs, which golden files replace.
var cardIDs = regexp.MustCompile(`\.[a-z2-7]{8}\b`)

func normCards(s string) string { return cardIDs.ReplaceAllString(s, ".<id>") }

// runWithInput runs the command line with stdin, as a script would type it.
func runWithInput(t *testing.T, home, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, options(home, home))
	norm := func(s string) string { return normCards(strings.ReplaceAll(s, home, "$STUDY_HOME")) }
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

// addCard adds a Card through the command line and returns its ID.
func addCard(t *testing.T, home, prompt, answer string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := []string{"card", "add", "linear-algebra", "--prompt", prompt, "--answer", answer, "--json"}
	if code := cli.Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, options(home, home)); code != cli.ExitOK {
		t.Fatalf("card add: exit %d, %s %s", code, stdout.String(), stderr.String())
	}
	var env struct {
		Data core.CardChange `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.Card.ID
}

func TestCardCommands(t *testing.T) {
	home := withTopic(t)
	steps := []struct {
		golden string
		args   func(first, second string) []string
		code   int
	}{
		{"card_add.json", func(_, _ string) []string {
			return []string{"card", "add", "linear-algebra", "--prompt", "What does a pivot column hold?",
				"--answer", "A leading 1", "--json"}
		}, cli.ExitOK},
		{"card_add.txt", func(_, _ string) []string {
			return []string{"card", "add", "linear-algebra", "--prompt", "What is a free variable?",
				"--answer", "One whose column has no pivot"}
		}, cli.ExitOK},
		{"card_list.txt", func(_, _ string) []string { return []string{"card", "list", "linear-algebra"} }, cli.ExitOK},
		{"card_edit.json", func(first, _ string) []string {
			return []string{"card", "edit", "linear-algebra", first, "--answer", "The leading 1 of its row", "--json"}
		}, cli.ExitOK},
		{"card_suspend.txt", func(first, _ string) []string {
			return []string{"card", "suspend", "linear-algebra", first}
		}, cli.ExitOK},
		{"card_due.json", func(_, _ string) []string {
			return []string{"card", "due", "linear-algebra", "--energy", "fumes", "--json"}
		}, cli.ExitOK},
		{"card_flag.txt", func(_, second string) []string {
			return []string{"card", "flag", "linear-algebra", second, "--note", "too vague"}
		}, cli.ExitOK},
		{"card_delete_dry_run.txt", func(first, _ string) []string {
			return []string{"card", "delete", "linear-algebra", first, "--dry-run"}
		}, cli.ExitOK},
		{"card_review.json", func(_, second string) []string {
			return []string{"card", "review", "linear-algebra", second, "--draft", "keep", "--rating", "good", "--json"}
		}, cli.ExitOK},
		{"card_edit_unknown.json", func(_, _ string) []string {
			return []string{"card", "edit", "linear-algebra", "explore.nope", "--prompt", "P", "--json"}
		}, cli.ExitError},
		{"card_add_no_prompt.json", func(_, _ string) []string {
			return []string{"card", "add", "linear-algebra", "--answer", "A", "--json"}
		}, cli.ExitUsage},
		{"card_due_bad_energy.json", func(_, _ string) []string {
			return []string{"card", "due", "linear-algebra", "--energy", "sleepy", "--json"}
		}, cli.ExitUsage},
		{"review_json.json", func(_, _ string) []string { return []string{"review", "--json"} }, cli.ExitUsage},
	}
	var first, second string
	for i, step := range steps {
		got := runWithInput(t, home, "", step.args(first, second)...)
		if got.code != step.code {
			t.Errorf("%s: exit %d, want %d (stdout %s, stderr %s)", step.golden, got.code, step.code, got.stdout, got.stderr)
		}
		golden(t, step.golden, got.stdout)
		if i < 2 {
			list := listCards(t, home)
			first = list[0].ID
			if len(list) > 1 {
				second = list[1].ID
			}
		}
	}
}

func listCards(t *testing.T, home string) []core.Card {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"card", "list", "linear-algebra", "--json"}, strings.NewReader(""),
		&stdout, &stderr, options(home, home)); code != cli.ExitOK {
		t.Fatalf("card list: exit %d, %s", code, stderr.String())
	}
	var env struct {
		Data core.CardList `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.Cards
}

// TestReviewSession drives study review with scripted input, one key per
// line, as it reads input that is not a terminal.
func TestReviewSession(t *testing.T) {
	home := withTopic(t)
	kept := addCard(t, home, "What does a pivot column hold?", "A leading 1")
	edited := addCard(t, home, "What is a free variable?", "One whose column has no pivot")
	dropped := addCard(t, home, "Who invented matrices?", "Many people")

	script := strings.Join([]string{
		"A leading one", "k", "3", // type an answer to compare and reveal, keep, good
		"f", "the prompt is vague", // flag at recall
		"", "e", // reveal, edit
		"What is a free‮ variable?", "", // a control character: refused, ask again
		"What is a free variable in a linear system?", "", "y", "2", // the new prompt, the answer kept, save, hard
		"", "d", "n", // reveal, drop, but not confirmed
		"d", "y", // drop, confirmed
	}, "\n") + "\n"
	got := runWithInput(t, home, script, "review", "linear-algebra")
	if got.code != cli.ExitOK {
		t.Fatalf("review: exit %d, stderr %s", got.code, got.stderr)
	}
	golden(t, "review_session.txt", got.stdout)

	cards := listCards(t, home)
	if len(cards) != 2 {
		t.Fatalf("after the session: %+v, want the kept and the edited Card", cards)
	}
	byID := map[string]core.Card{}
	for _, c := range cards {
		byID[c.ID] = c
	}
	if c := byID[kept]; c.Draft || c.Due.IsZero() {
		t.Errorf("the kept Card = %+v, want it reviewed and scheduled", c)
	}
	if c := byID[edited]; c.Draft || c.Flagged || c.Prompt != "What is a free variable in a linear system?" ||
		c.Answer != "One whose column has no pivot" {
		t.Errorf("the edited Card = %+v, want the new prompt, the old answer, and its flag settled by the edit", c)
	}
	if _, ok := byID[dropped]; ok {
		t.Error("the dropped Card is still listed")
	}

	// Nothing is left to review, and stopping straight away records nothing.
	if again := runWithInput(t, home, "", "review", "linear-algebra"); !strings.Contains(again.stdout, "Nothing to review now.") {
		t.Errorf("a second session: %s", again.stdout)
	}
	addCard(t, home, "What is a basis?", "A linearly independent spanning set")
	stopped := runWithInput(t, home, "q\n", "review")
	if stopped.code != cli.ExitOK || !strings.Contains(stopped.stdout, "Stopped. Nothing was recorded.") ||
		!strings.Contains(stopped.stdout, "Reviewing linear-algebra (it is the Topic you worked on most recently)") {
		t.Errorf("stopping at once: exit %d\n%s", stopped.code, stopped.stdout)
	}
	// The end of the input stops the session too.
	if eof := runWithInput(t, home, "", "review", "linear-algebra"); eof.code != cli.ExitOK || !strings.Contains(eof.stdout, "Stopped.") {
		t.Errorf("at the end of the input: exit %d\n%s", eof.code, eof.stdout)
	}
}
