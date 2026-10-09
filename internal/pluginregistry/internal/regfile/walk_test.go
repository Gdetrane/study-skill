package regfile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regtest"
)

// envOf is the registry's view of a run of study on c, started in dir.
func envOf(c *regtest.Computer, dir string) Env {
	return Env{StudyHome: c.StudyHome(), Dir: dir, Getenv: c.Getenv}
}

func TestWalkFollowsEachHopFromWhereItReallyIs(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	inner := regtest.Mkdir(t, filepath.Join(c.Home, "real", "inner"))
	sibling := regtest.Mkdir(t, filepath.Join(c.Home, "real", "sibling"))
	regtest.Program(t, filepath.Join(sibling, "kb"))
	// A folder reached under another name, and in it a link that goes up:
	// up from where the folder really is, which is not where the name is.
	alias := regtest.Symlink(t, filepath.Join("real", "inner"), filepath.Join(c.Home, "alias"))
	regtest.Symlink(t, filepath.Join("..", "sibling"), filepath.Join(inner, "up"))
	// What a path joined by name would find instead.
	regtest.WriteFile(t, filepath.Join(c.Home, "sibling"), "a decoy")
	regtest.WriteFile(t, filepath.Join(c.Home, "kb"), "a decoy")
	other := regtest.Mkdir(t, filepath.Join(c.Base, "other", "place"))
	regtest.Symlink(t, other, filepath.Join(c.Home, "abs"))
	regtest.Symlink(t, "tool", filepath.Join(c.Bin, "l2"))
	regtest.Symlink(t, "l2", filepath.Join(c.Bin, "l1"))
	dangling := regtest.Symlink(t, filepath.Join(c.Base, "nowhere", "x"), filepath.Join(c.Home, "dangling"))
	regtest.Symlink(t, "loop-b", filepath.Join(c.Home, "loop-a"))
	regtest.Symlink(t, "loop-a", filepath.Join(c.Home, "loop-b"))

	w := NewWalker(c.Study)
	defer w.Close()
	for _, tc := range []struct {
		name, path string
		// folder is the last folder the walk must be in, leaf what the path
		// ends in there, and missing what is not there below it.
		folder, leaf string
		missing      []string
		danglingLink string
	}{
		{name: "a file", path: tool, folder: c.Bin, leaf: "tool"},
		{name: "a folder", path: c.Bin, folder: c.Bin},
		{name: "a folder with a slash after it", path: c.Bin + "/", folder: c.Bin},
		{name: "the root folder", path: "/", folder: "/"},
		{name: "names that are not there", path: filepath.Join(c.Home, "new", "deeper"), folder: c.Home, missing: []string{"new", "deeper"}},
		{name: "a link to a folder, by a relative target", path: alias, folder: inner},
		{name: "a relative link in a folder reached under another name", path: filepath.Join(alias, "up"), folder: sibling},
		{name: "a file through both", path: filepath.Join(alias, "up", "kb"), folder: sibling, leaf: "kb"},
		{name: ".. after a link goes up from where the link leads", path: alias + "/../sibling/kb", folder: sibling, leaf: "kb"},
		{name: ". changes nothing", path: c.Home + "/./bin/./tool", folder: c.Bin, leaf: "tool"},
		{name: ".. at the root stays there", path: "/../../" + strings.TrimPrefix(tool, "/"), folder: c.Bin, leaf: "tool"},
		{name: "a link with an absolute target", path: filepath.Join(c.Home, "abs"), folder: other},
		{name: "a link to a link to a file", path: filepath.Join(c.Bin, "l1"), folder: c.Bin, leaf: "tool"},
		{name: "a link that leads nowhere", path: dangling, folder: c.Base, missing: []string{"nowhere", "x"}, danglingLink: dangling},
		{name: "a name missing below a link that leads somewhere", path: filepath.Join(c.Home, "abs", "new"), folder: other, missing: []string{"new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			place, err := w.Walk(tc.path)
			if err != nil {
				t.Fatalf("Walk(%s): %v", tc.path, err)
			}
			defer place.Close()
			if place.Path() != tc.folder || place.Leaf != tc.leaf || !slices.Equal(place.Missing, tc.missing) || place.DanglingLink != tc.danglingLink {
				t.Errorf("Walk(%s) ended in %s at %q, missing %q, dangling link %q; want %s at %q, missing %q, dangling link %q",
					tc.path, place.Path(), place.Leaf, place.Missing, place.DanglingLink, tc.folder, tc.leaf, tc.missing, tc.danglingLink)
			}
			// The folder it holds open is that folder.
			want, err := os.Stat(tc.folder)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := place.Folder().Stat("."); err != nil || !os.SameFile(want, got) {
				t.Errorf("the open folder is not %s (%v)", tc.folder, err)
			}
			if (tc.leaf != "") != (place.LeafInfo != nil) {
				t.Errorf("LeafInfo = %v for leaf %q", place.LeafInfo, tc.leaf)
			}
		})
	}

	for _, tc := range []struct{ name, path, want string }{
		{"links without end", filepath.Join(c.Home, "loop-a", "x"), "too many levels of symbolic links"},
		{"a file where a folder should be", filepath.Join(tool, "x"), "not a directory"},
		{"a path that is not absolute", filepath.Join("home", "bin", "tool"), "not an absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			place, err := w.Walk(tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.want) || IsInside(err) {
				t.Errorf("Walk(%s) = %v, %v; want an error with %q", tc.path, place, err, tc.want)
			}
		})
	}
}

