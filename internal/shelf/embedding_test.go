//go:build unix

package shelf_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// leaves is a PDF with one short page for each line, so that each line is
// one Passage.
func leaves(lines ...string) []byte {
	pages := make([][]string, len(lines))
	for i, line := range lines {
		pages[i] = []string{line}
	}
	return pdf(pages...)
}

// kept is what a collection's file says of a Source's vectors: what they
// were made with, and how many there are.
type kept struct {
	model, document   string
	dimensions, count int
}

func vectorsOf(t *testing.T, folder, collection, source string) kept {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(folder, collection+".sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var k kept
	var bytes int
	err = db.QueryRow(`SELECT e.model, e.document, e.dimensions, (SELECT coalesce(sum(length(v.vectors)), 0) FROM vectors v WHERE v.source = e.source)
		FROM embeddings e WHERE e.source = ?`, source).Scan(&k.model, &k.document, &k.dimensions, &bytes)
	switch {
	case err == sql.ErrNoRows:
		return kept{}
	case err != nil:
		t.Fatal(err)
	}
	k.count = bytes / (4 * k.dimensions)
	return k
}

// A Source is indexed with a vector for each of its Passages: they are sent
// to the endpoint a batch at a time, each marked as a stretch of the
// document with the Source's title, in requests that hold the model and the
// texts and nothing else, and the vectors are kept beside the Passages.
func TestASourceIsIndexedWithAVectorForEachPassage(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})

	var contract plugincontract.Contract
	s.ok(plugincontract.ToolContract, struct{}{}, &contract)
	if fmt.Sprint(contract.Search) != "[keyword embedding]" {
		t.Errorf("with an embedding endpoint, contract says Shelf searches by %v", contract.Search)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Fatalf("saying what it can do, Shelf asked the endpoint %+v", asked)
	}

	var lines []string
	for n := range 40 {
		lines = append(lines, fmt.Sprintf("Page %d of the book speaks of topic%d and of nothing else.", n+1, n+1))
	}
	book := file(t, "book.a1", leaves(lines...))
	if done := s.indexed("embedded", book); done.Hash != book.Hash {
		t.Fatalf("status = %+v", done)
	}
	asked := at.Asked()
	if len(asked) != 3 {
		t.Fatalf("the endpoint was asked %d times for 40 Passages, want 3 batches of up to %d", len(asked), endpoint.DefaultBatch)
	}
	var sent []string
	for n, a := range asked {
		if a.Path != "/v1/embeddings" || a.Key != testKey || a.Agent != "study-shelf/test" {
			t.Errorf("request %d went to %s, with the key %q, as %q", n, a.Path, a.Key, a.Agent)
		}
		if got := fmt.Sprint(a.Keys()); got != "[input model]" || a.Body["model"] != "test-embedder" {
			t.Errorf("request %d holds the keys %s and names the model %v: Shelf sends the model, the texts and what its settings name", n, got, a.Body["model"])
		}
		if want := []int{16, 16, 8}[n]; len(a.Texts()) != want {
			t.Errorf("request %d holds %d texts, want %d", n, len(a.Texts()), want)
		}
		sent = append(sent, a.Texts()...)
	}
	for n, line := range lines {
		if want := "title: The book.a1 book | text: " + line; n >= len(sent) || sent[n] != want {
			t.Fatalf("Passage %d was sent as %q, want %q", n, sent[min(n, len(sent)-1)], want)
		}
	}
	if got, want := vectorsOf(t, folder, "embedded", book.ID), (kept{"test-embedder", "title: {title} | text: {text}", 512, 40}); got != want {
		t.Errorf("the collection keeps %+v for the Source, want %+v", got, want)
	}

	// Asked for again it is indexed, and nothing is asked of the endpoint:
	// nor by saying what is indexed, by a Passage, or by a cancel.
	at.Forget()
	if again := s.index("embedded", book); again.State != plugincontract.StateIndexed {
		t.Errorf("index of what is indexed, vectors and all, gave %+v", again)
	}
	s.status("embedded")
	if _, ok := s.passage("embedded", index.PassageID(book.ID, book.Hash, 0, "no such text")); ok {
		t.Error("a Passage that is none was found")
	}
	s.cancel("embedded", book.ID)
	if asked := at.Asked(); len(asked) != 0 {
		t.Errorf("without indexing or searching, Shelf asked the endpoint %+v", asked)
	}
}

// A search finds a Passage by what the question means: here by a word the
// Passage does not hold, which no keyword search finds. It costs one
// request to the endpoint, for the question's vector.
func TestASearchFindsWhatTheQuestionMeans(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Same("automobile", "car", "motorcar", "car")
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	book := file(t, "book.a1", leaves(
		"A garden needs water and sunlight to grow.",
		"The river carries boats down to the harbour.",
		"The car needs fuel and a road to drive on.",
		"Bread is baked from flour, water and yeast.",
	))
	s.indexed("meaning", book)
	at.Forget()

	got := s.find("meaning", "automobile", 3)
	if len(got.Passages) != 3 || !strings.Contains(got.Passages[0].Text, "The car needs fuel") {
		t.Fatalf("a search for a word that means what a Passage says finds %q", texts(got.Passages))
	}
	if l := got.Passages[0].Location; l == nil || l.First != 3 || got.Passages[0].Source != book.ID {
		t.Errorf("the Passage found is %+v, on %+v; want page 3 of the book", got.Passages[0].Passage, l)
	}
	if fmt.Sprint(got.Search) != "[keyword embedding]" || got.Reranked || got.Notice != "" {
		t.Errorf("the search says it was made by %v (reranked: %v), with the notice %q", got.Search, got.Reranked, got.Notice)
	}
	asked := at.Asked()
	if len(asked) != 1 || asked[0].Path != "/v1/embeddings" || fmt.Sprint(asked[0].Texts()) != "[task: search result | query: automobile]" {
		t.Fatalf("for one search the endpoint was asked %+v; want the question, once, marked as one", asked)
	}
	if got := fmt.Sprint(asked[0].Keys()); got != "[input model]" || asked[0].Key != testKey {
		t.Errorf("the request holds the keys %s, with the key %q", got, asked[0].Key)
	}
	// The Passage is the one keyword search and passage know.
	if again, ok := s.passage("meaning", got.Passages[0].ID); !ok || again.Text != got.Passages[0].Text {
		t.Errorf("passage gave %+v (found: %v)", again, ok)
	}

	// A Shelf without an endpoint, on the same index: by keyword there is
	// nothing to find, and it says how it searched.
	_, plain := start(t, folder)
	if none := plain.find("meaning", "automobile", 3); len(none.Passages) != 0 || fmt.Sprint(none.Search) != "[keyword]" || none.Notice != "" {
		t.Errorf("by keyword alone the search finds %q, by %v, with the notice %q", texts(none.Passages), none.Search, none.Notice)
	}
	if now := plain.status("meaning")[book.ID]; now.State != plugincontract.StateIndexed {
		t.Errorf("a Shelf without an endpoint says %+v of a Source that has vectors", now)
	}
}

