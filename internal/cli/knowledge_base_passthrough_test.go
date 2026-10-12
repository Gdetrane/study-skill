//go:build linux || darwin

package cli_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// sharedComputer is a learner's computer as a shared one is laid out: the
// home folder in a folder of home folders, the Study home in a folder
// beside it, and the test plugin's program installed in a folder of its
// own, which is the one folder on PATH.
type sharedComputer struct {
	*kbComputer
	base, study, bin string
}

// folders are the folders on the way to the configuration folder, to the
// plugin's program, to the Study home and to the plugin's own folder, each
// before the ones inside it.
func (s *sharedComputer) folders() []string {
	return []string{s.base, filepath.Dir(s.home), s.home, filepath.Dir(s.study), filepath.Join(s.base, "opt"),
		filepath.Dir(s.bin), s.bin}
}

func newSharedComputer(t *testing.T) *sharedComputer {
	t.Helper()
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	s := &sharedComputer{base: base, study: filepath.Join(base, "classes", "study"), bin: filepath.Join(base, "opt", "kb", "bin")}
	home := filepath.Join(base, "home", "ada")
	s.kbComputer = &kbComputer{home: home, env: map[string]string{"HOME": home, "STUDY_HOME": s.study, "PATH": s.bin}}
	writeFile(t, filepath.Join(s.study, "go-concurrency", "kb.py"), "print()\n")
	s.program(t, filepath.Join(s.study, "go-concurrency", "kb"))
	for _, dir := range []string{home, s.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The plugin's program is this test binary, as a file of its own there.
	in, err := os.Open(plugintest.Command()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(s.bin, "notes-kb"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return s
}

// say runs study on the computer and returns what came of it, with the
// computer's folders named so that two computers can be compared.
func (s *sharedComputer) say(t *testing.T, args ...string) string {
	t.Helper()
	r := s.check(t, 20*time.Second, args...)
	text := fmt.Sprintf("$ study %s\nexit %d\n%s%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	return strings.ReplaceAll(text, s.base, "$BASE")
}

// story is everything study knowledge-base does on the computer, one
// command after the other.
func (s *sharedComputer) story(t *testing.T) string {
	t.Helper()
	script := filepath.Join(s.study, "go-concurrency", "kb.py")
	var b strings.Builder
	for _, args := range [][]string{
		{"knowledge-base", "list", "--json"},
		{"knowledge-base", "add", "kb", "--dry-run", "--json", "--", "notes-kb", plugintest.Arg, "--name", "below"},
		{"knowledge-base", "add", "kb", "--json", "--", "notes-kb", plugintest.Arg, "--name", "below"},
		{"knowledge-base", "add", "kb", "--json", "--", "~/../../opt/kb/bin/notes-kb"},
		{"knowledge-base", "add", "remote", "--json", "--url", "http://localhost:8765/mcp"},
		{"knowledge-base", "add", "agent", "--json", "--", filepath.Join(s.study, "go-concurrency", "kb")},
		{"knowledge-base", "add", "script", "--json", "--", "notes-kb", "-u", script},
		{"knowledge-base", "add", "script", "--allow-study-home-arguments", "--json", "--", "notes-kb", "-u", script},
		{"knowledge-base", "list", "--json"},
		{"knowledge-base", "list"},
		{"knowledge-base", "check", "kb", "--json"},
		{"knowledge-base", "check", "kb"},
		{"knowledge-base", "remove", "remote", "--dry-run", "--json"},
		{"knowledge-base", "remove", "remote", "--json"},
		{"knowledge-base", "remove", "kb"},
		{"knowledge-base", "check", "kb", "--json"},
		{"knowledge-base", "list", "--json"},
	} {
		b.WriteString(s.say(t, args...))
	}
	return b.String()
}

// study knowledge-base works below folders the learner may pass through and
// not list, as /home is on many shared computers (mode 711), and says there
// exactly what it says anywhere: above Lamplight's configuration folder,
// above the plugin's program and around it, above the Study home, and above
// the folder the plugin is given (#92). Before, every one of these commands
// failed there with "study cannot reach Lamplight's configuration folder".
func TestKnowledgeBaseBelowFoldersThatMayNotBeListed(t *testing.T) {
	listed := newSharedComputer(t).story(t)
	for _, want := range []string{
		"$ study knowledge-base add kb --json -- notes-kb lamplight-test-plugin --name below\nexit 0\n",
		`"$BASE/opt/kb/bin/notes-kb"`,
		`"message": "$BASE/classes/study/go-concurrency/kb is inside the Study home, $BASE/classes/study, or is reached through it`,
		`names something inside the Study home, $BASE/classes/study, or reached through it`,
		`"allow_study_home_arguments": true`,
		"$ study knowledge-base check kb --json\nexit 0\n",
		`"data_folder": "$HOME/.local/state/lamplight/knowledge-base-plugins/kb"`,
		`"name": "below"`,
		"$ study knowledge-base remove kb\nexit 0\n",
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("on a computer whose folders may all be listed, study knowledge-base does not say %q:\n%s", want, listed)
		}
	}

	s := newSharedComputer(t)
	plugintest.PassThrough(t, s.folders()...)
	if passed := s.story(t); passed != listed {
		t.Errorf("below folders that may not be listed:\n%s\nbelow folders that may be:\n%s", passed, listed)
	}
	// The plugin's folder was made, below the home folder, for the learner
	// alone.
	folder := filepath.Join(s.home, ".local", "state", "lamplight", "knowledge-base-plugins", "kb")
	if info, err := os.Stat(folder); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the plugin's folder: %v, %v; want one for the learner alone", info, err)
	}
}

// Below a folder the learner may not even pass through, study cannot tell
// where a path leads, and takes nothing there for outside the Study home:
// it says what it could not follow, and why.
func TestKnowledgeBaseRefusesWhatItCannotFollow(t *testing.T) {
	s := newSharedComputer(t)
	shut := filepath.Join(s.base, "shut")
	writeFile(t, filepath.Join(shut, "kb.toml"), "port = 8765\n")
	s.program(t, filepath.Join(shut, "bin", "kb"))
	s.must(t, "knowledge-base", "add", "kb", "--", "notes-kb", plugintest.Arg)
	plugintest.Shut(t, shut)

	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{name: "an argument", args: []string{"knowledge-base", "add", "notes", "--json", "--", "notes-kb", "--config=" + filepath.Join(shut, "kb.toml")},
			want: "names a path study cannot follow to its end (permission denied)"},
		{name: "a program", args: []string{"knowledge-base", "add", "notes", "--json", "--", filepath.Join(shut, "bin", "kb")},
			want: "study cannot follow $BASE/shut/bin/kb to check the program (permission denied)"},
		{name: "the configuration folder", env: map[string]string{"XDG_CONFIG_HOME": filepath.Join(shut, "config")},
			args: []string{"knowledge-base", "list", "--json"}, want: "study cannot reach Lamplight's configuration folder"},
		{name: "the plugin's folder", env: map[string]string{"XDG_STATE_HOME": filepath.Join(shut, "state")},
			args: []string{"knowledge-base", "check", "kb", "--json"}, want: "study cannot make the folder Knowledge base plugin kb keeps its index in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := map[string]string{}
			for name, value := range tc.env {
				before[name] = s.env[name]
				s.env[name] = value
			}
			defer func() {
				for name, value := range before {
					if value == "" {
						delete(s.env, name)
					} else {
						s.env[name] = value
					}
				}
			}()
			got := s.say(t, tc.args...)
			if !strings.Contains(got, fmt.Sprintf("exit %d\n", cli.ExitError)) || !strings.Contains(got, `"code": "failed_precondition"`) ||
				!strings.Contains(got, tc.want) {
				t.Errorf("%s\nwant failed_precondition, saying %q", got, tc.want)
			}
		})
	}
	// Allowed by the learner, the argument is registered, and the listing
	// has nothing to say against it.
	s.must(t, "knowledge-base", "add", "notes", "--allow-study-home-arguments", "--", "notes-kb", "--config="+filepath.Join(shut, "kb.toml"))
	if list := s.say(t, "knowledge-base", "list", "--json"); !strings.Contains(list, `"allow_study_home_arguments": true`) || strings.Contains(list, `"problem"`) {
		t.Errorf("the listing after the argument was allowed:\n%s", list)
	}
}
