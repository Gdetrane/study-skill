package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// The advice study topic remove prints, followed to the letter, brings the
// Topic back under its id, in a Study home whose path would need quoting:
// the command names the Topic and the removal's folder, never a path.
func TestTopicRemoveAdviceRestoresTheTopic(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Ada's study home")
	if r := run(t, home, "topic", "create", "--title", "Linear algebra", "--goal", "Solve systems by hand"); r.code != cli.ExitOK {
		t.Fatalf("topic create: %s", r.stderr)
	}
	removed := run(t, home, "topic", "remove", "linear-algebra")
	if removed.code != cli.ExitOK {
		t.Fatalf("topic remove: exit %d, %s", removed.code, removed.stderr)
	}
	advice := indentedCommand(t, removed.stdout)
	if want := "study topic restore linear-algebra --from " + removedFolder; advice != want {
		t.Fatalf("the restore command = %q, want %q", advice, want)
	}
	if r := run(t, home, "status"); !strings.Contains(r.stdout, "No Topics yet") {
		t.Fatalf("status after the removal:\n%s", r.stdout)
	}

	// While another Topic has the id, the command refuses and moves nothing.
	if r := run(t, home, "topic", "create", "--title", "Linear algebra", "--goal", "Pass the June exam"); r.code != cli.ExitOK {
		t.Fatalf("topic create: %s", r.stderr)
	}
	refused := run(t, home, strings.Fields(advice)[1:]...)
	if refused.code != cli.ExitError || !strings.Contains(refused.stderr, "another Topic is named linear-algebra now") {
		t.Fatalf("restoring over a Topic: exit %d, stdout %q, stderr %q", refused.code, refused.stdout, refused.stderr)
	}
	if nested := filepath.Join(home, "linear-algebra", removedFolder); exists(nested) || exists(filepath.Join(home, "linear-algebra", "linear-algebra")) {
		t.Fatal("the removed Topic was moved inside the one that took its id")
	}
	// Removing that one too leaves two removals of the id; the command still
	// restores the one it was printed for.
	second := run(t, home, "topic", "remove", "linear-algebra")
	if second.code != cli.ExitOK || indentedCommand(t, second.stdout) == advice {
		t.Fatalf("the second removal: exit %d\n%s%s", second.code, second.stdout, second.stderr)
	}
	if r := run(t, home, strings.Fields(advice)[1:]...); r.code != cli.ExitOK {
		t.Fatalf("%s: exit %d, %s", advice, r.code, r.stderr)
	}
	r := run(t, home, "topic", "update", "linear-algebra", "--title", "Linear algebra", "--json")
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, `"goal": "Solve systems by hand"`) {
		t.Errorf("the restored Topic: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// An empty --from is a mistake, such as a script's empty variable, not a way
// to ask for the newest removal: nothing is restored.
func TestAnEmptyFromRestoresNothing(t *testing.T) {
	home := withTwoRemovals(t)
	for _, args := range [][]string{
		{"topic", "restore", "linear-algebra", "--from", ""},
		{"topic", "restore", "linear-algebra", "--from="},
		{"topic", "restore", "linear-algebra", "--from", "", "--dry-run"},
	} {
		r := run(t, home, args...)
		if r.code != cli.ExitUsage || r.stdout != "" || !strings.Contains(r.stderr, "--from needs the name of a folder") {
			t.Errorf("study %s: exit %d, stdout %q, stderr %q", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
	}
	if exists(filepath.Join(home, "linear-algebra")) {
		t.Fatal("an empty --from restored a Topic")
	}
	// Left out, it is the newest removal that comes back.
	if r := run(t, home, "topic", "restore", "linear-algebra", "--json"); r.code != cli.ExitOK || !strings.Contains(r.stdout, removedFolder+"-") {
		t.Errorf("without --from: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// indentedCommand is the one indented line of study's output: a command to
// run.
func indentedCommand(t *testing.T, stdout string) string {
	t.Helper()
	var command string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "  ") {
			command = strings.TrimSpace(line)
		}
	}
	if !strings.HasPrefix(command, "study ") {
		t.Fatalf("no command in:\n%s", stdout)
	}
	return command
}
