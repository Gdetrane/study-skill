// Package pluginclient is study as the MCP client of a Knowledge base plugin
// (ADR-0012): it starts a registered command, or reaches a registered URL,
// checks that what answers speaks Lamplight's contract, and asks it to
// index, to say what is indexed, to stop, to search and to give a Passage
// again.
//
// A command is started as the registry's rules need it to be. The program is
// the file pluginregistry.OpenProgram checked, not its path resolved again;
// it runs without a shell, in the registry's folder and never in the Topic
// or the Study home study was started in, with pipes of its own and never
// study's standard input, in a session of its own, with the learner's
// environment less GIT_* and less whatever names something inside the Study
// home, and with a folder of its own under the Study home's .lamplight,
// which LAMPLIGHT_PLUGIN_DATA names. It is stopped when its Connection is
// closed, with everything it started.
//
// A plugin is not trusted with study's time, memory or terminal. Starting,
// every call and stopping have a deadline, so no plugin makes study hang.
// One message is read up to a limit, and every answer is checked against
// the contract before it is returned. Text a plugin wrote reaches an error
// only cleaned (see clean); a Passage's text is the Source's own and is
// returned as it is, for the adapters to print as they print any text from
// a file.
//
// The core calls this package, and the MCP server reaches a plugin only
// through the core.
package pluginclient

import (
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
	// reached, and to say which contract it speaks.
	DefaultStartTimeout = 30 * time.Second
	// DefaultCallTimeout is how long it has to answer index, status, cancel
	// or passage. None of them does the work itself: index answers at once.
	DefaultCallTimeout = 30 * time.Second
	// DefaultSearchTimeout is how long a search may take: a plugin may have
	// a model embed the query and another reorder what it found.
	DefaultSearchTimeout = 60 * time.Second
	// DefaultStopGrace is how long a command has to exit once its input is
	// closed, and again after SIGTERM, before it is killed.
	DefaultStopGrace = 2 * time.Second
)

// MCPVersion is the MCP version study asks a plugin for: the last one with
// the initialize handshake every MCP library has. A plugin may answer with
// an earlier one.
const MCPVersion = "2025-11-25"

// MaxTools is how many tools of a plugin study lists before it stops
// looking for the contract's six.
const MaxTools = 1000

// StderrKept is how much of the end of what a command wrote on its standard
// error study keeps, to say when the plugin fails.
const StderrKept = 8 << 10

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
	// Environ is the environment a command is started with, before study
	// takes out of it what a plugin must not get (see childEnviron). Nil is
	// the environment of this process.
	Environ []string
	// Logger receives the Log. Nil discards it.
	Logger *slog.Logger
	// StartTimeout, CallTimeout, SearchTimeout and StopGrace replace the
	// defaults above when they are not zero.
	StartTimeout  time.Duration
	CallTimeout   time.Duration
	SearchTimeout time.Duration
	StopGrace     time.Duration
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
		{&o.SearchTimeout, DefaultSearchTimeout}, {&o.StopGrace, DefaultStopGrace},
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
	// repeat.
	address string
	opts    Options

	// proc is the command, when the plugin is one.
	proc *process
	// closeIdle closes the connections kept to a plugin reached by URL.
	closeIdle func()
	cs        *mcp.ClientSession

	contract   plugincontract.Contract
	server     Server
	dataFolder string

	mu sync.Mutex
	// ended says why the Connection can no longer be used, once it cannot.
	ended     string
	closeOnce sync.Once
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
	var transport mcp.Transport
	switch {
	case len(plugin.Command) > 0:
		folder, err := makeDataFolder(env.StudyHome, plugin.Name)
		if err != nil {
			return nil, err
		}
		proc, err := startProcess(env, plugin, folder, s.opts.Environ, s.opts.StopGrace)
		if err != nil {
			return nil, err
		}
		s.proc, s.dataFolder = proc, folder
		s.opts.Logger.Debug("started a Knowledge base plugin", "name", s.name, "pid", proc.cmd.Process.Pid)
		transport = &mcp.IOTransport{Reader: proc.stdout, Writer: proc.stdin, MaxLineLength: plugincontract.MaxMessageBytes}
	case plugin.URL != "":
		s.address = plugin.URL
		transport, s.closeIdle = urlTransport(plugin.URL)
	default:
		return nil, errorf(CodeInternal, "Knowledge base plugin %s is registered with neither a command nor a URL", plugin.Name)
	}
	if err := s.handshake(ctx, transport); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// handshake opens the MCP session and checks that a Knowledge base plugin
