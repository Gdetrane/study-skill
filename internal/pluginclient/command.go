package pluginclient

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// process is a Knowledge base plugin started by its command.
//
// The MCP SDK has a command transport, and it starts any exec.Cmd. It is not
// used, for how it stops one: it signals the program alone, so what the
// program started lives on, and its Close runs only once the connection is
// idle, which a plugin that stopped reading its input never lets it be.
// study owns the pipes and the program here, and hands the SDK the two
// streams (mcp.IOTransport).
type process struct {
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

	// mu guards collected. Once the program has been collected, its process
	// id is the system's to give to another program, so no signal is sent
	// to it or to its group after that.
	mu        sync.Mutex
	collected bool

	stopOnce sync.Once
}

// beforeStart, when a test sets it, is called after the program and the
// folder were opened and checked and before the program is started: where
// another program could make their names lead elsewhere.
var beforeStart func()

// startProcess starts a registered command: the program by the file
// pluginregistry.OpenProgram checked, without a shell, in the registry's
// folder, with pipes of its own and in a session of its own.
func startProcess(env pluginregistry.Env, plugin pluginregistry.Plugin, dataFolder string, environ []string, grace time.Duration) (*process, error) {
	name := plugin.Name
	program, err := pluginregistry.OpenProgram(env, plugin)
	if err != nil {
		return nil, notStarted(name, err)
	}
	defer program.Close()
	dir, dirPath, err := pluginregistry.OpenFolder(env)
	if err != nil {
		return nil, notStarted(name, err)
	}
	defer dir.Close()
	guard := pluginregistry.NewGuard(env)
	defer guard.Close()
	if err := checkInterpreter(guard, program); err != nil {
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + name + " cannot be started: " +
			"its program, " + clean(plugin.Command[0], 400) + ", is a script, and the interpreter its first line names is not " +
			"one study runs. " + err.Error()}
	}

	if beforeStart != nil {
		beforeStart()
	}
	path, cwd, inherited, err := execNames(program, plugin.Command[0], dir, dirPath)
	if err != nil {
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + name +
			" was not started: " + err.Error()}
	}

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
	cmd := &exec.Cmd{
		Path: path,
		// The program is told the name it is registered by, whatever name it
		// is started through.
		Args:        slices.Clone(plugin.Command),
		Dir:         cwd,
		Env:         childEnviron(environ, guard.Inside, dirPath, dataFolder),
		Stdin:       pipes[0][0],
		Stdout:      pipes[1][1],
		Stderr:      pipes[2][1],
		ExtraFiles:  inherited,
		SysProcAttr: ownSession(),
	}
	err = cmd.Start()
	// The names given to the child are descriptors of this process: they
	// must stay open until it has started.
	runtime.KeepAlive(program)
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

	p := &process{cmd: cmd, stdin: pipes[0][1], stdout: pipes[1][0], stderr: pipes[2][0],
		said: &tail{max: StderrKept}, saidAll: make(chan struct{}), exited: make(chan struct{}), grace: grace}
	go func() {
		_ = cmd.Wait()
		// The plugin is the program study started. When it exits, whatever
		// it started and left behind goes with it: at once, while the
		// process group can only be its own.
		p.mu.Lock()
		signalGroup(cmd.Process, syscall.SIGKILL)
		p.collected = true
		p.mu.Unlock()
		close(p.exited)
	}()
	// What the program writes on its standard error is read, so that it
	// never blocks on a full pipe, and only its end is kept.
	go func() {
		_, _ = io.Copy(p.said, p.stderr)
		close(p.saidAll)
	}()
	return p, nil
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
func byName(program *os.File, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	for _, f := range []struct {
		file *os.File
		path string
	}{{program, programPath}, {dir, dirPath}} {
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

// childEnviron is the environment a plugin is started with: the learner's,
// without what would mislead it and without what would have it load what
// the agent can change.
//
//   - GIT_* go: GIT_DIR, GIT_INDEX_FILE and the like, set by a commit hook
//     or by study itself, would point a plugin that runs git at a repository
//     it knows nothing of.
//   - PWD and OLDPWD name the folder study was started in and the one
//     before, a Topic as likely as not. The plugin runs elsewhere: PWD is
//     set to where, and OLDPWD goes.
//   - Whatever names something inside the Study home goes, by inside, which
//     is the registry's own question (pluginregistry.Guard). The learner
//     starts their agent in a Topic, often with that Topic's virtual
//     environment switched on, and the MCP server inherits it: a PATH whose
//     first folder is the Topic's .venv/bin, a PYTHONPATH into the Topic. A
//     plugin started outside the sandbox with those would run what the
//     agent wrote there: a script started through env, a helper found on
//     PATH, a module imported by name. A value is a list of paths, divided
//     as PATH is: each path that leads through the Study home is taken out
//     of the list, and a variable left with nothing is taken out whole. So
//     is a variable that names such a path among other words, as
//     NODE_OPTIONS does with --require: there is no telling what is left of
//     an option without its path. Like the registry's look at a command's
//     arguments, this catches what is written as a path and is no
//     guarantee.
//   - The plugin's own folder is given anew, so that an inherited value
//     never stands for it. It is the one thing in the Study home a plugin is
//     told of.
//
// Everything else stays: the plugin is a program the learner chose, and it
// needs their PATH, their proxy and whatever its embedding endpoint is
// configured by.
func childEnviron(environ []string, inside func(path string) bool, pwd, dataFolder string) []string {
	// One look at each path is enough: the same folders are named again
	// and again.
	looked := map[string]bool{}
	insideOnce := func(path string) bool {
		is, ok := looked[path]
		if !ok {
			is = inside(path)
			looked[path] = is
		}
		return is
	}
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || name == "PWD" || name == "OLDPWD" || name == plugincontract.DataFolderEnv {
			continue
		}
		paths := filepath.SplitList(value)
		kept := paths[:0:0]
		among := false
		for _, path := range paths {
			if filepath.IsAbs(path) && insideOnce(path) {
				continue
			}
			kept = append(kept, path)
			among = among || namesAmongWords(path, insideOnce)
		}
		switch {
		case among:
		case len(kept) == len(paths):
			out = append(out, kv) // as it was, to the byte
		case len(kept) > 0:
			out = append(out, name+"="+strings.Join(kept, string(os.PathListSeparator)))
		}
	}
	return append(out, "PWD="+pwd, plugincontract.DataFolderEnv+"="+dataFolder)
}

// namesAmongWords reports whether one of the words of a value, or what
// follows the first = of a word, is an absolute path that leads through the
// Study home: --require /home/ada/study/x.js, --import=/home/ada/study/x.mjs.
func namesAmongWords(value string, inside func(string) bool) bool {
	for _, word := range strings.Fields(value) {
		if _, after, found := strings.Cut(word, "="); found && filepath.IsAbs(after) && inside(after) {
			return true
		}
		if word != value && filepath.IsAbs(word) && inside(word) {
			return true
		}
	}
	return false
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
// which writes there, cannot make the path lead elsewhere afterwards.
func checkInterpreter(guard *pluginregistry.Guard, program *os.File) error {
	script := program
	for range 4 {
		path := interpreterOf(script)
		if script != program {
			_ = script.Close()
		}
		// One that is not absolute is found from the folder the plugin is
		// started in, the registry's, where the agent does not write.
		if path == "" || !filepath.IsAbs(path) {
			return nil
		}
		if err := guard.CheckProgram(path); err != nil {
			return err
		}
		next, err := os.Open(path)
		if err != nil {
			return nil // it can be run and not read: there is no more to look at
		}
		script = next
	}
	if script != program {
		_ = script.Close()
	}
	return nil
}

// interpreterOf returns the program a script's first line names after #!,
// or nothing for a file that is no script.
func interpreterOf(file *os.File) string {
	// The system reads this much of a script's first line.
	head := make([]byte, 256)
	n, _ := file.ReadAt(head, 0)
	line, _, _ := strings.Cut(string(head[:n]), "\n")
	rest, isScript := strings.CutPrefix(line, "#!")
	if !isScript {
		return ""
	}
	words := strings.Fields(rest)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// hasExited reports whether the program exited, waiting up to d for it.
func (p *process) hasExited(d time.Duration) bool {
	select {
	case <-p.exited:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.exited:
		return true
	case <-timer.C:
		return false
	}
}

// signal sends sig to the program and to everything it started that is
// still in its process group, unless the program was collected already.
func (p *process) signal(sig syscall.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.collected {
		signalGroup(p.cmd.Process, sig)
	}
}

// kill ends the program at once, and everything it started: for a plugin
// that is stuck. The pipes are closed first, which frees whatever of study
// was reading or writing them. stop still has to follow.
func (p *process) kill() {
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	p.signal(syscall.SIGKILL)
}

// stop ends the program and everything it started, and waits for it: it
// closes the program's input, which asks an MCP server to exit, and its
// output; after the grace period it sends SIGTERM to the program's process
// group, and after another it kills the group. It takes three grace periods
// at most.
func (p *process) stop() {
	p.stopOnce.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdout.Close()
		if !p.hasExited(p.grace) {
			p.signal(syscall.SIGTERM)
			if !p.hasExited(p.grace) {
				p.signal(syscall.SIGKILL)
				p.hasExited(p.grace)
			}
		}
		_ = p.stderr.Close()
	})
}

// how says how the program ended, or that it has not.
func (p *process) how() string {
	if !p.hasExited(0) {
		return "it is still running"
	}
	if state := p.cmd.ProcessState; state != nil {
		return state.String()
	}
	return "it exited"
}

// wrote is the end of what the program wrote on its standard error. For a
// program that exited it waits, briefly, for the last of it to be read.
func (p *process) wrote() string {
	if p.hasExited(0) {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-p.saidAll:
		case <-timer.C:
		}
	}
	return p.said.String()
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
