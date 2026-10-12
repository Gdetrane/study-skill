//go:build linux || darwin

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// searchStory is what a search of the Topic's Sources says, and what
// recording a quote with the Passage it is from says, once the one book is
// indexed.
func (s *sharedShelf) searchStory(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(s.say(t, "evidence", "search", "linear-algebra", "one", "--json"))
	b.WriteString(s.say(t, "source", "index", "linear-algebra", "--json"))
	found := s.found(t, "one")
	if len(found) != 2 {
		t.Fatalf("the search found %+v, want the book's two Passages", found)
	}
	record := []string{"evidence", "record", "linear-algebra", "--lesson", "one", "--source", found[0].Source, "--passage", found[0].ID}
	for _, args := range [][]string{
		{"evidence", "search", "linear-algebra", "one", "--json"},
		{"evidence", "search", "linear-algebra", "one"},
		append(append([]string{}, record...), "--quote", "Page 1 speaks of one.", "--json"),
		append(append([]string{}, record...), "--quote", "Page 1 speaks of two.", "--json"),
		{"evidence", "list", "linear-algebra"},
	} {
		b.WriteString(s.say(t, args...))
	}
	return b.String()
}

// A Topic's Sources are searched, and a quote is checked against its
// Passage, below folders the learner may pass through and not list, as
// /home is on many shared computers: above Lamplight's configuration
// folder, above the plugin's program and around it, above the Study home,
// and above the folder the plugin is given. study says there exactly what it
// says anywhere.
func TestSearchingBelowFoldersThatMayNotBeListed(t *testing.T) {
	listed := newSharedShelf(t).searchStory(t)
	for _, want := range []string{
		"$ study evidence search linear-algebra one --json\nexit 0\n",
		`"note": "nothing was searched: Knowledge base plugin kb does not hold the Source indexed on this computer as the Topic records it"`,
		`"text": "Page 1 speaks of one."`,
		`"location": "p. 2"`,
		"  > Page 2 speaks of one.\n",
		`"checked": true`,
		`"location_from": "knowledge_base"`,
		"exit 2\n",
		`"code": "invalid_argument"`,
		"checked against Passage <passage> of Knowledge base plugin kb\n",
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("on a computer whose folders may all be listed, study does not say %q:\n%s", want, listed)
		}
	}
	if strings.Contains(listed, "failed_precondition") {
		t.Fatalf("on a computer whose folders may all be listed, the plugin was not heard:\n%s", listed)
	}

	s := newSharedShelf(t)
	plugintest.PassThrough(t, s.shared.folders()...)
	if passed := s.searchStory(t); passed != listed {
		t.Errorf("below folders that may not be listed:\n%s\nbelow folders that may be:\n%s", passed, listed)
	}
}

// Below a folder the learner may not even pass through, study cannot tell
// where a path leads, and takes nothing there for outside the Study home.
// The plugin is not started: a search says what study could not follow, and
// so does recording a quote with a Passage, which records nothing.
func TestSearchingRefusesWhatItCannotFollow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// shut returns the folder nobody may pass through, and what study
		// is told about where its folders are.
		shut func(s *sharedShelf) (string, map[string]string)
		says string
	}{
		{name: "the plugin's folder",
			shut: func(s *sharedShelf) (string, map[string]string) {
				shut := filepath.Join(s.shared.base, "shut")
				return shut, map[string]string{"XDG_STATE_HOME": filepath.Join(shut, "state")}
			},
			says: "study cannot make the folder Knowledge base plugin kb keeps its index in"},
		{name: "the configuration folder",
			shut: func(s *sharedShelf) (string, map[string]string) {
				shut := filepath.Join(s.shared.base, "shut")
				return shut, map[string]string{"XDG_CONFIG_HOME": filepath.Join(shut, "config")}
			},
			says: "study cannot reach Lamplight's configuration folder"},
		{name: "the plugin's program",
			shut: func(s *sharedShelf) (string, map[string]string) {
				// The program was registered where it could be followed.
				return filepath.Join(s.shared.base, "opt"), nil
			},
			says: "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSharedShelf(t)
			// The Source's id, as it was before anything was shut: the
			// Evidence names a Source the Topic has.
			var listed struct {
				Data core.SourceList `json:"data"`
			}
			s.asItIs(t, &listed, "source", "list", "linear-algebra", "--json")
			if len(listed.Data.Sources) != 1 {
				t.Fatalf("the Topic's Sources are %+v, want its one book", listed.Data.Sources)
			}
			source := listed.Data.Sources[0].ID
			// Listing the Sources started the plugin, to ask what it has
			// indexed: how it was started then, and what it was asked.
			started, _ := os.ReadFile(s.record)
			asked := len(s.calls(t))
			shut, env := tc.shut(s)
			if err := os.MkdirAll(shut, 0o755); err != nil {
				t.Fatal(err)
			}
			plugintest.Shut(t, shut)
			for name, value := range env {
				s.env[name] = value
			}
			for _, args := range [][]string{
				{"evidence", "search", "linear-algebra", "one", "--json"},
				{"evidence", "record", "linear-algebra", "--lesson", "one", "--source", source, "--passage", "0123456789abcdef01234567",
					"--quote", "Page 1 speaks of one.", "--json"},
				{"evidence", "record", "linear-algebra", "--lesson", "one", "--source", source, "--passage", "0123456789abcdef01234567",
					"--quote", "Page 1 speaks of one.", "--dry-run", "--json"},
			} {
				got := s.say(t, args...)
				if !strings.Contains(got, "exit 1\n") || !strings.Contains(got, `"code": "failed_precondition"`) ||
					!strings.Contains(got, tc.says) {
					t.Errorf("%s\nwant failed_precondition, saying %q", got, tc.says)
				}
			}
			if got := s.say(t, "evidence", "list", "linear-algebra"); !strings.Contains(got, "No Evidence recorded yet.") {
				t.Errorf("Evidence was recorded with %s shut:\n%s", tc.name, got)
			}
			if now, _ := os.ReadFile(s.record); len(started) == 0 || string(now) != string(started) || len(s.calls(t)) != asked {
				t.Errorf("the plugin was started, or asked %v, with %s shut", plugintest.Tools(s.calls(t)[asked:]), tc.name)
			}
		})
	}
}
