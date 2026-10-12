package shelftest

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// Endpoint is what a test gives Shelf for an embedding endpoint and a
// reranking endpoint: a server on this computer's loopback interface that
// speaks the OpenAI embeddings API at /v1/embeddings and the rerank API at
// /v1/rerank, with no model behind it, and remembers everything it was
// asked. No test asks a real endpoint, but the one that is skipped unless
// it is given the address of one.
//
// Its vectors are made of a text's words: two texts that share words are
// near each other, and words a test declares to mean the same (Same) count
// as one. That is enough to tell a search by vectors from a search by
// keyword, which no stem joins "automobile" and "car" for.
type Endpoint struct {
	// URL is the address to put in Shelf's settings: it ends in /v1.
	URL string
	// Host is the endpoint's host and port, which is what Shelf says of it.
	Host string

	mu sync.Mutex
	// size is how many numbers a vector has.
	size int
	// same maps a word to the word it counts as, and only, when it holds
	// any, are the words the "model" makes something of.
	same map[string]string
	only map[string]bool
	// prompts, when not nil, makes this a server that marks the texts
	// itself: the request names one of the prompts, by its key, and the
	// server writes the prompt's text before each input. Such a server
	// refuses a request key it does not know, and cuts vectors when asked.
	prompts map[string]string
	// answer, when not nil, answers a request in the server's place, and
	// reports whether it did.
	answer func(w http.ResponseWriter, r *http.Request, body map[string]any) bool
	// rank scores documents for a query. The default counts shared words.
	rank func(query string, documents []string) []float64
	// asked is every request received, in order.
	asked []Asked

	server *httptest.Server
}

// Asked is one request an Endpoint received.
type Asked struct {
	// Path is the path it was posted to: /v1/embeddings or /v1/rerank.
	Path string
	// Key is the bearer token it came with, and Agent its User-Agent.
	Key, Agent string
	// Body is the request, as the JSON object it was.
	Body map[string]any
}

// Keys are the names of the request's fields, in order.
func (a Asked) Keys() []string {
	keys := make([]string, 0, len(a.Body))
	for key := range a.Body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Texts are the texts of an embeddings request, or the documents of a
// rerank request.
func (a Asked) Texts() []string {
	list, _ := a.Body["input"].([]any)
	if a.Path == "/v1/rerank" {
		list, _ = a.Body["documents"].([]any)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

// NewEndpoint starts a test endpoint whose vectors have 768 numbers. It is
// stopped when the test ends.
func NewEndpoint(t testing.TB) *Endpoint {
	t.Helper()
	e := &Endpoint{size: 768, same: map[string]string{}}
	e.server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.server.Close)
	e.URL = e.server.URL + "/v1"
	e.Host = strings.TrimPrefix(e.server.URL, "http://")
	return e
}

// Stop stops the endpoint: nothing listens at its address any more.
func (e *Endpoint) Stop() { e.server.Close() }

// Size sets how many numbers a vector has.
func (e *Endpoint) Size(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.size = n
}

// Same declares words that mean the same to the endpoint's "model": each
// pair is a word and the word it counts as. A word that counts as "" is one
// the model makes nothing of, as a real one makes little of a name it never
// read.
func (e *Endpoint) Same(pairs ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := 0; i+1 < len(pairs); i += 2 {
		e.same[pairs[i]] = pairs[i+1]
	}
}

// Only has the endpoint's "model" make something of these words and of no
// other, once Same has said what each word counts as: for texts written as
// people write, whose many small words would otherwise decide which two
// are alike.
func (e *Endpoint) Only(words ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.only = map[string]bool{}
	for _, word := range words {
		e.only[word] = true
	}
}

// Prompts makes the endpoint a server that marks the texts itself, as the
// maintainer's EmbeddingGemma 2 service does: a request names one of these
// prompts with its prompt key, the server writes that prompt's text before
// each input, a request key it does not know is refused, and dimensions is
// honoured. Each pair is a prompt's name and its text.
func (e *Endpoint) Prompts(pairs ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prompts = map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		e.prompts[pairs[i]] = pairs[i+1]
	}
}

// Answer has fn answer requests in the endpoint's place from now on: fn
// reports whether it answered. nil gives the endpoint its own voice back.
func (e *Endpoint) Answer(fn func(w http.ResponseWriter, r *http.Request, body map[string]any) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.answer = fn
}

// Rank sets how the endpoint scores documents for a query when it is asked
// to rerank.
func (e *Endpoint) Rank(fn func(query string, documents []string) []float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rank = fn
}

// Asked returns every request received so far, in order.
func (e *Endpoint) Asked() []Asked {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.asked)
}

// AskedAt returns the requests received at a path.
func (e *Endpoint) AskedAt(path string) []Asked {
	var out []Asked
	for _, a := range e.Asked() {
		if a.Path == path {
			out = append(out, a)
		}
	}
	return out
}

// Forget forgets the requests received so far.
func (e *Endpoint) Forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = nil
}

func (e *Endpoint) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	e.mu.Lock()
	e.asked = append(e.asked, Asked{Path: r.URL.Path, Key: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		Agent: r.Header.Get("User-Agent"), Body: body})
	answer := e.answer
	e.mu.Unlock()
	if answer != nil && answer(w, r, body) {
		return
	}
	switch {
	case r.Method != http.MethodPost || body == nil:
		refuse(w, http.StatusBadRequest, "a request is a JSON object, posted")
	case r.URL.Path == "/v1/embeddings":
		e.embeddings(w, body)
	case r.URL.Path == "/v1/rerank":
		e.rerank(w, body)
	default:
		refuse(w, http.StatusNotFound, "no such route: "+r.URL.Path)
	}
}

