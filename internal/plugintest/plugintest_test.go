package plugintest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
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

// The conformance suite passes against the test plugin over a command,
// started as study starts one...
func TestTheTestPluginConformsAsACommand(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{
		Connect:  plugintest.AsCommand(plugintest.NewComputer(t), plugintest.Command("--page-delay", slow)...),
		Restarts: true,
	})
}

// ...and over a URL, as a service every connection reaches.
func TestTheTestPluginConformsAsAService(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t, plugintest.Config{PageDelay: 2 * time.Millisecond}))})
}

// A plugin that indexes one kind of Source conforms as well: what it does
// not list, it refuses as unsupported, and the rest is not asked of it.
func TestAPluginOfOneKindOfSourceConforms(t *testing.T) {
	for _, kind := range []string{"file", "url"} {
		t.Run(kind, func(t *testing.T) {
			plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t,
				plugintest.Config{Sources: []string{kind}, PageDelay: 2 * time.Millisecond}))})
		})
	}
}

// And one that gives its results as text alone.
func TestAPluginThatAnswersInTextConforms(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t,
		plugintest.Config{TextOnly: true, PageDelay: 2 * time.Millisecond}))})
}

// longNotice is a notice of n bytes, as a plugin words one.
func longNotice(n int) string {
	notice := "These Passages were found by keyword alone: the embedding endpoint did not answer."
	return notice + strings.Repeat(" It did not.", n)[:n-len(notice)]
}

// And one whose searches each carry a notice, of as many bytes as a notice
// may have: a search that says what it did less than it was set to is no
// broken search. The plugin is started to say it, as a command.
func TestAPluginThatGivesANoticeConforms(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{
		Connect: plugintest.AsCommand(plugintest.NewComputer(t),
			plugintest.Command("--page-delay", slow, "--notice", longNotice(plugincontract.MaxNoticeBytes))...),
		Restarts: true,
	})
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

// found is what a diagnose tool gives back with these findings, as JSON.
func found(findings ...plugincontract.Finding) string {
	data, _ := json.Marshal(plugincontract.DiagnoseResult{Findings: findings})
	return string(data)
}

// manyFindings are n findings, each under a name of its own.
func manyFindings(n int) []plugincontract.Finding {
	var out []plugincontract.Finding
	for i := range n {
		out = append(out, plugincontract.Finding{Name: fmt.Sprintf("part%d", i), OK: true, Message: "it answers"})
	}
	return out
}

// A plugin that depends on something says so, and conforms like one that
// depends on nothing: the suite takes no view on whether a finding is all
// right.
func TestAPluginWithFindingsConforms(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t, plugintest.Config{PageDelay: 2 * time.Millisecond,
		Findings: []plugincontract.Finding{
			{Name: "embedding", OK: true, Message: "the embedding endpoint 127.0.0.1:8080 gave a vector of 512 numbers"},
			{Name: "reranking", Message: strings.Repeat("x", plugincontract.MaxFindingBytes)},
		}}))})
}

// The same things under the same names, in another order the next time: the
// contract says nothing of the order but that study shows the findings in
// the one they come in, so a plugin that tests side by side and gives each
// finding as it arrives conforms.
func TestAPluginThatGivesItsFindingsInAnotherOrderConforms(t *testing.T) {
	one := plugincontract.Finding{Name: "embedding", OK: true, Message: "it answers"}
	two := plugincontract.Finding{Name: "reranking", Message: "it is down"}
	var asked atomic.Int64
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t, plugintest.Config{PageDelay: 2 * time.Millisecond,
		Findings: []plugincontract.Finding{one, two},
		Twist: func(tool string, answer []byte, refused bool) []byte {
			if tool == "diagnose" && asked.Add(1)%2 == 0 {
				return []byte(found(two, one))
			}
			return answer
		}}))})
	if asked.Load() < 2 {
		t.Fatalf("the suite asked diagnose %d times, so this test proves nothing of a second answer", asked.Load())
	}
}

