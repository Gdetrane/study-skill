// Package pluginclient is study as the MCP client of a Knowledge base plugin
// (ADR-0012): it starts a registered command, or reaches a registered URL,
// checks that what answers speaks Lamplight's contract, and asks it to
// index, to say what is indexed, to stop, to search, to give a Passage
// again and to test what it depends on.
//
// A command is started as the registry's rules need it to be (StartCommand).
// The program is the one pluginregistry.OpenCommand checked, with nothing on
// the way to it looked up again; it runs without a shell, in the registry's
// folder and never in the Topic or the Study home study was started in, with
// pipes of its own and no other file of study's, in a session of its own,
// with a small environment study makes and not the one study has, and with a
// folder of its own in the learner's state folder, outside the Study home,
// which LAMPLIGHT_PLUGIN_DATA names. It is stopped when its Connection is
// closed, with everything it started that stayed in its process group.
//
// A plugin is not trusted with study's time, memory or terminal. Starting,
// every call and stopping have a deadline, so no plugin makes study hang.
// One message is read up to a limit, a plugin that sends study a request is
// stopped at the first, and every answer is checked against the contract
// before it is returned. Text a plugin wrote reaches an error only cleaned
// (see clean); a Passage's text is the Source's own and is returned as it
// is, for the adapters to print as they print any text from a file.
//
// A call whose context is cancelled returns canceled, and one whose context
// is cancelled already returns it without asking the plugin. So a command
// that was stopped and still has to tell a plugin to stop indexing calls
// Cancel with a context of its own, not with the one that was cancelled.
//
// The core calls this package, and the MCP server reaches a plugin only
// through the core.
package pluginclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// How long a plugin has, unless Options say otherwise.
const (
	// DefaultStartTimeout is how long a plugin has to start, or to be
	// reached, and to say which contract it speaks. What study does to
	// start it, finding its program and its folders, is under it too.
	DefaultStartTimeout = 30 * time.Second
	// DefaultCallTimeout is how long it has to answer index, status, cancel
	// or passage. None of them does the work itself: index answers at once.
	DefaultCallTimeout = 30 * time.Second
	// DefaultSearchTimeout is how long a search may take: a plugin may have
	// a model embed the query and another reorder what it found.
	DefaultSearchTimeout = 60 * time.Second
	// DefaultDiagnoseTimeout is how long it has to test what it depends on
	// and say what it found: the contract's own time for it.
	DefaultDiagnoseTimeout = plugincontract.DiagnoseTime
	// DefaultStopGrace is how long a command has to exit once its input is
	// closed, and again after SIGTERM, before it is killed.
	DefaultStopGrace = 2 * time.Second
)

// MCPVersion is the MCP revision study asks a plugin for: the last one with
// the initialize handshake every MCP library has. A plugin may answer with
// an earlier one.
const MCPVersion = "2025-11-25"

// MaxTools is how many tools of a plugin study lists before it stops
// looking for the contract's seven, and maxToolPages in how many pages.
const (
	MaxTools     = 1000
	maxToolPages = 100
	// maxCursorBytes is how long a cursor to the next page may be: study
	// sends it back, and holds it meanwhile.
	maxCursorBytes = 4096
)

// StderrKept is how much of the end of what a command wrote on its standard
// error study keeps, and StderrShown how much of the end of that it repeats
// when the plugin fails.
const (
	StderrKept  = 8 << 10
	StderrShown = 600
)

// unstick is how long the MCP library has to return once a deadline passed
// or the request was stopped. It returns at once, unless it is stuck writing
// to a plugin that reads nothing; then the plugin is killed.
var unstick = time.Second

// collectWait is how long study waits, when a command's connection broke,
// to learn whether the command has exited: its output closes a moment
// before the system reports how it ended.
const collectWait = 250 * time.Millisecond

// Options says how a plugin is started and how long it has.
type Options struct {
	// Version is study's version, which the plugin is told.
	Version string
	// Environ is the environment study has, of which a command is given a
	// few variables and no more (see childEnviron). Nil is the environment
	// of this process.
	Environ []string
	// Logger receives the Log. Nil discards it.
	Logger *slog.Logger
	// Began is when study set out to start the plugin, when that was before
	// Start is called: finding the plugin in the registry comes first, and
	// StartTimeout counts from there. Zero is the moment Start is called.
	Began time.Time
	// Indexes says that the plugin is started to be asked to index, which
	// has a command write in its folder. Start then checks that the folder
	// can be written in, and fails with what to do when it cannot: a sandbox
	// that lets study write in the Study home alone refuses it. A plugin
	// that is only asked what it has indexed, or to search, needs no such
	// folder, and is not held to one.
	Indexes bool
	// StartTimeout, CallTimeout, SearchTimeout, DiagnoseTimeout and StopGrace
	// replace the defaults above when they are not zero.
	StartTimeout    time.Duration
	CallTimeout     time.Duration
	SearchTimeout   time.Duration
	DiagnoseTimeout time.Duration
	StopGrace       time.Duration
}

func (o Options) withDefaults() Options {
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	for _, d := range []struct {
		value *time.Duration
		usual time.Duration
	}{
		{&o.StartTimeout, DefaultStartTimeout}, {&o.CallTimeout, DefaultCallTimeout},
		{&o.SearchTimeout, DefaultSearchTimeout}, {&o.DiagnoseTimeout, DefaultDiagnoseTimeout},
		{&o.StopGrace, DefaultStopGrace},
	} {
		if *d.value <= 0 {
			*d.value = d.usual
		}
	}
	return o
}

// Server is what a plugin calls itself when the MCP session opens.
type Server struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Connection is one connection to a Knowledge base plugin that speaks the
// contract: a command that is running, or a URL that answered. Its methods
// can be called from several goroutines. Close it when done: that is what
// stops a command.
type Connection struct {
	name string
	// address is the URL of a plugin reached by URL, which no message may
	// repeat, and parts what finds the parts of it in a text.
	address string
	parts   *parts
	opts    Options

	// proc is the command, when the plugin is one, and http what carries
	// the requests to a plugin reached by URL.
	proc *Command
	http *boundedTransport
	cs   *mcp.ClientSession

	contract   plugincontract.Contract
	server     Server
	dataFolder string

	mu sync.Mutex
	// ended says why the Connection can no longer be used, once it cannot,
	// and exited that the reason is that the command exited by itself.
	ended  string
	exited bool
	// asked is the first request the plugin sent study, if it sent one.
	asked string
	// misread is the first thing read from the plugin that was no MCP
	// message, if there was one.
	misread   error
	closeOnce sync.Once
	// asking counts what is being asked of the plugin at this moment: the
	// handshake, or a tool's call, until whatever went wrong with it has
	// been told.
	asking atomic.Int32
}

