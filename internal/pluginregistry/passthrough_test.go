//go:build linux || darwin

package pluginregistry_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regtest"
)

// What starting a plugin needs is found below folders the learner may pass
// through and not list, as /home is on many shared computers: the registry,
// the program in its folder, the folder to start it in, and a folder of its
// own to make (#92).
func TestWhatStartsAPluginIsFoundBelowFoldersThatMayNotBeListed(t *testing.T) {
	ctx := context.Background()
	c := regtest.New(t)
	real := regtest.Program(t, filepath.Join(c.Base, "opt", "kb", "notes-kb"))
	link := regtest.Symlink(t, real, filepath.Join(c.Bin, "kb"))
	inside := regtest.Program(t, filepath.Join(c.Study, "topic", "kb"))
	c.WriteRegistry(t, fmt.Sprintf(`{"format": 1, "plugins": [
		{"name": "agent", "command": [%q]},
		{"name": "notes", "command": [%q, "serve", %q]}]}`, inside, link, "--root="+c.Home))
	regtest.PassThrough(t, c.Base, c.Home, c.Bin, filepath.Dir(c.ConfigDir()), filepath.Join(c.Base, "opt"), filepath.Dir(real))

	list, err := pluginregistry.Read(ctx, envOf(c))
	if err != nil || len(list.Plugins) != 2 {
		t.Fatalf("Read = %+v, %v", list, err)
	}
	agent, notes := list.Plugins[0], list.Plugins[1]
	if notes.Problem != "" || !strings.Contains(agent.Problem, "its program is inside the Study home") {
		t.Errorf("Read says of the plugin outside the Study home %q, and of the one inside it %q", notes.Problem, agent.Problem)
	}

	command, err := pluginregistry.OpenCommand(envOf(c), notes)
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
	if file, err := pluginregistry.OpenProgram(envOf(c), notes); err != nil {
		t.Errorf("OpenProgram: %v", err)
	} else {
		file.Close()
	}
	if command, err := pluginregistry.OpenCommand(envOf(c), agent); err == nil || !pluginregistry.IsInside(err) || command != nil {
		t.Errorf("OpenCommand of a program in the Study home = %v, %v; want it refused as inside", command, err)
	}

	dir, path, err := pluginregistry.OpenFolder(envOf(c))
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	defer dir.Close()
	named, err := os.Stat(c.ConfigDir())
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := dir.Stat(); err != nil || !os.SameFile(opened, named) || path != c.ConfigDir() {
		t.Errorf("OpenFolder opened %s (%v); want %s", path, err, c.ConfigDir())
	}

	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()
	for path, want := range map[string]bool{
		c.Home:                               true,
		c.Bin:                                true,
		real:                                 true,
		link:                                 true,
		filepath.Join(c.Base, "nowhere"):     true,
		c.Study:                              false,
		inside:                               false,
		filepath.Join(c.Study, "not", "yet"): false,
	} {
		if got := guard.Outside(path); got != want {
			t.Errorf("Outside(%s) = %v, want %v", path, got, want)
		}
	}
	if err := guard.CheckProgram(link); err != nil {
		t.Errorf("CheckProgram: %v", err)
	}
	state := filepath.Join(c.Home, ".local", "state", "lamplight", "knowledge-base-plugins", "notes")
	folder, made, err := guard.MakeFolder(state)
	if err != nil || made != state {
		t.Fatalf("MakeFolder = %s, %v", made, err)
	}
	defer folder.Close()
	if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the folder made: %v, %v; want one for the learner alone", info, err)
	}
}

