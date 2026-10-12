package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// collectionSuffix is what a collection's name ends with: a hash of where
// the Study home is, which is another folder in every run of the tests.
var collectionSuffix = regexp.MustCompile(`-[a-z2-7]{32}\b`)

// shelfComputer is a learner's computer on which the test plugin is
// registered as kb, with a Topic, linear-algebra, that names it.
type shelfComputer struct {
	*kbComputer
	// record is where the plugin writes how it was started, and beside it
	// what it was asked.
	record string
	ids    atomic.Int64
	// plugins says how long a plugin has.
	plugins core.PluginOptions
	// stdin is what study reads on its standard input.
	stdin string
}

// newShelfComputer registers the test plugin as kb, started with args, and
// creates the Topic.
func newShelfComputer(t *testing.T, args ...string) *shelfComputer {
	t.Helper()
	s := &shelfComputer{kbComputer: newKBComputer(t), record: filepath.Join(t.TempDir(), "record.json")}
	s.plugins = core.PluginOptions{
		Environ:      func() []string { return []string{"HOME=" + s.home, "STUDY_HOME=" + s.env["STUDY_HOME"]} },
		StartTimeout: 20 * time.Second, StopGrace: 300 * time.Millisecond, GlanceTimeout: 20 * time.Second,
		IndexPoll: 10 * time.Millisecond, CancelTimeout: 5 * time.Second,
	}
	s.registerWith(t, args...)
	s.ok(t, "topic", "create", "--title", "Linear algebra")
	s.ok(t, "topic", "update", "linear-algebra", "--knowledge-base", "kb")
	return s
}

// registerWith registers the test plugin as kb anew, started with args.
func (s *shelfComputer) registerWith(t *testing.T, args ...string) {
	t.Helper()
	s.ok(t, append([]string{"knowledge-base", "add", "kb", "--replace", "--"},
		plugintest.Command(append([]string{"--record", s.record}, args...)...)...)...)
}

// study runs study on the computer until ctx ends, with Event ids that are
// the computer's own and never repeat, and replaces in what it prints what
// differs from one run of the tests to the next: the home folder, the test
// binary that is the plugin, the port of a service on this computer, the
// ids the tests make, and the hash a collection's name ends with.
func (s *shelfComputer) study(ctx context.Context, args ...string) result {
	opts := core.Options{
		Getenv:  func(key string) string { return s.env[key] },
		Dir:     s.home,
		Now:     func() time.Time { return fixedNow },
		NewID:   func() string { return fmt.Sprintf("id%03d", s.ids.Add(1)) },
		Plugins: s.plugins,
	}
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, args, strings.NewReader(s.stdin), &stdout, &stderr, opts)
	norm := func(text string) string {
		if resolved, err := filepath.EvalSymlinks(s.home); err == nil {
			text = strings.ReplaceAll(text, resolved, "$HOME")
		}
		text = strings.ReplaceAll(text, s.home, "$HOME")
		text = strings.ReplaceAll(text, plugintest.Command()[0], "$PLUGIN")
		text = loopback.ReplaceAllString(text, "127.0.0.1:PORT")
		return collectionSuffix.ReplaceAllString(testIDs.ReplaceAllString(text, "<id>"), "-<collection>")
	}
	return result{code: code, stdout: norm(stdout.String()), stderr: norm(stderr.String())}
}

func (s *shelfComputer) run(t *testing.T, args ...string) result {
	t.Helper()
	return s.study(context.Background(), args...)
}

