package cli_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

var fixedNow = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

type result struct {
	code           int
	stdout, stderr string
}

func options(home, dir string) core.Options {
	var n atomic.Int64
	return core.Options{
		Getenv: func(key string) string {
			return map[string]string{"STUDY_HOME": home, "HOME": home}[key]
		},
		Dir:   dir,
		Now:   func() time.Time { return fixedNow },
		NewID: func() string { return fmt.Sprintf("id%03d", n.Add(1)) },
	}
}

// run executes the study command line in process against home, started in
// the Study home itself.
func run(t *testing.T, home string, args ...string) result {
	t.Helper()
	return runIn(t, home, home, args...)
}

// runIn executes the study command line started in dir, with a fixed clock
// and predictable IDs, and replaces the Study home path in the output with
// $STUDY_HOME so golden files are stable.
func runIn(t *testing.T, home, dir string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, options(home, dir))
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolved = home
	}
	norm := func(s string) string {
		s = strings.ReplaceAll(s, resolved, "$STUDY_HOME")
		return strings.ReplaceAll(s, home, "$STUDY_HOME")
	}
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

// withTopic returns a Study home holding one Topic, "Linear algebra".
func withTopic(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if r := run(t, home, "topic", "create", "--title", "Linear algebra", "--goal", "Solve systems by hand"); r.code != cli.ExitOK {
		t.Fatalf("setup: exit %d, stderr %s", r.code, r.stderr)
	}
	return home
}

func emptyHome(t *testing.T) string { return t.TempDir() }

func TestJSONOutput(t *testing.T) {
	for _, tc := range []struct {
		golden string
		home   func(*testing.T) string
		args   []string
		code   int
	}{
		{"status_empty", emptyHome, []string{"status", "--json"}, cli.ExitOK},
		{"topic_create_dry_run", emptyHome, []string{"topic", "create", "--title", "C", "--dry-run", "--json"}, cli.ExitOK},
		{"topic_create", emptyHome, []string{"topic", "create", "--title", "Linear algebra", "--goal", "Solve systems by hand", "--json"}, cli.ExitOK},
		{"status_one_topic", withTopic, []string{"--json"}, cli.ExitOK},
		{"topic_create_duplicate", withTopic, []string{"topic", "create", "--title", "Linear algebra", "--json"}, cli.ExitError},
		{"topic_create_missing_title", emptyHome, []string{"topic", "create", "--json"}, cli.ExitUsage},
		{"unknown_flag", emptyHome, []string{"status", "--bogus", "--json"}, cli.ExitUsage},
		{"unexpected_argument", emptyHome, []string{"--json", "status", "extra"}, cli.ExitUsage},
		{"unknown_subcommand", emptyHome, []string{"topic", "crate", "--json"}, cli.ExitUsage},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			home := tc.home(t)
			got := run(t, home, tc.args...)
			if got.code != tc.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, tc.code, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("--json wrote to stderr: %q", got.stderr)
			}
			golden(t, tc.golden+".json", got.stdout)
			if tc.golden == "topic_create_dry_run" {
				if _, err := os.Stat(filepath.Join(home, "c")); !os.IsNotExist(err) {
					t.Errorf("--dry-run created a Topic folder (err = %v)", err)
				}
			}
		})
	}
}

func TestHumanOutput(t *testing.T) {
	golden(t, "status_empty.txt", run(t, emptyHome(t)).stdout)

	created := run(t, emptyHome(t), "topic", "create", "--title", "Linear algebra")
	if created.code != cli.ExitOK {
		t.Fatalf("topic create: exit %d, stderr %s", created.code, created.stderr)
	}
	golden(t, "topic_create.txt", created.stdout)

	home := withTopic(t)
	golden(t, "status_one_topic.txt", run(t, home, "status").stdout)
	if r := run(t, home, "topic", "create", "--title", "Physics"); r.code != cli.ExitOK {
		t.Fatalf("second topic: %s", r.stderr)
	}
	golden(t, "status_from_topic_folder.txt", runIn(t, home, filepath.Join(home, "linear-algebra"), "status").stdout)

	duplicate := run(t, home, "topic", "create", "--title", "Linear algebra")
	if duplicate.code != cli.ExitError || duplicate.stdout != "" || !strings.Contains(duplicate.stderr, "already exists") {
		t.Errorf("duplicate: exit %d, stdout %q, stderr %q", duplicate.code, duplicate.stdout, duplicate.stderr)
	}
}

func TestFlagValuesAreNotMistakenForFlags(t *testing.T) {
	home := emptyHome(t)
	got := run(t, home, "topic", "create", "--title", "--json", "--id", "flags")
	if got.code != cli.ExitOK || !strings.HasPrefix(got.stdout, "Created Topic flags (--json)") {
		t.Errorf("exit %d, stdout %q: a title of --json must not switch to JSON output", got.code, got.stdout)
	}
}

// TestMCPCommand drives study mcp in process with a real MCP client over
// pipes, the way an agent harness runs it.
func TestMCPCommand(t *testing.T) {
	ctx := context.Background()
	home := withTopic(t)
	clientToServer, serverIn := io.Pipe()
	serverOut, serverToClient := io.Pipe()
	done := make(chan int, 1)
	go func() {
		var stderr bytes.Buffer
		done <- cli.Run(ctx, []string{"mcp"}, clientToServer, serverToClient, &stderr, options(home, home))
		_ = serverToClient.Close()
	}()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: serverOut, Writer: serverIn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("status: %v %+v", err, res)
	}
	if !strings.Contains(fmt.Sprint(res.StructuredContent), "linear-algebra") {
		t.Errorf("status did not mention the Topic: %v", res.StructuredContent)
	}
	_ = session.Close()
	if code := <-done; code != cli.ExitOK {
		t.Errorf("study mcp exited with %d after the client disconnected", code)
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

func TestLibraryCommands(t *testing.T) {
	books := func(t *testing.T) string {
		home := t.TempDir()
		for _, book := range []string{"Programming/The_C_Programming_Language.pdf", "Physics/Quantum.Mechanics.PDF"} {
			path := filepath.Join(home, "Books", book)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return home
	}
	indexed := func(t *testing.T) string {
		home := books(t)
		if r := run(t, home, "library", "build", "Books"); r.code != cli.ExitOK {
			t.Fatalf("setup: library build: %s", r.stderr)
		}
		return home
	}
	for _, tc := range []struct {
		golden string
		home   func(*testing.T) string
		args   []string
		code   int
	}{
		{"library_search_before_build", books, []string{"library", "search", "C", "--json"}, cli.ExitError},
		{"library_build", books, []string{"library", "build", "Books", "--json"}, cli.ExitOK},
		{"library_search", indexed, []string{"library", "search", "C", "--json"}, cli.ExitOK},
		{"library_search_no_match", indexed, []string{"library", "search", "organic", "chemistry", "--json"}, cli.ExitOK},
		{"library_build_no_folder", books, []string{"library", "build", "--json"}, cli.ExitUsage},
		{"library_unknown_subcommand", books, []string{"library", "serch", "C", "--json"}, cli.ExitUsage},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			got := run(t, tc.home(t), tc.args...)
			if got.code != tc.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, tc.code, got.stderr)
			}
			golden(t, tc.golden+".json", got.stdout)
		})
	}
	golden(t, "library_search.txt", run(t, indexed(t), "library", "search", "quantum", "mechanics").stdout)
}
