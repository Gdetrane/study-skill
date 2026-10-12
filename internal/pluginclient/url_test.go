//go:build unix

package pluginclient_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
// advice, in good time. A message says where the service is, by its scheme
// and host, and repeats nothing else of the address: a path or a query may
// be a key.
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
	const at = "Knowledge base plugin remote does not answer as an MCP server at HOST ("
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
			[]string{at, "connection refused", "study speaks MCP revision 2025-11-25 over streamable HTTP",
				"study knowledge-base add remote --replace --url <URL> changes it"}},
		{"a web page answers", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>Welcome</body></html>"))
		}), false, pluginclient.CodeFailedPrecondition, []string{at}},
		{"something that is no JSON answers", serve(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("\x1b]0;gotcha\x07 not json"))
		}), false, pluginclient.CodeFailedPrecondition, []string{at}},
		{"not found", serve(http.NotFound), false, pluginclient.CodeFailedPrecondition, []string{at + "Not Found: 404 page not found)"}},
		// A service that refuses says why, and study repeats it: one that
		// speaks only a later revision of MCP says which.
		{"a service of a later MCP revision", serve(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Unsupported protocol version: 2025-11-25 (supported versions: 2026-07-28)", http.StatusBadRequest)
		}), false, pluginclient.CodeFailedPrecondition,
			[]string{at + "Bad Request: Unsupported protocol version: 2025-11-25 (supported versions: 2026-07-28))",
				"study speaks MCP revision 2025-11-25 over streamable HTTP. Check that the service is running, that it speaks that revision or an earlier one"}},
		// And one that repeats the address it was asked at does not have
		// study print the key in it.
		{"a service that repeats what it was asked", serve(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no route for POST "+r.URL.String()+" on http://"+r.Host+r.URL.String()+" (key s3cr3t-k3y-of-the-learner, in t0ken-in-the-path)",
				http.StatusUnauthorized)
		}), false, pluginclient.CodeFailedPrecondition, []string{at + "Unauthorized: no route for POST … on http://HOSTNAME… (key …, in …))"}},
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
		}), false, pluginclient.CodeFailedPrecondition, []string{at, "larger than a plugin's message may be"}},
		{"no answer", serve(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}), true, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin remote did not open an MCP session within 400ms. Check that the service at HOST is running"}},
		{"a newer contract", service(t, plugintest.Config{Results: map[string]string{"contract": `{"version": 7}`}}), false, pluginclient.CodeNewerFormat,
			[]string{"Knowledge base plugin remote speaks version 7 of Lamplight's contract, and this version of study knows version 1: upgrade study"}},
		{"another MCP server", service(t, plugintest.Config{Tools: []string{"status"}}), false, pluginclient.CodeFailedPrecondition,
			[]string{"it is an MCP server without the contract, index, cancel, search and passage tools"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			address := tc.address + "/t0ken-in-the-path/mcp?key=s3cr3t-k3y-of-the-learner&mode=fast"
			host := pluginclient.Address(address)
			if host != strings.TrimSuffix(tc.address, "/mcp") {
				t.Fatalf("Address(%s) = %s", address, host)
			}
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
				want = strings.ReplaceAll(strings.ReplaceAll(want, "HOSTNAME", strings.TrimPrefix(host, "http://")), "HOST", host)
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message lacks %q:\n%s", want, err)
				}
			}
			for _, secret := range []string{"s3cr3t", "t0ken", "key=", "mode=fast", "/mcp?"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the message repeats %q of the address, which may be a key: %s", secret, err)
				}
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

// What is shown of a registered address is its scheme and host.
func TestOnlyTheHostOfAnAddressIsShown(t *testing.T) {
	for registered, want := range map[string]string{
		"https://kb.example/mcp":                               "https://kb.example",
		"https://kb.example:8443/v1/t0ken/mcp?key=s3cr3t#frag": "https://kb.example:8443",
		"http://127.0.0.1:8080":                                "http://127.0.0.1:8080",
		"http://[::1]:8080/mcp":                                "http://[::1]:8080",
		"not an address":                                       "its address",
		"":                                                     "its address",
	} {
		if got := pluginclient.Address(registered); got != want {
			t.Errorf("Address(%q) = %q, want %q", registered, got, want)
		}
	}
	// Text a service wrote is shown without what may be a key in the
	// address, however it is written there.
	const registered = "https://kb.example:8443/v1/t0ken-of-the-learner/mcp?key=s3cr3t%2Fk3y&mode=fast"
	for text, want := range map[string]string{
		"unknown session for " + registered:                           "unknown session for https://kb.example:8443…",
		"no route for /v1/t0ken-of-the-learner/mcp":                   "no route for …",
		"the key s3cr3t%2Fk3y is not one of ours":                     "the key … is not one of ours",
		"the key s3cr3t/k3y is not one of ours":                       "the key … is not one of ours",
		"GET /v1/t0ken-of-the-learner/mcp?key=s3cr3t%2Fk3y&mode=fast": "GET …",
		"t0ken-of-the-learner has no collection c":                    "… has no collection c",
		"the collection has no Passage 7f3a":                          "the collection has no Passage 7f3a",
		"kb.example:8443 is busy":                                     "kb.example:8443 is busy",
	} {
		if got := pluginclient.Without(text, registered); got != want {
			t.Errorf("Without(%q) = %q, want %q", text, got, want)
		}
	}
	if got := pluginclient.Without("anything /at/all", ""); got != "anything /at/all" {
		t.Errorf("Without, for a command, = %q", got)
	}
}

