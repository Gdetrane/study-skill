//go:build unix

package pluginclient_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// impatient are deadlines for a plugin that is expected to miss them.
func impatient(c *plugintest.Computer) pluginclient.Options {
	return pluginclient.Options{Version: "test", Environ: c.Environ(),
		StartTimeout: 400 * time.Millisecond, CallTimeout: 400 * time.Millisecond, SearchTimeout: 400 * time.Millisecond,
		StopGrace: 300 * time.Millisecond}
}

// refused starts the test plugin with args and returns the error it is
// refused with, and how the plugin was started when it wrote a record.
func refused(t *testing.T, opts func(*plugintest.Computer) pluginclient.Options, args ...string) (error, string) {
	t.Helper()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command(append([]string{"--record", record}, args...)...)...)
	began := time.Now()
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts(c))
	if err == nil {
		s.Close()
		t.Fatalf("Start succeeded with the plugin %v", args)
	}
	if took := time.Since(began); took > 8*time.Second {
		t.Errorf("Start took %s to give up", took)
	}
	return err, record
}

// gone fails the test unless the plugin that wrote the record is gone.
func gone(t *testing.T, record string) {
	t.Helper()
	got := plugintest.ReadRecord(t, record)
	if !plugintest.Gone(got.PID, 5*time.Second) {
		plugintest.Kill(got.PID)
		t.Errorf("the plugin, process %d, was left running", got.PID)
	}
}

// safe fails the test if a message holds anything a terminal would act on.
func safe(t *testing.T, message string) {
	t.Helper()
	if strings.ContainsFunc(message, func(r rune) bool { return r < 0x20 || r == 0x7f || r == 0x202e }) {
		t.Errorf("the message holds a control character: %q", message)
	}
}

// A plugin that does not start, answers something that is no MCP, hangs, or
// speaks a newer contract gives a documented code with advice, in good
// time, and is not left running.
func TestAPluginThatDoesNotSpeakIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts func(*plugintest.Computer) pluginclient.Options
		args []string
		code string
		want []string
	}{
		{"exits at once", quickOptions, []string{"--exit", "3", "--stderr", "cannot open the index: \x1b]0;gotcha\x07 disk full\n"},
			pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb stopped before it could open an MCP session (exit status 3)",
				"The last it wrote on its standard error: cannot open the index: \\u001b]0;gotcha\\u0007 disk full.",
				"study knowledge-base list shows the command"}},
		{"exits without a word", quickOptions, []string{"--exit", "0"}, pluginclient.CodeFailedPrecondition,
			[]string{"stopped before it could open an MCP session (exit status 0). Run its command"}},
		{"writes something that is no MCP", quickOptions, []string{"--garbage"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb does not speak MCP on its standard output: study read something there that is " +
				"no MCP message (invalid character 'L' looking for beginning of value), and stopped it",
				"only MCP messages on its standard output, each of 16 MiB at most, and anything else on its standard error"}},
		{"closes its output and stays", quickOptions, []string{"--close-output"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb closed its standard output before it could open an MCP session, so study stopped it"}},
		{"answers nothing", impatient, []string{"--hang", "start"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb did not open an MCP session within 400ms, so study stopped it",
				"study knowledge-base list shows the command"}},
		{"answers nothing and will not stop", impatient, []string{"--hang", "start", "--stubborn"}, pluginclient.CodeFailedPrecondition,
			[]string{"did not open an MCP session within 400ms, so study stopped it"}},
		// It has to start and open a session within the deadline, to miss it
		// at the contract: so the deadline is a longer one.
		{"never says which contract", unhurried, []string{"--hang", "contract"}, pluginclient.CodeFailedPrecondition,
			[]string{"did not say which contract it speaks within 4s, so study stopped it"}},
		{"speaks a newer contract", quickOptions, []string{"--contract", `{"version": 2, "collections": {"anything": "else"}}`},
			pluginclient.CodeNewerFormat,
			[]string{"Knowledge base plugin kb speaks version 2 of Lamplight's contract, and this version of study knows version 1: upgrade study"}},
		// (The MCP library reads the numbers of a result as it reads any
		// JSON number, so a large one comes back rounded, and written its
		// way.)
		{"speaks a contract newer than a number holds", quickOptions, []string{"--contract", `{"version": 9223372036854775808}`},
			pluginclient.CodeNewerFormat,
			[]string{"Knowledge base plugin kb speaks version 9223372036854776000 of Lamplight's contract, and this version of study knows version 1: upgrade study"}},
		{"speaks a contract of a version written large", quickOptions, []string{"--contract", `{"version": 1e30}`},
			pluginclient.CodeNewerFormat, []string{"Knowledge base plugin kb speaks version 1e+30 of Lamplight's contract"}},
		// Why a program failed is at the end of what it wrote, under
		// whatever it wrote first.
		{"fails under a long traceback", quickOptions, []string{"--exit", "1", "--stderr", "Traceback (most recent call last): " +
			strings.Repeat("File kb.py, line 12, in main; ", 60) + "KeyError: 'the index is from another version'"},
			pluginclient.CodeFailedPrecondition,
			[]string{"The last it wrote on its standard error: …", "in main; KeyError: 'the index is from another version'."}},
		{"sends study requests from the start", quickOptions, []string{"--ask", "start"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb sent study a request (ping), and study stopped it: a plugin answers what it is asked " +
				"and asks nothing, since study offers it nothing to call. docs/knowledge-base-contract.md describes the contract"}},
		{"asks study for its roots", quickOptions, []string{"--ask", "contract", "--ask-method", "roots/list", "--ask-times", "1"},
			pluginclient.CodeFailedPrecondition, []string{"Knowledge base plugin kb sent study a request (roots/list), and study stopped it"}},
		{"is another MCP server", quickOptions, []string{"--tools", "contract,status"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb does not speak Lamplight's contract: it is an MCP server without the index, cancel, search and passage tools",
				"docs/knowledge-base-contract.md"}},
		{"has no tools at all", quickOptions, []string{"--tools", "none"}, pluginclient.CodeFailedPrecondition,
			[]string{"does not speak Lamplight's contract"}},
		{"gives no version", quickOptions, []string{"--contract", `{"sources": ["file"], "search": ["keyword"]}`},
			pluginclient.CodeFailedPrecondition,
			[]string{"what its contract tool gave back is not what the contract describes: version is missing"}},
		{"gives a version that is no number", quickOptions, []string{"--contract", `{"version": "1", "sources": ["file"], "search": ["keyword"]}`},
			pluginclient.CodeFailedPrecondition, []string{"version is not a whole number of 1 or more"}},
		{"indexes nothing", quickOptions, []string{"--contract", `{"version": 1, "sources": [], "search": ["keyword"]}`},
			pluginclient.CodeFailedPrecondition, []string{"sources lists none of file, url"}},
		{"names a kind that is no word", quickOptions, []string{"--contract", `{"version": 1, "sources": ["file"], "search": ["keyword"], "locations": ["\u001b[31mpages"]}`},
			pluginclient.CodeFailedPrecondition, []string{`locations[0] is "\x1b[31mpages", not a kind`}},
		{"gives no object", quickOptions, []string{"--contract", `[1]`}, pluginclient.CodeFailedPrecondition,
			[]string{"what its contract tool gave back is not what the contract describes: it is not a JSON object"}},
		{"refuses to say", quickOptions, []string{"--refuse", "contract=internal:the index is locked"}, pluginclient.CodeFailedPrecondition,
			[]string{"Knowledge base plugin kb failed in its contract tool: the index is locked"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err, record := refused(t, tc.opts, tc.args...)
			if codeOf(err) != tc.code {
				t.Errorf("code = %q, want %q: %v", codeOf(err), tc.code, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message lacks %q:\n%s", want, err)
				}
			}
			if tc.name == "fails under a long traceback" && strings.Contains(err.Error(), "Traceback (most recent call last)") {
				t.Errorf("the message repeats the start of what the plugin wrote, and not only the end:\n%s", err)
			}
			safe(t, err.Error())
			gone(t, record)
		})
	}
}

