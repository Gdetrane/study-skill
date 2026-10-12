//go:build linux || darwin

package regfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regtest"
)

// These tests are about folders the learner may pass through and not list,
// as /home is on many shared computers (mode 711), and about folders they
// may not even pass through (#92). The first kind changes nothing: a walk
// goes through it and decides as it does anywhere. The second kind stops a
// walk, and a walk that was stopped has decided nothing, so whoever asked
// refuses.

// told is where a walk ended, or why it did not, as text to compare.
func told(w *Walker, path string) string {
	place, err := w.Walk(path)
	switch {
	case IsInside(err):
		return "inside the Study home"
	case err != nil:
		return "not followed: " + Reason(err)
	}
	defer place.Close()
	open := "the folder that is open is that folder"
	named, err := os.Stat(place.Path())
	if opened, serr := place.Folder().Stat("."); err != nil || serr != nil || !os.SameFile(named, opened) {
		open = fmt.Sprintf("the folder that is open is another (%v, %v)", err, serr)
	}
	kind := ""
	if place.LeafInfo != nil {
		kind = place.LeafInfo.Mode().Type().String()
	}
	return fmt.Sprintf("in %s at %q (%s), missing %q, dangling link %q; %s", place.Path(), place.Leaf, kind, place.Missing,
		place.DanglingLink, open)
}

// A walk decides the same whether or not it may list the folders it goes
// through: where it ends, what it finds there, and what it refuses as
// inside the Study home.
func TestWalkDecidesTheSameThroughFoldersItMayNotList(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	inner := regtest.Mkdir(t, filepath.Join(c.Home, "real", "inner"))
	sibling := regtest.Mkdir(t, filepath.Join(c.Home, "real", "sibling"))
	regtest.Program(t, filepath.Join(sibling, "kb"))
	alias := regtest.Symlink(t, filepath.Join("real", "inner"), filepath.Join(c.Home, "alias"))
	regtest.Symlink(t, filepath.Join("..", "sibling"), filepath.Join(inner, "up"))
	other := regtest.Mkdir(t, filepath.Join(c.Base, "other", "place"))
	regtest.Symlink(t, other, filepath.Join(c.Home, "abs"))
	regtest.Symlink(t, "tool", filepath.Join(c.Bin, "l1"))
	dangling := regtest.Symlink(t, filepath.Join(c.Base, "nowhere", "x"), filepath.Join(c.Home, "dangling"))
	regtest.Symlink(t, "loop-b", filepath.Join(c.Home, "loop-a"))
	regtest.Symlink(t, "loop-a", filepath.Join(c.Home, "loop-b"))
	regtest.WriteFile(t, filepath.Join(c.Home, "notes.txt"), "notes")
	// The Study home, what is in it, and the ways into it from outside.
	topic := regtest.Mkdir(t, filepath.Join(c.Study, "topic"))
	inTopic := regtest.Program(t, filepath.Join(topic, "kb"))
	hop := regtest.Symlink(t, tool, filepath.Join(topic, "hop"))
	toFolder := regtest.Symlink(t, topic, filepath.Join(c.Home, "links", "to-folder"))
	through := regtest.Symlink(t, hop, filepath.Join(c.Home, "links", "through"))
	studyAlias := regtest.Symlink(t, c.Study, filepath.Join(c.Base, "alias"))
	namesake := regtest.Program(t, filepath.Join(c.Home, "study", "kb"))

	paths := []string{
		"/", c.Base, c.Home, c.Bin, c.Bin + "/", tool,
		filepath.Join(c.Home, "new", "deeper"),
		alias, filepath.Join(alias, "up"), filepath.Join(alias, "up", "kb"), alias + "/../sibling/kb",
		c.Home + "/./bin/./tool", "/../../" + strings.TrimPrefix(tool, "/"),
		filepath.Join(c.Home, "abs"), filepath.Join(c.Home, "abs", "new"), filepath.Join(c.Bin, "l1"), dangling,
		filepath.Join(c.Home, "loop-a", "x"), filepath.Join(tool, "x"), filepath.Join(c.Home, "notes.txt"),
		namesake,
		c.Study, inTopic, filepath.Join(topic, "new", "kb"), hop, filepath.Join(toFolder, "kb"), through,
		toFolder + "/../../home/bin/tool", filepath.Join(studyAlias, "topic", "kb"),
	}
	decide := func() map[string]string {
		w := NewWalker(c.Study)
		defer w.Close()
		out := map[string]string{}
		for _, path := range paths {
			out[path] = told(w, path)
		}
		return out
	}
	listed := decide()
	// Something must have been decided each way, or the comparison below
	// compares nothing.
	if got := listed[tool]; !strings.Contains(got, `at "tool"`) || !strings.Contains(got, "is that folder") {
		t.Fatalf("with every folder listed, the walk to %s said: %s", tool, got)
	}
	if got := listed[through]; got != "inside the Study home" {
		t.Fatalf("with every folder listed, the walk to %s said: %s", through, got)
	}

	// Every folder outside the Study home that a path above goes through,
	// the one above the Study home and the ones that hold what is looked
	// at included.
	regtest.PassThrough(t, c.Base, c.Home, c.Bin, filepath.Join(c.Home, "real"), inner, sibling, filepath.Join(c.Base, "other"),
		other, filepath.Join(c.Home, "links"), filepath.Join(c.Home, "study"))
	passed := decide()
	for _, path := range paths {
		if passed[path] != listed[path] {
			t.Errorf("Walk(%s)\n  through folders it may not list: %s\n  through folders it may list:     %s", path, passed[path], listed[path])
		}
	}

	// And so for a Study home that is not there yet, below a folder that
	// may not be listed: what will be inside it is known by where it will
	// be.
	later := filepath.Join(c.Base, "later", "study")
	w := NewWalker(later)
	defer w.Close()
	for path, want := range map[string]bool{
		later:                                   true,
		filepath.Join(later, "topic", "kb"):     true,
		filepath.Join(c.Base, "later", "other"): false,
		tool:                                    false,
	} {
		if err := w.Follow(path); IsInside(err) != want || (!want && err != nil) {
			t.Errorf("with a Study home not yet there, Follow(%s) = %v; want inside the Study home: %v", path, err, want)
		}
	}
}

