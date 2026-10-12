package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// What study-shelf says when it is run by hand: its version, how it is
// registered, and nothing on its standard output but what was asked for,
// since that is where study reads MCP.
func TestTheCommandLine(t *testing.T) {
	// Not started by study: there is no folder to keep the index in.
	t.Setenv(plugincontract.DataFolderEnv, "")
	for name, tc := range map[string]struct {
		args           []string
		code           int
		stdout, stderr []string
	}{
		"the version":         {[]string{"--version"}, 0, []string{"study-shelf version dev\n"}, nil},
		"help":                {[]string{"--help"}, 0, []string{"study knowledge-base add shelf -- study-shelf", "study-shelf --version"}, nil},
		"help, shortly":       {[]string{"-h"}, 0, []string{"Usage:"}, nil},
		"a flag there is not": {[]string{"--data", "/tmp"}, 2, nil, []string{"flag provided but not defined", "Usage:"}},
		"an argument":         {[]string{"serve"}, 2, nil, []string{`it takes no arguments, and was given "serve"`, "Usage:"}},
		"a key":               {[]string{"--api-key=secret"}, 2, nil, []string{"flag provided but not defined"}},
		"an endpoint":         {[]string{"--endpoint=http://127.0.0.1:8080/v1"}, 2, nil, []string{"flag provided but not defined"}},
		"a settings file":     {[]string{"--settings=/tmp/shelf.toml"}, 2, nil, []string{"flag provided but not defined"}},
		"where settings are":  {[]string{"--help"}, 0, []string{"~/.config/lamplight/shelf.toml", "never on this command line"}, nil},
		"started by hand":     {nil, 1, nil, []string{"LAMPLIGHT_PLUGIN_DATA is not set", "study knowledge-base add shelf -- study-shelf"}},
		"and with --stdio":    {[]string{"--stdio"}, 1, nil, []string{"LAMPLIGHT_PLUGIN_DATA is not set"}},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(tc.args, &stdout, &stderr); code != tc.code {
			t.Errorf("%s: it exited with %d, want %d\n%s%s", name, code, tc.code, stdout.String(), stderr.String())
		}
		for _, want := range tc.stdout {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("%s: its standard output lacks %q:\n%s", name, want, stdout.String())
			}
		}
		for _, want := range tc.stderr {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("%s: its standard error lacks %q:\n%s", name, want, stderr.String())
			}
		}
		if len(tc.stdout) == 0 && stdout.Len() != 0 {
			t.Errorf("%s: it wrote on its standard output, which is study's to read MCP from:\n%s", name, stdout.String())
		}
	}
}

// The settings are read from the file in the configuration folder its
// environment names, and from nowhere else: a file it cannot use ends
// study-shelf before it speaks MCP, with the reason and no word of what the
// file holds, and a key or an address in its environment is nothing to it.
func TestASettingsFileItCannotUseEndsIt(t *testing.T) {
	const key = "sk-main-test-0c7d3a"
	home, folder := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(plugincontract.DataFolderEnv, folder)
	// What a learner's shell may hold for other programs.
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/v1")
	config := filepath.Join(home, ".config", "lamplight")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config, "shelf.toml")
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"a Recipe it does not have": {"format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"another\"\nkey = \"" + key + "\"\n",
			[]string{"names a Recipe this Shelf does not have"}},
		"a line that is no TOML": {"format = 1\n[embedding]\nkey = " + key + "\n", []string{"is not valid TOML at line 3"}},
		"a setting from its environment's name": {"format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nOPENAI_API_KEY = \"" + key + "\"\n",
			[]string{`the setting "OPENAI_API_KEY" in [embedding]`, "does not know"}},
	} {
		if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run(nil, &stdout, &stderr); code != 1 {
			t.Errorf("%s: it exited with %d, want 1\n%s", name, code, stderr.String())
		}
		for _, want := range append(tc.want, "study-shelf: Shelf's settings file, "+path, "docs/shelf.md") {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("%s: its standard error lacks %q:\n%s", name, want, stderr.String())
			}
		}
		if strings.Contains(stderr.String(), key) {
			t.Errorf("%s: it repeats what the file holds:\n%s", name, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: it wrote on its standard output, which is study's to read MCP from:\n%s", name, stdout.String())
		}
	}
	if left, _ := os.ReadDir(folder); len(left) != 0 {
		t.Errorf("a study-shelf that did not start left %v in its folder", left)
	}
}