func quickOptions(c *plugintest.Computer) pluginclient.Options { return quick(c) }

// unhurried is impatient with time for a plugin to start.
func unhurried(c *plugintest.Computer) pluginclient.Options {
	opts := impatient(c)
	opts.StartTimeout = 4 * time.Second
	return opts
}

// A program that cannot be run at all says why, and how to look.
func TestAProgramThatCannotBeRun(t *testing.T) {
	c := plugintest.NewComputer(t)
	bin := filepath.Join(c.Home, "bin")
	inTopic := filepath.Join(c.StudyHome, "linear-algebra", ".venv", "bin", "python")
	// write puts a file that may be run at path, and returns the path.
	write := func(path, content string) string {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write(inTopic, "")
	const notRun = "is a script, and the interpreter its first line names is not one study runs. "
	for name, tc := range map[string]struct {
		program string
		want    []string
	}{
		"no program for this computer": {write(filepath.Join(bin, "not-a-program"), "this is no program\n"),
			[]string{"Knowledge base plugin kb did not start: its program, " + filepath.Join(bin, "not-a-program") + ", could not be run (exec format error)"}},
		"a program that is gone": {filepath.Join(bin, "gone"),
			[]string{"Knowledge base plugin kb cannot be started: there is no program at"}},
		"a script without its interpreter": {write(filepath.Join(bin, "script"), "#!/nonexistent/interpreter\n"),
			[]string{"Knowledge base plugin kb cannot be started: its program, " + filepath.Join(bin, "script") + ", " + notRun +
				"there is no program at \"/nonexistent/interpreter\""}},
		// The system runs a script's interpreter, not the script, so the
		// interpreter is held to what the program is held to: it must not
		// be the agent's to change.
		"a script whose interpreter is in the Study home": {write(filepath.Join(bin, "in-topic"), "#! "+inTopic+" -u\n"),
			[]string{notRun + inTopic + " is inside the Study home"}},
		// The system would look for it from the folder the plugin is started
		// in, where nothing was checked.
		"a script whose interpreter is a relative path": {write(filepath.Join(bin, "relative"), "#!bin/python3 -u\n"),
			[]string{notRun + "bin/python3 is no absolute path", "Name the interpreter by its full path"}},
		"a script whose interpreter is a name": {write(filepath.Join(bin, "named"), "#!python3\n"),
			[]string{notRun + "python3 is no absolute path"}},
		"a script whose interpreter's interpreter is": {write(filepath.Join(bin, "twice"), "#!"+write(filepath.Join(bin, "wrapper"), "#!"+inTopic+"\n")+"\n"),
			[]string{notRun + inTopic + " is inside the Study home"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pluginclient.Start(context.Background(), c.Env(""), pluginregistry.Plugin{Name: "kb", Command: []string{tc.program}}, quick(c))
			if codeOf(err) != pluginclient.CodeFailedPrecondition {
				t.Fatalf("Start = %v; want failed_precondition", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message lacks %q:\n%s", want, err)
				}
			}
		})
	}
}

// A script whose first line is #!/usr/bin/env NAME is run by whatever env
// finds under the name in the PATH the plugin is given. A folder of that
// PATH is outside the Study home, and what a name in it leads to need not
// be: so the program env would start is found as env finds it, and held to
// what an interpreter is held to.
func TestAScriptStartedThroughEnv(t *testing.T) {
	const env = "/usr/bin/env"
	if _, err := os.Stat(env); err != nil {
		t.Skipf("there is no %s here", env)
	}
	c := plugintest.NewComputer(t)
	bin := filepath.Join(c.Home, "bin")
	tools, linked, empty := filepath.Join(c.Home, "tools"), filepath.Join(c.Home, "linked"), filepath.Join(c.Home, "empty")
	inTopic := filepath.Join(c.StudyHome, "linear-algebra", ".venv", "bin", "kb-python")
	for _, dir := range []string{bin, tools, linked, empty, filepath.Dir(inTopic)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The interpreter is the test plugin itself, under a name of its own in
	// a folder of PATH, as a package manager leaves a link.
	words := plugintest.Command()
	if err := os.Symlink(words[0], filepath.Join(tools, "kb-python")); err != nil {
		t.Fatal(err)
	}
	// What the agent wrote in a Topic's virtual environment, and a link to
	// it under the same name in another folder of PATH, outside the Study
	// home.
	if err := os.WriteFile(inTopic, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inTopic, filepath.Join(linked, "kb-python")); err != nil {
		t.Fatal(err)
	}
	// And one that nobody may run, which env passes over.
	if err := os.WriteFile(filepath.Join(empty, "kb-python"), []byte("no program\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := func(name, firstLine string) string {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte(firstLine+"\nnothing reads the rest\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := func(folders ...string) string { return "PATH=" + strings.Join(folders, string(os.PathListSeparator)) }
	const notRun = "is a script, and the interpreter its first line names is not one study runs. "

	t.Run("it starts what env finds", func(t *testing.T) {
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", script("found", "#!"+env+" -S kb-python "+words[1]), "--record", record, "--name", "through-env")
		// The folder without it and the one where it may not be run are
		// passed over, as env passes over them.
		s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c, path(bin, empty, tools, linked)))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		if s.Server().Name != "through-env" {
			t.Errorf("Server = %+v", s.Server())
		}
		if got := plugintest.ReadRecord(t, record); filepath.Base(got.Args[0]) != "kb-python" {
			t.Errorf("the interpreter was started as %q, want it under the name env found", got.Args[0])
		}
	})

	for name, tc := range map[string]struct {
		firstLine string
		environ   string
		want      []string
	}{
		// The first match is the one env starts: a good one later in PATH
		// does not make up for it.
		"the first one PATH has leads into the Study home": {"#!" + env + " kb-python", path(empty, linked, tools),
			[]string{notRun + "it has env look for kb-python in the folders of PATH, and the first one found is one your agent can change: " +
				filepath.Join(linked, "kb-python") + " is inside the Study home"}},
		"and with arguments for it": {"#!" + env + " -S kb-python -u", path(linked, tools),
			[]string{filepath.Join(linked, "kb-python") + " is inside the Study home"}},
		"no folder of PATH has it": {"#!" + env + " kb-python", path(bin, empty),
			[]string{notRun + "it has env look for kb-python in the folders of the PATH study gives a plugin (" + bin + ":" + empty +
				"), and none of them has a program of that name", "name the interpreter by its full path"}},
		// A PATH wholly inside the Study home is not given to a plugin.
		"the plugin is given no PATH": {"#!" + env + " kb-python", path(filepath.Dir(inTopic)),
			[]string{notRun + "it has env look for kb-python in the folders of PATH, and study gives this plugin no PATH"}},
		"env is given a path into the Study home": {"#!" + env + " " + inTopic,
			path(tools), []string{notRun + inTopic + " is inside the Study home"}},
		"env is given a relative path": {"#!" + env + " ./kb-python", path(tools),
			[]string{notRun + "./kb-python is no absolute path"}},
		// What else env is given decides what is started in ways study does
		// not follow.
		"env is given an option": {"#!" + env + " -i kb-python", path(tools),
			[]string{notRun + "it has env start -i, and study cannot tell which program that is", "#!/usr/bin/env python3"}},
		"env is given a setting": {"#!" + env + " -S PATH=" + filepath.Dir(inTopic) + " kb-python", path(tools),
			[]string{"study cannot tell which program that is"}},
	} {
		t.Run(name, func(t *testing.T) {
			plugin := pluginregistry.Plugin{Name: "refused", Command: []string{script("refused", tc.firstLine)}}
			_, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c, tc.environ))
			if codeOf(err) != pluginclient.CodeFailedPrecondition {
				t.Fatalf("Start = %v; want failed_precondition", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message lacks %q:\n%s", want, err)
				}
			}
			safe(t, err.Error())
			// A plugin that is refused leaves nothing behind.
			if _, err := os.Stat(c.DataFolder("refused")); err == nil {
				t.Errorf("a plugin that was refused was given a folder, %s", c.DataFolder("refused"))
			}
		})
	}
}

