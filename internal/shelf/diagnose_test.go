//go:build unix

package shelf_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// What Shelf sends an endpoint to test it: text of its own, and nothing
// else.
const (
	testQuestion = "What does a shelf hold?"
	testTextOne  = "A shelf holds books."
	testTextTwo  = "A lamp gives light."
)

// diagnose asks Shelf what it found of what it depends on, and holds the
// answer to the contract and to what no finding may hold: the key, or more
// of an endpoint's address than its host.
func (s *session) diagnose() map[string]plugincontract.Finding {
	s.t.Helper()
	var out plugincontract.DiagnoseResult
	began := time.Now()
	s.ok(plugincontract.ToolDiagnose, struct{}{}, &out)
	if took := time.Since(began); took > plugincontract.DiagnoseTime {
		s.t.Errorf("diagnose took %s, and study gives a plugin %s for it", took, plugincontract.DiagnoseTime)
	}
	if err := out.Validate(); err != nil {
		s.t.Fatalf("what diagnose gave back breaks the contract: %v", err)
	}
	found := map[string]plugincontract.Finding{}
	for _, f := range out.Findings {
		found[f.Name] = f
		switch {
		case strings.Contains(f.Message, testKey), strings.Contains(strings.ToLower(f.Message), "bearer sk-"):
			s.t.Errorf("the finding %s holds the key: %s", f.Name, f.Message)
		case strings.Contains(f.Message, "/v1"), strings.Contains(f.Message, "http://"):
			s.t.Errorf("the finding %s says more of the endpoint's address than its host: %s", f.Name, f.Message)
		case len(f.Message) > plugincontract.MaxFindingBytes:
			s.t.Errorf("the finding %s is %d bytes, more than the %d study shows: %s", f.Name, len(f.Message), plugincontract.MaxFindingBytes, f.Message)
		}
	}
	return found
}

// names are the names of some findings, in order.
func names(found map[string]plugincontract.Finding) []string {
	var out []string
	for name := range found {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// With no endpoint in its settings Shelf depends on nothing it could test:
// it has the tool, says that there is nothing to say, and asks nobody.
func TestShelfWithNoEndpointHasNothingToTest(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	_, s := start(t, t.TempDir())
	if found := s.diagnose(); len(found) != 0 {
		t.Errorf("a Shelf without endpoints found %+v", found)
	}
	// A list, and not nothing: the contract has an answer hold its list.
	var raw map[string]any
	s.ok(plugincontract.ToolDiagnose, struct{}{}, &raw)
	if list, ok := raw["findings"].([]any); !ok || len(list) != 0 {
		t.Errorf("diagnose of a Shelf without endpoints gave %v; want an empty list of findings", raw)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Errorf("an endpoint that is in no settings was asked %+v", asked)
	}
	// Whatever it is handed is passed over: the tool takes no arguments.
	s.ok(plugincontract.ToolDiagnose, map[string]any{"collection": "linear-algebra", "added_later": true}, &raw)
}

// Shelf tests each endpoint its settings name as a search asks it: one
// question embedded, with the Recipe's marking and the key, and one question
// and two texts ordered. It sends text of its own, and nothing of what it
// has indexed.
func TestShelfTestsItsEndpointsAsASearchAsksThem(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at)})
	book := file(t, "book.a1", leaves("The first page speaks of cobordism.", "The second page speaks of spectral sequences."))
	s.indexed("tested", book)

	// What a search asks the two endpoints, to hold the test to.
	at.Forget()
	s.find("tested", "cobordism spectral", 5)
	searched := at.Asked()
	if len(searched) != 2 {
		t.Fatalf("a search asked the endpoints %+v; want the question's vector, and then an order", searched)
	}

	at.Forget()
	found := s.diagnose()
	if got := names(found); !slices.Equal(got, []string{"embedding", "reranking"}) {
		t.Fatalf("Shelf's findings are %v, want one for each endpoint", got)
	}
	if f := found["embedding"]; !f.OK ||
		f.Message != "the embedding endpoint "+at.Host+" gave a question's vector, of 512 numbers, with the model test-embedder" {
		t.Errorf("of the embedding endpoint Shelf says %+v", f)
	}
	if f := found["reranking"]; !f.OK || f.Message != "the reranking endpoint "+at.Host+" ordered two texts for a question, with the model test-reranker" {
		t.Errorf("of the reranking endpoint Shelf says %+v", f)
	}

	embedded, ranked := at.AskedAt("/v1/embeddings"), at.AskedAt("/v1/rerank")
	if len(at.Asked()) != 2 || len(embedded) != 1 || len(ranked) != 1 {
		t.Fatalf("to test them, the endpoints were asked %+v; want each of them once", at.Asked())
	}
	// The same request a search sends, but for its text: the fields, the
	// key and what Shelf calls itself.
	for i, tested := range []shelftest.Asked{embedded[0], ranked[0]} {
		as := searched[i]
		if tested.Path != as.Path || tested.Key != testKey || tested.Key != as.Key || tested.Agent != as.Agent ||
			!slices.Equal(tested.Keys(), as.Keys()) || tested.Body["model"] != as.Body["model"] {
			t.Errorf("the test asked %s with the key %q and the fields %v of the model %v;\na search asks %s with the key %q and the "+
				"fields %v of the model %v", tested.Path, tested.Key, tested.Keys(), tested.Body["model"], as.Path, as.Key, as.Keys(), as.Body["model"])
		}
	}
	if got := embedded[0].Texts(); !slices.Equal(got, []string{"task: search result | query: " + testQuestion}) {
		t.Errorf("the embedding endpoint was sent %q; want Shelf's own question, marked as a search marks one", got)
	}
	if got := ranked[0]; got.Body["query"] != testQuestion || !slices.Equal(got.Texts(), []string{testTextOne, testTextTwo}) ||
		got.Body["top_n"] != float64(2) {
		t.Errorf("the reranking endpoint was sent %v; want Shelf's own question and its two texts", got.Body)
	}
	// Nothing of a Source, or of a collection.
	for _, asked := range at.Asked() {
		if said := fmt.Sprint(asked.Body); strings.Contains(said, "cobordism") || strings.Contains(said, "spectral") ||
			strings.Contains(said, "tested") || strings.Contains(said, book.ID) {
			t.Errorf("the test sent %s something of what is indexed: %s", asked.Path, said)
		}
	}
	// The same every time.
	at.Forget()
	if again := s.diagnose(); fmt.Sprint(again) != fmt.Sprint(found) {
		t.Errorf("asked again, Shelf says %+v, and said %+v", again, found)
	}
	if now := at.Asked(); len(now) != 2 || fmt.Sprint(now[0].Body)+fmt.Sprint(now[1].Body) != fmt.Sprint(embedded[0].Body)+fmt.Sprint(ranked[0].Body) &&
		fmt.Sprint(now[1].Body)+fmt.Sprint(now[0].Body) != fmt.Sprint(embedded[0].Body)+fmt.Sprint(ranked[0].Body) {
		t.Errorf("asked again, the endpoints were sent %+v", now)
	}
}

