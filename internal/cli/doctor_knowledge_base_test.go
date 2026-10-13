package cli_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// doctor runs study doctor on the computer, which must come out healthy:
// nothing about a Knowledge base plugin keeps the learner from studying.
// And it asks the plugin which contract it speaks and what it has indexed,
// never anything else: study doctor has no plugin index. It writes nothing
// in the Study home, and nothing in the configuration folder, where the
// registry is. The record's place, another in every run of the tests, is
// replaced.
func (s *shelfComputer) doctor(t *testing.T, args ...string) result {
	t.Helper()
	before := len(s.calls(t))
	kept := []string{s.env["STUDY_HOME"], filepath.Dir(s.registry())}
	var was []map[string]string
	for _, dir := range kept {
		was = append(was, tree256(t, dir))
	}
	r := s.run(t, append([]string{"doctor"}, args...)...)
	if r.code != cli.ExitOK || r.stderr != "" {
		t.Errorf("study doctor %s: exit %d, stderr %q", strings.Join(args, " "), r.code, r.stderr)
	}
	for _, call := range s.calls(t)[before:] {
		if call.Tool != "contract" && call.Tool != "status" {
			t.Errorf("study doctor %s asked the plugin %s", strings.Join(args, " "), call.Tool)
		}
	}
	for i, dir := range kept {
		if now := tree256(t, dir); !maps.Equal(now, was[i]) {
			t.Errorf("study doctor %s changed what %s holds", strings.Join(args, " "), dir)
		}
	}
	r.stdout = strings.ReplaceAll(r.stdout, s.record, "$RECORD")
	return r
}

