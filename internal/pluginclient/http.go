package pluginclient

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// errRedirect is what a plugin's address gets for answering with a redirect.
var errRedirect = errors.New("the address answered with a redirect to another one")

// errTooLarge is what a plugin gets for an answer over the limit.
var errTooLarge = errors.New("the answer is larger than a plugin's message may be")

// protocolHeader is the header in which an MCP client says which revision
// of the protocol a session speaks.
const protocolHeader = "Mcp-Protocol-Version"

// urlTransport reaches a plugin registered by URL over streamable HTTP.
//
// The SDK's transport is used as it is, with an HTTP client that holds a
// plugin to what study expects of one:
//
//   - A redirect is not followed. The learner registered one address;
//     following a redirect would send their queries and their files' paths
//     wherever the service at that address says.
//   - An answer is read up to the size a message may have and no further,
//     so a broken service cannot make study hold more. The SDK bounds one
//     server-sent event itself; a plain JSON answer it reads whole.
//   - No stream is kept open for messages the plugin starts, and no request
//     is tried again: study asks, the plugin answers, and each call has its
//     own deadline.
//
// The learner's proxy settings apply, as to any program of theirs.
func urlTransport(address string) (mcp.Transport, *boundedTransport) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	bounded := &boundedTransport{base: base, max: plugincontract.MaxMessageBytes}
	return &mcp.StreamableClientTransport{
		Endpoint:             address,
		HTTPClient:           &http.Client{Transport: bounded},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
		MaxEventSize:         plugincontract.MaxMessageBytes,
	}, bounded
}

// boundedTransport refuses redirects, bounds what is read of an answer, and
// keeps what a service said when it refused a request.
type boundedTransport struct {
	base *http.Transport
	max  int64

	mu sync.Mutex
	// protocol is the MCP revision the session speaks, once it is known.
	protocol string
	// refusal is what the service last answered a request it refused with:
	// the status, and the start of what it wrote.
	refusal string
}

// speaks records the MCP revision the session came to speak. Every request
// after the first names it in a header, as the protocol asks. The SDK does
// that through a hook of its own, which a connection that is wrapped, as
// study's is to see what a plugin sends, does not get: so it is done here
// (see answersOnly).
func (t *boundedTransport) speaks(revision string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.protocol = revision
}

// refused returns what the service last refused a request with, if it did.
func (t *boundedTransport) refused() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refusal
}

func (t *boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	protocol := t.protocol
	t.mu.Unlock()
	if protocol != "" && req.Header.Get(protocolHeader) == "" {
		// A round tripper leaves the request it was given as it is.
		req = req.Clone(req.Context())
		req.Header.Set(protocolHeader, protocol)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errRedirect
	}
	if resp.StatusCode >= 400 {
		// What it says of why, for the learner: the start of it, kept
		// aside, and the whole of it still there for whoever reads on.
		start, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(start), resp.Body), body: resp.Body}
		t.mu.Lock()
		t.refusal = strings.TrimSpace(http.StatusText(resp.StatusCode) + ": " + string(start))
		t.mu.Unlock()
	}
	// A stream of events has no end to bound: the SDK bounds each event.
	if kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); kind != "text/event-stream" {
		resp.Body = &boundedBody{body: resp.Body, left: t.max}
	}
	return resp, nil
}

// joinedBody reads from a reader and closes the body it was made from.
type joinedBody struct {
	io.Reader
	body io.Closer
}

func (b *joinedBody) Close() error { return b.body.Close() }

// boundedBody reads up to left bytes of a body and then fails.
type boundedBody struct {
	body io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// Only more data is too much: a body of exactly the limit ends here.
		var one [1]byte
		if n, err := b.body.Read(one[:]); n == 0 {
			return 0, err
		}
		return 0, errTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.body.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }

// Address is the address of a plugin reached by URL as study shows it
// outside the registry's own listing: its scheme and host. The rest of a
// registered address, a path or a query, may be a key.
func Address(registered string) string {
	u, err := url.Parse(registered)
	if err != nil || u.Host == "" {
		return "its address"
	}
	return u.Scheme + "://" + u.Host
}

// secrets are the parts of a registered address that may be a key, longest
// first: the address whole, what follows its host, its path, its query, and
// each long piece of the two. Text a service wrote is shown without them: a
// service that repeats the address it was asked at would otherwise have
// study print what it never prints itself.
func secrets(registered string) []string {
	u, err := url.Parse(registered)
	if err != nil {
		return []string{registered}
	}
	found := map[string]bool{registered: true}
	add := func(part string, least int) {
		if len(part) >= least {
			found[part] = true
		}
	}
	add(u.RequestURI(), 2)
	add(u.EscapedPath(), 2)
	add(u.Path, 2)
	add(u.RawQuery, 1)
	for _, piece := range strings.Split(u.Path, "/") {
		add(piece, 8)
	}
	for _, piece := range strings.Split(u.EscapedPath(), "/") {
		add(piece, 8)
	}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		_, value, _ := strings.Cut(pair, "=")
		add(value, 6)
		if plain, err := url.QueryUnescape(value); err == nil {
			add(plain, 6)
		}
	}
	parts := make([]string, 0, len(found))
	for part := range found {
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool {
		if len(parts[i]) != len(parts[j]) {
			return len(parts[i]) > len(parts[j])
		}
		return parts[i] < parts[j]
	})
	return parts
}