// Start starts the plugin, when it is registered by a command, or reaches
// it, when it is registered by URL, and asks which contract it speaks. The
// Connection it returns speaks version 1 with a plugin that answered as the
// contract says. The caller closes it.
//
// It fails with failed_precondition, and advice, when the plugin cannot be
// started, does not start, does not answer within Options.StartTimeout, is
// no MCP server or does not speak the contract; with newer_format when it
// speaks a newer contract than this study knows; and with canceled when ctx
// ends first. Whatever was started is stopped before Start returns an error.
func Start(ctx context.Context, env pluginregistry.Env, plugin pluginregistry.Plugin, opts Options) (*Connection, error) {
	s := &Connection{name: plugin.Name, opts: opts.withDefaults()}
	if ctx.Err() != nil {
		return nil, s.canceled(ctx)
	}
	// One clock for all of it: what study does to start the plugin, and
	// what the plugin does to answer.
	limit := s.opts.StartTimeout
	began := s.opts.Began
	if began.IsZero() {
		began = time.Now()
	}
	limited, cancel := context.WithDeadline(ctx, began.Add(limit))
	defer cancel()

	var transport mcp.Transport
	switch {
	case len(plugin.Command) > 0:
		// Finding the program and the folders is a walk through the file
		// system, which a drive that does not answer can hold up for ever.
		type started struct {
			command *Command
			err     error
		}
		done := make(chan started, 1)
		go func() {
			command, err := StartCommand(env, plugin, s.opts)
			done <- started{command, err}
		}()
		select {
		case r := <-done:
			if r.err != nil {
				return nil, r.err
			}
			s.proc = r.command
		case <-limited.Done():
			// Whatever it comes to start, late, is stopped.
			go func() {
				if r := <-done; r.command != nil {
					r.command.Stop()
				}
			}()
			if ctx.Err() != nil {
				return nil, s.canceled(ctx)
			}
			return nil, errorf(CodeFailedPrecondition, "Knowledge base plugin %s could not be started within %s: finding its "+
				"program, the registry's folder or the plugin's own folder took that long. Check that none of them is on a "+
				"drive that does not answer", s.name, limit)
		}
		s.dataFolder = s.proc.DataFolder()
		s.proc.whenExited(func() { s.end("its program has exited ("+s.proc.how()+")", true) })
		s.opts.Logger.Debug("started a Knowledge base plugin", "name", s.name, "pid", s.proc.PID())
		transport = s.proc.Transport()
	case plugin.URL != "":
		s.address, s.parts = plugin.URL, partsOf(plugin.URL)
		transport, s.http = urlTransport(plugin.URL)
	default:
		return nil, errorf(CodeInternal, "Knowledge base plugin %s is registered with neither a command nor a URL", plugin.Name)
	}
	guarded := &answersOnly{inner: transport, asked: s.wasAsked, tooMuch: s.heldTooMuch, misread: s.readNoMCP, closed: s.outputEnded}
	if s.http != nil {
		guarded.speaks = s.http.speaks
	}
	if err := s.handshake(ctx, limited, limit, guarded); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// answersOnly is a transport over which a plugin answers and notifies, and
// asks nothing.
//
// study offers a plugin nothing to call: no roots, no sampling, no
// questions to the learner. The MCP library still answers every request a
// server sends, each in a goroutine of its own that then waits to write its
// answer, so a plugin that sends requests without end, and reads none of
// the answers, makes study hold all of them: gigabytes in seconds, whether
// or not a call is under way. So the first request a plugin sends ends the
// connection, before the library sees it. Notifications are read and passed
// on: the library drops them, and holds nothing for them.
type answersOnly struct {
	inner mcp.Transport
	// asked is told the method of the request that ended the connection.
	asked func(method string)
	// tooMuch is told that a message held more values than one may, which
	// ended the connection too.
	tooMuch func()
	// misread is told what reading failed with, when that was what the
	// plugin wrote and not that it went away.
	misread func(error)
	// closed is told that reading ended because the plugin's output did.
	closed func()
	// speaks, when set, is told the MCP revision the plugin answered
	// initialize with, before the library goes on to its next message.
	//
	// Over HTTP every request after the first names that revision in a
	// header. The library sets it through a hook only its own connections
	// have, and this one stands in front of its own: so the revision is read
	// here, off the first answer, and the header is set by the HTTP client
	// (boundedTransport).
	speaks func(revision string)
}

// AnswersOnly returns a transport as study uses one with a plugin: the
// first request the plugin sends ends the connection, and asked, when it is
// not nil, is told the request's method. Notifications pass. The
// conformance suite holds a plugin to the same rule with it.
func AnswersOnly(transport mcp.Transport, asked func(method string)) mcp.Transport {
	// A transport that already ends at the first request, as ServiceTransport
	// gives one, is told whom to tell. A second one around it would see that
	// the first hung up, and never the request.
	if inner, ok := transport.(*answersOnly); ok {
		return &answersOnly{inner: inner.inner, asked: asked, tooMuch: inner.tooMuch, misread: inner.misread, closed: inner.closed,
			speaks: inner.speaks}
	}
	return &answersOnly{inner: transport, asked: asked}
}

// ServiceTransport is the transport study reaches a plugin registered by URL
// over: streamable HTTP, without redirects, with every answer read up to the
// size a message may have, and ended by the first request the service
// sends (see urlTransport and AnswersOnly).
func ServiceTransport(address string) mcp.Transport {
	transport, bounded := urlTransport(address)
	return &answersOnly{inner: transport, speaks: bounded.speaks}
}

func (t *answersOnly) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &answering{Connection: conn, asked: t.asked, tooMuch: t.tooMuch, misread: t.misread, closed: t.closed, speaks: t.speaks}, nil
}

type answering struct {
	mcp.Connection
	asked   func(method string)
	tooMuch func()
	misread func(error)
	closed  func()
	speaks  func(revision string)
	once    sync.Once
	first   sync.Once
}

