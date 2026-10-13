// Package endpoint asks the two services a learner can give Shelf: one that
// turns text into vectors, which speaks the OpenAI embeddings API, and one
// that orders Passages by how well they answer a question.
//
// Both are the learner's choice, named in Shelf's settings file, which the
// agent cannot write. So they are not held to the rule for a Source's web
// page (package web), which Shelf fetches because the agent named it: an
// endpoint is usually on the computer Shelf runs on, where no page is
// fetched from.
//
// An endpoint is not trusted with Shelf's time or memory, nor with the key:
// every request has a deadline, an answer is read up to a limit and checked
// before it is used, no redirect is followed, and whatever goes wrong is a
// *Problem whose reason names the endpoint's host and never its key, nor
// the rest of its address.
//
// A request holds what the settings name and nothing else: the model, the
// texts, and the fields the settings add. An endpoint may refuse a request
// for a key it does not know, so Shelf sends none of its own choosing.
package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// What an endpoint is given when the settings say nothing else.
const (
	// DefaultBatch is how many Passages one request asks vectors for.
	DefaultBatch = 16
	// DefaultTimeout is how long an endpoint has for one request while a
	// Source is indexed: a batch of Passages on a slow computer, with a
	// model that may have to be loaded first.
	DefaultTimeout = 2 * time.Minute
	// DefaultQueryTimeout is how long it has for the one vector of a
	// search, and DefaultRerankTimeout for ordering a search's Passages.
	// Together they are thirty seconds of the forty-five study gives a
	// search, starting Shelf included: so that Shelf can still answer, by
	// keyword or in the order it found, when an endpoint does not, and
	// say so, where study would otherwise stop it and say nothing of why.
	DefaultQueryTimeout  = 10 * time.Second
	DefaultRerankTimeout = 20 * time.Second
	// DefaultTestTimeout is how long an endpoint has for the one request by
	// which Shelf tests it when study asks what Shelf depends on: less than
	// the ten seconds the contract gives Shelf for all of that answer, so
	// that Shelf says in time that an endpoint did not answer, where study
	// would otherwise stop it and say nothing of which one.
	DefaultTestTimeout = 8 * time.Second
	// DefaultMaxBytes is how large one answer with vectors may be, and
	// DefaultRerankMaxBytes one answer of the reranking endpoint.
	DefaultMaxBytes       = 64 << 20
	DefaultRerankMaxBytes = 4 << 20
	// DefaultCandidates is how many of the best Passages are reranked.
	DefaultCandidates = 30
)

// Bounds of what the settings may say.
const (
	MaxBatch      = 256
	MaxDimensions = 16384
	MaxCandidates = 100
	// maxName is how long a model's name may be, and maxMark the text put
	// around a query or a document.
	maxName = 200
	maxMark = 1000
)

// Problem is why an endpoint did not give what it was asked for, in words
// for the learner. It names the endpoint by its host, and never holds the
// key or the rest of the address.
type Problem struct{ Reason string }

func (p *Problem) Error() string { return p.Reason }

func problem(format string, args ...any) error {
	return &Problem{Reason: fmt.Sprintf(format, args...)}
}

// Marking says how the endpoint is told that a text is a question, or that
// it is a stretch of a document: by text Shelf writes around it, or by
// fields added to the request, as a server that applies a model's own
// prompts by name wants it. One endpoint is told in one of the two ways,
// never both: text that Shelf marked would be marked again by the server.
type Marking struct {
	// Text is what the text is sent as: "task: search result | query:
	// {text}". {text} stands for the text and {title}, in a document's
	// marking, for the title of the Source. Empty, the text is sent as it
	// is.
	Text string
	// Fields are added to the request: {"prompt": "query"}. Each value is
	// text, a number or true or false.
	Fields map[string]any
}

// The places a Marking's Text has for what is filled in.
const (
	placeText  = "{text}"
	placeTitle = "{title}"
)

// mark returns a text as it is sent. Runs of white space become one space:
// a Passage is lines of a page, and the model reads words.
func (m Marking) mark(title, text string) string {
	text = squeeze(text)
	if m.Text == "" {
		return text
	}
	if title = squeeze(title); title == "" {
		title = "none"
	}
	// One pass over the marking: what is filled in is not read for places.
	return strings.NewReplacer(placeTitle, title, placeText, text).Replace(m.Text)
}

