//go:build unix

package pluginclient_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// TestMain runs this test binary as the test plugin when it is started as
// one: the plugin is the only program these tests start.
func TestMain(m *testing.M) {
	plugintest.Main()
	os.Exit(m.Run())
}

// quick are the deadlines of a test: short, so that a plugin that hangs
// costs a test a moment and not half a minute.
func quick(c *plugintest.Computer, more ...string) pluginclient.Options {
	return pluginclient.Options{Version: "test", Environ: c.Environ(more...),
		StartTimeout: 10 * time.Second, CallTimeout: 10 * time.Second, SearchTimeout: 10 * time.Second, StopGrace: 2 * time.Second}
}

// start starts the test plugin on a computer as the plugin kb, and closes
// it when the test ends.
func start(t *testing.T, c *plugintest.Computer, args ...string) *pluginclient.Connection {
	t.Helper()
	plugin := c.Register(t, "kb", plugintest.Command(args...)...)
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func codeOf(err error) string {
	var e *pluginclient.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// inode is what identifies an open file on its file system.
func inode(info fs.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Ino), true
}

// hashOf is a file's content hash as study computes it.
func hashOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// book writes a file Source of two pages into the computer's home folder.
func book(t *testing.T, c *plugintest.Computer) plugincontract.Source {
	t.Helper()
	path := filepath.Join(c.Home, "strang.txt")
	text := "Elimination produces an upper triangular system.\n\nPivots are the first nonzero entries.\f" +
		"A matrix times its inverse is the identity."
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return plugincontract.Source{ID: "strang-linear-algebra.k3f9a2", Title: "Linear Algebra", Kind: plugincontract.SourceFile,
		Path: path, Hash: hashOf(t, path)}
}

// indexed waits for a Source to be indexed and returns what the plugin says
// of it.
func indexed(t *testing.T, s *pluginclient.Connection, collection, source string) plugincontract.SourceStatus {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		sources, err := s.Status(context.Background(), collection)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		i := slices.IndexFunc(sources, func(st plugincontract.SourceStatus) bool { return st.ID == source })
		if i >= 0 && sources[i].State != plugincontract.StateIndexing {
			return sources[i]
		}
		if time.Now().After(deadline) {
			t.Fatalf("Source %s was not indexed in time: %+v", source, sources)
		}
	}
}

// A Connection speaks all of the contract: what #81 and #82 will ask of it.
func TestASessionSpeaksTheContract(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	s := start(t, c, "--name", "shelf-for-tests", "--version", "0.1.0")

	want := plugincontract.Contract{Version: 1, Sources: []string{"file", "url"}, Search: []string{"keyword"}, Locations: []string{"pages"}}
	if got := s.Contract(); got.Version != want.Version || !slices.Equal(got.Sources, want.Sources) ||
		!slices.Equal(got.Search, want.Search) || !slices.Equal(got.Locations, want.Locations) {
		t.Errorf("Contract = %+v, want %+v", got, want)
	}
	if got := s.Server(); got != (pluginclient.Server{Name: "shelf-for-tests", Version: "0.1.0"}) {
		t.Errorf("Server = %+v", got)
	}
	if s.Name() != "kb" || s.DataFolder() != filepath.Join(c.StudyHome, ".lamplight", "knowledge-base-plugins", "kb") {
		t.Errorf("Name = %q, DataFolder = %q", s.Name(), s.DataFolder())
	}

	source := book(t, c)
	first, err := s.Index(ctx, "linear-algebra", source)
	if err != nil || first.ID != source.ID || first.State != plugincontract.StateIndexing {
		t.Fatalf("Index = %+v, %v; want it accepted, and under way", first, err)
	}
	if done := indexed(t, s, "linear-algebra", source.ID); done.State != plugincontract.StateIndexed || done.Hash != source.Hash {
		t.Fatalf("Status = %+v; want it indexed with the hash of the file", done)
	}
	// Indexing what is indexed does nothing, and says so at once.
	again, err := s.Index(ctx, "linear-algebra", source)
	if err != nil || again.State != plugincontract.StateIndexed || again.Hash != source.Hash {
		t.Errorf("Index again = %+v, %v; want it indexed already", again, err)
	}

	found, err := s.Search(ctx, "linear-algebra", "inverse matrix identity", 5, nil)
	if err != nil || len(found) != 1 {
		t.Fatalf("Search = %+v, %v; want the one Passage about the inverse", found, err)
	}
	passage := found[0]
	if passage.Source != source.ID || passage.Text != "A matrix times its inverse is the identity." || passage.Score <= 0 ||
		passage.Location == nil || *passage.Location != (plugincontract.Location{Kind: "pages", First: 2, Last: 2}) {
		t.Errorf("the Passage found = %+v (location %+v)", passage, passage.Location)
	}
	same, err := s.Passage(ctx, "linear-algebra", passage.ID)
	if err != nil || same.ID != passage.ID || same.Text != passage.Text || same.Source != passage.Source ||
		same.Location == nil || *same.Location != *passage.Location {
		t.Errorf("Passage = %+v, %v; want the Passage the search found", same, err)
	}

	// Limited to Sources, a search finds only theirs; limited to none, it
	// finds nothing and asks nothing.
	if found, err := s.Search(ctx, "linear-algebra", "inverse", 5, []string{"another-book.a1b2c3"}); err != nil || len(found) != 0 {
		t.Errorf("Search in another Source = %+v, %v", found, err)
	}
	if found, err := s.Search(ctx, "linear-algebra", "inverse", 5, []string{}); err != nil || found == nil || len(found) != 0 {
		t.Errorf("Search in no Source = %+v, %v", found, err)
	}
	if found, err := s.Search(ctx, "linear-algebra", "inverse", 5, []string{source.ID}); err != nil || len(found) != 1 {
		t.Errorf("Search in the Source = %+v, %v", found, err)
	}

	if stopped, err := s.Cancel(ctx, "linear-algebra", source.ID); err != nil || stopped {
		t.Errorf("Cancel of a Source that is not being indexed = %v, %v", stopped, err)
	}
	// Another collection holds nothing of this one.
	if sources, err := s.Status(ctx, "go-concurrency"); err != nil || len(sources) != 0 {
		t.Errorf("Status of another collection = %+v, %v", sources, err)
	}
	if found, err := s.Search(ctx, "go-concurrency", "inverse", 5, nil); err != nil || len(found) != 0 {
		t.Errorf("Search in another collection = %+v, %v", found, err)
	}
}