// Below a folder the learner may not even pass through, study cannot tell
// where a path leads. What it cannot tell is outside the Study home it does
// not use: the listing says so, and nothing is opened to be started.
func TestWhatStudyCannotFollowIsNotUsed(t *testing.T) {
	ctx := context.Background()
	c := regtest.New(t)
	python := regtest.Program(t, filepath.Join(c.Bin, "python3"))
	closed := regtest.Mkdir(t, filepath.Join(c.Base, "closed"))
	beyond := regtest.Program(t, filepath.Join(closed, "bin", "kb"))
	script := filepath.Join(closed, "kb.py")
	regtest.WriteFile(t, script, "print()\n")
	regtest.Mkdir(t, filepath.Join(closed, "config", "lamplight"))
	c.WriteRegistry(t, fmt.Sprintf(`{"format": 1, "plugins": [
		{"name": "allowed", "command": [%[1]q, %[3]q], "allow_study_home_arguments": true},
		{"name": "argument", "command": [%[1]q, "-u", %[3]q]},
		{"name": "beyond", "command": [%[2]q]},
		{"name": "fine", "command": [%[1]q, "-m", "kb"]}]}`, python, beyond, script))
	regtest.Shut(t, closed)

	list, err := pluginregistry.Read(ctx, envOf(c))
	if err != nil || len(list.Plugins) != 4 {
		t.Fatalf("Read = %+v, %v", list, err)
	}
	allowed, argument, program, fine := list.Plugins[0], list.Plugins[1], list.Plugins[2], list.Plugins[3]
	if allowed.Problem != "" || fine.Problem != "" {
		t.Errorf("Read says of a plugin it uses: %q, %q", allowed.Problem, fine.Problem)
	}
	if got := program.Problem; !strings.Contains(got, "study cannot follow the path of its program to its end (permission denied)") ||
		!strings.Contains(got, "study does not use this plugin") || !strings.Contains(got, "study knowledge-base add beyond --replace") {
		t.Errorf("the problem of a program that cannot be followed = %q", got)
	}
	if got := argument.Problem; !strings.Contains(got, "argument 2 of its command") ||
		!strings.Contains(got, "names a path study cannot follow to its end (permission denied)") ||
		!strings.Contains(got, "study does not use it") || !strings.Contains(got, "--allow-study-home-arguments") {
		t.Errorf("the problem of an argument that cannot be followed = %q", got)
	}

	for _, refused := range []pluginregistry.Plugin{program, argument} {
		command, err := pluginregistry.OpenCommand(envOf(c), refused)
		if pluginregistry.CodeOf(err) != "failed_precondition" || command != nil {
			t.Errorf("%s: OpenCommand = %v, %v; want failed_precondition", refused.Name, command, err)
		}
		file, err := pluginregistry.OpenProgram(envOf(c), refused)
		if pluginregistry.CodeOf(err) != "failed_precondition" || file != nil {
			t.Errorf("%s: OpenProgram = %v, %v; want failed_precondition", refused.Name, file, err)
		}
	}
	// What the learner allowed, and what names nothing there, is opened.
	for _, used := range []pluginregistry.Plugin{allowed, fine} {
		command, err := pluginregistry.OpenCommand(envOf(c), used)
		if err != nil {
			t.Errorf("%s: OpenCommand: %v", used.Name, err)
			continue
		}
		command.Close()
	}

	// The folder to start a plugin in, and the registry in it.
	elsewhere := c.With(map[string]string{"XDG_CONFIG_HOME": filepath.Join(closed, "config")})
	if dir, _, err := pluginregistry.OpenFolder(envOf(elsewhere)); pluginregistry.CodeOf(err) != "failed_precondition" || dir != nil {
		t.Errorf("OpenFolder of a folder that cannot be reached = %v, %v; want failed_precondition", dir, err)
	}
	if list, err := pluginregistry.Read(ctx, envOf(elsewhere)); pluginregistry.CodeOf(err) != "failed_precondition" || len(list.Plugins) != 0 {
		t.Errorf("Read of a registry that cannot be reached = %+v, %v; want failed_precondition", list, err)
	}

	// And whatever else a plugin is started with.
	guard := pluginregistry.NewGuard(envOf(c))
	defer guard.Close()
	if guard.Outside(filepath.Join(closed, "bin")) || guard.Outside(beyond) {
		t.Error("a path that cannot be followed counts as outside the Study home")
	}
	if err := guard.CheckProgram(beyond); pluginregistry.CodeOf(err) != "failed_precondition" {
		t.Errorf("CheckProgram of a program that cannot be followed = %v; want failed_precondition", err)
	}
	folder, made, err := guard.MakeFolder(filepath.Join(closed, "state", "lamplight"))
	if err == nil || pluginregistry.IsInside(err) || folder != nil || made != "" {
		t.Errorf("MakeFolder below a folder that cannot be passed through = %v, %q, %v; want it refused", folder, made, err)
	}
}
