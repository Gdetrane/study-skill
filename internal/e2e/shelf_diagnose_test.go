//go:build unix

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// The two programs of a release, with an embedding endpoint and a reranking
// endpoint in Shelf's settings file. Shelf starts and speaks the contract
// whether or not they answer, so study doctor has Shelf test them: with both
// up it says nothing is wrong, and with one down it says so, in a warning
// that names the endpoint's host, before a book is read for nothing. study
// knowledge-base check shelf shows the same findings. Neither shows the
// key, or more of an address than its host, whatever the endpoint says.
func TestABuiltStudyHasTheBuiltShelfTestItsEndpoints(t *testing.T) {
	study, shelf := buildStudy(t), shelftest.Shelf(t)
	embedder, reranker := shelftest.NewEndpoint(t), shelftest.NewEndpoint(t)
	const key = "sk-e2e-test-0c7aa1f3d2"
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	state, config := filepath.Join(home, ".local", "state"), filepath.Join(home, ".config")
	env := []string{"HOME=" + home, "STUDY_HOME=" + filepath.Join(home, "study"), "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "LANG=C.UTF-8"}
	must := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(study, args...)
		cmd.Env, cmd.Dir = env, home
		out, err := cmd.Output()
		if exit := (*exec.ExitError)(nil); err != nil && asExit(err, &exit) {
			t.Fatalf("study %s: %v\n%s%s", strings.Join(args, " "), err, out, exit.Stderr)
		} else if err != nil {
			t.Fatalf("study %s: %v", strings.Join(args, " "), err)
		}
		if said := string(out); strings.Contains(said, key) || strings.Contains(said, "/v1") {
			t.Fatalf("study %s shows the endpoint's key, or more of its address than its host:\n%s", strings.Join(args, " "), said)
		}
		return string(out)
	}
	settings := filepath.Join(config, "lamplight", "shelf.toml")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("format = 1\n\n[embedding]\nendpoint = %q\nrecipe = \"embeddinggemma-2\"\nkey = %q\n\n"+
		"[reranking]\nendpoint = %q\nmodel = \"a-reranker\"\nkey = %q\n", embedder.URL, key, reranker.URL, key)
	if err := os.WriteFile(settings, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	must("knowledge-base", "add", "shelf", "--", shelf)

	// findings are the Findings of study doctor about Shelf, by name, and
	// what study knowledge-base check shelf found, by the finding's name.
	doctor := func() map[string]core.Finding {
		t.Helper()
		var d struct {
			Data core.Diagnosis `json:"data"`
		}
		if err := json.Unmarshal([]byte(must("doctor", "--json")), &d); err != nil {
			t.Fatal(err)
		}
		if !d.Data.Healthy {
			t.Errorf("study doctor says the setup is not healthy: %+v", d.Data.Findings)
		}
		found := map[string]core.Finding{}
		for _, f := range d.Data.Findings {
			if strings.HasPrefix(f.Name, "knowledge_base:shelf") {
				found[f.Name] = f
			}
		}
		return found
	}
	check := func() map[string]core.PluginFinding {
		t.Helper()
		var c struct {
			Data core.PluginCheck `json:"data"`
		}
		if err := json.Unmarshal([]byte(must("knowledge-base", "check", "shelf", "--json")), &c); err != nil {
			t.Fatal(err)
		}
		found := map[string]core.PluginFinding{}
		for _, f := range c.Data.Findings {
			found[f.Name] = f
		}
		return found
	}
	// agree fails the test unless the check shows what the doctor showed.
	agree := func(doctors map[string]core.Finding, checks map[string]core.PluginFinding) {
		t.Helper()
		if len(checks) != 2 || len(doctors) != 3 {
			t.Fatalf("study doctor says %+v of Shelf, and study knowledge-base check found %+v; want the plugin and its two endpoints", doctors, checks)
		}
		for name, f := range checks {
			if d := doctors["knowledge_base:shelf:"+name]; (d.Status == core.FindingOK) != f.OK || (d.Status != core.FindingOK && d.Message != f.Message) {
				t.Errorf("of %s, study doctor says %+v and study knowledge-base check %+v", name, d, f)
			}
		}
	}

	// Both answer: nothing is wrong, and each was asked with the key.
	up := doctor()
	if f := up["knowledge_base:shelf"]; f.Status != core.FindingOK {
		t.Errorf("study doctor says of Shelf: %+v", f)
	}
	if f := up["knowledge_base:shelf:embedding"]; f.Status != core.FindingOK ||
		f.Message != "the embedding endpoint "+embedder.Host+" gave a question's vector, of 512 numbers, with the model google/embeddinggemma-2" {
		t.Errorf("with the embedding endpoint up, study doctor says: %+v", f)
	}
	if f := up["knowledge_base:shelf:reranking"]; f.Status != core.FindingOK ||
		f.Message != "the reranking endpoint "+reranker.Host+" ordered two texts for a question, with the model a-reranker" {
		t.Errorf("with the reranking endpoint up, study doctor says: %+v", f)
	}
	for _, at := range []*shelftest.Endpoint{embedder, reranker} {
		if asked := at.Asked(); len(asked) != 1 || asked[0].Key != key {
			t.Errorf("the endpoint %s was asked %+v; want it tested once, with the key from the file", at.Host, asked)
		}
	}
	agree(up, check())
	if said := must("doctor"); !strings.Contains(said, "✓ knowledge_base:shelf:embedding") || !strings.Contains(said, "✓ knowledge_base:shelf:reranking") {
		t.Errorf("for people, study doctor says:\n%s", said)
	}

	// The reranking endpoint refuses, and repeats what it was sent.
	reranker.Answer(func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error": {"message": "the key %s is not allowed at http://%s%s"}}`, r.Header.Get("Authorization"), r.Host, r.URL.Path)
		return true
	})
	refused := doctor()
	if f := refused["knowledge_base:shelf:reranking"]; f.Status != core.FindingWarn || f.Fix != "" ||
		!strings.HasPrefix(f.Message, "the reranking endpoint "+reranker.Host+" answered 403 Forbidden: the key Bearer [the key] is not allowed at [the address]") ||
		!strings.HasSuffix(f.Message, "Without it a search gives its Passages in the order it found them.") {
		t.Errorf("with the reranking endpoint refusing, study doctor says: %+v", f)
	}
	if f := refused["knowledge_base:shelf:embedding"]; f.Status != core.FindingOK {
		t.Errorf("of the embedding endpoint, which answers, study doctor says: %+v", f)
	}
	agree(refused, check())
	reranker.Answer(nil)

	// The embedding endpoint is down.
	embedder.Stop()
	down := doctor()
	if f := down["knowledge_base:shelf:embedding"]; f.Status != core.FindingWarn || f.Fix != "" ||
		!strings.HasPrefix(f.Message, "the embedding endpoint "+embedder.Host+" could not be reached: ") ||
		!strings.HasSuffix(f.Message, "Without it no Source is indexed, and a search is by keyword alone.") {
		t.Errorf("with the embedding endpoint down, study doctor says: %+v; want a warning that names its host", f)
	}
	// Shelf itself is in order, and so is the other endpoint.
	if f := down["knowledge_base:shelf"]; f.Status != core.FindingOK {
		t.Errorf("with an endpoint down, study doctor says of Shelf itself: %+v", f)
	}
	if f := down["knowledge_base:shelf:reranking"]; f.Status != core.FindingOK {
		t.Errorf("of the reranking endpoint, which answers again, study doctor says: %+v", f)
	}
	agree(down, check())
	if said := must("doctor"); !strings.Contains(said, "! knowledge_base:shelf:embedding") || !strings.Contains(said, embedder.Host+" could not be reached") ||
		!strings.Contains(said, "Ready to study, with ") {
		t.Errorf("for people, study doctor says:\n%s", said)
	}
	if said := must("knowledge-base", "check", "shelf"); !strings.Contains(said, "It tested what it depends on:") ||
		!strings.Contains(said, "! embedding  the embedding endpoint "+embedder.Host+" could not be reached") ||
		!strings.Contains(said, "✓ reranking  the reranking endpoint "+reranker.Host) {
		t.Errorf("for people, study knowledge-base check shelf says:\n%s", said)
	}
}
