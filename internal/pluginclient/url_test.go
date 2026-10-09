//go:build unix

package pluginclient_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// service runs the test plugin as a service on this computer, as a learner
// would run one, and returns the address of its MCP endpoint. It answers on
// the loopback interface only.
func service(t *testing.T, cfg plugintest.Config) string {
	t.Helper()
	cfg.Folder = t.TempDir()
	plugin := plugintest.New(cfg)
	server := httptest.NewServer(plugin.Handler())
	t.Cleanup(func() {
		server.Close()
		plugin.Close()
	})
	return server.URL + "/mcp"
}

// A plugin registered by URL is reached over streamable HTTP, and speaks the
// same contract. It gets no folder: it is a service, and keeps its own.
func TestAURLIsReachedOverStreamableHTTP(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	plugin := c.RegisterURL(t, "remote", service(t, plugintest.Config{Name: "a-service", Version: "3.2.1"}))
	s, err := pluginclient.Start(ctx, c.Env(""), plugin, quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if s.Contract().Version != 1 || s.Server() != (pluginclient.Server{Name: "a-service", Version: "3.2.1"}) || s.DataFolder() != "" {
		t.Errorf("Contract = %+v, Server = %+v, DataFolder = %q", s.Contract(), s.Server(), s.DataFolder())
	}
	if _, err := os.Stat(filepath.Join(c.StudyHome, ".lamplight")); err == nil {
		t.Error("study made a folder in the Study home for a plugin reached by URL")
	}
	source := book(t, c)
	if _, err := s.Index(ctx, "linear-algebra", source); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if done := indexed(t, s, "linear-algebra", source.ID); done.State != plugincontract.StateIndexed || done.Hash != source.Hash {
		t.Errorf("Status = %+v", done)
	}
	found, err := s.Search(ctx, "linear-algebra", "inverse matrix identity", 5, nil)
	if err != nil || len(found) != 1 || found[0].Text != "A matrix times its inverse is the identity." {
		t.Fatalf("Search = %+v, %v", found, err)
	}
	if same, err := s.Passage(ctx, "linear-algebra", found[0].ID); err != nil || same.Text != found[0].Text {
		t.Errorf("Passage = %+v, %v", same, err)
	}
	if _, err := s.Passage(ctx, "linear-algebra", "nothing"); codeOf(err) != pluginclient.CodeNotFound {
		t.Errorf("Passage of an id that is none = %v; want not_found", err)
	}
	if stopped, err := s.Cancel(ctx, "linear-algebra", source.ID); err != nil || stopped {
		t.Errorf("Cancel = %v, %v", stopped, err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if _, err := s.Status(ctx, "linear-algebra"); codeOf(err) != pluginclient.CodeFailedPrecondition {
		t.Errorf("Status after Close = %v", err)
	}
}

// An address that does not answer as a plugin gives a documented code with
// advice, in good time, and no message repeats the address: it may hold a
// key.
func TestAnAddressThatIsNoPluginIsRefused(t *testing.T) {
	// An address nothing listens at: the port was free a moment ago.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	nobody := "http://" + listener.Addr().String()
	listener.Close()

	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	// What is still waiting when the test ends is let go, so the servers
	// can close.
	release := make(chan struct{})
	defer close(release)

	serve := func(handler http.HandlerFunc) string {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server.URL
	}
	for _, tc := range []struct {
		name    string
		address string
		// silent says that the service answers nothing, so that it is given
		// a short deadline; the others have time to answer wrongly.
		silent bool
		code   string
		want   []string
	}{
		{"nothing answers", nobody, false, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin remote does not answer as an MCP server at its address (", "connection refused",
				"study knowledge-base add remote --replace --url <URL> changes it"}},
		{"a web page answers", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>Welcome</body></html>"))
		}), false, pluginclient.CodeFailedPrecondition, []string{"does not answer as an MCP server at its address ("}},
		{"something that is no JSON answers", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("\x1b]0;gotcha\x07 not json"))
		}), false, pluginclient.CodeFailedPrecondition, []string{"does not answer as an MCP server at its address ("}},
		{"not found", serve(http.NotFound), false, pluginclient.CodeFailedPrecondition, []string{"does not answer as an MCP server at its address (", "Not Found"}},
		{"a redirect", serve(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/mcp", http.StatusTemporaryRedirect)
		}), false, pluginclient.CodeFailedPrecondition, []string{"the address answered with a redirect to another one"}},
		{"an answer without end", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			chunk := []byte(strings.Repeat("x", 1<<20))
			for range plugincontract.MaxMessageBytes>>20 + 4 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}), false, pluginclient.CodeFailedPrecondition, []string{"does not answer as an MCP server at its address (", "larger than a plugin's message may be"}},
		{"no answer", serve(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}), true, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin remote did not open an MCP session within 400ms. Check that the service at its address is running"}},
		{"a newer contract", service(t, plugintest.Config{Results: map[string]string{"contract": `{"version": 7}`}}), false, pluginclient.CodeNewerFormat,
			[]string{"Knowledge base plugin remote speaks version 7 of Lamplight's contract, and this version of study knows version 1: upgrade study"}},
		{"another MCP server", service(t, plugintest.Config{Tools: []string{"status"}}), false, pluginclient.CodeFailedPrecondition,
			[]string{"it is an MCP server without the contract, index, cancel, search and passage tools"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			address := tc.address + "?key=s3cr3t-k3y-of-the-learner"
			opts := quick(c)
			if tc.silent {
				opts = impatient(c)
			}
			began := time.Now()
			s, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", address), opts)
			if err == nil {
				s.Close()
				t.Fatal("Start succeeded")
			}
			if took := time.Since(began); took > 8*time.Second {
				t.Errorf("Start took %s to give up", took)
			}
			if codeOf(err) != tc.code {
				t.Errorf("code = %q, want %q: %v", codeOf(err), tc.code, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message lacks %q:\n%s", want, err)
				}
			}
			if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), tc.address) {
				t.Errorf("the message repeats the address: %s", err)
			}
			safe(t, err.Error())
		})
	}
	if elsewhere.Load() != 0 {
		t.Errorf("study followed a redirect: the other address was asked %d times", elsewhere.Load())
	}
}

