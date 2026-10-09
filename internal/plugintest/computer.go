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

// Getenv reads the computer's environment: HOME and STUDY_HOME.
func (c *Computer) Getenv(key string) string {
	return map[string]string{"HOME": c.Home, "STUDY_HOME": c.StudyHome}[key]
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
