//go:build unix

package shelf_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/settings"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// The variables that give TestTheRecipeAgainstARealEndpoint an endpoint to
// ask. They are the test's alone: Shelf reads no setting from its
// environment, and the test writes them into a settings file, as a learner
// would.
const (
	// realEndpointEnv is the address the endpoint serves the OpenAI API at,
	// such as http://127.0.0.1:8080/v1. The test is skipped without it.
	realEndpointEnv = "LAMPLIGHT_TEST_EMBEDDING_ENDPOINT"
	// realRecipeEnv names the Recipe to hold the endpoint to, which is how
	// the test is told who marks the texts: embeddinggemma-2, the default,
	// for a server that embeds a text as it is sent, such as llama.cpp's,
	// and embeddinggemma-2-prompts for one that applies the model's prompts
	// itself, by name.
	realRecipeEnv = "LAMPLIGHT_TEST_EMBEDDING_RECIPE"
	// realModelEnv is the name the endpoint knows the model by, when it is
	// not the Recipe's.
	realModelEnv = "LAMPLIGHT_TEST_EMBEDDING_MODEL"
	// realKeyEnv is the endpoint's key, when it takes one.
	realKeyEnv = "LAMPLIGHT_TEST_EMBEDDING_KEY"
	// realRerankEnv is the address of a reranking endpoint, and
	// realRerankModelEnv its model's name. Without the first, reranking is
	// not tested.
	realRerankEnv      = "LAMPLIGHT_TEST_RERANKING_ENDPOINT"
	realRerankModelEnv = "LAMPLIGHT_TEST_RERANKING_MODEL"
)

// asked is an endpoint to hold a Recipe to.
type asked struct {
	endpoint, recipe, model, key string
	rerank, rerankModel          string
}

// subjects are eight Passages on eight subjects, each with a question it
// answers that is put in other words: no keyword search finds the Passage
// by the question, and any model that embeds meaning does.
var subjects = []struct{ question, passage string }{
	{"How do green organisms produce nourishment from light?",
		"Plants make their own food. In their leaves, chlorophyll captures sunlight and uses its energy to turn water and carbon " +
			"dioxide into sugar, releasing oxygen as a by-product."},
	{"What procedure solves a system of linear equations step by step?",
		"Elimination subtracts multiples of one row from the rows below it until the matrix is upper triangular. The numbers left " +
			"on the diagonal are the pivots, and back substitution then finds each unknown in turn."},
	{"How do two computers agree to begin talking over a reliable network protocol?",
		"Before any data is sent, the two hosts exchange three segments: SYN, SYN-ACK and ACK. Each side learns the initial " +
			"sequence number of the other, and the connection is then established."},
	{"Which event marked the beginning of the uprising against the French monarchy?",
		"In July 1789 a crowd in Paris stormed the Bastille, a fortress used as a prison. The fall of this symbol of royal power " +
			"is remembered as the start of the Revolution in France."},
	{"What is a fast way to look something up in an ordered list?",
		"To find a value in a sorted array, compare it with the middle element and discard the half that cannot contain it. " +
			"Repeating this halves the remaining range each time, so the running time is logarithmic."},
	{"Why does bread made without commercial leavening still puff up and taste sour?",
		"A starter is a culture of wild yeast and bacteria kept alive with flour and water. Mixed into dough, it ferments slowly, " +
			"which makes the loaf rise and gives it a tangy flavour."},
	{"What explains why continents move and tremors happen?",
		"The outer shell of the Earth is broken into rigid plates that drift a few centimetres a year. Where two of them collide, " +
			"one may sink beneath the other, raising mountain ranges and causing earthquakes."},
	{"How does a runtime decide which allocations are safe to free automatically?",
		"A tracing collector starts from the roots, the variables a program can reach directly, and marks every object reachable " +
			"from them. Whatever is left unmarked can no longer be used, and its memory is reclaimed."},
}

// TestTheRecipeAgainstARealEndpoint holds a Recipe for EmbeddingGemma 2 to
// a real model. It runs only when it is given the address of an endpoint
// that serves one, on the maintainer's own computer or network:
//
//	LAMPLIGHT_TEST_EMBEDDING_ENDPOINT=http://127.0.0.1:8080/v1 \
//	    go test ./internal/shelf -run TestTheRecipeAgainstARealEndpoint -v
//
// with LAMPLIGHT_TEST_EMBEDDING_RECIPE=embeddinggemma-2-prompts for a
// server that applies the model's prompts itself, and the other variables
// above as the endpoint needs them. No other test asks an endpoint that is
// not the test's own.
func TestTheRecipeAgainstARealEndpoint(t *testing.T) {
	at := asked{
		endpoint: os.Getenv(realEndpointEnv), recipe: os.Getenv(realRecipeEnv), model: os.Getenv(realModelEnv), key: os.Getenv(realKeyEnv),
		rerank: os.Getenv(realRerankEnv), rerankModel: os.Getenv(realRerankModelEnv),
	}
	if at.endpoint == "" {
		t.Skipf("no endpoint to ask: set %s to the address of one that serves EmbeddingGemma 2, such as http://127.0.0.1:8080/v1", realEndpointEnv)
	}
	if at.recipe == "" {
		at.recipe = "embeddinggemma-2"
	}
	holdRecipeTo(t, at)
}

