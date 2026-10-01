package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var fixedNow = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

// testCore returns a Core over a fresh Study home, started in dir (the Study
// home itself when dir is empty), with a fixed clock and predictable IDs.
func testCore(t *testing.T, home, dir string) *core.Core {
	t.Helper()
	if dir == "" {
		dir = home
	}
	n := 0
	c, err := core.Open(core.Options{
		Getenv: envOf(map[string]string{"STUDY_HOME": home, "HOME": t.TempDir()}),
		Dir:    dir,
		Now:    func() time.Time { return fixedNow },
		NewID:  func() string { n++; return fmt.Sprintf("id%03d", n) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestCreateTopicWritesItsFolder(t *testing.T) {
	home := t.TempDir()
	c := testCore(t, home, "")

	topic, err := c.CreateTopic(context.Background(), core.TopicSpec{
		Title: "Linear Algebra & Calculus", Goal: "Solve linear systems by hand",
	})
	if err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	want := core.Topic{
		ID: "linear-algebra-calculus", Title: "Linear Algebra & Calculus",
		Goal: "Solve linear systems by hand", Path: filepath.Join(home, "linear-algebra-calculus"),
		Created: fixedNow,
	}
	if topic != want {
		t.Fatalf("topic = %+v, want %+v", topic, want)
	}

	settings := readFile(t, topic.Path, "topic.toml")
	for _, line := range []string{"format = 1", `title = "Linear Algebra & Calculus"`, `goal = "Solve linear systems by hand"`} {
		if !strings.Contains(settings, line) {
			t.Errorf("topic.toml lacks %q:\n%s", line, settings)
		}
	}
	if got := readFile(t, topic.Path, ".gitattributes"); got != "history.jsonl merge=union\n" {
		t.Errorf(".gitattributes = %q", got)
	}
	if _, err := os.Stat(filepath.Join(topic.Path, ".git")); err != nil {
		t.Errorf("the Topic is not a git repository: %v", err)
	}

	history := readFile(t, topic.Path, "history.jsonl")
	lines := strings.Split(strings.TrimSuffix(history, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("history has %d lines, want 1:\n%s", len(lines), history)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("history line is not JSON: %v", err)
	}
	if ev["type"] != "topic.created" || ev["id"] != "id001" || ev["format"] != float64(1) {
		t.Errorf("unexpected Event: %v", ev)
	}
}

func TestCreateTopicValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec core.TopicSpec
	}{
		{"empty title", core.TopicSpec{Title: "   "}},
		{"uppercase id", core.TopicSpec{Title: "C", ID: "Physics"}},
		{"path traversal", core.TopicSpec{Title: "C", ID: "../escape"}},
		{"leading hyphen", core.TopicSpec{Title: "C", ID: "-c"}},
		{"double hyphen", core.TopicSpec{Title: "C", ID: "a--b"}},
		{"title without letters", core.TopicSpec{Title: "!!!"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			_, err := testCore(t, home, "").CreateTopic(context.Background(), tc.spec)
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v (code %s), want invalid_argument", err, core.CodeOf(err))
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Errorf("an invalid request wrote %d entries to the Study home", len(entries))
			}
		})
	}
}

func TestCreateTopicRejectsDuplicates(t *testing.T) {
	c := testCore(t, t.TempDir(), "")
	ctx := context.Background()
	if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: "C"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateTopic(ctx, core.TopicSpec{Title: "Something else", ID: "c"})
	if core.CodeOf(err) != core.CodeAlreadyExists {
		t.Fatalf("err = %v, want already_exists", err)
	}
}

func TestCreateTopicDryRunWritesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "study")
	topic, err := testCore(t, home, "").CreateTopic(context.Background(), core.TopicSpec{Title: "C", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if topic.ID != "c" || topic.Path != filepath.Join(home, "c") {
		t.Errorf("dry run returned %+v", topic)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("dry run created the Study home (err = %v)", err)
	}
}

func TestStatusChoosesTheActiveTopic(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()

	status, err := testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveTopic != nil || len(status.Topics) != 0 {
		t.Fatalf("empty Study home: %+v", status)
	}

	writer := testCore(t, home, "")
	for _, title := range []string{"Physics", "C"} {
		if _, err := writer.CreateTopic(ctx, core.TopicSpec{Title: title}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name, dir, wantID, wantBy string
	}{
		{"most recent from the Study home", home, "c", core.ChosenByRecent},
		{"inside a Topic folder", filepath.Join(home, "physics", "notes"), "physics", core.ChosenByFolder},
		{"outside the Study home", t.TempDir(), "c", core.ChosenByRecent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := testCore(t, home, tc.dir).Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := status.ActiveTopic; got == nil || got.ID != tc.wantID || got.ChosenBy != tc.wantBy {
				t.Fatalf("active topic = %+v, want %s chosen by %s", got, tc.wantID, tc.wantBy)
			}
			if len(status.Topics) != 2 || status.Topics[0].ID != "c" || status.Topics[1].ID != "physics" {
				t.Errorf("topics = %+v", status.Topics)
			}
		})
	}

	if err := os.RemoveAll(filepath.Join(home, ".lamplight")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "c")); err != nil {
		t.Fatal(err)
	}
	status, err = testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := status.ActiveTopic; got == nil || got.ID != "physics" || got.ChosenBy != core.ChosenByOnly {
		t.Fatalf("with one Topic and no local state, active topic = %+v", got)
	}
}

func TestStatusReadsCreationTimeFromHistory(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if _, err := testCore(t, home, "").CreateTopic(ctx, core.TopicSpec{Title: "C"}); err != nil {
		t.Fatal(err)
	}
	// An interrupted write leaves a last line without a newline: it is ignored.
	appendTo(t, filepath.Join(home, "c", "history.jsonl"), `{"format":1,"id":"partial`)

	status, err := testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatalf("Status with a partial last line: %v", err)
	}
	if got := status.Topics[0].Created; !got.Equal(fixedNow) {
		t.Errorf("created = %v, want %v", got, fixedNow)
	}
}

func TestNewerFormatsAreRefused(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if _, err := testCore(t, home, "").CreateTopic(ctx, core.TopicSpec{Title: "C"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "c", "topic.toml")
	if err := os.WriteFile(path, []byte("format = 99\ntitle = \"C\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := testCore(t, home, "").Status(ctx)
	if core.CodeOf(err) != core.CodeNewerFormat {
		t.Fatalf("err = %v, want newer_format", err)
	}
}

func TestStudyHomeResolution(t *testing.T) {
	userHome := t.TempDir()
	configHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configHome, "lamplight"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "lamplight", "config.toml"),
		[]byte("format = 1\nstudy_home = \"~/notes/study\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", map[string]string{"HOME": userHome}, filepath.Join(userHome, "study")},
		{"config file", map[string]string{"HOME": userHome, "XDG_CONFIG_HOME": configHome},
			filepath.Join(userHome, "notes", "study")},
		{"STUDY_HOME wins", map[string]string{"HOME": userHome, "XDG_CONFIG_HOME": configHome, "STUDY_HOME": "~/elsewhere"},
			filepath.Join(userHome, "elsewhere")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := core.Open(core.Options{Getenv: envOf(tc.env), Dir: userHome})
			if err != nil {
				t.Fatal(err)
			}
			if c.Home() != tc.want {
				t.Errorf("home = %s, want %s", c.Home(), tc.want)
			}
		})
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