// The folder a walk opened to pass through is still the folder it looked
// at, or the walk stops: the races are the same ones, and so is what is
// made of them.
func TestWalkOpensTheFolderItLookedAtBelowAFolderItMayNotList(t *testing.T) {
	c := regtest.New(t)
	dir := regtest.Mkdir(t, c.ConfigDir())
	regtest.WriteFile(t, filepath.Join(dir, "marker"), "the folder that was looked at")
	regtest.PassThrough(t, c.Base, c.Home, filepath.Dir(dir), dir)
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
		// The folder took its mode with it: it gets it back under its new
		// name, so that the test can remove it.
		t.Cleanup(func() { _ = os.Chmod(dir+".moved", 0o755) })
		regtest.Symlink(t, regtest.Mkdir(t, filepath.Join(c.Study, "topic", "cfg")), dir)
	}
	place, err := w.Walk(filepath.Join(dir, "marker"))
	if !swapped {
		t.Fatal("the folder was never about to be opened, so this test proves nothing")
	}
	if err == nil || !strings.Contains(err.Error(), "changed while study was reading it") {
		t.Fatalf("Walk = %v, %v; want it to stop because the folder changed", place, err)
	}
	if again, err := w.Walk(filepath.Join(dir, "marker")); !IsInside(err) {
		t.Errorf("the second walk = %v, %v; want it refused as inside the Study home", again, err)
	}
}

// A folder study may not list is opened twice: to list it, which is
// refused, and then to pass through it. Whatever takes the folder's place
// between the two is opened by the second, and is not the folder the walk
// looked at: so the walk stops, whatever it is.
func TestWalkStopsWhenAFolderChangesBetweenItsTwoOpens(t *testing.T) {
	for _, put := range []string{"another folder that may not be listed", "another folder that may be listed",
		"a folder of the Study home, moved there", "a link into the Study home", "a file", "nothing"} {
		t.Run(put, func(t *testing.T) {
			c := regtest.New(t)
			dir := regtest.Mkdir(t, c.ConfigDir())
			regtest.WriteFile(t, filepath.Join(dir, "marker"), "the folder that was looked at")
			inside := regtest.Mkdir(t, filepath.Join(c.Study, "topic", "cfg"))
			regtest.WriteFile(t, filepath.Join(inside, "marker"), "the agent's")
			regtest.PassThrough(t, dir)
			w := NewWalker(c.Study)
			defer w.Close()
			swapped := false
			betweenOpens = func(name string) {
				if name != filepath.Base(dir) || swapped {
					return
				}
				swapped = true
				if err := os.Rename(dir, dir+".moved"); err != nil {
					t.Fatal(err)
				}
				// Folders that changed mode get it back under the names
				// they have by then, so that the test can remove them.
				t.Cleanup(func() {
					_ = os.Chmod(dir+".moved", 0o755)
					_ = os.Chmod(dir, 0o755)
				})
				switch put {
				case "another folder that may not be listed", "another folder that may be listed":
					regtest.WriteFile(t, filepath.Join(regtest.Mkdir(t, dir), "marker"), "another")
					if put == "another folder that may not be listed" {
						if err := os.Chmod(dir, 0o311); err != nil {
							t.Fatal(err)
						}
					}
				case "a folder of the Study home, moved there":
					if err := os.Chmod(inside, 0o311); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(inside, dir); err != nil {
						t.Fatal(err)
					}
				case "a link into the Study home":
					regtest.Symlink(t, inside, dir)
				case "a file":
					regtest.WriteFile(t, dir, "")
				}
			}
			t.Cleanup(func() { betweenOpens = nil })
			place, err := w.Walk(filepath.Join(dir, "marker"))
			if !swapped {
				t.Fatal("the folder was never opened a second time, so this test proves nothing")
			}
			if err == nil {
				place.Close()
				t.Fatalf("the walk went on into what took the folder's place")
			}
			if !strings.Contains(err.Error(), "changed while study was reading it") {
				t.Errorf("Walk = %v; want it to stop because the folder changed", err)
			}
		})
	}
}

