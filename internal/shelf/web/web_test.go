package web_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/web"
)

// limits are small enough for a test to reach each of them.
var limits = web.Limits{Timeout: 2 * time.Second, MaxBytes: 64 << 10, MaxRedirects: 3}

// serve runs a web server on this computer's loopback interface for a test.
func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// fetch fetches an address within the test's limits.
func fetch(t *testing.T, address string) (web.Page, error) {
	t.Helper()
	return web.NewFetcher(limits, "study-shelf/test").Fetch(context.Background(), address)
}

// problem checks that a fetch failed with a reason for the learner that
// holds each of the words given, and returns the reason.
func problem(t *testing.T, err error, words ...string) string {
	t.Helper()
	var p *web.Problem
	if !errors.As(err, &p) {
		t.Fatalf("the fetch gave %v, want a reason for the learner", err)
	}
	for _, word := range words {
		if !strings.Contains(p.Reason, word) {
			t.Errorf("the reason does not say %q: %s", word, p.Reason)
		}
	}
	return p.Reason
}

func words(text string) string { return strings.Join(strings.Fields(text), " ") }

const article = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Go Concurrency Patterns: Context</title>
<style>p { color: red }</style><script>document.write("written by a script")</script></head>
<body>
<nav><a href="/">Home</a> <a href="/blog">The blog</a> <a href="/tos">Terms of service</a></nav>
<article>
<h1>Go Concurrency Patterns: Context</h1>
<h2>Introduction</h2>
<p>In Go servers, each incoming request is handled in its own goroutine. Request handlers often start additional goroutines to
access backends such as databases and RPC services. The set of goroutines working on a request typically needs access to
request-specific values.</p>
<p>At Google, we developed a <code>context</code> package that makes it easy to pass request-scoped values, cancellation
signals, and deadlines across API boundaries to all the goroutines involved in handling a request.</p>
<h2>Context</h2>
<p>The core of the <code>context</code> package is the <code>Context</code> type:</p>
<pre><code>type Context interface {
    Done() &lt;-chan struct{}
}</code></pre>
<p style="display:none">Hidden from every reader.</p>
</article>
<footer>Copyright the authors. <a href="/privacy">Privacy policy</a></footer>
</body></html>`

func TestAPageIsFetchedAndItsReadableTextTaken(t *testing.T) {
	var asked atomic.Pointer[http.Request]
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, article)
	})
	page, err := fetch(t, server.URL+"/blog/context?utm=1")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(article))
	if want := "sha256:" + hex.EncodeToString(sum[:]); page.Hash != want {
		t.Errorf("the hash is %s, want the one of the bytes that were sent, %s", page.Hash, want)
	}
	text := words(page.Text)
	for _, want := range []string{
		"In Go servers, each incoming request is handled in its own goroutine.",
		"At Google, we developed a context package that makes it easy to pass request-scoped values",
		"The core of the context package is the Context type:",
		"type Context interface { Done() <-chan struct{} }",
		"Introduction",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text lacks %q:\n%s", want, page.Text)
		}
	}
	// What surrounds the article is not what the page says, and neither is
	// its title, a script, a style or what the page hides.
	for _, not := range []string{"Terms of service", "Privacy policy", "The blog", "written by a script", "color: red", "Hidden from every reader",
		"Go Concurrency Patterns: Context"} {
		if strings.Contains(text, not) {
			t.Errorf("the text holds %q, which is not the article's:\n%s", not, page.Text)
		}
	}
	// Paragraphs are divided by an empty line, and code keeps its lines.
	if !strings.Contains(page.Text, "request-specific values.\n\nAt Google") || !strings.Contains(page.Text, "interface {\n    Done()") {
		t.Errorf("the text does not keep the page's paragraphs and lines:\n%q", page.Text)
	}
	r := asked.Load()
	if r.Method != http.MethodGet || r.URL.RequestURI() != "/blog/context?utm=1" || r.UserAgent() != "study-shelf/test" ||
		!strings.Contains(r.Header.Get("Accept"), "text/html") {
		t.Errorf("the request was %s %s as %q, accepting %q", r.Method, r.URL.RequestURI(), r.UserAgent(), r.Header.Get("Accept"))
	}
}

// A page that is a title, a heading that repeats it and a few lines, as the
// conformance suite's is: the lines are its text, and the title is not.
func TestAShortPageIsItsParagraphs(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html>\n<html><head><title>A page for a test</title></head><body>\n<h1>A page for a test</h1>\n"+
			"<p>This page was written to test a plugin.</p>\n<p>The marker zqabc stands on this page &amp; nowhere else.</p>\n</body></html>\n")
	})
	page, err := fetch(t, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := page.Text, "This page was written to test a plugin.\n\nThe marker zqabc stands on this page & nowhere else."; got != want {
		t.Errorf("the text is %q, want %q", got, want)
	}
}

func TestPagesInOtherShapes(t *testing.T) {
	const latin1 = "Le th\xE9or\xE8me de Pythagore est d\xE9montr\xE9 \xE0 la page suivante."
	const want = "Le th\u00E9or\u00E8me de Pythagore est d\u00E9montr\u00E9 \u00E0 la page suivante."
	zipped := func(body string) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte(body))
		_ = zw.Close()
		return b.Bytes()
	}
	for name, tc := range map[string]struct {
		contentType, encoding string
		body                  []byte
		want                  string
	}{
		"plain text":                             {"text/plain; charset=utf-8", "", []byte("Elimination produces\nan upper triangular system.\n\nA second paragraph."), "Elimination produces\nan upper triangular system.\n\nA second paragraph."},
		"plain text in another encoding":         {"text/plain; charset=iso-8859-1", "", []byte(latin1), want},
		"HTML in the encoding its header names":  {"text/html; charset=ISO-8859-1", "", []byte("<html><body><p>" + latin1 + "</p></body></html>"), want},
		"HTML in the encoding the page names":    {"text/html", "", []byte(`<html><head><meta charset="windows-1252"></head><body><p>` + latin1 + "</p></body></html>"), want},
		"XHTML":                                  {"application/xhtml+xml; charset=utf-8", "", []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>An XHTML page says this.</p></body></html>`), "An XHTML page says this."},
		"a server that does not say what it is":  {"", "", []byte("<!doctype html><html><body><p>Nobody said this was HTML.</p></body></html>"), "Nobody said this was HTML."},
		"a transfer that is compressed":          {"text/html; charset=utf-8", "gzip", zipped("<html><body><p>This page travelled compressed.</p></body></html>"), "This page travelled compressed."},
		"a page with no article to find in it":   {"text/html; charset=utf-8", "", []byte("<html><body><table><tr><td>pivot</td><td>1</td></tr><tr><td>rank</td><td>3</td></tr></table></body></html>"), "pivot 1 rank 3"},
		"letters a browser would decode by name": {"text/html; charset=utf-8", "", []byte("<html><body><p>Caf&eacute; &lt;au lait&gt; &amp; cr&egrave;me.</p></body></html>"), "Caf\u00E9 <au lait> & cr\u00E8me."},
	} {
		t.Run(name, func(t *testing.T) {
			server := serve(t, func(w http.ResponseWriter, r *http.Request) {
				// A header that is set to nothing is not sent.
				w.Header()["Content-Type"] = nil
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				_, _ = w.Write(tc.body)
			})
			page, err := fetch(t, server.URL+"/page")
			if err != nil {
				t.Fatal(err)
			}
			if got := words(page.Text); got != words(tc.want) {
				t.Errorf("the text is %q, want %q", got, words(tc.want))
			}
		})
	}
}

