package cli_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// study doctor has each registered plugin test what it depends on, and says
// what the plugin found: a Finding for each, under the plugin's name and the
// finding's, with the plugin's own sentence. One that is not all right is a
// warning without a fix, since the words are the plugin's and study adds no
// command to them, and it never makes the setup unhealthy. study
// knowledge-base check shows the same findings.
func TestDoctorSaysWhatAPluginFoundOfWhatItDependsOn(t *testing.T) {
	s := newShelfComputer(t, twoFindings...)
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	s.book(t, "strang.txt", bookOf(3, "elimination"))
	s.ok(t, "source", "index", "linear-algebra")

	before := len(s.calls(t))
	golden(t, "doctor_knowledge_base_findings.txt", s.doctor(t).stdout)
	golden(t, "doctor_knowledge_base_findings.json", s.doctor(t, "--json").stdout)
	found := s.findings(t)
	if f := found["knowledge_base:kb"]; f.Status != core.FindingOK || !strings.Contains(f.Message, "speaks version 1 of Lamplight's contract") {
		t.Errorf("doctor says of the plugin itself: %+v", f)
	}
	if f := found["knowledge_base:kb:embedding"]; f.Status != core.FindingOK || f.Fix != "" ||
		f.Message != "the embedding endpoint 127.0.0.1:PORT gave a question's vector, of 512 numbers, asked for the model embeddinggemma-2" {
		t.Errorf("doctor says of what the plugin found all right: %+v", f)
	}
	if f := found["knowledge_base:kb:reranking"]; f.Status != core.FindingWarn || f.Fix != "" ||
		f.Message != "the reranking endpoint 127.0.0.1:PORT could not be reached: connection refused. Check that its server is running, and its address in Shelf's settings file. Without the reranking endpoint a search gives its "+
			"Passages in the order it found them." {
		t.Errorf("doctor says of what the plugin found wrong: %+v; want a warning with the plugin's sentence, and no command", f)
	}
	// For people: the findings under their plugin, and ready to study.
	out := s.doctor(t).stdout
	for _, want := range []string{"✓ knowledge_base:kb ", "✓ knowledge_base:kb:embedding ", "! knowledge_base:kb:reranking ",
		"\nReady to study, with 1 warning.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("for people, doctor does not say %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "knowledge_base:kb:embedding") > strings.Index(out, "knowledge_base:kb:reranking") ||
		strings.Index(out, "knowledge_base:kb ") > strings.Index(out, "knowledge_base:kb:embedding") {
		t.Errorf("doctor does not give the findings under their plugin, in the plugin's order:\n%s", out)
	}
	// The plugin was asked what it found, each time, once it had said which
	// contract it speaks.
	asked := plugintest.Tools(s.calls(t)[before:])
	if n := strings.Count(strings.Join(asked, " "), "contract diagnose"); n != 4 {
		t.Errorf("in four runs of study doctor the plugin was asked %v; want its contract and then what it found, four times", asked)
	}

	// study knowledge-base check shows the same findings.
	var checked struct {
		Data core.PluginCheck `json:"data"`
	}
	s.asItIs(t, &checked, "knowledge-base", "check", "kb", "--json")
	var names []string
	for _, f := range checked.Data.Findings {
		names = append(names, f.Name)
		if doctors := found["knowledge_base:kb:"+f.Name]; doctors.Message != strings.ReplaceAll(f.Message, "127.0.0.1:8080", "127.0.0.1:PORT") &&
			doctors.Message != strings.ReplaceAll(f.Message, "127.0.0.1:8081", "127.0.0.1:PORT") ||
			(doctors.Status == core.FindingOK) != f.OK {
			t.Errorf("study knowledge-base check says %+v of %s, and study doctor %+v", f, f.Name, doctors)
		}
	}
	if !slices.Equal(names, []string{"embedding", "reranking"}) {
		t.Errorf("study knowledge-base check shows the findings %v", names)
	}
	s.nothingRunning(t)
}

