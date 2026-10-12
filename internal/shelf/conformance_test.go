//go:build unix

package shelf_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	plugintest.Conformance(t, plugintest.Target{
		// The suite serves its web pages from this computer, which a Shelf
		// fetches nothing from unless it is registered to.
		Connect:   plugintest.AsCommand(plugintest.NewComputer(t), shelftest.Shelf(t), "--allow-loopback"),
		Restarts:  true,
		WriteFile: shelftest.WritePDF,
		Extension: ".pdf",
	})
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