func squeeze(text string) string { return strings.Join(strings.Fields(text), " ") }

// String says how texts are marked, in one line that is the same for the
// same marking: it is kept beside a Source's vectors, to tell later whether
// they were made as Shelf would make them now.
func (m Marking) String() string {
	switch {
	case len(m.Fields) > 0:
		fields, _ := json.Marshal(m.Fields) // keys in order
		return "request " + string(fields)
	case m.Text != "":
		return m.Text
	}
	return placeText
}

// check checks a marking. document says that it is a document's, which may
// name the Source's title.
func (m Marking) check(name string, document bool) error {
	if m.Text != "" {
		switch {
		case len(m.Text) > maxMark || !plain(m.Text):
			return fmt.Errorf("%s is not text of up to %d bytes on one line", name, maxMark)
		case strings.Count(m.Text, placeText) != 1:
			return fmt.Errorf("%s holds %s %d times: it is what the text is sent as, with %s once, where the text goes",
				name, placeText, strings.Count(m.Text, placeText), placeText)
		case !document && strings.Contains(m.Text, placeTitle):
			return fmt.Errorf("%s holds %s: a question has no title, only a document has its Source's", name, placeTitle)
		}
	}
	for key, value := range m.Fields {
		if key == "" || len(key) > maxName || !plain(key) {
			return fmt.Errorf("%s_request names a field that is not a word", name)
		}
		if slices.Contains([]string{"model", "input", "dimensions"}, key) {
			return fmt.Errorf("%s_request sets %s, which is Shelf's to send: it says how a text is marked, such as "+
				"prompt = \"query\"", name, key)
		}
		switch v := value.(type) {
		case string:
			if len(v) > maxMark || !plain(v) {
				return fmt.Errorf("%s_request.%s is not text of up to %d bytes on one line", name, key, maxMark)
			}
		case bool, int, int64:
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("%s_request.%s is not a number", name, key)
			}
		default:
			return fmt.Errorf("%s_request.%s is neither text, a number, nor true or false", name, key)
		}
	}
	return nil
}

