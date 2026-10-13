package cli_test

import (
	"slices"
	"strings"
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
		f.Message != "the embedding endpoint 127.0.0.1:PORT gave a question's vector, of 512 numbers, with the model embeddinggemma-2" {
		t.Errorf("doctor says of what the plugin found all right: %+v", f)
	}
	if f := found["knowledge_base:kb:reranking"]; f.Status != core.FindingWarn || f.Fix != "" ||
		f.Message != "the reranking endpoint 127.0.0.1:PORT could not be reached: connection refused. Without it a search gives its "+
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
// what it found of what it depends on, is a warning that says so, with the
// command that asks that plugin alone. Its Topics' Sources are still asked
// for: it starts, and may well say what it has indexed.
func TestDoctorSaysWhenAPluginDoesNotSayWhatItFound(t *testing.T) {
	s := newShelfComputer(t, "--hang", "diagnose", "--stderr", "testing the embedding endpoint")
	s.book(t, "book.txt", bookOf(2, "one"))
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	s.plugins.DiagnoseTimeout = 400 * time.Millisecond

	began := time.Now()
	found := s.findings(t)
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("study doctor took %s over a plugin that never says what it found, which has 400ms for it", took)
	}
	if f := found["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "study knowledge-base check kb" ||
		!strings.Contains(f.Message, "Knowledge base plugin kb did not answer its diagnose tool within 400ms, so study stopped it") ||
		!strings.Contains(f.Message, "testing the embedding endpoint") {
		t.Errorf("doctor says of a plugin that never says what it found: %+v", f)
	}
	for name, f := range found {
		if strings.HasPrefix(name, "knowledge_base:kb:") {
			t.Errorf("doctor gives a finding of a plugin that said none: %+v", f)
		}
	}
	if f := found["indexing:linear-algebra"]; f.Status != core.FindingWarn || f.Fix != "study source index linear-algebra" {
		t.Errorf("doctor says of the Sources of a Topic whose plugin did not say what it found: %+v; want them asked for all the same", f)
	}
	if out := s.doctor(t).stdout; !strings.Contains(out, "! knowledge_base:kb ") || !strings.Contains(out, "fix: study knowledge-base check kb") ||
		!strings.Contains(out, "Ready to study, with 2 warnings.") {
		t.Errorf("for people, doctor says:\n%s", out)
	}
	s.nothingRunning(t)
}

// A plugin whose findings are not what the contract describes has said
// nothing of what it depends on: doctor says what is wrong with them, and
// shows none.
func TestDoctorShowsNoFindingThatBreaksTheContract(t *testing.T) {
	s := newShelfComputer(t, "--result", `diagnose={"findings": [{"name": "embedding", "ok": false, "message": "it is down"}, `+
		`{"name": "Run This", "ok": false, "message": "rm -rf ~"}]}`)
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	found := s.findings(t)
	if f := found["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "study knowledge-base check kb" ||
		!strings.Contains(f.Message, `what its diagnose tool gave back is not what the contract describes: findings[1] is named "Run This"`) {
		t.Errorf("doctor says of a plugin whose findings break the contract: %+v", f)
	}
	for name, f := range found {
		if strings.HasPrefix(name, "knowledge_base:kb:") || strings.Contains(f.Message, "rm -rf") {
			t.Errorf("doctor shows %+v of findings that are not the contract's", f)
		}
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