// The two rankings are put in one order, two of the embeddings' for one of
// keyword's. So a Passage that holds a name the question spells is among
// the first three, though sixty Passages mean more like the question to the
// model, which makes nothing of the name.
func TestANameTheQuestionSpellsIsAmongTheFirstThree(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Same("strtok", "")
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	var lines []string
	for n := range 60 {
		lines = append(lines, fmt.Sprintf("What is the answer to question %d about cutting text into pieces?", n+1))
	}
	lines = append(lines, "strtok splits a string at each delimiter.")
	book := file(t, "book.a1", leaves(lines...))
	s.indexed("names", book)

	// The premise: by its vectors the Passage is not among the fifty
	// nearest the question.
	f, err := index.Open(folder, index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.Collection(context.Background(), "names", false)
	if err != nil || c == nil {
		t.Fatalf("Collection = %v, %v", c, err)
	}
	question := at.Vector("task: search result | query: what is strtok")[:512]
	near, err := c.Nearest(context.Background(), index.Made{Model: "test-embedder", Document: "title: {title} | text: {text}"}, unitOf(question), 50, nil)
	if err != nil || len(near) != 50 {
		t.Fatalf("Nearest = %d Passages, %v", len(near), err)
	}
	for _, p := range near {
		if strings.Contains(p.Text, "strtok") {
			t.Fatalf("the Passage with the name is among the fifty nearest by its vector: this test proves nothing")
		}
	}
	_ = f.Close()

	got := s.find("names", "what is strtok", 5)
	if len(got.Passages) != 5 || fmt.Sprint(got.Search) != "[keyword embedding]" {
		t.Fatalf("the search gave %d Passages, by %v", len(got.Passages), got.Search)
	}
	if !strings.Contains(got.Passages[2].Text, "strtok splits a string") {
		t.Errorf("the Passage with the name the question spells is not third: %q", texts(got.Passages))
	}
	// The first two are the embeddings' first two, and after the keyword
	// ranking's best the embeddings' go on.
	for i, want := range []int{0, 1, -1, 2, 3} {
		if want >= 0 && got.Passages[i].ID != near[want].ID {
			t.Errorf("Passage %d of the search is not number %d of the embedding ranking", i, want)
		}
	}
	// A score is the Passage's place in this search, and nothing more.
	for i, p := range got.Passages {
		if want := 1 / float64(i+1); p.Score != want {
			t.Errorf("Passage %d scores %v, want %v", i, p.Score, want)
		}
	}
}

// unitOf makes a vector of length 1, as Shelf does of one it cut.
func unitOf(vector []float32) []float32 {
	var sum float64
	for _, x := range vector {
		sum += float64(x) * float64(x)
	}
	out := make([]float32, len(vector))
	for i, x := range vector {
		out[i] = float32(float64(x) / math.Sqrt(sum))
	}
	return out
}

// With a reranking endpoint, its order is what comes back: the best
// Passages of the search are handed over with the question, and returned in
// the order of its scores, with its scores.
func TestTheRerankersOrderIsWhatComesBack(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	scores := map[string]float64{"alpha": 0.5, "beta": 0.1, "gamma": 0.99, "delta": 0.2, "epsilon": 0.8}
	at.Rank(func(_ string, documents []string) []float64 {
		out := make([]float64, len(documents))
		for i, document := range documents {
			for name, score := range scores {
				if strings.Contains(document, name) {
					out[i] = score
				}
			}
		}
		return out
	})
	folder := t.TempDir()
	settings := shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at)}
	_, s := startWith(t, folder, settings)
	book := file(t, "book.a1", leaves(
		"The alpha page speaks of eigenvalues.",
		"The beta page speaks of eigenvalues.",
		"The gamma page speaks of eigenvalues.",
		"The delta page speaks of eigenvalues.",
		"The epsilon page speaks of eigenvalues.",
	))
	s.indexed("reranked", book)
	at.Forget()

	got := s.find("reranked", "where are\nthe eigenvalues?", 3)
	if !got.Reranked || fmt.Sprint(got.Search) != "[keyword embedding]" || got.Notice != "" {
		t.Errorf("the search says: by %v, reranked %v, notice %q", got.Search, got.Reranked, got.Notice)
	}
	if len(got.Passages) != 3 {
		t.Fatalf("the search gave %q", texts(got.Passages))
	}
	for i, want := range []string{"gamma", "epsilon", "alpha"} {
		if p := got.Passages[i]; !strings.Contains(p.Text, want) || p.Score != scores[want] {
			t.Errorf("Passage %d is %q with the score %v, want the %s page with the reranker's %v", i, p.Text, p.Score, want, scores[want])
		}
	}
	asked := at.Asked()
	if len(asked) != 2 || asked[0].Path != "/v1/embeddings" || asked[1].Path != "/v1/rerank" {
		t.Fatalf("for one search the endpoints were asked %+v; want the question's vector, then the order", asked)
	}
	rerank := asked[1]
	if got := fmt.Sprint(rerank.Keys()); got != "[documents model query top_n]" || rerank.Key != testKey {
		t.Errorf("the reranking request holds the keys %s, with the key %q", got, rerank.Key)
	}
	// The question as it was asked, without the embedding model's mark, and
	// every Passage the search found, each once.
	if rerank.Body["model"] != "test-reranker" || rerank.Body["query"] != "where are the eigenvalues?" || rerank.Body["top_n"] != float64(5) {
		t.Errorf("the reranking request is %v", rerank.Body)
	}
	documents := rerank.Texts()
	slices.Sort(documents)
	if want := "[The alpha page speaks of eigenvalues. The beta page speaks of eigenvalues. The delta page speaks of eigenvalues. " +
		"The epsilon page speaks of eigenvalues. The gamma page speaks of eigenvalues.]"; fmt.Sprint(documents) != want {
		t.Errorf("the reranker was handed %q", documents)
	}

	// It is handed as many as the settings say, and never fewer than are
	// asked for.
	_, few := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at, func(r *endpoint.Reranking) { r.Candidates = 2 })})
	at.Forget()
	if got := few.find("reranked", "eigenvalues", 1); len(got.Passages) != 1 || !got.Reranked {
		t.Errorf("with two candidates and a limit of 1: %q, reranked %v", texts(got.Passages), got.Reranked)
	}
	if handed := at.AskedAt("/v1/rerank"); len(handed) != 1 || len(handed[0].Texts()) != 2 {
		t.Errorf("with two candidates, the reranker was handed %+v", handed)
	}
	at.Forget()
	if got := few.find("reranked", "eigenvalues", 4); len(got.Passages) != 4 || !got.Reranked {
		t.Errorf("with two candidates and a limit of 4: %q, reranked %v", texts(got.Passages), got.Reranked)
	}
	if handed := at.AskedAt("/v1/rerank"); len(handed) != 1 || len(handed[0].Texts()) != 4 {
		t.Errorf("asked for 4 Passages, the reranker was handed %+v", handed)
	}

	// A reranker that fails takes nothing from the search but its order:
	// the Passages come as the search found them, and the search says so.
	_, unranked := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	want := unranked.find("reranked", "eigenvalues", 5)
	for name, answer := range map[string]func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool{
		"answered 500": func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			if r.URL.Path != "/v1/rerank" {
				return false
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error": {"message": "out of memory, said to %s"}}`, r.Header.Get("Authorization"))
			return true
		},
		"ranked 1 Passages of the 5": func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			if r.URL.Path != "/v1/rerank" {
				return false
			}
			fmt.Fprint(w, `{"results": [{"index": 0, "relevance_score": 1}]}`)
			return true
		},
	} {
		at.Answer(answer)
		got := s.find("reranked", "eigenvalues", 5)
		if got.Reranked || fmt.Sprint(got.Search) != "[keyword embedding]" {
			t.Errorf("%s: the search says: by %v, reranked %v", name, got.Search, got.Reranked)
		}
		for _, word := range []string{"not reranked", "the reranking endpoint " + at.Host, name} {
			if !strings.Contains(got.Notice, word) {
				t.Errorf("%s: the notice does not say %q: %s", name, word, got.Notice)
			}
		}
		if fmt.Sprint(ids(got.Passages)) != fmt.Sprint(ids(want.Passages)) {
			t.Errorf("%s: without the reranker's order the Passages are %q, and a Shelf without a reranker gives %q", name, texts(got.Passages), texts(want.Passages))
		}
	}
	at.Answer(nil)

	// One Passage is in its order already: nothing is asked.
	alone := file(t, "alone.b2", leaves("One page alone speaks of quaternions."))
	s.indexed("reranked", alone)
	at.Forget()
	if got := s.find("reranked", "quaternions", 5, alone.ID); len(got.Passages) != 1 || got.Reranked {
		t.Errorf("a search that finds one Passage: %q, reranked %v", texts(got.Passages), got.Reranked)
	}
	if handed := at.AskedAt("/v1/rerank"); len(handed) != 0 {
		t.Errorf("the reranker was asked to order one Passage: %+v", handed)
	}
}