// plain reports whether s is text on one line: valid UTF-8 without control
// characters.
func plain(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// Embedding says which endpoint gives Shelf its vectors, and how it is
// asked. The fields are named as the settings file names them.
type Embedding struct {
	// Address is where the endpoint serves the OpenAI API, as an OpenAI
	// client's base address has it: "http://127.0.0.1:8080/v1". Shelf posts
	// to /embeddings below it. An address without a path stands for /v1.
	Address string
	// Key is sent as the request's bearer token when it is not empty.
	Key string
	// Model is the model's name, as the endpoint knows it.
	Model string
	// Dimensions is how many numbers Shelf keeps of each vector: the size
	// the model gives, or fewer. A longer vector is cut to it and made of
	// length 1 again, which only a model trained for it allows. It is
	// always said: what is kept is then of one size that the settings
	// name, and a size that changes is seen in them.
	Dimensions int
	// SendDimensions has the request ask the endpoint for vectors of that
	// size, as the OpenAI API's dimensions does. It is sent only when this
	// says so: not every endpoint knows the key.
	SendDimensions bool
	// Query and Document say how a question and a stretch of a document
	// are marked for the model.
	Query, Document Marking
	// Batch is how many texts one request holds while a Source is indexed.
	Batch int
	// Timeout is how long one such request may take, and QueryTimeout the
	// one request of a search.
	Timeout, QueryTimeout time.Duration
	// MaxBytes is how large one answer may be.
	MaxBytes int64
	// Agent is what Shelf calls itself to the endpoint.
	Agent string
}

// Check checks what the settings say of an embedding endpoint, and says
// what is wrong in the settings file's own names.
func (e Embedding) Check() error {
	if _, err := route(e.Address, "embeddings"); err != nil {
		return err
	}
	switch {
	case e.Model == "":
		return errors.New("model is missing: the name of the model, as the endpoint knows it")
	case len(e.Model) > maxName || !plain(e.Model):
		return fmt.Errorf("model is not a name of up to %d bytes", maxName)
	case !keyValue(e.Key):
		return errors.New("key is not one word of printable characters")
	case e.Dimensions < 0 || e.Dimensions > MaxDimensions:
		return fmt.Errorf("dimensions is not a number from 1 to %d", MaxDimensions)
	case e.Batch < 0 || e.Batch > MaxBatch:
		return fmt.Errorf("batch is not a number from 1 to %d", MaxBatch)
	}
	if err := e.Query.check("query", false); err != nil {
		return err
	}
	if err := e.Document.check("document", true); err != nil {
		return err
	}
	// One way for the endpoint: a server that marks the texts itself would
	// mark what Shelf had marked already.
	text := e.Query.Text != "" || e.Document.Text != ""
	fields := len(e.Query.Fields) > 0 || len(e.Document.Fields) > 0
	if text && fields {
		return errors.New("query and document say what text Shelf writes around a text, and query_request and " +
			"document_request what the request tells the endpoint to write itself: an endpoint takes one of the two, " +
			"or the text would be marked twice")
	}
	// Said last, since it is what is left out most: every other mistake of
	// the table is named before this one.
	if e.Dimensions == 0 {
		return errors.New("dimensions is missing: how many numbers a vector of the model has, which the model's own page " +
			"says (768 for many), or how many of them to keep")
	}
	return nil
}

// headerValue reports whether s can be sent in a request's header as it is.
func headerValue(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

// keyValue reports whether a key can be sent as a request's bearer token,
// exactly as it is written: one word. A key with a space at its end would
// be sent without it, and an endpoint that repeated what it was sent would
// then repeat a key Shelf does not know for its own (caller.said).
func keyValue(key string) bool {
	return headerValue(key) && !strings.Contains(key, " ")
}

// route is the address a request is posted to: last below the address the
// settings give. An address is http or https, names a host, and holds no
// user name, password, query or fragment: a key has a setting of its own,
// and is then never part of what Shelf says of the endpoint.
func route(address, last string) (*url.URL, error) {
	if address == "" {
		return nil, errors.New("endpoint is missing")
	}
	u, err := url.Parse(address)
	switch {
	case err != nil || !plain(address) || strings.ContainsAny(address, " \t"):
		return nil, errors.New("endpoint is not an address")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, errors.New("endpoint is not an http or https address")
	case u.Hostname() == "":
		return nil, errors.New("endpoint names no host")
	case u.User != nil:
		return nil, errors.New("endpoint holds a user name or a password: a key goes in key")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return nil, errors.New("endpoint holds a query or a fragment: give the address the API is served at, such as " +
			"http://127.0.0.1:8080/v1")
	}
	base := strings.TrimRight(u.Path, "/")
	if base == "" {
		// Where a server serves the OpenAI API, when it serves one: the
		// address was given as a host and a port alone.
		base = "/v1"
	}
	out := *u
	out.Path, out.RawPath = base+"/"+last, ""
	return &out, nil
}

// caller posts requests to one endpoint.
type caller struct {
	// what is how the endpoint is spoken of: "the embedding endpoint
	// 127.0.0.1:8080".
	what string
	url  string
	// address is what of the address a server's own words are searched
	// for, the longest first: the whole of it, then the address the
	// settings gave, then their paths, then every name of the path that is
	// long enough to be nobody else's.
	address  []string
	key      string
	agent    string
	maxBytes int64
	client   *http.Client
}

// bearer is a bearer token as a request's header holds it, and as a server
// repeats one: the word in any letter case, and what follows it after white
// space, a colon or an equals sign, between quotation marks or without
// them. A quotation mark may be written as a server writes one inside its
// own JSON, behind a backslash.
var bearer = regexp.MustCompile(`(?i)\bbearer(?:\s*[:=]\s*|\s+)(?:\\?"[^"]*"|\\?'[^']*'|(?:\\?["'])?[^\s"',;]+)`)

// What stands for Shelf's own key and for a part of the address while a
// server's words are gone through. Neither is in text that is shown, so
// nothing that is looked for afterwards is found in part of one, and each
// is written out in words at the end.
const (
	aKey      = "\x00"
	anAddress = "\x01"
)

func newCaller(kind string, to *url.URL, key, agent string, maxBytes int64) *caller {
	// Go's own transport with its defaults: it verifies certificates, and
	// takes a proxy from the environment, which it never uses for this
	// computer. No address is refused: the endpoint is the learner's.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxResponseHeaderBytes = 1 << 20
	if to.Scheme != "https" {
		// An http endpoint is asked directly, proxy or none: a proxy asked
		// to pass an http request on reads all of it, the key and every
		// Passage. Through one, an https request stays between Shelf and
		// the endpoint.
		transport.Proxy = nil
	}
	if agent == "" {
		agent = "study-shelf"
	}
	return &caller{
		what: kind + " " + to.Host, url: to.String(), address: namesOf(to), key: key, agent: agent, maxBytes: maxBytes,
		client: &http.Client{
			Transport: transport,
			// A redirect is not followed: the key would go where the
			// learner did not send it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// namesOf are the parts of an endpoint's address that a server's own words
// are not repeated with, the longest first: the address a request is posted
// to and the one the settings gave, which it is posted below; the path of
// each; and every name of the path of twelve characters or more, which is
// an account's or a tenant's and not a word of the API. The host is not
// among them: Shelf names an endpoint by it.
//
// Each is there as it is sent and as it reads, where a path holds what a
// URL escapes: a server repeats either. And each is as it will be shown
// (shown), which is where it is looked for.
func namesOf(to *url.URL) []string {
	base := *to
	base.Path, base.RawPath = path.Dir(to.Path), ""
	var names []string
	add := func(name string, least int) {
		if name = shown(name); len(name) >= least && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, u := range []*url.URL{to, &base} {
		add(u.String(), 2)
		add(u.Scheme+"://"+u.Host+u.Path, 2)
		add(u.EscapedPath(), 2)
		add(u.Path, 2)
	}
	for _, whole := range []string{to.EscapedPath(), to.Path} {
		for _, name := range strings.Split(whole, "/") {
			add(name, 12)
		}
	}
	// A name is part of a path, and a path of an address.
	sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return names
}

// close lets go of the connections kept for a next request.
func (c *caller) close() { c.client.CloseIdleConnections() }

// post sends body as JSON and reads the answer into out: an answer of up to
// most bytes, or of as many as an answer may ever have when most is 0.
// Whatever goes wrong is a *Problem, unless ctx was done, which gives ctx's
// error.
func (c *caller) post(ctx context.Context, within time.Duration, most int64, body, out any) error {
	if most <= 0 || most > c.maxBytes {
		most = c.maxBytes
	}
	data, err := json.Marshal(body)
	if err != nil {
		return problem("Shelf could not write its request to %s", c.what)
	}
	timed, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	req, err := http.NewRequestWithContext(timed, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return problem("Shelf could not write its request to %s", c.what)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.agent)
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	failed := func(err error) error {
		var timeout interface{ Timeout() bool }
		switch {
		case ctx.Err() != nil:
			// The caller's own time is over, or it was told to stop.
			return ctx.Err()
		case timed.Err() != nil, errors.As(err, &timeout) && timeout.Timeout():
			return problem("%s did not answer within %s. Check that its server is not busy, or still loading the model", c.what, within)
		}
		// The cause without the address: a url.Error repeats it whole.
		var wrapped *url.Error
		if errors.As(err, &wrapped) {
			err = wrapped.Err
		}
		return problem("%s could not be reached: %s. Check that its server is running, and its address in Shelf's settings file",
			c.what, c.said(err.Error()))
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return failed(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		reason := fmt.Sprintf("%s answered %d %s", c.what, resp.StatusCode, http.StatusText(resp.StatusCode))
		if words := c.said(message(said)); words != "" {
			reason += ": " + words
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			reason += ". Check the key in Shelf's settings file"
		case resp.StatusCode == http.StatusNotFound:
			reason += ". Check the endpoint's address and the model's name in Shelf's settings file"
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			reason += ". Shelf follows no redirect: give the address that answers in its settings file"
		case resp.StatusCode >= 500:
			reason += ". The fault is on the server's side: its own log says more"
		}
		return &Problem{Reason: reason}
	}
	// One byte more than is allowed, to tell an answer of that size from a
	// larger one.
	answer, err := io.ReadAll(io.LimitReader(resp.Body, most+1))
	if err != nil {
		return failed(err)
	}
	if int64(len(answer)) > most {
		return problem("%s sent more than %s in one answer, the most Shelf reads of an answer to what it asked", c.what, size(most))
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return problem("%s did not answer as its API does: %s", c.what, jsonProblem(err))
	}
	return nil
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// answerError is what is wrong with an answer, in words that repeat nothing
// of it.
type answerError string

func (e answerError) Error() string { return string(e) }

// jsonProblem says what is wrong with an answer without repeating it.
func jsonProblem(err error) string {
	var said answerError
	if errors.As(err, &said) {
		return string(said)
	}
	var wrongType *json.UnmarshalTypeError
	if errors.As(err, &wrongType) && wrongType.Field != "" {
		// The kind of value alone: a number is repeated whole there.
		kind, _, _ := strings.Cut(wrongType.Value, " ")
		return fmt.Sprintf("its %s is a %s", wrongType.Field, kind)
	}
	return "its answer is not JSON"
}

// named is the name an endpoint gives the model it answered with, when it
// gives one as text. An answer is none the worse for naming no model, or
// for naming it in a shape of its own: Shelf only repeats the name, when it
// tests the endpoint.
type named string

func (n *named) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		*n = named(text)
	}
	return nil
}

// counted reads a list of an answer, of as many items as were asked for and
// no more: it gives up at the item after. What an answer is decoded into is
// so bounded by what was asked, and not by how many bytes the answer has: a
// few megabytes of empty items would otherwise take a hundred times that.
type counted[T any] struct {
	most  int
	items []T
}

func (l *counted[T]) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return answerError("what should be a list of results is none")
	}
	for dec.More() {
		if len(l.items) >= l.most {
			return answerError(fmt.Sprintf("it holds more than the %d results that were asked for", l.most))
		}
		var item T
		if err := dec.Decode(&item); err != nil {
			return err
		}
		l.items = append(l.items, item)
	}
	return nil
}

// numbers is a vector as an endpoint gives it: a list of numbers, and no
// more of them than a vector may ever have.
type numbers []float32

func (n *numbers) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	switch {
	case err == nil && tok == nil:
		// No vector, which whoever reads the answer says.
		return nil
	case err != nil || tok != json.Delim('['):
		return answerError("an embedding is no list of numbers")
	}
	out := make([]float32, 0, 1024)
	for dec.More() {
		if len(out) >= MaxDimensions {
			return answerError(fmt.Sprintf("a vector has more than %d numbers, the most Shelf takes", MaxDimensions))
		}
		var x float32
		if err := dec.Decode(&x); err != nil {
			return answerError("an embedding holds what is no number a vector can hold")
		}
		out = append(out, x)
	}
	*n = out
	return nil
}

// message is what an endpoint says went wrong, taken from the body of its
// answer: the message of an OpenAI error, or a server's own few words.
func message(body []byte) string {
	var shaped struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &shaped) != nil {
		// Not JSON: a proxy's page, or a line of text. A page is no reason.
		if text := strings.TrimSpace(string(body)); !strings.HasPrefix(text, "<") {
			return text
		}
		return ""
	}
	var inner struct {
		Message string `json:"message"`
	}
	var text string
	switch {
	case json.Unmarshal(shaped.Error, &inner) == nil && inner.Message != "":
		return inner.Message
	case json.Unmarshal(shaped.Error, &text) == nil && text != "":
		return text
	case shaped.Message != "":
		return shaped.Message
	case json.Unmarshal(shaped.Detail, &text) == nil:
		return text
	}
	return ""
}

// said is text of the endpoint's, or of the system's, made safe to repeat:
// on one line, without what a terminal would act on, short, and without the
// key, should the endpoint have repeated it.
//
// What it promises, and no more: Shelf's own key, whole, wherever it
// stands; whatever follows the word "Bearer"; and the address but for its
// host. A key that is not Shelf's and follows no "Bearer" is words like any
// other to Shelf, and so is a name of the address's path that is shorter
// than twelve characters and stands by itself.
func (c *caller) said(text string) string {
	// The key is looked for in the text as it will be shown: one repeated
	// with a character in it that is not shown is whole again by now.
	text = shown(text)
	// Shelf's own key first, wherever it stands and whatever it holds: a
	// key may hold a comma, a semicolon or a quotation mark, at which what
	// follows "Bearer" is taken to end, and would be left in two.
	if c.key != "" {
		text = strings.ReplaceAll(text, c.key, aKey)
	}
	// And whatever follows "Bearer" is a key, Shelf's own or not: a server
	// that repeats the header it was sent repeats it as it was sent, and a
	// gateway repeats the one it sent on. What stands for Shelf's own is
	// taken with it, whole.
	text = bearer.ReplaceAllLiteralString(text, "Bearer "+aKey)
	// Nor the rest of the address, which a server names when it says what
	// it was asked for, and which may hold what is the learner's alone: in
	// whatever letter case the server writes it.
	for _, part := range c.address {
		text = withoutFold(text, part, anAddress)
	}
	text = strings.NewReplacer(aKey, "[the key]", anAddress, "[the address]").Replace(text)
	const most = 300
	if len(text) > most {
		cut := most
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return text
}

// Embedder asks an endpoint for vectors.
type Embedder struct {
	cfg  Embedding
	call *caller
}

// NewEmbedder returns an Embedder for the endpoint the settings name. It
// asks the endpoint nothing: Shelf is started for every question study has,
// and most of them need no vector.
func NewEmbedder(cfg Embedding) (*Embedder, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	to, err := route(cfg.Address, "embeddings")
	if err != nil {
		return nil, err
	}
	if cfg.Batch == 0 {
		cfg.Batch = DefaultBatch
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = DefaultQueryTimeout
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	return &Embedder{cfg: cfg, call: newCaller("the embedding endpoint", to, cfg.Key, cfg.Agent, cfg.MaxBytes)}, nil
}

// Close lets go of the connections to the endpoint.
func (e *Embedder) Close() { e.call.close() }

// Model is the model's name, Dimensions how many numbers Shelf keeps of a
// vector, 0 for as many as the endpoint gives, and Document how a stretch
// of a document is marked (Marking.String). Vectors made with other values
// of these are not compared with a question's.
func (e *Embedder) Model() string    { return e.cfg.Model }
func (e *Embedder) Dimensions() int  { return e.cfg.Dimensions }
func (e *Embedder) Document() string { return e.cfg.Document.String() }

// Name is how the endpoint is spoken of to the learner: "the embedding
// endpoint 127.0.0.1:8080". Its host, and nothing else of its address.
func (e *Embedder) Name() string { return e.call.what }

// Documents returns a vector for each of texts, stretches of a document
// with that title, in their order, each of length 1 and all of one size.
// It asks in batches, one after the other, and calls done, when it is not
// nil, with how many texts have their vector so far.
func (e *Embedder) Documents(ctx context.Context, title string, texts []string, done func(n int)) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.cfg.Batch {
		batch := texts[start:min(start+e.cfg.Batch, len(texts))]
		marked := make([]string, len(batch))
		for i, text := range batch {
			marked[i] = e.cfg.Document.mark(title, text)
		}
		vectors, err := e.embed(ctx, e.cfg.Timeout, e.cfg.Document, marked, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
		if done != nil {
			done(len(out))
		}
	}
	return out, nil
}

// Query returns the vector of a question, of length 1.
func (e *Embedder) Query(ctx context.Context, question string) ([]float32, error) {
	vector, _, err := e.Test(ctx, e.cfg.QueryTimeout, question)
	return vector, err
}

// Test asks the endpoint for the vector of a question exactly as a search
// asks it (Query is this): the same request, to the same address, with the
// same marking and key, and the answer held to the same checks. Only the
// time is the caller's: within, in place of the time a search gives the
// endpoint. It is for Shelf to test the endpoint with, so that what it
// finds is what a search will find.
//
// It also returns the name the endpoint gave the model it answered with,
// when it gave one, made safe to repeat as the endpoint's other words are.
// That name need not be the one the settings ask for: a server names one
// model in several ways, and some answer with the one model they have
// whatever they are asked for.
func (e *Embedder) Test(ctx context.Context, within time.Duration, question string) (vector []float32, answered string, err error) {
	vectors, err := e.embed(ctx, within, e.cfg.Query, []string{e.cfg.Query.mark("", question)}, &answered)
	if err != nil {
		return nil, "", err
	}
	return vectors[0], answered, nil
}

// embed is one request: a vector for each of texts, in their order. The name
// the endpoint gave its model is put in answered, when that is not nil.
func (e *Embedder) embed(ctx context.Context, within time.Duration, marking Marking, texts []string, answered *string) ([][]float32, error) {
	// The model, the texts, and what the settings add: an endpoint may
	// refuse a request for one key it does not know.
	request := map[string]any{"model": e.cfg.Model, "input": texts}
	for key, value := range marking.Fields {
		request[key] = value
	}
	if e.cfg.SendDimensions {
		request["dimensions"] = e.cfg.Dimensions
	}
	type vector struct {
		Index     *int    `json:"index"`
		Embedding numbers `json:"embedding"`
	}
	var answer struct {
		Model named           `json:"model"`
		Data  counted[vector] `json:"data"`
	}
	answer.Data.most = len(texts)
	// An answer is as large as the vectors that were asked for, written
	// out, and a little: of the size the request names, or of the largest
	// a vector may have.
	each := MaxDimensions
	if e.cfg.SendDimensions {
		each = e.cfg.Dimensions
	}
	const aNumber, around = 32, 64 << 10
	if err := e.call.post(ctx, within, int64(len(texts))*int64(each)*aNumber+around, request, &answer); err != nil {
		return nil, err
	}
	if answered != nil {
		*answered = e.call.said(string(answer.Model))
	}
	what := e.call.what
	if len(answer.Data.items) != len(texts) {
		return nil, problem("%s gave %d vectors for %d texts", what, len(answer.Data.items), len(texts))
	}
	// Each vector says which text it is of. An endpoint that does not say
	// gives them in the order of the texts.
	out := make([][]float32, len(texts))
	for place, item := range answer.Data.items {
		at := place
		if item.Index != nil {
			at = *item.Index
		}
		if at < 0 || at >= len(out) || out[at] != nil {
			return nil, problem("%s gave vectors that are not one for each text: it numbers one %d, of %d texts", what, at, len(texts))
		}
		vector := []float32(item.Embedding)
		switch want := e.cfg.Dimensions; {
		case len(vector) == 0:
			return nil, problem("%s gave a vector of no numbers", what)
		case len(vector) < want:
			return nil, problem("%s gave vectors of %d numbers, and Shelf's settings ask for %d: set dimensions to a size "+
				"the model has", what, len(vector), want)
		default:
			// The first numbers of a longer vector, which a model trained
			// for it allows, whether or not the endpoint was asked to cut:
			// a copy of them, so that the rest is let go of. Every vector
			// Shelf keeps is so of the one size its settings name.
			vector = slices.Clone(vector[:want])
		}
		if !unit(vector) {
			return nil, problem("%s gave a vector that is all zeros, or holds what is no number", what)
		}
		out[at] = vector
	}
	return out, nil
}

// unit makes a vector of length 1, in place, and reports whether it could.
func unit(vector []float32) bool {
	var sum float64
	for _, x := range vector {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return false
	}
	for i, x := range vector {
		vector[i] = float32(float64(x) / norm)
	}
	return true
}

// Reranking says which endpoint orders a search's Passages, and how it is
// asked.
type Reranking struct {
	// Address is where the endpoint serves its API: Shelf posts to /rerank
	// below it, and an address without a path stands for /v1.
	Address string
	// Key is sent as the request's bearer token when it is not empty.
	Key string
	// Model is the reranking model's name, as the endpoint knows it.
	Model string
	// Candidates is how many of the best Passages are handed over.
	Candidates int
	// Timeout is how long one request may take, and MaxBytes how large its
	// answer may be.
	Timeout  time.Duration
	MaxBytes int64
	// Agent is what Shelf calls itself to the endpoint.
	Agent string
}

// Check checks what the settings say of a reranking endpoint.
func (r Reranking) Check() error {
	if _, err := route(r.Address, "rerank"); err != nil {
		return err
	}
	switch {
	case r.Model == "":
		return errors.New("model is missing: the name of the reranking model, as the endpoint knows it")
	case len(r.Model) > maxName || !plain(r.Model):
		return fmt.Errorf("model is not a name of up to %d bytes", maxName)
	case !keyValue(r.Key):
		return errors.New("key is not one word of printable characters")
	case r.Candidates != 0 && (r.Candidates < 2 || r.Candidates > MaxCandidates):
		return fmt.Errorf("candidates is not a number from 2 to %d", MaxCandidates)
	}
	return nil
}

// Reranker asks an endpoint to order Passages.
type Reranker struct {
	cfg  Reranking
	call *caller
}

// NewReranker returns a Reranker for the endpoint the settings name, and
// asks it nothing.
func NewReranker(cfg Reranking) (*Reranker, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	to, err := route(cfg.Address, "rerank")
	if err != nil {
		return nil, err
	}
	if cfg.Candidates == 0 {
		cfg.Candidates = DefaultCandidates
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRerankTimeout
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultRerankMaxBytes
	}
	return &Reranker{cfg: cfg, call: newCaller("the reranking endpoint", to, cfg.Key, cfg.Agent, cfg.MaxBytes)}, nil
}

// Close lets go of the connections to the endpoint.
func (r *Reranker) Close() { r.call.close() }

// Candidates is how many of the best Passages are handed over.
func (r *Reranker) Candidates() int { return r.cfg.Candidates }

// Model is the reranking model's name, and Name how the endpoint is spoken
// of to the learner: "the reranking endpoint 127.0.0.1:8081".
func (r *Reranker) Model() string { return r.cfg.Model }
func (r *Reranker) Name() string  { return r.call.what }

// Ranked is one text as the endpoint ranks it: its place in what was handed
// over, and its score, higher for a better answer.
type Ranked struct {
	Index int
	Score float64
}

// Order returns the texts in the order the endpoint gives them for a
// question, the best first: each of them once.
func (r *Reranker) Order(ctx context.Context, question string, texts []string) ([]Ranked, error) {
	return r.rank(ctx, r.cfg.Timeout, question, texts, nil)
}

// Test asks the endpoint to order texts for a question exactly as a search
// asks it (Order is this), within a time of the caller's choosing: for
// Shelf to test the endpoint with. It also returns the name the endpoint
// gave the model it answered with, when it gave one, as Embedder.Test does.
func (r *Reranker) Test(ctx context.Context, within time.Duration, question string, texts []string) (ranked []Ranked, answered string, err error) {
	ranked, err = r.rank(ctx, within, question, texts, &answered)
	if err != nil {
		return nil, "", err
	}
	return ranked, answered, nil
}

// rank is one request: texts in the order the endpoint gives them for a
// question. The name the endpoint gave its model is put in answered, when
// that is not nil.
func (r *Reranker) rank(ctx context.Context, within time.Duration, question string, texts []string, answered *string) ([]Ranked, error) {
	documents := make([]string, len(texts))
	for i, text := range texts {
		documents[i] = squeeze(text)
	}
	// The request llama.cpp's server takes at /v1/rerank, which is the one
	// Shelf was measured with.
	request := map[string]any{"model": r.cfg.Model, "query": squeeze(question), "documents": documents, "top_n": len(documents)}
	type ranked struct {
		Index *int     `json:"index"`
		Score *float64 `json:"relevance_score"`
	}
	var answer struct {
		Model   named           `json:"model"`
		Results counted[ranked] `json:"results"`
	}
	answer.Results.most = len(texts)
	if err := r.call.post(ctx, within, 0, request, &answer); err != nil {
		return nil, err
	}
	if answered != nil {
		*answered = r.call.said(string(answer.Model))
	}
	what := r.call.what
	if len(answer.Results.items) != len(texts) {
		return nil, problem("%s ranked %d Passages of the %d it was handed", what, len(answer.Results.items), len(texts))
	}
	out, seen := make([]Ranked, 0, len(texts)), make([]bool, len(texts))
	for _, result := range answer.Results.items {
		switch {
		case result.Index == nil || result.Score == nil:
			return nil, problem("%s did not answer as its API does: a result without its index or its relevance_score", what)
		case *result.Index < 0 || *result.Index >= len(texts) || seen[*result.Index]:
			return nil, problem("%s ranked what it was not handed, or one Passage twice", what)
		case math.IsNaN(*result.Score) || math.IsInf(*result.Score, 0):
			return nil, problem("%s gave a score that is no number", what)
		}
		seen[*result.Index] = true
		out = append(out, Ranked{Index: *result.Index, Score: *result.Score})
	}
	// By score, and for the same score in the order the endpoint gave.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}