// How reading ends once a plugin has sent a request, or a message that
// holds more than one may.
var (
	errAsked   = errors.New("the plugin sent a request")
	errTooMuch = fmt.Errorf("the plugin sent a message of more than %d values", plugincontract.MaxMessageValues)
)

func (c *answering) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err != nil {
		// Whoever waits for an answer is told by the library that the
		// connection closed, or what was read, whichever it comes to first.
		// What was read is known here, and says more.
		switch {
		case ctx.Err() != nil:
		case !hungUp(err):
			if c.misread != nil {
				c.misread(err)
			}
		case c.closed != nil:
			// Not waited for here: whoever waits for an answer is told
			// first, and says what happened in the words of what it asked.
			go c.closed()
		}
		return nil, err
	}
	// The MCP library reads what a message holds into values of any kind,
	// each many times the size of the few bytes that spell it: [] is two
	// bytes, and a list in memory. So how many values a message holds is
	// bounded here, before the library reads it, as its size was bounded
	// before this.
	held := func(raw json.RawMessage) error {
		if values(raw) <= plugincontract.MaxMessageValues {
			return nil
		}
		c.once.Do(func() {
			if c.tooMuch != nil {
				c.tooMuch()
			}
		})
		return errTooMuch
	}
	switch msg := msg.(type) {
	case *jsonrpc.Request:
		if msg.IsCall() {
			c.once.Do(func() {
				if c.asked != nil {
					c.asked(msg.Method)
				}
			})
			return nil, errAsked
		}
		if err := held(msg.Params); err != nil {
			return nil, err
		}
	case *jsonrpc.Response:
		if err := held(msg.Result); err != nil {
			return nil, err
		}
		// The first answer is the one to initialize: the first thing asked.
		c.first.Do(func() {
			var init struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if c.speaks != nil && json.Unmarshal(msg.Result, &init) == nil && init.ProtocolVersion != "" {
				c.speaks(init.ProtocolVersion)
			}
		})
	}
	return msg, nil
}

// wasAsked is told that the plugin sent study a request. It ends the
// Connection, off the goroutine that reads from the plugin.
func (s *Connection) wasAsked(method string) {
	s.mu.Lock()
	if s.asked == "" {
		s.asked = s.say(method, 60)
		if s.asked == "" {
			s.asked = "without a method"
		}
	}
	why := s.askedWhy()
	s.mu.Unlock()
	// Ended at once, for whoever asks; stopping the program takes a moment.
	s.end(why, false)
	go s.drop(why)
}

// readNoMCP is told what reading from the plugin failed with, when the
// plugin wrote something that is no MCP message. Nothing more is read from
// such a plugin, so the Connection is over, whether or not a call is under
// way to say so: a command is stopped, and whoever holds the Connection
// finds it ended.
func (s *Connection) readNoMCP(err error) {
	const why = "it wrote something that is no MCP message, and study stopped it"
	s.mu.Lock()
	if s.misread == nil {
		s.misread = err
	}
	over := s.ended != ""
	if !over {
		s.ended = why
	}
	s.mu.Unlock()
	if !over {
		go s.drop(why)
	}
}

// outputEnded is told that the plugin's output ended: a command closed its
// standard output, or a service its connection. Nothing more can be read
// from it, so the Connection is over.
//
// It is for the plugin that goes quiet while nothing is asked of it and
// stays: whoever holds the Connection must find it ended, and a command
// must not be left running behind it. The other two ways an output ends are
// said better by who sees them. A call that is under way reports it with
// what it asked, and stops the plugin itself: so nothing is done here while
// anything is being asked. And a command that exited is reported with how
// it ended, a moment after its output closed: so that moment is waited.
func (s *Connection) outputEnded() {
	const why = "it closed its output while nothing was asked of it, and study stopped it"
	if s.asking.Load() > 0 {
		return
	}
	if s.proc != nil && s.proc.hasExited(collectWait) {
		return
	}
	s.mu.Lock()
	over := s.ended != ""
	if !over {
		s.ended = why
	}
	s.mu.Unlock()
	if !over {
		s.drop(why)
	}
}

// heldTooMuch is told that the plugin sent a message of more values than
// one may hold. It ends the Connection, as a request does.
func (s *Connection) heldTooMuch() {
	why := fmt.Sprintf("it sent a message that holds more than %d values, which no answer of the contract does, and "+
		"study stopped it", plugincontract.MaxMessageValues)
	s.end(why, false)
	go s.drop(why)
}

// values counts what a JSON document holds, generously: every list and
// object, and every member and element after the first of each. It reads
// the bytes once and keeps nothing.
func values(raw []byte) int {
	n, inText := 0, false
	for i := 0; i < len(raw); i++ {
		switch b := raw[i]; {
		case inText && b == '\\':
			i++ // whatever is escaped is text
		case b == '"':
			inText = !inText
		case !inText && (b == '[' || b == '{' || b == ','):
			n++
		}
	}
	return n
}

// askedWhy says why a Connection whose plugin sent a request ended. s.mu
// is held.
func (s *Connection) askedWhy() string {
	return "it sent study a request (" + s.asked + "), which a plugin never does, and study stopped it"
}