// A reranking endpoint works without an embedding endpoint too: what
// keyword search found is put in its order.
func TestARerankerOrdersWhatKeywordSearchFound(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Rank(func(_ string, documents []string) []float64 {
		out := make([]float64, len(documents))
		for i, document := range documents {
			out[i] = float64(strings.Count(document, "more"))
		}
		return out
	})
	_, s := startWith(t, t.TempDir(), shelf.Settings{Reranker: rerankerAt(t, at)})
	s.indexed("words", file(t, "book.a1", leaves("Lattices, and no more.", "Lattices, and more and more and more.", "Lattices, and more and more.")))
	got := s.find("words", "lattices", 3)
	if !got.Reranked || fmt.Sprint(got.Search) != "[keyword]" || len(got.Passages) != 3 {
		t.Fatalf("the search gave %q, by %v, reranked %v", texts(got.Passages), got.Search, got.Reranked)
	}
	for i, want := range []float64{3, 2, 1} {
		if got.Passages[i].Score != want {
			t.Errorf("Passage %d has the score %v, want the reranker's %v", i, got.Passages[i].Score, want)
		}
	}
	if asked := at.Asked(); len(asked) != 1 || asked[0].Path != "/v1/rerank" || len(asked[0].Texts()) != 3 {
		t.Errorf("the endpoint was asked %+v; want the three Passages to order, once", asked)
	}
}