// refuse answers with an error shaped as the OpenAI API shapes one.
func refuse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "param": nil}})
}

func (e *Endpoint) embeddings(w http.ResponseWriter, body map[string]any) {
	e.mu.Lock()
	size, prompts := e.size, e.prompts
	e.mu.Unlock()
	inputs, ok := body["input"].([]any)
	if model, _ := body["model"].(string); !ok || model == "" {
		refuse(w, http.StatusBadRequest, "a request names a model, and its input is a list of texts")
		return
	}
	before, cut := "", 0
	if prompts != nil {
		// A server that marks the texts itself is strict about what it is
		// sent, as the real one is.
		allowed := []string{"dimensions", "encoding_format", "input", "model", "prompt"}
		var unknown []string
		for key := range body {
			if !slices.Contains(allowed, key) {
				unknown = append(unknown, key)
			}
		}
		sort.Strings(unknown)
		if len(unknown) > 0 {
			refuse(w, http.StatusBadRequest, fmt.Sprintf("unknown request keys ['%s']; allowed keys are ['%s']",
				strings.Join(unknown, "', '"), strings.Join(allowed, "', '")))
			return
		}
		name := "document"
		if given, there := body["prompt"]; there {
			if name, ok = given.(string); !ok {
				refuse(w, http.StatusBadRequest, "prompt must be a string")
				return
			}
		}
		if before, ok = prompts[name]; !ok {
			refuse(w, http.StatusBadRequest, "unknown prompt '"+name+"'")
			return
		}
		if asked, there := body["dimensions"]; there {
			n, isNumber := asked.(float64)
			if !isNumber || n < 1 || int(n) > size {
				refuse(w, http.StatusBadRequest, "dimensions must be a number up to the model's size")
				return
			}
			cut = int(n)
		}
	}
	data := make([]map[string]any, 0, len(inputs))
	for i, input := range inputs {
		text, _ := input.(string)
		vector := e.Vector(before + text)
		if cut > 0 {
			vector = unit(vector[:cut])
		}
		data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": vector})
	}
	w.Header().Set("Content-Type", "application/json")
	// More than Shelf reads of an answer, as real servers send: an object
	// of the server's own, and no usage.
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": body["model"], "data": data,
		"provenance": map[string]any{"served_by": "shelftest"}})
}

// Vector is the vector the endpoint gives a text as it was sent: one number
// for each of the endpoint's size, of length 1.
func (e *Endpoint) Vector(text string) []float32 {
	e.mu.Lock()
	size, same, only := e.size, maps.Clone(e.same), maps.Clone(e.only)
	e.mu.Unlock()
	vector := make([]float32, size)
	// A word is four numbers among the first of the vector, each added or
	// taken away: two words share all four as good as never, and a vector
	// that is cut short, as a real model's can be, still holds every word.
	width := min(size, wordsWidth)
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if as, ok := same[word]; ok {
			word = as
		}
		if word == "" || (len(only) > 0 && !only[word]) {
			continue // a word the "model" makes nothing of
		}
		hash := fnv.New64a()
		hash.Write([]byte(word))
		bits := hash.Sum64()
		for range 4 {
			at, sign := int(bits%uint64(width)), float32(1)
			if bits>>15&1 == 1 {
				sign = -1
			}
			vector[at] += sign
			bits >>= 16
		}
	}
	// The rest of the vector is the same for every text: it changes no
	// order, and makes a vector that was cut one that has to be made of
	// length 1 again.
	for at := width; at < size; at++ {
		vector[at] = 0.05
	}
	return unit(vector)
}

// wordsWidth is how many of a vector's first numbers hold the text's words.
const wordsWidth = 256

func unit(vector []float32) []float32 {
	var sum float64
	for _, x := range vector {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		vector[0], sum = 1, 1
	}
	for i, x := range vector {
		vector[i] = float32(float64(x) / math.Sqrt(sum))
	}
	return vector
}

func (e *Endpoint) rerank(w http.ResponseWriter, body map[string]any) {
	e.mu.Lock()
	rank := e.rank
	e.mu.Unlock()
	query, _ := body["query"].(string)
	list, ok := body["documents"].([]any)
	if model, _ := body["model"].(string); !ok || model == "" || query == "" {
		refuse(w, http.StatusBadRequest, "a request names a model and a query, and its documents are a list of texts")
		return
	}
	documents := make([]string, len(list))
	for i, item := range list {
		documents[i], _ = item.(string)
	}
	if rank == nil {
		rank = SharedWords
	}
	scores := rank(query, documents)
	results := make([]map[string]any, 0, len(documents))
	for i := range documents {
		results = append(results, map[string]any{"index": i, "relevance_score": scores[i]})
	}
	// In the order they were handed over: the order is Shelf's to make of
	// the scores.
	if top, isNumber := body["top_n"].(float64); isNumber && int(top) < len(results) {
		sort.SliceStable(results, func(i, j int) bool {
			return results[i]["relevance_score"].(float64) > results[j]["relevance_score"].(float64)
		})
		results = results[:int(top)]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": body["model"], "results": results})
}

// SharedWords scores each document by how many of the query's words it
// holds: the test endpoint's own way of ranking.
func SharedWords(query string, documents []string) []float64 {
	words := strings.Fields(strings.ToLower(query))
	scores := make([]float64, len(documents))
	for i, document := range documents {
		lower := strings.ToLower(document)
		for _, word := range words {
			if strings.Contains(lower, word) {
				scores[i]++
			}
		}
	}
	return scores
}