// ok runs study and fails the test unless it succeeds.
func (s *shelfComputer) ok(t *testing.T, args ...string) result {
	t.Helper()
	r := s.run(t, args...)
	if r.code != cli.ExitOK {
		t.Fatalf("study %s: exit %d, stdout %q, stderr %q", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// book writes a file in the learner's Books folder, adds it to the Topic as
// a Source, and returns the Source's id.
func (s *shelfComputer) book(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(s.home, "Books", name)
	writeFile(t, path, content)
	return s.source(t, "--file", path)
}

// source adds a Source to the Topic and returns its id, as study wrote it.
func (s *shelfComputer) source(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts := core.Options{Getenv: func(key string) string { return s.env[key] }, Dir: s.home, Now: func() time.Time { return fixedNow },
		NewID: func() string { return fmt.Sprintf("id%03d", s.ids.Add(1)) }, Plugins: s.plugins}
	code := cli.Run(context.Background(), append([]string{"source", "add", "linear-algebra", "--json"}, args...),
		strings.NewReader(""), &stdout, &stderr, opts)
	var out struct {
		Data struct {
			Source struct {
				ID string `json:"id"`
			} `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); code != cli.ExitOK || err != nil || out.Data.Source.ID == "" {
		t.Fatalf("study source add %v: exit %d, %v, stdout %s, stderr %s", args, code, err, stdout.String(), stderr.String())
	}
	return out.Data.Source.ID
}

func (s *shelfComputer) calls(t *testing.T) []plugintest.Call {
	return plugintest.ReadCalls(t, s.record)
}

// indexed are the ids of the Sources the plugin was asked to index.
func indexedSources(calls []plugintest.Call) []string {
	var out []string
	for _, call := range calls {
		if call.Tool == "index" {
			out = append(out, call.Source())
		}
	}
	return out
}

// nothingRunning fails the test when the plugin's last program is still
// there.
func (s *shelfComputer) nothingRunning(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(s.record); err != nil {
		return
	}
	if started := plugintest.ReadRecord(t, s.record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
}

// bookOf is a text of n pages, each with one paragraph.
func bookOf(n int, word string) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteString("\f")
		}
		fmt.Fprintf(&b, "Page %d speaks of %s.\n", i+1, word)
	}
	return b.String()
}

// A Topic names a plugin, its Sources are listed with what the plugin has
// indexed, and study source index has the plugin index them: what each
// command prints, for people and as JSON.
func TestSourceIndexCommands(t *testing.T) {
	s := newShelfComputer(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body><p>A goroutine is a lightweight thread.</p></body></html>")
	}))
	defer page.Close()
	check := func(name string, code int, args ...string) result {
		t.Helper()
		r := s.run(t, args...)
		if r.code != code {
			t.Errorf("%s: exit %d, want %d (stdout: %s, stderr: %s)", name, r.code, code, r.stdout, r.stderr)
		}
		if strings.HasSuffix(name, ".json") && r.stderr != "" {
			t.Errorf("%s: --json wrote to stderr: %q", name, r.stderr)
		}
		golden(t, name, r.stdout)
		return r
	}

	// Naming a plugin, one this computer has and one it does not.
	check("topic_update_knowledge_base_plugin_unchanged.json", cli.ExitOK, "topic", "update", "linear-algebra", "--knowledge-base", "kb", "--json")
	check("topic_update_knowledge_base_elsewhere.json", cli.ExitOK, "topic", "update", "linear-algebra", "--knowledge-base", "shelf", "--dry-run", "--json")
	check("topic_update_knowledge_base_elsewhere.txt", cli.ExitOK, "topic", "update", "linear-algebra", "--knowledge-base", "shelf")
	check("topic_update_knowledge_base_plugin.txt", cli.ExitOK, "topic", "update", "linear-algebra", "--knowledge-base", "kb")

	// Nothing to index yet.
	check("source_index_no_sources.txt", cli.ExitOK, "source", "index", "linear-algebra")
	check("source_list_plugin_no_sources.txt", cli.ExitOK, "source", "list", "linear-algebra")

	// A book, a scan without a text layer, a book that is no longer on this
	// computer, and a web page.
	strang := s.book(t, "strang.txt", bookOf(3, "elimination"))
	scan := s.book(t, "scan.txt", " \n")
	s.book(t, "lost.txt", bookOf(2, "eigenvalues"))
	if err := os.Remove(filepath.Join(s.home, "Books", "lost.txt")); err != nil {
		t.Fatal(err)
	}
	blog := s.source(t, "--url", page.URL+"/blog", "--title", "Go blog")

	check("source_list_not_indexed.json", cli.ExitOK, "source", "list", "linear-algebra", "--json")
	check("source_list_not_indexed.txt", cli.ExitOK, "source", "list", "linear-algebra")
	check("status_not_indexed.txt", cli.ExitOK, "status")
	check("status_not_indexed.json", cli.ExitOK, "status", "--json")

	// The dry run says what the run then does.
	before := len(s.calls(t))
	check("source_index_dry_run.json", cli.ExitOK, "source", "index", "linear-algebra", "--dry-run", "--json")
	dry := check("source_index_dry_run.txt", cli.ExitOK, "source", "index", "linear-algebra", "--dry-run")
	if got := indexedSources(s.calls(t)[before:]); len(got) != 0 {
		t.Errorf("the dry run had the plugin index %v", got)
	}
	if dry.stderr != "" {
		t.Errorf("the dry run wrote to stderr: %q", dry.stderr)
	}
	indexed := check("source_index.txt", cli.ExitOK, "source", "index", "linear-algebra")
	if got := indexedSources(s.calls(t)[before:]); !slices.Equal(got, []string{strang, scan, blog}) {
		t.Errorf("the plugin was asked to index %v, want the three Sources the dry run named", got)
	}
	// Progress goes to stderr: each Source as the plugin is asked for it.
	if want := "Indexing Strang (1 of 3)…\nIndexing Scan (2 of 3)…\nIndexing Go blog (3 of 3)…\n"; indexed.stderr != want {
		t.Errorf("the progress on stderr is %q, want %q", indexed.stderr, want)
	}
	// Again: the one that is indexed is left alone, and the others asked for.
	check("source_index_again.json", cli.ExitOK, "source", "index", "linear-algebra", "--json")
	check("source_list_indexed.txt", cli.ExitOK, "source", "list", "linear-algebra")
	check("status_indexed.txt", cli.ExitOK, "status")
	// Named Sources, and --again for what is indexed.
	check("source_index_named.json", cli.ExitOK, "source", "index", "linear-algebra", blog, strang, "--again", "--json")
	check("source_index_nothing_to_do.txt", cli.ExitOK, "source", "index", "linear-algebra", strang)

	// What cannot be indexed, and why.
	check("source_index_unknown_source.json", cli.ExitError, "source", "index", "linear-algebra", "other.abc123", "--json")
	check("source_index_bad_source.json", cli.ExitUsage, "source", "index", "linear-algebra", "../strang", "--json")
	check("source_index_no_topic.json", cli.ExitUsage, "source", "index", "--json")
	check("source_index_unknown_topic.json", cli.ExitError, "source", "index", "biology", "--json")
	s.ok(t, "topic", "create", "--title", "C")
	check("source_index_no_plugin.json", cli.ExitError, "source", "index", "c", "--json")
	s.ok(t, "topic", "update", "c", "--knowledge-base", "shelf")
	check("source_index_not_registered.json", cli.ExitError, "source", "index", "c", "--json")
	check("status_plugin_missing.txt", cli.ExitOK, "status")
	check("source_list_plugin_missing.txt", cli.ExitOK, "source", "list", "c")
	check("source_list_plugin_missing.json", cli.ExitOK, "source", "list", "c", "--json")
	s.nothingRunning(t)

	// A human failure goes to stderr, with what to do, and nothing to stdout.
	r := s.run(t, "source", "index", "c")
	if r.code != cli.ExitError || r.stdout != "" || !strings.Contains(r.stderr, "Topic c names the Knowledge base plugin shelf, which is not "+
		"registered on this computer: register it with study knowledge-base add shelf -- <command>") {
		t.Errorf("study source index of a Topic whose plugin is missing: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

// Stopped halfway, as Ctrl-C stops it, study source index reports that it
// was canceled, has told the plugin to stop, and leaves no plugin running.
// Run again, it carries on.
func TestSourceIndexStoppedAndCarriedOn(t *testing.T) {
	for _, mode := range [][]string{nil, {"--json"}} {
		t.Run(strings.Join(append([]string{"study source index"}, mode...), " "), func(t *testing.T) {
			s := newShelfComputer(t, "--page-delay", "25ms")
			first := s.book(t, "first.txt", bookOf(2, "one"))
			long := s.book(t, "long.txt", bookOf(400, "two"))
			third := s.book(t, "third.txt", bookOf(2, "three"))

			// The command is stopped once the plugin was asked for the long
			// Source.
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			var watching sync.WaitGroup
			watching.Add(1)
			go func() {
				defer watching.Done()
				for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
					if slices.Contains(indexedSources(plugintest.ReadCalls(t, s.record)), long) {
						time.Sleep(100 * time.Millisecond)
						break
					}
				}
				stop()
			}()
			began := time.Now()
			r := s.study(ctx, append([]string{"source", "index", "linear-algebra"}, mode...)...)
			watching.Wait()
			if took := time.Since(began); took > 8*time.Second {
				t.Errorf("the stopped command took %s: the long Source alone takes 10s to index", took)
			}
			said := r.stderr
			if mode != nil {
				said = r.stdout
				if !strings.Contains(said, `"code": "canceled"`) || !json.Valid([]byte(said)) {
					t.Errorf("the stopped command printed %s", said)
				}
			}
			if r.code != cli.ExitError || !strings.Contains(said, "study source index was stopped before it finished") ||
				!strings.Contains(said, "Knowledge base plugin kb was told to stop indexing Source long.<id>, and stopped") ||
				!strings.Contains(said, "study source index linear-algebra carries on from there") {
				t.Errorf("the stopped command: exit %d, and it said %q", r.code, said)
			}
			// It said at once that it was stopping, on stderr, with --json
			// too: until the plugin answers, nothing else is printed.
			if !strings.Contains(r.stderr, "Stopping: telling the plugin to stop indexing Long…") {
				t.Errorf("the stopped command does not say that it is stopping: %q", r.stderr)
			}
			calls := s.calls(t)
			if got := indexedSources(calls); !slices.Equal(got, []string{first, long}) {
				t.Errorf("the plugin was asked to index %v, want the first two Sources", got)
			}
			if last := calls[len(calls)-1]; last.Tool != "cancel" || last.Source() != long {
				t.Errorf("the last thing the plugin was asked is %s of %s, want to stop indexing the long Source", last.Tool, last.Source())
			}
			s.nothingRunning(t)

			// A plugin that takes no time over a page, and the same command.
			s.registerWith(t)
			before := len(s.calls(t))
			done := s.ok(t, "source", "index", "linear-algebra", "--json")
			if got := indexedSources(s.calls(t)[before:]); !slices.Equal(got, []string{long, third}) {
				t.Errorf("run again, the plugin was asked to index %v, want what was left", got)
			}
			if strings.Count(done.stdout, `"state": "indexed"`) != 3 {
				t.Errorf("run again, the command printed %s", done.stdout)
			}
			s.nothingRunning(t)
		})
	}
}

// With --json a quick run writes nothing to stderr: stdout holds the one
// JSON document, and stderr only what a long run has to say meanwhile.
func TestSourceIndexWithJSONIsQuietWhenQuick(t *testing.T) {
	s := newShelfComputer(t)
	s.book(t, "book.txt", bookOf(2, "one"))
	if r := s.ok(t, "source", "index", "linear-algebra", "--json"); r.stderr != "" || !json.Valid([]byte(r.stdout)) {
		t.Errorf("a quick run with --json: stdout %s, stderr %q", r.stdout, r.stderr)
	}
}

// What a plugin writes reaches the terminal cleaned: a failure's reason,
// in a Topic's Sources, in status and after indexing.
func TestIndexingKeepsAPluginOffTheTerminal(t *testing.T) {
	const escapes = "\x1b]0;gotcha\x07\x1b[2J"
	s := newShelfComputer(t)
	id := s.book(t, "book.txt", bookOf(2, "one"))
	// A service, which answers what the test has it answer: the registry
	// holds no command with such text among its arguments.
	answer, err := json.Marshal(map[string]any{"sources": []any{map[string]any{"id": id, "state": "failed",
		"reason": "no text layer" + escapes + string(rune(0x202e)) + "\nsecond line"}}})
	if err != nil {
		t.Fatal(err)
	}
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Results: map[string]string{"status": string(answer)}})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	s.ok(t, "knowledge-base", "add", "kb", "--replace", "--url", server.URL+"/mcp")
	for _, args := range [][]string{{"source", "list", "linear-algebra"}, {"status"}, {"source", "index", "linear-algebra", "--dry-run"}} {
		for _, mode := range [][]string{nil, {"--json"}} {
			r := s.ok(t, append(args, mode...)...)
			for _, out := range []string{r.stdout, r.stderr} {
				if strings.ContainsAny(out, "\x1b\x07") || strings.ContainsRune(out, 0x202e) {
					t.Errorf("study %v %v wrote the plugin's escapes: %q", args, mode, out)
				}
			}
			if !strings.Contains(r.stdout, "no text layer") {
				t.Errorf("study %v %v does not give the plugin's reason: %s", args, mode, r.stdout)
			}
			if mode != nil && !json.Valid([]byte(r.stdout)) {
				t.Errorf("study %v --json printed what is no JSON: %s", args, r.stdout)
			}
		}
	}
}

// The session-start hook of the Claude Code plugin gives the agent study
// status, so it asks a Topic's plugin what it has indexed as status does,
// and waits for it no longer: a session never starts late for a plugin.
func TestTheSessionStartHookSaysWhatIsIndexedAndDoesNotWait(t *testing.T) {
	s := newShelfComputer(t)
	s.book(t, "book.txt", bookOf(2, "one"))
	cwd, err := json.Marshal(map[string]string{"cwd": filepath.Join(s.env["STUDY_HOME"], "linear-algebra")})
	if err != nil {
		t.Fatal(err)
	}
	s.stdin = string(cwd)
	r := s.ok(t, "claude-hook", "session-start")
	for _, want := range []string{"Knowledge base plugins:", "linear-algebra: not indexed: Book", "Run: study source index linear-algebra"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the hook does not say %q:\n%s", want, r.stdout)
		}
	}
	s.nothingRunning(t)

	s.registerWith(t, "--hang", "start")
	s.plugins.GlanceTimeout = 500 * time.Millisecond
	began := time.Now()
	r = s.ok(t, "claude-hook", "session-start")
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("the hook took %s over a plugin that never answers, which has half a second", took)
	}
	for _, want := range []string{"Active topic:", "did not say what it has indexed within 500ms", "Run: study knowledge-base check kb"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("with a plugin that never answers, the hook does not say %q:\n%s", want, r.stdout)
		}
	}
	s.nothingRunning(t)
}

// A Source that failed keeps the plugin's reason in a listing, and gets the
// command that has it tried again only when trying again would hand it to
// the plugin: not once its file has changed, which the command skips.
func TestAFailedSourceIsOfferedARetryOnlyWhereItCanHelp(t *testing.T) {
	s := newShelfComputer(t)
	id := s.book(t, "book.txt", bookOf(2, "one"))
	answer, err := json.Marshal(map[string]any{"sources": []any{map[string]any{"id": id, "state": "failed",
		"reason": "the file has no text layer"}}})
	if err != nil {
		t.Fatal(err)
	}
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Results: map[string]string{"status": string(answer)}})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(func() {
		server.Close()
		service.Close()
	})
	s.ok(t, "knowledge-base", "add", "kb", "--replace", "--url", server.URL+"/mcp")
	const reason, retry = "indexing failed: the file has no text layer", "To have it tried again, run: study source index linear-algebra book.<id>"
	if out := s.ok(t, "source", "list", "linear-algebra").stdout; !strings.Contains(out, reason) || !strings.Contains(out, retry) {
		t.Errorf("a failed Source whose file is there:\n%s", out)
	}
	writeFile(t, filepath.Join(s.home, "Books", "book.txt"), "Another book altogether, of another length.\n")
	out := s.ok(t, "source", "list", "linear-algebra").stdout
	if !strings.Contains(out, "the file here is no longer the one added") || !strings.Contains(out, reason) {
		t.Errorf("a failed Source whose file has changed does not say both:\n%s", out)
	}
	if strings.Contains(out, "tried again") {
		t.Errorf("a failed Source whose file has changed is offered a command that would skip it:\n%s", out)
	}
	var list struct {
		Data core.SourceList `json:"data"`
	}
	if err := json.Unmarshal([]byte(s.ok(t, "source", "list", "linear-algebra", "--json").stdout), &list); err != nil {
		t.Fatal(err)
	}
	if got := list.Data.Sources[0].Indexed; got == nil || got.State != core.IndexFailed || got.Retry != "" || got.Reason == "" {
		t.Errorf("as JSON, the failed Source whose file has changed: %+v", got)
	}
}

// With a plugin that never answers, a Topic's Sources and status are there
// all the same, soon, and say which command shows what is wrong.
func TestTheCommandLineNeverWaitsForAPlugin(t *testing.T) {
	s := newShelfComputer(t, "--hang", "start")
	s.book(t, "book.txt", bookOf(2, "one"))
	// A command that only has to know which Topic it is about asks no plugin
	// anything, and starts none: the plugin has twenty seconds here.
	began := time.Now()
	s.ok(t, "syllabus")
	if _, err := os.Stat(s.record); err == nil || time.Since(began) > 10*time.Second {
		t.Errorf("study syllabus started the Topic's plugin, or waited for it: %s", time.Since(began))
	}
	// Nor do the other two that find the Topic so, whatever comes of them:
	// a review of a Topic without Cards, and a Check that is run outside
	// the Topic's folder.
	for _, args := range [][]string{{"review"}, {"check", "a-lesson"}} {
		began := time.Now()
		r := s.study(context.Background(), args...)
		if _, err := os.Stat(s.record); err == nil || time.Since(began) > 10*time.Second {
			t.Errorf("study %v started the Topic's plugin, or waited for it: %s\n%s%s", args, time.Since(began), r.stdout, r.stderr)
		}
	}
	// A plugin has half a second to say what it has indexed, and thirty to
	// start when it is asked to index.
	s.plugins.GlanceTimeout, s.plugins.StartTimeout = 500*time.Millisecond, 30*time.Second
	// In this order: an open Session shows in status.
	for _, tc := range []struct {
		file string
		args []string
	}{
		{"source_list_plugin_silent.txt", []string{"source", "list", "linear-algebra"}},
		{"status_plugin_silent.txt", []string{"status"}},
		{"", []string{"session", "open", "linear-algebra", "--energy", "full"}},
	} {
		s.nothingRunning(t)
		started, _ := os.ReadFile(s.record)
		began := time.Now()
		r := s.ok(t, tc.args...)
		if took := time.Since(began); took > 10*time.Second {
			t.Errorf("study %s took %s with a plugin that never answers", strings.Join(tc.args, " "), took)
		}
		if tc.file != "" {
			golden(t, tc.file, r.stdout)
			continue
		}
		// Opening a Session started no plugin: the record of the last one
		// that was started is as it was.
		if now, _ := os.ReadFile(s.record); !bytes.Equal(now, started) {
			t.Errorf("study %s started the Topic's plugin", strings.Join(tc.args, " "))
		}
	}
	s.nothingRunning(t)
}

// The shell completes --knowledge-base with none and the names this
// computer has.
func TestTheKnowledgeBaseFlagCompletesRegisteredNames(t *testing.T) {
	k := withPlugins(t)
	r := k.run(t, "__complete", "topic", "update", "c", "--knowledge-base", "")
	if r.code != cli.ExitOK || r.stdout != "none\nremote\nshelf\n:4\n" {
		t.Errorf("completing --knowledge-base: exit %d, stdout %q", r.code, r.stdout)
	}
	// And study source index takes no file.
	if r := k.run(t, "__complete", "source", "index", ""); r.code != cli.ExitOK || !strings.HasSuffix(r.stdout, ":4\n") {
		t.Errorf("completing study source index: exit %d, stdout %q", r.code, r.stdout)
	}
}

// A plugin whose program is inside the Study home, where the agent can
// write, is never started: not to say what it has indexed, and not to index.
// study knowledge-base add registers no such program, so the registry is
// written here as it would be were the Study home another when the plugin
// was registered.
func TestAPluginInsideTheStudyHomeIsNeverStarted(t *testing.T) {
	s := newShelfComputer(t)
	s.book(t, "book.txt", bookOf(2, "one"))
	program, err := os.ReadFile(plugintest.Command()[0])
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(s.env["STUDY_HOME"], "linear-algebra", "kb")
	if err := os.WriteFile(inside, program, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := json.Marshal(map[string]any{"format": 1, "plugins": []any{
		map[string]any{"name": "kb", "command": []string{inside, plugintest.Arg, "--record", s.record}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.registry(), registry, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"source", "list", "linear-algebra", "--json"}, {"status", "--json"}} {
		r := s.ok(t, args...)
		for _, want := range []string{`"state": "unknown"`, `"action": "study knowledge-base list"`,
			"Knowledge base plugin kb is registered on this computer, and study does not use it", "inside the Study home"} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("study %s does not say %q:\n%s", strings.Join(args, " "), want, r.stdout)
			}
		}
	}
	for _, args := range [][]string{{"source", "index", "linear-algebra", "--json"}, {"source", "index", "linear-algebra", "--dry-run", "--json"}} {
		r := s.run(t, args...)
		if r.code != cli.ExitError || !strings.Contains(r.stdout, `"code": "failed_precondition"`) ||
			!strings.Contains(r.stdout, "inside the Study home") {
			t.Errorf("study %s: exit %d, stdout %s, stderr %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
	}
	if _, err := os.Stat(s.record); err == nil {
		t.Error("a plugin whose program is inside the Study home was started")
	}
}