// An endpoint that fails while a Source is indexed leaves the Source
// failed, with a reason that names the endpoint's host and never its key,
// and nothing of the Source in the index: a Source is there whole, with its
// vectors, or not at all. What was indexed before stays, and Shelf goes on.
func TestAnEndpointThatFailsLeavesTheSourceFailed(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	vectors := func(w http.ResponseWriter, count, size int) {
		data := make([]map[string]any, count)
		for i := range data {
			vector := make([]float32, size)
			vector[i%size] = 1
			data[i] = map[string]any{"index": i, "embedding": vector}
		}
		_ = jsonTo(w, map[string]any{"data": data})
	}
	for _, tc := range []struct {
		name   string
		answer func(w http.ResponseWriter, r *http.Request, body map[string]any) bool
		change func(*endpoint.Embedding)
		want   []string
		// batches is how many requests the endpoint gets before Shelf gives
		// up.
		batches int
	}{
		{name: "it is slow", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		}, change: func(e *endpoint.Embedding) { e.Timeout = 200 * time.Millisecond }, want: []string{"did not answer within 200ms"}, batches: 1},
		{name: "the wrong number of vectors", answer: func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
			vectors(w, 3, 768)
			return true
		}, want: []string{"gave 3 vectors for 16 texts"}, batches: 1},
		{name: "vectors of the wrong size", answer: func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
			vectors(w, len(body["input"].([]any)), 384)
			return true
		}, want: []string{"gave vectors of 384 numbers", "ask for 512"}, batches: 1},
		{name: "what is no JSON", answer: func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			fmt.Fprint(w, "It works!")
			return true
		}, want: []string{"did not answer as its API does", "not JSON"}, batches: 1},
		{name: "an error", answer: func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error": {"message": "Loading model, for %s"}}`, r.Header.Get("Authorization"))
			return true
		}, want: []string{"answered 503 Service Unavailable", "Loading model", "[the key]"}, batches: 1},
		{name: "too much", answer: func(w http.ResponseWriter, _ *http.Request, body map[string]any) bool {
			vectors(w, len(body["input"].([]any)), 20000)
			return true
		}, change: func(e *endpoint.Embedding) { e.MaxBytes = 256 << 10 }, want: []string{"sent more than 256 KB in one answer"}, batches: 1},
		{name: "an error at the second batch", answer: func() func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			var seen atomic.Int64
			return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
				if seen.Add(1) == 1 {
					return false
				}
				http.Error(w, "the model went away", http.StatusBadGateway)
				return true
			}
		}(), want: []string{"answered 502 Bad Gateway", "the model went away"}, batches: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := shelftest.NewEndpoint(t)
			folder := t.TempDir()
			_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
			before := file(t, "kept.a1", leaves("A book that was indexed before speaks of determinants."))
			s.indexed("failing", before)
			// The Shelf that meets the failure: with less time for the
			// endpoint, or less room for its answer, where the endpoint is
			// to go over them. What is to be indexed is asked of the Shelf
			// above, which gives an endpoint all the time it has: an answer
			// that only took its time on a busy computer is no failure.
			asking := s
			if tc.change != nil {
				_, asking = startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at, tc.change)})
			}

			var lines []string
			for n := range 40 {
				lines = append(lines, fmt.Sprintf("Page %d speaks of cobordism%d.", n+1, n+1))
			}
			book := file(t, "book.b2", leaves(lines...))
			at.Forget()
			at.Answer(tc.answer)
			began := time.Now()
			failed := asking.fails("failing", book, append(tc.want, "the embedding endpoint "+at.Host)...)
			// What went wrong and no more: study shows three hundred bytes
			// of a reason, and gives the command that has it tried again.
			if len(failed.Reason) > 300 || strings.Contains(failed.Reason, "key] key]") {
				t.Errorf("the reason is %d bytes, more than study shows, or was taken apart twice: %s", len(failed.Reason), failed.Reason)
			}
			if took := time.Since(began); took > time.Minute {
				t.Errorf("it took %s to find out", took)
			}
			if failed.Hash != "" {
				t.Errorf("status gives the hash %s for a Source nothing was indexed of", failed.Hash)
			}
			if strings.Contains(failed.Reason, "/v1/embeddings") {
				t.Errorf("the reason says more of the endpoint's address than its host: %s", failed.Reason)
			}
			// What the endpoint was asked before Shelf gave up: the first
			// Passages, with the key.
			asked := at.Asked()
			if len(asked) != tc.batches || asked[0].Key != testKey || len(asked[0].Texts()) != 16 ||
				asked[0].Texts()[0] != "title: The book.b2 book | text: Page 1 speaks of cobordism1." {
				t.Errorf("the endpoint was asked %d times, first %v", len(asked), asked)
			}
			// Nothing of the Source is found, by its words either, and what
			// was indexed before is.
			if found := s.find("failing", "cobordism1 cobordism20 cobordism40", 10, book.ID); len(found.Passages) != 0 {
				t.Errorf("a search finds %d Passages of a Source whose vectors could not be made: half a Source is found as if it were whole", len(found.Passages))
			}
			if got := vectorsOf(t, folder, "failing", book.ID); got != (kept{}) {
				t.Errorf("the collection keeps %+v for the Source", got)
			}
			if now := s.status("failing")[before.ID]; now.State != plugincontract.StateIndexed || now.Hash != before.Hash {
				t.Errorf("what was indexed before is now %+v", now)
			}
			// It stays failed for the next Shelf, and is indexed once the
			// endpoint answers.
			at.Answer(nil)
			s.indexed("failing", book)
			if found := holding(s.search("failing", "cobordism40", 5), "cobordism40."); len(found) != 1 {
				t.Errorf("indexed once the endpoint answered, the Source's last page is found %d times", len(found))
			}
		})
	}
}