// A server that marks the texts itself is told by name, and one that cuts
// the vectors is asked for the size: the test carries the Recipe's request,
// as a search does, and such a server refuses a request without it.
func TestTheTestCarriesTheRecipesRequest(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Prompts("query", "task: search result | query: ", "document", "title: none | text: ")
	strict := func(e *endpoint.Embedding) {
		e.Query, e.Document = endpoint.Marking{Fields: map[string]any{"prompt": "query"}}, endpoint.Marking{Fields: map[string]any{"prompt": "document"}}
		e.SendDimensions = true
	}
	_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at, strict)})
	found := s.diagnose()
	if f := found["embedding"]; len(found) != 1 || !f.OK || !strings.Contains(f.Message, "of 512 numbers") {
		t.Fatalf("Shelf's findings are %+v; want the embedding endpoint, all right", found)
	}
	asked := at.Asked()
	if len(asked) != 1 || asked[0].Body["prompt"] != "query" || asked[0].Body["dimensions"] != float64(512) ||
		!slices.Equal(asked[0].Texts(), []string{testQuestion}) {
		t.Errorf("the endpoint was asked %+v; want the question as it is, the prompt named, and 512 numbers asked for", asked)
	}
}

// An endpoint that does not answer as a search needs it to is a finding
// that is not all right, in a sentence that names the endpoint by its host,
// says what is wrong and what Shelf does without it, and holds neither the
// key nor the rest of the address, whatever the endpoint said. The other
// endpoint's finding is its own.
func TestShelfSaysWhatIsWrongWithAnEndpoint(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	only := func(path string, answer func(w http.ResponseWriter, r *http.Request, body map[string]any)) func(http.ResponseWriter, *http.Request, map[string]any) bool {
		return func(w http.ResponseWriter, r *http.Request, body map[string]any) bool {
			if r.URL.Path != path {
				return false
			}
			answer(w, r, body)
			return true
		}
	}
	vector := func(w http.ResponseWriter, numbers ...float32) {
		_ = jsonTo(w, map[string]any{"data": []map[string]any{{"index": 0, "embedding": numbers}}})
	}
	hangs := func(_ http.ResponseWriter, r *http.Request, _ map[string]any) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	const (
		noVectors = "Without it no Source is indexed, and a search is by keyword alone."
		noOrder   = "Without it a search gives its Passages in the order it found them."
	)
	for _, tc := range []struct {
		name, finding, path string
		answer              func(w http.ResponseWriter, r *http.Request, body map[string]any)
		want                []string
	}{
		{"the embedding endpoint is too slow", "embedding", "/v1/embeddings", hangs, []string{"did not answer within 300ms"}},
		{"it gives vectors of another length", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			vector(w, make([]float32, 384)...)
		}, []string{"gave vectors of 384 numbers, and Shelf's settings ask for 512"}},
		{"it gives a vector that is all zeros", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			vector(w, make([]float32, 768)...)
		}, []string{"gave a vector that is all zeros, or holds what is no number"}},
		{"it gives no vector", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			_ = jsonTo(w, map[string]any{"data": []any{}})
		}, []string{"gave 0 vectors for 1 texts"}},
		{"it does not know the model", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error": {"message": "model '%v' not found"}}`, body["model"])
		}, []string{"answered 404 Not Found: model 'test-embedder' not found", "Check the endpoint's address and the model's name in Shelf's settings file"}},
		// All that Shelf says of it fits what study shows of a finding: the
		// endpoint's words, what to check, and what Shelf does meanwhile.
		{"it does not know the model, and says so at length", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error": {"message": "%s"}}`, strings.Repeat("no such model here, ", 40))
		}, []string{"answered 404 Not Found: no such model here, no such model here,",
			"…. Check the endpoint's address and the model's name in Shelf's settings file. Without it no Source is indexed"}},
		{"it refuses the key, and repeats it", "embedding", "/v1/embeddings", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusUnauthorized)
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			// And a key of its own, which Shelf never knew: what follows
			// "Bearer" is a key, whoever's it is.
			fmt.Fprintf(w, `{"error": {"message": "no such key %s, sent as %s, at http://%s%s, passed on as Bearer sk-upstream-41c9e0"}}`,
				key, r.Header.Get("Authorization"), r.Host, r.URL.Path)
		}, []string{"answered 401 Unauthorized: no such key [the key], sent as Bearer [the key], at [the address], passed on as Bearer [the key]",
			"Check the key in Shelf's settings file"}},
		{"it answers what is no JSON", "embedding", "/v1/embeddings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			fmt.Fprint(w, "It works!")
		}, []string{"did not answer as its API does"}},
		{"it says a great deal", "embedding", "/v1/embeddings", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error": {"message": "%s"}}`, strings.Repeat("the model is loading for "+r.Header.Get("Authorization")+", ", 400))
		}, []string{"answered 503 Service Unavailable: the model is loading for Bearer [the key]", "…"}},
		{"the reranking endpoint is too slow", "reranking", "/v1/rerank", hangs, []string{"did not answer within 300ms"}},
		{"it scores nothing", "reranking", "/v1/rerank", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			_ = jsonTo(w, map[string]any{"results": []map[string]any{{"index": 0}, {"index": 1}}})
		}, []string{"did not answer as its API does: a result without its index or its relevance_score"}},
		{"it orders one text of the two", "reranking", "/v1/rerank", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			_ = jsonTo(w, map[string]any{"results": []map[string]any{{"index": 0, "relevance_score": 0.5}}})
		}, []string{"ranked 1 Passages of the 2 it was handed"}},
		{"it has no such route", "reranking", "/v1/rerank", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			http.NotFound(w, nil)
		}, []string{"answered 404 Not Found", "Check the endpoint's address and the model's name in Shelf's settings file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := shelftest.NewEndpoint(t)
			_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at), TestTime: 300 * time.Millisecond})
			at.Answer(only(tc.path, tc.answer))
			found := s.diagnose()
			if len(found) != 2 {
				t.Fatalf("Shelf's findings are %+v, want one for each endpoint", found)
			}
			wrong := found[tc.finding]
			what, without, other := "the embedding endpoint "+at.Host, noVectors, "reranking"
			if tc.finding == "reranking" {
				what, without, other = "the reranking endpoint "+at.Host, noOrder, "embedding"
			}
			if wrong.OK || !strings.HasPrefix(wrong.Message, what+" ") || !strings.HasSuffix(wrong.Message, ". "+without) {
				t.Errorf("of an endpoint of which it is true that %s, Shelf says %+v;\nwant it not all right, named by its host, with what "+
					"Shelf does without it", tc.name, wrong)
			}
			for _, want := range tc.want {
				if !strings.Contains(wrong.Message, want) {
					t.Errorf("the finding does not say %q: %s", want, wrong.Message)
				}
			}
			if strings.Contains(wrong.Message, "key] key]") {
				t.Errorf("the key was taken out of the finding twice: %s", wrong.Message)
			}
			// The other endpoint answers, and its finding says so.
			if f := found[other]; !f.OK {
				t.Errorf("the endpoint that answers is found as %+v", f)
			}
			// It was asked with the key, once.
			if asked := at.AskedAt(tc.path); len(asked) != 1 || asked[0].Key != testKey {
				t.Errorf("the endpoint was asked %+v", asked)
			}
			// Once it answers, the finding is all right again.
			at.Answer(nil)
			if again := s.diagnose(); !again[tc.finding].OK {
				t.Errorf("once the endpoint answers, Shelf says %+v", again[tc.finding])
			}
		})
	}
}

