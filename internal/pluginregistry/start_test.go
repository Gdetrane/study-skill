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
// asks of its program: is it the agent's to change? Only what is known to
// stay outside the Study home is outside.
func TestGuardTellsWhatTheAgentCouldChange(t *testing.T) {
	c := regtest.New(t)
	python := regtest.Program(t, filepath.Join(c.Bin, "python3"))
	inside := regtest.Program(t, filepath.Join(c.Study, "topic", ".venv", "bin", "python3"))
	link := regtest.Symlink(t, filepath.Join(c.Study, "topic"), filepath.Join(c.Home, "current-topic"))
	// A folder nobody may look into: where a path through it leads cannot
	// be told.
	closed := regtest.Mkdir(t, filepath.Join(c.Home, "closed"))
	regtest.Mkdir(t, filepath.Join(closed, "bin"))
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
	_, err := os.ReadDir(closed)
	unreadable := err != nil // root reads every folder

	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()
	for path, want := range map[string]bool{
		c.Study:                              false,
		filepath.Join(c.Study, "topic"):      false,
		inside:                               false,
		filepath.Join(c.Study, "not", "yet"): false,
		link:                                 false,
		filepath.Join(link, ".venv", "bin"):  false,
		filepath.Join(closed, "bin"):         !unreadable,
		// A file where a folder should be.
		filepath.Join(python, "x"):       false,
		c.Home:                           true,
		c.Bin:                            true,
		python:                           true,
		"/":                              true,
		filepath.Join(c.Base, "nowhere"): true,
	} {
		if got := guard.Outside(path); got != want {
			t.Errorf("Outside(%s) = %v, want %v", path, got, want)
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

// MakeFolder makes a folder for the learner alone, through the folders the
// walk opened, and gives the path the links lead to.
func TestMakeFolderMakesAFolderOutsideTheStudyHome(t *testing.T) {
	c := regtest.New(t)
	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()

	want := filepath.Join(c.Home, ".local", "state", "lamplight", "knowledge-base-plugins", "notes")
	folder, path, err := guard.MakeFolder(want)
	if err != nil {
		t.Fatalf("MakeFolder: %v", err)
	}
	defer folder.Close()
	if path != want {
		t.Errorf("MakeFolder gives the path %s; want %s", path, want)
	}
	opened, err := folder.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if named, err := os.Stat(want); err != nil || !os.SameFile(opened, named) {
		t.Errorf("the folder returned is not the one at %s (%v)", want, err)
	}
	// Every folder it made is the learner's alone.
	for dir := want; dir != c.Home; dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s was made with the mode %v (%v); want 0700", dir, info.Mode().Perm(), err)
		}
	}
	// Made already, it is found, and nothing changes.
	before := regtest.Snapshot(t, c.Base)
	again, path, err := guard.MakeFolder(want)
	if err != nil || path != want {
		t.Fatalf("MakeFolder again = %s, %v", path, err)
	}
	again.Close()
	if regtest.Snapshot(t, c.Base) != before {
		t.Error("finding a folder that is there wrote something")
	}

	// Through a link outside the Study home, the path is where it leads.
	real := regtest.Mkdir(t, filepath.Join(c.Base, "disk", "state"))
	link := regtest.Symlink(t, real, filepath.Join(c.Home, "state"))
	linked, path, err := guard.MakeFolder(filepath.Join(link, "lamplight", "notes"))
	if err != nil {
		t.Fatalf("MakeFolder through a link: %v", err)
	}
	linked.Close()
	if path != filepath.Join(real, "lamplight", "notes") || !regtest.Exists(path) {
		t.Errorf("MakeFolder through a link gives %s; want the folder the link leads to", path)
	}
}

// Nothing is made where the agent writes, or where a link there leads.
func TestMakeFolderRefusesTheStudyHome(t *testing.T) {
	c := regtest.New(t)
	outside := regtest.Mkdir(t, filepath.Join(c.Base, "outside"))
	regtest.Mkdir(t, filepath.Join(c.Study, "topic"))
	through := regtest.Symlink(t, outside, filepath.Join(c.Study, "topic", "state"))
	leads := regtest.Symlink(t, filepath.Join(c.Study, "topic"), filepath.Join(c.Home, "state"))
	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()
	before := regtest.Snapshot(t, c.Base)
	for _, path := range []string{
		c.Study,
		filepath.Join(c.Study, ".lamplight", "knowledge-base-plugins", "notes"),
		filepath.Join(through, "lamplight", "notes"),
		filepath.Join(leads, "lamplight", "notes"),
	} {
		folder, resolved, err := guard.MakeFolder(path)
		if err == nil || !pluginregistry.IsInside(err) || folder != nil || resolved != "" {
			t.Errorf("MakeFolder(%s) = %v, %q, %v; want it refused as inside the Study home", path, folder, resolved, err)
		}
		if folder != nil {
			folder.Close()
		}
	}
	if regtest.Snapshot(t, c.Base) != before {
		t.Error("a refused folder was made all the same, or part of it")
	}

	// What is no folder is no folder to make either, and that is another
	// refusal than being inside.
	file := filepath.Join(c.Home, "notes.txt")
	regtest.WriteFile(t, file, "notes")
	nowhere := regtest.Symlink(t, filepath.Join(c.Base, "gone"), filepath.Join(c.Home, "dangling"))
	for _, path := range []string{file, filepath.Join(file, "x"), filepath.Join(nowhere, "x"), c.Home + "/new/../x"} {
		folder, _, err := guard.MakeFolder(path)
		if err == nil || pluginregistry.IsInside(err) || folder != nil {
			t.Errorf("MakeFolder(%s) = %v, %v; want an error that is not about the Study home", path, folder, err)
		}
		if folder != nil {
			folder.Close()
		}
	}
	if regtest.Exists(filepath.Join(c.Home, "new")) || regtest.Exists(filepath.Join(c.Base, "gone")) {
		t.Error("a folder was made on the way to a path that was refused")
	}
}

// OpenCommand gives the program, and the folder that really holds it with
// the program's own name there: what a plugin is started by.
func TestOpenCommandOpensTheProgramInItsFolder(t *testing.T) {
	c := regtest.New(t)
	real := regtest.Program(t, filepath.Join(c.Base, "opt", "kb", "notes-kb"))
	link := regtest.Symlink(t, real, filepath.Join(c.Bin, "kb"))
	plugin := pluginregistry.Plugin{Name: "notes", Command: []string{link, "serve"}}
	command, err := pluginregistry.OpenCommand(envOf(c), plugin)
	if err != nil {
		t.Fatalf("OpenCommand: %v", err)
	}
	defer command.Close()
	want, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := command.Program.Stat(); err != nil || !os.SameFile(want, got) {
		t.Errorf("the program opened is not %s (%v)", real, err)
	}
	holds, err := os.Stat(filepath.Dir(real))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := command.Folder.Stat(); err != nil || !os.SameFile(holds, got) || command.Name != "notes-kb" {
		t.Errorf("the folder and name are not those of the file the link leads to: %s (%v)", command.Name, err)
	}

	// What the listing flags is not opened, and neither is a plugin with
	// no program.
	inside := regtest.Program(t, filepath.Join(c.Study, "topic", "kb"))
	for _, refused := range []pluginregistry.Plugin{
		{Name: "agent", Command: []string{inside}},
		{Name: "gone", Command: []string{filepath.Join(c.Bin, "gone")}},
		{Name: "argument", Command: []string{link, "--config", filepath.Join(c.Study, "topic", "config.toml")}},
		{Name: "remote", URL: "https://kb.example/mcp"},
	} {
		command, err := pluginregistry.OpenCommand(envOf(c), refused)
		if err == nil || command != nil {
			t.Errorf("%s: OpenCommand = %v, %v; want it refused", refused.Name, command, err)
		}
		if command != nil {
			command.Close()
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
