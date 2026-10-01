package cli_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// taskIDs matches the random suffix of Task ids.
var taskIDs = regexp.MustCompile(`\.[a-z2-7]{6}\b`)

func normTasks(s string) string { return taskIDs.ReplaceAllString(s, ".<id>") }

// withPlannedTopic is the Linear algebra Topic with its first Syllabus
// approved: Systems of equations (3.5 h, target 1 Dec 2026), then Vector
// spaces, if time allows.
func withPlannedTopic(t *testing.T) string {
	t.Helper()
	home := withTopic(t)
	writeHomeFile(t, home, "first.toml", firstSyllabusTOML)
	rev := proposed(t, home, "--summary", "A first Syllabus", "--syllabus", "first.toml")
	if r := run(t, home, "revision", "apply", "linear-algebra", rev, "--learner-said", "yes"); r.code != cli.ExitOK {
		t.Fatalf("apply: %s", r.stderr)
	}
	return home
}

func TestPlanCommands(t *testing.T) {
	home := withPlannedTopic(t)
	check := func(name string, code int, args ...string) result {
		t.Helper()
		r := run(t, home, args...)
		if r.code != code {
			t.Errorf("%s: exit %d, want %d (stderr: %s)", name, r.code, code, r.stderr)
		}
		golden(t, name, normTasks(r.stdout))
		return r
	}

	check("plan_pace_invalid.json", cli.ExitUsage, "topic", "update", "linear-algebra", "--pace", "lots", "--json")
	check("plan_pace_order.json", cli.ExitUsage, "topic", "update", "linear-algebra",
		"--pace", "3@2026-11-16", "--pace", "10@2026-11-01", "--json")
	check("plan_update_dry_run.txt", cli.ExitOK, "topic", "update", "linear-algebra",
		"--pace", "1", "--deadline", "2026-12-15", "--dry-run")
	// At 1 h/week the 3.5 h of Systems of equations end 25 Oct; Vector
	// spaces needs estimates.
	check("plan_update.json", cli.ExitOK, "topic", "update", "linear-algebra",
		"--pace", "1", "--pace", "0.5@2026-10-15", "--deadline", "2026-12-15", "--new-cards-per-day", "5", "--json")

	added := check("plan_task_add.json", cli.ExitOK, "task", "add", "linear-algebra", "Book", "the", "exam", "--by", "2026-11-20", "--json")
	var out struct {
		Data struct {
			AddedTasks []struct {
				ID string `json:"id"`
			} `json:"added_tasks"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(added.stdout), &out); err != nil || len(out.Data.AddedTasks) != 1 {
		t.Fatalf("task add: %v, %s", err, added.stdout)
	}
	task := out.Data.AddedTasks[0].ID
	check("plan_task_add_later.txt", cli.ExitOK, "task", "add", "linear-algebra", "Ask for feedback", "--after", "systems")
	check("plan_status.txt", cli.ExitOK, "status")
	check("plan_task_list.txt", cli.ExitOK, "task", "list", "linear-algebra")
	check("plan_task_done.txt", cli.ExitOK, "task", "done", "linear-algebra", task)
	check("plan_task_done_again.json", cli.ExitOK, "task", "done", "linear-algebra", task, "--json")
	check("plan_task_list_all.txt", cli.ExitOK, "task", "list", "linear-algebra", "--all")
	check("plan_task_unknown.json", cli.ExitError, "task", "done", "linear-algebra", "nothing.abcdef", "--json")
	check("plan_syllabus.txt", cli.ExitOK, "syllabus", "linear-algebra")

	// A Pace that misses the target date offers a Triage.
	check("plan_triage.txt", cli.ExitOK, "topic", "update", "linear-algebra", "--pace", "0.25")
	check("plan_status_triage.txt", cli.ExitOK, "status")

	check("plan_pause.txt", cli.ExitOK, "topic", "update", "linear-algebra", "--state", "paused")
	check("plan_status_paused.json", cli.ExitOK, "status", "--json")
	check("plan_card_due_paused.txt", cli.ExitOK, "card", "due", "linear-algebra")
	check("plan_task_remove.txt", cli.ExitOK, "task", "remove", "linear-algebra", task)
}
