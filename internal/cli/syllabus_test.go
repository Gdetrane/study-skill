package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

const firstSyllabusTOML = `[[milestones]]
id = "systems"
title = "Systems of equations"
outcome = "Solve a 3x3 system by hand"
target = 2026-12-01

[[milestones.lessons]]
id = "elimination"
title = "Gaussian elimination"
hours = 2

[[milestones.lessons]]
id = "matrices"
title = "Matrices"
hours = 1.5

[[milestones]]
id = "spaces"
title = "Vector spaces"
priority = "if_time"

[[milestones.lessons]]
id = "subspaces"
title = "Subspaces"
`

const nextSyllabusJSON = `{"milestones": [
  {"id": "systems", "title": "Systems of equations", "outcome": "Solve a 3x3 system by hand", "target": "2026-12-01",
   "lessons": [
     {"id": "vectors", "title": "Vectors", "hours": 1},
     {"id": "elimination", "title": "Gaussian elimination", "hours": 2},
     {"id": "matrices", "title": "Matrices", "hours": 1.5, "skipped": true}]},
  {"id": "spaces", "title": "Vector spaces", "priority": "if_time",
   "lessons": [{"id": "subspaces", "title": "Subspaces"}]}]}`

// proposed proposes a Revision of the Linear algebra Topic and returns its id.
func proposed(t *testing.T, home string, args ...string) string {
	t.Helper()
	r := run(t, home, append([]string{"revision", "propose", "linear-algebra", "--json"}, args...)...)
	var out struct {
		Data struct {
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if r.code != cli.ExitOK || json.Unmarshal([]byte(r.stdout), &out) != nil || out.Data.Revision == "" {
		t.Fatalf("propose %v: exit %d, stdout %s, stderr %s", args, r.code, r.stdout, r.stderr)
	}
	return out.Data.Revision
}

func writeHomeFile(t *testing.T, home, name, content string) string {
	t.Helper()
	path := filepath.Join(home, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyllabusCommands(t *testing.T) {
	home := withTopic(t)
	writeHomeFile(t, home, "first.toml", firstSyllabusTOML)
	writeHomeFile(t, home, "next.json", nextSyllabusJSON)
	check := func(name string, code int, args ...string) {
		t.Helper()
		r := runStdin(t, home, "", args...)
		if r.code != code {
			t.Errorf("%s: exit %d, want %d (stderr: %s)", name, r.code, code, r.stderr)
		}
		golden(t, name, r.stdout)
	}

	check("syllabus_none.txt", cli.ExitOK, "syllabus")
	check("revision_propose_nothing.json", cli.ExitUsage, "revision", "propose", "linear-algebra", "--summary", "s", "--json")
	check("revision_propose_first.txt", cli.ExitOK, "revision", "propose", "linear-algebra",
		"--summary", "A first Syllabus", "--syllabus", "first.toml", "--dry-run")
	first := proposed(t, home, "--summary", "A first Syllabus", "--syllabus", "first.toml")

	// Without a terminal, the learner's words must come from the
	// conversation.
	check("revision_apply_no_terminal.json", cli.ExitUsage, "revision", "apply", "linear-algebra", first, "--json")
	check("revision_apply_chat.json", cli.ExitOK, "revision", "apply", "linear-algebra", first,
		"--learner-said", "Yes, that's the plan", "--json")
	check("syllabus.txt", cli.ExitOK, "syllabus", "linear-algebra")

	next := proposed(t, home, "--summary", "Start with vectors; skip matrices", "--syllabus", "next.json")
	check("syllabus_waiting.json", cli.ExitOK, "syllabus", "--json")
	check("revision_decline_chat.txt", cli.ExitOK, "revision", "decline", "linear-algebra", next,
		"--learner-said", "No, matrices matter to me")
	check("revision_apply_declined.json", cli.ExitError, "revision", "apply", "linear-algebra", next,
		"--learner-said", "Actually yes", "--json")

	// A hand edit is shown, then adopted through a Revision.
	path := filepath.Join(home, "linear-algebra", "syllabus.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, "linear-algebra/syllabus.toml", strings.Replace(string(data), `title = "Subspaces"`, `title = ""`, 1))
	check("syllabus_edited_invalid.txt", cli.ExitOK, "syllabus", "linear-algebra")
	check("revision_propose_invalid_file.json", cli.ExitUsage, "revision", "propose", "linear-algebra",
		"--summary", "Keep my edit", "--from-file", "--json")
	writeHomeFile(t, home, "linear-algebra/syllabus.toml", strings.Replace(string(data), `title = "Subspaces"`, `title = "Subspaces and spans"`, 1))
	adopt := proposed(t, home, "--summary", "Keep my edit", "--from-file")
	check("revision_apply_adopted.txt", cli.ExitOK, "revision", "apply", "linear-algebra", adopt, "--learner-said", "Yes, keep it")
	if r := run(t, home, "syllabus", "linear-algebra"); !strings.Contains(r.stdout, "Subspaces and spans") || strings.Contains(r.stdout, "outside Lamplight") {
		t.Errorf("after adopting the edit:\n%s", r.stdout)
	}
}
