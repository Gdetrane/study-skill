package plugintest

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// Arg is the first argument of a test binary that is run as the test
// plugin.
const Arg = "lamplight-test-plugin"

// Main runs the test plugin over the standard streams, and never returns,
// when the program was started as one: with Arg as its first argument.
// Otherwise it does nothing. A package whose tests start the test plugin
// calls it first in TestMain, and registers Command: the test binary is then
// the one program its tests start, and nothing has to be built.
//
// The arguments after Arg say how the plugin behaves, and how it misbehaves:
//
//	--record FILE       write how it was started to FILE (see Record), and
//	                    add each tool it is asked, with what it is asked,
//	                    to FILE.calls (see ReadCalls)
//	--delay TOOL=D      TOOL takes D to answer
//	--sources KINDS     the kinds of Source it indexes: file,url
//	--page-delay D      take D over each page it indexes
//	--name N, --version V   what it calls itself
//	--tools TOOLS       the tools it has, when not the contract's seven
//	--contract JSON     what its contract tool gives back
//	--finding NAME=ok:SENTENCE, --finding NAME=wrong:SENTENCE
//	                    a finding of its diagnose tool, which gives them in
//	                    the order of the flags, and none without one. What
//	                    the contract does not allow of them, too many or
//	                    none of its shape, is given with --result, and a
//	                    plugin that says nothing in time with --delay,
//	                    --wait or --hang
//	--result TOOL=JSON  what TOOL gives back, whatever it is asked
//	--refuse TOOL=CODE:MESSAGE   TOOL refuses every call
//	--plain-error TOOL=TEXT      TOOL fails with text that is no JSON
//	--text-only         results as text, without structured content
//	--break RULES       the rules of the contract it breaks (the Break
//	                    constants), divided by commas
//	--linger            stay when its input closes, until it is signalled
//	--litter            write a file in the folder it was started in
//	--flood TOOL        TOOL gives back more than a message may hold
//	--ask WHERE         send study requests, which no plugin does: "start",
//	                    before it reads anything; "idle", once contract was
//	                    answered; or a tool, when it is called, which then
//	                    never answers
//	--ask-method M      the method of those requests; "ping" otherwise
//	--ask-times N       how many of them; without end otherwise
//	--notify WHERE      send study notifications without end, in the same
//	                    places
//	--stderr TEXT       write TEXT on its standard error first
//	--escaped           read \x1b, \a, \n and the like in the texts of
//	                    --name, --version, --stderr, --refuse, --finding and
//	                    --plain-error as the characters they stand for: the
//	                    registry stores no argument with such a character
//	                    in it, and a plugin can write one all the same
//	--exit N            exit at once with status N
//	--garbage           write a line that is no MCP message, and wait
//	--babble            write such a line once contract was answered, when
//	                    study asks nothing
//	--hush              close its standard output then, and stay
//	--close-output      close its standard output, and stay
//	--hang WHERE        "start": answer nothing; a tool: it never answers
//	--wait TOOL         TOOL answers only once its call is cancelled
//	--freeze TOOL       stop dead, reading and writing nothing, at TOOL,
//	                    having made the file FILE.frozen of --record FILE
//	--stubborn          ignore SIGTERM, and stay when its input closes
//	--child FILE        start a program of its own that is as stubborn
//	--stay              do nothing but stay (what --child starts)
func Main() {
	if len(os.Args) < 2 || os.Args[1] != Arg {
		return
	}
	// It leaves by the system's own exit, past what the Go runtime does on
	// the way out: a test binary built to measure coverage would write a
	// warning on its standard error there, into what a test reads of it.
	syscall.Exit(run(os.Args[2:]))
}

// Command is the command that starts this test binary as the test plugin
// with the given arguments: what follows -- in study knowledge-base add.
func Command(args ...string) []string {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return append([]string{exe, Arg}, args...)
}

