package plugintest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// Computer is a learner's computer made of temporary folders: a home
// folder, which holds Lamplight's configuration folder with the Knowledge
// base plugin registry in it, and a Study home apart from it, as the
// registry requires. Nothing of the real computer is in it.
type Computer struct {
	// Home is the home folder, StudyHome the Study home and ConfigDir
	// Lamplight's configuration folder in the home folder. All are paths
	// with every symbolic link followed.
	Home, StudyHome, ConfigDir string

	plugins []pluginregistry.Plugin
}

// NewComputer returns a computer with an empty home folder, an empty Study
// home, and an empty configuration folder.
func NewComputer(t testing.TB) *Computer {
	t.Helper()
	base := t.TempDir()
	// The temporary folder is itself reached through a link on some systems.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	c := &Computer{Home: filepath.Join(base, "home"), StudyHome: filepath.Join(base, "study")}
	c.ConfigDir = filepath.Join(c.Home, ".config", "lamplight")
	for _, dir := range []string{c.ConfigDir, c.StudyHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// Base is the folder that holds the home folder and the Study home, and
// nothing else at first.
func (c *Computer) Base() string { return filepath.Dir(c.Home) }

// PassThrough takes from each folder the permission to list it, and leaves
// the ones to pass through it and to write in it. That is what a folder of
// someone else's with mode 711 is to the learner, as /home is on many
// shared computers: their own folders have to change mode for a test to
// have one. Name a folder before the ones inside it. The test is skipped
// for root, who may list every folder, and the folders get their modes back
// when it ends, so that it can remove them.
func PassThrough(t testing.TB, dirs ...string) {
	t.Helper()
	restrict(t, 0o311, dirs)
	for _, dir := range dirs {
		if _, err := os.ReadDir(dir); err == nil {
			t.Fatalf("%s can still be listed, so this test proves nothing", dir)
		}
	}
}

// Shut takes every permission from each folder: the learner may not even
// pass through it, so nobody can tell where a path through it leads. The
// test is skipped for root, and the folders get their modes back when it
// ends.
func Shut(t testing.TB, dirs ...string) {
	t.Helper()
	restrict(t, 0, dirs)
}

func restrict(t testing.TB, mode os.FileMode, dirs []string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root may list every folder and pass through it, whatever its mode")
	}
	// A folder is reached through the ones before it, to restrict it and to
	// give its mode back: so the inner ones first, then back the other way.
	before := make([]os.FileInfo, len(dirs))
	t.Cleanup(func() {
		for i, dir := range dirs {
			if before[i] != nil {
				_ = os.Chmod(dir, before[i].Mode().Perm())
			}
		}
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		info, err := os.Stat(dirs[i])
		if err != nil {
			t.Fatal(err)
		}
		before[i] = info
		if err := os.Chmod(dirs[i], mode); err != nil {
			t.Fatal(err)
		}
	}
}

// Getenv reads the computer's environment: HOME and STUDY_HOME.
func (c *Computer) Getenv(key string) string {
	return map[string]string{"HOME": c.Home, "STUDY_HOME": c.StudyHome}[key]
}

// DataFolder is the folder study gives the plugin registered as name to
// keep its index in: in Lamplight's state folder, in the home folder.
func (c *Computer) DataFolder(name string) string {
	return filepath.Join(c.Home, ".local", "state", "lamplight", "knowledge-base-plugins", name)
}

// Env is what the registry needs to know of a study started on the computer
// in the folder dir, or in the home folder when dir is empty.
func (c *Computer) Env(dir string) pluginregistry.Env {
	if dir == "" {
		dir = c.Home
	}
	return pluginregistry.Env{StudyHome: c.StudyHome, Dir: dir, Getenv: c.Getenv}
}

// Environ is the environment a study started on the computer has, with the
// variables given after it, each as NAME=value.
func (c *Computer) Environ(more ...string) []string {
	return append([]string{"HOME=" + c.Home, "STUDY_HOME=" + c.StudyHome, "PATH=" + os.Getenv("PATH")}, more...)
}

// Register writes the plugin into the computer's registry, as study
// knowledge-base add would, and returns it. Give it the command that starts
// the plugin; RegisterURL registers a URL.
func (c *Computer) Register(t testing.TB, name string, command ...string) pluginregistry.Plugin {
	t.Helper()
	return c.register(t, pluginregistry.Plugin{Name: name, Command: command})
}

// RegisterURL writes a plugin reached by URL into the computer's registry.
func (c *Computer) RegisterURL(t testing.TB, name, url string) pluginregistry.Plugin {
	t.Helper()
	return c.register(t, pluginregistry.Plugin{Name: name, URL: url})
}

func (c *Computer) register(t testing.TB, plugin pluginregistry.Plugin) pluginregistry.Plugin {
	t.Helper()
	for i, p := range c.plugins {
		if p.Name == plugin.Name {
			c.plugins = append(c.plugins[:i], c.plugins[i+1:]...)
			break
		}
	}
	c.plugins = append(c.plugins, plugin)
	sort.Slice(c.plugins, func(i, j int) bool { return c.plugins[i].Name < c.plugins[j].Name })
	data, err := json.MarshalIndent(struct {
		Format  int                     `json:"format"`
		Plugins []pluginregistry.Plugin `json:"plugins"`
	}{1, c.plugins}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.ConfigDir, "knowledge-base-plugins.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return plugin
}
