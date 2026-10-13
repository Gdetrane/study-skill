//go:build unix

package shelf_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// What Shelf says of a search that did less than it was set to is the
// contract's notice, which study shows the learner and hands to the
// learner's agent. So a notice holds nothing an endpoint's refusal may
// repeat of what is the learner's alone: not the key Shelf sent, not
// whatever else follows "Bearer", and of the endpoint's address no more than
// its host.
//
// Here both endpoints refuse a search, each at length, in words that repeat
// the key and the address they were asked at. The search is answered by
// keyword, in the order it had, and its one notice says both, on one line
// and within what a notice may hold.
func TestANoticeHoldsNoKeyAndNoMoreOfAnAddressThanItsHost(t *testing.T) {
	at := shelftest.NewEndpoint(t)
	_, s := startWith(t, t.TempDir(), shelf.Settings{Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at)})
	book := file(t, "book.a1", leaves("The first page speaks of functors.", "The second page speaks of functors too."))
	s.indexed("notice", book)
	at.Forget()
	at.Answer(func(w http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error": {"message": "the key %s is not one of ours.\nYou sent Authorization: %s to http://%s%s (POST %s), `+
			`and the gateway sent Bearer sk-gateway-0123456789abcdef on. %s"}}`,
			testKey, r.Header.Get("Authorization"), r.Host, r.URL.Path, r.URL.Path, strings.Repeat("Ask the one who runs it. ", 100))
		return true
	})
	got := s.find("notice", "functors", 5)
	if len(got.Passages) != 2 || fmt.Sprint(got.Search) != "[keyword]" || got.Reranked {
		t.Fatalf("with both endpoints refusing, the search finds %q by %v, reranked %v; want what keyword search finds, in its order",
			texts(got.Passages), got.Search, got.Reranked)
	}
	if asked := at.Asked(); len(asked) != 2 || asked[0].Path != "/v1/embeddings" || asked[1].Path != "/v1/rerank" || asked[0].Key != testKey {
		t.Fatalf("the endpoints were asked %+v; want each once, with the key, or this test holds the notice to nothing", asked)
	}
	for _, word := range []string{
		"These Passages were found by keyword alone: the embedding endpoint " + at.Host + " answered 401 Unauthorized: the key [the key] is not one of ours. " +
			"You sent Authorization: Bearer [the key] to [the address] (POST [the address]), and the gateway sent Bearer [the key] on.",
		"These Passages are in the order the search found them, and not reranked: the reranking endpoint " + at.Host + " answered 401 Unauthorized: ",
		"Check the key in Shelf's settings file.",
	} {
		if !strings.Contains(got.Notice, word) {
			t.Errorf("the notice does not say %q:\n%s", word, got.Notice)
		}
	}
	for _, not := range []string{testKey, "sk-gateway", "/v1", "http://", "embeddings", "/rerank", "\n"} {
		if strings.Contains(got.Notice, not) {
			t.Errorf("the notice holds %q, of a key or of more of an address than its host:\n%s", not, got.Notice)
		}
	}
	// Two endpoints' words, each cut where Shelf cuts a server's, are one
	// notice of a size study keeps whole.
	if n := len(got.Notice); n > plugincontract.MaxNoticeBytes || n < 700 {
		t.Errorf("the notice has %d bytes; want what both endpoints said, each cut short, in the %d a notice may have:\n%s",
			n, plugincontract.MaxNoticeBytes, got.Notice)
	}
}
