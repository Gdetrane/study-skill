//go:build linux

package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A Topic's id is its folder's name, and a folder of the Study home is a
// Topic whatever someone called it: the agent can call one anything. On a
// terminal with colours, where nothing strips what study prints, status
// shows such a name quoted, and never as the terminal would act on it.
func TestStatusKeepsAFoldersNameOffTheTerminal(t *testing.T) {
	bin := buildStudy(t)
	home := t.TempDir()
	env := append(ownFolders(t, home), "NO_COLOR=1")
	create := exec.CommandContext(t.Context(), bin, "topic", "create", "--title", "C", "--id", "c")
	create.Env = env
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("study topic create: %v\n%s", err, out)
	}
	const hostile = "e\x1b[31mRED\x1b]0;owned\x07"
	if err := os.Rename(filepath.Join(home, "c"), filepath.Join(home, hostile)); err != nil {
		t.Fatal(err)
	}
	// Run in that folder, it is the Active topic too.
	term := startOnTerminalAs(t, bin, home, []string{"TERM=xterm-256color", "COLORTERM=truecolor"}, "status")
	if code := term.wait(); code != 0 {
		t.Fatalf("study status exited with %d:\n%q", code, term.output())
	}
	out := term.output()
	if !strings.Contains(out, "Topics:") || !strings.Contains(out, "RED") {
		t.Fatalf("study status does not list the Topic:\n%q", out)
	}
	for _, raw := range []string{"\x1b[31mRED", "\x1b]0;owned", "\x07"} {
		if strings.Contains(out, raw) {
			t.Errorf("study status wrote %q of a folder's name to the terminal as it is:\n%q", raw, out)
		}
	}
}