// jsonTo writes v as an answer's JSON.
func jsonTo(w http.ResponseWriter, v any) error { return json.NewEncoder(w).Encode(v) }

// An endpoint that is not there at all, and new content of a Source that is
// indexed: the earlier content is still found, with its hash, beside the
// reason.
func TestAnEndpointThatIsDownLeavesWhatWasIndexed(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	earlier := file(t, "book.a1", leaves("The first edition speaks of tensors."))
	s.indexed("down", earlier)
	at.Stop()

	later := earlier
	data := leaves("The second edition speaks of spinors.")
	later.Path = filepath.Join(t.TempDir(), "second-edition.pdf")
	if err := os.WriteFile(later.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	later.Hash = hashOf(data)
	failed := s.fails("down", later, "the embedding endpoint "+at.Host, "could not be reached")
	if failed.Hash != earlier.Hash {
		t.Errorf("status of a Source whose new content could not be embedded gives the hash %q, want the earlier content's", failed.Hash)
	}
	// The search has no endpoint either, and still finds the earlier
	// content, by keyword, saying so.
	got := s.find("down", "tensors spinors", 5)
	if len(got.Passages) != 1 || !strings.Contains(got.Passages[0].Text, "tensors") || fmt.Sprint(got.Search) != "[keyword]" {
		t.Errorf("with the endpoint down, a search finds %q by %v", texts(got.Passages), got.Search)
	}
	if got := vectorsOf(t, folder, "down", earlier.ID); got.count != 1 {
		t.Errorf("the earlier content's vectors are %+v", got)
	}
	// A new Source, with nothing indexed before, has no hash.
	fresh := file(t, "new.b2", leaves("A new book speaks of twistors."))
	if failed := s.fails("down", fresh, at.Host, "could not be reached"); failed.Hash != "" {
		t.Errorf("status gives the hash %s for a Source nothing was indexed of", failed.Hash)
	}
	// A Shelf that is started later says the same.
	_, next := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	if now := next.status("down")[fresh.ID]; now.State != plugincontract.StateFailed || !strings.Contains(now.Reason, at.Host) {
		t.Errorf("a Shelf started later says %+v", now)
	}
}

// An endpoint that does not answer while Shelf searches takes the vectors
// from the search and nothing else: it is answered by keyword, and says so,
// with the reason. Once the endpoint answers, so does the search by it.
func TestASearchFallsBackToKeywordWhenTheEndpointFails(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	book := file(t, "book.a1", leaves("The first page speaks of functors.", "The second page speaks of natural transformations."))
	s.indexed("fallback", book)
	// A Shelf that gives the endpoint a fifth of a second, for the endpoint
	// that is to be too slow, and for no other: an answer that only took its
	// time on a busy computer is no failure.
	_, hurried := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at, func(e *endpoint.Embedding) { e.QueryTimeout = 200 * time.Millisecond })})

	release := make(chan struct{})
	defer close(release)
	for name, answer := range map[string]func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool{
		"answered 500 Internal Server Error": func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error": "no memory left, said to %s"}`, r.Header.Get("Authorization"))
			return true
		},
		"did not answer within 200ms": func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		},
		"gave vectors of 16 numbers": func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
			_ = jsonTo(w, map[string]any{"data": []map[string]any{{"index": 0, "embedding": make([]float32, 16)}}})
			return true
		},
	} {
		at.Answer(answer)
		at.Forget()
		began := time.Now()
		asking := s
		if strings.HasPrefix(name, "did not answer within") {
			asking = hurried
		}
		got := asking.find("fallback", "functors", 5)
		if took := time.Since(began); took > time.Minute {
			t.Errorf("%s: the search took %s", name, took)
		}
		if len(got.Passages) != 1 || !strings.Contains(got.Passages[0].Text, "functors") || got.Passages[0].Score <= 0 {
			t.Errorf("%s: the search finds %+v; want the Passage keyword search finds", name, got.Passages)
		}
		if fmt.Sprint(got.Search) != "[keyword]" || got.Reranked {
			t.Errorf("%s: the search says it was made by %v", name, got.Search)
		}
		for _, word := range []string{"These Passages were found by keyword alone", "the embedding endpoint " + at.Host, name} {
			if !strings.Contains(got.Notice, word) {
				t.Errorf("%s: the notice does not say %q: %s", name, word, got.Notice)
			}
		}
		if strings.Contains(got.Notice, "/v1/") {
			t.Errorf("%s: the notice says more of the endpoint's address than its host: %s", name, got.Notice)
		}
		// It was asked, once, for the question's vector.
		if asked := at.Asked(); len(asked) != 1 || fmt.Sprint(asked[0].Texts()) != "[task: search result | query: functors]" || asked[0].Key != testKey {
			t.Errorf("%s: the endpoint was asked %+v", name, asked)
		}
		// The Source is indexed all the same: a search that fell back is no
		// failure of the Source.
		if now := s.status("fallback")[book.ID]; now.State != plugincontract.StateIndexed {
			t.Errorf("%s: status says %+v of the Source", name, now)
		}
	}
	// Once it answers again, the search is by vectors again.
	at.Answer(nil)
	if got := s.find("fallback", "functors", 5); fmt.Sprint(got.Search) != "[keyword embedding]" || got.Notice != "" || len(got.Passages) != 2 {
		t.Errorf("once the endpoint answers: %q by %v, with the notice %q", texts(got.Passages), got.Search, got.Notice)
	}
	// And down for good.
	at.Stop()
	if got := s.find("fallback", "natural transformations", 5); len(got.Passages) != 1 || fmt.Sprint(got.Search) != "[keyword]" ||
		!strings.Contains(got.Notice, "found by keyword alone") || !strings.Contains(got.Notice, at.Host+" could not be reached") {
		t.Errorf("with the endpoint down: %q by %v, with the notice %q", texts(got.Passages), got.Search, got.Notice)
	}
}