// A folder opened only to be passed through is used as it was opened, like
// any other: the program that is opened is the one in it, even when the
// folder's path comes to lead into the Study home between the check and
// the open.
func TestOpenProgramOpensTheProgramThatWasCheckedInAFolderItMayNotList(t *testing.T) {
	c := regtest.New(t)
	bin := regtest.Mkdir(t, filepath.Join(c.Home, "opt", "bin"))
	checked := regtest.Program(t, filepath.Join(bin, "kb"))
	want, err := os.Stat(checked)
	if err != nil {
		t.Fatal(err)
	}
	other := regtest.Mkdir(t, filepath.Join(c.Study, "topic", "bin"))
	regtest.WriteFile(t, filepath.Join(other, "kb"), "#!/bin/sh\necho the agent's\n")
	if err := os.Chmod(filepath.Join(other, "kb"), 0o755); err != nil {
		t.Fatal(err)
	}
	regtest.PassThrough(t, c.Base, c.Home, filepath.Dir(bin), bin)
	// The folder takes its mode with it when it is moved: it gets it back
	// under its new name, so that the test can remove it.
	t.Cleanup(func() { _ = os.Chmod(bin+".moved", 0o755) })

	w := NewWalker(c.Study)
	defer w.Close()
	swapped := lookThenSwap(t, "kb", func() {
		if err := os.Rename(bin, bin+".moved"); err != nil {
			t.Fatal(err)
		}
		regtest.Symlink(t, other, bin)
	})
	program, err := w.OpenProgramIn(checked)
	if !*swapped {
		t.Fatal("the program was never about to be opened, so this test proves nothing")
	}
	if err != nil {
		t.Fatalf("OpenProgramIn: %v", err)
	}
	defer program.Close()
	if got, err := program.File.Stat(); err != nil || !os.SameFile(want, got) {
		t.Errorf("the open file is not the program that was checked (%v)", err)
	}
	if data, _ := io.ReadAll(program.File); strings.Contains(string(data), "the agent's") {
		t.Errorf("the program at the path was opened in place of the one checked: %q", data)
	}
	// The folder handed on is the one that holds the program that was
	// checked, wherever its old name leads now.
	holds, err := os.Stat(bin + ".moved")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := program.Folder.Stat(); err != nil || !os.SameFile(holds, got) {
		t.Errorf("the folder handed on is not the one that holds the program (%v)", err)
	}
	afterLook = nil
	if again, err := w.OpenProgramIn(checked); !IsInside(err) || again != nil {
		t.Errorf("OpenProgramIn of the path afterwards = %v, %v; want it refused", again, err)
	}
}

