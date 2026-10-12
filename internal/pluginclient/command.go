package pluginclient

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// Command is a Knowledge base plugin started by its registered command, as
// study starts one: see StartCommand. Start uses it, and so does the
// conformance suite, so that a plugin is held to the contract in the
// process study would give it.
//
// The MCP SDK has a command transport, and it starts any exec.Cmd. It is not
// used, for how it stops one: it signals the program alone, so what the
// program started lives on, and its Close runs only once the connection is
// idle, which a plugin that stopped reading its input never lets it be.
// study owns the pipes and the program here, and hands the SDK the two
// streams (Transport).
type Command struct {
	cmd *exec.Cmd
	// stdin, stdout and stderr are study's ends of the program's three
	// pipes. The program never has study's own standard input: a plugin
	// started by the MCP server would otherwise read the agent's messages.
	stdin, stdout, stderr *os.File
	// said is the end of what the program wrote on its standard error, kept
	// for the message when it fails. saidAll is closed once everything it
	// wrote was read: when it, and what it started, have exited.
	said    *tail
	saidAll chan struct{}
	// exited is closed once the program has exited and been collected.
	exited chan struct{}
	grace  time.Duration

	dataFolder, workingFolder string

	// mu guards the rest. Once the program has been collected, its process
	// id is the system's to give to another program, so no signal is sent
	// to it or to its group after that.
	mu        sync.Mutex
	collected bool
	// signalled says that study signalled the program, to stop it or to
	// kill it: it did not exit on its own.
	signalled bool
	onExit    func()

	stopOnce sync.Once
}

// beforeStart, when a test sets it, is called after the program and the
// folder were opened and checked and before the program is started: where
// another program could make their names lead elsewhere.
var beforeStart atomic.Pointer[func()]

