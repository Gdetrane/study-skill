package pluginclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// findings are what a plugin's diagnose tool gives back, as JSON.
func findingsOf(t *testing.T, findings ...plugincontract.Finding) string {
	t.Helper()
	if findings == nil {
		findings = []plugincontract.Finding{}
	}
	data, err := json.Marshal(plugincontract.DiagnoseResult{Findings: findings})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A plugin says what it found of what it depends on: each finding by its
// name, whether it is all right, and in a sentence of its own. One that
// depends on nothing gives none, and that is an answer too.
func TestAPluginSaysWhatItFoundOfWhatItDependsOn(t *testing.T) {
	ctx := context.Background()
	record := filepath.Join(t.TempDir(), "record.json")
	s := start(t, plugintest.NewComputer(t), "--record", record,
		"--finding", "embedding=ok:the embedding endpoint 127.0.0.1:8080 gave a vector of 512 numbers",
		"--finding", "reranking=wrong:the reranking endpoint 127.0.0.1:8081 could not be reached: connection refused")
	got, err := s.Diagnose(ctx)
	want := []plugincontract.Finding{
		{Name: "embedding", OK: true, Message: "the embedding endpoint 127.0.0.1:8080 gave a vector of 512 numbers"},
		{Name: "reranking", Message: "the reranking endpoint 127.0.0.1:8081 could not be reached: connection refused"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Diagnose = %+v, %v; want %+v", got, err, want)
	}
	// It was asked that tool, with no arguments, after its contract.
	calls := plugintest.ReadCalls(t, record)
	if tools := plugintest.Tools(calls); !reflect.DeepEqual(tools, []string{"contract", "diagnose"}) || string(calls[1].Args) != "{}" {
		t.Errorf("the plugin was asked %v, the last with %s; want its contract, and then diagnose with no arguments", tools, calls[len(calls)-1].Args)
	}
	// Asked again it answers again, and the Connection is as good as it was.
	if again, err := s.Diagnose(ctx); err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("Diagnose again = %+v, %v", again, err)
	}
	if s.Ended() {
		t.Error("the Connection ended with a finding that is not all right: the plugin answered")
	}

	nothing := start(t, plugintest.NewComputer(t))
	if got, err := nothing.Diagnose(ctx); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Diagnose of a plugin that depends on nothing = %#v, %v; want no finding, and no error", got, err)
	}
	// As text alone, for a plugin whose MCP library gives no structured
	// content.
	text := service(t, plugintest.Config{TextOnly: true, Findings: want[:1]})
	c := plugintest.NewComputer(t)
	remote, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", text), quick(c))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if got, err := remote.Diagnose(ctx); err != nil || !reflect.DeepEqual(got, want[:1]) {
		t.Errorf("Diagnose of a service that answers in text = %+v, %v", got, err)
	}
}

// A finding's message is the plugin's text: it reaches whoever asked on one
// line, with nothing a terminal would act on, and no longer than a sentence
// may be. A long one is cut short, never refused.
func TestAFindingsMessageIsCleaned(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("the endpoint is down. ", 4000)
	for name, tc := range map[string]struct {
		message string
		want    func(got string) bool
	}{
		"what a terminal obeys": {"it is down\x1b[2J\x1b]0;owned\x07, said\nthe server\u202e", func(got string) bool {
			return got == `it is down\u001b[2J\u001b]0;owned\u0007, said the server\u202e`
		}},
		"a long one": {long, func(got string) bool {
			return len(got) <= plugincontract.MaxFindingBytes+len("…") && strings.HasPrefix(got, "the endpoint is down. the endpoint") &&
				strings.HasSuffix(got, "…")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			address := service(t, plugintest.Config{Results: map[string]string{
				"diagnose": findingsOf(t, plugincontract.Finding{Name: "embedding", Message: tc.message})}})
			c := plugintest.NewComputer(t)
			s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got, err := s.Diagnose(ctx)
			if err != nil || len(got) != 1 || got[0].Name != "embedding" || got[0].OK || !tc.want(got[0].Message) {
				t.Fatalf("Diagnose = %q, %v", fmt.Sprint(got), err)
			}
			safe(t, got[0].Message)
		})
	}
}

// A service can write the address it is registered at, with whatever key
// that holds, into a finding as easily as anywhere else: what is shown of it
// is its scheme and host.
func TestAFindingDoesNotRepeatARegisteredAddress(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	var registered atomic.Pointer[string]
	address := service(t, plugintest.Config{
		Findings: []plugincontract.Finding{{Name: "upstream", Message: "ADDRESS answered 500 to the key s3cr3t-k3y-of-the-learner"}},
		Twist: func(tool string, answer []byte, refused bool) []byte {
			if address := registered.Load(); address != nil {
				return []byte(strings.ReplaceAll(string(answer), "ADDRESS", *address))
			}
			return answer
		}})
	address = strings.TrimSuffix(address, "/mcp") + "/t0ken-in-the-path/mcp?key=s3cr3t-k3y-of-the-learner"
	registered.Store(&address)
	s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	got, err := s.Diagnose(ctx)
	if err != nil || len(got) != 1 || !strings.HasPrefix(got[0].Message, pluginclient.Address(address)+"… answered 500") {
		t.Fatalf("Diagnose = %+v, %v", got, err)
	}
	if strings.Contains(got[0].Message, "s3cr3t") || strings.Contains(got[0].Message, "t0ken") {
		t.Errorf("a finding is shown with the key of the address the plugin is registered at: %s", got[0].Message)
	}
}

