package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

const placementJSON = `{
  "kind": "placement",
  "items": [
    {"area": "variables", "question": "What does x := 1 do?", "outcome": "correct"},
    {"area": "pointers", "outcome": "incorrect", "note": "confused * and &"},
    {"area": "goroutines", "outcome": "not_reached"}
  ],
  "summary": "Knows variables; pointers need work",
  "minutes": 13,
  "level": "beginner"
}`

func TestAssessmentHintAndSignalsCommands(t *testing.T) {
	// study check takes a Checkpoint, which needs a git identity: CI has
	// none of its own.
	setGitIdentity(t)
	home := withLesson(t)
	if err := os.WriteFile(filepath.Join(home, "placement.json"), []byte(placementJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	expect := func(r result, code int) result {
		t.Helper()
		if r.code != code {
			t.Fatalf("exit %d, want %d; stdout %s; stderr %s", r.code, code, r.stdout, r.stderr)
		}
		return r
	}

	golden(t, "assessment_record_dry_run.txt",
		expect(run(t, home, "assessment", "record", "c", "--file", "placement.json", "--dry-run"), cli.ExitOK).stdout)
	golden(t, "assessment_record.json",
		expect(run(t, home, "assessment", "record", "c", "--file", "placement.json", "--json"), cli.ExitOK).stdout)
	golden(t, "assessment_record_again.txt",
		expect(runWithInput(t, home, placementJSON, "assessment", "record", "c", "--file", "-"), cli.ExitOK).stdout)
	golden(t, "assessment_record_no_file.json",
		expect(run(t, home, "assessment", "record", "c", "--json"), cli.ExitUsage).stdout)
	golden(t, "assessment_record_unknown_field.json",
		expect(runWithInput(t, home, `{"kind":"placement","itens":[]}`, "assessment", "record", "c", "--file", "-", "--json"),
			cli.ExitUsage).stdout)

	golden(t, "topic_update_level.txt", expect(run(t, home, "topic", "update", "c", "--level", "advanced"), cli.ExitOK).stdout)
	golden(t, "assessment_list.txt", expect(run(t, home, "assessment", "list", "c"), cli.ExitOK).stdout)

	// One measured Attempt that fails, then hints.
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPhase(context.Background(), "c", core.PhaseSpec{Lesson: "answer", Phase: core.PhasePracticing}); err != nil {
		t.Fatal(err)
	}
	expect(run(t, home, "check", "answer", "--topic", "c"), cli.ExitOK)
	golden(t, "hint_record.txt", expect(run(t, home, "hint", "record", "answer", "--topic", "c", "--kind", "step",
		"--note", "Showed how to read check.sh"), cli.ExitOK).stdout)
	golden(t, "hint_record.json", expect(run(t, home, "hint", "record", "answer", "--topic", "c", "--request", "r1",
		"--json"), cli.ExitOK).stdout)
	golden(t, "hint_record_bad_kind.json", expect(run(t, home, "hint", "record", "answer", "--topic", "c",
		"--kind", "answer", "--json"), cli.ExitUsage).stdout)

	golden(t, "signals.txt", expect(run(t, home, "signals", "c"), cli.ExitOK).stdout)
	golden(t, "signals.json", expect(run(t, home, "signals", "c", "--json"), cli.ExitOK).stdout)
	golden(t, "status_with_level.txt", expect(run(t, home, "status"), cli.ExitOK).stdout)
}
