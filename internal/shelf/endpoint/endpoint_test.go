package endpoint_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

var ctx = context.Background()

// key is a key no reason may hold.
const key = "sk-shelf-test-4f9a1c77d2"

// prefixed is the settings of an endpoint that embeds a text as it is sent,
// as llama.cpp's does: Shelf writes the model's marks around each text.
func prefixed(at *shelftest.Endpoint) endpoint.Embedding {
	return endpoint.Embedding{
		Address: at.URL, Key: key, Model: "embedding-model", Dimensions: 512,
		Query:    endpoint.Marking{Text: "task: search result | query: {text}"},
		Document: endpoint.Marking{Text: "title: {title} | text: {text}"},
		Agent:    "study-shelf/test",
	}
}

func embedder(t *testing.T, cfg endpoint.Embedding) *endpoint.Embedder {
	t.Helper()
	e, err := endpoint.NewEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// reason checks that err is a reason for the learner that holds each of the
// words given, names the endpoint by its host and holds neither the key nor
// the path Shelf posts to, and returns it.
func reason(t *testing.T, err error, host string, words ...string) string {
	t.Helper()
	var p *endpoint.Problem
	if !errors.As(err, &p) {
		t.Fatalf("the endpoint gave %v, want a reason for the learner", err)
	}
	for _, word := range append(words, host) {
		if !strings.Contains(p.Reason, word) {
			t.Errorf("the reason does not say %q: %s", word, p.Reason)
		}
	}
	for _, not := range []string{key, "/v1/embeddings", "/v1/rerank"} {
		if strings.Contains(p.Reason, not) {
			t.Errorf("the reason holds %q: %s", not, p.Reason)
		}
	}
	return p.Reason
}

func length(vector []float32) float64 {
	var sum float64
	for _, x := range vector {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// cut is the first n numbers of a vector, made of length 1 again.
func cut(vector []float32, n int) []float32 {
	out := slices.Clone(vector[:n])
	norm := length(out)
	for i := range out {
		out[i] = float32(float64(out[i]) / norm)
	}
	return out
}

func near(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-5 {
			return false
		}
	}
	return true
}

// Passages are sent a batch at a time, each marked as a stretch of the
// document with its title, in a request that holds the model and the texts
// and nothing else. What comes back is cut to the size the settings name
// and made of length 1 again.
func TestDocumentsAreAskedForInBatches(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	e := embedder(t, prefixed(at))
	var texts []string
	for n := range 40 {
		texts = append(texts, fmt.Sprintf("Passage %d speaks\nof   topic%d.", n, n))
	}
	var said []int
	vectors, err := e.Documents(ctx, "  Linear\tAlgebra ", texts, func(n int) { said = append(said, n) })
	if err != nil {
		t.Fatal(err)
	}
	asked := at.Asked()
	if len(asked) != 3 {
		t.Fatalf("the endpoint was asked %d times for 40 texts in batches of %d, want 3", len(asked), endpoint.DefaultBatch)
	}
	if fmt.Sprint(said) != "[16 32 40]" {
		t.Errorf("it said how far it was at %v, want after each batch", said)
	}
	place := 0
	for n, a := range asked {
		if a.Path != "/v1/embeddings" || a.Key != key || a.Agent != "study-shelf/test" {
			t.Errorf("request %d went to %s with the key %q as %q", n, a.Path, a.Key, a.Agent)
		}
		if got := fmt.Sprint(a.Keys()); got != "[input model]" {
			t.Errorf("request %d holds the keys %s: Shelf sends the model, the texts and what the settings name", n, got)
		}
		if a.Body["model"] != "embedding-model" {
			t.Errorf("request %d names the model %v", n, a.Body["model"])
		}
		for _, text := range a.Texts() {
			want := fmt.Sprintf("title: Linear Algebra | text: Passage %d speaks of topic%d.", place, place)
			if text != want {
				t.Errorf("text %d was sent as %q, want %q", place, text, want)
			}
			if got, want := vectors[place], cut(at.Vector(text), 512); !near(got, want) {
				t.Errorf("the vector of text %d is not the endpoint's, cut to 512 numbers and made of length 1", place)
			}
			place++
		}
	}
	if place != 40 || len(vectors) != 40 {
		t.Fatalf("%d texts were sent and %d vectors came back, want 40 of each", place, len(vectors))
	}
	if e.Model() != "embedding-model" || e.Dimensions() != 512 || e.Document() != "title: {title} | text: {text}" {
		t.Errorf("the embedder says its vectors are made with %q, %d, %q", e.Model(), e.Dimensions(), e.Document())
	}
}

func TestAQuestionIsMarkedAsOne(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	e := embedder(t, prefixed(at))
	vector, err := e.Query(ctx, "why does elimination\n give a triangle?")
	if err != nil {
		t.Fatal(err)
	}
	asked := at.Asked()
	if len(asked) != 1 || fmt.Sprint(asked[0].Texts()) != "[task: search result | query: why does elimination give a triangle?]" {
		t.Fatalf("the endpoint was asked %+v", asked)
	}
	if got := fmt.Sprint(asked[0].Keys()); got != "[input model]" {
		t.Errorf("the request holds the keys %s", got)
	}
	if len(vector) != 512 || math.Abs(length(vector)-1) > 1e-5 {
		t.Errorf("the vector has %d numbers and the length %v, want 512 and 1", len(vector), length(vector))
	}
}

// A text with nothing written around it, and vectors of the size the model
// gives, which the settings name: a model that wants no marks.
func TestATextIsSentAsItIsWhenNothingMarksIt(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Size(384)
	e := embedder(t, endpoint.Embedding{Address: at.URL, Model: "plain-model", Dimensions: 384})
	vectors, err := e.Documents(ctx, "A title", []string{"one  text", "another"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(at.Asked()[0].Texts()); got != "[one text another]" {
		t.Errorf("the texts were sent as %s", got)
	}
	if at.Asked()[0].Key != "" {
		t.Errorf("a key was sent, and the settings have none")
	}
	if len(vectors[0]) != 384 || len(vectors[1]) != 384 || e.Dimensions() != 384 || e.Document() != "{text}" {
		t.Errorf("vectors of %d numbers, and the embedder says %d and %q", len(vectors[0]), e.Dimensions(), e.Document())
	}
}

// A server that marks the texts itself, by the name of one of the model's
// prompts: the texts go as they are, the name goes in a field the settings
// give, and the size is asked for because the settings say to ask. Such a
// server refuses a key it does not know, so nothing else is sent.
func TestAServerThatMarksTheTextsItselfIsToldInAField(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Prompts("query", "task: search result | query: ", "document", "title: none | text: ")
	cfg := endpoint.Embedding{
		Address: at.URL, Model: "google/embeddinggemma-2", Dimensions: 512, SendDimensions: true,
		Query:    endpoint.Marking{Fields: map[string]any{"prompt": "query"}},
		Document: endpoint.Marking{Fields: map[string]any{"prompt": "document"}},
	}
	e := embedder(t, cfg)
	documents, err := e.Documents(ctx, "A title that is not sent", []string{"Elimination gives a triangle."}, nil)
	if err != nil {
		t.Fatal(err)
	}
	question, err := e.Query(ctx, "what gives a triangle?")
	if err != nil {
		t.Fatal(err)
	}
	asked := at.Asked()
	if len(asked) != 2 {
		t.Fatalf("the endpoint was asked %d times", len(asked))
	}
	for n, want := range []struct{ prompt, text string }{{"document", "Elimination gives a triangle."}, {"query", "what gives a triangle?"}} {
		a := asked[n]
		if got := fmt.Sprint(a.Keys()); got != "[dimensions input model prompt]" {
			t.Errorf("request %d holds the keys %s", n, got)
		}
		if a.Body["prompt"] != want.prompt || a.Body["dimensions"] != float64(512) || fmt.Sprint(a.Texts()) != "["+want.text+"]" {
			t.Errorf("request %d = %v; want the prompt %s, 512 numbers and the text as it is", n, a.Body, want.prompt)
		}
	}
	// What the server made of them is what Shelf's own marks would have.
	if want := cut(at.Vector("title: none | text: Elimination gives a triangle."), 512); !near(documents[0], want) {
		t.Errorf("the document's vector is not the one of the text the server marked")
	}
	if want := cut(at.Vector("task: search result | query: what gives a triangle?"), 512); !near(question, want) {
		t.Errorf("the question's vector is not the one of the text the server marked")
	}
	if e.Document() != `request {"prompt":"document"}` {
		t.Errorf("the embedder says a document is marked by %q", e.Document())
	}

	// A field the server does not know fails the request, with its words.
	cfg.Document.Fields = map[string]any{"prompt": "document", "input_type": "passage"}
	_, err = embedder(t, cfg).Documents(ctx, "T", []string{"a text"}, nil)
	reason(t, err, at.Host, "answered 400", "unknown request keys ['input_type']")
}

func TestWhatTheSettingsCannotSayIsRefused(t *testing.T) {
	good := endpoint.Embedding{Address: "http://127.0.0.1:8080/v1", Model: "m", Dimensions: 768}
	if err := good.Check(); err != nil {
		t.Fatalf("a plain endpoint is refused: %v", err)
	}
	for name, tc := range map[string]struct {
		change func(*endpoint.Embedding)
		want   string
	}{
		"no address":                 {func(e *endpoint.Embedding) { e.Address = "" }, "endpoint is missing"},
		"a file":                     {func(e *endpoint.Embedding) { e.Address = "file:///etc/passwd" }, "not an http or https address"},
		"no host":                    {func(e *endpoint.Embedding) { e.Address = "http:///v1" }, "names no host"},
		"a password in it":           {func(e *endpoint.Embedding) { e.Address = "http://ada:secret@127.0.0.1/v1" }, "a key goes in key"},
		"a query in it":              {func(e *endpoint.Embedding) { e.Address = "http://127.0.0.1/v1?api-key=secret" }, "a query or a fragment"},
		"no model":                   {func(e *endpoint.Embedding) { e.Model = "" }, "model is missing"},
		"a model of two lines":       {func(e *endpoint.Embedding) { e.Model = "a\nb" }, "model is not a name"},
		"a key of two lines":         {func(e *endpoint.Embedding) { e.Key = "a\nb" }, "key is not one word"},
		"a key of two words":         {func(e *endpoint.Embedding) { e.Key = "sk-test key" }, "key is not one word"},
		"a key with a space after":   {func(e *endpoint.Embedding) { e.Key = "sk-test " }, "key is not one word"},
		"a size below none":          {func(e *endpoint.Embedding) { e.Dimensions = -1 }, "dimensions is not a number"},
		"no size":                    {func(e *endpoint.Embedding) { e.Dimensions = 0 }, "dimensions is missing"},
		"a size beyond any":          {func(e *endpoint.Embedding) { e.Dimensions = endpoint.MaxDimensions + 1 }, "dimensions is not a number"},
		"too large a batch":          {func(e *endpoint.Embedding) { e.Batch = 1000 }, "batch is not a number"},
		"a mark without the text":    {func(e *endpoint.Embedding) { e.Query.Text = "query: " }, "holds {text} 0 times"},
		"a mark with the text twice": {func(e *endpoint.Embedding) { e.Document.Text = "{text} {text}" }, "holds {text} 2 times"},
		"a title for a question":     {func(e *endpoint.Embedding) { e.Query.Text = "{title}: {text}" }, "a question has no title"},
		"a field that is Shelf's":    {func(e *endpoint.Embedding) { e.Query.Fields = map[string]any{"input": "x"} }, "which is Shelf's to send"},
		"a field that is a list":     {func(e *endpoint.Embedding) { e.Query.Fields = map[string]any{"prompt": []any{"x"}} }, "neither text, a number"},
		"both ways of marking": {func(e *endpoint.Embedding) {
			e.Query.Text = "query: {text}"
			e.Document.Fields = map[string]any{"prompt": "document"}
		}, "marked twice"},
	} {
		cfg := good
		tc.change(&cfg)
		err := cfg.Check()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Check() = %v, want it refused with %q", name, err, tc.want)
		}
		if _, err := endpoint.NewEmbedder(cfg); err == nil {
			t.Errorf("%s: NewEmbedder made an embedder of it", name)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the refusal repeats what may be a key: %v", name, err)
		}
	}
}

// Where the requests go: below the address the settings give, and below
// /v1 when it has no path.
func TestRequestsGoBelowTheAddress(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	defer server.Close()
	for _, base := range []string{"", "/", "/v1", "/v1/", "/api/openai/v2"} {
		e := embedder(t, endpoint.Embedding{Address: server.URL + base, Model: "m", Dimensions: 8})
		_, _ = e.Query(ctx, "x")
		r, err := endpoint.NewReranker(endpoint.Reranking{Address: server.URL + base, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.Order(ctx, "x", []string{"a", "b"})
		r.Close()
	}
	want := "[/v1/embeddings /v1/rerank /v1/embeddings /v1/rerank /v1/embeddings /v1/rerank /v1/embeddings /v1/rerank " +
		"/api/openai/v2/embeddings /api/openai/v2/rerank]"
	if got := fmt.Sprint(paths); got != want {
		t.Errorf("the requests went to %s, want %s", got, want)
	}
}

// vectors answers an embeddings request with these vectors, numbered in
// their order.
func vectors(w http.ResponseWriter, list ...[]float32) {
	data := make([]map[string]any, len(list))
	for i, v := range list {
		data[i] = map[string]any{"index": i, "embedding": v}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func ones(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = 1
	}
	return v
}

// Whatever an endpoint gets wrong is a reason for the learner, which names
// the endpoint by its host and never holds the key, and Shelf goes on.
func TestWhatAnEndpointGetsWrongIsAReason(t *testing.T) {
	elsewhere := shelftest.NewEndpoint(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter, r *http.Request, body map[string]any) bool
		change func(*endpoint.Embedding)
		want   []string
	}{
		{name: "it is slow", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		}, change: func(e *endpoint.Embedding) { e.Timeout = 100 * time.Millisecond }, want: []string{"did not answer within 100ms"}},
		{name: "too few vectors", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(600))
			return true
		}, want: []string{"gave 1 vectors for 3 texts"}},
		{name: "too many vectors", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(600), ones(600), ones(600), ones(600))
			return true
		}, want: []string{"did not answer as its API does", "more than the 3 results that were asked for"}},
		{name: "a great many results of nothing", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			// Few bytes each, and each a value to hold once it is read.
			fmt.Fprint(w, `{"data": [`+strings.Repeat("{},", 500000)+`{}]}`)
			return true
		}, want: []string{"more than the 3 results that were asked for"}},
		{name: "a vector longer than a vector may be", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(endpoint.MaxDimensions+1), ones(endpoint.MaxDimensions+1), ones(endpoint.MaxDimensions+1))
			return true
		}, want: []string{"a vector has more than 16384 numbers"}},
		{name: "a number no vector can hold", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			fmt.Fprint(w, `{"data": [{"index": 0, "embedding": [1e`+strings.Repeat("9", 5000)+`]}]}`)
			return true
		}, want: []string{"did not answer as its API does", "what is no number a vector can hold"}},
		{name: "a field of another kind", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			fmt.Fprint(w, `{"data": [{"index": "first", "embedding": [1]}]}`)
			return true
		}, want: []string{"did not answer as its API does", "index is a string"}},
		{name: "vectors that are too short", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(384), ones(384), ones(384))
			return true
		}, want: []string{"gave vectors of 384 numbers", "ask for 512", "set dimensions"}},
		{name: "vectors of different sizes, one of them too short", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(768), ones(384), ones(768))
			return true
		}, want: []string{"gave vectors of 384 numbers", "ask for 512"}},
		{name: "a vector of zeros", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(512), make([]float32, 512), ones(512))
			return true
		}, want: []string{"all zeros"}},
		{name: "a vector of no numbers", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(512), nil, ones(512))
			return true
		}, want: []string{"a vector of no numbers"}},
		{name: "two vectors for one text", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			one, _ := json.Marshal(ones(512))
			fmt.Fprintf(w, `{"data": [{"index": 0, "embedding": %s}, {"index": 0, "embedding": %s}, {"index": 2, "embedding": %s}]}`, one, one, one)
			return true
		}, want: []string{"not one for each text"}},
		{name: "vectors as text", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			fmt.Fprint(w, `{"data": [{"index": 0, "embedding": "AACAPwAAgD8="}]}`)
			return true
		}, want: []string{"did not answer as its API does"}},
		{name: "what is no JSON", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			fmt.Fprint(w, "<html><body>It works!</body></html>")
			return true
		}, want: []string{"did not answer as its API does", "not JSON"}},
		{name: "an error of the server", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusInternalServerError)
			// A server that repeats what it was sent, the key among it.
			fmt.Fprintf(w, `{"error": {"message": "input is too large to process for %s\u001b[31m", "type": "server_error"}}`, r.Header.Get("Authorization"))
			return true
		}, want: []string{"answered 500 Internal Server Error", "input is too large to process for Bearer [the key]"}},
		{name: "an error that repeats the key with what is not shown in it", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusForbidden)
			// The key with a zero-width space, an escape and a NUL in it:
			// each is taken out of what is shown, which leaves the key.
			fmt.Fprintf(w, `{"error": {"message": "no access for the key %s\u200b%s\u001b%s\u0000%s"}}`, key[:3], key[3:9], key[9:14], key[14:])
			return true
		}, want: []string{"answered 403 Forbidden", "no access for the key [the key]"}},
		{name: "an error that repeats another key than Shelf's", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusForbidden)
			// As a gateway does that sends its own key on.
			fmt.Fprint(w, `{"error": {"message": "upstream refused Authorization: Bearer sk-gateway-0123456789abcdef, try later"}}`)
			return true
		}, want: []string{"upstream refused Authorization: Bearer [the key], try later"}},
		{name: "an error that names the address", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusMethodNotAllowed)
			fmt.Fprintf(w, "Cannot POST %s on http://%s%s", r.URL.Path, r.Host, r.URL.Path)
			return true
		}, want: []string{"answered 405 Method Not Allowed", "Cannot POST [the address] on [the address]"}},
		{name: "a key it does not take", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error": "invalid api key"}`)
			return true
		}, want: []string{"answered 401 Unauthorized", "invalid api key", "Check the key in Shelf's settings file"}},
		{name: "a model it does not have", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			http.Error(w, "model not found", http.StatusNotFound)
			return true
		}, want: []string{"answered 404 Not Found", "model not found", "the model's name"}},
		{name: "a redirect", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			http.Redirect(w, r, elsewhere.URL+"/embeddings", http.StatusTemporaryRedirect)
			return true
		}, want: []string{"answered 307 Temporary Redirect", "Shelf follows no redirect"}},
		{name: "more than Shelf reads", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			vectors(w, ones(40000), ones(40000), ones(40000))
			return true
		}, change: func(e *endpoint.Embedding) { e.MaxBytes = 128 << 10 }, want: []string{"sent more than 128 KB in one answer"}},
		{name: "more than the vectors that were asked for can be", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			// Three vectors of 512 numbers are some tens of kilobytes: an
			// answer of megabytes is not read to its end.
			fmt.Fprint(w, `{"data": [], "padding": "`+strings.Repeat("x", 4<<20)+`"}`)
			return true
		}, change: func(e *endpoint.Embedding) { e.SendDimensions = true }, want: []string{"sent more than 112 KB in one answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := shelftest.NewEndpoint(t)
			at.Answer(tc.answer)
			// The time an endpoint has is the one Shelf gives it by itself,
			// but where the endpoint is to be too slow: an answer that only
			// took its time on a busy computer is no failure.
			cfg := prefixed(at)
			if tc.change != nil {
				tc.change(&cfg)
			}
			e := embedder(t, cfg)
			_, err := e.Documents(ctx, "A book", []string{"one", "two", "three"}, nil)
			said := reason(t, err, at.Host, tc.want...)
			if strings.ContainsRune(said, 0x1b) {
				t.Errorf("the reason holds what a terminal would act on: %q", said)
			}
			// A reason is a sentence or two, whatever the endpoint sent, and
			// holds neither the key nor the rest of the address.
			if len(said) > 600 || strings.Contains(said, key) || strings.Contains(said, "sk-gateway") || strings.Contains(said, "/v1/embeddings") ||
				strings.Contains(said, "key] key]") {
				t.Errorf("the reason is %d bytes, or holds the key or the address: %.700q", len(said), said)
			}
			// It was asked once, with the key, and not again: Shelf does not
			// hammer an endpoint that is in trouble.
			if asked := at.Asked(); len(asked) != 1 || asked[0].Key != key || len(asked[0].Texts()) != 3 {
				t.Errorf("the endpoint was asked %+v; want the three texts, once", asked)
			}
			// It answers again, and so does Shelf: with all the time it gives
			// an endpoint, for this answer is to arrive.
			at.Answer(nil)
			if cfg.Timeout != 0 {
				cfg.Timeout = 0
				e = embedder(t, cfg)
			}
			if _, err := e.Documents(ctx, "A book", []string{"one", "two", "three"}, nil); err != nil {
				t.Errorf("once the endpoint answers again: %v", err)
			}
		})
	}
	if asked := elsewhere.Asked(); len(asked) != 0 {
		t.Errorf("Shelf followed a redirect, with %+v", asked)
	}
}