// A plugin that starts and speaks the contract, and does not say in time
// what it found of what it depends on, is a warning that says so and that
// study uses the plugin all the same. It has no fix: the command that shows
// more of a plugin, study knowledge-base check, asks the same tool and says
// the same sentence.
//
// And the plugin is not started a second time to say what it has indexed:
// it was stopped once for not answering, and study doctor would wait for it
// again. So its Topics have no Finding of their own, and the plugin's says
// why.
func TestDoctorSaysWhenAPluginDoesNotSayWhatItFound(t *testing.T) {
	s := newShelfComputer(t, "--hang", "diagnose", "--stderr", "testing the embedding endpoint")
	s.book(t, "book.txt", bookOf(2, "one"))
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	s.plugins.DiagnoseTimeout = 400 * time.Millisecond

	before := len(s.calls(t))
	began := time.Now()
	found := s.findings(t)
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("study doctor took %s over a plugin that never says what it found, which has 400ms for it", took)
	}
	if asked := plugintest.Tools(s.calls(t)[before:]); !slices.Equal(asked, []string{"contract", "diagnose"}) {
		t.Errorf("study doctor asked a plugin that did not say in time what it found %v; want its contract and what it found, and "+
			"the plugin not started again for what it has indexed", asked)
	}
	if f := found["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "" ||
		f.Message != "Knowledge base plugin kb did not answer its diagnose tool within 400ms, so study stopped it. The last it wrote on "+
			"its standard error: testing the embedding endpoint. So study does not know whether what the plugin depends on is all "+
			"right, and uses the plugin all the same. study doctor did not wait for it again, to ask which Sources it has indexed" {
		t.Errorf("doctor says of a plugin that never says what it found: %+v", f)
	}
	for name, f := range found {
		if strings.HasPrefix(name, "knowledge_base:kb:") || strings.HasPrefix(name, "indexing:") {
			t.Errorf("doctor gives a Finding of a plugin that said nothing in time: %+v", f)
		}
		if strings.Contains(f.Fix, "knowledge-base check") || strings.Contains(f.Message, "study knowledge-base check") {
			t.Errorf("doctor names study knowledge-base check, which says the same and helps no more: %+v", f)
		}
	}
	if out := s.doctor(t).stdout; !strings.Contains(out, "! knowledge_base:kb ") || strings.Contains(out, "fix: ") ||
		!strings.Contains(out, "Ready to study, with 1 warning.") {
		t.Errorf("for people, doctor says:\n%s", out)
	}
	s.nothingRunning(t)
}