// handshake opens the MCP session and checks that a Knowledge base plugin
// is at the other end: it has the seven tools, and its contract tool gives a
// version this study knows. parent is the caller's context and limited the
// same with the deadline of limit, which the whole start has.
func (s *Connection) handshake(parent, limited context.Context, limit time.Duration, transport mcp.Transport) error {
	s.asking.Add(1)
	defer s.asking.Add(-1)
	client := mcp.NewClient(&mcp.Implementation{Name: "study", Title: "Lamplight", Version: s.opts.Version}, &mcp.ClientOptions{
		// The library's own Log would carry text the plugin wrote.
		Logger: slog.New(slog.DiscardHandler),
		// study offers a plugin nothing to call back: no roots, no sampling,
		// no questions to the learner.
		Capabilities:   &mcp.ClientCapabilities{},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	cs, err := within(s, limited, func(ctx context.Context) (*mcp.ClientSession, error) {
		return client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: MCPVersion})
	})
	if err != nil {
		return s.failure(parent, limited, "open an MCP session", "", limit, err)
	}
	s.opened(cs)
	if init := cs.InitializeResult(); init != nil && init.ServerInfo != nil {
		s.server = Server{Name: s.say(init.ServerInfo.Name, 100), Version: s.say(init.ServerInfo.Version, 100)}
	}

	tools, err := within(s, limited, func(ctx context.Context) ([]string, error) {
		// Only the contract's own names are kept, of however many tools
		// with names however long: a page is bounded, and what is kept of
		// every page must be too.
		var names []string
		cursor := ""
		listed := 0
		for range maxToolPages {
			page, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return nil, err
			}
			for _, tool := range page.Tools {
				if slices.Contains(plugincontract.Tools, tool.Name) && !slices.Contains(names, tool.Name) {
					names = append(names, tool.Name)
				}
				if listed++; listed >= MaxTools {
					return names, nil
				}
			}
			if page.NextCursor == "" || page.NextCursor == cursor || len(page.NextCursor) > maxCursorBytes {
				break
			}
			cursor = page.NextCursor
		}
		return names, nil
	})
	if err != nil {
		return s.failure(parent, limited, "list its tools", "", limit, err)
	}
	var missing []string
	for _, tool := range plugincontract.Tools {
		if !slices.Contains(tools, tool) {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return s.notTheContract("it is an MCP server without the " + list(missing) + " " + plural(len(missing), "tool", "tools"))
	}

	data, err := s.callWithin(parent, limited, plugincontract.ToolContract, "say which contract it speaks", limit, struct{}{})
	var refused *Error
	switch {
	case errors.As(err, &refused) && (refused.Code == CodeInvalidArgument || refused.Code == CodeNotFound):
		// The contract tool is asked nothing it could find wrong or
		// missing. A plugin that refuses it so does not speak the contract:
		// its code is not one for whoever asked study for the plugin.
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: refused.Message + ". The contract tool takes no " +
			"arguments and always answers: docs/knowledge-base-contract.md describes the contract"}
	case err != nil:
		return err
	}
	contract, err := plugincontract.ParseContract(data)
	var newer *plugincontract.NewerVersionError
	switch {
	case errors.As(err, &newer):
		return errorf(CodeNewerFormat, "Knowledge base plugin %s speaks version %s of Lamplight's contract, and this version of "+
			"study knows version %d: upgrade study", s.name, clean(newer.Version, 40), plugincontract.Version)
	case err != nil:
		return s.notTheContract("what its contract tool gave back is not what the contract describes: " + s.say(err.Error(), 300))
	}
	s.contract = contract
	return nil
}

// Name is the name the plugin is registered under.
func (s *Connection) Name() string { return s.name }

// Contract is what the plugin said it can do when the Connection was started.
func (s *Connection) Contract() plugincontract.Contract { return s.contract }

// Server is what the plugin calls itself, cleaned for printing.
func (s *Connection) Server() Server { return s.server }

// DataFolder is the folder a plugin started by a command was given to keep
// its index in. It is empty for a plugin reached by URL.
func (s *Connection) DataFolder() string { return s.dataFolder }

// Ended reports whether the Connection can no longer be used: it was
// closed; its command has exited, or study stopped it; or study gave up on
// its plugin, which did not answer in time, sent study a request, or whose
// connection broke. It is true as soon as that is so, whether or not a call
// is under way. Whoever keeps a Connection for more than one request starts
// another when this one has ended.
func (s *Connection) Ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended != ""
}

// Index asks the plugin to index one Source of a collection. The plugin
// answers at once with what it says of the Source now: indexed, when that
// content is indexed already, or indexing, or failed. The work goes on
// afterwards, for as long as the Connection is open when the plugin is a
// command, and Status follows it.
func (s *Connection) Index(ctx context.Context, collection string, source plugincontract.Source) (plugincontract.SourceStatus, error) {
	in := plugincontract.IndexInput{Collection: collection, Source: source}
	if err := in.Validate(); err != nil {
		return plugincontract.SourceStatus{}, s.notSent(plugincontract.ToolIndex, err)
	}
	if !slices.Contains(s.contract.Sources, source.Kind) {
		return plugincontract.SourceStatus{}, errorf(CodeFailedPrecondition, "Knowledge base plugin %s does not index a Source "+
			"of the kind %s: it indexes %s", s.name, source.Kind, list(s.contract.Sources))
	}
	var out plugincontract.IndexResult
	err := s.ask(ctx, plugincontract.ToolIndex, s.opts.CallTimeout, in, &out, func() error { return out.Validate(in) })
	if err != nil {
		return plugincontract.SourceStatus{}, err
	}
	return s.tidy(out.Source), nil
}

// Status asks what the plugin says of the Sources of a collection. A Source
// it does not list is not indexed.
func (s *Connection) Status(ctx context.Context, collection string) ([]plugincontract.SourceStatus, error) {
	in := plugincontract.StatusInput{Collection: collection}
	if err := in.Validate(); err != nil {
		return nil, s.notSent(plugincontract.ToolStatus, err)
	}
	var out plugincontract.StatusResult
	if err := s.ask(ctx, plugincontract.ToolStatus, s.opts.CallTimeout, in, &out, func() error { return out.Validate() }); err != nil {
		return nil, err
	}
	sources := make([]plugincontract.SourceStatus, 0, len(out.Sources))
	for _, source := range out.Sources {
		sources = append(sources, s.tidy(source))
	}
	return sources, nil
}

// Cancel asks the plugin to stop indexing a Source, and reports whether it
// was indexing it. What was indexed before stays.
//
// Like every call, it asks nothing when ctx is cancelled already: a command
// that was stopped cancels with a context of its own.
func (s *Connection) Cancel(ctx context.Context, collection, source string) (stopped bool, err error) {
	in := plugincontract.CancelInput{Collection: collection, Source: source}
	if err := in.Validate(); err != nil {
		return false, s.notSent(plugincontract.ToolCancel, err)
	}
	var out plugincontract.CancelResult
	if err := s.ask(ctx, plugincontract.ToolCancel, s.opts.CallTimeout, in, &out, nil); err != nil {
		return false, err
	}
	return out.Stopped, nil
}

