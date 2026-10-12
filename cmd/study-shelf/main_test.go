package main

import (
	"bytes"
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
