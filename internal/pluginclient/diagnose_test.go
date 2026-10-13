package pluginclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
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
// that are not what it describes are no findings. The error says that of the
// tool, and what of the answer breaks the contract, and never that the
// plugin does not speak the contract: it started and said which contract it
// speaks, and study goes on using it.
func TestFindingsThatBreakTheContractAreRefused(t *testing.T) {
	ctx := context.Background()
	var many []plugincontract.Finding
	for n := range plugincontract.MaxFindings + 1 {
		many = append(many, plugincontract.Finding{Name: fmt.Sprintf("part%d", n), OK: true, Message: "it answers"})
	}
	for name, tc := range map[string]struct{ result, want string }{
		"too many":            {findingsOf(t, many...), "it holds 17 findings, more than the 16 an answer may hold"},
		"a name that is none": {`{"findings": [{"name": "\u001b[2JEmbedding", "ok": true, "message": "it answers"}]}`, `findings[0] is named "\x1b[2JEmbedding", which is no name`},
		"a name that is long": {findingsOf(t, plugincontract.Finding{Name: strings.Repeat("n", 33), Message: "x"}), "up to 32 characters"},
		"one name twice":      {`{"findings": [{"name": "a", "ok": true, "message": "x"}, {"name": "a", "ok": false, "message": "y"}]}`, "it holds the finding a twice"},
		"no message":          {`{"findings": [{"name": "embedding", "ok": false, "message": " "}]}`, "the finding embedding has no message"},
		// Characters that show nothing, and a blank that is no white space:
		// cleaned, nothing of it is left for the learner to read.
		"a message of nothing":  {`{"findings": [{"name": "embedding", "ok": false, "message": "\u200b\udb40\udc41\ufe0f \u2800\u2060"}]}`, "the finding embedding has a message that shows nothing"},
		"no verdict":            {`{"findings": [{"name": "embedding", "message": "it is down"}]}`, `findings[0]: "ok" is missing`},
		"a verdict in words":    {`{"findings": [{"name": "embedding", "ok": "yes", "message": "it answers"}]}`, "findings.ok is a string, not a bool"},
		"no list":               {`{}`, `"findings" is missing`},
		"a list that is null":   {`{"findings": null}`, "findings: it is not a list"},
		"a key of its own":      {`{"Findings": []}`, `"findings" is spelled "Findings"`},
		"findings by name":      {`{"findings": {"embedding": true}}`, "it is not the JSON the contract describes"},
		"what is no object":     {`["embedding is fine"]`, "it is not a JSON object"},
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
				!strings.HasPrefix(err.Error(), "the diagnose tool of Knowledge base plugin kb gave an answer that breaks the contract: ") ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Diagnose = %+v, %v; want it refused for %q", got, err, tc.want)
			}
			unhelped(t, err)
			safe(t, err.Error())
			if s.Ended() {
				t.Error("the Connection ended over an answer that breaks the contract: the plugin answers, and is asked the rest")
			}
		})
	}
	// A sentence that stands behind more of what shows nothing than study
	// reads of a message passes the contract's check, and nothing of it is
	// left to show: that is no message either.
	behind := `{"findings": [{"name": "embedding", "ok": false, "message": "` + strings.Repeat(`\u200b`, 8000) + `it is down"}]}`
	hidden := start(t, plugintest.NewComputer(t), "--result", "diagnose="+behind)
	if got, err := hidden.Diagnose(ctx); got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "gave an answer that breaks the contract: the finding embedding has a message that shows nothing") {
		t.Errorf("Diagnose of a finding whose sentence stands behind what shows nothing = %+v, %v", got, err)
	}
	// And an answer that is no JSON at all, from a service that answers in
	// text.
	c := plugintest.NewComputer(t)
	words, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", service(t, plugintest.Config{TextOnly: true,
		Results: map[string]string{"diagnose": "embedding is fine"}})), quick(c))
	if err != nil {
		t.Fatal(err)
	}
	defer words.Close()
	if _, err := words.Diagnose(ctx); codeOf(err) != pluginclient.CodeFailedPrecondition || err.Error() != "the diagnose tool of "+
		"Knowledge base plugin remote gave an answer that breaks the contract: it is no JSON object, neither as structured content "+
		"nor as text. docs/knowledge-base-contract.md describes the contract" {
		t.Errorf("Diagnose of a service that answers in words = %v", err)
	}
}