func TestAnEndpointThatIsDownIsAReason(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	e := embedder(t, prefixed(at))
	at.Stop()
	_, err := e.Query(ctx, "anything")
	reason(t, err, at.Host, "the embedding endpoint", "could not be reached")
}

// An endpoint that names the model it answered with as it was asked for it
// gave no other name, whatever the address holds: the two names are compared
// as the endpoint wrote them. Made safe to repeat first, a name that is a
// piece of the endpoint's address is "[the address]", and would be shown as
// a second model.
func TestTheModelAnEndpointWasAskedForIsNoOtherName(t *testing.T) {
	const model, ranker = "embedding-model-v2", "reranking-model-v2"
	var other atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer := map[string]any{"model": model, "data": []map[string]any{{"index": 0, "embedding": ones(8)}}}
		if strings.HasSuffix(r.URL.Path, "/rerank") {
			answer = map[string]any{"model": ranker, "results": []map[string]any{
				{"index": 0, "relevance_score": 0.9}, {"index": 1, "relevance_score": 0.1}}}
		}
		if other.Load() {
			answer["model"] = "served-as-" + answer["model"].(string)
		}
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer server.Close()
	// Each model's name is a piece of its endpoint's address.
	e := embedder(t, endpoint.Embedding{Address: server.URL + "/models/" + model + "/v1", Model: model, Dimensions: 8})
	r := reranker(t, endpoint.Reranking{Address: server.URL + "/models/" + ranker + "/v1", Model: ranker})
	if _, answered, err := e.Test(ctx, time.Minute, "x"); err != nil || answered != "" {
		t.Errorf("of an embedding endpoint that answered with the model it was asked for, Test gives the name %q, %v", answered, err)
	}
	if _, answered, err := r.Test(ctx, time.Minute, "x", []string{"a", "b"}); err != nil || answered != "" {
		t.Errorf("of a reranking endpoint that answered with the model it was asked for, Test gives the name %q, %v", answered, err)
	}
	// Another name is given, without the piece of the address in it.
	other.Store(true)
	if _, answered, err := e.Test(ctx, time.Minute, "x"); err != nil || answered != "served-as-[the address]" {
		t.Errorf("of an embedding endpoint that answered with another model, Test gives the name %q, %v", answered, err)
	}
	if _, answered, err := r.Test(ctx, time.Minute, "x", []string{"a", "b"}); err != nil || answered != "served-as-[the address]" {
		t.Errorf("of a reranking endpoint that answered with another model, Test gives the name %q, %v", answered, err)
	}
}