// Record is how a test plugin was started, as it saw it itself.
type Record struct {
	PID  int      `json:"pid"`
	Args []string `json:"args"`
	// Script is the name it was handed for the script it is the interpreter
	// of, when it was started as one. ScriptPath is where that name really
	// leads, and Beside the names of the files in the script's folder, as
	// the script finds them through its own name.
	Script     string   `json:"script,omitempty"`
	ScriptPath string   `json:"script_path,omitempty"`
	Beside     []string `json:"beside,omitempty"`
	// Name is what the system calls the process, where it can be asked, and
	// Exe the path of the program it runs, as the system tells it.
	Name string `json:"name,omitempty"`
	Exe  string `json:"exe,omitempty"`
	// Dir is the folder it was started in, and Env its whole environment.
	Dir string            `json:"dir"`
	Env map[string]string `json:"env"`
	// Stdin says what its standard input is.
	Stdin Stream `json:"stdin"`
	// Leader says that it leads a session of its own, and Terminal that it
	// could open the terminal that controls it.
	Leader   bool `json:"leader"`
	Terminal bool `json:"terminal"`
	// Files are the files it was started with besides its three standard
	// streams, by descriptor: where each leads, on a system that can tell.
	Files map[int]string `json:"files,omitempty"`
}

// Stream identifies an open file.
type Stream struct {
	Pipe   bool   `json:"pipe"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// ReadRecord reads what a test plugin started with --record wrote, waiting
// for it to have been written.
func ReadRecord(t testing.TB, path string) Record {
	t.Helper()
	var record Record
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &record) == nil {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test plugin wrote no record to %s: %v", path, err)
		}
	}
}

// Call is one call of a tool, as a test plugin received it.
type Call struct {
	// Tool is the tool's name, and Args what it was asked: a JSON object.
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Collection is the collection the call names.
func (c Call) Collection() string {
	var args struct {
		Collection string `json:"collection"`
	}
	_ = json.Unmarshal(c.Args, &args)
	return args.Collection
}

// Source is the id of the Source the call is about: the one an index call
// hands over, or the one a cancel call names.
func (c Call) Source() string {
	var args struct {
		Source json.RawMessage `json:"source"`
	}
	_ = json.Unmarshal(c.Args, &args)
	var id string
	if json.Unmarshal(args.Source, &id) == nil {
		return id
	}
	var source plugincontract.Source
	_ = json.Unmarshal(args.Source, &source)
	return source.ID
}

// Indexed is the Source an index call hands over.
func (c Call) Indexed() plugincontract.Source {
	var in plugincontract.IndexInput
	_ = json.Unmarshal(c.Args, &in)
	return in.Source
}

// ReadCalls reads the calls a test plugin started with --record path
// received so far, in the order it received them, by every copy of it that
// was started so: a plugin is started anew by each command and each tool
// that needs it. A plugin that was asked nothing, and one that was never
// started, give none.
func ReadCalls(t testing.TB, path string) []Call {
	t.Helper()
	data, err := os.ReadFile(path + ".calls")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []Call
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call Call
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatalf("a line of %s.calls is no call: %v: %q", path, err, line)
		}
		calls = append(calls, call)
	}
	return calls
}

// Tools are the names of the tools some calls called, in order.
func Tools(calls []Call) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Tool)
	}
	return names
}

// recordCall adds a call the plugin received to the calls of its record:
// one line, written at once, so that the copies of a plugin and the calls of
// one copy never write into each other's.
func recordCall(record, tool string, args json.RawMessage) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	line, err := json.Marshal(Call{Tool: tool, Args: args})
	if err == nil {
		var f *os.File
		if f, err = os.OpenFile(record+".calls", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			_, err = f.Write(append(line, '\n'))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "the test plugin could not record a call:", err)
	}
}

// pairs collects flags given as TOOL=VALUE.
type pairs map[string]string

func (p pairs) String() string { return fmt.Sprint(map[string]string(p)) }

func (p pairs) Set(value string) error {
	tool, rest, ok := strings.Cut(value, "=")
	if !ok {
		return fmt.Errorf("%q is not TOOL=VALUE", value)
	}
	p[tool] = rest
	return nil
}

// findings collects flags given as NAME=ok:SENTENCE or NAME=wrong:SENTENCE,
// in the order they are given.
type findings []plugincontract.Finding

func (f *findings) String() string { return fmt.Sprint([]plugincontract.Finding(*f)) }

func (f *findings) Set(value string) error {
	name, rest, named := strings.Cut(value, "=")
	verdict, sentence, judged := strings.Cut(rest, ":")
	if !named || !judged || (verdict != "ok" && verdict != "wrong") {
		return fmt.Errorf("%q is not NAME=ok:SENTENCE or NAME=wrong:SENTENCE", value)
	}
	*f = append(*f, plugincontract.Finding{Name: name, OK: verdict == "ok", Message: sentence})
	return nil
}

func run(args []string) int {
	// Started as the interpreter of a script, by a first line of
	// "#!<this binary> lamplight-test-plugin", the plugin is handed the
	// script's name before the arguments.
	script := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		script, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(Arg, flag.ContinueOnError)
	cfg := Config{Results: map[string]string{}, Refusals: map[string]string{}, PlainErrors: map[string]string{}}
	var (
		record      = flags.String("record", "", "")
		sources     = flags.String("sources", "", "")
		tools       = flags.String("tools", "", "")
		contract    = flags.String("contract", "", "")
		breaks      = flags.String("break", "", "")
		linger      = flags.Bool("linger", false, "")
		litter      = flags.Bool("litter", false, "")
		ask         = flags.String("ask", "", "")
		askMethod   = flags.String("ask-method", "ping", "")
		askTimes    = flags.Int("ask-times", 0, "")
		notify      = flags.String("notify", "", "")
		flood       = flags.String("flood", "", "")
		stderr      = flags.String("stderr", "", "")
		escaped     = flags.Bool("escaped", false, "")
		exit        = flags.Int("exit", -1, "")
		garbage     = flags.Bool("garbage", false, "")
		babble      = flags.Bool("babble", false, "")
		hush        = flags.Bool("hush", false, "")
		closeOutput = flags.Bool("close-output", false, "")
		hang        = flags.String("hang", "", "")
		wait        = flags.String("wait", "", "")
		freeze      = flags.String("freeze", "", "")
		stubborn    = flags.Bool("stubborn", false, "")
		child       = flags.String("child", "", "")
		stay        = flags.Bool("stay", false, "")
	)
	flags.DurationVar(&cfg.PageDelay, "page-delay", 0, "")
	flags.StringVar(&cfg.Name, "name", "", "")
	flags.StringVar(&cfg.Version, "version", "", "")
	flags.BoolVar(&cfg.TextOnly, "text-only", false, "")
	delays := pairs{}
	flags.Var((*findings)(&cfg.Findings), "finding", "")
	flags.Var(pairs(cfg.Results), "result", "")
	flags.Var(pairs(cfg.Refusals), "refuse", "")
	flags.Var(pairs(cfg.PlainErrors), "plain-error", "")
	flags.Var(delays, "delay", "")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *escaped {
		cfg.Name, cfg.Version, *stderr = unescaped(cfg.Name), unescaped(cfg.Version), unescaped(*stderr)
		for _, texts := range []map[string]string{cfg.Refusals, cfg.PlainErrors} {
			for tool, text := range texts {
				texts[tool] = unescaped(text)
			}
		}
		for i := range cfg.Findings {
			cfg.Findings[i].Message = unescaped(cfg.Findings[i].Message)
		}
	}

	if *stubborn {
		ignoreStops()
	}
	if *record != "" {
		if err := writeRecord(*record, script); err != nil {
			fmt.Fprintln(os.Stderr, "the test plugin could not write its record:", err)
			return 1
		}
	}
	if *stderr != "" {
		fmt.Fprint(os.Stderr, *stderr)
	}
	if *litter {
		_ = os.WriteFile("test-plugin.log", []byte("the test plugin was started\n"), 0o600)
	}
	// What a plugin never does: it sends study messages of its own, as
	// fast as it can, and reads none of the answers.
	pour := func(request bool) {
		for n := 0; ; n++ {
			line := `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"the plugin is busy"}}`
			if request {
				if *askTimes > 0 && n >= *askTimes {
					return
				}
				line = fmt.Sprintf(`{"jsonrpc":"2.0","id":"plugin-%d","method":%q}`, n, *askMethod)
			}
			if _, err := os.Stdout.WriteString(line + "\n"); err != nil {
				return
			}
		}
	}
	if *ask == "start" || *notify == "start" {
		pour(*ask == "start")
		forever()
	}
	if *child != "" {
		// Its pipes are the plugin's own, so it holds them open as long as
		// it lives.
		words := Command("--stay", "--stubborn", "--record", *child)
		cmd := exec.Command(words[0], words[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "the test plugin could not start its child:", err)
			return 1
		}
		// Go on once the child is up, so that a test finds it whatever the
		// plugin does next.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if _, err := os.Stat(*child); err == nil {
				break
			}
		}
	}
	switch {
	case *exit >= 0:
		return *exit
	case *stay:
		forever()
	case *garbage:
		fmt.Println("Loading the index... this is no MCP message")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	case *closeOutput:
		_ = os.Stdout.Close()
		forever()
	case *hang == "start":
		_, _ = io.Copy(io.Discard, os.Stdin)
		if *stubborn {
			forever()
		}
		return 0
	}

	cfg.Folder = os.Getenv(plugincontract.DataFolderEnv)
	if cfg.Folder == "" {
		fmt.Fprintln(os.Stderr, "the test plugin was not told its folder: "+plugincontract.DataFolderEnv+" is not set")
		return 1
	}
	if *sources != "" {
		cfg.Sources = strings.Split(*sources, ",")
	}
	if *tools != "" {
		cfg.Tools = strings.Split(*tools, ",")
	}
	if *breaks != "" {
		cfg.Breaks = strings.Split(*breaks, ",")
	}
	if *contract != "" {
		cfg.Results[plugincontract.ToolContract] = *contract
	}
	if *flood != "" {
		// One answer, larger than a message may be.
		cfg.Results[*flood] = `{"sources":[{"id":"flood.1","state":"failed","reason":"` +
			strings.Repeat("x", plugincontract.MaxMessageBytes+1024) + `"}]}`
	}
	if *record != "" {
		cfg.Asked = func(tool string, args json.RawMessage) { recordCall(*record, tool, args) }
	}
	cfg.Before = func(ctx context.Context, tool string) {
		if slow, err := time.ParseDuration(delays[tool]); err == nil {
			// A plugin that is slow, and answers in the end: it gives up
			// sooner only when it is told to.
			select {
			case <-time.After(slow):
			case <-ctx.Done():
			}
		}
		for _, messages := range []struct {
			where   string
			request bool
		}{{*ask, true}, {*notify, false}} {
			switch {
			case messages.where == "idle" && tool == plugincontract.ToolContract:
				// Once the answer to this call has gone out, and study
				// asks nothing.
				go func() {
					time.Sleep(150 * time.Millisecond)
					pour(messages.request)
				}()
			case messages.where != "" && messages.where == tool:
				pour(messages.request)
				forever()
			}
		}
		if (*babble || *hush) && tool == plugincontract.ToolContract {
			// Once the answer to this call has gone out.
			go func() {
				time.Sleep(150 * time.Millisecond)
				if *hush {
					_ = os.Stdout.Close()
					return
				}
				_, _ = os.Stdout.WriteString("Indexing finished: this is no MCP message\n")
			}()
		}
		switch tool {
		case *hang:
			// It answers nothing, whether or not the call is cancelled.
			forever()
		case *wait:
			// A plugin that is slow, and well behaved: it gives up when
			// told to, or when its connection closes.
			<-ctx.Done()
		case *freeze:
			// It says that it is about to, for a test to wait for.
			if *record != "" {
				_ = os.WriteFile(*record+".frozen", nil, 0o600)
			}
			stopDead()
		}
	}

	plugin := New(cfg)
	defer plugin.Close()
	ctx, stop := stopsCancel(context.Background(), *stubborn)
	defer stop()
	// Its input closing is how a plugin is asked to exit: Run returns then.
	_ = plugin.Server().Run(ctx, &mcp.StdioTransport{})
	if *stubborn {
		forever()
	}
	if *linger {
		// Not until it is told twice: by a signal.
		<-ctx.Done()
	}
	return 0
}

func forever() {
	for {
		time.Sleep(time.Hour)
	}
}

// writeRecord writes how the plugin was started, whole or not at all.
func writeRecord(path, script string) error {
	dir, _ := os.Getwd()
	record := Record{PID: os.Getpid(), Args: os.Args, Script: script, Dir: dir, Env: map[string]string{}, Stdin: streamOf(os.Stdin),
		Leader: leadsSession(), Terminal: hasTerminal(), Files: inheritedFiles(), Name: processName()}
	record.Exe, _ = os.Executable()
	if script != "" {
		record.ScriptPath, _ = filepath.EvalSymlinks(script)
		if beside, err := os.ReadDir(filepath.Dir(script)); err == nil {
			for _, entry := range beside {
				record.Beside = append(record.Beside, entry.Name())
			}
		}
	}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		record.Env[name] = value
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

// unescaped is text with its backslash escapes read as Go reads them in a
// string, or the text as it is when they are not ones Go knows.
func unescaped(text string) string {
	if read, err := strconv.Unquote(`"` + strings.ReplaceAll(text, `"`, `\"`) + `"`); err == nil {
		return read
	}
	return text
}