// StartCommand starts a plugin registered by a command, and returns once the
// program is running. The caller stops it with Stop.
//
//   - The program is the one pluginregistry.OpenCommand opened and checked,
//     and a script's interpreter is checked as the program is. Nothing on
//     the way to it is looked up again (see execNames).
//   - It is started without a shell, with the arguments it is registered
//     with, in the registry's folder and never in the Topic or the Study
//     home study was started in.
//   - Its three standard streams are pipes of its own, and it inherits no
//     other file study has open.
//   - It runs in a session of its own, without a terminal.
//   - Its environment is a small one study makes (see childEnviron), not
//     the one study was started with.
//   - It is given a folder of its own for its index, outside the Study home
//     (see DataFolder), which LAMPLIGHT_PLUGIN_DATA names.
func StartCommand(env pluginregistry.Env, plugin pluginregistry.Plugin, opts Options) (*Command, error) {
	opts = opts.withDefaults()
	name := plugin.Name
	guard := pluginregistry.NewGuard(env)
	defer guard.Close()

	command, err := pluginregistry.OpenCommand(env, plugin)
	if err != nil {
		return nil, notStarted(name, err)
	}
	defer command.Close()
	dir, dirPath, err := pluginregistry.OpenFolder(env)
	if err != nil {
		return nil, notStarted(name, err)
	}
	defer dir.Close()
	// The environment is made before the last look at the names, so that
	// the walks it takes do not come between that look and the start. And
	// before the interpreter is checked: a script started through env has
	// its interpreter found in the PATH the plugin is given.
	environ := inheritedEnviron(opts.Environ, guard.Outside)
	if err := checkInterpreter(guard, command.Program, searched(environ)); err != nil {
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + name + " cannot be started: " +
			"its program, " + clean(plugin.Command[0], 400) + ", is a script, and the interpreter its first line names is not " +
			"one study runs. " + err.Error()}
	}
	// Its folder is made once the program is one study starts: a plugin
	// that is refused leaves nothing behind.
	dataFolder, err := makeDataFolder(guard, env, name)
	if err != nil {
		return nil, err
	}
	environ = append(environ, plugincontract.DataFolderEnv+"="+dataFolder)
	sealInherited()

	var pipes [3][2]*os.File // stdin, stdout, stderr: each the read end, then the write end
	closeAll := func() {
		for _, pipe := range pipes {
			for _, end := range pipe {
				if end != nil {
					_ = end.Close()
				}
			}
		}
	}
	for i := range pipes {
		if pipes[i][0], pipes[i][1], err = os.Pipe(); err != nil {
			closeAll()
			return nil, &Error{Code: CodeInternal, Err: err, Message: "making a pipe for Knowledge base plugin " + name + ": " + err.Error()}
		}
	}
	if hook := beforeStart.Load(); hook != nil {
		(*hook)()
	}
	path, cwd, inherited, err := execNames(command, plugin.Command[0], dir, dirPath)
	if err != nil {
		closeAll()
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + name +
			" was not started: " + err.Error()}
	}
	cmd := &exec.Cmd{
		Path: path,
		// The program is told the name it is registered by, whatever name it
		// is started through.
		Args:        slices.Clone(plugin.Command),
		Dir:         cwd,
		Env:         environ,
		Stdin:       pipes[0][0],
		Stdout:      pipes[1][1],
		Stderr:      pipes[2][1],
		ExtraFiles:  inherited,
		SysProcAttr: ownSession(),
	}
	err = cmd.Start()
	// The names given to the child are descriptors of this process: they
	// must stay open until it has started.
	runtime.KeepAlive(command)
	runtime.KeepAlive(dir)
	if err != nil {
		closeAll()
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: fmt.Sprintf("Knowledge base plugin %s did not "+
			"start: its program, %s, could not be run (%s). Check that it is a program for this computer, and, if it is a "+
			"script, that the interpreter its first line names is installed; study knowledge-base list shows its command",
			name, clean(plugin.Command[0], 400), pathReason(err))}
	}
	// The program's ends are the program's now.
	_ = pipes[0][0].Close()
	_ = pipes[1][1].Close()
	_ = pipes[2][1].Close()

	c := &Command{cmd: cmd, stdin: pipes[0][1], stdout: pipes[1][0], stderr: pipes[2][0],
		said: &tail{max: StderrKept}, saidAll: make(chan struct{}), exited: make(chan struct{}), grace: opts.StopGrace,
		dataFolder: dataFolder, workingFolder: dirPath}
	go func() {
		_ = cmd.Wait()
		// The plugin is the program study started. When it exits, whatever
		// it started and left behind goes with it: at once, while the
		// process group can only be its own.
		c.mu.Lock()
		signalGroup(cmd.Process, syscall.SIGKILL)
		c.collected = true
		onExit := c.onExit
		c.mu.Unlock()
		close(c.exited)
		if onExit != nil {
			onExit()
		}
	}()
	// What the program writes on its standard error is read, so that it
	// never blocks on a full pipe, and only its end is kept.
	go func() {
		_, _ = io.Copy(c.said, c.stderr)
		close(c.saidAll)
	}()
	return c, nil
}

// Transport is the MCP transport over the command's standard input and
// output: one JSON-RPC message on each line, each up to the size a message
// may have. Closing the connection it makes closes the two pipes, which asks
// the program to exit; Stop makes sure it does.
func (c *Command) Transport() mcp.Transport {
	return &mcp.IOTransport{Reader: c.stdout, Writer: c.stdin, MaxLineLength: plugincontract.MaxMessageBytes}
}

// PID is the program's process id.
func (c *Command) PID() int { return c.cmd.Process.Pid }

// DataFolder is the folder the program was given for its index, as
// LAMPLIGHT_PLUGIN_DATA names it.
func (c *Command) DataFolder() string { return c.dataFolder }

// WorkingFolder is the folder the program was started in: the registry's.
func (c *Command) WorkingFolder() string { return c.workingFolder }

// Wrote is the end of what the program wrote on its standard error so far,
// as it wrote it: text of the plugin's, to clean before it is printed.
func (c *Command) Wrote() string { return c.wrote() }