// is at the other end: it has the six tools, and its contract tool gives a
// version this study knows. All of it has one deadline.
func (s *Connection) handshake(ctx context.Context, transport mcp.Transport) error {
	limit := s.opts.StartTimeout
	limited, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

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
		return s.failure(ctx, limited, "open an MCP session", "", limit, err)
	}
	s.cs = cs
	if init := cs.InitializeResult(); init != nil && init.ServerInfo != nil {
		s.server = Server{Name: clean(init.ServerInfo.Name, 100), Version: clean(init.ServerInfo.Version, 100)}
	}

	tools, err := within(s, limited, func(ctx context.Context) ([]string, error) {
		var names []string
		for tool, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return nil, err
			}
			if names = append(names, tool.Name); len(names) >= MaxTools {
				break
			}
		}
		return names, nil
	})
	if err != nil {
		return s.failure(ctx, limited, "list its tools", "", limit, err)
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

	data, err := s.callWithin(ctx, limited, plugincontract.ToolContract, "say which contract it speaks", limit, struct{}{})
	if err != nil {
		return err
	}
	contract, err := plugincontract.ParseContract(data)
	var newer *plugincontract.NewerVersionError
	switch {
	case errors.As(err, &newer):
		return errorf(CodeNewerFormat, "Knowledge base plugin %s speaks version %d of Lamplight's contract, and this version of "+
			"study knows version %d: upgrade study", s.name, newer.Version, plugincontract.Version)
	case err != nil:
		return s.notTheContract("what its contract tool gave back is not what the contract describes: " + err.Error())
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

// Ended reports whether the Connection can no longer be used: it was closed,
// or study gave up on its plugin, which did not answer in time or whose
// connection broke. Whoever keeps a Connection for more than one request
// starts another when this one has ended.
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
	return tidy(out.Source), nil
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
		sources = append(sources, tidy(source))
	}
	return sources, nil
}

// Cancel asks the plugin to stop indexing a Source, and reports whether it
// was indexing it. What was indexed before stays.
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
func (s *Connection) Search(ctx context.Context, collection, query string, limit int, sources []string) ([]plugincontract.ScoredPassage, error) {
	if sources != nil && len(sources) == 0 {
		// The contract reads an empty list as a mistake, and a missing one
		// as "every Source": neither is what the caller means.
		if err := (plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit}).Validate(); err != nil {
			return nil, s.notSent(plugincontract.ToolSearch, err)
		}
		return []plugincontract.ScoredPassage{}, nil
	}
	in := plugincontract.SearchInput{Collection: collection, Query: query, Limit: limit, Sources: sources}
	if err := in.Validate(); err != nil {
		return nil, s.notSent(plugincontract.ToolSearch, err)
	}
	var out plugincontract.SearchResult
	err := s.ask(ctx, plugincontract.ToolSearch, s.opts.SearchTimeout, in, &out, func() error { return out.Validate(in) })
	if err != nil {
		return nil, err
	}
	if out.Passages == nil {
		out.Passages = []plugincontract.ScoredPassage{}
	}
	return out.Passages, nil
}

