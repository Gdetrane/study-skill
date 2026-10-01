package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// testIDs are the IDs the tests' NewID makes. They number Events across the
// whole test process, so golden files show them as <id>.
var testIDs = regexp.MustCompile(`\bid\d+\b`)

// runStdin runs the command line with stdin, like run, and hides IDs.
func runStdin(t *testing.T, home, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, options(home, home))
	norm := func(s string) string {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			s = strings.ReplaceAll(s, resolved, "$STUDY_HOME")
		}
		return testIDs.ReplaceAllString(strings.ReplaceAll(s, home, "$STUDY_HOME"), "<id>")
	}
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

// sourceID reads the Source id from a source add --json result.
func sourceID(t *testing.T, home string, args ...string) string {
	t.Helper()
	r := run(t, home, args...)
	var out struct {
		Data struct {
			Source struct {
				ID string `json:"id"`
			} `json:"source"`
		} `json:"data"`
	}
	if r.code != cli.ExitOK || json.Unmarshal([]byte(r.stdout), &out) != nil || out.Data.Source.ID == "" {
		t.Fatalf("%v: exit %d, stdout %s, stderr %s", args, r.code, r.stdout, r.stderr)
	}
	return out.Data.Source.ID
}

func TestKnowledgeCommands(t *testing.T) {
	home := withTopic(t)
	book := filepath.Join(home, "Books", "strang_linear_algebra.pdf")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(book, []byte("%PDF-1.4 Strang"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(name string, code int, stdin string, args ...string) {
		t.Helper()
		r := runStdin(t, home, stdin, args...)
		if r.code != code {
			t.Errorf("%s: exit %d, want %d (stderr: %s)", name, r.code, code, r.stderr)
		}
		golden(t, name, r.stdout)
	}

	check("knowledge_base_set.json", cli.ExitOK, "",
		"topic", "update", "linear-algebra", "--knowledge-base", "notebooklm", "--notebook", "nb-42", "--json")
	check("knowledge_base_no_notebook.json", cli.ExitUsage, "",
		"topic", "update", "linear-algebra", "--knowledge-base", "notebooklm", "--json")
	check("knowledge_base_set.txt", cli.ExitOK, "", "topic", "update", "linear-algebra", "--knowledge-base", "none")

	// A relative path is resolved against the folder study started in.
	strang := sourceID(t, home, "source", "add", "linear-algebra", "--file", "Books/strang_linear_algebra.pdf", "--json")
	if !strings.HasPrefix(strang, "strang-linear-algebra.") {
		t.Errorf("Source id = %q", strang)
	}
	check("source_add_url.txt", cli.ExitOK, "", "source", "add", "linear-algebra",
		"--url", "https://math.mit.edu/~gs/linearalgebra/", "--title", "Strang's course page")
	check("source_add_duplicate.json", cli.ExitError, "", "source", "add", "linear-algebra", "--file", book, "--json")
	check("source_add_dry_run.json", cli.ExitOK, "", "source", "add", "linear-algebra",
		"--url", "https://example.com/notes", "--dry-run", "--json")
	check("source_update.json", cli.ExitOK, "", "source", "update", "linear-algebra", strang, "--notebooklm-id", "nlm-7", "--json")
	check("source_list.json", cli.ExitOK, "", "source", "list", "linear-algebra", "--json")
	check("source_list.txt", cli.ExitOK, "", "source", "list", "linear-algebra")

	check("evidence_record.json", cli.ExitOK, "", "evidence", "record", "linear-algebra", "--lesson", "elimination",
		"--source", strang, "--quote", "Elimination produces an upper triangular system.",
		"--location", "p. 46", "--location-from", "source", "--json")
	check("evidence_record_stdin.txt", cli.ExitOK, "The pivots are on the diagonal.\nThey must not be zero.\n",
		"evidence", "record", "linear-algebra", "--lesson", "pivots", "--source", strang, "--quote", "-")
	check("evidence_list.txt", cli.ExitOK, "", "evidence", "list", "linear-algebra")
	check("evidence_list_lesson.json", cli.ExitOK, "", "evidence", "list", "linear-algebra", "--lesson", "pivots", "--json")
	check("evidence_unknown_source.json", cli.ExitError, "", "evidence", "record", "linear-algebra",
		"--lesson", "pivots", "--source", "nothing.abc123", "--quote", "x", "--json")
	check("evidence_location_without_origin.json", cli.ExitUsage, "", "evidence", "record", "linear-algebra",
		"--lesson", "pivots", "--source", strang, "--quote", "x", "--location", "p. 1", "--json")
	check("source_no_subcommand.json", cli.ExitUsage, "", "source", "--json")
}