func TestWalkRefusesTheStudyHomeAtEveryHop(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	topic := regtest.Mkdir(t, filepath.Join(c.Study, "topic"))
	inTopic := regtest.Program(t, filepath.Join(topic, "kb"))
	sub := regtest.Mkdir(t, filepath.Join(topic, "sub"))
	// From outside into the Study home, by a file and by a folder.
	toFile := regtest.Symlink(t, inTopic, filepath.Join(c.Home, "links", "to-file"))
	toFolder := regtest.Symlink(t, topic, filepath.Join(c.Home, "links", "to-folder"))
	// Through the Study home and out again: a link outside leads to a link
	// in the Study home, which leads to a program outside. Whoever writes in
	// the Study home decides where the second one leads.
	hop := regtest.Symlink(t, tool, filepath.Join(topic, "hop"))
	through := regtest.Symlink(t, hop, filepath.Join(c.Home, "links", "through"))
	// The same with a folder, and with a relative link inside it.
	regtest.Symlink(t, filepath.Join("..", "kb"), filepath.Join(sub, "up"))
	viaSub := regtest.Symlink(t, sub, filepath.Join(c.Home, "links", "via-sub"))
	// Other names for the Study home.
	alias := regtest.Symlink(t, c.Study, filepath.Join(c.Base, "alias"))
	// Names that only look like it.
	lookalike := regtest.Program(t, filepath.Join(c.Base, "study-notes", "kb"))
	namesake := regtest.Program(t, filepath.Join(c.Home, "study", "kb"))
	regtest.Symlink(t, c.Bin, filepath.Join(c.Home, "links", "study"))

	inside := []struct{ name, path string }{
		{"the Study home itself", c.Study},
		{"a file in a Topic", inTopic},
		{"a folder in the Study home", sub},
		{"something that is not there yet in a Topic", filepath.Join(topic, "new", "kb")},
		{"a link outside to a file in the Study home", toFile},
		{"a link outside to a folder in the Study home", filepath.Join(toFolder, "kb")},
		{"a link outside to a link in the Study home that leads out again", through},
		{"a folder link into the Study home and a relative link there", filepath.Join(viaSub, "up")},
		{"into the Study home and out again by ..", toFolder + "/../../home/bin/tool"},
		{"the Study home by another name", filepath.Join(alias, "topic", "kb")},
		{"a link in the Study home, named directly", hop},
	}
	outside := []struct{ name, path string }{
		{"a program outside", tool},
		{"a folder whose name starts like the Study home's", lookalike},
		{"another folder of the same name", namesake},
		{"a link of the same name", filepath.Join(c.Home, "links", "study", "tool")},
		{"something that is not there, outside", filepath.Join(c.Home, "new", "kb")},
	}
	// By each name the Study home has.
	for _, home := range []string{c.Study, alias, c.Study + "/", filepath.Join(c.Home, "..", "study")} {
		w := NewWalker(home)
		for _, tc := range inside {
			if place, err := w.Walk(tc.path); !IsInside(err) {
				t.Errorf("Study home %s: %s (%s) = %v, %v; want it refused as inside the Study home", home, tc.name, tc.path, place, err)
			}
			if !w.Inside(tc.path) {
				t.Errorf("Study home %s: Inside(%s) = false", home, tc.path)
			}
		}
		for _, tc := range outside {
			place, err := w.Walk(tc.path)
			if err != nil {
				t.Errorf("Study home %s: %s (%s): %v; want it followed", home, tc.name, tc.path, err)
				continue
			}
			place.Close()
			if w.Inside(tc.path) {
				t.Errorf("Study home %s: Inside(%s) = true", home, tc.path)
			}
		}
		w.Close()
	}

	// A path that cannot be followed for another reason is not inside.
	w := NewWalker(c.Study)
	defer w.Close()
	if w.Inside(filepath.Join(tool, "x")) || w.Inside("relative/path") {
		t.Error("a path that cannot be followed counts as inside the Study home")
	}
}