// Passage asks the plugin for one Passage of a collection again, by the id
// a search gave. An id the collection does not have is not_found.
func (s *Connection) Passage(ctx context.Context, collection, id string) (plugincontract.Passage, error) {
	in := plugincontract.PassageInput{Collection: collection, ID: id}
	if err := in.Validate(); err != nil {
		return plugincontract.Passage{}, s.notSent(plugincontract.ToolPassage, err)
	}
	var out plugincontract.PassageResult
	err := s.ask(ctx, plugincontract.ToolPassage, s.opts.CallTimeout, in, &out, func() error { return out.Validate(in) })
	if err != nil {
		return plugincontract.Passage{}, err
	}
	return out.Passage, nil
}

// Close ends the Connection. A command is stopped, with everything it started:
// its input is closed, which asks it to exit, and it is signalled and then
// killed if it does not, within three times Options.StopGrace. A plugin
// reached by URL is told the session is over. Close can be called more than
// once, and always returns nil.
func (s *Connection) Close() error {
	s.shut(true)
	return nil
}

// shut ends the Connection, once. politely says whether the plugin is one to
// wait for: a service that answers is told the session is over, and that
// may take it a moment; one that stopped answering is not waited for again.
func (s *Connection) shut(politely bool) {
	s.closeOnce.Do(func() {
		s.end("its connection was closed")
		if s.proc != nil {
			s.proc.stop()
			s.opts.Logger.Debug("stopped a Knowledge base plugin", "name", s.name, "how", s.proc.how())
		}
		if s.cs != nil {
			// The library closes a connection once nothing is under way on
			// it, and tells a service that the session is over. Neither may
			// hold study up.
			done := make(chan struct{})
			go func() {
				_ = s.cs.Close()
				if s.closeIdle != nil {
					s.closeIdle()
				}
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
		} else if s.closeIdle != nil {
			s.closeIdle()
		}
	})
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
func (s *Connection) end(why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended == "" {
		s.ended = why
	}
}

// drop ends a Connection whose plugin cannot be relied on any more: one that
// did not answer in time, or whose connection broke. A command is killed at
// once, since it was given its time already.
func (s *Connection) drop(why string) {
	s.end(why)
	if s.proc != nil {
		s.proc.kill()
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
	if err := plugincontract.Decode(data, out); err != nil {
		return s.notTheContract("what its " + tool + " tool gave back is not what the contract describes: " + err.Error())
	}
	if check != nil {
		if err := check(); err != nil {
			return s.notTheContract("what its " + tool + " tool gave back is not what the contract describes: " + err.Error())
		}
	}
	return nil
}

// callWithin calls a tool and returns the JSON object of its result. parent
// is the caller's context and limited the same with study's own deadline;
// doing says what the plugin is asked to do, for the message when it does
// not.
func (s *Connection) callWithin(parent, limited context.Context, tool, doing string, limit time.Duration, in any) (json.RawMessage, error) {
	if over := s.over(); over != nil {
		return nil, over
	}
	res, err := within(s, limited, func(ctx context.Context) (*mcp.CallToolResult, error) {
		return s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: in})
	})
	if err != nil {
		return nil, s.failure(parent, limited, doing, tool, limit, err)
	}
	if res.NeedsInput() {
		return nil, s.notTheContract("its " + tool + " tool asks study for more input, which no tool of the contract does")
	}
	data, isJSON := payload(res)
	if res.IsError {
		var refusal plugincontract.ErrorResult
		if !isJSON || plugincontract.Decode(data, &refusal) != nil || (refusal.Error.Code == "" && refusal.Error.Message == "") {
			// An error that is not the contract's: the text is all there is.
			refusal.Error = plugincontract.ErrorBody{Message: text(res)}
		}
		return nil, toolError(s.name, tool, refusal.Error)
	}
	if !isJSON {
		return nil, s.notTheContract("its " + tool + " tool gave back no JSON object: neither as structured content nor as text")
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
		if data, err := json.Marshal(structured); err == nil {
			return data, true
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

// tidy cleans what a plugin wrote of a Source for people to read.
func tidy(source plugincontract.SourceStatus) plugincontract.SourceStatus {
	source.Reason = clean(source.Reason, plugincontract.MaxTextBytes)
	return source
}

// within runs f, which talks to the plugin, and returns when f does, or
// about a second after ctx ended when f is stuck: the library returns at
// once then, unless it is blocked writing to a command that reads nothing,
// and such a command is killed, which frees it.
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
	if s.proc != nil {
		s.proc.kill()
	}
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
	return &Error{Code: CodeInvalidArgument, Err: err, Message: "Knowledge base plugin " + s.name + " was not asked (" +
		tool + "): " + err.Error()}
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
	answered := protocolError(err)
	switch {
	case parent.Err() != nil:
		// The request was stopped. The plugin was told, and the Connection
		// stays as it is: whoever holds it closes it.
		return s.canceled(parent)
	case limited.Err() != nil:
		if s.proc != nil {
			wrote := s.wrote()
			s.drop("it did not answer in time, and study stopped it")
			return errorf(CodeFailedPrecondition, "Knowledge base plugin %s did not %s within %s, so study stopped it.%s Run "+
				"its command in a terminal to see what it waits for: study knowledge-base list shows the command", s.name,
				doing, limit, wrote)
		}
		s.drop("it did not answer in time")
		return errorf(CodeFailedPrecondition, "Knowledge base plugin %s did not %s within %s. Check that the service at its "+
			"address is running and is not busy: study knowledge-base list shows the address", s.name, doing, limit)
	case answered != nil:
		// The plugin answered, with an error of the protocol's own.
		switch {
		case tool == "":
			return s.notTheContract("it would not " + doing + " (" + clean(answered.Message, plugincontract.MaxTextBytes) + ")")
		case answered.Code == jsonrpc.CodeInvalidParams:
			return toolError(s.name, tool, plugincontract.ErrorBody{Code: plugincontract.CodeInvalidArgument, Message: answered.Message})
		default:
			return toolError(s.name, tool, plugincontract.ErrorBody{Code: plugincontract.CodeInternal, Message: answered.Message})
		}
	}
	// The connection broke: nothing more can be asked on it. When it broke
	// because the Connection was closed, or given up, under this call, that is
	// what happened to this call too.
	if over := s.over(); over != nil {
		return over
	}
	if s.proc == nil {
		s.drop("its connection broke")
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s does not answer as an "+
			"MCP server at its address (%s). Check that the service is running and that the address is the one of its MCP "+
			"endpoint: study knowledge-base list shows it, and study knowledge-base add %s --replace --url <URL> changes it",
			s.name, reason(err, s.address), s.name)}
	}
	// A command that exited closes its output, and a moment may pass before
	// it is collected.
	exited := s.proc.hasExited(collectWait)
	how, wrote := s.proc.how(), s.wrote()
	s.drop("its connection broke")
	const look = " Run its command in a terminal to see why: study knowledge-base list shows the command"
	switch {
	case !hungUp(err):
		// Something was read, and it was no MCP message. That the program
		// exited since says nothing: its pipes were closed on it.
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s does not speak MCP "+
			"on its standard output: study read something there that is no MCP message (%s), and stopped it.%s A plugin "+
			"writes only MCP messages on its standard output, each of %d MiB at most, and anything else on its standard error",
			s.name, reason(rootCause(err), ""), wrote, plugincontract.MaxMessageBytes>>20)}
	case exited:
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s stopped before it "+
			"could %s (%s).%s%s", s.name, doing, how, wrote, look)}
	}
	return &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s closed its standard "+
		"output before it could %s, so study stopped it.%s%s", s.name, doing, wrote, look)}
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
// error, cleaned, or nothing when it wrote nothing.
func (s *Connection) wrote() string {
	if s.proc == nil {
		return ""
	}
	said := clean(s.proc.wrote(), 600)
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