// A server closes a connection it kept open, when it is restarted or has
// kept it long enough, and Shelf may send its next request on it before it
// has seen that. A request changes nothing at the endpoint, so it is sent
// again, once, on a new connection: Shelf says nothing of an endpoint that
// answers, and of one that is gone it says that it is, not "EOF".
func TestARequestOnAConnectionTheServerClosedIsSentAgain(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked[r.RemoteAddr]++
		again := asked[r.RemoteAddr] > 1
		mu.Unlock()
		if _, marked := r.Header["Idempotency-Key"]; marked {
			t.Error("the mark that lets a request be sent again was sent to the endpoint")
		}
		if again {
			// The second request on a connection: it is closed without a
			// word, as one the server had given up is.
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		vectors(w, ones(8))
	}))
	defer server.Close()
	e := embedder(t, endpoint.Embedding{Address: server.URL, Model: "m", Dimensions: 8})
	for n := 1; n <= 3; n++ {
		if _, err := e.Query(ctx, "x"); err != nil {
			t.Fatalf("request %d, the server having closed the connection of the one before: %v", n, err)
		}
	}
	mu.Lock()
	connections := len(asked)
	mu.Unlock()
	if connections != 3 {
		t.Errorf("three requests went over %d connections, want each after the first sent again on a new one", connections)
	}
	// Nothing listens any more, and the connection Shelf kept is closed on
	// its next request: the endpoint is gone, and that is what is said.
	_ = server.Listener.Close()
	_, err := e.Query(ctx, "x")
	if said := reason(t, err, strings.TrimPrefix(server.URL, "http://"), "could not be reached", "connection refused"); strings.Contains(said, "EOF") {
		t.Errorf("of an endpoint that is gone, Shelf says what the closed connection gave: %s", said)
	}
}