// A plugin whose connection breaks while it is asked diagnose started, and
// said which contract it speaks: the error says that this tool was not
// answered and what was read in its place, and not that the plugin "does
// not speak MCP", which would be said of a plugin study goes on using.
func TestAConnectionThatBreaksUnderDiagnoseIsSaidOfThatTool(t *testing.T) {
	ctx := context.Background()
	// A command that writes more than a message may hold.
	c := plugintest.NewComputer(t)
	opts := quick(c)
	opts.DiagnoseTimeout = time.Minute
	flood, err := pluginclient.Start(ctx, c.Env(""), c.Register(t, "kb", plugintest.Command("--flood", "diagnose")...), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer flood.Close()
	got, err := flood.Diagnose(ctx)
	if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.HasPrefix(err.Error(), "Knowledge base plugin kb did "+
		"not answer its diagnose tool: study read something on its standard output that is no MCP message (") {
		t.Errorf("Diagnose of a command that writes what is no MCP message = %+v, %v", got, err)
	}
	broken(t, err)

	// A service that hangs up on the call.
	var server *httptest.Server
	plugin := plugintest.New(plugintest.Config{Folder: t.TempDir(), Before: func(_ context.Context, tool string) {
		if tool == plugincontract.ToolDiagnose {
			server.CloseClientConnections()
		}
	}})
	server = httptest.NewServer(plugin.Handler())
	t.Cleanup(func() {
		server.Close()
		plugin.Close()
	})
	remote, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp"), quick(c))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	got, err = remote.Diagnose(ctx)
	if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.HasPrefix(err.Error(), "Knowledge base plugin remote "+
		"did not answer its diagnose tool: the service at "+server.URL+" did not answer as an MCP server does (") {
		t.Errorf("Diagnose of a service that hangs up = %+v, %v", got, err)
	}
	broken(t, err)
}

// broken fails the test when the error of a diagnose call whose connection
// broke says of the whole plugin what is true of that call, names a command,
// or says that the plugin was late.
func broken(t *testing.T, err error) {
	t.Helper()
	unhelped(t, err)
	for _, not := range []string{"does not speak MCP", "does not answer as an MCP server at", "knowledge-base add"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("the error of a diagnose call whose connection broke says %q: %v", not, err)
		}
	}
	var failed *pluginclient.Error
	if errors.As(err, &failed) && failed.Late {
		t.Errorf("a plugin whose connection broke is said to have been late: %v", err)
	}
}

// unhelped fails the test when the error of a diagnose tool says of the
// plugin what is true of one study cannot use, or names a command: the
// plugin started and said which contract it speaks, and the command that
// shows more of a plugin asks this tool again and says the same.
func unhelped(t *testing.T, err error) {
	t.Helper()
	for _, not := range []string{"does not speak Lamplight's contract", "study knowledge-base", "Run its command"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("the error of a diagnose tool says %q: %v", not, err)
		}
	}
}