// A file is looked at and then opened in a folder the walk opened as it is
// in an os.Root: a link put under its name in between is not followed.
func TestOpenRegularOpensTheFileItLookedAtInAFolderTheWalkOpened(t *testing.T) {
	for _, mode := range []string{"listed", "passed through"} {
		t.Run(mode, func(t *testing.T) {
			c := regtest.New(t)
			dir := regtest.Mkdir(t, c.ConfigDir())
			lock := filepath.Join(dir, LockName)
			regtest.WriteFile(t, lock, "")
			regtest.WriteFile(t, filepath.Join(dir, FileName), `{"format": 1, "plugins": []}`)
			if mode != "listed" {
				regtest.PassThrough(t, c.Base, c.Home, filepath.Dir(dir), dir)
			}
			w := NewWalker(c.Study)
			defer w.Close()
			place, err := w.Walk(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer place.Close()

			swapped := lookThenSwap(t, LockName, func() {
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				regtest.Symlink(t, FileName, lock)
			})
			file, _, err := OpenRegular(place.Folder(), LockName, os.O_RDWR)
			if !*swapped {
				t.Fatal("the file was never about to be opened, so this test proves nothing")
			}
			if !errors.Is(err, ErrNotRegular) || file != nil {
				t.Fatalf("OpenRegular = %v, %v; want the link refused, not followed", file, err)
			}
			// A link that leads out of the folder is no more followed than
			// one that stays in it.
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			regtest.Symlink(t, regtest.Program(t, filepath.Join(c.Study, "topic", "kb")), lock)
			if file, _, err := OpenRegular(place.Folder(), LockName, os.O_RDWR); !errors.Is(err, ErrNotRegular) || file != nil {
				t.Fatalf("OpenRegular of a link out of the folder = %v, %v; want it refused, not followed", file, err)
			}

			replaced := lookThenSwap(t, FileName, func() {
				regtest.WriteFile(t, filepath.Join(dir, "new"), `{"format": 1, "plugins": [], "new": true}`)
				if err := os.Rename(filepath.Join(dir, "new"), filepath.Join(dir, FileName)); err != nil {
					t.Fatal(err)
				}
			})
			data, found, err := ReadRegular(place.Folder(), FileName, MaxBytes)
			if !*replaced || err != nil || !found || !strings.Contains(string(data), `"new": true`) {
				t.Errorf("ReadRegular after the file was replaced = %q, %v, %v; want the new file", data, found, err)
			}
			if file, _, err := OpenRegular(place.Folder(), "missing", os.O_RDONLY); file != nil || err != nil {
				t.Errorf("OpenRegular of nothing = %v, %v", file, err)
			}
			if _, _, err := OpenRegular(place.Folder(), ".", os.O_RDONLY); !errors.Is(err, ErrNotRegular) {
				t.Errorf("OpenRegular of a folder = %v; want it refused", err)
			}
		})
	}
}

// A folder the walk has open takes one name at a time: nothing that would
// lead out of it, or further than one hop into it.
func TestAnOpenFolderTakesOneNameAtATime(t *testing.T) {
	c := regtest.New(t)
	regtest.Program(t, filepath.Join(c.Bin, "tool"))
	regtest.WriteFile(t, filepath.Join(c.Home, "notes.txt"), "notes")
	w := NewWalker(c.Study)
	defer w.Close()
	place, err := w.Walk(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer place.Close()
	home := place.Folder()
	for _, name := range []string{"", "..", "bin/tool", "../home", "/etc"} {
		if info, err := home.Lstat(name); err == nil {
			t.Errorf("Lstat(%q) = %v; want it refused", name, info.Name())
		}
		if file, err := home.OpenFile(name, os.O_RDONLY, 0); err == nil {
			file.Close()
			t.Errorf("OpenFile(%q) opened something", name)
		}
		if dir, err := home.OpenDir(name); err == nil {
			dir.Close()
			t.Errorf("OpenDir(%q) opened something", name)
		}
		if target, err := home.Readlink(name); err == nil {
			t.Errorf("Readlink(%q) = %q", name, target)
		}
		if err := home.Mkdir(name, 0o700); err == nil {
			t.Errorf("Mkdir(%q) made something", name)
		}
		if err := home.Remove(name); err == nil {
			t.Errorf("Remove(%q) removed something", name)
		}
		for _, pair := range [][2]string{{name, "x"}, {"notes.txt", name}} {
			if err := home.Rename(pair[0], pair[1]); err == nil {
				t.Errorf("Rename(%q, %q) renamed something", pair[0], pair[1])
			}
		}
	}
	if !regtest.Exists(filepath.Join(c.Home, "notes.txt")) || regtest.Exists(filepath.Join(c.Base, "x")) {
		t.Error("a name that was refused changed something all the same")
	}
	// A link in the folder is looked at, read and never opened through.
	regtest.Symlink(t, "notes.txt", filepath.Join(c.Home, "link"))
	if info, err := home.Lstat("link"); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Lstat of a link = %v, %v; want the link itself", info, err)
	}
	if target, err := home.Readlink("link"); err != nil || target != "notes.txt" {
		t.Errorf("Readlink = %q, %v", target, err)
	}
	if file, err := home.OpenFile("link", os.O_RDONLY, 0); err == nil {
		file.Close()
		t.Error("OpenFile followed a link")
	}
	if dir, err := home.OpenDir("notes.txt"); err == nil {
		dir.Close()
		t.Error("OpenDir opened a file")
	}
	// Nor is a link to a folder: whoever makes a folder and opens it at
	// once (MakeFolder, the configuration folder) would otherwise go where
	// a link put under the name meanwhile leads.
	regtest.Symlink(t, regtest.Mkdir(t, filepath.Join(c.Base, "elsewhere")), filepath.Join(c.Home, "away"))
	if dir, err := home.OpenDir("away"); err == nil {
		dir.Close()
		t.Error("OpenDir followed a link to a folder")
	}
}

// Two files are one file when they are on one device and have one number
// there. The number alone is shared by files on different devices: the top
// folder of every file system has the same one on many of them.
func TestTwoFilesWithOneNumberOnTwoDevicesAreNotTheSame(t *testing.T) {
	looks := map[string]os.FileInfo{}
	for _, path := range []string{"/", "/proc", "/sys", "/dev", "/run", "/tmp", "/home", "/boot", "/var", "/System/Volumes/Data"} {
		if info, err := os.Stat(path); err == nil {
			looks[path] = info
		}
	}
	compared := 0
	for a, one := range looks {
		for b, other := range looks {
			x, _ := identityOf(one)
			y, _ := identityOf(other)
			if a >= b || x.number != y.number || x.device == y.device {
				continue
			}
			compared++
			if same(one, other) {
				t.Errorf("%s and %s have the number %d on two devices, and are taken for one file", a, b, x.number)
			}
		}
	}
	if compared == 0 {
		t.Skip("no two folders here have one number on two devices")
	}
}

// The look an open folder takes at a name says what the os package says of
// the same name: what it is, and which file it is. The walk compares the
// two kinds of look with each other, a folder looked at by its name with
// the same folder once it is open.
func TestALookInAnOpenFolderSaysWhatTheOsPackageSays(t *testing.T) {
	c := regtest.New(t)
	dir := regtest.Mkdir(t, filepath.Join(c.Home, "kinds"))
	regtest.WriteFile(t, filepath.Join(dir, "file"), "four")
	regtest.Program(t, filepath.Join(dir, "program"))
	regtest.Mkdir(t, filepath.Join(dir, "folder"))
	regtest.Symlink(t, "file", filepath.Join(dir, "link"))
	regtest.Symlink(t, filepath.Join(c.Base, "nowhere"), filepath.Join(dir, "dangling"))
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		"setuid": 0o755 | os.ModeSetuid, "setgid": 0o755 | os.ModeSetgid, "private": 0o600, "sticky": 0o700 | os.ModeSticky,
	} {
		path := filepath.Join(dir, name)
		if name == "sticky" {
			regtest.Mkdir(t, path)
		} else {
			regtest.WriteFile(t, path, name)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	w := NewWalker(c.Study)
	defer w.Close()
	compare := func(folder *Dir, path string) {
		t.Helper()
		want, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := folder.Lstat(filepath.Base(path))
		if err != nil {
			t.Fatalf("Lstat(%s): %v", path, err)
		}
		if got.Name() != want.Name() || got.Mode() != want.Mode() || got.IsDir() != want.IsDir() || got.Size() != want.Size() ||
			!got.ModTime().Equal(want.ModTime()) {
			t.Errorf("the look at %s says %v, %d bytes, written %v; the os package says %v, %d bytes, written %v", path, got.Mode(),
				got.Size(), got.ModTime(), want.Mode(), want.Size(), want.ModTime())
		}
		if !same(got, want) || !same(want, got) {
			t.Errorf("the look at %s and the os package's are not of the same file", path)
		}
	}
	place, err := w.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer place.Close()
	names := []string{"file", "program", "folder", "link", "dangling", "pipe", "setuid", "setgid", "private", "sticky"}
	for _, name := range names {
		compare(place.Folder(), filepath.Join(dir, name))
	}
	// A device, where the system keeps them.
	if dev, err := w.Walk("/dev"); err == nil {
		compare(dev.Folder(), "/dev/null")
		dev.Close()
	}

	// No two of them are the same file, whichever kind of look is taken at
	// each; and a folder looked at by its name is the folder once open.
	for _, a := range names {
		for _, b := range names {
			one, err := place.Folder().Lstat(a)
			if err != nil {
				t.Fatal(err)
			}
			other, err := os.Lstat(filepath.Join(dir, b))
			if err != nil {
				t.Fatal(err)
			}
			if got := same(one, other); got != (a == b) {
				t.Errorf("same(%s, %s) = %v", a, b, got)
			}
		}
	}
	looked, err := place.Folder().Lstat("folder")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := place.Folder().OpenDir("folder")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if info, err := opened.Stat("."); err != nil || !same(looked, info) {
		t.Errorf("a folder that was opened is not the folder that was looked at (%v)", err)
	}
	if info, err := place.Folder().Stat("."); err != nil || same(looked, info) {
		t.Errorf("a folder counts as the same file as the folder that holds it (%v)", err)
	}
}

// Lamplight's configuration folder is found, made, read and written below a
// folder the learner may not list.
func TestTheConfigurationFolderIsUsedBelowAFolderThatMayNotBeListed(t *testing.T) {
	c := regtest.New(t)
	regtest.PassThrough(t, c.Base, c.Home)
	folder, err := Open(envOf(c, c.Home))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer folder.Close()
	if folder.Exists() || folder.Path != c.ConfigDir() {
		t.Fatalf("Open on a new computer = %+v, exists %v", folder, folder.Exists())
	}
	if nearest, path := folder.Nearest(); nearest == nil || path != c.Home || !Writable(nearest) {
		t.Errorf("Nearest = %v, %s; want the home folder, which can be written in", nearest, path)
	}
	if err := folder.CanCreate(); err != nil {
		t.Fatalf("CanCreate: %v", err)
	}
	if err := folder.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want, err := os.Stat(c.ConfigDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := folder.Dir().Stat("."); err != nil || !os.SameFile(want, got) || want.Mode().Perm() != 0o700 {
		t.Errorf("the open folder is not %s, made for the learner alone (%v, %v)", c.ConfigDir(), err, want.Mode().Perm())
	}
	if plugins, err := folder.Read(); err != nil || len(plugins) != 0 {
		t.Errorf("Read of a registry that is not there = %v, %v", plugins, err)
	}
	c.WriteRegistry(t, `{"format": 1, "plugins": [{"name": "remote", "url": "http://localhost:8765/mcp"}]}`)
	if plugins, err := folder.Read(); err != nil || len(plugins) != 1 || plugins[0].Name != "remote" {
		t.Errorf("Read = %v, %v; want the plugin that is registered", plugins, err)
	}

	// The folder itself is the learner's. Were it one they may write in and
	// not list, it would still be found, and say that it can be written in.
	regtest.PassThrough(t, c.ConfigDir())
	again, err := Open(envOf(c, c.Home))
	if err != nil {
		t.Fatalf("Open of a configuration folder that may not be listed: %v", err)
	}
	defer again.Close()
	if !again.Exists() || !Writable(again.Dir()) {
		t.Errorf("the configuration folder: exists %v, writable %v", again.Exists(), Writable(again.Dir()))
	}
	if plugins, err := again.Read(); err != nil || len(plugins) != 1 {
		t.Errorf("Read in a folder that may not be listed = %v, %v", plugins, err)
	}
}

// A program is checked, found on PATH and opened to be started below
// folders the learner may not list, the one that holds it included.
func TestAProgramIsCheckedAndOpenedBelowFoldersThatMayNotBeListed(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	plain := filepath.Join(c.Bin, "notes.txt")
	regtest.WriteFile(t, plain, "notes")
	link := regtest.Symlink(t, tool, filepath.Join(c.Home, "links", "tool"))
	inside := regtest.Program(t, filepath.Join(c.Study, "topic", "kb"))
	toInside := regtest.Symlink(t, inside, filepath.Join(c.Bin, "kb"))
	regtest.PassThrough(t, c.Base, c.Home, c.Bin, filepath.Join(c.Home, "links"))

	w := NewWalker(c.Study)
	defer w.Close()
	if err := w.CheckProgram(tool); err != nil {
		t.Errorf("CheckProgram: %v", err)
	}
	for program, want := range map[string]string{"tool": tool, tool: tool, "~/bin/tool": tool, link: link} {
		if got, err := w.ResolveProgram(envOf(c, c.Home), program); err != nil || got != want {
			t.Errorf("ResolveProgram(%q) = %q, %v; want %s", program, got, err, want)
		}
	}
	// What it refuses, it refuses for the same reason as anywhere.
	for path, code := range map[string]string{
		plain:                        CodeInvalidArgument,
		c.Bin:                        CodeInvalidArgument,
		filepath.Join(c.Bin, "gone"): CodeNotFound,
		inside:                       CodeInvalidArgument,
		toInside:                     CodeInvalidArgument,
	} {
		if err := w.CheckProgram(path); CodeOf(err) != code || IsInside(err) != (path == inside || path == toInside) {
			t.Errorf("CheckProgram(%s) = %v; want %s", path, err, code)
		}
	}
	if got, err := w.ResolveProgram(envOf(c, c.Home), "kb"); !IsInside(err) || got != "" {
		t.Errorf("ResolveProgram of a link on PATH into the Study home = %q, %v; want it refused", got, err)
	}

	want, err := os.Stat(tool)
	if err != nil {
		t.Fatal(err)
	}
	holds, err := os.Stat(c.Bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tool, link} {
		program, err := w.OpenProgramIn(path)
		if err != nil {
			t.Fatalf("OpenProgramIn(%s): %v", path, err)
		}
		if got, err := program.File.Stat(); err != nil || !os.SameFile(want, got) {
			t.Errorf("OpenProgramIn(%s): the open file is not the program (%v)", path, err)
		}
		if got, err := program.Folder.Stat(); err != nil || !os.SameFile(holds, got) || program.Name != "tool" {
			t.Errorf("OpenProgramIn(%s): the folder and name are not the program's: %s (%v)", path, program.Name, err)
		}
		if data, err := io.ReadAll(program.File); err != nil || !strings.HasPrefix(string(data), "#!/bin/sh") {
			t.Errorf("the open program reads %q, %v", data, err)
		}
		program.Close()
	}
	file, err := w.OpenProgram(tool)
	if err != nil {
		t.Fatalf("OpenProgram: %v", err)
	}
	file.Close()

	// An argument is followed through them too.
	script := filepath.Join(c.Study, "topic", "kb")
	data := regtest.Mkdir(t, filepath.Join(c.Home, "data"))
	if i, arg := w.InsideArgument(envOf(c, c.Home), []string{tool, "--data=" + data, "~/data", "--config", script}); i != 4 || arg != script {
		t.Errorf("InsideArgument = %d, %q; want argument 4, the one in the Study home", i, arg)
	}
	if err := w.Unfollowed(envOf(c, c.Home), script); err != nil {
		t.Errorf("Unfollowed of an argument in the Study home = %v; want nil, since it was followed there", err)
	}
}

// A folder is made, for the learner alone, below folders they may not list.
func TestMakeFolderMakesAFolderBelowFoldersThatMayNotBeListed(t *testing.T) {
	c := regtest.New(t)
	state := regtest.Mkdir(t, filepath.Join(c.Home, ".local", "state"))
	regtest.PassThrough(t, c.Base, c.Home, filepath.Join(c.Home, ".local"))
	w := NewWalker(c.Study)
	defer w.Close()
	want := filepath.Join(state, "lamplight", "knowledge-base-plugins", "notes")
	for range 2 {
		folder, path, err := w.MakeFolder(want)
		if err != nil {
			t.Fatalf("MakeFolder: %v", err)
		}
		if path != want {
			t.Errorf("MakeFolder gives the path %s; want %s", path, want)
		}
		opened, err := folder.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if named, err := os.Stat(want); err != nil || !os.SameFile(opened, named) || named.Mode().Perm() != 0o700 {
			t.Errorf("the folder returned is not the one at %s, made for the learner alone (%v)", want, err)
		}
		// It is open as a folder of the learner's is: to list, and to change.
		if _, err := folder.Readdirnames(-1); err != nil {
			t.Errorf("the folder returned cannot be listed: %v", err)
		}
		if err := folder.Chmod(0o700); err != nil {
			t.Errorf("the folder returned cannot be changed: %v", err)
		}
		folder.Close()
	}
	// Nothing is made where the agent writes, below such folders either.
	link := regtest.Symlink(t, regtest.Mkdir(t, filepath.Join(c.Study, "topic")), filepath.Join(state, "linked"))
	if folder, _, err := w.MakeFolder(filepath.Join(link, "lamplight")); !IsInside(err) || folder != nil {
		t.Errorf("MakeFolder through a link into the Study home = %v, %v; want it refused", folder, err)
	}
	if regtest.Exists(filepath.Join(c.Study, "topic", "lamplight")) {
		t.Error("a folder was made in the Study home")
	}
}

// A folder the learner may not pass through stops a walk, and nobody takes
// the path for one outside the Study home: every way of asking refuses it.
func TestAWalkThatCannotBeFinishedIsRefusedByWhoeverAsks(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	closed := regtest.Mkdir(t, filepath.Join(c.Home, "closed"))
	program := regtest.Program(t, filepath.Join(closed, "bin", "kb"))
	regtest.Mkdir(t, filepath.Join(closed, ".config", "lamplight"))
	c.Vars["PATH"] = filepath.Join(closed, "bin")
	regtest.Shut(t, closed)
	env := envOf(c, c.Home)

	w := NewWalker(c.Study)
	defer w.Close()
	t.Run("Walk, Follow and Inside", func(t *testing.T) {
		place, err := w.Walk(program)
		if err == nil || IsInside(err) || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("Walk = %v, %v; want it stopped for want of permission", place, err)
		}
		if err := w.Follow(program); err == nil || IsInside(err) {
			t.Errorf("Follow = %v; want an error that is not about the Study home", err)
		}
		if !w.Inside(program) {
			t.Error("a path that cannot be followed counts as outside the Study home")
		}
		// Outside the folder, nothing changed.
		if w.Inside(tool) {
			t.Error("a path that can be followed counts as inside the Study home")
		}
	})
	t.Run("the configuration folder", func(t *testing.T) {
		folder, err := Open(envOf(c.With(map[string]string{"XDG_CONFIG_HOME": filepath.Join(closed, ".config")}), c.Home))
		if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot reach Lamplight's configuration folder") ||
			!strings.Contains(err.Error(), "permission denied") || folder != nil {
			t.Errorf("Open = %v, %v; want failed_precondition, the folder cannot be reached", folder, err)
		}
	})
	t.Run("a program", func(t *testing.T) {
		if err := w.CheckProgram(program); CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "study cannot follow") {
			t.Errorf("CheckProgram = %v; want failed_precondition, the path cannot be followed", err)
		}
		if file, err := w.OpenProgram(program); CodeOf(err) != CodeFailedPrecondition || file != nil {
			t.Errorf("OpenProgram = %v, %v; want failed_precondition", file, err)
		}
		if opened, err := w.OpenProgramIn(program); CodeOf(err) != CodeFailedPrecondition || opened != nil {
			t.Errorf("OpenProgramIn = %v, %v; want failed_precondition", opened, err)
		}
		if got, err := w.ResolveProgram(env, program); CodeOf(err) != CodeFailedPrecondition || got != "" {
			t.Errorf("ResolveProgram by its path = %q, %v; want failed_precondition", got, err)
		}
	})
	t.Run("a program on PATH", func(t *testing.T) {
		// A folder of PATH that cannot be followed gives no program, as it
		// gives a shell none: it is passed over, never taken.
		if got, err := w.ResolveProgram(env, "kb"); CodeOf(err) != CodeNotFound || got != "" {
			t.Errorf("ResolveProgram on PATH = %q, %v; want not_found", got, err)
		}
		later := envOf(c.With(map[string]string{"PATH": filepath.Join(closed, "bin") + string(os.PathListSeparator) + c.Bin}), c.Home)
		if got, err := w.ResolveProgram(later, "tool"); err != nil || got != tool {
			t.Errorf("ResolveProgram of a program later on PATH = %q, %v; want %s", got, err, tool)
		}
	})
	t.Run("a folder to make", func(t *testing.T) {
		folder, path, err := w.MakeFolder(filepath.Join(closed, "state", "lamplight"))
		if err == nil || IsInside(err) || folder != nil || path != "" {
			t.Errorf("MakeFolder = %v, %q, %v; want an error that is not about the Study home", folder, path, err)
		}
	})
	t.Run("an argument", func(t *testing.T) {
		command := []string{tool, "serve", "--config=" + filepath.Join(closed, "kb.toml")}
		i, arg := w.InsideArgument(env, command)
		if i != 2 || arg != command[2] {
			t.Fatalf("InsideArgument = %d, %q; want argument 2, which cannot be followed", i, arg)
		}
		if err := w.Unfollowed(env, arg); err == nil || !errors.Is(err, os.ErrPermission) {
			t.Errorf("Unfollowed = %v; want the reason the path cannot be followed", err)
		}
		// One that can be followed is none.
		if i, arg := w.InsideArgument(env, []string{tool, "--config=" + filepath.Join(c.Home, "kb.toml")}); i != 0 {
			t.Errorf("InsideArgument of an argument outside = %d, %q", i, arg)
		}
	})
}

// A Study home that is not there yet is known by where it will be. When
// that cannot be found, nothing tells what will be inside it, and every
// walk fails.
func TestNothingIsOutsideAStudyHomeWhosePlaceCannotBeFound(t *testing.T) {
	c := regtest.New(t)
	tool := regtest.Program(t, filepath.Join(c.Bin, "tool"))
	// The place of a Study home cannot be found from a path that is not
	// absolute. NewWalker never asks for one; the walk that looks for the
	// place fails the same way when a folder on the way changes under it.
	w := &Walker{studyHome: filepath.Join("later", "study")}
	if w.blind = w.expect(); w.blind == nil || !strings.Contains(w.blind.Error(), "cannot find where the Study home") {
		t.Fatalf("expect = %v; want it to say that the place cannot be found", w.blind)
	}
	if place, err := w.Walk(tool); err == nil || IsInside(err) {
		t.Errorf("Walk = %v, %v; want it to stop", place, err)
	}
	if !w.Inside(tool) {
		t.Error("a path counts as outside a Study home whose place cannot be found")
	}
	if err := w.CheckProgram(tool); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("CheckProgram = %v; want failed_precondition", err)
	}
	if folder, _, err := w.MakeFolder(filepath.Join(c.Home, "state")); err == nil || folder != nil {
		t.Errorf("MakeFolder = %v, %v; want it refused", folder, err)
	}
	if regtest.Exists(filepath.Join(c.Home, "state")) {
		t.Error("a folder was made although nothing could be told of it")
	}
}
