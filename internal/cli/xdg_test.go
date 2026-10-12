package cli_test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// TestATopicCannotChooseTheStudyHome starts study inside a Topic that holds
// rel/lamplight/config.toml, with XDG_CONFIG_HOME=rel and no STUDY_HOME. A
// relative XDG folder is ignored, as the specification says, so the file the
// agent could write there does not decide where the Study home is.
func TestATopicCannotChooseTheStudyHome(t *testing.T) {
	home := t.TempDir()
	if r := runEnv(t, map[string]string{"HOME": home}, home, nil, "topic", "create", "--title", "Rust"); r.code != cli.ExitOK {
		t.Fatalf("setup: exit %d, %s", r.code, r.stderr)
	}
	// Another Study home, with a Topic to tell it by.
	other := t.TempDir()
	if r := runEnv(t, map[string]string{"HOME": home, "STUDY_HOME": other}, home, nil, "topic", "create", "--title", "Planted"); r.code != cli.ExitOK {
		t.Fatalf("setup: exit %d, %s", r.code, r.stderr)
	}
	topic := filepath.Join(home, "study", "rust")
	writeFile(t, filepath.Join(topic, "rel", "lamplight", "config.toml"), fmt.Sprintf("format = 1\nstudy_home = %q\n", other))
	// A relative path is one from the folder the process is in.
	t.Chdir(topic)

	status := func(env map[string]string) (studyHome string, topics []string) {
		t.Helper()
		data := decodeData(t, runEnv(t, env, topic, nil, "status", "--json"))
		for _, topic := range data["topics"].([]any) {
			topics = append(topics, topic.(map[string]any)["id"].(string))
		}
		return data["study_home"].(string), topics
	}
	for _, value := range []string{"rel", "./rel", "../rust/rel"} {
		studyHome, topics := status(map[string]string{"HOME": home, "XDG_CONFIG_HOME": value})
		if studyHome != "$HOME/study" || !slices.Equal(topics, []string{"rust"}) {
			t.Errorf("XDG_CONFIG_HOME=%s: the Study home is %s with Topics %v; want $HOME/study with rust", value, studyHome, topics)
		}
	}
	// The same file, in an absolute XDG_CONFIG_HOME, is the learner's
	// configuration and is read.
	studyHome, topics := status(map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(topic, "rel")})
	if studyHome != other || !slices.Equal(topics, []string{"planted"}) {
		t.Errorf("an absolute XDG_CONFIG_HOME: the Study home is %s with Topics %v; want %s with planted", studyHome, topics, other)
	}
	// A relative HOME is not used either: nothing is looked for under it,
	// and the command says why it cannot go on. (runEnv writes $HOME for
	// the value of HOME.)
	writeFile(t, filepath.Join(topic, "rel", ".config", "lamplight", "config.toml"), fmt.Sprintf("format = 1\nstudy_home = %q\n", other))
	r := runEnv(t, map[string]string{"HOME": "rel"}, topic, nil, "status", "--json")
	if r.code != cli.ExitUsage || errorCode(t, r) != "invalid_argument" || !strings.Contains(r.stdout, `HOME is \"$HOME\", which is not an absolute path`) {
		t.Errorf("a relative HOME: exit %d, %s", r.code, r.stdout)
	}
}

// TestDoctorWarnsAboutARelativeXDGFolder: study doctor says which variable
// is ignored, what is used instead, and how to fix it.
func TestDoctorWarnsAboutARelativeXDGFolder(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": "rel", "XDG_STATE_HOME": "state"}
	if r := runEnv(t, env, home, nil, "topic", "create", "--title", "Rust"); r.code != cli.ExitOK {
		t.Fatalf("setup: exit %d, %s", r.code, r.stderr)
	}
	fakeGit(t, "Ada Lovelace", "ada@example.com")

	human := runEnv(t, env, home, nil, "doctor")
	if human.code != cli.ExitOK {
		t.Errorf("doctor: exit %d, stderr %s", human.code, human.stderr)
	}
	golden(t, "doctor_relative_xdg.txt", human.stdout)

	data := decodeData(t, runEnv(t, env, home, nil, "doctor", "--json"))
	f := finding(t, data, "environment")
	if f["status"] != "warn" ||
		f["message"] != `XDG_CONFIG_HOME is "rel", which is not an absolute path, so study ignores it and uses its default under your home folder; `+
			`XDG_STATE_HOME is "state", which is not an absolute path, so study ignores it and uses its default under your home folder` ||
		f["fix"] != "set XDG_CONFIG_HOME to an absolute path, or unset it; set XDG_STATE_HOME to an absolute path, or unset it" {
		t.Errorf("the environment Finding = %v", f)
	}
	if data["findings"].([]any)[0].(map[string]any)["name"] != "environment" {
		t.Errorf("the environment Finding is not the first: %v", data["findings"])
	}
	if msg := finding(t, data, "config")["message"]; msg != "no $HOME/.config/lamplight/config.toml; using the defaults" {
		t.Errorf("the config Finding = %v", msg)
	}
	if msg, _ := finding(t, data, "log")["message"].(string); !strings.HasPrefix(msg, "$HOME/.local/state/lamplight/study.log") {
		t.Errorf("the log Finding = %v", msg)
	}

	// A relative HOME is reported too; nothing is found under it. (runEnv
	// writes $HOME for the value of HOME.)
	code, data := failure(t, runEnv(t, map[string]string{"HOME": "rel"}, home, nil, "doctor", "--json"))
	if code != "unhealthy" {
		t.Errorf("doctor with a relative HOME: %s", code)
	}
	f = finding(t, data, "environment")
	if f["status"] != "warn" ||
		f["message"] != `HOME is "$HOME", which is not an absolute path, so study does not use it and cannot find your home folder` ||
		f["fix"] != "set HOME to the absolute path of your home folder" {
		t.Errorf("the environment Finding with a relative HOME = %v", f)
	}
	if f := finding(t, data, "study_home"); f["status"] != "fail" {
		t.Errorf("the study_home Finding with a relative HOME = %v", f)
	}

	// With absolute folders there is no such Finding.
	data = decodeData(t, runEnv(t, map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}, home, nil, "doctor", "--json"))
	for _, f := range data["findings"].([]any) {
		if f.(map[string]any)["name"] == "environment" {
			t.Errorf("an environment Finding with nothing to warn about: %v", f)
		}
	}
}