// A finding's name is shown as it is, so it is not passed through what takes
// a registered address's parts out of a sentence: a name that holds one,
// which may be a key, is no name, and the answer breaks the contract. The
// error does not repeat it.
func TestAFindingIsNotNamedAfterAPartOfARegisteredAddress(t *testing.T) {
	ctx := context.Background()
	const key = "c0ffee5ecr3tk3y0123456789abcdef0"
	for name, tc := range map[string]struct {
		rest, finding string
		refused       bool
	}{
		"the key in the path":     {"/" + key + "/mcp", key, true},
		"the key in the query":    {"/mcp?key=" + key, key, true},
		"the key inside a name":   {"/mcp?key=s3cr3t", "the_s3cr3t_key", true},
		"a name of its own":       {"/" + key + "/mcp", "embedding", false},
		"a short part of a path":  {"/kb/mcp", "kb", false},
		"no key in the address":   {"/mcp", "mcp", false},
		"a word the address ends": {"/v1/mcp", "v1", false},
	} {
		t.Run(name, func(t *testing.T) {
			address := service(t, plugintest.Config{Findings: []plugincontract.Finding{
				{Name: "reranking", OK: true, Message: "it answers"}, {Name: tc.finding, Message: "it is down"}}})
			address = strings.TrimSuffix(address, "/mcp") + tc.rest
			c := plugintest.NewComputer(t)
			s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			got, err := s.Diagnose(ctx)
			if !tc.refused {
				if err != nil || len(got) != 2 || got[1].Name != tc.finding {
					t.Fatalf("Diagnose = %+v, %v; want the finding %s, which holds no part of %s that may be a key", got, err, tc.finding, address)
				}
				return
			}
			if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
				!strings.Contains(err.Error(), "the diagnose tool of Knowledge base plugin remote gave an answer that breaks the contract: "+
					"findings[1] has a name that holds a part of the address study reaches the plugin at") {
				t.Fatalf("Diagnose = %+v, %v; want the answer refused for the name of its second finding", got, err)
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("the error repeats the key: %v", err)
			}
			unhelped(t, err)
		})
	}
	// An answer that breaks the contract in another way is said to, with
	// what it held: a name given twice, or one that is no name. That is the
	// service's text as well, and is said without the key.
	for name, result := range map[string]string{
		"the key as a name, twice":      `{"findings": [{"name": "` + key + `", "ok": true, "message": "x"}, {"name": "` + key + `", "ok": true, "message": "y"}]}`,
		"the key in what is no name":    `{"findings": [{"name": "The.Key.` + key + `", "ok": true, "message": "x"}]}`,
		"the key in a field of its own": `{"findings": [{"name": "embedding", "ok": "` + key + `", "message": "x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			address := service(t, plugintest.Config{Results: map[string]string{"diagnose": result}})
			address += "?key=" + key
			c := plugintest.NewComputer(t)
			s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			got, err := s.Diagnose(ctx)
			if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition ||
				!strings.HasPrefix(err.Error(), "the diagnose tool of Knowledge base plugin remote gave an answer that breaks the contract: ") {
				t.Fatalf("Diagnose = %+v, %v; want the answer refused", got, err)
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("what is wrong with the answer is said with the key of the address the plugin is registered at: %v", err)
			}
			unhelped(t, err)
		})
	}
	// A command has no address: its findings are named as they like.
	named := start(t, plugintest.NewComputer(t), "--finding", key+"=ok:it answers")
	if got, err := named.Diagnose(ctx); err != nil || len(got) != 1 || got[0].Name != key {
		t.Errorf("Diagnose of a command = %+v, %v", got, err)
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
		err.Error() != "Knowledge base plugin kb did not answer its diagnose tool within 300ms, so study stopped it." {
		t.Errorf("Diagnose = %+v, %v", got, err)
	}
	// It says that the plugin was late, for whoever would start it again.
	var late *pluginclient.Error
	if !errors.As(err, &late) || !late.Late {
		t.Errorf("the error of a plugin that did not answer in time does not say it was late: %+v", late)
	}
	unhelped(t, err)
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Diagnose took %s to give up on a plugin that has 300ms", took)
	}
	gone(t, record)
	if !s.Ended() {
		t.Error("the Connection of a plugin that was stopped has not ended")
	}
	// A service that is late was not stopped, and the error names nothing to
	// look at either: the doctor that asked has nothing to run.
	opts.DiagnoseTimeout = 300 * time.Millisecond
	remote, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", service(t, plugintest.Config{
		Before: func(ctx context.Context, tool string) {
			if tool == "diagnose" {
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
		}})), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	_, err = remote.Diagnose(ctx)
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !errors.As(err, &late) || !late.Late ||
		err.Error() != "Knowledge base plugin remote did not answer its diagnose tool within 300ms." {
		t.Errorf("Diagnose of a service that is late = %v", err)
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
	// setup to put right, and no finding. Whatever code it refuses with, the
	// error is failed_precondition: the tool is asked nothing, so
	// invalid_argument and not_found would be said of what the learner
	// typed, which was the plugin's name.
	for code, want := range map[string]string{
		plugincontract.CodeInvalidArgument: "Knowledge base plugin kb does not take what its diagnose tool was given: no settings file",
		plugincontract.CodeNotFound:        "Knowledge base plugin kb does not have what its diagnose tool was asked for: no settings file",
		plugincontract.CodeUnsupported:     "Knowledge base plugin kb cannot do what its diagnose tool was asked: no settings file",
		plugincontract.CodeInternal:        "Knowledge base plugin kb failed in its diagnose tool: no settings file",
		"a_code_of_its_own":                "Knowledge base plugin kb failed in its diagnose tool: no settings file",
	} {
		refuses := start(t, plugintest.NewComputer(t), "--refuse", "diagnose="+code+":no settings file")
		got, err := refuses.Diagnose(ctx)
		if got != nil || codeOf(err) != pluginclient.CodeFailedPrecondition || err.Error() != want {
			t.Errorf("Diagnose of a plugin that refuses with %s = %+v, %v (%s); want failed_precondition and %q", code, got, err, codeOf(err), want)
		}
		if errors.As(err, &late) && late.Late {
			t.Errorf("a plugin that refused with %s is said to be late", code)
		}
		if refuses.Ended() {
			t.Errorf("the Connection ended over a refusal with %s: the plugin answers", code)
		}
	}
	plain := start(t, plugintest.NewComputer(t), "--plain-error", "diagnose=no settings file")
	if _, err := plain.Diagnose(ctx); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		err.Error() != "Knowledge base plugin kb failed in its diagnose tool: no settings file" {
		t.Errorf("Diagnose of a plugin that fails with text = %v", err)
	}

	// And a command that was stopped asks nothing.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := start(t, plugintest.NewComputer(t)).Diagnose(stopped); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Diagnose with a context that is cancelled = %v", err)
	}
}

// The time a plugin has for each thing, when nothing says otherwise, is the
// one written for that thing: the time to say what it found of what it
// depends on is the contract's ten seconds, and not the time of a search,
// which is six times as long and the next line in the same list.
func TestEachTimeAPluginHasIsItsOwnByDefault(t *testing.T) {
	got := pluginclient.WithDefaults(pluginclient.Options{})
	for name, d := range map[string][2]time.Duration{
		"to start":                     {got.StartTimeout, pluginclient.DefaultStartTimeout},
		"to answer a call":             {got.CallTimeout, pluginclient.DefaultCallTimeout},
		"to search":                    {got.SearchTimeout, pluginclient.DefaultSearchTimeout},
		"to say what it found":         {got.DiagnoseTimeout, pluginclient.DefaultDiagnoseTimeout},
		"to say what it found, at all": {got.DiagnoseTimeout, plugincontract.DiagnoseTime},
		"to say what it found, in s":   {got.DiagnoseTimeout, 10 * time.Second},
		"to stop":                      {got.StopGrace, pluginclient.DefaultStopGrace},
	} {
		if d[0] != d[1] {
			t.Errorf("a plugin has %s %s by default, want %s", d[0], name, d[1])
		}
	}
	set := pluginclient.WithDefaults(pluginclient.Options{StartTimeout: time.Second, CallTimeout: 2 * time.Second,
		SearchTimeout: 3 * time.Second, DiagnoseTimeout: 4 * time.Second, StopGrace: 5 * time.Second})
	if set.StartTimeout != time.Second || set.CallTimeout != 2*time.Second || set.SearchTimeout != 3*time.Second ||
		set.DiagnoseTimeout != 4*time.Second || set.StopGrace != 5*time.Second {
		t.Errorf("the times a caller set are not kept: %+v", set)
	}
}