// A command is started as ADR-0012 says: in the registry's folder and never
// in the Topic study was started in, with pipes of its own, in a session of
// its own, with the learner's environment less what would mislead it, and
// with a folder of its own. It is gone when its Connection is closed.
func TestACommandIsStartedApartFromStudy(t *testing.T) {
	c := plugintest.NewComputer(t)
	topic := filepath.Join(c.StudyHome, "linear-algebra")
	if err := os.MkdirAll(topic, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record)...)
	// The learner's shell has the Topic's virtual environment switched on,
	// and a link in their home folder that leads into the Topic.
	link := filepath.Join(c.Home, "current-topic")
	if err := os.Symlink(topic, link); err != nil {
		t.Fatal(err)
	}
	const colours = "rs=0:di=01;34:ln=01;36"
	opts := quick(c, "GIT_DIR="+topic+"/.git", "GIT_INDEX_FILE=/somewhere/index", "GIT_CONFIG_GLOBAL=/somewhere/config",
		"PWD="+topic, "OLDPWD="+c.StudyHome, plugincontract.DataFolderEnv+"=/somewhere/else", "EMBEDDINGS_API_KEY=kept",
		"PATH="+topic+"/.venv/bin:/usr/bin:"+link+"/bin:"+c.Home+"/bin::.", "VIRTUAL_ENV="+topic+"/.venv",
		"PYTHONPATH="+topic+"/src:"+link+"/lib", "NODE_PATH="+c.Home+"/node:"+topic+"/node_modules",
		"NODE_OPTIONS=--max-old-space-size=4096 --require "+topic+"/hook.js", "JAVA_OPTS=-Xmx1g -Dconf="+link+"/kb.conf",
		"LESS=-R --tabs=4", "LS_COLORS="+colours, "EMPTY=", "NOTES="+c.Home+"/notes")
	// study has a file of the learner's open, and was started in the Topic.
	held, err := os.Create(filepath.Join(c.Home, "held-open"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	s, err := pluginclient.Start(context.Background(), c.Env(topic), plugin, opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	got := plugintest.ReadRecord(t, record)
	defer plugintest.Kill(got.PID)

	if got.Dir != c.ConfigDir {
		t.Errorf("the plugin was started in %s, want the registry's folder, %s", got.Dir, c.ConfigDir)
	}
	if strings.HasPrefix(got.Dir, c.StudyHome) {
		t.Errorf("the plugin was started inside the Study home: %s", got.Dir)
	}
	for name, value := range got.Env {
		if strings.HasPrefix(name, "GIT_") || name == "OLDPWD" {
			t.Errorf("the plugin was given %s=%s", name, value)
		}
	}
	folder := filepath.Join(c.StudyHome, ".lamplight", "knowledge-base-plugins", "kb")
	for name, want := range map[string]string{"PWD": c.ConfigDir, plugincontract.DataFolderEnv: folder, "EMBEDDINGS_API_KEY": "kept",
		"HOME": c.Home, "LS_COLORS": colours, "NOTES": c.Home + "/notes", "LESS": "-R --tabs=4",
		// What leads into the Study home is taken out of a list of paths,
		// by a link too, and the rest of the list is left as it was.
		"PATH": "/usr/bin:" + c.Home + "/bin::.", "NODE_PATH": c.Home + "/node"} {
		if got.Env[name] != want {
			t.Errorf("the plugin's %s is %q, want %q", name, got.Env[name], want)
		}
	}
	if value, has := got.Env["EMPTY"]; !has || value != "" {
		t.Errorf("the plugin's EMPTY is %q (there: %v), want it empty", value, has)
	}
	// And a variable that names nothing else is taken out whole: the plugin
	// is told of nothing in the Study home but its own folder.
	// So is one that names such a path among other words.
	for _, name := range []string{"VIRTUAL_ENV", "PYTHONPATH", "STUDY_HOME", "NODE_OPTIONS", "JAVA_OPTS"} {
		if value, has := got.Env[name]; has {
			t.Errorf("the plugin was given %s=%s, which names something inside the Study home", name, value)
		}
	}
	for name, value := range got.Env {
		if name != plugincontract.DataFolderEnv && strings.Contains(value, c.StudyHome) {
			t.Errorf("the plugin's %s names the Study home: %s", name, value)
		}
	}
	if info, err := os.Lstat(folder); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the plugin's folder, %s: %v, %v; want a folder for the learner alone", folder, info, err)
	}
	// It is told the name it is registered by, whatever it was started through.
	if got.Args[0] != plugin.Command[0] {
		t.Errorf("the plugin's own name is %q, want %q", got.Args[0], plugin.Command[0])
	}

	// Its standard input is a pipe of its own, not the one study has.
	mine, err := os.Stdin.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Stdin.Pipe {
		t.Errorf("the plugin's standard input is no pipe: %+v", got.Stdin)
	}
	if theirs, ok := inode(mine); ok && theirs == got.Stdin.Inode && mine.Mode()&os.ModeNamedPipe != 0 {
		t.Errorf("the plugin has study's own standard input")
	}
	if !got.Leader {
		t.Error("the plugin does not lead a session of its own")
	}
	if got.Terminal {
		t.Error("the plugin can open study's terminal")
	}
	// It inherits nothing study had open: not the file this test holds, and
	// not the registry's folder, which study opened to start it there.
	for fd, target := range got.Files {
		if strings.HasPrefix(target, c.Home) || strings.HasPrefix(target, c.StudyHome) {
			t.Errorf("the plugin inherited descriptor %d, %s", fd, target)
		}
	}
	if runtime.GOOS == "linux" && got.Files[3] != plugin.Command[0] {
		t.Errorf("the plugin does not run from the file study opened: descriptor 3 is %q, want %s", got.Files[3], plugin.Command[0])
	}

	s.Close()
	if !plugintest.Gone(got.PID, 5*time.Second) {
		t.Errorf("the plugin, process %d, is still there after its connection was closed", got.PID)
	}
	if _, err := s.Status(context.Background(), "linear-algebra"); codeOf(err) != pluginclient.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "can no longer be asked") {
		t.Errorf("Status after Close = %v", err)
	}
	// Closing again is nothing.
	if err := s.Close(); err != nil {
		t.Errorf("Close again = %v", err)
	}
}