// An endpoint that is not there at all: both findings say so, each of its
// own endpoint, and Shelf goes on answering.
func TestShelfSaysThatAnEndpointIsDown(t *testing.T) {
	at, ranker := shelftest.NewEndpoint(t), shelftest.NewEndpoint(t)
	_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, ranker)})
	at.Stop()
	found := s.diagnose()
	if f := found["embedding"]; f.OK || !strings.HasPrefix(f.Message, "the embedding endpoint "+at.Host+" could not be reached: ") ||
		!strings.HasSuffix(f.Message, ". Without it no Source is indexed, and a search is by keyword alone.") {
		t.Errorf("with the embedding endpoint down, Shelf says %+v", f)
	}
	if f := found["reranking"]; !f.OK || !strings.Contains(f.Message, ranker.Host) {
		t.Errorf("of the reranking endpoint, which is up, Shelf says %+v", f)
	}
	ranker.Stop()
	if f := s.diagnose()["reranking"]; f.OK || !strings.HasPrefix(f.Message, "the reranking endpoint "+ranker.Host+" could not be reached: ") {
		t.Errorf("with the reranking endpoint down, Shelf says %+v", f)
	}
	// It still says which contract it speaks, and what is indexed.
	var contract plugincontract.Contract
	s.ok(plugincontract.ToolContract, struct{}{}, &contract)
	if len(s.status("down")) != 0 || contract.Version != 1 {
		t.Errorf("after its endpoints went down, Shelf says %+v of itself", contract)
	}
}