// Shelf being told to stop is no failure of the endpoint: the caller's own
// error comes back, at once.
func TestBeingStoppedIsNoProblemOfTheEndpoint(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	var began atomic.Bool
	release := make(chan struct{})
	defer close(release)
	at.Answer(func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		began.Store(true)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	})
	e := embedder(t, prefixed(at))
	stopped, stop := context.WithCancel(ctx)
	go func() {
		for !began.Load() {
			time.Sleep(time.Millisecond)
		}
		stop()
	}()
	_, err := e.Documents(stopped, "A book", []string{"one"}, nil)
	var p *endpoint.Problem
	if !errors.Is(err, context.Canceled) || errors.As(err, &p) {
		t.Errorf("stopped, the embedder gave %v, want the caller's own error", err)
	}
}

func reranker(t *testing.T, cfg endpoint.Reranking) *endpoint.Reranker {
	t.Helper()
	r, err := endpoint.NewReranker(cfg)
	if err != nil {
		t.Fatalf("NewReranker: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

// The reranking endpoint is handed the question and the Passages, and its
// scores decide the order, whatever order it lists them in.
func TestTheRerankersScoresAreTheOrder(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Rank(func(_ string, documents []string) []float64 {
		scores := make([]float64, len(documents))
		for i, document := range documents {
			scores[i] = float64(len(document)) // the longest first
		}
		return scores
	})
	r := reranker(t, endpoint.Reranking{Address: at.URL, Key: key, Model: "reranking-model", Agent: "study-shelf/test"})
	if r.Candidates() != endpoint.DefaultCandidates {
		t.Errorf("it reranks %d Passages when the settings do not say, want %d", r.Candidates(), endpoint.DefaultCandidates)
	}
	order, err := r.Order(ctx, "which is\nthe longest?", []string{"bb", "a", "dddd", "c  c"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(order); got != "[{2 4} {3 3} {0 2} {1 1}]" {
		t.Errorf("the order is %s, want the longest first", got)
	}
	asked := at.Asked()
	if len(asked) != 1 || asked[0].Path != "/v1/rerank" || asked[0].Key != key {
		t.Fatalf("the endpoint was asked %+v", asked)
	}
	a := asked[0]
	if got := fmt.Sprint(a.Keys()); got != "[documents model query top_n]" {
		t.Errorf("the request holds the keys %s", got)
	}
	if a.Body["model"] != "reranking-model" || a.Body["query"] != "which is the longest?" || a.Body["top_n"] != float64(4) ||
		fmt.Sprint(a.Texts()) != "[bb a dddd c c]" {
		t.Errorf("the request is %v", a.Body)
	}
}

func TestWhatARerankerGetsWrongIsAReason(t *testing.T) {
	for name, tc := range map[string]struct {
		answer string
		status int
		want   []string
	}{
		"too few results":             {`{"results": [{"index": 0, "relevance_score": 1}]}`, 200, []string{"ranked 1 Passages of the 3 it was handed"}},
		"one Passage twice":           {`{"results": [{"index": 0, "relevance_score": 1}, {"index": 0, "relevance_score": 1}, {"index": 1, "relevance_score": 1}]}`, 200, []string{"one Passage twice"}},
		"a Passage it was not handed": {`{"results": [{"index": 0, "relevance_score": 1}, {"index": 7, "relevance_score": 1}, {"index": 1, "relevance_score": 1}]}`, 200, []string{"ranked what it was not handed"}},
		"a result with no score":      {`{"results": [{"index": 0}, {"index": 1, "relevance_score": 1}, {"index": 2, "relevance_score": 1}]}`, 200, []string{"without its index or its relevance_score"}},
		"another API's answer":        {`[{"index": 0, "score": 1}]`, 200, []string{"did not answer as its API does"}},
		"an error":                    {`{"error": {"message": "This server does not support reranking. Start it with --reranking"}}`, 501, []string{"answered 501 Not Implemented", "does not support reranking"}},
	} {
		t.Run(name, func(t *testing.T) {
			at := shelftest.NewEndpoint(t)
			at.Answer(func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.answer)
				return true
			})
			r := reranker(t, endpoint.Reranking{Address: at.URL, Key: key, Model: "reranking-model"})
			_, err := r.Order(ctx, "a question", []string{"a", "b", "c"})
			reason(t, err, at.Host, append(tc.want, "the reranking endpoint")...)
		})
	}
	for name, cfg := range map[string]endpoint.Reranking{
		"no model":            {Address: "http://127.0.0.1:8081/v1"},
		"no address":          {Model: "m"},
		"one candidate":       {Address: "http://127.0.0.1:8081/v1", Model: "m", Candidates: 1},
		"too many candidates": {Address: "http://127.0.0.1:8081/v1", Model: "m", Candidates: 1000},
	} {
		if _, err := endpoint.NewReranker(cfg); err == nil {
			t.Errorf("%s: NewReranker made a reranker of it", name)
		}
	}
}

// study gives a search forty-five seconds for all of it, starting the
// plugin included, and stops a plugin that takes longer without hearing
// what it found. The time Shelf waits for an endpoint that never answers,
// for the question's vector and then for the reranker, leaves it time to
// start, to search by keyword and to answer: so that the search is
// answered, by what is left, and says what it did without.
func TestTheSearchDeadlinesLeaveTimeToAnswer(t *testing.T) {
	// What starting Shelf, the keyword search, reading every vector of a
	// large collection and writing the answer may take on a slow computer.
	const rest = 10 * time.Second
	waited := endpoint.DefaultQueryTimeout + endpoint.DefaultRerankTimeout
	if waited+rest > plugincontract.EvidenceTime {
		t.Errorf("a search waits up to %s for its two endpoints, which with %s for the rest is more than the %s study gives a search: "+
			"study would stop Shelf before it could answer by keyword", waited, rest, plugincontract.EvidenceTime)
	}
	// And each is the time a request really has: an embedder and a reranker
	// made from settings that name none.
	at := shelftest.NewEndpoint(t)
	release := make(chan struct{})
	defer close(release)
	at.Answer(func(_ http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	})
	cfg := prefixed(at)
	cfg.QueryTimeout = 150 * time.Millisecond
	began := time.Now()
	_, err := embedder(t, cfg).Query(ctx, "a question")
	if took := time.Since(began); took < 150*time.Millisecond || took > 5*time.Second {
		t.Errorf("a question's vector was waited for %s, and its deadline is 150ms", took)
	}
	reason(t, err, at.Host, "did not answer within 150ms")
}

// An endpoint's address is the learner's, and may hold what is theirs
// alone in its path: an account's or a tenant's name. A server's own words
// are repeated without it, whichever part of the address they name: the
// address a request was posted to, the one the settings gave, either one's
// path, or the name by itself. The host stays: Shelf names an endpoint by
// it.
func TestAServersWordsAreRepeatedWithoutTheAddress(t *testing.T) {
	const tenant = "tok_5f3c9a1e77b2"
	for name, says := range map[string]func(r *http.Request) string{
		"the address a request was posted to": func(r *http.Request) string { return "no route http://" + r.Host + r.URL.Path },
		"the address the settings gave": func(r *http.Request) string {
			return "no model at http://" + r.Host + "/tenants/" + tenant + "/v1 for you"
		},
		"the path of a request":       func(r *http.Request) string { return "Cannot POST " + r.URL.Path },
		"the path the settings gave":  func(r *http.Request) string { return "nothing is served under /tenants/" + tenant + "/v1" },
		"the tenant's name by itself": func(r *http.Request) string { return "tenant " + tenant + " has no such model" },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, says(r), http.StatusNotFound)
			}))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			address := server.URL + "/tenants/" + tenant + "/v1"
			e := embedder(t, endpoint.Embedding{Address: address, Model: "m", Dimensions: 8})
			_, err := e.Query(ctx, "a question")
			said := reason(t, err, host, "answered 404 Not Found", "[the address]")
			r, rerr := endpoint.NewReranker(endpoint.Reranking{Address: address, Model: "m"})
			if rerr != nil {
				t.Fatal(rerr)
			}
			defer r.Close()
			_, err = r.Order(ctx, "a question", []string{"one", "two"})
			said += " " + reason(t, err, host, "answered 404 Not Found", "[the address]")
			for _, part := range []string{tenant, "/tenants", "/v1"} {
				if strings.Contains(said, part) {
					t.Errorf("the reason repeats %q of the address: %s", part, said)
				}
			}
		})
	}
}
