package cli_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var fixedNow = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

type result struct {
	code           int
	stdout, stderr string
}

// run executes the study command line in process against home, with a fixed
// clock and predictable IDs, and replaces the Study home path in the output
// with $STUDY_HOME so golden files are stable.
func run(t *testing.T, home string, args ...string) result {
	t.Helper()
	n := 0
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, &stdout, &stderr, core.Options{
		Getenv: func(key string) string {
			return map[string]string{"STUDY_HOME": home, "HOME": home}[key]
		},
		Dir:   home,
		Now:   func() time.Time { return fixedNow },
		NewID: func() string { n++; return fmt.Sprintf("id%03d", n) },
	})
	norm := func(s string) string { return strings.ReplaceAll(s, home, "$STUDY_HOME") }
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

func TestJSONOutput(t *testing.T) {
	home := t.TempDir()
	steps := []struct {
		golden string
		args   []string
		code   int
	}{
		{"status_empty", []string{"status", "--json"}, cli.ExitOK},
		{"topic_create_dry_run", []string{"topic", "create", "--title", "C", "--dry-run", "--json"}, cli.ExitOK},
		{"topic_create", []string{"topic", "create", "--title", "Linear algebra", "--goal", "Solve systems by hand", "--json"}, cli.ExitOK},
		{"status_one_topic", []string{"--json"}, cli.ExitOK},
		{"topic_create_duplicate", []string{"topic", "create", "--title", "Linear algebra", "--json"}, cli.ExitError},
		{"topic_create_missing_title", []string{"topic", "create", "--json"}, cli.ExitUsage},
		{"unknown_flag", []string{"status", "--bogus", "--json"}, cli.ExitUsage},
		{"unexpected_argument", []string{"--json", "status", "extra"}, cli.ExitUsage},
	}
	for _, step := range steps {
		t.Run(step.golden, func(t *testing.T) {
			got := run(t, home, step.args...)
			if got.code != step.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, step.code, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("--json wrote to stderr: %q", got.stderr)
			}
			golden(t, step.golden+".json", got.stdout)
		})
	}
	if _, err := os.Stat(filepath.Join(home, "c")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created a Topic folder (err = %v)", err)
	}
}

func TestHumanOutput(t *testing.T) {
	home := t.TempDir()
	golden(t, "status_empty.txt", run(t, home).stdout)

	created := run(t, home, "topic", "create", "--title", "Linear algebra")
	if created.code != cli.ExitOK {
		t.Fatalf("topic create: exit %d, stderr %s", created.code, created.stderr)
	}
	golden(t, "topic_create.txt", created.stdout)
	golden(t, "status_one_topic.txt", run(t, home, "status").stdout)

	duplicate := run(t, home, "topic", "create", "--title", "Linear algebra")
	if duplicate.code != cli.ExitError || duplicate.stdout != "" || !strings.Contains(duplicate.stderr, "already exists") {
		t.Errorf("duplicate: exit %d, stdout %q, stderr %q", duplicate.code, duplicate.stdout, duplicate.stderr)
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
