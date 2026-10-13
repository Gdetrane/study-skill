package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// Shelf's settings file says where Shelf sends a book's text, and with which
// key. Shelf cannot know where the Study home is, so study doctor says when
// the file is one the learner's agent can write: in the Study home, or
// reached through it. A file of its own says nothing, wherever a link of the
// learner's own leads outside the Study home, and neither does none.
func TestDoctorSaysWhenShelfsSettingsLeadIntoTheStudyHome(t *testing.T) {
	s := newShelfComputer(t)
	studyHome := s.env["STUDY_HOME"]
	file := filepath.Join(filepath.Dir(s.registry()), "shelf.toml")
	const text = "format = 1\n"
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target string) {
		t.Helper()
		_ = os.Remove(file)
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
	}
	quiet := func(when string) {
		t.Helper()
		if f, said := s.findings(t)["shelf_settings"]; said {
			t.Errorf("%s, study doctor says of Shelf's settings: %+v", when, f)
		}
	}
	inside := func(when string) {
		t.Helper()
		f := s.findings(t)["shelf_settings"]
		if f.Status != core.FindingWarn || !strings.Contains(f.Message, "Shelf's settings file, $HOME/.config/lamplight/shelf.toml, is in "+
			"the Study home or is reached through it") || !strings.Contains(f.Message, "your agent") || f.Fix == "" {
			t.Errorf("%s, study doctor says of Shelf's settings: %+v", when, f)
		}
	}

	quiet("with no settings file")
	write(file)
	quiet("with a settings file of its own")
	// Where a learner who keeps their settings in one folder has the file.
	dotfiles := filepath.Join(s.home, "dotfiles", "shelf.toml")
	write(dotfiles)
	link(dotfiles)
	quiet("with a link to a file outside the Study home")

	// A link to a file in the Study home: the agent writes that file.
	write(filepath.Join(studyHome, "settings", "shelf.toml"))
	link(filepath.Join(studyHome, "settings", "shelf.toml"))
	inside("with a link to a file in the Study home")
	if said := s.doctor(t).stdout; !strings.Contains(said, "! shelf_settings") || !strings.Contains(said, "Ready to study") {
		t.Errorf("study doctor prints:\n%s", said)
	}
	// A link that leads nowhere yet: the agent makes the file.
	link(filepath.Join(studyHome, "not-there-yet.toml"))
	inside("with a link to a file the Study home does not hold yet")
	// A link to a file outside, through a folder that is a link into the
	// Study home: by its name the file is nowhere near it.
	if err := os.Symlink(filepath.Join(studyHome, "settings"), filepath.Join(s.home, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	link(filepath.Join(s.home, "elsewhere", "shelf.toml"))
	inside("with a link that leads through a folder into the Study home")

	// A path that cannot be followed has decided nothing: it is not taken
	// for one outside the Study home.
	if os.Getuid() == 0 {
		t.Skip("root passes through any folder")
	}
	locked := filepath.Join(s.home, "locked")
	write(filepath.Join(locked, "shelf.toml"))
	link(filepath.Join(locked, "shelf.toml"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	f := s.findings(t)["shelf_settings"]
	if f.Status != core.FindingWarn || !strings.Contains(f.Message, "study could not follow Shelf's settings file, "+
		"$HOME/.config/lamplight/shelf.toml, to see that it stays outside the Study home") {
		t.Errorf("with a link through a folder study may not pass through, study doctor says of Shelf's settings: %+v", f)
	}
}

// A learner who wrote Shelf's settings before their first Topic has no Study
// home yet: nothing can be inside what is not there, and the doctor says
// nothing of the file.
func TestDoctorSaysNothingOfShelfsSettingsBeforeThereIsAStudyHome(t *testing.T) {
	k := newKBComputer(t)
	file := filepath.Join(filepath.Dir(k.registry()), "shelf.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("format = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(k.env["STUDY_HOME"]); err == nil {
		t.Fatal("this computer has a Study home already, so the test proves nothing")
	}
	r := k.run(t, "doctor", "--json")
	if strings.Contains(r.stdout, "shelf_settings") {
		t.Errorf("with no Study home yet, study doctor says of Shelf's settings:\n%s", r.stdout)
	}
}
