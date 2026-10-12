package core_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// pluginCore returns a Core for a learner's computer made of temporary
// folders, started in dir (the home folder when empty), whose plugins get
// the computer's environment and short deadlines.
func pluginCore(t *testing.T, c *plugintest.Computer, dir string, start time.Duration) *core.Core {
	t.Helper()
	if dir == "" {
		dir = c.Home
	}
	k, err := core.Open(core.Options{
		Getenv: c.Getenv,
		Dir:    dir,
		Now:    func() time.Time { return fixedNow },
		Plugins: core.PluginOptions{
			Environ:      func() []string { return c.Environ("GIT_DIR=/somewhere/.git") },
			StartTimeout: start,
			StopGrace:    300 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// CheckPlugin starts a registered command where the registry is and never
// where study was started, asks it which contract it speaks, and stops it.
// It writes nothing in the Study home: the plugin's own folder is in the
// learner's state folder.
func TestCheckPluginStartsAsksAndStops(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	// study is started inside a Topic of the Study home.
	topic := filepath.Join(c.StudyHome, "linear-algebra")
	if err := os.MkdirAll(topic, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--name", "a-plugin", "--version", "2.0.0", "--sources", "file")...)
	got, err := pluginCore(t, c, topic, 20*time.Second).CheckPlugin(ctx, "kb")
	want := core.PluginCheck{
		Name:       "kb",
		Command:    plugin.Command,
		Contract:   core.PluginContract{Version: 1, Sources: []string{"file"}, Search: []string{"keyword"}, Locations: []string{"pages"}},
		Server:     core.PluginServer{Name: "a-plugin", Version: "2.0.0"},
		DataFolder: c.DataFolder("kb"),
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CheckPlugin = %+v, %v; want %+v", got, err, want)
	}
	started := plugintest.ReadRecord(t, record)
	if !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
	if started.Dir != c.ConfigDir {
		t.Errorf("the plugin was started in %s, want the registry's folder, %s", started.Dir, c.ConfigDir)
	}
	// Of the environment study has, a plugin gets a few variables.
	for _, name := range []string{"GIT_DIR", "STUDY_HOME", "PWD"} {
		if value, has := started.Env[name]; has {
			t.Errorf("the plugin was given %s=%s", name, value)
		}
	}
	if started.Env["LAMPLIGHT_PLUGIN_DATA"] != want.DataFolder || started.Env["HOME"] != c.Home || started.Env["PATH"] == "" {
		t.Errorf("the plugin's environment: %v", started.Env)
	}
	// Nothing was made in the Study home: the plugin's folder is the
	// learner's state.
	if left := tree(t, c.StudyHome); !reflect.DeepEqual(left, []string{"linear-algebra/"}) {
		t.Errorf("the Study home holds %v", left)
	}
	if info, err := os.Stat(want.DataFolder); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("the plugin's folder, %s: %v, %v; want a folder for the learner alone", want.DataFolder, info, err)
	}
}

// tree lists what a folder holds, folders with a slash after them.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCheckPluginReachesAURL(t *testing.T) {
	c := plugintest.NewComputer(t)
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Name: "a-service"})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	defer service.Close()
	c.RegisterURL(t, "remote", server.URL+"/t0ken-in-the-path/mcp?key=s3cr3t")
	got, err := pluginCore(t, c, "", 20*time.Second).CheckPlugin(context.Background(), "remote")
	// It is reported by its scheme and host: the rest of an address may be
	// a key.
	if err != nil || got.Name != "remote" || got.Address != server.URL || got.Command != nil || got.Contract.Version != 1 ||
		got.Server.Name != "a-service" || got.DataFolder != "" {
		t.Errorf("CheckPlugin = %+v, %v", got, err)
	}
	// A service keeps its own index: study makes it no folder, anywhere.
	if left := tree(t, c.StudyHome); len(left) != 0 {
		t.Errorf("the Study home holds %v", left)
	}
	if left := tree(t, c.Home); !reflect.DeepEqual(left, []string{".config/", ".config/lamplight/", ".config/lamplight/knowledge-base-plugins.json"}) {
		t.Errorf("the home folder holds %v", left)
	}
}

// What cannot be checked carries a documented code and says what to do,
// and nothing is left running or written.
func TestCheckPluginRefusals(t *testing.T) {
	ctx := context.Background()
	stopped, stop := context.WithCancel(ctx)
	stop()
	for name, tc := range map[string]struct {
		setup func(t *testing.T, c *plugintest.Computer)
		ctx   context.Context
		ask   string
		start time.Duration
		code  core.ErrorCode
		want  string
	}{
		"a name that is not registered": {func(t *testing.T, c *plugintest.Computer) { c.Register(t, "kb", plugintest.Command()...) },
			ctx, "shelf", time.Minute, core.CodeNotFound, "no Knowledge base plugin named shelf is registered on this computer"},
		"no registry at all": {func(*testing.T, *plugintest.Computer) {},
			ctx, "shelf", time.Minute, core.CodeNotFound, "no Knowledge base plugin named shelf is registered"},
		"a name that is none": {func(*testing.T, *plugintest.Computer) {},
			ctx, "../shelf\x1b[2J", time.Minute, core.CodeInvalidArgument, `"../shelf\x1b[2J" is not the name of a Knowledge base plugin`},
		"a plugin that hangs": {func(t *testing.T, c *plugintest.Computer) {
			c.Register(t, "kb", plugintest.Command("--hang", "start")...)
		},
			ctx, "kb", 300 * time.Millisecond, core.CodeFailedPrecondition, "did not open an MCP session within 300ms, so study stopped it"},
		"a plugin that speaks a newer contract": {func(t *testing.T, c *plugintest.Computer) {
			c.Register(t, "kb", plugintest.Command("--contract", `{"version": 2}`)...)
		}, ctx, "kb", time.Minute, core.CodeNewerFormat, "speaks version 2 of Lamplight's contract, and this version of study knows version 1: upgrade study"},
		"a command that was stopped": {func(t *testing.T, c *plugintest.Computer) { c.Register(t, "kb", plugintest.Command()...) },
			stopped, "kb", time.Minute, core.CodeCanceled, ""},
		"a registry that is damaged": {func(t *testing.T, c *plugintest.Computer) {
			if err := os.WriteFile(filepath.Join(c.ConfigDir, "knowledge-base-plugins.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ctx, "kb", time.Minute, core.CodeCorrupt, "knowledge-base-plugins.json"},
	} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			tc.setup(t, c)
			began := time.Now()
			got, err := pluginCore(t, c, "", tc.start).CheckPlugin(tc.ctx, tc.ask)
			if core.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(got, core.PluginCheck{}) {
				t.Errorf("CheckPlugin = %+v, %v (%s); want %s with %q", got, err, core.CodeOf(err), tc.code, tc.want)
			}
			if took := time.Since(began); took > 10*time.Second {
				t.Errorf("CheckPlugin took %s", took)
			}
		})
	}
}

// A registry the agent could write is not read, so nothing it names is
// started.
func TestCheckPluginStartsNothingFromARegistryInTheStudyHome(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	// The Study home is the home folder: the registry is inside it.
	k, err := core.Open(core.Options{Getenv: func(key string) string {
		return map[string]string{"HOME": c.Home, "STUDY_HOME": c.Home}[key]
	}, Dir: c.Home})
	if err != nil {
		t.Fatal(err)
	}
	_, err = k.CheckPlugin(context.Background(), "kb")
	if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), "is inside the Study home") {
		t.Errorf("CheckPlugin = %v; want failed_precondition", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("a plugin from a registry inside the Study home was started")
	}
}