// Vectors that came from another model, or are of another size, or were
// made of text marked another way, say nothing to a question's vector. A
// Source that has such vectors, or none, is reported by status with the
// reason, as failed and still found, and indexing it again puts it right:
// its Passages are given new vectors and stay as they are, and the Source
// is not read a second time.
func TestVectorsOfAnotherMakingAreReportedAndPutRightByIndexingAgain(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	var fetched atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><head><title>Notes</title></head><body><h1>Notes</h1><p>A web page speaks of sheaves and of stalks.</p></body></html>")
	}))
	defer server.Close()
	withModel := func(model string, more ...func(*endpoint.Embedding)) shelf.Settings {
		return shelf.Settings{Embedder: embedderAt(t, at, append([]func(*endpoint.Embedding){func(e *endpoint.Embedding) { e.Model = model }}, more...)...)}
	}

	first, s := startWith(t, folder, withModel("model-a"))
	book := file(t, "book.a1", leaves("The first page speaks of sheaves.", "The second page speaks of schemes.", "The third page speaks of stacks."))
	page := plugincontract.Source{ID: "notes.b2", Title: "Notes", Kind: plugincontract.SourceURL, URL: server.URL + "/notes"}
	s.indexed("changed", book)
	s.indexed("changed", page)
	before := s.search("changed", "sheaves schemes stacks stalks", 10)
	if len(before) != 4 {
		t.Fatalf("found %q", texts(before))
	}
	first.Close()

	// The learner names another model in Shelf's settings.
	second, s := startWith(t, folder, withModel("model-b"))
	at.Forget()
	status := s.status("changed")
	for _, source := range []plugincontract.Source{book, page} {
		now := status[source.ID]
		if now.State != plugincontract.StateFailed || now.Hash == "" {
			t.Fatalf("status of %s, whose vectors are another model's = %+v; want it failed, with the hash of what is still found", source.ID, now)
		}
		for _, word := range []string{"found by keyword alone", "the model model-a", "now name the model model-b", "Index it again"} {
			if !strings.Contains(now.Reason, word) {
				t.Errorf("the reason does not say %q: %s", word, now.Reason)
			}
		}
		// study shows 300 bytes of a reason where it shows what is indexed.
		if len(now.Reason) > 300 {
			t.Errorf("the reason is %d bytes, more than study shows of one: %s", len(now.Reason), now.Reason)
		}
	}
	if status[book.ID].Hash != book.Hash {
		t.Errorf("status gives the hash %s for the book, want its own", status[book.ID].Hash)
	}
	// What is there is found, by its words, and the search says why by no
	// more. Neither that nor status asked the endpoint anything: there is
	// no vector to compare a question's with.
	got := s.find("changed", "schemes", 5)
	if len(got.Passages) != 1 || fmt.Sprint(got.Search) != "[keyword]" ||
		!strings.Contains(got.Notice, "found by keyword alone") || !strings.Contains(got.Notice, "none of the Sources searched has vectors of the model model-b") {
		t.Errorf("a search finds %q by %v, with the notice %q", texts(got.Passages), got.Search, got.Notice)
	}
	if asked := at.Asked(); len(asked) != 0 {
		t.Fatalf("to say what is indexed, and to search where no vector fits, Shelf asked the endpoint %+v", asked)
	}

	// Indexing again, as study source index does for a Source that failed
	// and as --again does for any: the same content is asked for. The file
	// is gone by now, and need not be there: what is indexed is embedded.
	if err := os.Remove(book.Path); err != nil {
		t.Fatal(err)
	}
	if began := s.index("changed", book); began.State != plugincontract.StateIndexing || began.Hash != book.Hash {
		t.Fatalf("index of content whose vectors are another model's answered %+v; want it being indexed, and still found", began)
	}
	if done, _ := s.settled("changed", book.ID); done.State != plugincontract.StateIndexed || done.Hash != book.Hash || done.Reason != "" {
		t.Fatalf("after it was indexed again, status says %+v", done)
	}
	s.indexed("changed", page)
	if n := fetched.Load(); n != 1 {
		t.Errorf("the web page was fetched %d times: giving its Passages vectors fetches nothing", n)
	}
	asked := at.Asked()
	var sent []string
	for _, a := range asked {
		if a.Body["model"] != "model-b" || a.Path != "/v1/embeddings" {
			t.Errorf("the endpoint was asked for the model %v at %s", a.Body["model"], a.Path)
		}
		sent = append(sent, a.Texts()...)
	}
	slices.Sort(sent)
	if want := "[title: Notes | text: A web page speaks of sheaves and of stalks. title: The book.a1 book | text: The first page speaks of sheaves. " +
		"title: The book.a1 book | text: The second page speaks of schemes. title: The book.a1 book | text: The third page speaks of stacks.]"; fmt.Sprint(sent) != want {
		t.Errorf("the endpoint was sent %q", sent)
	}
	if got, want := vectorsOf(t, folder, "changed", book.ID), (kept{"model-b", "title: {title} | text: {text}", 512, 3}); got != want {
		t.Errorf("the collection keeps %+v for the book, want %+v", got, want)
	}
	// The Passages are the ones they were, under their ids.
	if after := s.search("changed", "sheaves schemes stacks stalks", 10); len(after) != 4 {
		t.Errorf("found %q", texts(after))
	} else {
		was, now := ids(before), ids(after)
		slices.Sort(was)
		slices.Sort(now)
		if fmt.Sprint(was) != fmt.Sprint(now) {
			t.Errorf("the Passages' ids were %v, and are %v after their vectors were made anew", was, now)
		}
	}
	if got := s.find("changed", "schemes", 5); fmt.Sprint(got.Search) != "[keyword embedding]" || got.Notice != "" {
		t.Errorf("once the vectors are the model's, a search is by %v, with the notice %q", got.Search, got.Notice)
	}
	second.Close()

	// Another size of the same model's vectors.
	third, s := startWith(t, folder, withModel("model-b", func(e *endpoint.Embedding) { e.Dimensions = 256 }))
	if now := s.status("changed")[book.ID]; now.State != plugincontract.StateFailed || now.Hash != book.Hash ||
		!strings.Contains(now.Reason, "its vectors have 512 numbers, and Shelf's settings now ask for 256") {
		t.Errorf("status of a Source whose vectors are of another size = %+v", now)
	}
	// One Source is put right, and the other not yet: the search is by
	// vectors where they fit, and says of how many Sources they do not.
	s.indexed("changed", book)
	if got := vectorsOf(t, folder, "changed", book.ID); got.dimensions != 256 || got.count != 3 {
		t.Errorf("the collection keeps %+v for the book", got)
	}
	got = s.find("changed", "stalks stacks", 5)
	if fmt.Sprint(got.Search) != "[keyword embedding]" || !strings.Contains(got.Notice, "1 of the 2 Sources searched were searched by keyword alone") ||
		len(holding(got.Passages, "stalks")) != 1 {
		t.Errorf("with one Source of two put right, a search finds %q by %v, with the notice %q", texts(got.Passages), got.Search, got.Notice)
	}
	if got := s.find("changed", "stacks", 5, book.ID); got.Notice != "" {
		t.Errorf("searching only the Source that is put right, the notice is %q", got.Notice)
	}
	third.Close()

	// Text marked for the model in another way.
	fourth, s := startWith(t, folder, withModel("model-b", func(e *endpoint.Embedding) {
		e.Dimensions = 256
		e.Document = endpoint.Marking{Text: "passage: {text}"}
	}))
	if now := s.status("changed")[book.ID]; now.State != plugincontract.StateFailed || !strings.Contains(now.Reason, "marked for the model model-b in another way") {
		t.Errorf("status of a Source whose Passages were marked another way = %+v", now)
	}
	fourth.Close()

	// A Shelf with no endpoint has nothing to say of vectors: the Sources
	// are indexed, and it indexes without them.
	fifth, s := start(t, folder)
	for id, now := range s.status("changed") {
		if now.State != plugincontract.StateIndexed || now.Reason != "" {
			t.Errorf("a Shelf without an endpoint says %+v of %s", now, id)
		}
	}
	plain := file(t, "plain.c3", leaves("A book indexed without an endpoint speaks of gerbes."))
	s.indexed("changed", plain)
	fifth.Close()

	// And one that has an endpoint says of that Source that it has none.
	_, s = startWith(t, folder, withModel("model-b", func(e *endpoint.Embedding) { e.Dimensions = 256 }))
	now := s.status("changed")[plain.ID]
	if now.State != plugincontract.StateFailed || now.Hash != plain.Hash ||
		!strings.Contains(now.Reason, "it was indexed without vectors, and Shelf now has an embedding endpoint, for the model model-b") {
		t.Errorf("status of a Source that was indexed without vectors = %+v", now)
	}
	// An endpoint that fails while the Passages are given vectors leaves
	// them as they were: found, by their words, with the endpoint's reason.
	at.Answer(func(w http.ResponseWriter, _ *http.Request, _ map[string]any) bool {
		http.Error(w, "the model is loading", http.StatusServiceUnavailable)
		return true
	})
	failed := s.fails("changed", plain, "the embedding endpoint "+at.Host, "answered 503", "the model is loading")
	if failed.Hash != plain.Hash {
		t.Errorf("status gives the hash %q for content that is still found", failed.Hash)
	}
	if found := s.search("changed", "gerbes", 5); len(found) != 1 {
		t.Errorf("after its Passages could not be given vectors, the Source is found %d times", len(found))
	}
	at.Answer(nil)
	s.indexed("changed", plain)
	if got := vectorsOf(t, folder, "changed", plain.ID); got.model != "model-b" || got.count != 1 {
		t.Errorf("the collection keeps %+v for the Source", got)
	}
}

