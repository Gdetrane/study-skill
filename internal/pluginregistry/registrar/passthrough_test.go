//go:build linux || darwin

package registrar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regtest"
)

// What registering and removing plugins comes to on a computer, as text
// that names no folder of this test's: to compare two computers by.
func changes(t *testing.T, c *regtest.Computer) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	say := func(what string, result any, err error) {
		if err != nil {
			fmt.Fprintf(&b, "%s: %s, %v\n", what, pluginregistry.CodeOf(err), err)
			return
		}
		fmt.Fprintf(&b, "%s: %+v\n", what, result)
	}
	script := filepath.Join(c.Study, "topic", "kb.py")
	notes := filepath.Join(c.Home, "notes")

	list, err := pluginregistry.Read(ctx, at(c))
	say("list on a new computer", list, err)
	dry, err := Add(ctx, at(c), Spec{Name: "shelf", Command: []string{"study-shelf", "--stdio"}, DryRun: true})
	say("add, dry run", dry, err)
	fmt.Fprintf(&b, "the configuration folder after a dry run: %v\n", regtest.Exists(c.ConfigDir()))
	added, err := Add(ctx, at(c), Spec{Name: "shelf", Command: []string{"study-shelf", "--stdio"}})
	say("add by a name on PATH", added, err)
	again, err := Add(ctx, at(c), Spec{Name: "shelf", Command: []string{filepath.Join(c.Bin, "study-shelf"), "--stdio"}})
	say("add again by its path", again, err)
	remote, err := Add(ctx, at(c), Spec{Name: "remote", URL: "http://localhost:8765/mcp"})
	say("add a URL", remote, err)
	home, err := Add(ctx, at(c), Spec{Name: "notes", Command: []string{"~/bin/study-shelf", "--root=" + notes}})
	say("add by a path from the home folder", home, err)

	_, err = Add(ctx, at(c), Spec{Name: "agent", Command: []string{filepath.Join(c.Study, "topic", "kb")}})
	say("a program in the Study home", nil, err)
	_, err = Add(ctx, at(c), Spec{Name: "linked", Command: []string{"kb-link"}})
	say("a link on PATH into the Study home", nil, err)
	_, err = Add(ctx, at(c), Spec{Name: "gone", Command: []string{filepath.Join(c.Bin, "gone")}})
	say("a program that is not there", nil, err)
	_, err = Add(ctx, at(c), Spec{Name: "kb", Command: []string{"study-shelf", "-u", script}})
	say("an argument in the Study home", nil, err)
	allowed, err := Add(ctx, at(c), Spec{Name: "kb", Command: []string{"study-shelf", "-u", script}, AllowStudyHomeArguments: true})
	say("the same, allowed", allowed, err)
	_, err = Add(ctx, at(c), Spec{Name: "shelf", URL: "http://localhost:1/"})
	say("a name that is registered", nil, err)

	list, err = pluginregistry.Read(ctx, at(c))
	say("list", list, err)
	gone, err := Remove(ctx, at(c), "remote", true)
	say("remove, dry run", gone, err)
	gone, err = Remove(ctx, at(c), "remote", false)
	say("remove", gone, err)
	_, err = Remove(ctx, at(c), "remote", false)
	say("remove what is not registered", nil, err)
	list, err = pluginregistry.Read(ctx, at(c))
	say("list after removing", list, err)

	// With another Study home, what is registered is judged against that
	// one: the folder an argument names is inside it now.
	moved := c.With(map[string]string{"STUDY_HOME": notes})
	list, err = pluginregistry.Read(ctx, at(moved))
	say("list with another Study home", list, err)

	fmt.Fprintf(&b, "the registry:\n%s", regtest.Text(t, c.Registry()))
	for _, path := range []string{c.Registry(), c.ConfigDir(), filepath.Dir(c.ConfigDir())} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "mode of %s: %v\n", path, info.Mode())
	}
	return strings.ReplaceAll(b.String(), c.Base, "$BASE")
}

// A learner's computer with a program on PATH, a Topic with a program and a
// script in it, and a link on PATH that leads to the program in the Topic.
func computerWithATopic(t *testing.T) *regtest.Computer {
	t.Helper()
	c := regtest.New(t)
	regtest.Program(t, filepath.Join(c.Bin, "study-shelf"))
	regtest.WriteFile(t, filepath.Join(c.Study, "topic", "kb.py"), "print()\n")
	regtest.Symlink(t, regtest.Program(t, filepath.Join(c.Study, "topic", "kb")), filepath.Join(c.Bin, "kb-link"))
	regtest.Mkdir(t, filepath.Join(c.Home, "notes"))
	return c
}

// Registering, listing and removing plugins work below folders the learner
// may pass through and not list, as /home is on many shared computers, and
// decide there exactly as they do anywhere: above Lamplight's
// configuration folder, above a program and around it, and above the Study
// home (#92).
func TestChangesAreTheSameBelowFoldersThatMayNotBeListed(t *testing.T) {
	listed := changes(t, computerWithATopic(t))
	for _, want := range []string{
		"add by a name on PATH: {Plugin:{Name:shelf Command:[$BASE/home/bin/study-shelf --stdio]",
		"a program in the Study home: invalid_argument,",
		"a link on PATH into the Study home: invalid_argument,",
		"a program that is not there: not_found,",
		"an argument in the Study home: invalid_argument,",
		"AllowStudyHomeArguments:true",
		"remove: {Plugin:{Name:remote",
		"names something inside the Study home",
	} {
		if !strings.Contains(listed, want) {
			t.Fatalf("on a computer whose folders may all be listed, the changes do not say %q:\n%s", want, listed)
		}
	}

	c := computerWithATopic(t)
	regtest.PassThrough(t, c.Base, c.Home, c.Bin)
	if passed := changes(t, c); passed != listed {
		t.Errorf("below folders that may not be listed:\n%s\nbelow folders that may be:\n%s", passed, listed)
	}
}

