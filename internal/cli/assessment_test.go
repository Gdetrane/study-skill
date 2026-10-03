package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
  "level": "beginner",
  "request": "placement"
}`

const milestoneJSON = `{
  "kind": "milestone",
  "milestone": "basics",
  "items": [
    {"area": "pointers", "outcome": "partly", "note": "reads *p, not &x"},
    {"area": "slices", "outcome": "correct"}
  ],
  "summary": "Pointers still need work"
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
	golden(t, "assessment_record_two_objects.json",
		expect(runWithInput(t, home, placementJSON+placementJSON, "assessment", "record", "c", "--file", "-", "--json"),
			cli.ExitUsage).stdout)

	golden(t, "topic_update_level.txt", expect(run(t, home, "topic", "update", "c", "--level", "advanced",
		"--approach", "project"), cli.ExitOK).stdout)
	golden(t, "topic_update_bad_approach.json", expect(run(t, home, "topic", "update", "c", "--approach", "lectures",
		"--json"), cli.ExitUsage).stdout)
	// A milestone Assessment with weak areas proposes a Revision.
	golden(t, "assessment_record_milestone.txt",
		expect(runWithInput(t, home, milestoneJSON, "assessment", "record", "c", "--file", "-"), cli.ExitOK).stdout)
	golden(t, "assessment_list.txt", expect(run(t, home, "assessment", "list", "c"), cli.ExitOK).stdout)
	golden(t, "assessment_list.json", expect(run(t, home, "assessment", "list", "c", "--json"), cli.ExitOK).stdout)

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
		"--requested-by", "agent", "--note", "Showed how to read check.sh"), cli.ExitOK).stdout)
	golden(t, "hint_record.json", expect(run(t, home, "hint", "record", "answer", "--topic", "c", "--request", "r1",
		"--requested-by", "learner", "--json"), cli.ExitOK).stdout)
	golden(t, "hint_record_bad_kind.json", expect(run(t, home, "hint", "record", "answer", "--topic", "c",
		"--kind", "answer", "--requested-by", "agent", "--json"), cli.ExitUsage).stdout)
	golden(t, "hint_record_no_asker.json", expect(run(t, home, "hint", "record", "answer", "--topic", "c",
		"--json"), cli.ExitUsage).stdout)

	// The signals are for agents: JSON only, and not in help.
	golden(t, "signals.txt", expect(run(t, home, "signals", "c"), cli.ExitOK).stdout)
	golden(t, "signals.json", expect(run(t, home, "signals", "c", "--json"), cli.ExitOK).stdout)
	if help := expect(run(t, home, "--help"), cli.ExitOK).stdout; strings.Contains(help, "signals") {
		t.Errorf("study --help lists signals:\n%s", help)
	}
	golden(t, "status_with_level.txt", expect(run(t, home, "status"), cli.ExitOK).stdout)
}
