//go:build linux || darwin

package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// sharedShelf is a shared computer (see sharedComputer) on which the test
// plugin, installed in a folder of its own, is registered as kb, with a
// Topic, linear-algebra, that names it and has one book as its Source.
type sharedShelf struct {
	*shelfComputer
	shared *sharedComputer
}

func newSharedShelf(t *testing.T) *sharedShelf {
	t.Helper()
	shared := newSharedComputer(t)
	s := &shelfComputer{kbComputer: shared.kbComputer, record: filepath.Join(t.TempDir(), "record.json")}
	s.plugins = core.PluginOptions{
		Environ:      func() []string { return []string{"HOME=" + s.home, "STUDY_HOME=" + s.env["STUDY_HOME"]} },
		StartTimeout: 20 * time.Second, StopGrace: 300 * time.Millisecond, GlanceTimeout: 20 * time.Second,
		IndexPoll: 10 * time.Millisecond, CancelTimeout: 5 * time.Second,
	}
	s.ok(t, "knowledge-base", "add", "kb", "--", "notes-kb", plugintest.Arg, "--record", s.record)
	s.ok(t, "topic", "create", "--title", "Linear algebra")
	s.ok(t, "topic", "update", "linear-algebra", "--knowledge-base", "kb")
	s.book(t, "book.txt", bookOf(2, "one"))
	return &sharedShelf{shelfComputer: s, shared: shared}
}

// say runs study on the computer and returns what came of it, with the
// computer's folders named so that two computers can be compared.
func (s *sharedShelf) say(t *testing.T, args ...string) string {
	t.Helper()
	r := s.run(t, args...)
	text := fmt.Sprintf("$ study %s\nexit %d\n%s%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	text = strings.ReplaceAll(text, s.record, "$RECORD")
	return strings.ReplaceAll(text, s.shared.base, "$BASE")
}

// story is what is said of a Topic's plugin and what it has indexed, before
// study source index and after it.
func (s *sharedShelf) story(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, args := range [][]string{
		{"source", "list", "linear-algebra", "--json"},
		{"status", "--json"},
		{"source", "index", "linear-algebra", "--dry-run", "--json"},
		{"source", "index", "linear-algebra", "--json"},
		{"source", "list", "linear-algebra", "--json"},
		{"source", "list", "linear-algebra"},
		{"status"},
	} {
		b.WriteString(s.say(t, args...))
	}
	return b.String()
}

// What a Topic's plugin has indexed is asked, and study source index has it
// index, below folders the learner may pass through and not list, as /home
// is on many shared computers: above Lamplight's configuration folder, above
// the plugin's program and around it, above the Study home, and above the
// folder the plugin is given. study says there exactly what it says anywhere.
func TestIndexingBelowFoldersThatMayNotBeListed(t *testing.T) {
	listed := newSharedShelf(t).story(t)
	for _, want := range []string{
		"$ study source list linear-algebra --json\nexit 0\n",
		`"state": "not_indexed"`,
		`"action": "study source index linear-algebra"`,
		"$ study source index linear-algebra --dry-run --json\nexit 0\n",
		"$ study source index linear-algebra --json\nexit 0\n",
		`"path": "$HOME/Books/book.txt"`,
		`"asked": true`,
		`"state": "indexed"`,
		"$ study status\nexit 0\n",
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("on a computer whose folders may all be listed, study does not say %q:\n%s", want, listed)
		}
	}
	if strings.Contains(listed, `"problem"`) || strings.Contains(listed, `"state": "unknown"`) {
		t.Fatalf("on a computer whose folders may all be listed, the plugin was not heard:\n%s", listed)
	}

	s := newSharedShelf(t)
	plugintest.PassThrough(t, s.shared.folders()...)
	if passed := s.story(t); passed != listed {
		t.Errorf("below folders that may not be listed:\n%s\nbelow folders that may be:\n%s", passed, listed)
	}
}

// Below a folder the learner may not even pass through, study cannot tell
// where a path leads, and takes nothing there for outside the Study home.
// The plugin is not started: a Topic's Sources and status are there all the
// same, with the index state not known and what study could not follow, and
// study source index refuses, saying the same.
func TestIndexingRefusesWhatItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// shut returns the folder nobody may pass through, and what study
		// is told about where its folders are.
		shut func(s *sharedShelf) (string, map[string]string)
		// glance is what a Topic's Sources and status say is wrong, and
		// index what study source index says.
		glance, index string
		action        string
	}{
		{name: "the plugin's folder",
			shut: func(s *sharedShelf) (string, map[string]string) {
				shut := filepath.Join(s.shared.base, "shut")
				return shut, map[string]string{"XDG_STATE_HOME": filepath.Join(shut, "state")}
			},
			glance: "study cannot make the folder Knowledge base plugin kb keeps its index in",
			index:  "study cannot make the folder Knowledge base plugin kb keeps its index in",
			action: "study knowledge-base check kb"},
		{name: "the configuration folder",
			shut: func(s *sharedShelf) (string, map[string]string) {
				shut := filepath.Join(s.shared.base, "shut")
				return shut, map[string]string{"XDG_CONFIG_HOME": filepath.Join(shut, "config")}
			},
			glance: "study cannot reach Lamplight's configuration folder",
			index:  "study cannot reach Lamplight's configuration folder",
			action: "study knowledge-base list"},
		{name: "the plugin's program",
			shut: func(s *sharedShelf) (string, map[string]string) {
				// The program was registered where it could be followed.
				return filepath.Join(s.shared.base, "opt"), nil
			},
			glance: "Knowledge base plugin kb is registered on this computer, and study does not use it",
			index:  "permission denied",
			action: "study knowledge-base list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSharedShelf(t)
			shut, env := tc.shut(s)
			if err := os.MkdirAll(shut, 0o755); err != nil {
				t.Fatal(err)
			}
			plugintest.Shut(t, shut)
			for name, value := range env {
				s.env[name] = value
			}
			for _, args := range [][]string{{"source", "list", "linear-algebra", "--json"}, {"status", "--json"}} {
				got := s.say(t, args...)
				for _, want := range []string{"exit 0\n", `"state": "unknown"`, tc.glance, `"action": "` + tc.action + `"`,
					`"title": "Book"`} {
					if !strings.Contains(got, want) {
						t.Errorf("%s\nwant it to say %q", got, want)
					}
				}
			}
			for _, args := range [][]string{{"source", "index", "linear-algebra", "--json"},
				{"source", "index", "linear-algebra", "--dry-run", "--json"}} {
				got := s.say(t, args...)
				if !strings.Contains(got, "exit 1\n") || !strings.Contains(got, `"code": "failed_precondition"`) ||
					!strings.Contains(got, tc.index) {
					t.Errorf("%s\nwant failed_precondition, saying %q", got, tc.index)
				}
			}
			if _, err := os.Stat(s.record); err == nil {
				t.Errorf("the plugin was started, with %s shut", tc.name)
			}
		})
	}
}