// What a crash left beside the registry is removed by the next change, in a
// configuration folder below folders that may not be listed too.
func TestAChangeBelowFoldersThatMayNotBeListedRemovesWhatACrashLeft(t *testing.T) {
	ctx := context.Background()
	c := regtest.New(t)
	left := filepath.Join(c.ConfigDir(), tempPrefix+"0123456789abcdef")
	regtest.WriteFile(t, left, "{")
	regtest.PassThrough(t, c.Base, c.Home, filepath.Dir(c.ConfigDir()))
	if _, err := Add(ctx, at(c), Spec{Name: "remote", URL: "http://localhost:8765/mcp"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if regtest.Exists(left) {
		t.Error("the temporary file a crash left is still there")
	}
	if got := names(t, c); !reflect.DeepEqual(got, []string{"remote"}) {
		t.Errorf("registered: %v", got)
	}
}

// A folder the learner may not even pass through stops study from telling
// where a path leads. What it cannot tell is outside the Study home, it
// refuses: the configuration folder, a program, and an argument, which is
// registered only when the learner allows it.
func TestAddRefusesWhatItCannotFollow(t *testing.T) {
	ctx := context.Background()
	c := regtest.New(t)
	shelf := regtest.Program(t, filepath.Join(c.Bin, "study-shelf"))
	closed := regtest.Mkdir(t, filepath.Join(c.Base, "closed"))
	program := regtest.Program(t, filepath.Join(closed, "bin", "kb"))
	config := filepath.Join(closed, "kb.toml")
	regtest.WriteFile(t, config, "port = 8765\n")
	regtest.Shut(t, closed)

	for _, dryRun := range []bool{true, false} {
		// The configuration folder.
		beyond := c.With(map[string]string{"XDG_CONFIG_HOME": filepath.Join(closed, "config")})
		_, err := Add(ctx, at(beyond), Spec{Name: "remote", URL: "http://localhost:8765/mcp", DryRun: dryRun})
		if pluginregistry.CodeOf(err) != "failed_precondition" || !strings.Contains(err.Error(), "cannot reach Lamplight's configuration folder") {
			t.Errorf("a configuration folder that cannot be reached (dry run %v) = %v; want failed_precondition", dryRun, err)
		}
		if _, err := Remove(ctx, at(beyond), "remote", dryRun); pluginregistry.CodeOf(err) != "failed_precondition" {
			t.Errorf("Remove with a configuration folder that cannot be reached (dry run %v) = %v; want failed_precondition", dryRun, err)
		}
		if list, err := pluginregistry.Read(ctx, at(beyond)); pluginregistry.CodeOf(err) != "failed_precondition" || len(list.Plugins) != 0 {
			t.Errorf("Read with a configuration folder that cannot be reached = %+v, %v; want failed_precondition", list, err)
		}

		// A program.
		_, err = Add(ctx, at(c), Spec{Name: "kb", Command: []string{program}, DryRun: dryRun})
		if pluginregistry.CodeOf(err) != "failed_precondition" || !strings.Contains(err.Error(), "study cannot follow") {
			t.Errorf("a program that cannot be followed (dry run %v) = %v; want failed_precondition", dryRun, err)
		}

		// An argument, as a whole and after an =.
		for _, arg := range []string{config, "--config=" + config} {
			_, err = Add(ctx, at(c), Spec{Name: "kb", Command: []string{shelf, "serve", arg}, DryRun: dryRun})
			if pluginregistry.CodeOf(err) != "failed_precondition" || !strings.Contains(err.Error(), "argument 2 of the command") ||
				!strings.Contains(err.Error(), "names a path study cannot follow to its end (permission denied)") ||
				!strings.Contains(err.Error(), "--allow-study-home-arguments") || strings.Contains(err.Error(), "names something inside") {
				t.Errorf("an argument that cannot be followed (dry run %v) = %v; want failed_precondition, with the reason and "+
					"--allow-study-home-arguments", dryRun, err)
			}
		}
	}
	if regtest.Exists(c.ConfigDir()) {
		t.Fatal("a refused registration made the configuration folder")
	}

	// The learner can allow the argument, as one in the Study home: they
	// know where it leads. The registry records that they did.
	got, err := Add(ctx, at(c), Spec{Name: "kb", Command: []string{shelf, "serve", "--config=" + config}, AllowStudyHomeArguments: true})
	want := Plugin{Name: "kb", Command: []string{shelf, "serve", "--config=" + config}, AllowStudyHomeArguments: true}
	if err != nil || !got.Changed || !reflect.DeepEqual(got.Plugin, want) {
		t.Fatalf("Add, allowed = %+v, %v; want %+v", got, err, want)
	}
	list, err := pluginregistry.Read(ctx, at(c))
	if err != nil || len(list.Plugins) != 1 || !reflect.DeepEqual(list.Plugins[0], want) {
		t.Errorf("Read = %+v, %v; want the plugin, allowed and without a problem", list, err)
	}
}