// A plugin that is a script is started through its interpreter, as the
// system starts any script. On Linux the script is run by its name in the
// folder study opened, so the interpreter is handed that name through the
// folder's descriptor: a script finds what is beside it through its own
// name, and where it really is by asking. It is what
// docs/knowledge-base-contract.md tells whoever writes one.
func TestAPluginThatIsAScript(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	// The learner registered a link to it, as an installer leaves one.
	real := filepath.Join(c.Home, "opt", "notes-kb", "kb")
	script := filepath.Join(c.Home, "bin", "kb")
	for _, dir := range []string{filepath.Dir(real), filepath.Dir(script)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The interpreter is the test plugin itself: nothing else is started.
	words := plugintest.Command()
	if err := os.WriteFile(real, []byte("#!"+words[0]+" "+words[1]+"\nnothing reads the rest\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(real), "helper.py"), []byte("# what the script loads\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, script); err != nil {
		t.Fatal(err)
	}
	plugin := c.Register(t, "kb", script, "--record", record, "--name", "a-script")
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if s.Server().Name != "a-script" || s.Contract().Version != 1 {
		t.Errorf("Server = %+v, Contract = %+v", s.Server(), s.Contract())
	}
	got := plugintest.ReadRecord(t, record)
	if runtime.GOOS != "linux" {
		if got.Script != script {
			t.Errorf("the script was handed to its interpreter as %q, want %q", got.Script, script)
		}
		return
	}
	if got.Script != "/proc/self/fd/3/kb" {
		t.Errorf("the script was handed to its interpreter as %q, want %q", got.Script, "/proc/self/fd/3/kb")
	}
	if got.Files[3] != filepath.Dir(real) {
		t.Errorf("descriptor 3 of the interpreter is %q, want the folder that holds the script, %s", got.Files[3], filepath.Dir(real))
	}
	// Through its own name it finds what is beside it, and where it is.
	if !slices.Contains(got.Beside, "helper.py") || !slices.Contains(got.Beside, "kb") {
		t.Errorf("beside itself the script finds %v, want its helper.py", got.Beside)
	}
	if got.ScriptPath != real {
		t.Errorf("the script's name leads to %q, want %q", got.ScriptPath, real)
	}
}

// An answer that is not the contract's is refused, and names what is wrong
// with it. The plugin is left running: it answered, and may answer the next
// call as it should.
func TestAnAnswerThatBreaksTheContractIsRefused(t *testing.T) {
	ctx := context.Background()
	const hash = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	source := plugincontract.Source{ID: "strang.k3f9a2", Title: "Linear Algebra", Kind: "file", Path: "/books/strang.pdf", Hash: hash}
	for _, tc := range []struct {
		name   string
		result string
		call   func(*pluginclient.Connection) error
		want   string
	}{
		{"index speaks of another Source", `index={"source": {"id": "other.1", "state": "indexing"}}`,
			func(s *pluginclient.Connection) error { _, err := s.Index(ctx, "c", source); return err },
			"what its index tool gave back is not what the contract describes: it speaks of Source other.1, and Source strang.k3f9a2 was to be indexed"},
		{"indexed without a hash", `index={"source": {"id": "strang.k3f9a2", "state": "indexed"}}`,
			func(s *pluginclient.Connection) error { _, err := s.Index(ctx, "c", source); return err },
			"Source strang.k3f9a2 is indexed and has no hash"},
		{"a state that is none", `status={"sources": [{"id": "strang.k3f9a2", "state": "\u001b[2Jready"}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			`the state of Source strang.k3f9a2 is "\x1b[2Jready", not indexed, indexing or failed`},
		{"failed without a reason", `status={"sources": [{"id": "strang.k3f9a2", "state": "failed"}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			"Source strang.k3f9a2 failed and has no reason"},
		{"progress beyond the end", `status={"sources": [{"id": "strang.k3f9a2", "state": "indexing", "progress": 1.5}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			"the progress of Source strang.k3f9a2 is not a number from 0 to 1"},
		{"a Source listed twice", `status={"sources": [{"id": "a.1", "state": "indexing"}, {"id": "a.1", "state": "indexing"}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			"it lists Source a.1 twice"},
		{"an id that is no id", `status={"sources": [{"id": "../../etc/passwd", "state": "indexing"}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			`"../../etc/passwd" is not a Source's id`},
		{"a state of the wrong type", `status={"sources": [{"id": "a.1", "state": 3}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Status(ctx, "c"); return err },
			"what its status tool gave back is not what the contract describes: it is not the JSON the contract describes: sources.state is a number, not a string"},
		{"the worse Passage first", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 1}, {"id": "b", "source": "s.1", "text": "y", "score": 2}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"Passage b has a higher score than the one before it: the best comes first"},
		{"more Passages than asked for", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2}, {"id": "b", "source": "s.1", "text": "y", "score": 1}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 1, nil); return err },
			"it holds 2 Passages, and 1 were asked for"},
		{"a Passage twice", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2}, {"id": "a", "source": "s.1", "text": "x", "score": 1}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"it holds Passage a twice"},
		{"a Passage of a Source that was not asked for", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2}]}`,
			func(s *pluginclient.Connection) error {
				_, err := s.Search(ctx, "c", "q", 5, []string{"s.2"})
				return err
			},
			"Passage a is from Source s.1, which the search was not limited to"},
		{"a Passage without text", `search={"passages": [{"id": "a", "source": "s.1", "text": " ", "score": 2}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"Passage a has no text"},
		{"a Passage that is too long", `search={"passages": [{"id": "a", "source": "s.1", "text": "` + strings.Repeat("x", plugincontract.MaxPassageBytes+1) + `", "score": 2}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"the text of Passage a is longer than 32768 bytes"},
		{"pages that end before they start", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2, "location": {"kind": "pages", "first": 4, "last": 3}}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"the pages of Passage a are 4 to 3"},
		{"an id that is a flag", `search={"passages": [{"id": "--help", "source": "s.1", "text": "x", "score": 2}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			`"--help" is not a Passage's id`},
		// What a location means is decided by its kind, and the plugin said
		// which kinds it gives.
		{"a location of a kind it did not list", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2, "location": {"kind": "seconds", "first": 4, "last": 9}}]}`,
			func(s *pluginclient.Connection) error { _, err := s.Search(ctx, "c", "q", 5, nil); return err },
			"what its search tool gave back is not what the contract describes: Passage a has a location of the kind seconds, and the plugin's contract lists pages"},
		{"a Passage given again with such a location", `passage={"passage": {"id": "a", "source": "s.1", "text": "x", "location": {"kind": "seconds", "first": 4, "last": 9}}}`,
			func(s *pluginclient.Connection) error { _, err := s.Passage(ctx, "c", "a"); return err },
			"what its passage tool gave back is not what the contract describes: Passage a has a location of the kind seconds, and the plugin's contract lists pages"},
		{"another Passage than was asked for", `passage={"passage": {"id": "b", "source": "s.1", "text": "x"}}`,
			func(s *pluginclient.Connection) error { _, err := s.Passage(ctx, "c", "a"); return err },
			"it is Passage b, and Passage a was asked for"},
		{"text where an object goes", `passage="see above"`,
			func(s *pluginclient.Connection) error { _, err := s.Passage(ctx, "c", "a"); return err },
			"what its passage tool gave back is not what the contract describes: it is not a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := plugintest.NewComputer(t)
			s := start(t, c, "--result", tc.result)
			err := tc.call(s)
			if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "Knowledge base plugin kb does not speak Lamplight's contract") {
				t.Errorf("the call = %v\nwant failed_precondition with %q", err, tc.want)
			}
			if err != nil {
				safe(t, err.Error())
			}
			// It answered, so it is still there to ask.
			if _, err := s.Cancel(ctx, "c", "strang.k3f9a2"); err != nil {
				t.Errorf("Cancel afterwards = %v", err)
			}
		})
	}
}

// What a plugin refuses, it refuses with a code, and each code is one of
// study's.
func TestAToolErrorCarriesStudysCode(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, arg, code, want string
	}{
		{"not_found", "--refuse=passage=not_found:collection c has no Passage a", pluginclient.CodeNotFound,
			"Knowledge base plugin kb does not have what its passage tool was asked for: collection c has no Passage a"},
		{"invalid_argument", "--refuse=passage=invalid_argument:ids are numbers here", pluginclient.CodeInvalidArgument,
			"Knowledge base plugin kb does not take what its passage tool was given: ids are numbers here"},
		{"unsupported", "--refuse=passage=unsupported:this index keeps no Passages", pluginclient.CodeFailedPrecondition,
			"Knowledge base plugin kb cannot do what its passage tool was asked: this index keeps no Passages"},
		{"internal", "--refuse=passage=internal:database is locked", pluginclient.CodeFailedPrecondition,
			"Knowledge base plugin kb failed in its passage tool: database is locked. The plugin's own messages may say more"},
		{"a code of its own", "--refuse=passage=E_BUSY:try later", pluginclient.CodeFailedPrecondition,
			"Knowledge base plugin kb failed in its passage tool: try later"},
		{"no reason", "--refuse=passage=internal:", pluginclient.CodeFailedPrecondition, "failed in its passage tool: it gave no reason"},
		{"text and no JSON", "--plain-error=passage=Traceback (most recent call last):\n  File \"kb.py\"\nKeyError: 'a'",
			pluginclient.CodeFailedPrecondition,
			`Knowledge base plugin kb failed in its passage tool: Traceback (most recent call last): File "kb.py" KeyError: 'a'`},
		{"a terminal's escapes", "--refuse=passage=internal:\x1b[31mred\x1b[0m \u202edesrever", pluginclient.CodeFailedPrecondition,
			`failed in its passage tool: \u001b[31mred\u001b[0m \u202edesrever`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := start(t, plugintest.NewComputer(t), tc.arg)
			_, err := s.Passage(ctx, "c", "a")
			if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Passage = %v (%s)\nwant %s with %q", err, codeOf(err), tc.code, tc.want)
			}
			if err != nil {
				safe(t, err.Error())
			}
		})
	}
}

// A request the contract does not allow is not sent: study checks what it
// asks before a plugin has to.
func TestWhatTheContractDoesNotAllowIsNotAsked(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	// The plugin would fail the test's own way if it were asked.
	s := start(t, c, "--sources", "file", "--plain-error=index=asked", "--plain-error=status=asked", "--plain-error=cancel=asked",
		"--plain-error=search=asked", "--plain-error=passage=asked")
	file := book(t, c)
	for name, call := range map[string]func() error{
		"a collection that is a path": func() error { _, err := s.Status(ctx, "../other"); return err },
		"no collection":               func() error { _, err := s.Status(ctx, ""); return err },
		"a relative path": func() error {
			src := file
			src.Path = "strang.txt"
			_, err := s.Index(ctx, "c", src)
			return err
		},
		"a hash that is none": func() error {
			src := file
			src.Hash = "md5:abc"
			_, err := s.Index(ctx, "c", src)
			return err
		},
		"a Source without a title": func() error {
			src := file
			src.Title = ""
			_, err := s.Index(ctx, "c", src)
			return err
		},
		"a kind that is none": func() error {
			src := file
			src.Kind = "recording"
			_, err := s.Index(ctx, "c", src)
			return err
		},
		"an address with a password": func() error {
			_, err := s.Index(ctx, "c", plugincontract.Source{ID: "page.1", Title: "Page", Kind: "url", URL: "https://ada:secret@example.org/"})
			return err
		},
		"an empty query":                func() error { _, err := s.Search(ctx, "c", "  ", 5, nil); return err },
		"no limit":                      func() error { _, err := s.Search(ctx, "c", "q", 0, nil); return err },
		"too high a limit":              func() error { _, err := s.Search(ctx, "c", "q", plugincontract.MaxSearchLimit+1, nil); return err },
		"an empty query in no Source":   func() error { _, err := s.Search(ctx, "c", "", 5, []string{}); return err },
		"a Source id that is a path":    func() error { _, err := s.Search(ctx, "c", "q", 5, []string{"../x"}); return err },
		"a Passage id that is a flag":   func() error { _, err := s.Passage(ctx, "c", "--help"); return err },
		"a Source to stop that is none": func() error { _, err := s.Cancel(ctx, "c", ""); return err },
	} {
		if err := call(); codeOf(err) != pluginclient.CodeInvalidArgument || !strings.Contains(err.Error(), "Knowledge base plugin kb was not asked") ||
			strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v; want invalid_argument, and nothing asked", name, err)
		}
	}
	// A kind of Source the plugin said it does not index is not asked for
	// either.
	_, err := s.Index(ctx, "c", plugincontract.Source{ID: "page.1", Title: "Page", Kind: "url", URL: "https://example.org/"})
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not index a Source of the kind url: it indexes file") {
		t.Errorf("Index of a kind the plugin does not index = %v", err)
	}
}

// A plugin may give its results as text alone, as some MCP libraries do.
func TestResultsAsTextAlone(t *testing.T) {
	c := plugintest.NewComputer(t)
	s := start(t, c, "--text-only")
	source := book(t, c)
	if _, err := s.Index(context.Background(), "c", source); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if done := indexed(t, s, "c", source.ID); done.State != plugincontract.StateIndexed {
		t.Errorf("Status = %+v", done)
	}
	// And its refusals.
	_, err := s.Passage(context.Background(), "c", "nothing")
	if codeOf(err) != pluginclient.CodeNotFound {
		t.Errorf("Passage = %v; want not_found", err)
	}
}

// A call that is not answered in time ends, and the plugin with it: one that
// hangs is not left running, and not asked again.
func TestACallThatIsNotAnsweredEnds(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "search", "--stderr", "loading the model\n")...)
	opts := impatient(c)
	opts.StartTimeout = 10 * time.Second
	s, err := pluginclient.Start(ctx, c.Env(""), plugin, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Ended() {
		t.Error("a Connection that was just started has ended")
	}
	began := time.Now()
	_, err = s.Search(ctx, "c", "inverse", 5, nil)
	if codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin kb did not answer its search tool within 400ms, so study stopped it. "+
			"The last it wrote on its standard error: loading the model.") {
		t.Errorf("Search = %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Search took %s to give up", took)
	}
	gone(t, record)
	// Whoever keeps Sessions sees that this one is over, and starts another.
	if !s.Ended() {
		t.Error("the Connection of a plugin that was stopped has not ended")
	}
	if _, err := s.Status(ctx, "c"); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "can no longer be asked: it did not answer in time, and study stopped it") {
		t.Errorf("Status afterwards = %v", err)
	}
}

// A plugin that stops reading leaves study stuck writing to it. Its
// deadline still holds: the plugin is killed, which frees the write.
func TestAPluginThatStopsReadingIsKilled(t *testing.T) {
	defer pluginclient.SetUnstick(200 * time.Millisecond)()
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--freeze", "status")...)
	opts := impatient(c)
	opts.StartTimeout, opts.CallTimeout = 10*time.Second, 30*time.Second
	s, err := pluginclient.Start(ctx, c.Env(""), plugin, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The first call stops the plugin dead. It is not answered either.
	frozen := make(chan error, 1)
	go func() {
		_, err := s.Status(ctx, "c")
		frozen <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(record + ".frozen"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the plugin did not stop dead")
		}
	}
	// More than a pipe holds: the write cannot finish.
	sources := make([]string, plugincontract.MaxSources)
	for i := range sources {
		sources[i] = "a-long-source-id-to-fill-the-pipe-with." + strings.Repeat("x", 20) + "." + string(rune('a'+i%26)) + itoa(i)
	}
	began := time.Now()
	_, err = s.Search(ctx, "c", "inverse", 5, sources)
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "did not answer its search tool within 400ms, so study stopped it") {
		t.Errorf("Search = %v", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Search took %s to give up", took)
	}
	select {
	case err := <-frozen:
		if codeOf(err) != pluginclient.CodeFailedPrecondition {
			t.Errorf("the call the plugin froze in = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the call the plugin froze in never returned")
	}
	gone(t, record)
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var out []byte
	for ; n > 0; n /= 10 {
		out = append([]byte{digits[n%10]}, out...)
	}
	return string(out)
}

// Stopping the request stops the call at once, with the canceled code. The
// Connection stays open, so that whoever holds it can still tell the plugin to
// stop indexing; closing it is what stops the plugin.
func TestStoppingTheRequestStopsTheCall(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "search")...)
	// The plugin hangs in its search tool for good, so it has to be killed
	// in the end: a short grace keeps the test short.
	opts := quick(c)
	opts.StopGrace = 200 * time.Millisecond
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, stop := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, stop)
	began := time.Now()
	_, err = s.Search(ctx, "c", "inverse", 5, nil)
	if codeOf(err) != pluginclient.CodeCanceled || !strings.Contains(err.Error(), "the command was stopped before Knowledge base plugin kb answered") {
		t.Errorf("Search = %v; want canceled", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("Search took %s to stop", took)
	}
	// A request that was stopped already asks nothing.
	if _, err := s.Status(ctx, "c"); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Status with the request stopped = %v", err)
	}
	// The plugin is still there for a request that goes on.
	if stopped, err := s.Cancel(context.Background(), "c", "strang.k3f9a2"); err != nil || stopped {
		t.Errorf("Cancel afterwards = %v, %v", stopped, err)
	}
	s.Close()
	gone(t, record)

	// A request stopped while the plugin starts leaves nothing running.
	record = filepath.Join(t.TempDir(), "record.json")
	plugin = c.Register(t, "slow", plugintest.Command("--record", record, "--hang", "start")...)
	ctx, stop = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, stop)
	if _, err := pluginclient.Start(ctx, c.Env(""), plugin, quick(c)); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Start with the request stopped = %v; want canceled", err)
	}
	gone(t, record)
	// And one stopped before it began starts nothing.
	record = filepath.Join(t.TempDir(), "record.json")
	plugin = c.Register(t, "never", plugintest.Command("--record", record)...)
	if _, err := pluginclient.Start(ctx, c.Env(""), plugin, quick(c)); codeOf(err) != pluginclient.CodeCanceled {
		t.Errorf("Start with the request stopped before = %v; want canceled", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("a plugin was started for a request that was stopped already")
	}
}

// A plugin that ignores being asked to stop is killed, and what a plugin
// started goes with it.
func TestStoppingAPluginStopsAllOfIt(t *testing.T) {
	c := plugintest.NewComputer(t)
	dir := t.TempDir()
	record, child := filepath.Join(dir, "record.json"), filepath.Join(dir, "child.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--stubborn", "--child", child)...)
	opts := quick(c)
	opts.StopGrace = 300 * time.Millisecond
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts)
	if err != nil {
		t.Fatal(err)
	}
	parent, started := plugintest.ReadRecord(t, record), plugintest.ReadRecord(t, child)
	defer plugintest.Kill(parent.PID)
	defer plugintest.Kill(started.PID)
	began := time.Now()
	s.Close()
	if took := time.Since(began); took < 500*time.Millisecond || took > 5*time.Second {
		t.Errorf("Close took %s; want it to ask, then signal, then kill, a grace period apart", took)
	}
	for what, pid := range map[string]int{"the plugin": parent.PID, "the program the plugin started": started.PID} {
		if !plugintest.Gone(pid, 5*time.Second) {
			t.Errorf("%s, process %d, is still there after Close", what, pid)
		}
	}

	// A plugin that exits by itself takes what it started with it too.
	record, child = filepath.Join(dir, "record2.json"), filepath.Join(dir, "child2.json")
	plugin = c.Register(t, "kb", plugintest.Command("--record", record, "--child", child, "--exit", "1")...)
	if _, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts); codeOf(err) != pluginclient.CodeFailedPrecondition {
		t.Errorf("Start = %v", err)
	}
	if left := plugintest.ReadRecord(t, child); !plugintest.Gone(left.PID, 5*time.Second) {
		plugintest.Kill(left.PID)
		t.Errorf("the program a plugin started, process %d, outlived the plugin", left.PID)
	}
}

// One message from a plugin is read up to a limit and no further.
func TestAnAnswerOverTheLimitIsNotRead(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--flood", "status")...)
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = s.Status(context.Background(), "c")
	runtime.ReadMemStats(&after)
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "does not speak MCP on its standard output") {
		t.Errorf("Status = %v", err)
	}
	if grown := int64(after.TotalAlloc) - int64(before.TotalAlloc); grown > 8*plugincontract.MaxMessageBytes {
		t.Errorf("study allocated %d bytes reading an answer over the limit of %d", grown, plugincontract.MaxMessageBytes)
	}
	gone(t, record)
}

// The folder a plugin is given is in the learner's state folder, and never
// where the agent writes: a state folder that is the Study home, is inside
// it or is reached through it gives a plugin no folder, and no plugin.
func TestThePluginsFolderIsOutsideTheStudyHome(t *testing.T) {
	for name, vars := range map[string]func(t *testing.T, c *plugintest.Computer) (state string){
		"the state folder is in the Study home": func(t *testing.T, c *plugintest.Computer) string {
			return filepath.Join(c.StudyHome, ".state")
		},
		"the state folder is the Study home": func(t *testing.T, c *plugintest.Computer) string { return c.StudyHome },
		"the state folder is reached through the Study home": func(t *testing.T, c *plugintest.Computer) string {
			link := filepath.Join(c.StudyHome, "state")
			if err := os.Symlink(t.TempDir(), link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"the home folder's state folder leads into the Study home": func(t *testing.T, c *plugintest.Computer) string {
			if err := os.MkdirAll(filepath.Join(c.Home, ".local"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(c.StudyHome, "topic"), filepath.Join(c.Home, ".local", "state")); err != nil {
				t.Fatal(err)
			}
			return ""
		},
		"a folder on the way is a link into the Study home": func(t *testing.T, c *plugintest.Computer) string {
			state := filepath.Join(c.Home, "state")
			if err := os.MkdirAll(filepath.Join(state, "lamplight"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(c.StudyHome, filepath.Join(state, "lamplight", "knowledge-base-plugins")); err != nil {
				t.Fatal(err)
			}
			return state
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			if err := os.Mkdir(filepath.Join(c.StudyHome, "topic"), 0o755); err != nil {
				t.Fatal(err)
			}
			state := vars(t, c)
			env := c.Env("")
			env.Getenv = func(key string) string {
				if key == "XDG_STATE_HOME" {
					return state
				}
				return c.Getenv(key)
			}
			before := listing(t, c.StudyHome)
			record := filepath.Join(t.TempDir(), "record.json")
			plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
			s, err := pluginclient.Start(context.Background(), env, plugin, quick(c))
			if err == nil {
				s.Close()
			}
			if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "is inside the Study home, "+c.StudyHome+", or is reached through it") ||
				!strings.Contains(err.Error(), "set XDG_STATE_HOME to a folder outside the Study home") {
				t.Errorf("Start = %v; want it refused, with what to do", err)
			}
			if _, err := os.Stat(record); err == nil {
				t.Error("the plugin was started")
			}
			if now := listing(t, c.StudyHome); now != before {
				t.Errorf("study made something in the Study home:\nbefore: %s\nnow:    %s", before, now)
			}
		})
	}

	// A state folder reached through a link outside the Study home is the
	// folder the link leads to, and the plugin is told that path.
	t.Run("a link outside the Study home is followed", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		disk := t.TempDir()
		if real, err := filepath.EvalSymlinks(disk); err == nil {
			disk = real
		}
		link := filepath.Join(c.Home, "state")
		if err := os.Symlink(disk, link); err != nil {
			t.Fatal(err)
		}
		env := c.Env("")
		env.Getenv = func(key string) string {
			if key == "XDG_STATE_HOME" {
				return link
			}
			return c.Getenv(key)
		}
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
		s, err := pluginclient.Start(context.Background(), env, plugin, quick(c))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		want := filepath.Join(disk, "lamplight", "knowledge-base-plugins", "kb")
		if got := plugintest.ReadRecord(t, record); s.DataFolder() != want || got.Env[plugincontract.DataFolderEnv] != want {
			t.Errorf("DataFolder = %q, and the plugin was told %q; want the folder the link leads to, %s", s.DataFolder(),
				got.Env[plugincontract.DataFolderEnv], want)
		}
	})

	// Without a home folder there is no state folder to find.
	t.Run("no state folder", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		env := c.Env("")
		env.Getenv = func(key string) string {
			if key == "HOME" {
				return "home"
			}
			return c.Getenv(key)
		}
		// The registry is found through XDG_CONFIG_HOME then.
		config := filepath.Dir(c.ConfigDir)
		inner := env.Getenv
		env.Getenv = func(key string) string {
			if key == "XDG_CONFIG_HOME" {
				return config
			}
			return inner(key)
		}
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
		s, err := pluginclient.Start(context.Background(), env, plugin, quick(c))
		if err == nil {
			s.Close()
		}
		if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "keeps its index in (HOME is \"home\", which is not "+
			"an absolute path): set HOME, or XDG_STATE_HOME, to an absolute path") {
			t.Errorf("Start = %v; want it refused, with what to do", err)
		}
		if _, err := os.Stat(record); err == nil {
			t.Error("the plugin was started")
		}
	})
}

// listing names everything in a folder, links as links.
func listing(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names = append(names, strings.TrimPrefix(path, dir)+" "+entry.Type().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(names, ", ")
}

// What is started is the program that was checked, in the folder that was
// checked, whatever the folders on the way to them lead to by the time it
// starts. Linux runs the program from the folder it opened, and enters the
// open folder; elsewhere a last look refuses a name that moved.
func TestWhatWasCheckedIsWhatIsStarted(t *testing.T) {
	copyOf := func(t *testing.T, path string) string {
		t.Helper()
		in, err := os.Open(plugintest.Command()[0])
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Run("a folder on the way to the program", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		bin := filepath.Join(c.Home, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		checked := copyOf(t, filepath.Join(bin, "kb"))
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", checked, plugintest.Arg, "--record", record)
		// Between the check and the start, the folder's name comes to lead
		// to another folder, with another program under the same name.
		moved := bin + ".checked"
		defer pluginclient.SetBeforeStart(func() {
			if err := os.Rename(bin, moved); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Error(err)
			}
			copyOf(t, checked)
		})()
		s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
		if runtime.GOOS != "linux" {
			if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "came to name something else") {
				t.Errorf("Start = %v; want it refused", err)
			}
			if _, err := os.Stat(record); err == nil {
				t.Error("a program was started")
			}
			return
		}
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		if got := plugintest.ReadRecord(t, record); got.Exe != filepath.Join(moved, "kb") || got.Files[3] != moved {
			t.Errorf("the program that runs is %q, from the folder %q; want the one that was checked, now in %s", got.Exe, got.Files[3], moved)
		}
	})
	// The program's own name, in its own folder, is looked at last: whoever
	// can put another file there could have done so before the check. A
	// file that took the name since is still not started.
	t.Run("the program", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		bin := filepath.Join(c.Home, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		checked, other := copyOf(t, filepath.Join(bin, "kb")), copyOf(t, filepath.Join(bin, "other"))
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", checked, plugintest.Arg, "--record", record)
		defer pluginclient.SetBeforeStart(func() {
			if err := os.Rename(checked, checked+".checked"); err != nil {
				t.Error(err)
			}
			if err := os.Rename(other, checked); err != nil {
				t.Error(err)
			}
		})()
		s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
		if err == nil {
			s.Close()
		}
		if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), checked+" came to name something else") {
			t.Errorf("Start = %v; want it refused", err)
		}
		if _, err := os.Stat(record); err == nil {
			t.Error("a program was started")
		}
	})
	t.Run("the folder", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
		moved := c.ConfigDir + ".checked"
		defer pluginclient.SetBeforeStart(func() {
			if err := os.Rename(c.ConfigDir, moved); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(c.ConfigDir, 0o755); err != nil {
				t.Error(err)
			}
		})()
		s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
		if runtime.GOOS != "linux" {
			if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "came to name something else") {
				t.Errorf("Start = %v; want it refused", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		if got := plugintest.ReadRecord(t, record); got.Dir != moved {
			t.Errorf("the plugin was started in %s; want the folder that was checked, now %s", got.Dir, moved)
		}
	})
}

// A plugin that sends study requests is stopped at the first one. study
// offers a plugin nothing to call, and the MCP library would answer each
// request in a goroutine of its own, which a plugin that reads no answer
// leaves waiting: without end, and with them study's memory.
func TestAPluginThatSendsRequestsIsStopped(t *testing.T) {
	ctx := context.Background()
	// While study asks nothing: nobody is waiting for the plugin, and the
	// Connection still ends, and says why to whoever asks next.
	t.Run("while study asks nothing", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--ask", "idle")...)
		before := heap()
		s, err := pluginclient.Start(ctx, c.Env(""), plugin, quick(c))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		for deadline := time.Now().Add(10 * time.Second); !s.Ended(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the Connection of a plugin that sends requests did not end")
			}
		}
		gone(t, record)
		if _, err := s.Status(ctx, "c"); codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(),
			"Knowledge base plugin kb can no longer be asked: it sent study a request (ping), which a plugin never does, and study stopped it") {
			t.Errorf("Status afterwards = %v", err)
		}
		if grown := heap() - before; grown > 64<<20 {
			t.Errorf("study holds %d MiB more after a plugin flooded it with requests", grown>>20)
		}
	})
	// In the middle of a call: the call ends at once, not at its deadline.
	t.Run("in the middle of a call", func(t *testing.T) {
		c := plugintest.NewComputer(t)
		record := filepath.Join(t.TempDir(), "record.json")
		plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--ask", "search")...)
		before := heap()
		s, err := pluginclient.Start(ctx, c.Env(""), plugin, quick(c))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		began := time.Now()
		_, err = s.Search(ctx, "c", "inverse", 5, nil)
		if codeOf(err) != pluginclient.CodeFailedPrecondition ||
			!strings.Contains(err.Error(), "Knowledge base plugin kb sent study a request (ping), and study stopped it") {
			t.Errorf("Search = %v", err)
		}
		if took := time.Since(began); took > 5*time.Second {
			t.Errorf("Search took %s to end: the first request ends the Connection, not the call's deadline", took)
		}
		if !s.Ended() {
			t.Error("the Connection has not ended")
		}
		gone(t, record)
		if grown := heap() - before; grown > 64<<20 {
			t.Errorf("study holds %d MiB more after a plugin flooded it with requests", grown>>20)
		}
	})
}

// Notifications a plugin may send, and study passes over them. A flood of
// them costs study nothing it keeps: the call still ends at its deadline.
func TestAFloodOfNotificationsIsPassedOver(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--notify", "search")...)
	opts := impatient(c)
	opts.StartTimeout, opts.SearchTimeout = 10*time.Second, 1500*time.Millisecond
	before := heap()
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	_, err = s.Search(context.Background(), "c", "inverse", 5, nil)
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "did not answer its search tool within 1.5s, so study stopped it") {
		t.Errorf("Search = %v", err)
	}
	gone(t, record)
	if grown := heap() - before; grown > 256<<20 {
		t.Errorf("study holds %d MiB more after a plugin flooded it with notifications for a second and a half", grown>>20)
	}
}

// heap is how much memory the program holds, after collecting what it no
// longer does.
func heap() int64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return int64(stats.HeapAlloc)
}

// A Connection has ended as soon as its program has exited, whether or not
// anything was asked of it since: whoever keeps one starts another.
func TestAConnectionEndsWhenItsProgramExits(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	s := start(t, c, "--record", record)
	if s.Ended() {
		t.Fatal("a Connection that was just started has ended")
	}
	// The plugin dies of something that is not study's doing.
	plugintest.Kill(plugintest.ReadRecord(t, record).PID)
	for deadline := time.Now().Add(10 * time.Second); !s.Ended(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the Connection of a plugin that has exited did not end until it was asked something")
		}
	}
	if _, err := s.Status(ctx, "c"); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "Knowledge base plugin kb can no longer be asked: its program has exited (signal: killed)") {
		t.Errorf("Status afterwards = %v", err)
	}
}

// Everything study does to start a plugin is under the one deadline, what it
// does before the program runs included: a drive that does not answer holds
// up the walk to a program as surely as a plugin holds up its answer.
func TestStartingIsUnderOneDeadline(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
	release := make(chan struct{})
	defer pluginclient.SetBeforeStart(func() { <-release })()
	began := time.Now()
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, impatient(c))
	if err == nil {
		s.Close()
	}
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "Knowledge base plugin kb could not be started within 400ms") {
		t.Errorf("Start = %v; want it to give up at its deadline", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Start took %s to give up", took)
	}
	// What it comes to start, late, is stopped.
	close(release)
	gone(t, record)
}