// Search asks the plugin for the Passages of a collection that answer a
// query, best first, limit at most. A nil sources searches the whole
// collection; a list limits the search to those Sources, and an empty one
// finds nothing, without asking.
//
// A Passage's text is the Source's own, as the plugin gave it. It is checked
// for its length and not cleaned: whoever prints it prints it as text from a
// file.
//
// The result's Notice is what the plugin says the search did less than it
// was set to, when it says so. It is the plugin's own text, and is cleaned
// as a reason is: on one line, with nothing a terminal would interpret, and
// cut to plugincontract.MaxNoticeBytes. Whatever else the plugin said of
// the search, in fields the contract does not name, is not read.
func (s *Connection) Search(ctx context.Context, collection, query string, limit int, sources []string) (plugincontract.SearchResult, error) {
	if sources != nil && len(sources) == 0 {
		// The contract reads an empty list as a mistake, and a missing one
		// as "every Source": neither is what the caller means.
		if err := (plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit}).Validate(); err != nil {
			return plugincontract.SearchResult{}, s.notSent(plugincontract.ToolSearch, err)
		}
		return plugincontract.SearchResult{Passages: []plugincontract.ScoredPassage{}}, nil
	}
	in := plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit, Sources: sources}
	if err := in.Validate(); err != nil {
		return plugincontract.SearchResult{}, s.notSent(plugincontract.ToolSearch, err)
	}
	var out plugincontract.SearchResult
	err := s.ask(ctx, plugincontract.ToolSearch, s.opts.SearchTimeout, in, &out, func() error {
		if err := out.Validate(in); err != nil {
			return err
		}
		for _, passage := range out.Passages {
			if err := s.located(passage.Passage); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return plugincontract.SearchResult{}, err
	}
	if out.Passages == nil {
		out.Passages = []plugincontract.ScoredPassage{}
	}
	out.Notice = s.say(out.Notice, plugincontract.MaxNoticeBytes)
	return out, nil
}

// Passage asks the plugin for one Passage of a collection again, by the id
// a search gave. An id the collection does not have is not_found.
func (s *Connection) Passage(ctx context.Context, collection, id string) (plugincontract.Passage, error) {
	in := plugincontract.PassageInput{Collection: collection, ID: id}
	if err := in.Validate(); err != nil {
		return plugincontract.Passage{}, s.notSent(plugincontract.ToolPassage, err)
	}
	var out plugincontract.PassageResult
	err := s.ask(ctx, plugincontract.ToolPassage, s.opts.CallTimeout, in, &out, func() error {
		if err := out.Validate(in); err != nil {
			return err
		}
		return s.located(out.Passage)
	})
	if err != nil {
		return plugincontract.Passage{}, err
	}
	return out.Passage, nil
}

// Diagnose asks the plugin to test what it depends on, an endpoint it asks
// over a network say, and returns what it found: a finding for each, and
// none for a plugin that depends on nothing of the kind. A finding that is
// not all right is no error: the plugin answered, and said so.
//
// What it answers is the learner's to read, at a command line, and it may
// take the ten seconds the contract gives it: whoever calls it does so for
// study doctor or study knowledge-base check, and no MCP tool leads here.
//
// A finding's message is the plugin's text. It is returned cleaned, as a
// reason is: one line, with nothing a terminal would interpret, cut to
// plugincontract.MaxFindingBytes, and without the parts of a registered
// address that may be a key. A name is shown as it is, so one that holds
// such a part is no name: the answer breaks the contract.
//
// An error is said of this tool alone, and is failed_precondition whatever
// the plugin refused with, or canceled (untested).
func (s *Connection) Diagnose(ctx context.Context) ([]plugincontract.Finding, error) {
	var out plugincontract.DiagnoseResult
	err := s.ask(ctx, plugincontract.ToolDiagnose, s.opts.DiagnoseTimeout, struct{}{}, &out, func() error { return out.Validate() })
	if err != nil {
		return nil, s.untested(err)
	}
	findings := make([]plugincontract.Finding, 0, len(out.Findings))
	for i, finding := range out.Findings {
		if s.say(finding.Name, plugincontract.MaxKindBytes) != finding.Name {
			// Not shown: what it holds may be the key.
			return nil, s.untested(&Error{Code: CodeFailedPrecondition, breaks: fmt.Sprintf("findings[%d] has a name that holds a "+
				"part of the address study reaches the plugin at, which may be a key: nothing a plugin depends on is named so", i)})
		}
		finding.Message = s.say(finding.Message, plugincontract.MaxFindingBytes)
		if finding.Message == "" {
			// The contract's check passed it, so it holds something that
			// shows, and nothing of that is left: it stood behind more of
			// what shows nothing than study reads of a message. The learner
			// would read a name and no sentence.
			return nil, s.untested(&Error{Code: CodeFailedPrecondition, breaks: fmt.Sprintf("the finding %s has a message that "+
				"shows nothing", finding.Name)})
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

// untested is the error of a plugin that did not say what it found of what
// it depends on, given the error of asking it.
//
// It is said of the diagnose tool alone. A plugin that is asked it started
// and said which contract it speaks, and its other tools are none the worse
// for this one: "does not speak Lamplight's contract" would be said of a
// plugin study goes on using. So an answer that breaks the contract is said
// to be that, with what of it does.
//
// It names nothing to run. What the client says of another tool that failed
// ends with a command that shows more, study knowledge-base check among
// them, and that command asks this tool again and says the same.
//
// And it is failed_precondition whatever code the plugin refused with: the
// tool is asked nothing a plugin could find wrong or missing, so
// invalid_argument and not_found would be said of what the learner typed,
// which was the plugin's name and was right. A call that was stopped stays
// canceled.
func (s *Connection) untested(err error) error {
	var failed *Error
	if !errors.As(err, &failed) || failed.Code == CodeCanceled {
		return err
	}
	said := &Error{Code: CodeFailedPrecondition, Err: err, Late: failed.Late, Message: strings.TrimSuffix(failed.Message, failed.advice)}
	switch {
	case failed.breaks != "":
		said.Message = "the diagnose tool of Knowledge base plugin " + s.name + " gave an answer that breaks the contract: " +
			failed.breaks + ". docs/knowledge-base-contract.md describes the contract"
	case failed.broke != "":
		// The plugin had started and said which contract it speaks: that it
		// "does not speak MCP" would be said of a plugin study goes on using.
		said.Message = "Knowledge base plugin " + s.name + " did not answer its diagnose tool: " + failed.broke
	}
	return said
}

// located checks that a Passage's location is of a kind the plugin said it
// gives: what a location means is decided by its kind, and study was told
// which kinds to expect.
func (s *Connection) located(passage plugincontract.Passage) error {
	if l := passage.Location; l != nil && !slices.Contains(s.contract.Locations, l.Kind) {
		return fmt.Errorf("Passage %s has a location of the kind %s, and the plugin's contract lists %s", passage.ID, l.Kind,
			list(s.contract.Locations))
	}
	return nil
}

// Close ends the Connection. A command is stopped, with everything it
// started that stayed in its process group: its input is closed, which asks
// it to exit, and it is signalled and then killed if it does not, within
// three times Options.StopGrace. A plugin reached by URL is told the session
// is over. Close can be called more than once, and always returns nil.
func (s *Connection) Close() error {
	s.shut(true)
	return nil
}

// GiveUp ends the Connection with a plugin that did not answer in the time
// it had, for a caller that kept that time itself, with a deadline shorter
// than the Connection's own: a command is killed at once, since it was given
// its time already, and a service is not waited for. Close is for a plugin
// that answered.
func (s *Connection) GiveUp() {
	s.drop("it did not answer in time, and study stopped it")
}

// shut ends the Connection, once. politely says whether the plugin is one to
// wait for: a service that answers is told the session is over, and that
// may take it a moment; one that stopped answering is not waited for again.
func (s *Connection) shut(politely bool) {
	s.closeOnce.Do(func() {
		s.end("its connection was closed", false)
		if s.proc != nil {
			s.proc.Stop()
			s.opts.Logger.Debug("stopped a Knowledge base plugin", "name", s.name, "how", s.proc.how())
		}
		closeIdle := func() {
			if s.http != nil {
				s.http.base.CloseIdleConnections()
			}
		}
		s.mu.Lock()
		cs := s.cs
		s.mu.Unlock()
		if cs == nil {
			closeIdle()
			return
		}
		// The library closes a connection once nothing is under way on it,
		// and tells a service that the session is over. Neither may hold
		// study up.
		done := make(chan struct{})
		go func() {
			_ = cs.Close()
			closeIdle()
			close(done)
		}()
		if politely {
			timer := time.NewTimer(3 * s.opts.StopGrace)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
			}
		}
	})
}

// opened records the MCP session once it is open. A Connection that ended
// while it was being opened, because its plugin sent a request at once,
// closes it here: nothing else would.
func (s *Connection) opened(cs *mcp.ClientSession) {
	s.mu.Lock()
	s.cs = cs
	ended := s.ended != "" && !s.exited
	s.mu.Unlock()
	if ended {
		go func() { _ = cs.Close() }()
	}
}

// over is the error for a Connection that can no longer be used, or nil for
// one that can.
func (s *Connection) over() *Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended == "" {
		return nil
	}
	return errorf(CodeFailedPrecondition, "Knowledge base plugin %s can no longer be asked: %s", s.name, s.ended)
}

// end records why the Connection can no longer be used, the first time.
// exited says that the reason is that the command exited by itself.
func (s *Connection) end(why string, exited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended == "" {
		s.ended, s.exited = why, exited
	}
}

// drop ends a Connection whose plugin cannot be relied on any more: one that
// did not answer in time, that sent a request, or whose connection broke. A
// command is killed at once, since it was given its time already.
func (s *Connection) drop(why string) {
	s.end(why, false)
	if s.proc != nil {
		s.proc.Kill()
	}
	s.shut(false)
}

// ask calls one of the plugin's tools and reads its answer into out, which
// check, when given, then holds to the contract.
func (s *Connection) ask(ctx context.Context, tool string, limit time.Duration, in, out any, check func() error) error {
	if ctx.Err() != nil {
		return s.canceled(ctx)
	}
	limited, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	data, err := s.callWithin(ctx, limited, tool, "answer its "+tool+" tool", limit, in)
	if err != nil {
		return err
	}
	// Decode holds the answer to its shape: read without that, a field the
	// contract requires and the answer lacks is nothing, a cancel that
	// stopped nothing and a score of 0. What is wrong may be said with a
	// key as the plugin spelled it.
	if err := plugincontract.Decode(data, out); err != nil {
		return s.misanswered(tool, s.say(err.Error(), 300))
	}
	// What is wrong with an answer is said with what the answer held, a
	// name or an id: the plugin's text, like any other.
	if check != nil {
		if err := check(); err != nil {
			return s.misanswered(tool, s.say(s.wrong(data, out, check, err), 300))
		}
	}
	return nil
}

// misanswered is the error for a tool whose answer is not what the contract
// describes. breaks says what of the answer is not, and is kept alone too.
func (s *Connection) misanswered(tool, breaks string) *Error {
	refusal := s.notTheContract("what its " + tool + " tool gave back is not what the contract describes: " + breaks)
	refusal.breaks = breaks
	return refusal
}

// callWithin calls a tool and returns the JSON object of its result. parent
// is the caller's context and limited the same with study's own deadline;
// doing says what the plugin is asked to do, for the message when it does
// not.
func (s *Connection) callWithin(parent, limited context.Context, tool, doing string, limit time.Duration, in any) (json.RawMessage, error) {
	if over := s.over(); over != nil {
		return nil, over
	}
	s.asking.Add(1)
	defer s.asking.Add(-1)
	res, err := within(s, limited, func(ctx context.Context) (*mcp.CallToolResult, error) {
		return s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: in})
	})
	if err != nil {
		return nil, s.failure(parent, limited, doing, tool, limit, err)
	}
	if res.NeedsInput() {
		refusal := s.notTheContract("its " + tool + " tool asks study for more input, which no tool of the contract does")
		refusal.breaks = "it asks study for more input, which no tool of the contract does"
		return nil, refusal
	}
	data, isJSON := payload(res)
	if res.IsError {
		var refusal plugincontract.ErrorResult
		if !isJSON || plugincontract.Decode(data, &refusal) != nil || (refusal.Error.Code == "" && refusal.Error.Message == "") {
			// An error that is not the contract's: the text is all there is.
			refusal.Error = plugincontract.ErrorBody{Message: text(res)}
		}
		return nil, s.toolError(tool, refusal.Error)
	}
	if !isJSON {
		refusal := s.notTheContract("its " + tool + " tool gave back no JSON object: neither as structured content nor as text")
		refusal.breaks = "it is no JSON object, neither as structured content nor as text"
		return nil, refusal
	}
	return data, nil
}

