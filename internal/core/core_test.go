package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

var fixedNow = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

// eventIDs numbers Events across the whole test process: like real random
// IDs, they never repeat between Cores over one Study home.
var eventIDs atomic.Int64

// testCore returns a Core over a fresh Study home, started in dir (the Study
// home itself when dir is empty), with a fixed clock and predictable IDs.
func testCore(t *testing.T, home, dir string) *core.Core {
	t.Helper()
	if dir == "" {
		dir = home
	}
	n := &eventIDs
	c, err := core.Open(core.Options{
		Getenv: envOf(map[string]string{"STUDY_HOME": home, "HOME": t.TempDir()}),
		Dir:    dir,
		Now:    func() time.Time { return fixedNow },
		NewID:  func() string { return fmt.Sprintf("id%03d", n.Add(1)) },
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
	if !reflect.DeepEqual(topic, want) {
		t.Fatalf("topic = %+v, want %+v", topic, want)
	}

	settings := readFile(t, topic.Path, "topic.toml")
	for _, line := range []string{"format = 1", `title = "Linear Algebra & Calculus"`, `goal = "Solve linear systems by hand"`} {
		if !strings.Contains(settings, line) {
			t.Errorf("topic.toml lacks %q:\n%s", line, settings)
		}
	}
	if got := readFile(t, topic.Path, ".gitattributes"); got != "history.jsonl merge=union\ncards.jsonl merge=union\nsources.jsonl merge=union\n" {
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
	if ev["type"] != "topic.created" || !strings.HasPrefix(fmt.Sprint(ev["id"]), "id") || ev["format"] != float64(1) {
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

func TestStatusReportsUnreadableTopics(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	c := testCore(t, home, "")
	for _, title := range []string{"C", "Go", "Physics"} {
		if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "c", "topic.toml"), []byte("format = 99\ntitle = \"C\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "go", "topic.toml"), []byte("title = [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatalf("one broken Topic must not break status: %v", err)
	}
	if len(status.Topics) != 1 || status.Topics[0].ID != "physics" {
		t.Errorf("topics = %+v", status.Topics)
	}
	codes := map[string]core.ErrorCode{}
	for _, p := range status.Problems {
		codes[p.ID] = p.Code
	}
	if codes["c"] != core.CodeNewerFormat || codes["go"] != core.CodeCorrupt || len(codes) != 2 {
		t.Errorf("problems = %+v", status.Problems)
	}
	// Without a most recent Topic, the readable one is chosen, without
	// claiming it is the learner's only Topic.
	if err := os.Remove(filepath.Join(home, ".lamplight", "state.toml")); err != nil {
		t.Fatal(err)
	}
	status, err = testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := status.ActiveTopic; got == nil || got.ID != "physics" || got.Reason != "it is the only Topic that could be read" {
		t.Errorf("active topic = %+v, want physics as the only readable Topic", got)
	}
}

func TestCreateTopicIgnoresTheCallersGitEnvironment(t *testing.T) {
	stray := filepath.Join(t.TempDir(), "stray.git")
	t.Setenv("GIT_DIR", stray)
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	home := t.TempDir()
	topic, err := testCore(t, home, "").CreateTopic(context.Background(), core.TopicSpec{Title: "Biology"})
	if err != nil {
		t.Fatalf("CreateTopic with GIT_DIR set: %v", err)
	}
	if _, err := os.Stat(filepath.Join(topic.Path, ".git", "HEAD")); err != nil {
		t.Errorf("the Topic is not a git repository: %v", err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("git touched the repository named by GIT_DIR (err = %v)", err)
	}
}

func TestStatusFollowsASymlinkedStudyHome(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := testCore(t, link, "")
	for _, title := range []string{"Physics", "C"} {
		if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{filepath.Join(real, "physics"), filepath.Join(link, "physics")} {
		status, err := testCore(t, link, dir).Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := status.ActiveTopic; got == nil || got.ID != "physics" || got.ChosenBy != core.ChosenByFolder {
			t.Errorf("from %s: active topic = %+v, want physics chosen by folder", dir, got)
		}
	}
}

func TestConcurrentTopicCreation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	c := testCore(t, home, "")

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := c.CreateTopic(ctx, core.TopicSpec{Title: fmt.Sprintf("Topic %d", i)})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := c.CreateTopic(ctx, core.TopicSpec{Title: "Shared"})
			if core.CodeOf(err) == core.CodeAlreadyExists {
				err = nil
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent creation: %v", err)
		}
	}
	status, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 21 || len(status.Problems) != 0 {
		t.Errorf("got %d Topics and problems %+v, want 21 Topics", len(status.Topics), status.Problems)
	}
	leftovers, _ := os.ReadDir(filepath.Join(home, ".lamplight", "tmp"))
	if len(leftovers) != 0 {
		t.Errorf("%d staging folders were left behind", len(leftovers))
	}
}

func TestTopicTextAndFolderNames(t *testing.T) {
	ctx := context.Background()
	c := testCore(t, t.TempDir(), "")
	topic, err := c.CreateTopic(ctx, core.TopicSpec{Title: "Lineare Algebra für Anfänger", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if topic.ID != "lineare-algebra-fur-anfanger" {
		t.Errorf("id = %q", topic.ID)
	}
	for _, spec := range []core.TopicSpec{
		{Title: "Evil\x1b[2J"},
		{Title: "C", Goal: "bell\a"},
	} {
		if _, err := c.CreateTopic(ctx, spec); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", spec, err)
		}
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
		{"absolute STUDY_HOME needs no HOME", map[string]string{"STUDY_HOME": "/srv/study"}, "/srv/study"},
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

func TestRelativeStudyHomeIsRefused(t *testing.T) {
	_, err := core.Open(core.Options{Getenv: envOf(map[string]string{"STUDY_HOME": "study", "HOME": t.TempDir()})})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument for a relative STUDY_HOME", err)
	}
}

func TestUpdateTopic(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	c := testCore(t, home, "")
	for _, title := range []string{"C", "Physics"} {
		if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	title, goal := "Systems programming in C", "Write a shell"

	dry, err := c.UpdateTopic(ctx, "c", core.TopicChanges{Title: &title, DryRun: true})
	if err != nil || !dry.Changed || dry.Topic.Title != title {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if got := readFile(t, filepath.Join(home, "c"), "topic.toml"); strings.Contains(got, title) {
		t.Errorf("--dry-run changed topic.toml:\n%s", got)
	}

	updated, err := c.UpdateTopic(ctx, "c", core.TopicChanges{Title: &title, Goal: &goal})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Changed || updated.Topic.Title != title || updated.Topic.Goal != goal || !updated.Topic.Created.Equal(fixedNow) {
		t.Errorf("update = %+v", updated)
	}
	status, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveTopic == nil || status.ActiveTopic.ID != "c" || status.ActiveTopic.ChosenBy != core.ChosenByRecent {
		t.Errorf("the updated Topic should become the most recent: %+v", status.ActiveTopic)
	}

	empty := ""
	cleared, err := c.UpdateTopic(ctx, "c", core.TopicChanges{Goal: &empty})
	if err != nil || cleared.Topic.Goal != "" {
		t.Errorf("clearing the goal: %+v, %v", cleared, err)
	}

	blank, bell := "  ", "bell\a"
	for name, tc := range map[string]struct {
		id      string
		changes core.TopicChanges
		code    core.ErrorCode
	}{
		"nothing to change": {"c", core.TopicChanges{}, core.CodeInvalidArgument},
		"empty title":       {"c", core.TopicChanges{Title: &blank}, core.CodeInvalidArgument},
		"control character": {"c", core.TopicChanges{Goal: &bell}, core.CodeInvalidArgument},
		"invalid id":        {"../c", core.TopicChanges{Title: &title}, core.CodeInvalidArgument},
		"unknown Topic":     {"biology", core.TopicChanges{Title: &title}, core.CodeNotFound},
	} {
		if _, err := c.UpdateTopic(ctx, tc.id, tc.changes); core.CodeOf(err) != tc.code {
			t.Errorf("%s: err = %v, want %s", name, err, tc.code)
		}
	}
}

func TestStatusReportsATopicWithoutSettings(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if _, err := testCore(t, home, "").CreateTopic(ctx, core.TopicSpec{Title: "C"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "c", "topic.toml")); err != nil {
		t.Fatal(err)
	}
	status, err := testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Problems) != 1 || status.Problems[0].ID != "c" || status.Problems[0].Code != core.CodeCorrupt {
		t.Errorf("problems = %+v: a Topic whose settings are missing is damaged, not gone", status.Problems)
	}
}

func TestStatusReportsATopicItCannotCheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	ctx := context.Background()
	home := t.TempDir()
	c := testCore(t, home, "")
	for _, title := range []string{"C", "Physics"} {
		if _, err := c.CreateTopic(ctx, core.TopicSpec{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(home, "c")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	status, err := testCore(t, home, "").Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Problems) != 1 || status.Problems[0].ID != "c" || status.Problems[0].Code != core.CodeInternal {
		t.Errorf("problems = %+v, want Topic c reported, not hidden", status.Problems)
	}
	if len(status.Topics) != 1 || status.Topics[0].ID != "physics" {
		t.Errorf("topics = %+v", status.Topics)
	}
}

func TestCreateTopicKeepsNewerLocalState(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".lamplight", "state.toml")
	newer := "format = 99\nrecent_topic = \"physics\"\nsomething_new = true\n"
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := testCore(t, home, "").CreateTopic(context.Background(), core.TopicSpec{Title: "C"}); err != nil {
		t.Fatalf("creating a Topic must still succeed: %v", err)
	}
	if got, _ := os.ReadFile(state); string(got) != newer {
		t.Errorf("state.toml written by a newer study was replaced:\n%s", got)
	}
}