// The Study home is known by what it is, not by what it is called: another
// spelling of its name, on a file system that ignores letter case, is the
// same folder, and a folder elsewhere with the same name is another.
func TestWalkKnowsTheStudyHomeByWhatItIsNotByItsName(t *testing.T) {
	c := regtest.New(t)
	kb := regtest.Program(t, filepath.Join(c.Study, "topic", "kb"))

	// The rule itself, with two folders whose names say the opposite of
	// what they are: one named like the Study home that is another folder,
	// and the Study home reached under a name that shares nothing with it.
	namesake := regtest.Mkdir(t, filepath.Join(c.Home, filepath.Base(c.Study)))
	elsewhere := regtest.Symlink(t, c.Study, filepath.Join(c.Home, "elsewhere"))
	w := NewWalker(c.Study)
	home, err := os.Stat(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(namesake)
	if err != nil {
		t.Fatal(err)
	}
	place := &Place{root: "/"}
	if err := w.entered(place, home); !IsInside(err) {
		t.Errorf("the Study home under another name was entered: %v", err)
	}
	if err := w.entered(place, other); err != nil {
		t.Errorf("another folder named like the Study home was refused: %v", err)
	}
	w.Close()

	// The same on a file system that ignores letter case, when the tests
	// run on one: macOS has one by default, Linux seldom.
	upper := filepath.Join(c.Base, "CASE")
	regtest.Mkdir(t, upper)
	lower := filepath.Join(c.Base, "case")
	a, errA := os.Stat(upper)
	b, errB := os.Stat(lower)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Log("this file system tells letter case apart, so the other spelling of a name is another name here")
		return
	}
	shouted := filepath.Join(c.Base, strings.ToUpper(filepath.Base(c.Study)))
	for _, tc := range []struct{ home, path string }{
		{shouted, kb},
		{c.Study, filepath.Join(shouted, "Topic", "KB")},
		{shouted, filepath.Join(shouted, "topic", "kb")},
	} {
		w := NewWalker(tc.home)
		if !w.Inside(tc.path) {
			t.Errorf("with the Study home named %s, %s passed as outside it", tc.home, tc.path)
		}
		w.Close()
	}
}

// Between looking at a folder and opening it, another program can put
// something else under its name. The walk opens what it looked at or stops:
// it never goes on into what took its place.
func TestWalkOpensTheFolderItLookedAt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// swap puts something else where the folder was.
		swap func(t *testing.T, c *regtest.Computer, dir string)
	}{
		{"a link into the Study home", func(t *testing.T, c *regtest.Computer, dir string) {
			regtest.Symlink(t, regtest.Mkdir(t, filepath.Join(c.Study, "topic", "cfg")), dir)
		}},
		{"a link to another folder outside", func(t *testing.T, c *regtest.Computer, dir string) {
			regtest.Symlink(t, regtest.Mkdir(t, filepath.Join(c.Base, "elsewhere")), dir)
		}},
		{"a link to the folder beside it", func(t *testing.T, c *regtest.Computer, dir string) {
			regtest.Mkdir(t, filepath.Join(filepath.Dir(dir), "beside"))
			regtest.Symlink(t, "beside", dir)
		}},
		{"another folder", func(t *testing.T, c *regtest.Computer, dir string) {
			regtest.Mkdir(t, dir)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := regtest.New(t)
			dir := regtest.Mkdir(t, c.ConfigDir())
			regtest.WriteFile(t, filepath.Join(dir, "marker"), "the folder that was looked at")
			w := NewWalker(c.Study)
			defer w.Close()
			swapped := false
			w.beforeEnter = func(path string) {
				if path != dir || swapped {
					return
				}
				swapped = true
				if err := os.Rename(dir, dir+".moved"); err != nil {
					t.Fatal(err)
				}
				tc.swap(t, c, dir)
			}
			place, err := w.Walk(filepath.Join(dir, "marker"))
			if !swapped {
				t.Fatal("the folder was never about to be opened, so this test proves nothing")
			}
			if err == nil || !strings.Contains(err.Error(), "changed while study was reading it") {
				t.Fatalf("Walk = %v, %v; want it to stop because the folder changed", place, err)
			}
			// Asked again, it judges what is there now.
			again, err := w.Walk(filepath.Join(dir, "marker"))
			if strings.HasPrefix(tc.name, "a link into the Study home") {
				if !IsInside(err) {
					t.Errorf("the second walk = %v, %v; want it refused as inside the Study home", again, err)
				}
			} else if err != nil || len(again.Missing) != 1 {
				t.Errorf("the second walk = %+v, %v; want the folder that is there now, without the marker", again, err)
			}
			if again != nil {
				again.Close()
			}
		})
	}
}