// broken are plugins that each break the contract in one way, and what the
// suite says of each. One with args is a command, which the rule it breaks
// is about; the others are services.
var broken = map[string]struct {
	cfg  plugintest.Config
	args []string
	want string
	// patience is how long the suite waits for an answer, when not 20
	// seconds, and once what its output holds once and no more.
	patience time.Duration
	once     string
}{
	"it summarises": {cfg: plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
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
	}}, want: "which is not in its Source: a Passage is the Source's own text"},
	"it gives back the hash it was given": {cfg: plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
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
	}}, want: "status gives the hash sha256:0000"},
	"it spells a key its own way": {cfg: plugintest.Config{Twist: replaced("search", `"passages"`, `"Passages"`, false)},
		want: `"passages" is spelled "Passages"`},
	"it leaves a key out":      {cfg: plugintest.Config{Twist: replaced("cancel", `"stopped"`, `"halted"`, false)}, want: `"stopped" is missing`},
	"it gives a score as text": {cfg: plugintest.Config{Twist: replaced("search", `"score":1`, `"score":"1"`, false)}, want: "score: it is not a number"},
	"it says it stopped what goes on": {cfg: plugintest.Config{PageDelay: 5 * time.Millisecond, Results: map[string]string{"cancel": `{"stopped": true}`}},
		want: "status says the Source is being indexed after cancel answered"},
	"it has a code of its own for what is not there": {cfg: plugintest.Config{Twist: replaced("passage", `"not_found"`, `"missing"`, true)},
		want: `was refused with the code "missing", want "not_found"`},
	"it refuses in plain text where a code is due": {cfg: plugintest.Config{Twist: func(tool string, answer []byte, refused bool) []byte {
		if tool == "passage" && refused {
			return []byte(`"no such passage"`)
		}
		return answer
	}}, want: `was refused without the contract's error, want one with the code "not_found"`},
	"it takes anything": {cfg: plugintest.Config{Results: map[string]string{"status": `{"sources": []}`}}, want: "was not refused"},
	"it speaks a version of its own": {cfg: plugintest.Config{Results: map[string]string{"contract": `{"version": 0, "sources": ["file"], "search": ["keyword"], "locations": []}`}},
		want: "what contract gave back is not the contract's: version is not a whole number of 1 or more"},
	"it lacks a tool": {cfg: plugintest.Config{Tools: []string{"contract", "index", "status", "search", "passage"}}, want: "it has no cancel tool"},

	// What a plugin got past the suite with before the suite looked.
	"it gives more Passages than the limit": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakOverLimit}},
		want: "gave 12 Passages: limit is the most a search gives back"},
	"its status waits for indexing": {cfg: plugintest.Config{PageDelay: 4 * time.Millisecond, Breaks: []string{plugintest.BreakStatusWaits}},
		want: "status answers only once indexing is over"},
	"its search waits for indexing": {cfg: plugintest.Config{PageDelay: 4 * time.Millisecond, Breaks: []string{plugintest.BreakSearchWaits}},
		want: "search answers only once indexing is over"},
	"its cancel forgets what was indexed before": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakCancelDropsEarlier}},
		want: "what was indexed stays, so want it indexed with the hash"},
	"it gives no hash for what a search still finds": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakNoEarlierHash}},
		want: "gives no hash for it"},
	"it reads the Source to search it": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakSearchReadsSource}},
		want: "a search reads the index and never the Source"},
	"it forgets that a Source failed": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakFailedForgotten}},
		want: "a Source stays failed until it is indexed again"},
	"it indexes a file that is not the hash it was handed": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakIndexesWhatItReads}},
		want: "a plugin indexes none of it"},
	"it is slow to say what it has indexed": {args: []string{"--delay", "contract=2200ms"},
		want: "study status gives a plugin 2s for all of it"},
	"it loses a failure when it stops": {args: []string{"--break", plugintest.BreakFailedLost},
		want: "however often the plugin is started"},
	"it gives a score with a Passage": {cfg: plugintest.Config{Twist: replaced("passage", `"text":`, `"score":0.5,"text":`, false)},
		want: "with a score: a score belongs to one search"},
	"it refuses a field it does not know": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakStrict}},
		want: "a plugin passes over what it does not know"},
	"it takes an argument under any spelling": {cfg: plugintest.Config{Breaks: []string{plugintest.BreakAnyCase}},
		want: "a collection under another spelling (status of {\"Collection\":"},
	"it stays when its input closes": {args: []string{"--linger"},
		want: "the plugin did not exit within 2 seconds of its standard input closing"},
	"it writes where it is started": {args: []string{"--litter"}, want: "a plugin writes in its own folder"},
	"it follows a link out of its folder": {args: []string{"--break", plugintest.BreakFollowsLinks},
		want: "the plugin wrote outside its folder, through a symbolic link"},
	"it minds a file study leaves in its folder": {args: []string{"--break", plugintest.BreakMindsStrays},
		want: "A plugin passes over a name there that begins with .lamplight-"},
	"it sends study requests": {args: []string{"--ask", "status"},
		want: "the plugin sent a request (ping), which ended the session"},
	// What a plugin says of what it depends on, and when.
	"it lacks the tool that tests what it depends on": {cfg: plugintest.Config{Tools: []string{"contract", "index", "status", "cancel", "search", "passage"}},
		want: "it has no diagnose tool"},
	"it finds more than an answer may hold": {cfg: plugintest.Config{Results: map[string]string{"diagnose": found(manyFindings(plugincontract.MaxFindings + 1)...)}},
		want: "what diagnose gave back is not the contract's: it holds 17 findings, more than the 16 an answer may hold"},
	"it names a finding in words of its own": {cfg: plugintest.Config{Findings: []plugincontract.Finding{{Name: "The embedding endpoint", OK: true, Message: "it answers"}}},
		want: `findings[0] is named "The embedding endpoint", which is no name`},
	"it gives one name to two findings": {cfg: plugintest.Config{Findings: []plugincontract.Finding{{Name: "endpoint", OK: true, Message: "it answers"},
		{Name: "endpoint", Message: "it is down"}}}, want: "it holds the finding endpoint twice"},
	"its finding says nothing": {cfg: plugintest.Config{Findings: []plugincontract.Finding{{Name: "embedding", Message: " "}}},
		want: "the finding embedding has no message"},
	"its finding does not say whether it is all right": {cfg: plugintest.Config{Twist: replaced("diagnose", `"ok":true,`, ``, false),
		Findings: []plugincontract.Finding{{Name: "embedding", OK: true, Message: "it answers"}}}, want: `findings[0]: "ok" is missing`},
	"its finding says more than study shows": {cfg: plugintest.Config{Findings: []plugincontract.Finding{{Name: "embedding",
		Message: strings.Repeat("x", plugincontract.MaxFindingBytes+1)}}}, want: "has a message of 601 bytes: study shows the first 600 of it"},
	"it refuses to say what it found": {cfg: plugintest.Config{Refusals: map[string]string{"diagnose": "internal:no endpoint answers"}},
		want: "diagnose refused {}: "},
	"it gives no findings to arguments it does not know": {cfg: plugintest.Config{Twist: func() func(string, []byte, bool) []byte {
		var asked atomic.Int64
		return func(tool string, answer []byte, refused bool) []byte {
			if tool == "diagnose" && asked.Add(1) > 1 {
				return []byte(`"unknown field added_later"`)
			}
			return answer
		}
	}()}, want: "what diagnose gave back the second time: it is not a JSON object"},
	"it tests something else each time": {cfg: plugintest.Config{Twist: func() func(string, []byte, bool) []byte {
		var asked atomic.Int64
		return func(tool string, answer []byte, refused bool) []byte {
			if tool != "diagnose" {
				return answer
			}
			return []byte(found(plugincontract.Finding{Name: fmt.Sprintf("endpoint%d", asked.Add(1)), OK: true, Message: "it answers"}))
		}
	}()}, want: "the same things are tested each time, under the same names"},
	"it is slow to say what it found": {args: []string{"--delay", "diagnose=10300ms"},
		want: "study gives a plugin 10s for the whole answer"},
	// And every time it is asked, not the first alone.
	"its finding says more than study shows the second time": {cfg: plugintest.Config{
		Findings: []plugincontract.Finding{{Name: "embedding", Message: "it is down"}},
		Twist: func() func(string, []byte, bool) []byte {
			var asked atomic.Int64
			return func(tool string, answer []byte, refused bool) []byte {
				if tool == "diagnose" && asked.Add(1) > 1 {
					return []byte(found(plugincontract.Finding{Name: "embedding", Message: strings.Repeat("x", plugincontract.MaxFindingBytes+1)}))
				}
				return answer
			}
		}()}, want: "has a message of 601 bytes the second time: study shows the first 600 of it"},
	"its second answer breaks the contract": {cfg: plugintest.Config{
		Findings: []plugincontract.Finding{{Name: "embedding", Message: "it is down"}},
		Twist: func() func(string, []byte, bool) []byte {
			var asked atomic.Int64
			return func(tool string, answer []byte, refused bool) []byte {
				if tool == "diagnose" && asked.Add(1) > 1 {
					return []byte(found(plugincontract.Finding{Name: "embedding", Message: " "}))
				}
				return answer
			}
		}()}, want: "what diagnose gave back the second time is not the contract's: the finding embedding has no message"},
	"it is slow to say what it found the second time": {cfg: plugintest.Config{
		Findings: []plugincontract.Finding{{Name: "embedding", OK: true, Message: "it answers"}},
		Before: func() func(context.Context, string) {
			var asked atomic.Int64
			return func(ctx context.Context, tool string) {
				if tool == "diagnose" && asked.Add(1) > 1 {
					select {
					case <-ctx.Done():
					case <-time.After(plugincontract.DiagnoseTime + 300*time.Millisecond):
					}
				}
			}
		}()}, want: "the second time: study gives a plugin 10s for the whole answer"},
	"its notice is a byte longer than a notice may be": {args: []string{"--notice", longNotice(plugincontract.MaxNoticeBytes + 1)},
		want: "search gave a notice of " + strconv.Itoa(plugincontract.MaxNoticeBytes+1) + " bytes"},
	// And one that stops answering is waited for once.
	"it stops answering": {args: []string{"--hang", "search"}, patience: 2 * time.Second,
		want: "nothing after it was checked", once: ": search got no answer within 2s"},
}