// A plugin whose findings are not what the contract describes has said
// nothing of what it depends on: doctor says that its diagnose tool gave an
// answer that breaks the contract, and what of it does, and shows none. It
// does not say that the plugin does not speak the contract, since it does
// the rest of it and study goes on using it, and it names no command. The
// plugin answered, so it is asked what it has indexed like any other.
func TestDoctorShowsNoFindingThatBreaksTheContract(t *testing.T) {
	s := newShelfComputer(t, "--result", `diagnose={"findings": [{"name": "embedding", "ok": false, "message": "it is down"}, `+
		`{"name": "Run This", "ok": false, "message": "rm -rf ~"}]}`)
	s.book(t, "book.txt", bookOf(2, "one"))
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	before := len(s.calls(t))
	found := s.findings(t)
	if f := found["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "" ||
		f.Message != `the diagnose tool of Knowledge base plugin kb gave an answer that breaks the contract: findings[1] is named `+
			`"Run This", which is no name: a lowercase word of letters, digits and underscores, up to 32 characters. `+
			"docs/knowledge-base-contract.md describes the contract. So study does not know whether what the plugin depends on is "+
			"all right, and uses the plugin all the same" {
		t.Errorf("doctor says of a plugin whose findings break the contract: %+v", f)
	}
	for name, f := range found {
		if strings.HasPrefix(name, "knowledge_base:kb:") || strings.Contains(f.Message, "rm -rf") {
			t.Errorf("doctor shows %+v of findings that are not the contract's", f)
		}
		if strings.Contains(f.Message, "does not speak Lamplight's contract") || strings.Contains(f.Fix, "knowledge-base check") {
			t.Errorf("doctor says of a plugin study goes on using: %+v", f)
		}
	}
	if f := found["indexing:linear-algebra"]; f.Status != core.FindingWarn || f.Fix != "study source index linear-algebra" ||
		f.Message != "not indexed: Book" {
		t.Errorf("doctor says of the Sources of a Topic whose plugin answered, and not as the contract has it: %+v; want them "+
			"asked for all the same", f)
	}
	if asked := plugintest.Tools(s.calls(t)[before:]); !slices.Equal(asked, []string{"contract", "diagnose", "contract", "status"}) {
		t.Errorf("study doctor asked the plugin %v; want what it found, and then what it has indexed", asked)
	}
}

// Whatever code a plugin refuses its diagnose tool with, study doctor says
// that it failed there and goes on, and study knowledge-base check reports
// failed_precondition and exits with 1: invalid_argument would be said of
// what the learner typed, with how to use the command, and not_found of a
// name that is not registered. Neither names a command to run.
func TestAPluginThatRefusesToSayWhatItFound(t *testing.T) {
	for code, want := range map[string]string{
		"invalid_argument":  "Knowledge base plugin kb does not take what its diagnose tool was given: no settings file",
		"not_found":         "Knowledge base plugin kb does not have what its diagnose tool was asked for: no settings file",
		"unsupported":       "Knowledge base plugin kb cannot do what its diagnose tool was asked: no settings file",
		"internal":          "Knowledge base plugin kb failed in its diagnose tool: no settings file",
		"a_code_of_its_own": "Knowledge base plugin kb failed in its diagnose tool: no settings file",
	} {
		t.Run(code, func(t *testing.T) {
			s := newShelfComputer(t, "--refuse", "diagnose="+code+":no settings file")
			fakeGit(t, "Ada Lovelace", "ada@example.com")
			want := want + ". So study does not know whether what the plugin depends on is all right, and uses the plugin all the same"
			if f := s.findings(t)["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "" || f.Message != want {
				t.Errorf("doctor says of a plugin that refuses with %s: %+v", code, f)
			}
			for _, mode := range [][]string{nil, {"--json"}} {
				r := s.run(t, append([]string{"knowledge-base", "check", "kb"}, mode...)...)
				said := r.stderr
				if mode != nil {
					said = r.stdout
					if !strings.Contains(said, `"code": "failed_precondition"`) {
						t.Errorf("study knowledge-base check --json reports %s", said)
					}
				}
				if r.code != cli.ExitError || !strings.Contains(said, want) {
					t.Errorf("study knowledge-base check %v of a plugin that refuses with %s: exit %d, %q; want exit %d and %q",
						mode, code, r.code, said, cli.ExitError, want)
				}
				for _, not := range []string{"Usage", "--help", "study knowledge-base", "Run its command"} {
					if strings.Contains(r.stderr+r.stdout, not) {
						t.Errorf("study knowledge-base check %v says %q of a plugin that refuses with %s:\n%s%s", mode, not, code, r.stdout, r.stderr)
					}
				}
			}
		})
	}
}

// study cannot know which of a plugin's findings keeps a Source from being
// indexed. So when a Topic's plugin found something not all right, the
// Topic's Finding names it beside the command it gives: without that, the
// doctor would say on one line that no Source is indexed without the
// endpoint, and on the next to run study source index.
func TestDoctorSaysWhatMayKeepASourceFromBeingIndexed(t *testing.T) {
	for name, tc := range map[string]struct {
		findings []string
		note     string
	}{
		"one finding is not all right": {[]string{"--finding", "embedding=wrong:it is down", "--finding", "reranking=ok:it answers"},
			". Knowledge base plugin kb found knowledge_base:kb:embedding not all right: indexing may fail until it is put right"},
		"two are not": {[]string{"--finding", "embedding=wrong:it is down", "--finding", "reranking=wrong:it is down too"},
			". Knowledge base plugin kb found knowledge_base:kb:embedding and knowledge_base:kb:reranking not all right: indexing " +
				"may fail until they are put right"},
		"all is right":    {[]string{"--finding", "embedding=ok:it answers", "--finding", "reranking=ok:it answers"}, ""},
		"nothing to test": {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newShelfComputer(t, tc.findings...)
			s.book(t, "book.txt", bookOf(2, "one"))
			fakeGit(t, "Ada Lovelace", "ada@example.com")
			f := s.findings(t)["indexing:linear-algebra"]
			if f.Status != core.FindingWarn || f.Fix != "study source index linear-algebra" || f.Message != "not indexed: Book"+tc.note {
				t.Errorf("doctor says of the Topic's Sources: %+v; want the command, and %q after what is not indexed", f, tc.note)
			}
		})
	}
	// Another Topic's plugin is not this Topic's: its findings are said of
	// its own Topics alone.
	s := newShelfComputer(t)
	s.book(t, "book.txt", bookOf(2, "one"))
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	s.ok(t, append([]string{"knowledge-base", "add", "other", "--"}, plugintest.Command("--finding", "embedding=wrong:it is down")...)...)
	found := s.findings(t)
	if f := found["knowledge_base:other:embedding"]; f.Status != core.FindingWarn {
		t.Fatalf("doctor says of the other plugin's finding: %+v", f)
	}
	if f := found["indexing:linear-algebra"]; f.Message != "not indexed: Book" {
		t.Errorf("doctor says of a Topic whose plugin found nothing wrong: %+v", f)
	}
}