// A service that stops answering in the middle of a Connection ends the call
// in time, and is not asked again on that Connection.
func TestAServiceThatStopsAnswering(t *testing.T) {
	c := plugintest.NewComputer(t)
	release := make(chan struct{})
	defer close(release)
	address := service(t, plugintest.Config{Before: func(ctx context.Context, tool string) {
		if tool == plugincontract.ToolSearch {
			select {
			case <-ctx.Done():
			case <-release:
			}
		}
	}})
	opts := impatient(c)
	opts.StartTimeout = 10 * time.Second
	s, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", address), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	began := time.Now()
	_, err = s.Search(context.Background(), "c", "inverse", 5, nil)
	if codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin remote did not answer its search tool within 400ms. Check that the service") {
		t.Errorf("Search = %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Search took %s to give up", took)
	}
	if _, err := s.Status(context.Background(), "c"); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "can no longer be asked: it did not answer in time") {
		t.Errorf("Status afterwards = %v", err)
	}

	// Stopping the request stops the call, and leaves the Connection open.
	s2, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ctx, stop := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, stop)
	if _, err := s2.Search(ctx, "c", "inverse", 5, nil); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Search with the request stopped = %v; want canceled", err)
	}
	if _, err := s2.Status(context.Background(), "c"); err != nil {
		t.Errorf("Status afterwards = %v", err)
	}
}

// A service that stops answering anything at all, the end of the session
// included, is given its deadline once and not waited for again.
func TestAServiceThatGoesSilent(t *testing.T) {
	c := plugintest.NewComputer(t)
	plugin := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	defer plugin.Close()
	var silent atomic.Bool
	over := make(chan struct{})
	handler := plugin.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if silent.Load() {
			<-over
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer close(over)
	opts := impatient(c)
	opts.StartTimeout = 10 * time.Second
	s, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp"), opts)
	if err != nil {
		t.Fatal(err)
	}
	silent.Store(true)
	began := time.Now()
	if _, err := s.Status(context.Background(), "c"); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "did not answer its status tool within 400ms") {
		t.Errorf("Status = %v", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("Status took %s: the deadline was 400ms, and a service that missed it is not waited for again", took)
	}
	began = time.Now()
	s.Close()
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Close took %s on a Connection that was given up already", took)
	}
}