func TestWhatCannotBeFetchedIsSaid(t *testing.T) {
	big := strings.Repeat("<p>A paragraph that is said over and over again, to no end.</p>\n", 2000) // about 128 KiB
	t.Run("a page that is not there", func(t *testing.T) {
		server := serve(t, http.NotFound)
		_, err := fetch(t, server.URL+"/missing?key=a-secret-of-the-learners")
		reason := problem(t, err, "answered 404 Not Found", strings.TrimPrefix(server.URL, "http://"))
		if strings.Contains(reason, "a-secret") || strings.Contains(reason, "/missing") {
			t.Errorf("the reason repeats the address's path or query, which may hold a key: %s", reason)
		}
	})
	t.Run("a server that fails", func(t *testing.T) {
		server := serve(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
		_, err := fetch(t, server.URL)
		problem(t, err, "answered 503")
	})
	t.Run("nothing answers", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := "http://" + listener.Addr().String() + "/page?key=a-secret"
		listener.Close()
		_, err = fetch(t, address)
		if reason := problem(t, err, "could not be reached"); strings.Contains(reason, "a-secret") {
			t.Errorf("the reason repeats the address's query: %s", reason)
		}
	})
	t.Run("a page larger than the most that is fetched", func(t *testing.T) {
		sent := 0
		server := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			sent, _ = fmt.Fprint(w, "<html><body>", big, "</body></html>")
		})
		_, err := fetch(t, server.URL)
		problem(t, err, "larger than")
		_ = sent
	})
	t.Run("a page that expands without end", func(t *testing.T) {
		// A megabyte of one letter is a kilobyte on the wire.
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(bytes.Repeat([]byte("a"), 1<<20))
		_ = zw.Close()
		server := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(b.Bytes())
		})
		_, err := fetch(t, server.URL)
		problem(t, err, "larger than")
	})
	t.Run("a server that never sends the page", func(t *testing.T) {
		release := make(chan struct{})
		server := serve(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		defer close(release)
		began := time.Now()
		_, err := fetch(t, server.URL)
		problem(t, err, "did not send the page within 2s")
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("giving up took %s", took)
		}
	})
	t.Run("a server that sends the page without end", func(t *testing.T) {
		server := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			for r.Context().Err() == nil {
				if _, err := w.Write([]byte("a")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(5 * time.Millisecond)
			}
		})
		began := time.Now()
		_, err := fetch(t, server.URL)
		problem(t, err, "did not send the page within 2s")
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("giving up took %s", took)
		}
	})
	for name, tc := range map[string]struct{ contentType, body, want string }{
		"a PDF":                  {"application/pdf", "%PDF-1.7\n", "sent application/pdf"},
		"a picture":              {"image/png", "\x89PNG\r\n\x1a\n", "sent image/png"},
		"bytes that are no text": {"", "\x00\x01\x02\x03\xFF\xFE\x00\x00\x12\x34", "sent application/octet-stream"},
		"JSON":                   {"application/json", `{"text": "not a page"}`, "sent application/json"},
	} {
		t.Run(name, func(t *testing.T) {
			server := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header()["Content-Type"] = nil
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				fmt.Fprint(w, tc.body)
			})
			_, err := fetch(t, server.URL)
			problem(t, err, tc.want, "HTML or plain text")
		})
	}
	t.Run("a page that shows nothing without its script", func(t *testing.T) {
		server := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>An app</title></head><body><div id="root"></div><script src="/app.js"></script></body></html>`)
		})
		_, err := fetch(t, server.URL)
		problem(t, err, "no text to read")
	})
}

func TestRedirectsAreFollowedAFewTimesAndOnlyToWebPages(t *testing.T) {
	var hops atomic.Int64
	var server *httptest.Server
	server = serve(t, func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		switch r.URL.Path {
		case "/once", "/twice":
			http.Redirect(w, r, map[string]string{"/once": "/page", "/twice": "/once"}[r.URL.Path], http.StatusFound)
		case "/around":
			http.Redirect(w, r, "/around", http.StatusFound)
		case "/to-a-file":
			http.Redirect(w, r, "file:///etc/hostname", http.StatusFound)
		case "/to-a-password":
			http.Redirect(w, r, "http://ada:secret@"+strings.TrimPrefix(server.URL, "http://")+"/page", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "The page a redirect leads to.")
		}
	})
	for _, from := range []string{"/once", "/twice"} {
		if page, err := fetch(t, server.URL+from); err != nil || page.Text != "The page a redirect leads to." {
			t.Errorf("%s: %q, %v", from, page.Text, err)
		}
	}
	hops.Store(0)
	_, err := fetch(t, server.URL+"/around")
	problem(t, err, "redirects more than 3 times")
	if n := hops.Load(); n > 5 {
		t.Errorf("an address that redirects to itself was asked %d times", n)
	}
	_, err = fetch(t, server.URL+"/to-a-file")
	problem(t, err, "redirects to one that is no web page")
	_, err = fetch(t, server.URL+"/to-a-password")
	if reason := problem(t, err, "redirects to one that is no web page"); strings.Contains(reason, "secret") {
		t.Errorf("the reason repeats a password: %s", reason)
	}
}

func TestOnlyWebAddressesAreFetched(t *testing.T) {
	for _, address := range []string{"file:///etc/hostname", "ftp://example.org/book.pdf", "https://ada:secret@example.org/", "http:///page",
		"example.org/page", "", "javascript:alert(1)", "http://[::1", "gopher://example.org/"} {
		_, err := fetch(t, address)
		if reason := problem(t, err); strings.Contains(reason, "secret") {
			t.Errorf("the reason for %q repeats a password: %s", address, reason)
		}
	}
}

func TestAFetchStopsWhenItIsToldTo(t *testing.T) {
	release := make(chan struct{})
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	began := time.Now()
	_, err := web.NewFetcher(limits, "study-shelf/test").Fetch(ctx, server.URL)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a fetch that was stopped gave %v, want the stopping", err)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("stopping took %s", took)
	}
}
