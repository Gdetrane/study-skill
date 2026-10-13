package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// loopback is the address of a service on this computer, whatever its port.
var loopback = regexp.MustCompile(`127\.0\.0\.1:\d+`)

// check runs study on the computer as run does, with a plugin started by a
// command getting the computer's environment and not this test's, and with
// start as how long a plugin has to start. It replaces what differs from
// one run to the next: the home folder, this test binary, which is the
// plugin's program, and the port of a service on this computer.
func (k *kbComputer) check(t *testing.T, start time.Duration, args ...string) result {
	t.Helper()
	var n atomic.Int64
	opts := core.Options{
		Getenv: func(key string) string { return k.env[key] },
		Dir:    k.home,
		Now:    func() time.Time { return fixedNow },
		NewID:  func() string { return fmt.Sprintf("id%03d", n.Add(1)) },
		Plugins: core.PluginOptions{
			Environ:         func() []string { return []string{"HOME=" + k.home, "STUDY_HOME=" + k.env["STUDY_HOME"]} },
			StartTimeout:    start,
			StopGrace:       300 * time.Millisecond,
			DiagnoseTimeout: k.diagnose,
		},
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr, opts)
	norm := func(s string) string {
		if resolved, err := filepath.EvalSymlinks(k.home); err == nil {
			s = strings.ReplaceAll(s, resolved, "$HOME")
		}
		s = strings.ReplaceAll(s, k.home, "$HOME")
		s = strings.ReplaceAll(s, plugintest.Command()[0], "$PLUGIN")
		return loopback.ReplaceAllString(s, "127.0.0.1:PORT")
	}
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

// withTestPlugin is a computer where the test plugin is registered as kb,
// started with args.
func withTestPlugin(args ...string) func(*testing.T) *kbComputer {
	return func(t *testing.T) *kbComputer {
		t.Helper()
		k := newKBComputer(t)
		k.must(t, append([]string{"knowledge-base", "add", "kb", "--"}, plugintest.Command(args...)...)...)
		return k
	}
}

// withASilentPlugin is a computer where the test plugin is registered as kb,
// and says which contract it speaks and nothing of what it found of what it
// depends on. It has less than half a second for that.
func withASilentPlugin(t *testing.T) *kbComputer {
	t.Helper()
	k := withTestPlugin("--hang", "diagnose", "--stderr", "testing the embedding endpoint")(t)
	k.diagnose = 400 * time.Millisecond
	return k
}

// The findings of a plugin that depends on two endpoints, one of which is
// down: as the test plugin is told to give them.
var twoFindings = []string{
	"--finding", "embedding=ok:the embedding endpoint 127.0.0.1:8080 gave a question's vector, of 512 numbers, asked for the model embeddinggemma-2",
	"--finding", "reranking=wrong:the reranking endpoint 127.0.0.1:8081 could not be reached: connection refused. Check that its server is running, and its address in Shelf's settings file. Without the reranking endpoint a search gives its " +
		"Passages in the order it found them.",
}

// withService is a computer where a service on this computer, which answers
// as handler does, is registered as remote.
func withService(handler func(t *testing.T) http.Handler) func(*testing.T) *kbComputer {
	return func(t *testing.T) *kbComputer {
		t.Helper()
		k := newKBComputer(t)
		// The server closes after whatever the handler leaves to clean up.
		var server *httptest.Server
		t.Cleanup(func() { server.Close() })
		server = httptest.NewServer(handler(t))
		k.must(t, "knowledge-base", "add", "remote", "--url", server.URL+"/t0ken-in-the-path/mcp?key=s3cr3t")
		return k
	}
}

// aPlugin serves the test plugin.
func aPlugin(cfg plugintest.Config) func(t *testing.T) http.Handler {
	return func(t *testing.T) http.Handler {
		cfg.Folder = t.TempDir()
		plugin := plugintest.New(cfg)
		t.Cleanup(plugin.Close)
		return plugin.Handler()
	}
}

// withNobodyAnswering is a computer where remote is registered at an
// address nothing answers at: the port was free a moment ago.
func withNobodyAnswering(t *testing.T) *kbComputer {
	t.Helper()
	k := newKBComputer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "http://" + listener.Addr().String() + "/t0ken-in-the-path/mcp?key=s3cr3t"
	listener.Close()
	k.must(t, "knowledge-base", "add", "remote", "--url", address)
	return k
}

// withThePluginInTheStudyHome is withTestPlugin run with a Study home that
// holds the plugin's program: the Study home was another when it was
// registered.
func withThePluginInTheStudyHome(t *testing.T) *kbComputer {
	t.Helper()
	k := newKBComputer(t)
	program := filepath.Join(k.home, "bin", "kb")
	if err := os.Symlink(plugintest.Command()[0], program); err != nil {
		t.Fatal(err)
	}
	k.must(t, "knowledge-base", "add", "kb", "--", program, plugintest.Arg)
	k.env["STUDY_HOME"] = filepath.Join(k.home, "bin")
	return k
}

func TestKnowledgeBaseCheckJSON(t *testing.T) {
	const patient, impatient = 20 * time.Second, 400 * time.Millisecond
	for _, tc := range []struct {
		golden   string
		computer func(*testing.T) *kbComputer
		start    time.Duration
		name     string
		code     int
	}{
		{"knowledge_base_check", withTestPlugin("--name", "test-plugin", "--version", "1.2.3"), patient, "kb", cli.ExitOK},
		{"knowledge_base_check_files_only", withTestPlugin("--sources", "file"), patient, "kb", cli.ExitOK},
		{"knowledge_base_check_url", withService(aPlugin(plugintest.Config{Name: "a-service", Version: "0.9"})), patient, "remote", cli.ExitOK},
		{"knowledge_base_check_later_kinds", withTestPlugin("--contract",
			`{"version": 1, "sources": ["file", "recording"], "search": ["embedding", "keyword"], "locations": ["pages", "time"]}`), patient, "kb", cli.ExitOK},
		// What the plugin found of what it depends on is part of the answer,
		// and a finding that is not all right is no failure of the check: the
		// plugin starts and speaks the contract.
		{"knowledge_base_check_findings", withTestPlugin(twoFindings...), patient, "kb", cli.ExitOK},
		{"knowledge_base_check_url_findings", withService(aPlugin(plugintest.Config{Findings: []plugincontract.Finding{
			{Name: "upstream", Message: "the index behind this service has not answered since Tuesday"}}})), patient, "remote", cli.ExitOK},
		// A plugin that does not say what it found in the time it has for
		// that, or says what is no finding, does not speak the contract.
		{"knowledge_base_check_no_findings_in_time", withASilentPlugin, patient, "kb", cli.ExitError},
		{"knowledge_base_check_broken_findings", withTestPlugin("--result", `diagnose={"findings": [{"name": "embedding", "ok": true}]}`),
			patient, "kb", cli.ExitError},
		{"knowledge_base_check_unknown", withPlugins, patient, "notes", cli.ExitError},
		{"knowledge_base_check_bad_name", withPlugins, patient, "My Shelf", cli.ExitUsage},
		{"knowledge_base_check_does_not_start", withTestPlugin("--exit", "3", "--stderr", "cannot open the index: disk full"), patient, "kb", cli.ExitError},
		{"knowledge_base_check_garbage", withTestPlugin("--garbage"), patient, "kb", cli.ExitError},
		{"knowledge_base_check_hangs", withTestPlugin("--hang", "start"), impatient, "kb", cli.ExitError},
		{"knowledge_base_check_newer_contract", withTestPlugin("--contract", `{"version": 2}`), patient, "kb", cli.ExitError},
		{"knowledge_base_check_not_the_contract", withTestPlugin("--tools", "status,search"), patient, "kb", cli.ExitError},
		{"knowledge_base_check_broken_contract", withTestPlugin("--contract", `{"version": 1, "sources": ["file"], "search": [], "locations": []}`), patient, "kb", cli.ExitError},
		{"knowledge_base_check_program_in_study_home", withThePluginInTheStudyHome, patient, "kb", cli.ExitError},
		{"knowledge_base_check_nobody_answers", withNobodyAnswering, patient, "remote", cli.ExitError},
		{"knowledge_base_check_url_hangs", withService(func(t *testing.T) http.Handler {
			// It answers nothing until the test is over, and then lets its
			// server close.
			over := make(chan struct{})
			t.Cleanup(func() { close(over) })
			return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-over })
		}), impatient, "remote", cli.ExitError},
		{"knowledge_base_check_url_newer_contract", withService(aPlugin(plugintest.Config{Results: map[string]string{"contract": `{"version": 3}`}})),
			patient, "remote", cli.ExitError},
		// A contract of a version no number of this program's holds is a
		// newer one all the same.
		{"knowledge_base_check_far_newer_contract", withTestPlugin("--contract", `{"version": 1e30}`), patient, "kb", cli.ExitError},
		// A plugin answers, and asks study nothing.
		{"knowledge_base_check_sends_requests", withTestPlugin("--ask", "start"), patient, "kb", cli.ExitError},
		// A service that refuses says why, and study repeats it without what
		// the service repeats of its address.
		{"knowledge_base_check_url_refuses", withService(func(*testing.T) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Unsupported protocol version (supported versions: 2026-07-28), asked at "+r.URL.String(), http.StatusBadRequest)
			})
		}), patient, "remote", cli.ExitError},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			k := tc.computer(t)
			began := time.Now()
			got := k.check(t, tc.start, "knowledge-base", "check", tc.name, "--json")
			if took := time.Since(began); took > 10*time.Second {
				t.Errorf("study knowledge-base check took %s", took)
			}
			if got.code != tc.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, tc.code, got.stderr)
			}
			if got.stderr != "" {
				t.Errorf("--json wrote to stderr: %q", got.stderr)
			}
			// Whatever came of it, only the scheme and host of an address
			// are shown: the rest may be a key.
			if strings.Contains(got.stdout, "s3cr3t") || strings.Contains(got.stdout, "t0ken") {
				t.Errorf("study knowledge-base check repeats the address: %s", got.stdout)
			}
			golden(t, tc.golden+".json", got.stdout)
		})
	}
	if r := newKBComputer(t).run(t, "knowledge-base", "check", "--json"); r.code != cli.ExitUsage || !strings.Contains(r.stdout, `"code": "usage"`) {
		t.Errorf("study knowledge-base check without a name: exit %d, stdout %s", r.code, r.stdout)
	}
}