// What a service says in an answer is shown without the address's key too:
// a refusal, a reason, the name it gives itself.
func TestAServiceThatRepeatsItsAddressInAnAnswer(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	var registered atomic.Pointer[string]
	echo := func(tool string, answer []byte, refused bool) []byte {
		if address := registered.Load(); address != nil {
			return []byte(strings.ReplaceAll(string(answer), "ADDRESS", *address))
		}
		return answer
	}
	address := service(t, plugintest.Config{Twist: echo,
		Refusals: map[string]string{"passage": "internal:cannot reach the index behind ADDRESS"},
		Results:  map[string]string{"status": `{"sources": [{"id": "a.1", "state": "failed", "reason": "ADDRESS answered 500"}]}`}})
	address = strings.TrimSuffix(address, "/mcp") + "/t0ken-in-the-path/mcp?key=s3cr3t-k3y-of-the-learner"
	registered.Store(&address)
	s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	host := pluginclient.Address(address)
	_, err = s.Passage(ctx, "c", "a")
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "failed in its passage tool: cannot reach the index behind "+host+"….") {
		t.Errorf("Passage = %v", err)
	}
	sources, serr := s.Status(ctx, "c")
	if serr != nil || len(sources) != 1 || sources[0].Reason != host+"… answered 500" {
		t.Fatalf("Status = %+v, %v", sources, serr)
	}
	for _, text := range []string{err.Error(), sources[0].Reason} {
		if strings.Contains(text, "s3cr3t") || strings.Contains(text, "t0ken") {
			t.Errorf("what the service wrote is shown with the address's key: %s", text)
		}
	}
}

// A service that sends study requests is hung up on at the first, as a
// command is stopped: over HTTP they come in the stream that answers a call.
func TestAServiceThatSendsRequestsIsHungUpOn(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	plugin := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	defer plugin.Close()
	var flooding atomic.Bool
	var sent atomic.Int64
	left := make(chan struct{}, 1)
	handler := plugin.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !flooding.Load() || r.Method != http.MethodPost {
			handler.ServeHTTP(w, r)
			return
		}
		// The call is answered with a stream of requests, without end.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for n := 0; ; n++ {
			if _, err := fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"service-%d\",\"method\":\"sampling/createMessage\"}\n\n", n); err != nil {
				break
			}
			w.(http.Flusher).Flush()
			sent.Add(1)
			if r.Context().Err() != nil {
				break
			}
		}
		select {
		case left <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp"), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	flooding.Store(true)
	before := heap()
	began := time.Now()
	_, err = s.Status(ctx, "c")
	if codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin remote sent study a request (sampling/createMessage), and study stopped it") {
		t.Errorf("Status = %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Status took %s to end: the first request ends the Connection, not the call's deadline", took)
	}
	if !s.Ended() {
		t.Error("the Connection has not ended")
	}
	// study hung up: the service's stream ends.
	select {
	case <-left:
	case <-time.After(10 * time.Second):
		t.Errorf("study still reads the stream of a service that sends it requests, %d so far", sent.Load())
	}
	if grown := heap() - before; grown > 64<<20 {
		t.Errorf("study holds %d MiB more after a service flooded it with requests", grown>>20)
	}
}

// Every request after the first names the MCP revision the session speaks,
// as the protocol asks, the notification that ends the handshake included.
func TestEveryRequestNamesTheRevision(t *testing.T) {
	c := plugintest.NewComputer(t)
	plugin := plugintest.New(plugintest.Config{Folder: t.TempDir()})
	defer plugin.Close()
	var mu sync.Mutex
	var named []string
	handler := plugin.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		named = append(named, r.Method+" "+r.Header.Get("Mcp-Protocol-Version"))
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	s, err := pluginclient.Start(context.Background(), c.Env(""), c.RegisterURL(t, "remote", server.URL+"/mcp"), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Status(context.Background(), "c"); err != nil {
		t.Errorf("Status = %v", err)
	}
	s.Close()
	mu.Lock()
	defer mu.Unlock()
	// initialize, the notification that it is done, tools/list, contract,
	// status, and the end of the session.
	if len(named) < 6 || named[0] != "POST " {
		t.Fatalf("the service was asked %q; want initialize first, without a revision, and five requests more", named)
	}
	for _, request := range named[1:] {
		if !strings.HasSuffix(request, " "+pluginclient.MCPVersion) {
			t.Errorf("a request after initialize names the revision %q, want %s: %q", request, pluginclient.MCPVersion, named)
		}
	}
}