// A Study home that is not there yet has no identity. What will be inside
// it is still refused, by where it will be.
func TestWalkRefusesWhatWillBeInsideAStudyHomeNotYetThere(t *testing.T) {
	c := regtest.New(t)
	later := filepath.Join(c.Base, "later", "study")
	regtest.Mkdir(t, filepath.Join(c.Base, "elsewhere"))
	// The folder the Study home will be made in, under another name.
	alias := regtest.Symlink(t, c.Base, filepath.Join(c.Home, "base"))

	w := NewWalker(later)
	defer w.Close()
	for path, want := range map[string]bool{
		later: true,
		filepath.Join(later, ".config", "lamplight"):      true,
		filepath.Join(alias, "later", "study", ".config"): true,
		// Whether these will be the same folder is the file system's to
		// say once it is made. Until then they are taken for it.
		filepath.Join(c.Base, "Later", "STUDY", ".config"):        true,
		filepath.Join(c.Base, "later"):                            false,
		filepath.Join(c.Base, "later", "other"):                   false,
		filepath.Join(c.Base, "elsewhere", "later", "study", "x"): false,
		filepath.Join(c.Home, "later", "study"):                   false,
		c.Bin:                                                     false,
	} {
		if got := w.Inside(path); got != want {
			t.Errorf("Inside(%s) = %v, want %v", path, got, want)
		}
	}

	// Once it is there, it is known by what it is.
	regtest.Program(t, filepath.Join(later, "topic", "kb"))
	made := NewWalker(later)
	defer made.Close()
	if !made.Inside(filepath.Join(alias, "later", "study", "topic", "kb")) || made.Inside(filepath.Join(c.Base, "later", "other")) {
		t.Error("the Study home, once made, is not known by its identity")
	}
}

// A Study home that is there and cannot be looked at cannot be told apart
// from other folders. Then nothing is taken for outside it.
func TestWalkTakesNothingForOutsideAStudyHomeItCannotLookAt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may look anywhere")
	}
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	locked := regtest.Mkdir(t, filepath.Join(c.Base, "locked"))
	hidden := regtest.Mkdir(t, filepath.Join(locked, "study"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	w := NewWalker(hidden)
	defer w.Close()
	if place, err := w.Walk(tool); err == nil || IsInside(err) || !strings.Contains(err.Error(), "cannot look at the Study home") {
		t.Errorf("Walk = %v, %v; want it to stop because the Study home cannot be looked at", place, err)
	}
	if !w.Inside(tool) {
		t.Error("a path counts as outside a Study home that cannot be looked at")
	}
	if err := w.CheckProgram(tool); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("CheckProgram = %v; want failed_precondition", err)
	}
	folder, err := Open(envOf(c.With(map[string]string{"STUDY_HOME": hidden}), c.Home))
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot look at the Study home") || folder != nil {
		t.Errorf("Open = %v, %v; want failed_precondition, the Study home cannot be looked at", folder, err)
	}

	// One it may not list, and may still see, has its identity.
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	regtest.Program(t, filepath.Join(hidden, "kb"))
	if err := os.Chmod(hidden, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o755) })
	seen := NewWalker(hidden)
	defer seen.Close()
	if place, err := seen.Walk(tool); err != nil {
		t.Errorf("Walk outside a Study home that cannot be listed: %v", err)
	} else {
		place.Close()
	}
	if !seen.Inside(filepath.Join(hidden, "kb")) {
		t.Error("a path into a Study home that cannot be listed counts as outside it")
	}
}