// payload is the JSON object a tool's result carries: its structured
// content, or else the text of its first block when that is JSON.
func payload(res *mcp.CallToolResult) (json.RawMessage, bool) {
	switch structured := res.StructuredContent.(type) {
	case nil:
	case json.RawMessage:
		return structured, true
	default:
		// The MCP library hands over what it read, and it is written out
		// again to be read as the contract's. Written as it came, and not
		// as a web page would want it: escaping each < and & makes six
		// bytes of one.
		var data bytes.Buffer
		encoder := json.NewEncoder(&data)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(structured); err == nil {
			return data.Bytes(), true
		}
	}
	if t := text(res); strings.HasPrefix(strings.TrimSpace(t), "{") && json.Valid([]byte(t)) {
		return json.RawMessage(t), true
	}
	return nil, false
}

// text is the text of a result's first block, when it is text.
func text(res *mcp.CallToolResult) string {
	if len(res.Content) > 0 {
		if block, ok := res.Content[0].(*mcp.TextContent); ok {
			return block.Text
		}
	}
	return ""
}

// say is text the plugin wrote, as study repeats it: cleaned, cut to max
// bytes, and without the parts of a registered address that may be a key,
// which a service can send back as easily as anything else (parts.without).
//
// Only the start of a long text is looked at. What that start ends in is
// left out when it may be the start of a part of the address: all of a key
// but its last byte is no key, and behind enough white space clean would
// bring it to the front.
func (s *Connection) say(text string, max int) string { return s.sayStart(text, max, false) }

