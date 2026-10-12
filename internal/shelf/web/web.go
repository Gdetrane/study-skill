// Package web fetches a web page and takes its readable text.
//
// Shelf fetches an address only when it is asked to index it. The fetch is
// bounded in every way a server could stretch it: in time, in size, in how
// often it sends Shelf elsewhere, and in what it may send back.
//
// What is readable on a page, the article without the menus around it, is
// decided by the readability library, which follows what Firefox's reader
// view does. Nothing here parses HTML itself.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"codeberg.org/readeck/go-readability/v2/render"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Limits bound one fetch.
type Limits struct {
	// Timeout is how long the whole fetch may take, redirects included.
	Timeout time.Duration
	// MaxBytes is how large the page may be, as it arrives once any
	// compression of the transfer is undone.
	MaxBytes int64
	// MaxRedirects is how often the server may send Shelf to another
	// address.
	MaxRedirects int
}

// DefaultLimits are the limits Shelf fetches with.
var DefaultLimits = Limits{Timeout: 60 * time.Second, MaxBytes: 16 << 20, MaxRedirects: 5}

// Page is a web page as it was fetched.
type Page struct {
	// Text is the readable text of the page: paragraphs divided by an empty
	// line. It is not cleaned (cut.Clean).
	Text string
	// Hash is the content hash of the body as it was fetched: "sha256:" and
	// 64 hexadecimal digits.
	Hash string
}

// Problem is why a page could not be fetched or read, in words for the
// learner. It never holds more of the address than its host.
type Problem struct{ Reason string }

func (p *Problem) Error() string { return p.Reason }

func problem(format string, args ...any) error {
	return &Problem{Reason: fmt.Sprintf(format, args...)}
}

// Fetcher fetches web pages.
type Fetcher struct {
	limits Limits
	agent  string
	// transport is what requests go out through. A test gives its own.
	transport http.RoundTripper
}

// NewFetcher returns a Fetcher that keeps to limits and calls itself agent
// to the servers it asks.
func NewFetcher(limits Limits, agent string) *Fetcher {
	// The transport is Go's own, with its defaults: it verifies
	// certificates, and takes a proxy from the environment when one is set
	// there. study gives a plugin no such variable, so a learner behind a
	// proxy registers a script that sets it (docs/shelf.md).
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = 1 << 20
	// One page from one server: nothing is kept open for a next request.
	transport.DisableKeepAlives = true
	return &Fetcher{limits: limits, agent: agent, transport: transport}
}

// Fetch fetches the page at address, an http or https address, and returns
// its text and the hash of what was fetched. Whatever goes wrong is a
// *Problem, unless ctx was done, which gives ctx's error.
func (f *Fetcher) Fetch(ctx context.Context, address string) (Page, error) {
	u, err := checkAddress(address)
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.limits.Timeout)
	defer cancel()
	client := &http.Client{
		Transport: f.transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > f.limits.MaxRedirects {
				return problem("the address redirects more than %d times", f.limits.MaxRedirects)
			}
			if _, err := checkAddress(req.URL.String()); err != nil {
				return problem("the address redirects to one that is no web page: %v", err)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, problem("the address cannot be fetched")
	}
	req.Header.Set("User-Agent", f.agent)
	req.Header.Set("Accept", "text/html, application/xhtml+xml, text/plain;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return Page{}, f.failure(ctx, u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, problem("%s answered %d %s", u.Host, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	// One byte more than is allowed, to tell a page of that size from a
	// larger one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.limits.MaxBytes+1))
	if err != nil {
		return Page{}, f.failure(ctx, u, err)
	}
	if int64(len(body)) > f.limits.MaxBytes {
		return Page{}, problem("the page is larger than %d MB, the most Shelf fetches", f.limits.MaxBytes>>20)
	}
	sum := sha256.Sum256(body)
	page := Page{Hash: "sha256:" + hex.EncodeToString(sum[:])}

	declared := resp.Header.Get("Content-Type")
	kind, _, err := mime.ParseMediaType(declared)
	if declared == "" || err != nil {
		// A server that does not say what it sends: the bytes do.
		declared = http.DetectContentType(body)
		kind, _, _ = mime.ParseMediaType(declared)
	}
	switch kind {
	case "text/html", "application/xhtml+xml":
		page.Text, err = readable(body, declared, resp.Request.URL)
	case "text/plain":
		page.Text, err = decoded(body, declared)
	default:
		return Page{}, problem("%s sent %s: Shelf reads web pages, which are HTML or plain text. Download the file and add it as "+
			"a file Source instead", u.Host, printable(kind))
	}
	if err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(page.Text) == "" {
		return Page{}, problem("the page has no text to read: it may be drawn by a script, which Shelf does not run")
	}
	return page, nil
}

// failure says why a request failed, without the address's path or query,
// which may hold a key.
func (f *Fetcher) failure(ctx context.Context, u *url.URL, err error) error {
	var p *Problem
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &p):
		return p
	case ctx.Err() == context.Canceled:
		return ctx.Err()
	case ctx.Err() != nil, errors.As(err, &timeout) && timeout.Timeout():
		// The fetch's own time is over: the caller's being over is the
		// case above.
		return problem("%s did not send the page within %s", u.Host, f.limits.Timeout)
	}
	// The cause without the address: a url.Error repeats it whole.
	var wrapped *url.Error
	if errors.As(err, &wrapped) {
		err = wrapped.Err
	}
	return problem("%s could not be reached: %v", u.Host, err)
}

// checkAddress checks that address is one Shelf fetches: http or https, with
// a host, and without a user name or password.
func checkAddress(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	switch {
	case err != nil:
		return nil, problem("it is not an address")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, problem("it is not an http or https address")
	case u.Hostname() == "":
		return nil, problem("it names no host")
	case u.User != nil:
		return nil, problem("it holds a user name or a password")
	}
	return u, nil
}

// readable returns the readable text of an HTML page.
func readable(body []byte, contentType string, address *url.URL) (string, error) {
	// The page in UTF-8, whatever it was written in: the Content-Type says,
	// or the page itself does.
	utf8, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", problem("the page is written in an encoding Shelf does not know")
	}
	doc, err := html.Parse(utf8)
	if err != nil {
		return "", problem("the page cannot be read as HTML")
	}
	// The article, as a reader view shows it: without menus, footers and
	// whatever else surrounds it, and without the page's title, which is
	// not part of what the page says.
	if article, err := readability.FromDocument(doc, address); err == nil && article.Node != nil {
		if text := render.InnerText(article.Node); strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	// A page with no article to find, a table of numbers or a few lines:
	// everything it shows.
	fresh, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return "", problem("the page is written in an encoding Shelf does not know")
	}
	if doc, err = html.Parse(fresh); err != nil {
		return "", problem("the page cannot be read as HTML")
	}
	return render.InnerText(doc), nil
}

// decoded returns a page of plain text in UTF-8.
func decoded(body []byte, contentType string) (string, error) {
	_, params, _ := mime.ParseMediaType(contentType)
	name := params["charset"]
	if name == "" {
		return string(body), nil
	}
	encoding, _ := charset.Lookup(name)
	if encoding == nil {
		return "", problem("the page is written in an encoding Shelf does not know")
	}
	text, err := encoding.NewDecoder().Bytes(body)
	if err != nil {
		return "", problem("the page is not in the encoding it says it is in")
	}
	return string(text), nil
}

// printable is a server's word made safe to repeat: short, and without what
// a terminal would act on.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E {
			return -1
		}
		return r
	}, s)
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		return "something that is no text"
	}
	return s
}
