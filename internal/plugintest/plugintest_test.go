package plugintest_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// TestMain runs this test binary as the test plugin when it is started as
// one: the plugin is the only program these tests start.
func TestMain(m *testing.M) {
	plugintest.Main()
	os.Exit(m.Run())
}

// slow is how long the test plugin takes over a page: long enough for the
// suite to stop the indexing of a long book halfway.
const slow = "2ms"

// The conformance suite passes against the test plugin over a command...
func TestTheTestPluginConformsAsACommand(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{
		Connect:  plugintest.CommandTransport(t.TempDir(), plugintest.Command("--page-delay", slow)...),
		Restarts: true,
	})
}

// ...and over a URL, as a service every connection reaches.
func TestTheTestPluginConformsAsAService(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.URLTransport(serve(t, plugintest.Config{PageDelay: 2 * time.Millisecond}))})
}

// A plugin that indexes one kind of Source conforms as well: what it does
// not list, it refuses as unsupported, and the rest is not asked of it.
func TestAPluginOfOneKindOfSourceConforms(t *testing.T) {
	for _, kind := range []string{"file", "url"} {
		t.Run(kind, func(t *testing.T) {
			plugintest.Conformance(t, plugintest.Target{Connect: plugintest.URLTransport(serve(t,
				plugintest.Config{Sources: []string{kind}, PageDelay: 2 * time.Millisecond}))})
		})
	}
}

// And one that gives its results as text alone.
func TestAPluginThatAnswersInTextConforms(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.URLTransport(serve(t,
		plugintest.Config{TextOnly: true, PageDelay: 2 * time.Millisecond}))})
}

// serve runs a test plugin as a service on this computer's loopback
// interface and returns its address.
func serve(t *testing.T, cfg plugintest.Config) string {
	t.Helper()
	cfg.Folder = t.TempDir()
	plugin := plugintest.New(cfg)
	server := httptest.NewServer(plugin.Handler())
	t.Cleanup(func() {
		server.Close()
		plugin.Close()
	})
	return server.URL + "/mcp"
}

// replaced returns a Twist that replaces one text by another in the answers
// of a tool that are, or are not, refusals.
func replaced(tool, old, new string, inRefusals bool) func(string, []byte, bool) []byte {
	return func(called string, answer []byte, refused bool) []byte {
		if called != tool || refused != inRefusals {
			return answer
		}
		return bytes.ReplaceAll(answer, []byte(old), []byte(new))
	}
}

// broken are plugins that each break the contract in one way, and what the
// suite says of each.
var broken = map[string]struct {
	cfg  plugintest.Config
	want string
}{
	"it summarises": {plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
		var found struct {
			Passages []map[string]any `json:"passages"`
		}
		if tool != "search" || refused || json.Unmarshal(answer, &found) != nil {
			return answer
		}
		for _, passage := range found.Passages {
			passage["text"] = passage["text"].(string) + " In short, the page is about markers."
		}
		out, _ := json.Marshal(found)
		return out
	}}, "which is not in its Source: a Passage is the Source's own text"},
	"it gives back the hash it was given": {plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
		var status struct {
			Sources []map[string]any `json:"sources"`
		}
		if tool != "status" || refused || json.Unmarshal(answer, &status) != nil {
			return answer
		}
		for _, source := range status.Sources {
			if source["state"] == "indexed" {
				source["hash"] = "sha256:" + strings.Repeat("0", 64)
			}
		}
		out, _ := json.Marshal(status)
		return out
	}}, "status gives the hash sha256:0000"},
	"it spells a key its own way": {plugintest.Config{Twist: replaced("search", `"passages"`, `"Passages"`, false)},
		`"passages" is spelled "Passages"`},
	"it leaves a key out":      {plugintest.Config{Twist: replaced("cancel", `"stopped"`, `"halted"`, false)}, `"stopped" is missing`},
	"it gives a score as text": {plugintest.Config{Twist: replaced("search", `"score":1`, `"score":"1"`, false)}, "score: it is not a number"},
	"it says it stopped what goes on": {plugintest.Config{PageDelay: 5 * time.Millisecond, Results: map[string]string{"cancel": `{"stopped": true}`}},
		"status says the Source is being indexed after cancel answered"},
	"it has a code of its own for what is not there": {plugintest.Config{Twist: replaced("passage", `"not_found"`, `"missing"`, true)},
		`was refused with the code "missing", want "not_found"`},
	"it refuses in plain text where a code is due": {plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
		if tool == "passage" && refused {
			return []byte(`"no such passage"`)
		}
		return answer
	}}, `was refused without the contract's error, want one with the code "not_found"`},
	"it takes anything": {plugintest.Config{Results: map[string]string{"status": `{"sources": []}`}}, "was not refused"},
	"it speaks a version of its own": {plugintest.Config{Results: map[string]string{"contract": `{"version": 0, "sources": ["file"], "search": ["keyword"], "locations": []}`}},
		"what contract gave back is not the contract's: version is not a whole number of 1 or more"},
	"it lacks a tool": {plugintest.Config{Tools: []string{"contract", "index", "status", "search", "passage"}}, "it has no cancel tool"},
}

// TestABrokenPlugin runs the suite against one of the broken plugins. It is
// run by TestTheSuiteCatchesABrokenPlugin, in a process of its own, and is
// expected to fail there.
func TestABrokenPlugin(t *testing.T) {
	name := os.Getenv("LAMPLIGHT_BROKEN_PLUGIN")
	if name == "" {
		t.Skip("run by TestTheSuiteCatchesABrokenPlugin")
	}
	cfg := broken[name].cfg
	if cfg.PageDelay == 0 {
		cfg.PageDelay = 2 * time.Millisecond
	}
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.URLTransport(serve(t, cfg)), Patience: 20 * time.Second})
}

// A suite that passes whatever it is shown checks nothing. Each plugin here
// breaks the contract in one way, and the suite must fail it for that.
func TestTheSuiteCatchesABrokenPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the suite once for each broken plugin")
	}
	for name, tc := range broken {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command(os.Args[0], "-test.run=^TestABrokenPlugin$", "-test.count=1", "-test.v")
			cmd.Env = append(os.Environ(), "LAMPLIGHT_BROKEN_PLUGIN="+name)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the suite passed a plugin of which it is true that %s:\n%s", name, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("the suite failed the plugin, but not for %q:\n%s", tc.want, out)
			}
		})
	}
}