// A Connection is asked from several goroutines at once, as an MCP server
// answering several tool calls asks it, and can be closed under them: every
// call returns, with an answer or with an error.
func TestASessionIsAskedFromManyAtOnce(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	s := start(t, c)
	source := book(t, c)
	if _, err := s.Index(ctx, "c", source); err != nil {
		t.Fatal(err)
	}
	indexed(t, s, "c", source.ID)
	done := make(chan error, 16)
	for i := range 16 {
		go func() {
			var err error
			for range 20 {
				if i%2 == 0 {
					_, err = s.Search(ctx, "c", "inverse matrix identity", 5, nil)
				} else {
					_, err = s.Status(ctx, "c")
				}
				if err != nil {
					break
				}
			}
			done <- err
		}()
	}
	time.Sleep(20 * time.Millisecond)
	s.Close()
	for range 16 {
		select {
		case err := <-done:
			// Closed under a call, the call says so, and not that the
			// plugin failed.
			if err != nil && (codeOf(err) != pluginclient.CodeFailedPrecondition ||
				!strings.Contains(err.Error(), "Knowledge base plugin kb can no longer be asked: its connection was closed")) {
				t.Errorf("a call on a Connection closed under it = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a call on a Connection closed under it never returned")
		}
	}
	if !s.Ended() {
		t.Error("a closed Connection has not ended")
	}
}

// The registry stands for what was checked: a plugin whose program is in
// the Study home, where the agent writes, is not started.
func TestAProgramInTheStudyHomeIsNotStarted(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	inside := filepath.Join(c.StudyHome, "linear-algebra", "kb")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(plugintest.Command()[0], inside); err != nil {
		t.Fatal(err)
	}
	plugin := pluginregistry.Plugin{Name: "kb", Command: []string{inside, plugintest.Arg, "--record", record}}
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin, quick(c))
	if err == nil {
		s.Close()
	}
	if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot be started") ||
		!strings.Contains(err.Error(), "inside the Study home") {
		t.Errorf("Start = %v; want it refused", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the program in the Study home was started")
	}
}