// What diagnose gives back is held to the contract like any answer: findings
// that are not what it describes are no findings, and the plugin does not
// speak the contract.
func TestFindingsThatBreakTheContractAreRefused(t *testing.T) {
	ctx := context.Background()
	var many []plugincontract.Finding
	for n := range plugincontract.MaxFindings + 1 {
		many = append(many, plugincontract.Finding{Name: fmt.Sprintf("part%d", n), OK: true, Message: "it answers"})
	}
	for name, tc := range map[string]struct{ result, want string }{
		"too many":              {findingsOf(t, many...), "it holds 17 findings, more than the 16 an answer may hold"},
		"a name that is none":   {`{"findings": [{"name": "\u001b[2JEmbedding", "ok": true, "message": "it answers"}]}`, `findings[0] is named "\x1b[2JEmbedding", which is no name`},
		"a name that is long":   {findingsOf(t, plugincontract.Finding{Name: strings.Repeat("n", 33), Message: "x"}), "up to 32 characters"},
		"one name twice":        {`{"findings": [{"name": "a", "ok": true, "message": "x"}, {"name": "a", "ok": false, "message": "y"}]}`, "it holds the finding a twice"},
		"no message":            {`{"findings": [{"name": "embedding", "ok": false, "message": " "}]}`, "the finding embedding has no message"},
		"no verdict":            {`{"findings": [{"name": "embedding", "message": "it is down"}]}`, `findings[0]: "ok" is missing`},
		"a verdict in words":    {`{"findings": [{"name": "embedding", "ok": "yes", "message": "it answers"}]}`, "findings.ok is a string, not a bool"},
		"no list":               {`{}`, `"findings" is missing`},
		"a list that is null":   {`{"findings": null}`, "findings: it is not a list"},
		"a key of its own":      {`{"Findings": []}`, `"findings" is spelled "Findings"`},
		"findings by name":      {`{"findings": {"embedding": true}}`, "it is not the JSON the contract describes"},
		"what is no object":     {`["embedding is fine"]`, "what its diagnose tool gave back is not what the contract describes: it is not a JSON object"},
		"a fix of the plugin's": {`{"findings": [{"name": "embedding", "ok": false, "message": "it is down", "fix": "rm -rf ~"}]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := start(t, plugintest.NewComputer(t), "--result", "diagnose="+tc.result)
			got, err := s.Diagnose(ctx)
			if tc.want == "" {
				// What the contract does not name is passed over: a plugin
				// may say more, and study shows none of it.
				if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], plugincontract.Finding{Name: "embedding", Message: "it is down"}) {
					t.Fatalf("Diagnose = %+v, %v; want the finding, without what the contract does not name", got, err)
				}
				return
			}
			if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
				!strings.Contains(err.Error(), "Knowledge base plugin kb does not speak Lamplight's contract") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Diagnose = %+v, %v; want it refused for %q", got, err, tc.want)
			}
			safe(t, err.Error())
		})
	}
}

// A plugin that does not answer diagnose in the time it has is stopped, and
// the error says how long it had: the time is the tool's own, and neither
// the time of another call nor of a search.
func TestAPluginThatDoesNotSayWhatItFoundIsStopped(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "diagnose")...)
	opts := quick(c)
	opts.DiagnoseTimeout, opts.StopGrace = 300*time.Millisecond, 300*time.Millisecond
	s, err := pluginclient.Start(ctx, c.Env(""), plugin, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	began := time.Now()
	got, err := s.Diagnose(ctx)
	if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin kb did not answer its diagnose tool within 300ms, so study stopped it") {
		t.Errorf("Diagnose = %+v, %v", got, err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Diagnose took %s to give up on a plugin that has 300ms", took)
	}
	gone(t, record)
	if !s.Ended() {
		t.Error("the Connection of a plugin that was stopped has not ended")
	}

	// One that takes its time and answers within it is waited for: the
	// other calls' shorter time is not this one's.
	patient := quick(c)
	patient.CallTimeout, patient.SearchTimeout, patient.DiagnoseTimeout = 100*time.Millisecond, 100*time.Millisecond, 10*time.Second
	slow, err := pluginclient.Start(ctx, c.Env(""), c.Register(t, "slow", plugintest.Command("--delay", "diagnose=600ms",
		"--finding", "embedding=ok:it answers")...), patient)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if got, err := slow.Diagnose(ctx); err != nil || len(got) != 1 || !got[0].OK {
		t.Errorf("Diagnose of a plugin that takes 600ms of its 10s = %+v, %v", got, err)
	}

	// A plugin that refuses the tool has failed in it: that is the learner's
	// setup to put right, and no finding.
	refuses := start(t, plugintest.NewComputer(t), "--refuse", "diagnose=internal:no settings file")
	if got, err := refuses.Diagnose(ctx); got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin kb failed in its diagnose tool: no settings file") {
		t.Errorf("Diagnose of a plugin that refuses = %+v, %v", got, err)
	}

	// And a command that was stopped asks nothing.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := start(t, plugintest.NewComputer(t)).Diagnose(stopped); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Diagnose with a context that is cancelled = %v", err)
	}
}
