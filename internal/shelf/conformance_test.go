//go:build unix

package shelf_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/web"
)

func TestMain(m *testing.M) { shelftest.Main(m) }

// The tests that run the go command, to build study-shelf, run the same
// under a caller's git environment and write nothing through it.
func TestTheTestsIgnoreTheCallersGitEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the study-shelf binary")
	}
	stray := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^(TestStudyLinksNothingOfShelf|TestTheBuiltStudyShelfRuns)$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(stray, "repo.git"),
		"GIT_WORK_TREE="+filepath.Join(stray, "tree"), "GIT_INDEX_FILE="+filepath.Join(stray, "index"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the tests under a caller's git environment: %v\n%s", err, out)
	}
	if left, _ := os.ReadDir(stray); len(left) != 0 {
		t.Errorf("git wrote where the caller's environment pointed: %v", left)
	}
}

// study-shelf, built and started exactly as study starts a registered
// plugin, is held to everything the contract promises: with PDFs for its
// files, and the suite's own web pages, which it serves from this computer.
func TestShelfConformsAsStudyStartsIt(t *testing.T) {
	// Beside the same suite with endpoints set, below, and the test that
	// kills study-shelf: each has a computer of its own and spends its time
	// waiting for programs it started, a minute of it.
	t.Parallel()
	plugintest.Conformance(t, plugintest.Target{
		// The suite serves its web pages from this computer, which a Shelf
		// fetches nothing from unless it is registered to.
		Connect:   plugintest.AsCommand(plugintest.NewComputer(t), shelftest.Shelf(t), "--allow-loopback"),
		Restarts:  true,
		WriteFile: shelftest.WritePDF,
		Extension: ".pdf",
	})
}

// The same, of a study-shelf whose settings file names an embedding
// endpoint and a reranking endpoint: every Source is indexed with its
// vectors, and every search asks for the question's and has its Passages
// reranked. What the contract promises does not depend on how Shelf
// searches.
func TestShelfConformsWithItsEndpointsSet(t *testing.T) {
	t.Parallel()
	computer := plugintest.NewComputer(t)
	at := shelftest.NewEndpoint(t)
	writeSettings(t, computer, at, "embeddinggemma-2", true)
	plugintest.Conformance(t, plugintest.Target{
		Connect:   plugintest.AsCommand(computer, shelftest.Shelf(t), "--allow-loopback"),
		Restarts:  true,
		WriteFile: shelftest.WritePDF,
		Extension: ".pdf",
	})
	// The suite did not pass by Shelf's leaving its endpoints alone.
	heldToSettings(t, at, "google/embeddinggemma-2", "[input model]")
	if len(at.AskedAt("/v1/rerank")) == 0 {
		t.Error("the reranking endpoint was never asked")
	}
}

// heldToSettings checks what a test endpoint received from a study-shelf:
// Passages and questions, each marked as the Recipe marks it, for the
// model, with the key, and with no key of a request but the ones given.
func heldToSettings(t *testing.T, at *shelftest.Endpoint, model, keys string) {
	t.Helper()
	documents, questions := 0, 0
	for _, a := range at.AskedAt("/v1/embeddings") {
		if a.Key != testKey || a.Body["model"] != model || fmt.Sprint(a.Keys()) != keys {
			t.Fatalf("the embedding endpoint was asked %v with the key %q; want the model %s and the keys %s", a.Body, a.Key, model, keys)
		}
		for _, text := range a.Texts() {
			switch {
			case strings.HasPrefix(text, "title: ") && strings.Contains(text, " | text: "):
				documents++
			case strings.HasPrefix(text, "task: search result | query: "):
				questions++
			default:
				t.Fatalf("the embedding endpoint was sent %q, which is marked neither as a document nor as a question", text)
			}
		}
	}
	if documents == 0 || questions == 0 {
		t.Errorf("the embedding endpoint was sent %d Passages and %d questions; want some of each", documents, questions)
	}
}

// writeSettings writes Shelf's settings file on a computer, as a learner
// would: a Recipe, the test endpoint for it, the key, and the same endpoint
// to rerank when reranking says so.
func writeSettings(t *testing.T, computer *plugintest.Computer, at *shelftest.Endpoint, recipe string, reranking bool) string {
	t.Helper()
	text := fmt.Sprintf("# Shelf's settings, written by a test.\nformat = 1\n\n[embedding]\nendpoint = %q\nrecipe = %q\nkey = %q\n", at.URL, recipe, testKey)
	if reranking {
		text += fmt.Sprintf("\n[reranking]\nendpoint = %q\nmodel = \"test-reranker\"\nkey = %q\n", at.URL, testKey)
	}
	path := filepath.Join(computer.ConfigDir, "shelf.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The same suite against a Shelf in this test's own process, with its
// endpoints set: the race detector watches Shelf index with vectors and
// search by them, from several sessions at once.
func TestShelfConformsInOneProcessWithItsEndpointsSet(t *testing.T) {
	warm(t)
	at := shelftest.NewEndpoint(t)
	plugin := shelf.New(shelf.Options{Folder: t.TempDir(), Settings: shelf.Settings{
		Fetch: web.Limits{Loopback: true}, Embedder: embedderAt(t, at), Reranker: rerankerAt(t, at),
	}})
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return plugin.Server() }, nil))
	t.Cleanup(func() {
		server.Close()
		plugin.Close()
	})
	plugintest.Conformance(t, plugintest.Target{
		Connect:   plugintest.AsService(server.URL + "/mcp"),
		WriteFile: shelftest.WritePDF,
		Extension: ".pdf",
	})
	heldToSettings(t, at, "test-embedder", "[input model]")
	if len(at.AskedAt("/v1/rerank")) == 0 {
		t.Error("the reranking endpoint was never asked")
	}
}

// The same suite against a Shelf in this test's own process, reached over
// HTTP: every session reaches one Shelf, so what the suite does at once it
// does to one index, and the race detector watches Shelf do it. study-shelf
// itself serves no address.
func TestShelfConformsInOneProcess(t *testing.T) {
	// The suite gives a plugin half a minute to index a few pages, and the
	// race detector makes compiling the PDF reader take longer than that.
	warm(t)
	plugin := shelf.New(shelf.Options{Folder: t.TempDir(), Settings: shelf.Settings{Fetch: web.Limits{Loopback: true}}})
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return plugin.Server() }, nil))
	t.Cleanup(func() {
		server.Close()
		plugin.Close()
	})
	plugintest.Conformance(t, plugintest.Target{
		Connect:   plugintest.AsService(server.URL + "/mcp"),
		WriteFile: shelftest.WritePDF,
		Extension: ".pdf",
	})
}