// Vectors are given to the content that was asked for, or to none. Another
// copy of Shelf may put other content of a file in the Source's place
// between the moment index answers and the moment the Passages are read:
// those Passages are not embedded in the place of the ones asked for.
func TestVectorsAreNotGivenToContentThatWasNotAskedFor(t *testing.T) {
	warm(t)
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	first, s := start(t, folder)
	asked := file(t, "book.a1", leaves("The first edition speaks of motives."))
	s.indexed("swapped", asked)
	first.Close()

	// The other copy, and the content it indexes meanwhile.
	_, other := start(t, folder)
	later := asked
	data := leaves("The second edition speaks of periods.")
	later.Path = filepath.Join(t.TempDir(), "second-edition.pdf")
	if err := os.WriteFile(later.Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	later.Hash = hashOf(data)

	// The work waits, where it is about to read the Passages, until the
	// other copy has indexed.
	reached, swapped := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	t.Cleanup(func() { release.Do(func() { close(swapped) }) })
	plugin := shelf.New(shelf.Options{Folder: folder, Settings: shelf.Settings{Embedder: embedderAt(t, at)}, Crash: func(point string) {
		if point == shelf.PointGivingVectors {
			once.Do(func() { close(reached) })
			<-swapped
		}
	}})
	t.Cleanup(plugin.Close)
	s = connect(t, plugin)
	if began := s.index("swapped", asked); began.State != plugincontract.StateIndexing {
		t.Fatalf("index answered %+v", began)
	}
	select {
	case <-reached:
	case <-time.After(patience):
		t.Fatal("the work never came to read the Passages")
	}
	other.indexed("swapped", later)
	release.Do(func() { close(swapped) })
	now, _ := s.settled("swapped", asked.ID)
	if now.State != plugincontract.StateFailed || now.Hash != later.Hash || !strings.Contains(now.Reason, "its content was indexed anew") {
		t.Errorf("status says %+v; want the work failed, and the content the other copy indexed still found", now)
	}
	if sent := at.Asked(); len(sent) != 0 {
		t.Errorf("the Passages of content nobody asked to have embedded were sent to the endpoint: %+v", sent)
	}
	if got := vectorsOf(t, folder, "swapped", asked.ID); got != (kept{}) {
		t.Errorf("the collection keeps %+v for the Source", got)
	}
	if found := s.search("swapped", "periods", 5); len(found) != 1 {
		t.Errorf("the content the other copy indexed is found %d times", len(found))
	}
}

// Giving vectors to what is indexed can be stopped like any indexing, and
// leaves the Source as it was: found by keyword, and said to be.
func TestGivingVectorsToWhatIsIndexedCanBeStopped(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	folder := t.TempDir()
	first, s := start(t, folder)
	book := file(t, "book.a1", leaves("A book that was indexed without vectors speaks of motives."))
	s.indexed("stopped", book)
	first.Close()

	_, s = startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at)})
	release := make(chan struct{})
	defer close(release)
	var began atomic.Bool
	at.Answer(func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		began.Store(true)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	})
	if answered := s.index("stopped", book); answered.State != plugincontract.StateIndexing || answered.Hash != book.Hash {
		t.Fatalf("index answered %+v", answered)
	}
	for deadline := time.Now().Add(patience); !began.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the endpoint was never asked")
		}
	}
	// Asked for again meanwhile, it is one piece of work.
	if again := s.index("stopped", book); again.State != plugincontract.StateIndexing {
		t.Errorf("asked again while its Passages are embedded, index answered %+v", again)
	}
	if !s.cancel("stopped", book.ID) {
		t.Fatal("cancel says it stopped nothing")
	}
	now := s.status("stopped")[book.ID]
	if now.State != plugincontract.StateFailed || now.Hash != book.Hash || !strings.Contains(now.Reason, "indexed without vectors") {
		t.Errorf("after the embedding was stopped, status says %+v; want the Source as it was", now)
	}
	if found := s.search("stopped", "motives", 5); len(found) != 1 {
		t.Errorf("after the embedding was stopped, the Source is found %d times", len(found))
	}
	if got := vectorsOf(t, folder, "stopped", book.ID); got != (kept{}) {
		t.Errorf("the collection keeps %+v of an embedding that was stopped", got)
	}
	if asked := at.Asked(); len(asked) != 1 {
		t.Errorf("the endpoint was asked %d times, want once: two requests to index the same content are one piece of work", len(asked))
	}
}

