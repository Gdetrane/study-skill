package cli_test

import (
	"encoding/json"
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
// home yet: a file of its own is inside nothing, and the doctor says nothing
// of it.
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

// Where study looks for the file is where Shelf reads it: in the folder
// XDG_CONFIG_HOME names, when that is an absolute path, and not under the
// home folder then. And a file study may not look at is not taken for one
// that is not there.
func TestDoctorLooksForShelfsSettingsWhereShelfReadsThem(t *testing.T) {
	k := newKBComputer(t)
	k.must(t, "topic", "create", "--title", "Linear algebra")
	studyHome := k.env["STUDY_HOME"]
	inside := filepath.Join(studyHome, "shelf.toml")
	if err := os.WriteFile(inside, []byte("format = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := func(folder string) string {
		t.Helper()
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(folder, "shelf.toml")
		if err := os.Symlink(inside, file); err != nil {
			t.Fatal(err)
		}
		return file
	}
	said := func() string {
		t.Helper()
		var out struct {
			Data core.Diagnosis `json:"data"`
		}
		if err := json.Unmarshal([]byte(k.run(t, "doctor", "--json").stdout), &out); err != nil {
			t.Fatal(err)
		}
		for _, f := range out.Data.Findings {
			if f.Name == "shelf_settings" {
				return f.Message
			}
		}
		return ""
	}
	// The configuration folder is elsewhere: a link under the home folder
	// is no settings file of Shelf's, and the one in that folder is.
	elsewhere := filepath.Join(k.home, "xdg")
	k.env["XDG_CONFIG_HOME"] = elsewhere
	under := link(filepath.Join(k.home, ".config", "lamplight"))
	if message := said(); message != "" {
		t.Errorf("with the configuration folder elsewhere, study doctor says of a link under the home folder: %s", message)
	}
	link(filepath.Join(elsewhere, "lamplight"))
	if message := said(); !strings.HasPrefix(message, "Shelf's settings file, $HOME/xdg/lamplight/shelf.toml, is in the Study home") {
		t.Errorf("with the configuration folder elsewhere, study doctor says of Shelf's settings: %q", message)
	}
	// Without the variable it is the one under the home folder.
	delete(k.env, "XDG_CONFIG_HOME")
	if message := said(); !strings.HasPrefix(message, "Shelf's settings file, $HOME/.config/lamplight/shelf.toml, is in the Study home") {
		t.Errorf("study doctor says of Shelf's settings under the home folder: %q", message)
	}
	// A folder study may not look into: whether the file is there is not
	// known, and that is said, not passed over.
	if os.Getuid() == 0 {
		t.Skip("root looks into any folder")
	}
	if err := os.Chmod(filepath.Dir(under), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(under), 0o700) })
	if message := said(); !strings.HasPrefix(message, "study could not follow Shelf's settings file, $HOME/.config/lamplight/shelf.toml") {
		t.Errorf("with a configuration folder study may not look into, study doctor says of Shelf's settings: %q", message)
	}
}
