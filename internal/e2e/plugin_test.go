//go:build unix

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// pluginComputer is a learner's computer for a built study that starts a
// Knowledge base plugin: a home folder with Lamplight's configuration
// folder, a Study home apart from it with a Topic in it, and the test
// plugin registered as kb.
type pluginComputer struct {
	t                  *testing.T
	study              string // the built study
	home, topic, input string
	record             string
}

func newPluginComputer(t *testing.T, args ...string) *pluginComputer {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the study binary")
	}
	// Built before HOME moves, so the build uses the usual module cache.
	bin := buildStudy(t)
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	c := &pluginComputer{t: t, study: bin, home: filepath.Join(base, "home"), record: filepath.Join(base, "record.json")}
	c.topic = filepath.Join(c.home, "study", "linear-algebra")
	c.input = filepath.Join(base, "stdin")
	for _, dir := range []string{c.topic, filepath.Join(c.home, ".config")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(c.input, []byte(`{"jsonrpc":"2.0","id":1,"method":"the agent's messages, which are not the plugin's to read"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := plugintest.Command(append([]string{"--record", c.record}, args...)...)
	if out, err := c.command("knowledge-base", append([]string{"add", "kb", "--"}, command...)...).CombinedOutput(); err != nil {
		t.Fatalf("study knowledge-base add: %v\n%s", err, out)
	}
	return c
}

// command is the built study, started inside the Topic, as an agent or the
// MCP server's harness starts it: with a standard input that is its own to
// read, and a git environment such as a hook leaves.
func (c *pluginComputer) command(group string, args ...string) *exec.Cmd {
	c.t.Helper()
	cmd := exec.Command(c.study, append([]string{group}, args...)...)
	cmd.Dir = c.topic
	cmd.Env = []string{"HOME=" + c.home, "STUDY_HOME=" + filepath.Join(c.home, "study"), "PATH=" + os.Getenv("PATH"),
		"XDG_STATE_HOME=" + filepath.Join(c.home, ".state"), "GIT_DIR=" + filepath.Join(c.topic, ".git"), "GIT_INDEX_FILE=/nowhere/index"}
	stdin, err := os.Open(c.input)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { stdin.Close() })
	cmd.Stdin = stdin
	return cmd
}

// A built study, started inside a Topic with the agent's messages on its
// standard input, starts a registered plugin apart from all of that, and
// has stopped it when it exits.
func TestABuiltStudyStartsAPluginApartFromItself(t *testing.T) {
	c := newPluginComputer(t)
	cmd := c.command("knowledge-base", "check", "kb", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("study knowledge-base check: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	var env struct {
		OK   bool             `json:"ok"`
		Data core.PluginCheck `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || !env.OK || env.Data.Contract.Version != 1 {
		t.Fatalf("stdout = %s (%v)", stdout.String(), err)
	}

	got := plugintest.ReadRecord(t, c.record)
	defer plugintest.Kill(got.PID)
	config := filepath.Join(c.home, ".config", "lamplight")
	if got.Dir != config {
		t.Errorf("the plugin was started in %s, want the registry's folder, %s: never the Topic study was started in, %s", got.Dir, config, c.topic)
	}
	// Its standard input is a pipe of its own, not the file study has.
	input, err := os.Stat(c.input)
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := input.Sys().(*syscall.Stat_t); !got.Stdin.Pipe || (ok && uint64(st.Ino) == got.Stdin.Inode && uint64(st.Dev) == got.Stdin.Device) {
		t.Errorf("the plugin's standard input is study's own, or no pipe: %+v", got.Stdin)
	}
	for name := range got.Env {
		if strings.HasPrefix(name, "GIT_") {
			t.Errorf("the plugin was given %s", name)
		}
	}
	if want := filepath.Join(c.home, "study", ".lamplight", "knowledge-base-plugins", "kb"); got.Env["LAMPLIGHT_PLUGIN_DATA"] != want || env.Data.DataFolder != want {
		t.Errorf("the plugin's folder is %q, and study says %q; want %q", got.Env["LAMPLIGHT_PLUGIN_DATA"], env.Data.DataFolder, want)
	}
	if !got.Leader || got.Terminal {
		t.Errorf("the plugin leads a session of its own: %v; it has a terminal: %v", got.Leader, got.Terminal)
	}
	// study has exited, and the plugin with it.
	if !plugintest.Gone(got.PID, 5*time.Second) {
		t.Errorf("the plugin, process %d, outlived study", got.PID)
	}
}

// SIGTERM to study, as a harness sends when the agent's turn is cancelled,
// stops the request and the plugin: one that will not stop by itself, and
// what it started.
func TestSIGTERMStopsAPlugin(t *testing.T) {
	child := filepath.Join(t.TempDir(), "child.json")
	c := newPluginComputer(t, "--hang", "start", "--stubborn", "--child", child)
	cmd := c.command("knowledge-base", "check", "kb", "--json")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	plugin, started := plugintest.ReadRecord(t, c.record), plugintest.ReadRecord(t, child)
	defer plugintest.Kill(plugin.PID)
	defer plugintest.Kill(started.PID)
	began := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	// It asks the plugin to exit, signals it, and kills it: two grace
	// periods of two seconds for a plugin that ignores both.
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("study took %s to stop after SIGTERM", took)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("exit = %v, want status 1", err)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || env.OK || env.Error.Code != string(core.CodeCanceled) {
		t.Errorf("stdout = %s (%v), want a canceled error", stdout.String(), err)
	}
	for what, pid := range map[string]int{"the plugin": plugin.PID, "the program the plugin started": started.PID} {
		if !plugintest.Gone(pid, 5*time.Second) {
			t.Errorf("%s, process %d, outlived study", what, pid)
		}
	}
}

// A study that is killed can stop nothing. Its end of the plugin's standard
// input closes with it, which is what tells a plugin to exit.
func TestAPluginExitsWhenStudyIsKilled(t *testing.T) {
	c := newPluginComputer(t, "--wait", "contract")
	cmd := c.command("knowledge-base", "check", "kb", "--json")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	plugin := plugintest.ReadRecord(t, c.record)
	defer plugintest.Kill(plugin.PID)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if !plugintest.Gone(plugin.PID, 5*time.Second) {
		t.Errorf("the plugin, process %d, went on after study was killed", plugin.PID)
	}
}