// A server that applies the model's prompts itself, by name, as the full
// EmbeddingGemma 2 is served: the request names the prompt and the size,
// the texts go as they are, and nothing the server does not know is sent,
// since it refuses a request for one key it does not know.
func TestAServerThatAppliesTheModelsPromptsIsToldByName(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	at.Prompts("query", "task: search result | query: ", "document", "title: none | text: ")
	at.Same("automobile", "car")
	folder := t.TempDir()
	_, s := startWith(t, folder, shelf.Settings{Embedder: embedderAt(t, at, func(e *endpoint.Embedding) {
		e.Model, e.SendDimensions = "google/embeddinggemma-2", true
		e.Query = endpoint.Marking{Fields: map[string]any{"prompt": "query"}}
		e.Document = endpoint.Marking{Fields: map[string]any{"prompt": "document"}}
	})})
	book := file(t, "book.a1", leaves("A garden needs water and sunlight.", "The car needs fuel and a road."))
	s.indexed("prompts", book)
	got := s.find("prompts", "automobile", 1)
	if len(got.Passages) != 1 || !strings.Contains(got.Passages[0].Text, "The car needs fuel") || fmt.Sprint(got.Search) != "[keyword embedding]" || got.Notice != "" {
		t.Fatalf("the search finds %q by %v, with the notice %q", texts(got.Passages), got.Search, got.Notice)
	}
	asked := at.Asked()
	if len(asked) != 2 {
		t.Fatalf("the endpoint was asked %d times, want once for the Passages and once for the question", len(asked))
	}
	for n, want := range []struct{ prompt, texts string }{
		{"document", "[A garden needs water and sunlight. The car needs fuel and a road.]"},
		{"query", "[automobile]"},
	} {
		a := asked[n]
		if got := fmt.Sprint(a.Keys()); got != "[dimensions input model prompt]" {
			t.Errorf("request %d holds the keys %s", n, got)
		}
		if a.Body["prompt"] != want.prompt || a.Body["dimensions"] != float64(512) || a.Body["model"] != "google/embeddinggemma-2" || fmt.Sprint(a.Texts()) != want.texts {
			t.Errorf("request %d is %v; want the prompt %s and the texts as they are, %s", n, a.Body, want.prompt, want.texts)
		}
	}
	if got, want := vectorsOf(t, folder, "prompts", book.ID), (kept{"google/embeddinggemma-2", `request {"prompt":"document"}`, 512, 2}); got != want {
		t.Errorf("the collection keeps %+v for the Source, want %+v", got, want)
	}
}