// The endpoints are tested side by side, each under a time shorter than the
// one study gives Shelf for the whole answer: so Shelf answers in time
// whatever an endpoint does, and one that never answers costs its own
// finding and no more.
func TestShelfTestsItsEndpointsSideBySideAndAnswersInTime(t *testing.T) {
	if endpoint.DefaultTestTimeout >= plugincontract.DiagnoseTime {
		t.Fatalf("an endpoint has %s to answer Shelf's test, and Shelf %s to answer study: Shelf could not say in time that an endpoint "+
			"did not answer", endpoint.DefaultTestTimeout, plugincontract.DiagnoseTime)
	}
	// Each request is held until the other has arrived: asked one after the
	// other, the first would never be answered.
	at := shelftest.NewEndpoint(t)
	var mu sync.Mutex
	arrived, both := map[string]bool{}, make(chan struct{})
	at.Answer(func(_ http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		mu.Lock()
		if arrived[r.URL.Path] = true; len(arrived) == 2 {
			select {
			case <-both:
			default:
				close(both)
			}
		}
		mu.Unlock()
		select {
		case <-both:
		case <-r.Context().Done():
		}
		return false
	})
	_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at), TestTime: 5 * time.Second})
	if found := s.diagnose(); !found["embedding"].OK || !found["reranking"].OK {
		t.Errorf("with each endpoint waiting for the other to be asked, Shelf found %+v: it asks them one after the other", found)
	}

	// Neither ever answers: Shelf says so of both, after the time one of
	// them has, and not after the time of two.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	at.Answer(func(_ http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	})
	_, hurried := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at), TestTime: 400 * time.Millisecond})
	began := time.Now()
	found := hurried.diagnose()
	took := time.Since(began)
	for _, name := range []string{"embedding", "reranking"} {
		if f := found[name]; f.OK || !strings.Contains(f.Message, at.Host+" did not answer within 400ms") {
			t.Errorf("of an endpoint that never answers, Shelf says %+v", f)
		}
	}
	if took < 400*time.Millisecond || took > plugincontract.DiagnoseTime/2 {
		t.Errorf("Shelf took %s to say that neither endpoint answers within 400ms", took)
	}
}
