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

// The two programs of a release, with an embedding endpoint in Shelf's
// settings file, as a learner who serves a model has it: study knowledge-base
// check says that Shelf searches by embeddings, study source index has the
// book indexed with its vectors, and once the learner names another model
// in the file, study says of the book that it is found by keyword alone,
// and why, and study source index puts it right, with --again as without.
//
// The endpoint is the test's own, on this computer. Shelf is told of it by
// the file and by nothing else: study hands a plugin no variable that
// could.
func TestABuiltStudyHasTheBuiltShelfEmbedABook(t *testing.T) {
	study, shelf := buildStudy(t), shelftest.Shelf(t)
	at := shelftest.NewEndpoint(t)
	// The key holds a semicolon, at which what follows "Bearer" in a
	// header's value is taken to end: an endpoint that repeats the header
	// it was sent must not have Shelf leave the second half standing.
	const key = "sk-e2e;test-5b20cc91e7"
	// holdsKey reports whether text holds the key, or either half of it.
	holdsKey := func(text string) bool {
		return strings.Contains(text, key) || strings.Contains(text, "sk-e2e") || strings.Contains(text, "test-5b20cc91e7")
	}
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	state, config := filepath.Join(home, ".local", "state"), filepath.Join(home, ".config")
	env := []string{"HOME=" + home, "STUDY_HOME=" + filepath.Join(home, "study"), "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state, "LANG=C.UTF-8",
		// What a learner's shell may hold, and Shelf must not be told by.
		"OPENAI_API_KEY=sk-not-for-shelf", "OPENAI_BASE_URL=http://127.0.0.1:1/v1"}
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(study, args...)
		cmd.Env, cmd.Dir = env, home
		out, err := cmd.Output()
		if exit := (*exec.ExitError)(nil); err != nil && asExit(err, &exit) {
			return string(out) + string(exit.Stderr), err
		}
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("study %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if holdsKey(out) {
			t.Fatalf("study %s shows the endpoint's key, or a part of it:\n%s", strings.Join(args, " "), out)
		}
		return out
	}
	settings := filepath.Join(config, "lamplight", "shelf.toml")
	write := func(model string) {
		t.Helper()
		text := fmt.Sprintf("format = 1\n\n[embedding]\nendpoint = %q\nrecipe = \"embeddinggemma-2\"\nkey = %q\n", at.URL, key)
		if model != "" {
			text += fmt.Sprintf("model = %q\n", model)
		}
		if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	book := filepath.Join(home, "Books", "elimination.pdf")
	if err := os.MkdirAll(filepath.Dir(book), 0o755); err != nil {
		t.Fatal(err)
	}
	shelftest.WritePDF(t, book, [][]string{
		{"Elimination turns a system of equations into a triangular one."},
		{"Back substitution then gives the unknowns, the last one first."},
	})

	must("topic", "create", "--title", "Linear algebra")
	must("source", "add", "linear-algebra", "--file", book)
	must("knowledge-base", "add", "shelf", "--", shelf)
	must("topic", "update", "linear-algebra", "--knowledge-base", "shelf")
	write("")

	var check struct {
		Data core.PluginCheck `json:"data"`
	}
	if err := json.Unmarshal([]byte(must("knowledge-base", "check", "shelf", "--json")), &check); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(check.Data.Contract.Search); got != "[keyword embedding]" {
		t.Errorf("study knowledge-base check says Shelf searches by %s; want by embeddings too, with an endpoint in its settings file", got)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Fatalf("to say what it can do, Shelf asked the endpoint %+v", asked)
	}

	// index runs study source index, and returns what came of the book.
	index := func(args ...string) core.IndexOutcome {
		t.Helper()
		var indexed struct {
			Data core.IndexResult `json:"data"`
		}
		if err := json.Unmarshal([]byte(must(append([]string{"source", "index", "linear-algebra", "--json"}, args...)...)), &indexed); err != nil {
			t.Fatal(err)
		}
		if len(indexed.Data.Sources) != 1 {
			t.Fatalf("study source index = %+v", indexed.Data.Sources)
		}
		return indexed.Data.Sources[0]
	}
	// listed is what study source list says of the book, which a Shelf
	// started for the question answers. A busy computer may leave it less
	// than its two seconds once, not five times running.
	listed := func() core.SourceStatus {
		t.Helper()
		var list struct {
			Data core.SourceList `json:"data"`
		}
		for tries := 1; ; tries++ {
			list.Data = core.SourceList{}
			if err := json.Unmarshal([]byte(must("source", "list", "linear-algebra", "--json")), &list); err != nil {
				t.Fatal(err)
			}
			if x := list.Data.Indexing; x == nil || x.Problem == "" || tries == 5 {
				break
			}
		}
		if len(list.Data.Sources) != 1 || list.Data.Sources[0].Indexed == nil {
			t.Fatalf("study source list = %+v, with %+v", list.Data.Sources, list.Data.Indexing)
		}
		return list.Data.Sources[0]
	}
	// sent checks what the endpoint was sent since it was last asked: the
	// book's two Passages, marked as the Recipe marks a document, for the
	// model, with the key from the file, and no request key but the two.
	source := listed()
	sent := func(model string) {
		t.Helper()
		asked := at.Asked()
		at.Forget()
		if len(asked) != 1 || asked[0].Path != "/v1/embeddings" || asked[0].Key != key || asked[0].Body["model"] != model ||
			fmt.Sprint(asked[0].Keys()) != "[input model]" {
			t.Fatalf("the endpoint was asked %+v; want the book's Passages, once, for the model %s", asked, model)
		}
		if got, want := fmt.Sprint(asked[0].Texts()), "[title: "+source.Title+" | text: Elimination turns a system of equations into a triangular one. "+
			"title: "+source.Title+" | text: Back substitution then gives the unknowns, the last one first.]"; got != want {
			t.Errorf("the endpoint was sent %s, want %s", got, want)
		}
	}

	if got := index(); got.State != core.IndexIndexed || !got.Asked {
		t.Fatalf("study source index = %+v; want the book indexed", got)
	}
	sent("google/embeddinggemma-2")
	if now := listed().Indexed; now.State != core.IndexIndexed {
		t.Errorf("afterwards the book is %+v", now)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Errorf("to say what is indexed, Shelf asked the endpoint %+v", asked)
	}
	// search runs study evidence search, by a Shelf started for the
	// question, and returns what it found.
	search := func(query string) core.PassageSearch {
		t.Helper()
		var found struct {
			Data core.PassageSearch `json:"data"`
		}
		if err := json.Unmarshal([]byte(must("evidence", "search", "linear-algebra", query, "--json")), &found); err != nil {
			t.Fatal(err)
		}
		return found.Data
	}
	// A search asks the endpoint once, for the question's vector, marked as
	// a question, and returns the book's Passages.
	if got := search("triangular"); len(got.Passages) != 2 || len(got.Searched) != 1 || got.Passages[0].Location != "p. 1" || got.Notice != "" {
		t.Errorf("study evidence search = %+v; want the book's two Passages, the page that holds the word first, and no notice", got)
	}
	if asked := at.Asked(); len(asked) != 1 || fmt.Sprint(asked[0].Texts()) != "[task: search result | query: triangular]" || asked[0].Key != key {
		t.Errorf("for a search the endpoint was asked %+v; want the question's vector, once", asked)
	}
	at.Forget()

	// The learner serves another model, and names it. Twice: what is said
	// of the book is put right by study source index, and by --again.
	for model, again := range map[string][]string{"another-model": nil, "a-third-model": {"--again"}} {
		write(model)
		now := listed().Indexed
		if now.State != core.IndexOutdated || now.Retry != "" {
			t.Fatalf("with %s named in Shelf's settings, the book is %+v; want it outdated: indexed, and to be indexed again", model, now)
		}
		for _, word := range []string{"found by keyword alone", "its vectors are of the model", "now name the model " + model} {
			if !strings.Contains(now.Reason, word) {
				t.Errorf("the reason does not say %q: %s", word, now.Reason)
			}
		}
		// status says so, and names the command that brings it up to date
		// as what to do next.
		status := must("status")
		for _, word := range []string{"to be indexed again", "found by keyword alone", "Run: study source index linear-algebra"} {
			if !strings.Contains(status, word) {
				t.Errorf("study status does not say %q:\n%s", word, status)
			}
		}
		if asked := at.Asked(); len(asked) != 0 {
			t.Errorf("to say so, Shelf asked the endpoint %+v", asked)
		}
		// Meanwhile the book is still found, by its words: the search asks
		// no endpoint, having no vector to compare a question's with, and
		// carries Shelf's notice that it was by keyword alone, and why.
		if got := search("substitution"); len(got.Passages) != 1 || len(got.LeftOut) != 0 || got.Passages[0].Location != "p. 2" ||
			!strings.Contains(got.Notice, "found by keyword alone: none of the Sources searched has vectors of the model "+model) {
			t.Errorf("while the book is outdated, study evidence search = %+v; want it found by keyword, and saying so", got)
		}
		if asked := at.Asked(); len(asked) != 0 {
			t.Errorf("a search of a book without vectors of the model asked the endpoint %+v", asked)
		}
		if got := index(again...); got.State != core.IndexIndexed || !got.Asked {
			t.Fatalf("study source index %s = %+v; want the book asked for, and indexed", strings.Join(again, " "), got)
		}
		sent(model)
		if now := listed().Indexed; now.State != core.IndexIndexed || now.Reason != "" {
			t.Errorf("once it was indexed again, the book is %+v", now)
		}
	}
	// With its vectors the model's, --again asks and changes nothing.
	if got := index("--again"); got.State != core.IndexIndexed || !got.Asked {
		t.Errorf("study source index --again = %+v", got)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Errorf("indexing again what is indexed, vectors and all, Shelf asked the endpoint %+v", asked)
	}

	// The endpoint stops giving vectors. A search is answered all the same,
	// by keyword, and study shows what Shelf says of it: for people before
	// the Passages, and as JSON in a field of its own, which is what the
	// learner's agent is given too. The endpoint refuses in words that
	// repeat the key it was sent and the address it was asked at: what
	// study shows holds neither, and names the endpoint by its host.
	at.Answer(func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": {"message": "no model is loaded for %s, asked at http://%s%s"}}`, r.Header.Get("Authorization"), r.Host, r.URL.Path)
		return true
	})
	noticed := func(what string, got core.PassageSearch, words ...string) {
		t.Helper()
		if len(got.Passages) != 1 || got.Passages[0].Location != "p. 2" || len(got.LeftOut) != 0 || got.Note != "" || got.Action != "" {
			t.Errorf("%s, study evidence search = %+v; want the Passage keyword search finds, and nothing to do", what, got)
		}
		for _, word := range append(words, "These Passages were found by keyword alone: the embedding endpoint "+at.Host) {
			if !strings.Contains(got.Notice, word) {
				t.Errorf("%s, the search's notice does not say %q: %s", what, word, got.Notice)
			}
		}
		for _, not := range []string{key, "sk-e2e", "test-5b20cc91e7", "/v1", "http://", "embeddings"} {
			if strings.Contains(got.Notice, not) {
				t.Errorf("%s, the search's notice repeats %q, of the endpoint's key or of its address: %s", what, not, got.Notice)
			}
		}
		// For people it is under the line that says what was found, and
		// before the Passage.
		shown := must("evidence", "search", "linear-algebra", "substitution")
		notice, passage := strings.Index(shown, "! The plugin says of this search: "+got.Notice+"\n"), strings.Index(shown, "> Back substitution")
		if !strings.HasPrefix(shown, "Knowledge base plugin shelf found 1 Passage") || notice < 0 || passage < notice {
			t.Errorf("%s, study evidence search does not show Shelf's notice before the Passage:\n%s", what, shown)
		}
	}
	noticed("with an endpoint that refuses", search("substitution"), "answered 500 Internal Server Error",
		"no model is loaded for Bearer [the key], asked at [the address]")
	// And once nothing listens there at all.
	at.Stop()
	noticed("with the endpoint down", search("substitution"), "could not be reached")

	// A settings file Shelf cannot use: study says so, with Shelf's reason,
	// where it says what is wrong with a plugin, and repeats no key.
	if err := os.WriteFile(settings, []byte("format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:1/v1\"\nrecipe = \"embeddinggemma-2\"\nkey = \""+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run("knowledge-base", "check", "shelf")
	if err == nil || !strings.Contains(out, "Shelf's settings file") || !strings.Contains(out, "is not valid TOML at line 5") {
		t.Errorf("study knowledge-base check, with a settings file Shelf cannot read: %v\n%s", err, out)
	}
	if holdsKey(out) {
		t.Errorf("study repeats the key that the file holds, or a part of it:\n%s", out)
	}
}
