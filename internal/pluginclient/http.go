package pluginclient

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// errRedirect is what a plugin's address gets for answering with a redirect.
var errRedirect = errors.New("the address answered with a redirect to another one")

// errTooLarge is what a plugin gets for an answer over the limit.
var errTooLarge = errors.New("the answer is larger than a plugin's message may be")

// urlTransport reaches a plugin registered by URL over streamable HTTP, and
// returns what closes its idle connections.
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
func urlTransport(address string) (mcp.Transport, func()) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: &boundedTransport{base: base, max: plugincontract.MaxMessageBytes}}
	return &mcp.StreamableClientTransport{
		Endpoint:             address,
		HTTPClient:           client,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
		MaxEventSize:         plugincontract.MaxMessageBytes,
	}, base.CloseIdleConnections
}

// boundedTransport refuses redirects and bounds what is read of an answer.
type boundedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t *boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errRedirect
	}
	// A stream of events has no end to bound: the SDK bounds each event.
	if kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); kind != "text/event-stream" {
		resp.Body = &boundedBody{body: resp.Body, left: t.max}
	}
	return resp, nil
}

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