// TestCompletionAsksForHomeOnlyWhenItNeedsIt: an install whose every path is
// absolute works whatever HOME is, and one that needs the home folder fails
// on a relative HOME, names it, and writes nothing in the folder study
// started in.
func TestCompletionAsksForHomeOnlyWhenItNeedsIt(t *testing.T) {
	work := t.TempDir()
	// A relative path is one from the folder the process is in.
	t.Chdir(work)
	config, state, zfunc := t.TempDir(), t.TempDir(), t.TempDir()
	record := filepath.Join(state, "lamplight", "completions.json")

	for _, tc := range []struct {
		name   string
		env    map[string]string
		args   []string
		script string
	}{
		{"fish, with XDG_CONFIG_HOME and XDG_STATE_HOME absolute",
			map[string]string{"HOME": "rel", "SHELL": "/usr/bin/fish", "XDG_CONFIG_HOME": config, "XDG_STATE_HOME": state},
			nil, filepath.Join(config, "fish", "completions", "study.fish")},
		{"zsh, with --dir and XDG_STATE_HOME absolute",
			map[string]string{"HOME": "rel", "SHELL": "/bin/zsh", "XDG_STATE_HOME": state},
			[]string{"--dir", zfunc}, filepath.Join(zfunc, "_study")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			install := append([]string{"completion", "install", "--json"}, tc.args...)
			if r := runEnv(t, tc.env, work, nil, install...); r.code != cli.ExitOK {
				t.Fatalf("install: exit %d, %s%s", r.code, r.stdout, r.stderr)
			}
			if !exists(tc.script) || !exists(record) {
				t.Errorf("after install: script %v, record %v", exists(tc.script), exists(record))
			}
			if r := runEnv(t, tc.env, work, nil, "completion", "uninstall", "--json"); r.code != cli.ExitOK {
				t.Fatalf("uninstall: exit %d, %s%s", r.code, r.stdout, r.stderr)
			}
			if exists(tc.script) || exists(record) {
				t.Errorf("after uninstall: script %v, record %v", exists(tc.script), exists(record))
			}
		})
	}

	// What needs the home folder says so. (runEnv writes $HOME for the
	// value of HOME.)
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"fish, its folder by default", map[string]string{"HOME": "rel", "SHELL": "/usr/bin/fish", "XDG_STATE_HOME": state}, nil},
		{"bash, its folder by default", map[string]string{"HOME": "rel", "SHELL": "/bin/bash", "XDG_STATE_HOME": state}, nil},
		{"zsh, a --dir in the home folder", map[string]string{"HOME": "rel", "SHELL": "/bin/zsh", "XDG_STATE_HOME": state}, []string{"--dir", "~/.zfunc"}},
		{"zsh, its rc file", map[string]string{"HOME": "rel", "SHELL": "/bin/zsh", "XDG_STATE_HOME": state, "XDG_DATA_HOME": config}, []string{"--yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runEnv(t, tc.env, work, nil, append([]string{"completion", "install", "--json"}, tc.args...)...)
			if r.code == cli.ExitOK || !strings.Contains(r.stdout, `cannot find your home folder: HOME is \"$HOME\", which is not an absolute path`) {
				t.Errorf("install: exit %d, %s%s", r.code, r.stdout, r.stderr)
			}
		})
	}
	if left := tree(t, work); !slices.Equal(left, []string{"."}) {
		t.Errorf("the folder study started in holds %v", left)
	}
	if exists(record) {
		t.Error("a failed install left its record")
	}
}