// sayStart is say for a text that may be the start of a longer one already,
// as cut says: what a service wrote when it refused a request is kept up to
// a few hundred bytes.
func (s *Connection) sayStart(text string, max int, cut bool) string {
	start := plugincontract.Cut(text, 16*max+4096)
	return clean(s.parts.without(start, cut || len(start) < len(text)), max)
}

// wrong is what is wrong with an answer, in the words of the contract's
// check, for saying.
//
// Those words repeat what the answer held, a name, a state or an id, cut
// short and with what is not ASCII written as escapes. A part of the
// address in such a value is then no longer what say looks for: a key cut
// in two, or one with an accent in it, would be shown. So when the answer
// holds a part of the address anywhere, it is checked once more without
// it, and what is wrong is said of that: of the same answer, as it is
// shown. An answer that holds none is said of as it is.
func (s *Connection) wrong(data json.RawMessage, out any, check func() error, err error) string {
	if s.parts == nil {
		return err.Error()
	}
	shown, changed := s.parts.withoutIn(data)
	if !changed {
		return err.Error()
	}
	if plugincontract.Decode(shown, out) == nil {
		if again := check(); again != nil {
			return again.Error()
		}
	}
	return "it holds a value the contract does not have, with a part of the address study reaches the plugin at in it"
}

// tidy cleans what a plugin wrote of a Source for people to read.
func (s *Connection) tidy(source plugincontract.SourceStatus) plugincontract.SourceStatus {
	source.Reason = s.say(source.Reason, plugincontract.MaxTextBytes)
	return source
}

// toolError is the error for a call the plugin refused or failed.
func (s *Connection) toolError(tool string, body plugincontract.ErrorBody) *Error {
	said := s.say(body.Message, plugincontract.MaxTextBytes)
	if said == "" {
		said = "it gave no reason"
	}
	code := StudyCode(body.Code)
	var refusal *Error
	switch body.Code {
	case plugincontract.CodeInvalidArgument:
		refusal = errorf(code, "Knowledge base plugin %s does not take what its %s tool was given: %s", s.name, tool, said)
	case plugincontract.CodeNotFound:
		refusal = errorf(code, "Knowledge base plugin %s does not have what its %s tool was asked for: %s", s.name, tool, said)
	case plugincontract.CodeUnsupported:
		refusal = errorf(code, "Knowledge base plugin %s cannot do what its %s tool was asked: %s", s.name, tool, said)
	default:
		refusal = errorf(code, "Knowledge base plugin %s failed in its %s tool: %s", s.name, tool, said)
		refusal.advice = ". The plugin's own messages may say more; study knowledge-base check " + s.name +
			" shows whether it still starts"
		refusal.Message += refusal.advice
	}
	refusal.AboutTheCall = true
	return refusal
}

// within runs f, which talks to the plugin, and returns when f does, or
// about a second after ctx ended when f is stuck: the library returns at
// once then, unless it is blocked writing to a command that reads nothing.
// Such a command is killed, which frees it, and the Connection is over.
func within[T any](s *Connection, ctx context.Context, f func(context.Context) (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := f(ctx)
		done <- result{value, err}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-ctx.Done():
	}
	timer := time.NewTimer(unstick)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.value, r.err
	case <-timer.C:
	}
	s.drop("it read nothing of what study wrote to it, and study stopped it")
	var none T
	return none, ctx.Err()
}

// canceled is the error for a request that was stopped before the plugin
// answered.
func (s *Connection) canceled(ctx context.Context) *Error {
	return &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base plugin " +
		s.name + " answered"}
}

// notSent is the error for a request study did not send, because it is not
// one the contract allows.
func (s *Connection) notSent(tool string, err error) *Error {
	return &Error{Code: CodeInvalidArgument, Err: err, AboutTheCall: true, Message: "Knowledge base plugin " + s.name +
		" was not asked (" + tool + "): " + err.Error()}
}

// notTheContract is the error for a plugin that answers, and not as the
// contract says.
func (s *Connection) notTheContract(what string) *Error {
	return errorf(CodeFailedPrecondition, "Knowledge base plugin %s does not speak Lamplight's contract: %s. "+
		"docs/knowledge-base-contract.md describes the contract", s.name, what)
}

