package pluginregistry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regtest"
)

// OpenFolder is what a plugin is started from: the registry's folder, open,
// found and checked as the registry itself is.
func TestOpenFolderOpensTheRegistrysFolder(t *testing.T) {
	c := regtest.New(t)
	c.WriteRegistry(t, `{"format": 1, "plugins": []}`)
	before := regtest.Snapshot(t, c.Base)
	dir, path, err := pluginregistry.OpenFolder(envOf(c))
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil {
		t.Fatal(err)
	}
	named, err := os.Stat(c.ConfigDir())
	if err != nil || !os.SameFile(opened, named) || path != c.ConfigDir() {
		t.Errorf("OpenFolder opened %s (%v); want %s", path, err, c.ConfigDir())
	}
	// It is opened to stand in, and nothing more: nothing is made in it.
	if regtest.Snapshot(t, c.Base) != before {
		t.Error("opening the folder wrote something")
	}

	// Reached through a link outside the Study home, it is the folder the
	// link leads to, and the path says so.
	real := regtest.Mkdir(t, filepath.Join(c.Base, "settings", "lamplight"))
	linked := c.With(map[string]string{"XDG_CONFIG_HOME": regtest.Symlink(t, filepath.Dir(real), filepath.Join(c.Base, "config"))})
	dir2, path, err := pluginregistry.OpenFolder(envOf(linked))
	if err != nil {
		t.Fatalf("OpenFolder through a link: %v", err)
	}
	defer dir2.Close()
	if path != real {
		t.Errorf("OpenFolder through a link gives the path %s; want the folder it leads to, %s", path, real)
	}
}

// A Guard asks of whatever else a plugin is started with what the registry
// asks of its program: is it the agent's to change?
func TestGuardTellsWhatTheAgentCouldChange(t *testing.T) {
	c := regtest.New(t)
	python := regtest.Program(t, filepath.Join(c.Bin, "python3"))
	inside := regtest.Program(t, filepath.Join(c.Study, "topic", ".venv", "bin", "python3"))
	link := regtest.Symlink(t, filepath.Join(c.Study, "topic"), filepath.Join(c.Home, "current-topic"))
	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()
	for path, want := range map[string]bool{
		c.Study:                              true,
		filepath.Join(c.Study, "topic"):      true,
		inside:                               true,
		filepath.Join(c.Study, "not", "yet"): true,
		link:                                 true,
		filepath.Join(link, ".venv", "bin"):  true,
		c.Home:                               false,
		c.Bin:                                false,
		python:                               false,
		"/":                                  false,
		filepath.Join(c.Base, "nowhere"):     false,
	} {
		if got := guard.Inside(path); got != want {
			t.Errorf("Inside(%s) = %v, want %v", path, got, want)
		}
	}
	if err := guard.CheckProgram(python); err != nil {
		t.Errorf("CheckProgram of a program outside the Study home: %v", err)
	}
	for path, want := range map[string]string{
		inside: "is inside the Study home",
		filepath.Join(link, ".venv", "bin", "python3"): "is inside the Study home",
		filepath.Join(c.Bin, "gone"):                   "there is no program at",
		c.Bin:                                          "is a folder, not a program",
		// A file where a folder should be.
		regtest.Symlink(t, python, filepath.Join(c.Bin, "linked")) + "/x": "study cannot follow",
	} {
		err := guard.CheckProgram(path)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckProgram(%s) = %v; want an error with %q", path, err, want)
		}
	}
}

// A folder the agent could write, or lead elsewhere, is no place to start
// a plugin from, and neither is one that is not there.
func TestOpenFolderRefusesWhatTheRegistryRefuses(t *testing.T) {
	c := regtest.New(t)
	if dir, _, err := pluginregistry.OpenFolder(envOf(c)); err == nil || pluginregistry.CodeOf(err) != "failed_precondition" ||
		!strings.Contains(err.Error(), "is not there") {
		if dir != nil {
			dir.Close()
		}
		t.Errorf("OpenFolder without a configuration folder = %v; want failed_precondition", err)
	}
	if regtest.Exists(c.ConfigDir()) {
		t.Error("OpenFolder made the configuration folder")
	}

	inside := c.With(map[string]string{"XDG_CONFIG_HOME": regtest.Mkdir(t, filepath.Join(c.Study, ".config"))})
	regtest.Mkdir(t, filepath.Join(c.Study, ".config", "lamplight"))
	if dir, _, err := pluginregistry.OpenFolder(envOf(inside)); err == nil || pluginregistry.CodeOf(err) != "failed_precondition" ||
		!strings.Contains(err.Error(), "is inside the Study home") {
		if dir != nil {
			dir.Close()
		}
		t.Errorf("OpenFolder of a folder inside the Study home = %v; want failed_precondition", err)
	}

	// Reached through a link in the Study home, wherever it leads.
	outside := regtest.Mkdir(t, filepath.Join(c.Base, "outside", "lamplight"))
	through := c.With(map[string]string{"XDG_CONFIG_HOME": regtest.Symlink(t, filepath.Dir(outside), filepath.Join(c.Study, "topic", "config"))})
	if dir, _, err := pluginregistry.OpenFolder(envOf(through)); err == nil || !strings.Contains(err.Error(), "is inside the Study home") {
		if dir != nil {
			dir.Close()
		}
		t.Errorf("OpenFolder of a folder reached through the Study home = %v; want it refused", err)
	}
}
