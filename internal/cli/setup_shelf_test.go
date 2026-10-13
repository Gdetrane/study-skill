package cli_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// shelf puts a study-shelf at path that speaks the contract: a script whose
// interpreter is the test plugin, which is this test binary. No test starts
// a study-shelf that is installed on the computer.
func shelfAt(t *testing.T, path string) string {
	t.Helper()
	writeExec(t, path, "#!"+plugintest.Command()[0]+" "+plugintest.Arg+"\n")
	return path
}

// registry is the Knowledge base plugin registry in the test's home.
func (h setupHome) registry() string {
	return h.path(".config", "lamplight", "knowledge-base-plugins.json")
}

// registered is what the registry holds under each name: the command, its
// words divided by spaces, or the URL. It is empty when there is no registry.
func (h setupHome) registered(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	data, err := os.ReadFile(h.registry())
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Plugins []struct {
			Name    string   `json:"name"`
			Command []string `json:"command"`
			URL     string   `json:"url"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%s: %v\n%s", h.registry(), err, data)
	}
	for _, p := range file.Plugins {
		out[p.Name] = strings.ReplaceAll(strings.Join(p.Command, " ")+p.URL, h.home, "$HOME")
	}
	return out
}

// shelfOf is what a result of study setup says of Shelf.
func shelfOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	got, ok := data["shelf"].(map[string]any)
	if !ok {
		t.Fatalf("the result says nothing of Shelf: %v", data)
	}
	return got
}

// With study-shelf installed beside study, study setup registers it as the
// Knowledge base plugin shelf and says so, --check and study doctor agree,
// and --remove takes out exactly that registration. The dry runs say what
// the runs then do, and write nothing.
func TestSetupRegistersShelfWhenItIsInstalled(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	// The learner has a plugin of their own under another name.
	writeExec(t, filepath.Join(h.bin, "notes-kb"), "#!/bin/sh\nexit 0\n")
	decodeData(t, h.run(t, "knowledge-base", "add", "notes", "--json", "--", "notes-kb", "--stdio"))
	before := tree256(t, h.home)

	// Nothing is registered yet: --check says setup has that to do.
	code, data := failure(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, data, "setup:shelf"); code != "unhealthy" || f["status"] != "warn" || f["fix"] != "study setup" ||
		f["message"] != "$HOME/bin/study-shelf is installed, and Shelf is not registered as a Knowledge base plugin" {
		t.Errorf("--check before setup says of Shelf: %v", f)
	}
	dry := shelfOf(t, decodeData(t, h.run(t, "setup", "--dry-run", "--json")))
	if dry["status"] != "registered" || dry["program"] != "$HOME/bin/study-shelf" || dry["name"] != "shelf" {
		t.Errorf("the dry run says of Shelf: %v", dry)
	}
	for p, sum := range tree256(t, h.home) {
		if before[p] != sum && p != "agent-calls.log" {
			t.Errorf("--check or a dry run wrote %s", p)
		}
	}

	got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json")))
	if !maps.Equal(got, dry) {
		t.Errorf("setup did %v with Shelf, and the dry run said %v", got, dry)
	}
	if !strings.Contains(got["note"].(string), "study topic update <topic> --knowledge-base shelf") {
		t.Errorf("setup does not say how a Topic comes to use Shelf: %v", got["note"])
	}
	want := map[string]string{"notes": "$HOME/bin/notes-kb --stdio", "shelf": "$HOME/bin/study-shelf"}
	if reg := h.registered(t); !maps.Equal(reg, want) {
		t.Errorf("the registry holds %v, want %v", reg, want)
	}
	if rec, _ := h.record(t)["shelf"].(map[string]any); rec["name"] != "shelf" || rec["program"] != h.path("bin", "study-shelf") {
		t.Errorf("the record says of Shelf: %v", h.record(t)["shelf"])
	}
	// For people, one line says what was done.
	if out := h.run(t, "setup").stdout; !strings.Contains(out, "  shelf   current: Knowledge base plugin shelf runs $HOME/bin/study-shelf\n") {
		t.Errorf("a second setup says of Shelf:\n%s", out)
	}
	if again := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); again["status"] != "current" || again["note"] != nil {
		t.Errorf("a second setup says of Shelf: %v", again)
	}
	check := decodeData(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true || f["status"] != "ok" ||
		f["message"] != "Shelf is registered as Knowledge base plugin shelf: $HOME/bin/study-shelf" {
		t.Errorf("--check after setup: %v", check)
	}
	// study doctor starts it, as it starts every plugin that is registered,
	// and it speaks the contract. The learner's own answers nothing.
	doctor := decodeData(t, h.run(t, "doctor", "--json"))
	if f := finding(t, doctor, "knowledge_base:shelf"); f["status"] != "ok" ||
		f["message"] != "started by $HOME/bin/study-shelf, speaks version 1 of Lamplight's contract" {
		t.Errorf("doctor says of Shelf: %v", f)
	}
	if f := finding(t, doctor, "knowledge_base:notes"); f["status"] != "warn" || f["fix"] != "study knowledge-base check notes" {
		t.Errorf("doctor says of a plugin that is no MCP server: %v", f)
	}
	if f := finding(t, doctor, "setup"); f["status"] != "ok" {
		t.Errorf("doctor's setup Finding: %v", f)
	}

	// --remove takes Shelf's registration out, and leaves the learner's.
	registry := readText(t, h.registry())
	dryRemove := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--dry-run", "--json")))
	if dryRemove["status"] != "removed" || readText(t, h.registry()) != registry {
		t.Errorf("the dry run of --remove says of Shelf %v, or changed the registry", dryRemove)
	}
	removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json")))
	if !maps.Equal(removed, dryRemove) {
		t.Errorf("--remove did %v with Shelf, and the dry run said %v", removed, dryRemove)
	}
	if reg := h.registered(t); !maps.Equal(reg, map[string]string{"notes": "$HOME/bin/notes-kb --stdio"}) {
		t.Errorf("after --remove the registry holds %v, want the learner's own plugin alone", reg)
	}
	if exists(h.recordPath()) {
		t.Error("--remove left setup.json")
	}
	if out := h.run(t, "setup", "--remove").stdout; !strings.Contains(out, "nothing to remove") {
		t.Errorf("--remove again:\n%s", out)
	}
}

// Shelf is no agent's. Undoing one agent's setup leaves it registered, the
// last agent's too: a learner who hands Claude Code over to the plugin keeps
// the Knowledge base their Topics name. Undoing all of setup takes it out.
func TestSetupRemoveForOneAgentLeavesShelfRegistered(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	decodeData(t, h.run(t, "setup", "--json"))
	for _, agent := range []string{"claude", "codex"} {
		for _, args := range [][]string{{"--dry-run"}, nil} {
			got := decodeData(t, h.run(t, append([]string{"setup", "--remove", "--agent", agent, "--json"}, args...)...))
			if s := shelfOf(t, got); s["status"] != "kept" || s["program"] != "$HOME/bin/study-shelf" ||
				s["note"] != "Shelf is no agent's, so it stays registered: study setup --remove without --agent removes it" {
				t.Errorf("--remove --agent %s %v says of Shelf: %v", agent, args, s)
			}
		}
		if reg := h.registered(t); reg["shelf"] != "$HOME/bin/study-shelf" {
			t.Fatalf("--remove --agent %s left the registry holding %v", agent, reg)
		}
	}
	// No agent's setup is left, and the skill went with the last: Shelf's
	// registration is still setup's to undo, and Shelf still starts.
	if exists(h.skill()) || h.record(t)["shelf"] == nil {
		t.Fatalf("after both agents were removed, the skill is there or the record forgot Shelf: %v", h.record(t))
	}
	if f := finding(t, decodeData(t, h.run(t, "doctor", "--json")), "knowledge_base:shelf"); f["status"] != "ok" {
		t.Errorf("doctor says of Shelf: %v", f)
	}
	if out := h.run(t, "setup", "--remove").stdout; !strings.Contains(out, "  shelf   removed Knowledge base plugin shelf from this computer's registry\n") {
		t.Errorf("--remove says:\n%s", out)
	}
	if reg := h.registered(t); len(reg) != 0 || exists(h.recordPath()) {
		t.Errorf("after --remove the registry holds %v, and the record is there: %v", reg, exists(h.recordPath()))
	}
}

// Without study-shelf, study setup registers no Knowledge base plugin and
// says so, makes no registry, and has nothing to do about it.
func TestSetupWithoutShelfRegistersNothing(t *testing.T) {
	h := newSetupHome(t, "codex")
	got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json")))
	if got["status"] != "skipped" || got["program"] != nil ||
		got["note"] != "study-shelf is neither beside study nor on your PATH; run study setup again once it is installed" {
		t.Errorf("setup without study-shelf says of Shelf: %v", got)
	}
	if exists(h.path(".config")) || h.record(t)["shelf"] != nil {
		t.Errorf("setup without study-shelf made a registry, or recorded one: %v", h.record(t))
	}
	check := decodeData(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true || f["status"] != "ok" ||
		f["message"] != "study-shelf is not installed, so Shelf is not registered as a Knowledge base plugin" {
		t.Errorf("--check without study-shelf: %v", check)
	}
	doctor := decodeData(t, h.run(t, "doctor", "--json"))
	if f := finding(t, doctor, "knowledge_base"); f["status"] != "ok" ||
		f["message"] != "no Knowledge base plugin is registered on this computer: study setup registers Shelf when study-shelf is installed" {
		t.Errorf("doctor without a plugin: %v", f)
	}
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "not_installed" {
		t.Errorf("--remove without a registration of Shelf's says: %v", removed)
	}

	// Once it is installed, doctor says what setup has to do, and setup does it.
	decodeData(t, h.run(t, "setup", "--json"))
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	if f := finding(t, decodeData(t, h.run(t, "doctor", "--json")), "setup"); f["status"] != "warn" || f["fix"] != "study setup" ||
		f["message"] != "$HOME/bin/study-shelf is installed, and Shelf is not registered as a Knowledge base plugin" {
		t.Errorf("doctor's setup Finding with study-shelf installed and not registered: %v", f)
	}
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); got["status"] != "registered" {
		t.Errorf("setup, once study-shelf is installed, says of Shelf: %v", got)
	}
}

// study-shelf is taken from beside the study that agents run, where a
// release installs it, and from PATH only when there is none there.
func TestSetupTakesTheShelfBesideStudyBeforeOneOnPATH(t *testing.T) {
	h := newSetupHome(t, "codex")
	other := h.path("other", "bin")
	shelfAt(t, filepath.Join(other, "study-shelf"))
	// The other folder comes first on PATH, and holds no study.
	h.env["PATH"] = other + string(os.PathListSeparator) + h.env["PATH"]
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--dry-run", "--json"))); got["status"] != "registered" ||
		got["program"] != "$HOME/other/bin/study-shelf" {
		t.Errorf("with study-shelf on PATH alone, the dry run says of Shelf: %v", got)
	}
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); got["status"] != "registered" ||
		got["program"] != "$HOME/bin/study-shelf" {
		t.Errorf("with a study-shelf beside study and another first on PATH, setup says of Shelf: %v", got)
	}
	if reg := h.registered(t); reg["shelf"] != "$HOME/bin/study-shelf" {
		t.Errorf("the registry holds %v", reg)
	}
}

// A plugin the learner registered as shelf, to whatever program, is left
// exactly as it is: by setup, which says so, and by --remove.
func TestSetupLeavesAShelfTheLearnerRegisteredAlone(t *testing.T) {
	for name, own := range map[string][]string{
		"another program":                 {"my-shelf", "--mine"},
		"study-shelf with arguments":      {"study-shelf", "--allow-loopback"},
		"the study-shelf setup takes":     {"study-shelf"},
		"a service the learner runs":      {"--url", "http://localhost:8765/mcp"},
		"a study-shelf that is elsewhere": {"other-shelf/study-shelf"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			shelfAt(t, filepath.Join(h.bin, "study-shelf"))
			writeExec(t, filepath.Join(h.bin, "my-shelf"), "#!/bin/sh\nexit 0\n")
			shelfAt(t, h.path("other-shelf", "study-shelf"))
			add := []string{"knowledge-base", "add", "shelf", "--json"}
			if own[0] == "--url" {
				add = append(add, own...)
			} else {
				add = append(append(add, "--"), own...)
			}
			decodeData(t, h.run(t, add...))
			registry := readText(t, h.registry())
			runs := h.registered(t)["shelf"]

			for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}, {"setup", "--json"}} {
				got := shelfOf(t, decodeData(t, h.run(t, args...)))
				if got["status"] != "kept" || !strings.Contains(got["note"].(string), "a Knowledge base plugin named shelf is registered "+
					"already, not by study setup, so it is left alone (it runs "+runs+")") {
					t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), got)
				}
				if now := readText(t, h.registry()); now != registry {
					t.Fatalf("study %s changed the registry:\n%s\nwas:\n%s", strings.Join(args, " "), now, registry)
				}
			}
			if h.record(t)["shelf"] != nil {
				t.Errorf("setup recorded a registration it did not make: %v", h.record(t)["shelf"])
			}
			if out := h.run(t, "setup").stdout; !strings.Contains(out, "  shelf   kept\n") || !strings.Contains(out, "so it is left alone") {
				t.Errorf("for people, setup says of Shelf:\n%s", out)
			}
			check := decodeData(t, h.run(t, "setup", "--check", "--json"))
			if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true ||
				f["message"] != "a Knowledge base plugin named shelf is registered, not by study setup" {
				t.Errorf("--check: %v", check)
			}
			if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "not_installed" {
				t.Errorf("--remove says of Shelf: %v", removed)
			}
			if now := readText(t, h.registry()); now != registry {
				t.Errorf("--remove changed the registry:\n%s\nwas:\n%s", now, registry)
			}
		})
	}
}

// The learner's own shelf is theirs whatever study-shelf this computer has:
// none, or one setup would refuse to register. Setup has nothing to do
// about Shelf then, and says so without failing.
func TestSetupLeavesTheLearnersShelfWhateverIsInstalled(t *testing.T) {
	for name, installed := range map[string]func(t *testing.T, h setupHome){
		"no study-shelf": func(*testing.T, setupHome) {},
		"a study-shelf inside the Study home": func(t *testing.T, h setupHome) {
			real := shelfAt(t, h.path("study", "linear-algebra", "study-shelf"))
			symlink(t, real, filepath.Join(h.bin, "study-shelf"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			writeExec(t, filepath.Join(h.bin, "my-shelf"), "#!/bin/sh\nexit 0\n")
			decodeData(t, h.run(t, "knowledge-base", "add", "shelf", "--json", "--", "my-shelf"))
			installed(t, h)
			registry := readText(t, h.registry())
			for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
				got := shelfOf(t, decodeData(t, h.run(t, args...)))
				if got["status"] != "kept" || !strings.Contains(got["note"].(string), "so it is left alone (it runs $HOME/bin/my-shelf)") {
					t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), got)
				}
			}
			check := decodeData(t, h.run(t, "setup", "--check", "--json"))
			if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true || f["status"] != "ok" {
				t.Errorf("--check: %v", check)
			}
			if now := readText(t, h.registry()); now != registry {
				t.Errorf("setup changed the learner's registration:\n%s\nwas:\n%s", now, registry)
			}
		})
	}
}

// The learner replaces the registration setup made with one of their own:
// setup leaves it from then on, and --remove does not take it out.
func TestSetupLeavesAShelfTheLearnerRegisteredSince(t *testing.T) {
	replaced := func(t *testing.T) (h setupHome, registry string) {
		t.Helper()
		h = newSetupHome(t, "codex")
		shelfAt(t, filepath.Join(h.bin, "study-shelf"))
		decodeData(t, h.run(t, "setup", "--json"))
		decodeData(t, h.run(t, "knowledge-base", "add", "shelf", "--replace", "--json", "--", "study-shelf", "--allow-loopback"))
		return h, readText(t, h.registry())
	}

	// --remove while setup's record still names the registration it made:
	// the one that is there is no longer that one.
	h, registry := replaced(t)
	for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}} {
		removed := shelfOf(t, decodeData(t, h.run(t, args...)))
		if removed["status"] != "kept" || removed["note"] != "its registration changed since study setup made it, so it is left alone" {
			t.Errorf("study %s says of a registration the learner replaced: %v", strings.Join(args, " "), removed)
		}
		if now := readText(t, h.registry()); now != registry {
			t.Fatalf("study %s changed the learner's registration:\n%s\nwas:\n%s", strings.Join(args, " "), now, registry)
		}
	}
	// And one the learner removed is gone, which is no error.
	h, _ = replaced(t)
	decodeData(t, h.run(t, "knowledge-base", "remove", "shelf", "--json"))
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "already_gone" {
		t.Errorf("--remove says of a registration the learner removed: %v", removed)
	}

	// Setup first: it sees the learner's own, and no longer calls it its own.
	h, registry = replaced(t)
	got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json")))
	if got["status"] != "kept" || !strings.Contains(got["note"].(string), "(it runs $HOME/bin/study-shelf --allow-loopback)") {
		t.Errorf("setup says of a registration the learner replaced: %v", got)
	}
	if h.record(t)["shelf"] != nil {
		t.Errorf("setup still records the registration as its own: %v", h.record(t)["shelf"])
	}
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "not_installed" {
		t.Errorf("--remove says of Shelf: %v", removed)
	}
	if now := readText(t, h.registry()); now != registry {
		t.Errorf("setup or --remove changed the learner's registration:\n%s\nwas:\n%s", now, registry)
	}
}

// When study and study-shelf move, as to another install folder, setup
// registers Shelf where it is now: the registration it replaces is its own.
func TestSetupReregistersAShelfThatMoved(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	decodeData(t, h.run(t, "setup", "--json"))

	moved := h.path("newbin")
	writeExec(t, filepath.Join(moved, "study"), "#!/bin/sh\n# upgraded\n")
	shelfAt(t, filepath.Join(moved, "study-shelf"))
	t.Cleanup(cli.SetExecutable(filepath.Join(moved, "study")))
	h.env["PATH"] = moved + string(os.PathListSeparator) + h.env["PATH"]

	_, data := failure(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, data, "setup:shelf"); f["status"] != "warn" || f["fix"] != "study setup" ||
		f["message"] != "Knowledge base plugin shelf runs $HOME/bin/study-shelf, but study-shelf is now $HOME/newbin/study-shelf" {
		t.Errorf("--check after study-shelf moved: %v", f)
	}
	got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json")))
	if got["status"] != "re_registered" || got["program"] != "$HOME/newbin/study-shelf" ||
		got["note"] != "study-shelf moved from $HOME/bin/study-shelf" {
		t.Errorf("setup after study-shelf moved says of Shelf: %v", got)
	}
	if reg := h.registered(t); reg["shelf"] != "$HOME/newbin/study-shelf" {
		t.Errorf("the registry holds %v", reg)
	}
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "removed" {
		t.Errorf("--remove says of Shelf: %v", removed)
	}
	if reg := h.registered(t); len(reg) != 0 {
		t.Errorf("after --remove the registry holds %v", reg)
	}
}

// A study-shelf inside the Study home, where the agent can write, is held to
// the rule study knowledge-base add holds a program to: setup refuses to
// register it, says why, and does the rest of its work.
func TestSetupRefusesAShelfInsideTheStudyHome(t *testing.T) {
	for name, place := range map[string]func(t *testing.T, h setupHome){
		"beside a study that is inside the Study home": func(t *testing.T, h setupHome) {
			// The registry itself is outside it, in the home folder.
			h.env["STUDY_HOME"] = h.bin
		},
		"on PATH, in a folder of a Topic": func(t *testing.T, h setupHome) {
			// The one beside study is gone, and PATH has one in a Topic.
			if err := os.Remove(filepath.Join(h.bin, "study-shelf")); err != nil {
				t.Fatal(err)
			}
			tools := h.path("study", "linear-algebra", "tools")
			shelfAt(t, filepath.Join(tools, "study-shelf"))
			h.env["PATH"] += string(os.PathListSeparator) + tools
		},
		"beside study, through a link into the Study home": func(t *testing.T, h setupHome) {
			real := shelfAt(t, h.path("study", "linear-algebra", "study-shelf"))
			if err := os.Remove(filepath.Join(h.bin, "study-shelf")); err != nil {
				t.Fatal(err)
			}
			symlink(t, real, filepath.Join(h.bin, "study-shelf"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			shelfAt(t, filepath.Join(h.bin, "study-shelf"))
			place(t, h)
			for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
				r := h.run(t, args...)
				code, data := failure(t, r)
				got := shelfOf(t, data)
				if code != "failed_precondition" || got["status"] != "failed" ||
					!strings.Contains(got["note"].(string), "study setup did not register Shelf as a Knowledge base plugin") ||
					!strings.Contains(got["note"].(string), "study-shelf is inside the Study home") {
					t.Errorf("study %s: %s, and of Shelf %v; want it refused for being inside the Study home", strings.Join(args, " "), code, got)
				}
				// The agent is set up all the same.
				if s := agentStatus(t, data, "codex")["status"]; s != "registered" && s != "current" {
					t.Errorf("study %s: codex is %v", strings.Join(args, " "), s)
				}
				if reg := h.registered(t); len(reg) != 0 {
					t.Errorf("study %s registered %v", strings.Join(args, " "), reg)
				}
				if h.record(t)["shelf"] != nil {
					t.Errorf("study %s recorded a registration of Shelf's: %v", strings.Join(args, " "), h.record(t)["shelf"])
				}
			}
			// For people: what was done, then why Shelf was not registered.
			r := h.run(t, "setup")
			if r.code != cli.ExitError || !strings.Contains(r.stdout, "  shelf   failed\n") ||
				!strings.Contains(r.stderr, "study setup did not register Shelf as a Knowledge base plugin") {
				t.Errorf("for people: exit %d\n%s%s", r.code, r.stdout, r.stderr)
			}
			_, data := failure(t, h.run(t, "setup", "--check", "--json"))
			if f := finding(t, data, "setup:shelf"); f["status"] != "warn" || !strings.Contains(f["message"].(string), "study-shelf is inside the Study home") {
				t.Errorf("--check says of Shelf: %v", f)
			}
		})
	}
}

// A study-shelf that study cannot follow to its end, because it may not pass
// through a folder on the way, may lead anywhere, into the Study home too.
// Setup takes it for no program outside the Study home: it registers
// nothing, and says what stopped it.
func TestSetupRegistersNoShelfItCannotFollow(t *testing.T) {
	h := newSetupHome(t, "codex")
	shut := h.path("shut")
	real := shelfAt(t, filepath.Join(shut, "study-shelf"))
	symlink(t, real, filepath.Join(h.bin, "study-shelf"))
	plugintest.Shut(t, shut)
	for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
		code, data := failure(t, h.run(t, args...))
		if got := shelfOf(t, data); code != "failed_precondition" || got["status"] != "failed" ||
			!strings.Contains(got["note"].(string), "study cannot follow $HOME/bin/study-shelf to check the program") {
			t.Errorf("study %s: %s, and of Shelf %v; want it refused as a path study cannot follow", strings.Join(args, " "), code, got)
		}
		if reg := h.registered(t); len(reg) != 0 {
			t.Errorf("study %s registered %v", strings.Join(args, " "), reg)
		}
	}
	// A link that leads nowhere is no study-shelf at all.
	if err := os.Remove(filepath.Join(h.bin, "study-shelf")); err != nil {
		t.Fatal(err)
	}
	symlink(t, h.path("nowhere", "study-shelf"), filepath.Join(h.bin, "study-shelf"))
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); got["status"] != "skipped" {
		t.Errorf("with a study-shelf that is a link to nothing, setup says of Shelf: %v", got)
	}
	if reg := h.registered(t); len(reg) != 0 {
		t.Errorf("setup registered %v", reg)
	}
}

// A setup.json that names a Knowledge base plugin setup never registers is
// refused, and nothing is removed from the registry for it.
func TestSetupRefusesARecordThatNamesAnotherPlugin(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	writeExec(t, filepath.Join(h.bin, "notes-kb"), "#!/bin/sh\nexit 0\n")
	decodeData(t, h.run(t, "knowledge-base", "add", "notes", "--json", "--", "notes-kb"))
	decodeData(t, h.run(t, "setup", "--json"))
	registry := readText(t, h.registry())
	for _, shelf := range []map[string]any{
		{"name": "notes", "program": h.path("bin", "notes-kb")},
		{"name": "shelf", "program": h.path("bin", "notes-kb")},
		{"name": "shelf", "program": "study-shelf"},
	} {
		rec := h.record(t)
		rec["shelf"] = shelf
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, h.recordPath(), string(data))
		for _, args := range [][]string{{"setup", "--remove", "--json"}, {"setup", "--json"}} {
			if r := h.run(t, args...); errorCode(t, r) != "corrupt" || !strings.Contains(r.stdout, "a Knowledge base plugin study setup never registers") {
				t.Errorf("study %s with a record that names %v: %s", strings.Join(args, " "), shelf, r.stdout)
			}
		}
		if now := readText(t, h.registry()); now != registry {
			t.Errorf("a refused record changed the registry:\n%s", now)
		}
	}
	if !slices.Equal(slices.Sorted(maps.Keys(h.registered(t))), []string{"notes", "shelf"}) {
		t.Errorf("the registry holds %v", h.registered(t))
	}
}