// The same checks against the test endpoint, in both of its ways: so that
// the test above, which nobody runs by accident, is one that runs.
func TestTheRecipeTestRunsAgainstATestEndpoint(t *testing.T) {
	// What the test endpoint's "model" has to be told that a real one
	// knows: which word of each question means which word of its Passage.
	// It makes something of those words and of the marks, and of nothing
	// else: a real model is not misled by what every sentence holds.
	same := []string{"nourishment", "food", "procedure", "elimination", "computers", "hosts", "uprising", "revolution",
		"ordered", "sorted", "leavening", "yeast", "tremors", "earthquakes", "allocations", "memory"}
	only := []string{"food", "elimination", "hosts", "revolution", "sorted", "yeast", "earthquakes", "memory", "task", "title"}
	t.Run("a server that embeds a text as it is sent", func(t *testing.T) {
		at := shelftest.NewEndpoint(t)
		at.Same(same...)
		at.Only(only...)
		// And its "reranker": a Passage answers a question whose word it
		// holds the meaning of.
		at.Rank(func(query string, documents []string) []float64 {
			scores := make([]float64, len(documents))
			for i, document := range documents {
				for n := 0; n < len(same); n += 2 {
					if strings.Contains(strings.ToLower(query), same[n]) && strings.Contains(strings.ToLower(document), same[n+1]) {
						scores[i]++
					}
				}
			}
			return scores
		})
		holdRecipeTo(t, asked{endpoint: at.URL, recipe: "embeddinggemma-2", key: testKey, rerank: at.URL, rerankModel: "test-reranker"})
		if got := at.AskedAt("/v1/embeddings"); len(got) == 0 || fmt.Sprint(got[0].Keys()) != "[input model]" || got[0].Body["model"] != "google/embeddinggemma-2" {
			t.Errorf("the endpoint was asked %+v", got)
		}
	})
	t.Run("a server that applies the model's prompts", func(t *testing.T) {
		at := shelftest.NewEndpoint(t)
		at.Same(same...)
		at.Only(only...)
		at.Prompts("query", "task: search result | query: ", "document", "title: none | text: ")
		holdRecipeTo(t, asked{endpoint: at.URL, recipe: "embeddinggemma-2-prompts", model: "served-under-another-name"})
		if got := at.AskedAt("/v1/embeddings"); len(got) == 0 || fmt.Sprint(got[0].Keys()) != "[dimensions input model prompt]" || got[0].Body["model"] != "served-under-another-name" {
			t.Errorf("the endpoint was asked %+v", got)
		}
	})
}

