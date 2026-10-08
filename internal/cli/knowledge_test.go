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

// testIDs are the IDs the tests' NewID makes, numbered per Study home (see
// options). runStdin shows them as <id>, so these golden files don't change
// when a test writes one more Event before the command it checks.
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

// dataID reads an id from a --json result: data.<field>.id.
func dataID(t *testing.T, home, field string, args ...string) string {
	t.Helper()
	r := run(t, home, args...)
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	var entity struct {
		ID string `json:"id"`
	}
	if r.code != cli.ExitOK || json.Unmarshal([]byte(r.stdout), &out) != nil ||
		json.Unmarshal(out.Data[field], &entity) != nil || entity.ID == "" {
		t.Fatalf("%v: exit %d, stdout %s, stderr %s", args, r.code, r.stdout, r.stderr)
	}
	return entity.ID
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

	// none is the only kind this version has.
	check("knowledge_base_set.json", cli.ExitOK, "", "topic", "update", "linear-algebra", "--knowledge-base", "none", "--json")
	check("knowledge_base_unknown_kind.json", cli.ExitUsage, "",
		"topic", "update", "linear-algebra", "--knowledge-base", "rag", "--json")

	// A relative path is resolved against the folder study started in.
	strang := dataID(t, home, "source", "source", "add", "linear-algebra", "--file", "Books/strang_linear_algebra.pdf", "--json")
	if !strings.HasPrefix(strang, "strang-linear-algebra.") {
		t.Errorf("Source id = %q", strang)
	}
	check("source_add_url.txt", cli.ExitOK, "", "source", "add", "linear-algebra",
		"--url", "https://math.mit.edu/~gs/linearalgebra/", "--title", "Strang's course page")
	check("source_add_duplicate.json", cli.ExitError, "", "source", "add", "linear-algebra", "--file", book, "--json")
	check("source_add_dry_run.json", cli.ExitOK, "", "source", "add", "linear-algebra",
		"--url", "https://example.com/notes", "--dry-run", "--json")
	check("source_update.json", cli.ExitOK, "", "source", "update", "linear-algebra", strang,
		"--title", "Introduction to Linear Algebra", "--json")
	check("source_list.json", cli.ExitOK, "", "source", "list", "linear-algebra", "--json")

	// Hand edits, a file this computer no longer has, and a Knowledge base
	// kind this version does not know, as a newer version would record it.
	settings := filepath.Join(home, "linear-algebra", "topic.toml")
	toml, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(string(toml), `kind = "none"`, `kind = "rag"`, 1)
	if newer == string(toml) {
		t.Fatalf("topic.toml has no Knowledge base kind to replace:\n%s", toml)
	}
	if err := os.WriteFile(settings, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := os.OpenFile(filepath.Join(home, "linear-algebra", "sources.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lines.WriteString(`{"id":"by-hand.abc123","kind":"url","title":"By hand","url":"https://example.com/hand"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	lines.Close()
	if err := os.Rename(book, book+".away"); err != nil {
		t.Fatal(err)
	}
	check("source_list.txt", cli.ExitOK, "", "source", "list", "linear-algebra")
	if err := os.Rename(book+".away", book); err != nil {
		t.Fatal(err)
	}
	check("knowledge_base_set.txt", cli.ExitOK, "", "topic", "update", "linear-algebra", "--knowledge-base", "none")

	check("evidence_record.json", cli.ExitOK, "", "evidence", "record", "linear-algebra", "--lesson", "elimination",
		"--source", strang, "--quote", "Elimination produces an upper triangular system.",
		"--location", "p. 46", "--location-from", "source", "--json")
	check("evidence_record_stdin.txt", cli.ExitOK, "The pivots are on the diagonal.\nThey must not be zero.\n",
		"evidence", "record", "linear-algebra", "--lesson", "pivots", "--source", strang, "--quote", "-")
	mistake := dataID(t, home, "evidence", "evidence", "record", "linear-algebra", "--lesson", "pivots",
		"--source", strang, "--quote", "A misquote.", "--json")
	check("evidence_retract.txt", cli.ExitOK, "", "evidence", "retract", "linear-algebra", mistake)
	check("evidence_retract_again.json", cli.ExitOK, "", "evidence", "retract", "linear-algebra", mistake, "--json")
	check("evidence_list.txt", cli.ExitOK, "", "evidence", "list", "linear-algebra")
	check("evidence_list_all.txt", cli.ExitOK, "", "evidence", "list", "linear-algebra", "--all")
	check("evidence_list_lesson.json", cli.ExitOK, "", "evidence", "list", "linear-algebra", "--lesson", "pivots", "--json")
	check("evidence_unknown_source.json", cli.ExitError, "", "evidence", "record", "linear-algebra",
		"--lesson", "pivots", "--source", "nothing.abc123", "--quote", "x", "--json")
	check("evidence_location_without_origin.json", cli.ExitUsage, "", "evidence", "record", "linear-algebra",
		"--lesson", "pivots", "--source", strang, "--quote", "x", "--location", "p. 1", "--json")
	check("source_no_subcommand.json", cli.ExitUsage, "", "source", "--json")
	check("status_knowledge_base.txt", cli.ExitOK, "", "status")
}