// whenExited has f called once the program has exited: at once when it has
// already.
func (c *Command) whenExited(f func()) {
	c.mu.Lock()
	if !c.collected {
		c.onExit = f
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	f()
}

// notStarted is the error for a plugin the registry does not let study
// start: its program is gone, is no program, or is where the agent writes.
// The registry says which, and what to do. Whatever its own code, to whoever
// asked for the plugin it is a plugin that is not ready: the name they gave
// is a registered one.
func notStarted(name string, err error) *Error {
	said := err.Error()
	if !strings.HasPrefix(said, "Knowledge base plugin "+name+" ") {
		said = "Knowledge base plugin " + name + " cannot be started: " + said
	}
	return &Error{Code: CodeFailedPrecondition, Err: err, Message: said}
}

// byName gives the names to start a program by, for a system that cannot be
// handed open files: the path it is registered by, and the folder's path.
// Each must still name the file that was opened and checked. What is left
// is the moment between this look and the system's own, when it starts the
// program.
func byName(command *pluginregistry.Command, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	for _, f := range []struct {
		file *os.File
		path string
	}{{command.Program, programPath}, {dir, dirPath}} {
		opened, err := f.file.Stat()
		if err != nil {
			return "", "", nil, err
		}
		if named, err := os.Stat(f.path); err != nil || !os.SameFile(opened, named) {
			return "", "", nil, fmt.Errorf("%s came to name something else while study was starting the plugin: try again", f.path)
		}
	}
	return programPath, dirPath, nil, nil
}

// The environment a plugin is started with is one study makes, of these
// variables of the environment study has and of nothing else. A plugin runs
// outside the agent's sandbox, and the environment study has is as likely
// as not the one of an agent started in a Topic, with that Topic's virtual
// environment switched on: PATH, PYTHONPATH, NODE_OPTIONS, PERL5OPT,
// BASH_ENV, LD_LIBRARY_PATH and many more can each have a program load what
// the agent wrote. A list of what to take out cannot be finished, so the
// list is of what goes in.
var (
	// A list of folders: each folder that is an absolute path outside the
	// Study home is kept.
	folderLists = []string{"PATH", "XDG_DATA_DIRS", "XDG_CONFIG_DIRS"}
	// One folder: kept when it is an absolute path outside the Study home.
	folders = []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"}
	// A name, of the user or of a locale: kept when it is no path.
	names = []string{"USER", "LOGNAME", "LANG"}
	// The time zone: a name such as Europe/Rome, or a file's path.
	zones = []string{"TZ"}
)

// EnvironmentVariables names the variables of study's environment that a
// plugin may be given, for the documents that list them. LC_* stands for
// every variable whose name begins so.
func EnvironmentVariables() []string {
	return slices.Concat(folderLists, folders, names, []string{"LC_*"}, zones)
}

// childEnviron is the environment a plugin is started with: PATH, HOME,
// USER, LOGNAME, LANG, the LC_* variables, TZ, TMPDIR and the XDG base
// folders, each as environ has it and only when it leads nowhere inside the
// Study home, and LAMPLIGHT_PLUGIN_DATA, which names the plugin's folder.
//
// outside says whether an absolute path is known to stay outside the Study
// home (pluginregistry.Guard.Outside): what it cannot decide is not
// outside, and is left out. A plugin that needs anything else, a key or a
// proxy or a library's folder, is given it in its arguments, or is
// registered through a wrapper that sets it.
func childEnviron(environ []string, outside func(path string) bool, dataFolder string) []string {
	return append(inheritedEnviron(environ, outside), plugincontract.DataFolderEnv+"="+dataFolder)
}

// inheritedEnviron is what a plugin is given of the environment study has:
// childEnviron without the plugin's folder.
func inheritedEnviron(environ []string, outside func(path string) bool) []string {
	// The last value given for a name is the one a program started with
	// environ would see.
	values := map[string]string{}
	var order []string
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = value
	}
	// One look at each path is enough: the same folders are named again
	// and again.
	looked := map[string]bool{}
	isOutside := func(path string) bool {
		is, ok := looked[path]
		if !ok {
			is = filepath.IsAbs(path) && outside(path)
			looked[path] = is
		}
		return is
	}
	// noPath reports whether a value names nothing by a path that could
	// lead into the Study home: no absolute path that is not outside it,
	// and no .. to climb there from wherever the value is taken from.
	noPath := func(value string) bool {
		for _, part := range strings.Split(value, ":") {
			if filepath.IsAbs(part) && !isOutside(part) {
				return false
			}
			if slices.Contains(strings.Split(part, "/"), "..") {
				return false
			}
		}
		return true
	}
	var out []string
	for _, name := range order {
		value := values[name]
		switch {
		case name == plugincontract.DataFolderEnv:
			// Given anew below, so that an inherited value never stands
			// for it.
		case slices.Contains(folderLists, name):
			var kept []string
			for _, folder := range filepath.SplitList(value) {
				if isOutside(folder) {
					kept = append(kept, folder)
				}
			}
			if len(kept) > 0 {
				out = append(out, name+"="+strings.Join(kept, string(os.PathListSeparator)))
			}
		case slices.Contains(folders, name):
			if isOutside(value) {
				out = append(out, name+"="+value)
			}
		case slices.Contains(names, name) || strings.HasPrefix(name, "LC_"):
			if value != "" && !strings.Contains(value, "/") {
				out = append(out, name+"="+value)
			}
		case slices.Contains(zones, name):
			if value != "" && noPath(value) {
				out = append(out, name+"="+value)
			}
		}
	}
	return out
}