// holdRecipeTo writes a settings file that names the endpoint and the
// Recipe, reads it as study-shelf does, and checks what the endpoint makes
// of the Recipe's requests: that it takes them, that the vectors are the
// Recipe's size, that a question's vector is nearest the Passage that
// answers it, that the longest Passage the Recipe allows is taken, and that
// a Shelf set this way indexes a book and finds its Passages by meaning.
func holdRecipeTo(t *testing.T, at asked) {
	t.Helper()
	recipe, ok := settings.RecipeNamed(at.recipe)
	if !ok {
		t.Fatalf("%s names the Recipe %q, and Shelf has %v", realRecipeEnv, at.recipe, settings.Recipes())
	}
	// The settings file, as a learner writes it.
	home := t.TempDir()
	written := fmt.Sprintf("format = 1\n\n[embedding]\nendpoint = %q\nrecipe = %q\n", at.endpoint, at.recipe)
	if at.model != "" {
		written += fmt.Sprintf("model = %q\n", at.model)
	}
	if at.key != "" {
		written += fmt.Sprintf("key = %q\n", at.key)
	}
	if at.rerank != "" {
		written += fmt.Sprintf("\n[reranking]\nendpoint = %q\nmodel = %q\n", at.rerank, at.rerankModel)
		if at.key != "" {
			written += fmt.Sprintf("key = %q\n", at.key)
		}
	}
	config := filepath.Join(home, ".config", "lamplight")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, settings.FileName), []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := settings.Load(func(key string) string { return map[string]string{"HOME": home}[key] }, t.TempDir())
	if err != nil || read.Embedding == nil {
		t.Fatalf("the settings file was not read: %+v, %v", read, err)
	}
	read.Embedding.Agent = "study-shelf/test"
	embedder, err := endpoint.NewEmbedder(*read.Embedding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(embedder.Close)
	ctx := context.Background()

	// The Passages and the questions, as the Recipe has them asked for.
	var passages, questions []string
	for _, subject := range subjects {
		passages, questions = append(passages, subject.passage), append(questions, subject.question)
	}
	began := time.Now()
	documents, err := embedder.Documents(ctx, "A book of many subjects", passages, nil)
	if err != nil {
		t.Fatalf("the endpoint did not give the Passages' vectors as the Recipe %s asks for them: %v", at.recipe, err)
	}
	t.Logf("%d Passages were embedded in %s", len(passages), time.Since(began).Round(time.Millisecond))
	for n, vector := range documents {
		if len(vector) != recipe.Dimensions || math.Abs(length(vector)-1) > 1e-3 {
			t.Fatalf("the vector of Passage %d has %d numbers and the length %v, want the Recipe's %d and 1", n, len(vector), length(vector), recipe.Dimensions)
		}
	}
	right, slowest := 0, time.Duration(0)
	for n, question := range questions {
		began := time.Now()
		vector, err := embedder.Query(ctx, question)
		if err != nil {
			t.Fatalf("the endpoint did not give the vector of a question as the Recipe asks for it: %v", err)
		}
		slowest = max(slowest, time.Since(began))
		if len(vector) != recipe.Dimensions {
			t.Fatalf("the vector of a question has %d numbers, want %d", len(vector), recipe.Dimensions)
		}
		best, second := -1, -1
		for i := range documents {
			switch score := cosine(vector, documents[i]); {
			case best < 0 || score > cosine(vector, documents[best]):
				best, second = i, best
			case second < 0 || score > cosine(vector, documents[second]):
				second = i
			}
		}
		own := cosine(vector, documents[n])
		if best == n {
			right++
			t.Logf("question %d: its Passage is nearest, at %.3f, and the next is at %.3f", n+1, own, cosine(vector, documents[second]))
		} else {
			t.Errorf("question %d, %q: the nearest Passage is number %d, at %.3f, and its own is at %.3f", n+1, question, best+1, cosine(vector, documents[best]), own)
		}
	}
	t.Logf("%d of %d questions have their Passage nearest; the slowest question's vector took %s", right, len(questions), slowest.Round(time.Millisecond))

	// A question and a Passage are marked differently, and the endpoint
	// makes something of the difference: the same words as a question are
	// not the vector they are as a stretch of a document.
	asQuestion, err := embedder.Query(ctx, passages[0])
	if err != nil {
		t.Fatal(err)
	}
	if same := cosine(asQuestion, documents[0]); same > 0.9999 {
		t.Errorf("the same text marked as a question and as a document has the same vector (cosine %.5f): the endpoint makes nothing of "+
			"how the Recipe %s marks a text", same, at.recipe)
	} else {
		t.Logf("the same text as a question and as a document: cosine %.3f", same)
	}

	// The longest Passage the Recipe allows, a batch of them at once: the
	// endpoint takes it, so its context and its batch are large enough for
	// what Shelf sends.
	longest := strings.Repeat("The eigenvalues of a symmetric matrix are real, and its eigenvectors can be chosen orthonormal. ", 40)
	longest = longest[:cut.Longest(recipe.PassageSize)]
	batch := make([]string, endpoint.DefaultBatch)
	for i := range batch {
		batch[i] = fmt.Sprintf("%d %s", i, longest[4:])
	}
	began = time.Now()
	if _, err := embedder.Documents(ctx, "A book of long Passages", batch, nil); err != nil {
		t.Errorf("the endpoint does not take %d Passages of %d characters, the longest the Recipe allows, in one request: %v",
			len(batch), len(longest), err)
	} else {
		took := time.Since(began)
		t.Logf("%d Passages of %d characters were embedded in %s: %.1f Passages a second", len(batch), len(longest), took.Round(time.Millisecond),
			float64(len(batch))/took.Seconds())
	}

	// And a Shelf with these settings: a book is indexed with its vectors,
	// and each question finds its Passage first, by what it means.
	how := shelf.Settings{Embedder: embedder, PassageSize: read.PassageSize}
	if read.Reranking != nil {
		reranker, err := endpoint.NewReranker(*read.Reranking)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reranker.Close)
		how.Reranker = reranker
	}
	_, s := startWith(t, t.TempDir(), how)
	var pages [][]string
	for _, subject := range subjects {
		pages = append(pages, []string{subject.passage})
	}
	s.indexed("recipe", file(t, "subjects.a1", pdf(pages...)))
	for n, subject := range subjects {
		got := s.find("recipe", subject.question, 3)
		if fmt.Sprint(got.Search) != "[keyword embedding]" || got.Notice != "" || got.Reranked != (how.Reranker != nil) {
			t.Fatalf("question %d: the search was by %v, reranked %v, with the notice %q", n+1, got.Search, got.Reranked, got.Notice)
		}
		if len(got.Passages) == 0 || got.Passages[0].Location == nil || got.Passages[0].Location.First != n+1 {
			t.Errorf("question %d, %q: Shelf's first Passage is %q, want the one on page %d", n+1, subject.question, texts(got.Passages), n+1)
		}
	}
}

func length(vector []float32) float64 { return math.Sqrt(cosine(vector, vector)) }

// cosine is the sum of the products of two vectors' numbers: the cosine of
// the angle between them, for vectors of length 1.
func cosine(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