// TestABrokenPlugin runs the suite against one of the broken plugins. It is
// run by TestTheSuiteCatchesABrokenPlugin, in a process of its own, and is
// expected to fail there.
func TestABrokenPlugin(t *testing.T) {
	name := os.Getenv("LAMPLIGHT_BROKEN_PLUGIN")
	if name == "" {
		t.Skip("run by TestTheSuiteCatchesABrokenPlugin")
	}
	tc := broken[name]
	if tc.patience == 0 {
		tc.patience = 20 * time.Second
	}
	if tc.args != nil {
		plugintest.Conformance(t, plugintest.Target{Restarts: true, Patience: tc.patience,
			Connect: plugintest.AsCommand(plugintest.NewComputer(t), plugintest.Command(append([]string{"--page-delay", slow}, tc.args...)...)...)})
		return
	}
	if tc.cfg.PageDelay == 0 {
		tc.cfg.PageDelay = 2 * time.Millisecond
	}
	plugintest.Conformance(t, plugintest.Target{Connect: plugintest.AsService(serve(t, tc.cfg)), Patience: tc.patience})
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
			began := time.Now()
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the suite passed a plugin of which it is true that %s:\n%s", name, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("the suite failed the plugin, but not for %q:\n%s", tc.want, out)
			}
			if tc.once != "" {
				if n := strings.Count(string(out), tc.once); n != 1 {
					t.Errorf("the suite's output holds %q %d times, want once: it waits for a plugin that stopped answering "+
						"once, and not in every check after that:\n%s", tc.once, n, out)
				}
				if took := time.Since(began); took > 10*tc.patience {
					t.Errorf("the suite took %s over a plugin that stopped answering, with %s for each call", took, tc.patience)
				}
			}
		})
	}
}