// searched is the folders of the PATH in an environment, in its order: where
// a program started with it finds another by its name.
func searched(environ []string) []string {
	for _, kv := range environ {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			return filepath.SplitList(value)
		}
	}
	return nil
}

// checkInterpreter checks the interpreter of a program that is a script.
//
// The system does not run a script: it runs the program the script's first
// line names, by that program's path, with the script as its argument. So
// that program is started as surely as the plugin's own, and must be one
// the agent cannot change either. An interpreter that is itself a script is
// followed, as the system follows it, a few times over.
//
// It is checked by its path, since the system finds it by its path. No hop
// of that path is in the Study home, or the check fails: so the agent,
// which writes there, cannot make the path lead elsewhere afterwards. What
// is then read, to see whether the interpreter is a script too, is the file
// that was checked, as the walk opened it. A path that is not absolute is
// refused: the system would find it from the folder the plugin is started
// in, and nothing was checked there.
//
// A first line such as #!/usr/bin/env python3 names env, which is no
// interpreter: it starts the program its argument names, found in the
// folders of the PATH it has, and that is the PATH study gives the plugin
// (path). So what env would start is found as env finds it, and checked in
// env's place (startedByEnv).
func checkInterpreter(guard *pluginregistry.Guard, program *os.File, path []string) error {
	script := program
	defer func() {
		if script != program {
			_ = script.Close()
		}
	}()
	for range maxInterpreters {
		interpreter, argument, err := firstLine(script)
		if err != nil || interpreter == "" {
			return err
		}
		if !filepath.IsAbs(interpreter) {
			return notAbsolute(interpreter)
		}
		next, err := openInterpreter(guard, interpreter)
		if err != nil {
			return err
		}
		// A file called env that is a script is a script, and is followed
		// as one: the system runs what its own first line names. Only env
		// the program starts what it is given.
		if filepath.Base(interpreter) == "env" && !isScript(next) {
			_ = next.Close()
			if next, err = startedByEnv(guard, argument, path); err != nil || next == nil {
				return err
			}
		}
		if script != program {
			_ = script.Close()
		}
		script = next
	}
	// The system follows a few more than anyone needs. What is left
	// unchecked after this many is refused, not passed.
	if isScript(script) {
		return fmt.Errorf("it is a script whose interpreter is a script, %d times over, and study follows no more: name "+
			"an interpreter that is a program", maxInterpreters)
	}
	return nil
}

// maxInterpreters is how many interpreters in a row study checks for a
// script: the script's, that one's own when it is a script too, and so on.
const maxInterpreters = 4

// openInterpreter checks a program a script would be run by and returns it
// open, to see whether it is a script itself. The system reads a program's
// first line whether or not this user may read the file, and follows it:
// one that study cannot read is one whose interpreter nobody checked, and
// is refused.
func openInterpreter(guard *pluginregistry.Guard, path string) (*os.File, error) {
	// Checked first, so that what is wrong with the path or the program is
	// said as it is for any program; what is left to go wrong when it is
	// then opened is that it may not be read.
	if err := guard.CheckProgram(path); err != nil {
		return nil, err
	}
	file, err := guard.OpenProgram(path)
	if errors.Is(err, fs.ErrPermission) {
		return nil, fmt.Errorf("study cannot read %s to see whether it is a script too (permission denied): a program you "+
			"may run and not read can name an interpreter that nobody checked. Make it readable for you, or name another "+
			"interpreter", clean(path, 200))
	}
	return file, err
}