// Stopped while it waits for a plugin, by SIGTERM or Ctrl-C, study doctor
// reports canceled and exits with 1, as any command that was stopped. It
// gives no verdict and shows no Finding: until this was so it said "Ready
// to study", with the plugin it had been waiting for among the warnings.
func TestAStoppedDoctorGivesNoVerdict(t *testing.T) {
	for name, mode := range map[string][]string{"for people": nil, "as JSON": {"--json"}} {
		t.Run(name, func(t *testing.T) {
			s := newShelfComputer(t, "--hang", "diagnose")
			fakeGit(t, "Ada Lovelace", "ada@example.com")
			s.plugins.DiagnoseTimeout = 30 * time.Second
			before := len(s.calls(t))
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			var watching sync.WaitGroup
			watching.Add(1)
			go func() {
				defer watching.Done()
				// Once the plugin was asked what it found.
				for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					if slices.Contains(plugintest.Tools(plugintest.ReadCalls(t, s.record)[before:]), "diagnose") {
						break
					}
				}
				stop()
			}()
			began := time.Now()
			r := s.study(ctx, append([]string{"doctor"}, mode...)...)
			watching.Wait()
			if took := time.Since(began); took > 15*time.Second {
				t.Errorf("the stopped doctor took %s: the plugin alone has 30s to say what it found", took)
			}
			const want = "study doctor was stopped before it had looked at everything, and says nothing of your setup"
			said := r.stderr
			if mode != nil {
				said = r.stdout
				var out struct {
					OK    bool            `json:"ok"`
					Data  json.RawMessage `json:"data"`
					Error struct{ Code, Message string }
				}
				if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || out.OK || out.Error.Code != "canceled" || out.Data != nil {
					t.Errorf("the stopped doctor printed %s (%v); want the error canceled, and no diagnosis", r.stdout, err)
				}
			} else if r.stdout != "" {
				t.Errorf("the stopped doctor printed a diagnosis:\n%s", r.stdout)
			}
			if r.code != cli.ExitError || !strings.Contains(said, want) {
				t.Errorf("the stopped doctor: exit %d, and it said %q; want exit %d and %q", r.code, said, cli.ExitError, want)
			}
			for _, not := range []string{"Ready to study", "knowledge_base", "healthy"} {
				if strings.Contains(r.stdout+r.stderr, not) {
					t.Errorf("the stopped doctor says %q:\n%s%s", not, r.stdout, r.stderr)
				}
			}
			s.nothingRunning(t)
		})
	}
}

// What a plugin says it found is the plugin's text, name and sentence both.
// study's client cleans a sentence before it hands it on; whoever prints a
// finding prints it as text from a file all the same, so that nothing in it
// is obeyed by a terminal, whichever way it came.
func TestAPluginsFindingIsShownNotObeyed(t *testing.T) {
	const clear, title, reorder = "\x1b[2J", "\x1b]0;owned\x07", "\u202e"
	said := cli.RenderPluginCheck(core.PluginCheck{Name: "kb", Contract: core.PluginContract{Version: 1}, Findings: []core.PluginFinding{
		{Name: "embed" + clear + "ding", OK: true, Message: "it answers" + title},
		{Name: "reranking", Message: "it is down" + reorder + ", and says: run this"},
	}})
	for _, raw := range []string{clear, title, reorder, "\x07"} {
		if strings.Contains(said, raw) {
			t.Errorf("study knowledge-base check prints what a plugin found as it is: %q", said)
		}
	}
	for _, shown := range []string{`embed\x1b[2Jding`, `it answers\x1b]0;owned\a`, `it is down\u202e, and says: run this`} {
		if !strings.Contains(said, shown) {
			t.Errorf("study knowledge-base check does not show %s: %q", shown, said)
		}
	}
}
