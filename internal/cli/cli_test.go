package cli_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// eventIDs numbers Events per Study home: like real random IDs they never
// repeat between runs against one home, and because each home counts from
// id001 the golden files don't depend on which tests ran before.
var eventIDs sync.Map // Study home → *atomic.Int64

func options(home, dir string) core.Options {
	counter, _ := eventIDs.LoadOrStore(home, new(atomic.Int64))
	n := counter.(*atomic.Int64)
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
		{"topic_update", withTopic, []string{"topic", "update", "linear-algebra", "--title", "Linear algebra II", "--goal", "", "--json"}, cli.ExitOK},
		{"topic_update_unchanged", withTopic, []string{"topic", "update", "linear-algebra", "--title", "Linear algebra", "--json"}, cli.ExitOK},
		{"topic_update_nothing", withTopic, []string{"topic", "update", "linear-algebra", "--json"}, cli.ExitUsage},
		{"topic_update_unknown", withTopic, []string{"topic", "update", "biology", "--title", "Biology", "--json"}, cli.ExitError},
		{"status_with_flags", withFlaggedTopic, []string{"status", "--json"}, cli.ExitOK},
		{"topic_dismiss_flag", withFlaggedTopic, []string{"topic", "dismiss-flag", "linear-algebra", heldFlag, "--json"}, cli.ExitOK},
		{"topic_dismiss_flag_dry_run", withFlaggedTopic, []string{"topic", "dismiss-flag", "linear-algebra", heldFlag, "--dry-run", "--json"}, cli.ExitOK},
		{"topic_dismiss_flag_unknown", withFlaggedTopic, []string{"topic", "dismiss-flag", "linear-algebra", "0123456789", "--json"}, cli.ExitError},
		{"topic_dismiss_flag_bad_id", withFlaggedTopic, []string{"topic", "dismiss-flag", "linear-algebra", "nope", "--json"}, cli.ExitUsage},
		{"topic_remove_dry_run", withTopic, []string{"topic", "remove", "linear-algebra", "--dry-run", "--json"}, cli.ExitOK},
		{"topic_remove", withTopic, []string{"topic", "remove", "linear-algebra", "--json"}, cli.ExitOK},
		{"topic_remove_unknown", withTopic, []string{"topic", "remove", "biology", "--json"}, cli.ExitError},
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

	updated := run(t, home, "topic", "update", "linear-algebra", "--goal", "Pass the June exam")
	if updated.code != cli.ExitOK {
		t.Fatalf("topic update: exit %d, stderr %s", updated.code, updated.stderr)
	}
	golden(t, "topic_update.txt", updated.stdout)
	golden(t, "status_with_flags.txt", run(t, withFlaggedTopic(t), "status").stdout)

	flagged := withFlaggedTopic(t)
	golden(t, "topic_dismiss_flag.txt", run(t, flagged, "topic", "dismiss-flag", "linear-algebra", heldFlag).stdout)
	golden(t, "topic_dismiss_flag_again.txt", run(t, flagged, "topic", "dismiss-flag", "linear-algebra", heldFlag).stdout)

	removable := withTopic(t)
	golden(t, "topic_remove_dry_run.txt", run(t, removable, "topic", "remove", "linear-algebra", "--dry-run").stdout)
	golden(t, "topic_remove.txt", run(t, removable, "topic", "remove", "linear-algebra").stdout)
	golden(t, "status_after_topic_remove.txt", run(t, removable, "status").stdout)
}

// heldFlag is the ID of the flag withFlaggedTopic's held Event raises. Flag
// IDs are stable, so it is the same on every run and machine.
const heldFlag = "e9bd1dc27f"

// withFlaggedTopic returns a Study home whose one Topic has an Event this
// version of study doesn't know, as a newer version could have written.
func withFlaggedTopic(t *testing.T) string {
	t.Helper()
	home := withTopic(t)
	f, err := os.OpenFile(filepath.Join(home, "linear-algebra", "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"format":1,"id":"zz1","time":"2026-10-01T10:00:00Z","wall":"2026-10-01T10:00:00Z","type":"card.reviewed"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	return home
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

// failingWriter fails every write, like a closed pipe.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestLibraryOutputErrorsAreReported(t *testing.T) {
	home := t.TempDir()
	books := filepath.Join(home, "Books", "Programming")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(books, "The_C_Programming_Language.pdf"), []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"library", "build", "Books"}, {"library", "search", "C"}} {
		var stderr bytes.Buffer
		code := cli.Run(context.Background(), args, strings.NewReader(""), failingWriter{}, &stderr, options(home, home))
		if code == cli.ExitOK {
			t.Errorf("study %s exited 0 although writing its output failed", strings.Join(args, " "))
		}
	}
}

func TestCheckpointCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Ada Learner\n\temail = ada@example.com\n[maintenance]\n\tauto = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	study := filepath.Join(home, "study")
	if r := run(t, study, "topic", "create", "--title", "Linear algebra"); r.code != cli.ExitOK {
		t.Fatalf("setup: %s", r.stderr)
	}
	hashes := regexp.MustCompile(`[0-9a-f]{12,40}`)
	checkpoint := func(args ...string) result {
		t.Helper()
		r := run(t, study, append([]string{"checkpoint"}, args...)...)
		r.stdout = hashes.ReplaceAllString(r.stdout, "<commit>")
		return r
	}

	if r := checkpoint("--topic", "linear-algebra", "--role", "agent", "--dry-run"); r.code != cli.ExitOK {
		t.Errorf("dry run: exit %d, stderr %s", r.code, r.stderr)
	} else {
		golden(t, "checkpoint_dry_run.txt", r.stdout)
	}
	golden(t, "checkpoint.json", checkpoint("--topic", "linear-algebra", "--role", "agent", "-m", "Lesson 1 notes", "--json").stdout)
	golden(t, "checkpoint_unchanged.txt", checkpoint("--topic", "linear-algebra", "--role", "learner").stdout)

	for _, tc := range []struct {
		golden string
		args   []string
		code   int
	}{
		{"checkpoint_missing_role", []string{"--topic", "linear-algebra", "--json"}, cli.ExitUsage},
		{"checkpoint_unknown_topic", []string{"--topic", "physics", "--role", "learner", "--json"}, cli.ExitError},
	} {
		r := checkpoint(tc.args...)
		if r.code != tc.code {
			t.Errorf("%s: exit %d, want %d", tc.golden, r.code, tc.code)
		}
		golden(t, tc.golden+".json", r.stdout)
	}
}

func TestTopicUpdateOutputErrorsAreReported(t *testing.T) {
	home := withTopic(t)
	var stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"topic", "update", "linear-algebra", "--goal", "Pass the exam"},
		strings.NewReader(""), failingWriter{}, &stderr, options(home, home))
	if code == cli.ExitOK {
		t.Error("study topic update exited 0 although writing its output failed")
	}
}