func notAbsolute(interpreter string) error {
	return fmt.Errorf("%s is no absolute path: the system would look for it from the folder the plugin is started "+
		"in. Name the interpreter by its full path, or register the interpreter itself with the script as an "+
		"argument", clean(interpreter, 200))
}

// envName is a name, or a path, that env starts and nothing else: no
// option, no NAME=value setting, nothing quoted or escaped and nothing for
// env to fill in.
var envName = regexp.MustCompile(`^[A-Za-z0-9._+/][A-Za-z0-9._+/-]*$`)

// startedByEnv finds the program env starts when a script's first line
// gives it argument, checks it as an interpreter is checked, and returns it
// open: nil when env starts none.
//
// Two forms are read: env NAME, and env -S NAME ARGUMENTS, which is how a
// first line gives an interpreter arguments. Without -S the whole of what
// follows env is one name to it, spaces and all, so anything more than a
// name there is refused: study would check one program, and env would look
// for another.
//
// NAME is looked up as env looks it up: in each folder of path, in order,
// the first that has a program of that name which this user may run. A
// folder of path is outside the Study home already (childEnviron), but what
// a name in it leads to need not be: a link there can point into a Topic's
// virtual environment. The first match is the one env starts, so when it is
// inside the Study home nothing later is looked at.
//
// Whatever else the line gives env, an option or a setting before the name,
// decides what is started in ways study does not follow, and is refused
// rather than guessed at.
func startedByEnv(guard *pluginregistry.Guard, argument string, path []string) (*os.File, error) {
	name := argument
	split := false
	if rest, ok := strings.CutPrefix(argument, "-S"); ok {
		// env splits what follows -S itself, at spaces and tabs.
		split = true
		name, _, _ = strings.Cut(strings.ReplaceAll(strings.TrimLeft(rest, " \t"), "\t", " "), " ")
	}
	switch {
	case name == "":
		return nil, nil // env alone prints its environment and starts nothing
	case !split && strings.ContainsAny(name, " \t"):
		return nil, fmt.Errorf("it gives env %s, and env takes all of that for the name of one program: a first line "+
			"hands over what follows the interpreter as one word. Have env split it, as in #!/usr/bin/env -S python3 "+
			"-u, or name the interpreter by its full path", clean(name, 200))
	case !envName.MatchString(name):
		return nil, fmt.Errorf("it has env start %s, and study cannot tell which program that is. Have env start the "+
			"interpreter by its name alone, as in #!/usr/bin/env python3, or name the interpreter by its full path",
			clean(name, 200))
	case filepath.IsAbs(name):
		return openInterpreter(guard, name)
	case strings.ContainsRune(name, '/'):
		return nil, notAbsolute(name)
	case len(path) == 0:
		return nil, fmt.Errorf("it has env look for %s in the folders of PATH, and study gives this plugin no PATH: no "+
			"folder of yours is known to be outside the Study home. Name the interpreter by its full path", name)
	}
	for _, folder := range path {
		candidate := strings.TrimRight(folder, "/") + "/" + name
		file, err := openInterpreter(guard, candidate)
		switch {
		case err == nil:
			return file, nil
		case pluginregistry.IsInside(err):
			return nil, fmt.Errorf("it has env look for %s in the folders of PATH, and the first one found is one your "+
				"agent can change: %w", name, err)
		case pluginregistry.CodeOf(err) == CodeNotFound || pluginregistry.CodeOf(err) == CodeInvalidArgument:
			// Not there, or nothing this user may run: env goes on to the
			// next folder too.
		default:
			// It cannot be followed, or cannot be read, so it cannot be
			// said to be outside, or to be no script.
			return nil, err
		}
	}
	return nil, fmt.Errorf("it has env look for %s in the folders of the PATH study gives a plugin (%s), and none of "+
		"them has a program of that name. Install it in one of them, or name the interpreter by its full path",
		name, clean(strings.Join(path, string(os.PathListSeparator)), 400))
}