func TestKnowledgeBaseCheckHumanOutput(t *testing.T) {
	k := withTestPlugin("--version", "1.2.3")(t)
	golden(t, "knowledge_base_check.txt", k.check(t, 20*time.Second, "knowledge-base", "check", "kb").stdout)
	remote := withService(aPlugin(plugintest.Config{Name: "a-service", Version: "0.9", Sources: []string{"url"}}))(t)
	reached := remote.check(t, 20*time.Second, "knowledge-base", "check", "remote")
	if strings.Contains(reached.stdout, "s3cr3t") || strings.Contains(reached.stdout, "t0ken") {
		t.Errorf("study knowledge-base check repeats the address: %s", reached.stdout)
	}
	golden(t, "knowledge_base_check_url.txt", reached.stdout)
	// What the plugin found of what it depends on, under what it says of
	// itself: a line for each finding, and the check succeeds whatever they
	// say.
	found := withTestPlugin(twoFindings...)(t).check(t, 20*time.Second, "knowledge-base", "check", "kb")
	if found.code != cli.ExitOK || found.stderr != "" {
		t.Errorf("a plugin with a finding that is not all right: exit %d, stderr %q", found.code, found.stderr)
	}
	golden(t, "knowledge_base_check_findings.txt", found.stdout)
	// One that never says what it found fails the check, with how long it
	// had and what it last wrote.
	silent := withASilentPlugin(t).check(t, 20*time.Second, "knowledge-base", "check", "kb")
	if silent.code != cli.ExitError || silent.stdout != "" ||
		!strings.Contains(silent.stderr, "Knowledge base plugin kb did not answer its diagnose tool within 400ms, so study stopped it") ||
		!strings.Contains(silent.stderr, "testing the embedding endpoint") {
		t.Errorf("a plugin that does not say what it found: exit %d, stdout %q, stderr %q", silent.code, silent.stdout, silent.stderr)
	}

	// A failure goes to stderr, with the advice, and nothing to stdout.
	broken := withTestPlugin("--exit", "3", "--stderr", "cannot open the index")(t)
	r := broken.check(t, 20*time.Second, "knowledge-base", "check", "kb")
	if r.code != cli.ExitError || r.stdout != "" || !strings.Contains(r.stderr, "Knowledge base plugin kb stopped before it could open an MCP session (exit status 3)") ||
		!strings.Contains(r.stderr, "cannot open the index") || !strings.Contains(r.stderr, "study knowledge-base list shows the command") {
		t.Errorf("a plugin that does not start: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

// What a plugin writes never reaches the learner's terminal as it is: its
// standard error is the plugin's own, and what study repeats of it, and of
// its name for itself, has nothing in it a terminal would act on.
func TestKnowledgeBaseCheckKeepsAPluginOffTheTerminal(t *testing.T) {
	// As the plugin is told them: the registry stores no argument with such
	// a character in it, so the plugin reads these escapes itself, and
	// writes the characters (--escaped).
	const escapes = `\x1b]0;gotcha\a\x1b[2J`
	for name, tc := range map[string]struct {
		args []string
		// said is a text of the plugin's that study repeats, cleaned.
		said string
	}{
		"a plugin that speaks":          {[]string{"--name", "kb" + escapes, "--version", "1" + escapes, "--stderr", "ready" + escapes + `\n`}, "gotcha"},
		"a plugin that does not":        {[]string{"--exit", "1", "--stderr", "failed" + escapes + `\n`}, "failed"},
		"a plugin that breaks it":       {[]string{"--contract", `{"version": 1, "sources": ["file\u001b[2J"], "search": ["keyword"], "locations": []}`}, ""},
		"a plugin that refuses":         {[]string{"--refuse", "contract=internal:no way" + escapes}, "no way"},
		"a plugin that writes no MCP":   {[]string{"--garbage", "--stderr", "noise" + escapes}, "noise"},
		"a plugin that found something": {[]string{"--finding", "embedding=wrong:it is down" + escapes + `\n, said the server`}, "it is down"},
	} {
		for _, mode := range [][]string{nil, {"--json"}} {
			record := filepath.Join(t.TempDir(), "record.json")
			k := withTestPlugin(append([]string{"--escaped", "--record", record}, tc.args...)...)(t)
			r := k.check(t, 20*time.Second, append([]string{"knowledge-base", "check", "kb"}, mode...)...)
			// The plugin was started, and study repeats what it wrote:
			// without both, nothing of a plugin's was there to keep off the
			// terminal.
			if _, err := os.Stat(record); err != nil {
				t.Fatalf("%s, %v: the plugin was never started (%v): exit %d, %s%s", name, mode, err, r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout+r.stderr, tc.said) {
				t.Errorf("%s, %v: study does not repeat the plugin's %q: %s%s", name, mode, tc.said, r.stdout, r.stderr)
			}
			for _, out := range []string{r.stdout, r.stderr} {
				if strings.ContainsAny(out, "\x1b\x07") {
					t.Errorf("%s, %v: study wrote the plugin's escapes: %q", name, mode, out)
				}
			}
		}
	}
}

// study knowledge-base check stops what it started, whatever came of it.
func TestKnowledgeBaseCheckLeavesNothingRunning(t *testing.T) {
	for name, args := range map[string][]string{
		"a plugin that speaks":             nil,
		"a plugin that hangs":              {"--hang", "start"},
		"a plugin that will not stop":      {"--stubborn"},
		"a plugin that speaks another one": {"--contract", `{"version": 9}`},
	} {
		t.Run(name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "record.json")
			k := withTestPlugin(append([]string{"--record", record}, args...)...)(t)
			k.check(t, 500*time.Millisecond, "knowledge-base", "check", "kb")
			got := plugintest.ReadRecord(t, record)
			if !plugintest.Gone(got.PID, 5*time.Second) {
				plugintest.Kill(got.PID)
				t.Errorf("the plugin, process %d, was left running", got.PID)
			}
			// It was started in the registry's folder, with a folder of its
			// own in the learner's state folder, and nothing was made in
			// the Study home.
			if want := filepath.Join(k.home, ".config", "lamplight"); !sameFolder(got.Dir, want) {
				t.Errorf("the plugin was started in %s, want %s", got.Dir, want)
			}
			if want := filepath.Join(k.home, ".local", "state", "lamplight", "knowledge-base-plugins", "kb"); !sameFolder(got.Env["LAMPLIGHT_PLUGIN_DATA"], want) {
				t.Errorf("the plugin's folder is %q, want %q", got.Env["LAMPLIGHT_PLUGIN_DATA"], want)
			}
			if _, err := os.Stat(filepath.Join(k.env["STUDY_HOME"], ".lamplight")); err == nil {
				t.Error("study made a folder in the Study home for the plugin")
			}
		})
	}
}

func sameFolder(a, b string) bool {
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}

// The shell completes the registered names for study knowledge-base check.
func TestKnowledgeBaseCheckCompletesRegisteredNames(t *testing.T) {
	k := withPlugins(t)
	for args, want := range map[string]string{
		"knowledge-base check ":       "remote\nshelf\n:4\n",
		"knowledge-base check shelf ": ":4\n",
	} {
		r := k.run(t, append([]string{"__complete"}, strings.Split(args, " ")...)...)
		if r.code != cli.ExitOK || r.stdout != want {
			t.Errorf("completing study %s: exit %d, stdout %q, want %q", args, r.code, r.stdout, want)
		}
	}
}