// findings are the Findings of study doctor --json, by name.
func (s *shelfComputer) findings(t *testing.T) map[string]core.Finding {
	t.Helper()
	var out struct {
		Data core.Diagnosis `json:"data"`
	}
	if err := json.Unmarshal([]byte(s.doctor(t, "--json").stdout), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Data.Healthy {
		t.Errorf("study doctor says the setup is not healthy: %+v", out.Data.Findings)
	}
	found := map[string]core.Finding{}
	for _, f := range out.Data.Findings {
		found[f.Name] = f
	}
	return found
}

// study doctor says of the Knowledge base plugins what a learner can act on:
// that each registered plugin starts and speaks the contract, which Topic
// names a plugin this computer lacks, and which Sources are not indexed,
// with the one command that helps, by status's own rule. It asks a plugin
// its contract and what it has indexed, and never to index.
func TestDoctorChecksThePluginsAndSaysWhatIsIndexed(t *testing.T) {
	s := newShelfComputer(t)
	// A Topic that names a plugin this computer lacks, and one whose only
	// Source will fail: made while git is the computer's own.
	for _, topic := range [][]string{{"C", "c", "shelf"}, {"Go", "go", "kb"}} {
		s.ok(t, "topic", "create", "--title", topic[0])
		s.ok(t, "topic", "update", topic[1], "--knowledge-base", topic[2])
	}
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	strang := s.book(t, "strang.txt", bookOf(3, "elimination"))
	s.book(t, "scan.txt", " \n")
	s.book(t, "lost.txt", bookOf(2, "eigenvalues"))
	if err := os.Remove(filepath.Join(s.home, "Books", "lost.txt")); err != nil {
		t.Fatal(err)
	}
	paper := filepath.Join(s.home, "Books", "paper.txt")
	writeFile(t, paper, " \n")
	s.ok(t, "source", "add", "go", "--file", paper)

	// Nothing is indexed: the command that indexes is the one to run.
	before := len(s.calls(t))
	golden(t, "doctor_knowledge_base_not_indexed.txt", s.doctor(t).stdout)
	golden(t, "doctor_knowledge_base_not_indexed.json", s.doctor(t, "--json").stdout)
	found := s.findings(t)
	if f := found["indexing:linear-algebra"]; f.Status != core.FindingWarn || f.Fix != "study source index linear-algebra" ||
		!strings.Contains(f.Message, "not indexed: Strang, Scan, Lost (its file is not on this computer)") {
		t.Errorf("with nothing indexed, doctor says of linear-algebra's Sources: %+v", f)
	}
	if f := found["indexing:c"]; f.Status != core.FindingWarn || f.Fix != "study knowledge-base add shelf -- <command>" ||
		f.Message != "no Knowledge base plugin named shelf is registered on this computer" {
		t.Errorf("doctor says of a Topic that names a plugin this computer lacks: %+v", f)
	}
	if f := found["knowledge_base:kb"]; f.Status != core.FindingOK || !strings.Contains(f.Message, "speaks version 1 of Lamplight's contract") {
		t.Errorf("doctor says of the plugin: %+v", f)
	}
	if _, has := found["knowledge_base:shelf"]; has {
		t.Errorf("doctor checked a plugin that is not registered: %+v", found["knowledge_base:shelf"])
	}

	// Indexed: what failed is said with its reason and is nothing to run a
	// command for, and a file that is not here is the listing's to explain.
	s.ok(t, "source", "index", "linear-algebra")
	s.ok(t, "source", "index", "go")
	golden(t, "doctor_knowledge_base_indexed.txt", s.doctor(t).stdout)
	found = s.findings(t)
	if f := found["indexing:go"]; f.Status != core.FindingOK || f.Fix != "" ||
		f.Message != "indexing failed: Paper (there is no text in it)" {
		t.Errorf("doctor says of a Topic whose one Source failed: %+v; want the reason, and no command", f)
	}
	if f := found["indexing:linear-algebra"]; f.Status != core.FindingWarn || f.Fix != "study source list linear-algebra" ||
		f.Message != "not indexed: Lost (its file is not on this computer); indexing failed: Scan (there is no text in it)" {
		t.Errorf("doctor says of linear-algebra's Sources, once they were indexed: %+v", f)
	}
	// With the file back and indexed, only the failure is left to tell of.
	writeFile(t, filepath.Join(s.home, "Books", "lost.txt"), bookOf(2, "eigenvalues"))
	s.ok(t, "source", "index", "linear-algebra")
	if f := s.findings(t)["indexing:linear-algebra"]; f.Status != core.FindingOK || f.Fix != "" ||
		f.Message != "indexing failed: Scan (there is no text in it)" {
		t.Errorf("doctor says of linear-algebra's Sources, with all but the scan indexed: %+v", f)
	}

	// A Source the plugin holds and would index otherwise now is to be
	// indexed again, and the same command does it.
	var list struct {
		Data core.SourceList `json:"data"`
	}
	s.asItIs(t, &list, "source", "list", "linear-algebra", "--json")
	answer, err := json.Marshal(map[string]any{"sources": []any{map[string]any{"id": strang, "state": "outdated",
		"hash": list.Data.Sources[0].Hash, "reason": "its Passages were indexed before an embedding model was set"}}})
	if err != nil || list.Data.Sources[0].ID != strang {
		t.Fatalf("the Topic's Sources are %+v (%v)", list.Data.Sources, err)
	}
	s.registerWith(t, "--result", "status="+string(answer))
	if f := s.findings(t)["indexing:linear-algebra"]; f.Status != core.FindingWarn || f.Fix != "study source index linear-algebra" ||
		!strings.Contains(f.Message, "to be indexed again: Strang (its Passages were indexed before an embedding model was set)") {
		t.Errorf("doctor says of a Source that is outdated: %+v", f)
	}

	// And the doctor did ask: a plugin was started for it each time.
	if got := plugintest.Tools(s.calls(t)[before:]); !slices.Contains(got, "status") || !slices.Contains(got, "contract") {
		t.Errorf("the plugin was asked %v; want its contract and what it has indexed among it", got)
	}
	s.nothingRunning(t)
}

// A plugin that does not answer is not waited for, and one whose program is
// gone is said to be: each with what to do, as a warning. The Topic that
// names such a plugin has nothing to add, and the setup stays healthy.
func TestDoctorWaitsForNoPlugin(t *testing.T) {
	s := newShelfComputer(t, "--hang", "start")
	s.book(t, "book.txt", bookOf(2, "one"))
	gone := filepath.Join(s.home, "bin", "old-kb")
	s.program(t, gone)
	s.ok(t, "knowledge-base", "add", "old", "--", "old-kb")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	// A plugin has half a second for a glance, and thirty to start when
	// study knowledge-base check asks it.
	s.plugins.GlanceTimeout, s.plugins.StartTimeout = 500*time.Millisecond, 30*time.Second

	began := time.Now()
	found := s.findings(t)
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("study doctor took %s over a plugin that never answers, which has half a second", took)
	}
	if f := found["knowledge_base:kb"]; f.Status != core.FindingWarn || f.Fix != "study knowledge-base check kb" ||
		f.Message != "Knowledge base plugin kb did not say which contract it speaks within 500ms, and study did not wait for it" {
		t.Errorf("doctor says of a plugin that never answers: %+v", f)
	}
	if f := found["knowledge_base:old"]; f.Status != core.FindingWarn ||
		!strings.Contains(f.Message, `Knowledge base plugin old cannot be started: there is no program at "$HOME/bin/old-kb"`) ||
		!strings.Contains(f.Fix, "study knowledge-base add old --replace -- <command>") {
		t.Errorf("doctor says of a plugin whose program is gone: %+v", f)
	}
	if f, has := found["indexing:linear-algebra"]; has {
		t.Errorf("doctor says of the Topic what its plugin's own Finding says already: %+v", f)
	}
	// For people it is two warnings, and ready to study.
	out := s.doctor(t).stdout
	for _, want := range []string{"! knowledge_base:kb", "fix: study knowledge-base check kb", "! knowledge_base:old", "Ready to study, with 2 warnings."} {
		if !strings.Contains(out, want) {
			t.Errorf("for people, doctor does not say %q:\n%s", want, out)
		}
	}
	s.nothingRunning(t)
}

// A registry study cannot read is said once, in the registry's own words,
// which say what to do: no plugin is started, and no Topic repeats it.
func TestDoctorSaysWhyTheRegistryIsNotRead(t *testing.T) {
	s := newShelfComputer(t)
	s.book(t, "book.txt", bookOf(2, "one"))
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	writeFile(t, s.registry(), "{")
	started, _ := os.ReadFile(s.record)
	found := s.findings(t)
	if f := found["knowledge_base"]; f.Status != core.FindingWarn || !strings.Contains(f.Message, "knowledge-base-plugins.json is damaged") ||
		!strings.Contains(f.Message, "fix it by hand, or delete it and register your Knowledge base plugins") {
		t.Errorf("doctor says of a registry that is damaged: %+v", f)
	}
	for name, f := range found {
		if strings.HasPrefix(name, "knowledge_base:") || strings.HasPrefix(name, "indexing:") {
			t.Errorf("with a registry it cannot read, doctor also says: %+v", f)
		}
	}
	if now, _ := os.ReadFile(s.record); string(now) != string(started) {
		t.Error("with a registry it cannot read, doctor started a plugin")
	}
}