// firstLineBytes is how much of a file's start the system reads to find a
// script's first line. Linux reads 256 bytes and macOS 512, and each does
// its own thing with a line that does not fit. A line that ends within 255
// bytes is read whole by both.
const firstLineBytes = 256

// firstLine reads a script's first line as the system does: the interpreter
// it names after #!, and the one argument that the rest of the line is,
// spaces within it kept. Both are empty for a file that is no script.
//
// It fails for a first line the systems study runs on would not all read
// alike: one that does not end within what the shortest of them reads, or
// one with a character in it that ends the line for one of them. What
// study checked must be what is started.
func firstLine(file *os.File) (interpreter, argument string, err error) {
	head := make([]byte, firstLineBytes)
	n, _ := file.ReadAt(head, 0)
	rest, isScript := strings.CutPrefix(string(head[:n]), "#!")
	if !isScript {
		return "", "", nil
	}
	line, _, ended := strings.Cut(rest, "\n")
	if !ended && n == firstLineBytes {
		return "", "", fmt.Errorf("its first line is longer than %d bytes: systems differ in how much of such a line "+
			"they read, so study cannot tell what they would start. Shorten it", firstLineBytes-1)
	}
	for _, b := range []byte(line) {
		if (b < 0x20 && b != '\t') || b == 0x7f {
			return "", "", errors.New("its first line holds a character that is no text, such as the carriage return of " +
				"a file saved with Windows line endings: systems differ in what they make of it. Save the script with " +
				"plain line endings")
		}
	}
	interpreter, argument, _ = strings.Cut(strings.ReplaceAll(strings.Trim(line, " \t"), "\t", " "), " ")
	return interpreter, strings.Trim(argument, " "), nil
}

// isScript reports whether a file begins as a script does.
func isScript(file *os.File) bool {
	head := make([]byte, 2)
	n, _ := file.ReadAt(head, 0)
	return string(head[:n]) == "#!"
}

// hasExited reports whether the program exited, waiting up to d for it.
func (c *Command) hasExited(d time.Duration) bool {
	select {
	case <-c.exited:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.exited:
		return true
	case <-timer.C:
		return false
	}
}

// signal sends sig to the program and to everything it started that is
// still in its process group, unless the program was collected already.
func (c *Command) signal(sig syscall.Signal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.collected {
		c.signalled = true
		signalGroup(c.cmd.Process, sig)
	}
}

// Kill ends the program at once, and everything it started that is still
// in its process group: for a plugin that is stuck, or was given its time
// already. The pipes are closed first, which frees whatever was reading or
// writing them. Stop still has to follow.
func (c *Command) Kill() {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	c.signal(syscall.SIGKILL)
}

// Stop ends the program and everything it started that is still in its
// process group, and waits for it: it closes the program's input, which
// asks an MCP server to exit, and its output; after the grace period it
// sends SIGTERM to the program's process group, and after another it kills
// the group. It takes three grace periods at most.
//
// It reports whether the program exited on its own, once its pipes were
// closed or before, as the contract asks of a plugin: false means study had
// to signal it.
func (c *Command) Stop() (onItsOwn bool) {
	c.stopOnce.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if !c.hasExited(c.grace) {
			c.signal(syscall.SIGTERM)
			if !c.hasExited(c.grace) {
				c.signal(syscall.SIGKILL)
				c.hasExited(c.grace)
			}
		}
		_ = c.stderr.Close()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.collected && !c.signalled
}

// how says how the program ended, or that it has not.
func (c *Command) how() string {
	if !c.hasExited(0) {
		return "it is still running"
	}
	if state := c.cmd.ProcessState; state != nil {
		return state.String()
	}
	return "it exited"
}

// wrote is the end of what the program wrote on its standard error. For a
// program that exited it waits, briefly, for the last of it to be read.
func (c *Command) wrote() string {
	if c.hasExited(0) {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-c.saidAll:
		case <-timer.C:
		}
	}
	return c.said.String()
}

// tail keeps the end of what is written to it.
type tail struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if over := len(t.data) - t.max; over > 0 {
		t.data = append(t.data[:0], t.data[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}