// failure explains why the plugin gave no result. parent is the caller's
// context and limited the one with study's deadline of limit; doing says
// what the plugin was asked to do, and tool which tool was called, if one
// was.
func (s *Connection) failure(parent, limited context.Context, doing, tool string, limit time.Duration, err error) *Error {
	s.mu.Lock()
	asked, askedWhy, ended, exited, misread := s.asked, s.askedWhy(), s.ended, s.exited, s.misread
	s.mu.Unlock()
	answered := protocolError(err)
	switch {
	case asked != "":
		// Whatever else went wrong went wrong because of this.
		s.drop(askedWhy)
		return errorf(CodeFailedPrecondition, "Knowledge base plugin %s sent study a request (%s), and study stopped it: a "+
			"plugin answers what it is asked and asks nothing, since study offers it nothing to call. "+
			"docs/knowledge-base-contract.md describes the contract", s.name, asked)
	case parent.Err() != nil:
		// The request was stopped. The plugin was told, and the Connection
		// stays as it is: whoever holds it closes it.
		return s.canceled(parent)
	case limited.Err() != nil:
		if s.proc != nil {
			wrote := s.wrote()
			s.drop("it did not answer in time, and study stopped it")
			late := errorf(CodeFailedPrecondition, "Knowledge base plugin %s did not %s within %s, so study stopped it.%s",
				s.name, doing, limit, wrote)
			late.advice = " Run its command in a terminal to see what it waits for: study knowledge-base list shows the command"
			late.Message += late.advice
			late.Late = true
			return late
		}
		s.drop("it did not answer in time")
		late := errorf(CodeFailedPrecondition, "Knowledge base plugin %s did not %s within %s.", s.name, doing, limit)
		late.advice = " Check that the service at " + Address(s.address) + " is running and is not busy: study knowledge-base " +
			"list shows its whole address"
		late.Message += late.advice
		late.Late = true
		return late
	case ended != "" && !exited && misread == nil:
		// The Connection was closed, or given up, under this call: that is
		// what happened to this call too. What the plugin wrote that was no
		// MCP message is said below, with what it was.
		return s.over()
	case answered != nil:
		// The plugin answered, with an error of the protocol's own.
		switch {
		case tool == "":
			return s.notTheContract("it would not " + doing + " (" + s.say(answered.Message, plugincontract.MaxTextBytes) + ")")
		case answered.Code == jsonrpc.CodeInvalidParams:
			return s.toolError(tool, plugincontract.ErrorBody{Code: plugincontract.CodeInvalidArgument, Message: answered.Message})
		default:
			return s.toolError(tool, plugincontract.ErrorBody{Code: plugincontract.CodeInternal, Message: answered.Message})
		}
	}
	// The connection broke: nothing more can be asked on it.
	if s.proc == nil {
		why := reason(err, s.address)
		if refusal, cut := s.http.refused(); refusal != "" {
			// What the service itself said of why is worth more than the
			// name of its status.
			why = s.sayStart(refusal, 300, cut)
		}
		s.drop("its connection broke")
		advice := fmt.Sprintf(" study speaks MCP revision %s over streamable HTTP. Check that the service is running, "+
			"that it speaks that revision or an earlier one, and that the address is the one of its MCP endpoint: study "+
			"knowledge-base list shows it, and study knowledge-base add %s --replace --url <URL> changes it", MCPVersion, s.name)
		return &Error{Code: CodeFailedPrecondition, Err: err, advice: advice, Message: fmt.Sprintf("Knowledge base plugin %s "+
			"does not answer as an MCP server at %s (%s).", s.name, Address(s.address), why) + advice,
			broke: fmt.Sprintf("the service at %s did not answer as an MCP server does (%s)", Address(s.address), why)}
	}
	// What was read and was no MCP message is why, whatever the library
	// came to report first.
	if misread != nil {
		err = misread
	}
	// A command that exited closes its output, and a moment may pass before
	// it is collected.
	exited = s.proc.hasExited(collectWait)
	how, wrote := s.proc.how(), s.wrote()
	s.drop("its connection broke")
	const look = " Run its command in a terminal to see why: study knowledge-base list shows the command"
	gone := func(message string) *Error {
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: message + look, advice: look}
	}
	switch {
	case !hungUp(err):
		// Something was read, and it was no MCP message. That the program
		// exited since says nothing: its pipes were closed on it.
		what := reason(rootCause(err), "")
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s does not speak MCP "+
			"on its standard output: study read something there that is no MCP message (%s), and stopped it.%s A plugin "+
			"writes only MCP messages on its standard output, each of %d MiB at most, and anything else on its standard error",
			s.name, what, wrote, plugincontract.MaxMessageBytes>>20),
			broke: fmt.Sprintf("study read something on its standard output that is no MCP message (%s), and stopped it.%s",
				what, wrote)}
	case exited:
		return gone(fmt.Sprintf("Knowledge base plugin %s stopped before it could %s (%s).%s", s.name, doing, how, wrote))
	}
	return gone(fmt.Sprintf("Knowledge base plugin %s closed its standard output before it could %s, so study stopped it.%s",
		s.name, doing, wrote))
}

// The MCP library reports what went wrong on its own side with errors of the
// protocol's type too, under codes of its own: the request was not sent, or
// the connection is closing. They are not answers.
var libraryCodes = []int64{-32001, -32003, -32004, -32005}

// protocolError returns the error the plugin answered a request with, when
// err holds one: a JSON-RPC error that is not one of the library's own.
func protocolError(err error) *jsonrpc.Error {
	if err == nil {
		return nil
	}
	if answered, ok := err.(*jsonrpc.Error); ok && answered != nil && !slices.Contains(libraryCodes, answered.Code) {
		return answered
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() error }:
		return protocolError(wrapped.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			if answered := protocolError(inner); answered != nil {
				return answered
			}
		}
	}
	return nil
}

// hungUp reports whether a connection to a command broke because the
// command went away or closed its end, and not because of what it wrote:
// its output ended, or its input no longer takes anything.
func hungUp(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, mcp.ErrConnectionClosed) ||
		errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE)
}

// rootCause is the innermost error of a chain.
func rootCause(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		err = inner
	}
}

// wrote is a sentence with the end of what a command wrote on its standard
// error, cleaned, or nothing when it wrote nothing. It is the end that says
// why a program failed: the exception under a traceback, not its first line.
func (s *Connection) wrote() string {
	if s.proc == nil {
		return ""
	}
	said := cleanEnd(s.proc.wrote(), StderrShown)
	if said == "" {
		return ""
	}
	return " The last it wrote on its standard error: " + said + "."
}

// list joins words as a sentence lists them.
func list(words []string) string {
	switch len(words) {
	case 0:
		return "nothing"
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
