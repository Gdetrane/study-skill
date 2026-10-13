package cli_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
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

// bySetup is how registered marks the registration that carries study
// setup's mark in the registry.
const bySetup = " (by setup)"

// registered is what the registry holds under each name: the command, its
// words divided by spaces, or the URL, and whether the registration carries
// study setup's mark. It is empty when there is no registry.
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
			BySetup bool     `json:"by_setup"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%s: %v\n%s", h.registry(), err, data)
	}
	for _, p := range file.Plugins {
		out[p.Name] = strings.ReplaceAll(strings.Join(p.Command, " ")+p.URL, h.home, "$HOME")
		if p.BySetup {
			out[p.Name] += bySetup
		}
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
// Knowledge base plugin shelf, with its mark in the registry, and says so.
// --check, the listing and study doctor agree, and --remove takes out
// exactly that registration. The dry runs say what the runs then do, and
// write nothing.
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
	// The registry says which registration is setup's, and setup's record
	// has no word of it.
	want := map[string]string{"notes": "$HOME/bin/notes-kb --stdio", "shelf": "$HOME/bin/study-shelf" + bySetup}
	if reg := h.registered(t); !maps.Equal(reg, want) {
		t.Errorf("the registry holds %v, want %v", reg, want)
	}
	if record := readText(t, h.recordPath()); strings.Contains(record, "shelf") {
		t.Errorf("setup's record speaks of Shelf:\n%s", record)
	}
	// The listing shows the mark, in words and as a field.
	if out := h.run(t, "knowledge-base", "list").stdout; !strings.Contains(out, "shelf  $HOME/bin/study-shelf\n  Registered by study setup, "+
		"which keeps it up to date, and removes it with study setup --remove.\n") || strings.Count(out, "Registered by study setup") != 1 {
		t.Errorf("study knowledge-base list does not say which plugin setup registered:\n%s", out)
	}
	for _, p := range decodeData(t, h.run(t, "knowledge-base", "list", "--json"))["plugins"].([]any) {
		if p := p.(map[string]any); (p["by_setup"] == true) != (p["name"] == "shelf") {
			t.Errorf("study knowledge-base list --json says of %v: by_setup %v", p["name"], p["by_setup"])
		}
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
	if dryRemove["status"] != "removed" || dryRemove["program"] != "$HOME/bin/study-shelf" || readText(t, h.registry()) != registry {
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
// the Knowledge base their Topics name. Undoing all of setup takes it out,
// also once nothing else of setup's is left to undo.
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
		if reg := h.registered(t); reg["shelf"] != "$HOME/bin/study-shelf"+bySetup {
			t.Fatalf("--remove --agent %s left the registry holding %v", agent, reg)
		}
	}
	// No agent's setup is left, and the record went with the last. The
	// registry still says that the registration is setup's, and Shelf still
	// starts.
	if exists(h.skill()) || exists(h.recordPath()) {
		t.Fatal("after both agents were removed, the skill or the record is still there")
	}
	if f := finding(t, decodeData(t, h.run(t, "doctor", "--json")), "knowledge_base:shelf"); f["status"] != "ok" {
		t.Errorf("doctor says of Shelf: %v", f)
	}
	if out := h.run(t, "setup", "--remove", "--dry-run").stdout; !strings.Contains(out, "study setup --remove would undo:\n") ||
		!strings.Contains(out, "  shelf   would remove Knowledge base plugin shelf from this computer's registry\n") {
		t.Errorf("the dry run of --remove says:\n%s", out)
	}
	if out := h.run(t, "setup", "--remove").stdout; !strings.Contains(out, "Removed what study setup did:\n") ||
		!strings.Contains(out, "  shelf   removed Knowledge base plugin shelf from this computer's registry\n") ||
		strings.Contains(out, "has not installed anything") {
		t.Errorf("--remove says:\n%s", out)
	}
	if reg := h.registered(t); len(reg) != 0 {
		t.Errorf("after --remove the registry holds %v", reg)
	}
	if out := h.run(t, "setup", "--remove").stdout; !strings.Contains(out, "nothing to remove") {
		t.Errorf("--remove again:\n%s", out)
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
	if exists(h.path(".config")) {
		t.Error("setup without study-shelf made a registry")
	}
	check := decodeData(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true || f["status"] != "ok" ||
		f["message"] != "study-shelf is not installed, so study setup registers no Knowledge base plugin" {
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
	if reg := h.registered(t); reg["shelf"] != "$HOME/bin/study-shelf"+bySetup {
		t.Errorf("the registry holds %v", reg)
	}
}

// A study on PATH may be a link to the program, as a package manager makes
// one: the study-shelf beside the program itself is Shelf too, when none is
// beside the link.
func TestSetupFindsTheShelfBesideTheProgramALinkLeadsTo(t *testing.T) {
	h := newSetupHome(t, "codex")
	opt := h.path("opt", "lamplight", "bin")
	writeExec(t, filepath.Join(opt, "study"), "#!/bin/sh\n")
	shelfAt(t, filepath.Join(opt, "study-shelf"))
	if err := os.Remove(filepath.Join(h.bin, "study")); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(opt, "study"), filepath.Join(h.bin, "study"))
	t.Cleanup(cli.SetExecutable(filepath.Join(opt, "study")))

	got := decodeData(t, h.run(t, "setup", "--dry-run", "--json"))
	if s := shelfOf(t, got); got["study"] != "$HOME/bin/study" || s["status"] != "registered" ||
		s["program"] != "$HOME/opt/lamplight/bin/study-shelf" {
		t.Errorf("with study-shelf beside the program a link on PATH leads to, the dry run says %v of study and %v of Shelf",
			got["study"], s)
	}
	// One beside the link, where agents run study from, comes first: it is
	// the path that lasts.
	symlink(t, filepath.Join(opt, "study-shelf"), filepath.Join(h.bin, "study-shelf"))
	if s := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); s["status"] != "registered" || s["program"] != "$HOME/bin/study-shelf" {
		t.Errorf("with a link to study-shelf beside the link to study, setup says of Shelf: %v", s)
	}
}

// What has the name study-shelf beside study and is no program, a file the
// learner cannot run or a folder, is passed over as a shell passes it over:
// setup goes on to PATH, and to nothing when PATH has none.
func TestSetupPassesOverAStudyShelfThatIsNoProgram(t *testing.T) {
	for name, put := range map[string]func(t *testing.T, path string){
		"a file that is not executable": func(t *testing.T, path string) { writeFile(t, path, "notes about Shelf\n") },
		"a folder":                      func(t *testing.T, path string) { writeFile(t, filepath.Join(path, "README"), "notes about Shelf\n") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			put(t, filepath.Join(h.bin, "study-shelf"))
			if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--dry-run", "--json"))); got["status"] != "skipped" {
				t.Errorf("with %s beside study and no other, the dry run says of Shelf: %v", name, got)
			}
			other := h.path("other", "bin")
			shelfAt(t, filepath.Join(other, "study-shelf"))
			h.env["PATH"] += string(os.PathListSeparator) + other
			if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); got["status"] != "registered" ||
				got["program"] != "$HOME/other/bin/study-shelf" {
				t.Errorf("with %s beside study and a study-shelf on PATH, setup says of Shelf: %v", name, got)
			}
		})
	}
}

// A plugin the learner registered as shelf, to whatever program, is left
// exactly as it is: by setup, which says so, and by --remove. It has no
// mark, and that alone makes it theirs: the very study-shelf setup would
// register is theirs too. What the note says a URL plugin runs is its
// scheme and host: the rest of an address may be a key.
func TestSetupLeavesAShelfTheLearnerRegisteredAlone(t *testing.T) {
	for name, own := range map[string]struct {
		args []string
		runs string
	}{
		"another program":                 {[]string{"my-shelf", "--mine"}, "$HOME/bin/my-shelf --mine"},
		"study-shelf with arguments":      {[]string{"study-shelf", "--allow-loopback"}, "$HOME/bin/study-shelf --allow-loopback"},
		"the study-shelf setup takes":     {[]string{"study-shelf"}, "$HOME/bin/study-shelf"},
		"a service the learner runs":      {[]string{"--url", "http://localhost:8765/t0ken-in-the-path/mcp?key=s3cr3t"}, "http://localhost:8765"},
		"a study-shelf that is elsewhere": {[]string{"other-shelf/study-shelf"}, "$HOME/other-shelf/study-shelf"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			shelfAt(t, filepath.Join(h.bin, "study-shelf"))
			writeExec(t, filepath.Join(h.bin, "my-shelf"), "#!/bin/sh\nexit 0\n")
			shelfAt(t, h.path("other-shelf", "study-shelf"))
			add := []string{"knowledge-base", "add", "shelf", "--json"}
			if own.args[0] == "--url" {
				add = append(add, own.args...)
			} else {
				add = append(append(add, "--"), own.args...)
			}
			decodeData(t, h.run(t, add...))
			registry := readText(t, h.registry())

			for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}, {"setup", "--json"}} {
				r := h.run(t, args...)
				got := shelfOf(t, decodeData(t, r))
				if got["status"] != "kept" || got["note"] != "a Knowledge base plugin named shelf is registered already, not by "+
					"study setup, so it is left alone (it runs "+own.runs+")" {
					t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), got)
				}
				if strings.Contains(r.stdout, "s3cr3t") || strings.Contains(r.stdout, "t0ken") {
					t.Errorf("study %s shows what may be the key of the learner's service:\n%s", strings.Join(args, " "), r.stdout)
				}
				if now := readText(t, h.registry()); now != registry {
					t.Fatalf("study %s changed the registry:\n%s\nwas:\n%s", strings.Join(args, " "), now, registry)
				}
			}
			if out := h.run(t, "setup").stdout; !strings.Contains(out, "  shelf   kept\n") || !strings.Contains(out, "so it is left alone") ||
				strings.Contains(out, "s3cr3t") {
				t.Errorf("for people, setup says of Shelf:\n%s", out)
			}
			check := decodeData(t, h.run(t, "setup", "--check", "--json"))
			if f := finding(t, check, "setup:shelf"); check["up_to_date"] != true ||
				f["message"] != "a Knowledge base plugin named shelf is registered, not by study setup" {
				t.Errorf("--check: %v", check)
			}
			for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}} {
				if removed := shelfOf(t, decodeData(t, h.run(t, args...))); removed["status"] != "not_installed" {
					t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), removed)
				}
				if now := readText(t, h.registry()); now != registry {
					t.Errorf("study %s changed the registry:\n%s\nwas:\n%s", strings.Join(args, " "), now, registry)
				}
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

// A registration the learner makes over the one setup made is theirs from
// then on: with --replace, by another command, or by registering the very
// command setup registered, which takes the mark away. Setup leaves it, and
// --remove does not take it out. A learner who removes the registration
// has it back at the next setup: a registered plugin does nothing until a
// Topic names it.
func TestALearnersRegistrationOverSetupsIsTheirs(t *testing.T) {
	for name, own := range map[string]struct {
		args []string
		runs string
	}{
		"another command, with --replace":   {[]string{"--replace", "--", "study-shelf", "--allow-loopback"}, "$HOME/bin/study-shelf --allow-loopback"},
		"the same command, registered anew": {[]string{"--", "study-shelf"}, "$HOME/bin/study-shelf"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			shelfAt(t, filepath.Join(h.bin, "study-shelf"))
			decodeData(t, h.run(t, "setup", "--json"))
			// The registration says whose it was, and whose it is now.
			taken := h.run(t, append([]string{"knowledge-base", "add", "shelf"}, own.args...)...)
			if taken.code != cli.ExitOK || !strings.Contains(taken.stdout, "It replaces: $HOME/bin/study-shelf\n") ||
				!strings.Contains(taken.stdout, "study setup had registered that one. This one is yours: study setup leaves it as it is.\n") {
				t.Fatalf("study knowledge-base add over setup's registration: exit %d\n%s%s", taken.code, taken.stdout, taken.stderr)
			}
			registry := readText(t, h.registry())
			if reg := h.registered(t); reg["shelf"] != own.runs {
				t.Fatalf("the registry holds %v; want the learner's own, without setup's mark", reg)
			}
			for _, args := range [][]string{{"setup", "--remove", "--agent", "codex", "--dry-run", "--json"}, {"setup", "--dry-run", "--json"},
				{"setup", "--json"}, {"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}} {
				got := shelfOf(t, decodeData(t, h.run(t, args...)))
				want := "kept"
				if args[1] == "--remove" {
					want = "not_installed"
				}
				if got["status"] != want || want == "kept" && !strings.Contains(got["note"].(string), "(it runs "+own.runs+")") {
					t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), got)
				}
				if now := readText(t, h.registry()); now != registry {
					t.Fatalf("study %s changed the learner's registration:\n%s\nwas:\n%s", strings.Join(args, " "), now, registry)
				}
			}
		})
	}

	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	decodeData(t, h.run(t, "setup", "--json"))
	decodeData(t, h.run(t, "knowledge-base", "remove", "shelf", "--json"))
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--agent", "codex", "--json"))); removed["status"] != "not_installed" {
		t.Errorf("--remove says of a registration the learner removed: %v", removed)
	}
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); got["status"] != "registered" ||
		h.registered(t)["shelf"] != "$HOME/bin/study-shelf"+bySetup {
		t.Errorf("setup after the learner removed Shelf says %v, and the registry holds %v", got, h.registered(t))
	}
}

// setup.json is in the state folder, which may be inside the Study home and
// is above the folder a learner may have opened to an agent's sandbox. A
// record there that calls the learner's shelf setup's own gives setup
// nothing: the registry says whose a registration is, and a record says
// nothing of Shelf that setup believes.
func TestAForgedSetupRecordGivesSetupNothing(t *testing.T) {
	for name, place := range map[string]func(h setupHome) string{
		"in the state folder": func(h setupHome) string { return h.recordPath() },
		"in a state folder inside the Study home": func(h setupHome) string {
			h.env["XDG_STATE_HOME"] = h.path("study", ".state")
			return h.path("study", ".state", "lamplight", "setup.json")
		},
	} {
		for program, registers := range map[string][]string{
			"a build of the learner's own": {"opt/my-build/study-shelf"},
			"the study-shelf setup takes":  {"study-shelf"},
		} {
			t.Run(name+", "+program, func(t *testing.T) {
				h := newSetupHome(t, "codex")
				shelfAt(t, filepath.Join(h.bin, "study-shelf"))
				shelfAt(t, h.path("opt", "my-build", "study-shelf"))
				decodeData(t, h.run(t, append([]string{"knowledge-base", "add", "shelf", "--json", "--"}, registers...)...))
				registry, theirs := readText(t, h.registry()), h.registered(t)["shelf"]
				// What a setup of an earlier day would have recorded, had it
				// registered what the learner registered.
				forged, err := json.Marshal(map[string]any{"format": 1, "registrations": []any{},
					"shelf": map[string]any{"name": "shelf", "program": strings.ReplaceAll(theirs, "$HOME", h.home)}})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, place(h), string(forged))

				for _, args := range [][]string{{"setup", "--check", "--json"}, {"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
					r := h.run(t, args...)
					if now := readText(t, h.registry()); now != registry {
						t.Fatalf("study %s, with a record that claims the learner's shelf, changed the registry:\n%s\nwas:\n%s",
							strings.Join(args, " "), now, registry)
					}
					if args[1] == "--check" {
						continue
					}
					if got := shelfOf(t, decodeData(t, r)); got["status"] != "kept" {
						t.Errorf("study %s says of the learner's shelf: %v", strings.Join(args, " "), got)
					}
				}
				writeFile(t, place(h), string(forged))
				for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}} {
					got := shelfOf(t, decodeData(t, h.run(t, args...)))
					if now := readText(t, h.registry()); now != registry || got["status"] != "not_installed" {
						t.Fatalf("study %s, with a record that claims the learner's shelf, says %v and left the registry as:\n%s\nwas:\n%s",
							strings.Join(args, " "), got, now, registry)
					}
				}
			})
		}
	}
}

// A registration of the learner's reaches the registry between setup's look
// and its write. Setup asked the registry to register Shelf as its own, and
// the registry, under its lock, finds a registration without the mark: it
// is left as it is, the very command setup would register too, and is not
// setup's to remove afterwards.
func TestSetupLeavesWhatTheLearnerRegistersBetweenItsLookAndItsWrite(t *testing.T) {
	for name, own := range map[string][]string{
		"another command":             {"my-shelf", "--mine"},
		"the study-shelf setup takes": {"study-shelf"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHome(t, "codex")
			shelfAt(t, filepath.Join(h.bin, "study-shelf"))
			writeExec(t, filepath.Join(h.bin, "my-shelf"), "#!/bin/sh\nexit 0\n")
			landed := false
			restore := cli.SetBeforeShelfRegister(func() {
				decodeData(t, h.run(t, append([]string{"knowledge-base", "add", "shelf", "--json", "--"}, own...)...))
				landed = true
			})
			// Whatever becomes of this test, no other meets its hook.
			t.Cleanup(restore)
			got := shelfOf(t, decodeData(t, h.run(t, "setup", "--json")))
			restore()
			if !landed {
				t.Fatal("setup did not come to register Shelf, so this test proves nothing")
			}
			want := strings.Join(append([]string{"$HOME/bin/" + own[0]}, own[1:]...), " ")
			if reg := h.registered(t); got["status"] != "kept" || got["program"] != nil || reg["shelf"] != want {
				t.Errorf("setup says %v of Shelf, and the registry holds %v; want the learner's own, %s, kept without setup's mark",
					got, reg, want)
			}
			registry := readText(t, h.registry())
			if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "not_installed" ||
				readText(t, h.registry()) != registry {
				t.Errorf("--remove says %v of Shelf, or changed the learner's registration", removed)
			}
		})
	}
}

// When study and study-shelf move, as to another install folder, setup
// registers Shelf where it is now: the registration it replaces carries its
// mark.
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
	if reg := h.registered(t); reg["shelf"] != "$HOME/newbin/study-shelf"+bySetup {
		t.Errorf("the registry holds %v", reg)
	}
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "removed" {
		t.Errorf("--remove says of Shelf: %v", removed)
	}
	if reg := h.registered(t); len(reg) != 0 {
		t.Errorf("after --remove the registry holds %v", reg)
	}
}

// With study-shelf gone from where setup looks, the registration setup made
// is in the registry all the same. When its program is gone too, --check
// says so, as it does of an agent that runs a study that is gone, setup says
// that the registration remains and what takes it away, and study doctor
// says it once, where it reports the plugin. When the program is still
// there, as after study alone moved, the registration is as good as it was.
func TestSetupSaysWhenItsRegistrationRunsAProgramThatIsGone(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelf := shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	decodeData(t, h.run(t, "setup", "--json"))
	registry := readText(t, h.registry())

	// study moved alone: the study-shelf that is registered is where it was.
	moved := h.path("newbin")
	writeExec(t, filepath.Join(moved, "study"), "#!/bin/sh\n# upgraded\n")
	restore := cli.SetExecutable(filepath.Join(moved, "study"))
	path := h.env["PATH"]
	h.env["PATH"] = moved + string(os.PathListSeparator) + h.path("tools")
	if got := shelfOf(t, decodeData(t, h.run(t, "setup", "--dry-run", "--json"))); got["status"] != "current" ||
		got["program"] != "$HOME/bin/study-shelf" || got["note"] != nil {
		t.Errorf("with study moved and its registration's study-shelf where it was, the dry run says of Shelf: %v", got)
	}
	_, data := failure(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, data, "setup:shelf"); f["status"] != "ok" ||
		f["message"] != "Shelf is registered as Knowledge base plugin shelf: $HOME/bin/study-shelf" {
		t.Errorf("--check with study moved: %v", f)
	}
	restore()
	h.env["PATH"] = path

	if err := os.Remove(shelf); err != nil {
		t.Fatal(err)
	}
	code, data := failure(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, data, "setup:shelf"); code != "unhealthy" || f["status"] != "warn" ||
		f["message"] != "Knowledge base plugin shelf runs $HOME/bin/study-shelf, which is gone" ||
		f["fix"] != "install study-shelf again and run study setup, or run study setup --remove" {
		t.Errorf("--check with study-shelf gone and setup's registration there: %s, %v", code, f)
	}
	for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
		got := shelfOf(t, decodeData(t, h.run(t, args...)))
		if got["status"] != "skipped" || got["program"] != "$HOME/bin/study-shelf" {
			t.Errorf("study %s says of Shelf: %v", strings.Join(args, " "), got)
		}
		for _, want := range []string{"The registration study setup made remains", "runs $HOME/bin/study-shelf, which is gone",
			"study setup --remove, which takes the registration away"} {
			if !strings.Contains(got["note"].(string), want) {
				t.Errorf("study %s does not say %q of Shelf: %v", strings.Join(args, " "), want, got["note"])
			}
		}
		if now := readText(t, h.registry()); now != registry {
			t.Fatalf("study %s changed the registry", strings.Join(args, " "))
		}
	}
	// The doctor says it where it reports the plugin, and not again as
	// something setup has to do.
	doctor := decodeData(t, h.run(t, "doctor", "--json"))
	if f := finding(t, doctor, "knowledge_base:shelf"); f["status"] != "warn" || !strings.Contains(f["message"].(string), "there is no program at") {
		t.Errorf("doctor says of the plugin whose program is gone: %v", f)
	}
	if f := finding(t, doctor, "setup"); f["status"] != "ok" || strings.Contains(f["message"].(string), "gone") {
		t.Errorf("doctor's setup Finding says it a second time: %v", f)
	}
	if removed := shelfOf(t, decodeData(t, h.run(t, "setup", "--remove", "--json"))); removed["status"] != "removed" || len(h.registered(t)) != 0 {
		t.Errorf("--remove says %v of Shelf, and the registry holds %v", removed, h.registered(t))
	}
}

// Shelf is no agent's: it is registered whatever becomes of the skill. A
// skill folder that is not setup's stops setup for the agents, which the
// result says, and Shelf is registered all the same, which it says too.
func TestSetupRegistersShelfWhateverBecomesOfTheSkill(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	writeFile(t, h.skill("SKILL.md"), "installed with npx skills add\n")

	code, data := failure(t, h.run(t, "setup", "--dry-run", "--json"))
	if s := shelfOf(t, data); code != "already_exists" || data["skill"].(map[string]any)["status"] != "kept" ||
		s["status"] != "registered" || len(h.registered(t)) != 0 {
		t.Errorf("the dry run over a skill folder that is not setup's: %s, of the skill %v, of Shelf %v", code, data["skill"], s)
	}
	code, data = failure(t, h.run(t, "setup", "--json"))
	if s := shelfOf(t, data); code != "already_exists" || data["skill"].(map[string]any)["status"] != "kept" ||
		s["status"] != "registered" || s["program"] != "$HOME/bin/study-shelf" || len(data["agents"].([]any)) != 0 {
		t.Errorf("setup over a skill folder that is not setup's: %s, of the skill %v, of Shelf %v, of agents %v",
			code, data["skill"], s, data["agents"])
	}
	if reg := h.registered(t); reg["shelf"] != "$HOME/bin/study-shelf"+bySetup {
		t.Errorf("the registry holds %v; want Shelf registered whatever became of the skill", reg)
	}
	if h.calls(t) != nil || readText(t, h.skill("SKILL.md")) != "installed with npx skills add\n" {
		t.Errorf("setup did something for an agent, or to the skill it did not install: %v", h.calls(t))
	}
	// For people: what was done, and then what stopped setup.
	r := h.run(t, "setup")
	for _, want := range []string{"study setup stopped part-way, with study at $HOME/bin/study, after this:\n",
		"  skill   kept in $HOME/.agents/skills/lamplight\n", "  shelf   current: Knowledge base plugin shelf runs $HOME/bin/study-shelf\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("for people, setup does not say %q:\n%s", want, r.stdout)
		}
	}
	if r.code != cli.ExitError || !strings.Contains(r.stderr, "already exists and study setup did not create it") ||
		strings.Contains(r.stdout, "Start a new conversation") {
		t.Errorf("for people: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	// When Shelf cannot be registered either, the error holds both reasons,
	// and its code is the skill's, which came first.
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	h.env["STUDY_HOME"] = h.bin
	r = h.run(t, "setup", "--json")
	code, data = failure(t, r)
	if s := shelfOf(t, data); code != "already_exists" || s["status"] != "failed" || data["skill"].(map[string]any)["status"] != "kept" ||
		!strings.Contains(r.stdout, "already exists and study setup did not create it") ||
		!strings.Contains(r.stdout, "study setup did not register Shelf as a Knowledge base plugin") {
		t.Errorf("setup with a skill folder that is not its own and a study-shelf in the Study home: %s\n%s", code, r.stdout)
	}
}

// A study-shelf inside the Study home, where the agent can write, is held to
// the rule study knowledge-base add holds a program to: setup refuses to
// register it and exits with 1. It says exactly what was done and what was
// not: the skill and the agents are set up, Shelf is not registered, and
// why is said once.
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
			}
			// For people: Lamplight is set up, Shelf is not registered, and
			// the one error says why.
			r := h.run(t, "setup")
			for _, want := range []string{"Lamplight is set up with study at $HOME/bin/study\n", "  codex   current\n", "  shelf   not registered\n",
				"Start a new conversation in your agent to use Lamplight.\n"} {
				if !strings.Contains(r.stdout, want) {
					t.Errorf("for people, setup does not say %q:\n%s", want, r.stdout)
				}
			}
			if r.code != cli.ExitError || strings.Contains(r.stdout, "stopped part-way") || strings.Contains(r.stdout, "inside the Study home") ||
				strings.Count(r.stderr, "study setup did not register Shelf as a Knowledge base plugin") != 1 ||
				strings.Count(r.stderr, "is inside the Study home") != 1 {
				t.Errorf("for people: exit %d, and why is not said once, after what was done\n%s%s", r.code, r.stdout, r.stderr)
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

// What setup registers lasts no longer than the folder it is in. A PATH is
// whoever started study's to arrange: a temporary folder first on it, with a
// study and a study-shelf in it, gets neither registered unless --force
// says so, whichever way setup came to the path.
func TestSetupRegistersNothingFromATemporaryFolderWithoutForce(t *testing.T) {
	// A folder named as a version manager names its shims, first on PATH:
	// the study agents would run is the one in it.
	t.Run("a study and a study-shelf in a folder of shims", func(t *testing.T) {
		h := newSetupHome(t, "codex")
		shims := h.path("tmp", "x", "shims")
		writeExec(t, filepath.Join(shims, "study"), "#!/bin/sh\n")
		shelfAt(t, filepath.Join(shims, "study-shelf"))
		h.env["PATH"] = shims + string(os.PathListSeparator) + h.env["PATH"]
		for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
			r := h.run(t, args...)
			if errorCode(t, r) != "failed_precondition" || !strings.Contains(r.stdout, "$HOME/tmp/x/shims/study, a temporary build folder") {
				t.Errorf("study %s from a temporary folder first on PATH: %s", strings.Join(args, " "), r.stdout)
			}
		}
		if reg := h.registered(t); len(reg) != 0 || h.calls(t) != nil || exists(h.skill()) {
			t.Errorf("a refused setup registered %v, ran %v, or installed the skill", reg, h.calls(t))
		}
		got := decodeData(t, h.run(t, "setup", "--force", "--json"))
		if s := shelfOf(t, got); got["study"] != "$HOME/tmp/x/shims/study" || s["status"] != "registered" ||
			s["program"] != "$HOME/tmp/x/shims/study-shelf" || !strings.Contains(got["study_note"].(string), "temporary build folder") {
			t.Errorf("setup --force: study %v (%v), Shelf %v", got["study"], got["study_note"], s)
		}
	})
	// study is where it stays, and only study-shelf is in a temporary folder.
	t.Run("a study-shelf in a temporary folder on PATH", func(t *testing.T) {
		h := newSetupHome(t, "codex")
		shelfAt(t, h.path("tmp", "y", "study-shelf"))
		h.env["PATH"] = h.path("tmp", "y") + string(os.PathListSeparator) + h.env["PATH"]
		for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
			code, data := failure(t, h.run(t, args...))
			if s := shelfOf(t, data); code != "failed_precondition" || s["status"] != "failed" ||
				!strings.Contains(s["note"].(string), "$HOME/tmp/y/study-shelf is in a temporary folder, which will soon be gone") ||
				!strings.Contains(s["note"].(string), "--force") {
				t.Errorf("study %s with a study-shelf in a temporary folder on PATH: %s, of Shelf %v", strings.Join(args, " "), code, s)
			}
		}
		if reg := h.registered(t); len(reg) != 0 {
			t.Errorf("setup registered %v from a temporary folder, without --force", reg)
		}
		_, data := failure(t, h.run(t, "setup", "--check", "--json"))
		if f := finding(t, data, "setup:shelf"); f["status"] != "warn" || !strings.Contains(f["message"].(string), "is in a temporary folder") {
			t.Errorf("--check says of Shelf: %v", f)
		}
		if s := shelfOf(t, decodeData(t, h.run(t, "setup", "--force", "--json"))); s["status"] != "registered" ||
			h.registered(t)["shelf"] != "$HOME/tmp/y/study-shelf"+bySetup {
			t.Errorf("setup --force says %v of Shelf, and the registry holds %v", s, h.registered(t))
		}
		// Registered, it is nothing setup has to register again.
		if s := shelfOf(t, decodeData(t, h.run(t, "setup", "--json"))); s["status"] != "current" {
			t.Errorf("setup, once the study-shelf is registered, says of Shelf: %v", s)
		}
	})
	// A link in a folder that stays, to a program in a temporary one, is
	// gone as soon as the program is.
	t.Run("a study-shelf beside study that a link leads to in a temporary folder", func(t *testing.T) {
		h := newSetupHome(t, "codex")
		symlink(t, shelfAt(t, h.path("tmp", "z", "study-shelf")), filepath.Join(h.bin, "study-shelf"))
		code, data := failure(t, h.run(t, "setup", "--json"))
		if s := shelfOf(t, data); code != "failed_precondition" || s["status"] != "failed" ||
			!strings.Contains(s["note"].(string), "$HOME/bin/study-shelf is in a temporary folder") || len(h.registered(t)) != 0 {
			t.Errorf("setup with a link to a study-shelf in a temporary folder: %s, of Shelf %v, and the registry holds %v",
				code, s, h.registered(t))
		}
		if s := shelfOf(t, decodeData(t, h.run(t, "setup", "--force", "--json"))); s["status"] != "registered" ||
			s["program"] != "$HOME/bin/study-shelf" {
			t.Errorf("setup --force says of Shelf: %v", s)
		}
	})
}

// A registry study cannot read keeps setup from registering Shelf, and
// --remove from undoing it: each says so, with the registry's own words,
// does the rest of its work, and leaves the registry as it is. The doctor
// says it once, where it reports the registry.
func TestSetupSaysWhenTheRegistryCannotBeRead(t *testing.T) {
	h := newSetupHome(t, "codex")
	shelfAt(t, filepath.Join(h.bin, "study-shelf"))
	writeFile(t, h.registry(), "{")
	for _, args := range [][]string{{"setup", "--dry-run", "--json"}, {"setup", "--json"}} {
		code, data := failure(t, h.run(t, args...))
		if s := shelfOf(t, data); code != "corrupt" || s["status"] != "failed" ||
			!strings.Contains(s["note"].(string), "study setup did not register Shelf as a Knowledge base plugin: $HOME/.config/lamplight/"+
				"knowledge-base-plugins.json is damaged") || agentStatus(t, data, "codex")["status"] != "registered" {
			t.Errorf("study %s over a damaged registry: %s, of Shelf %v", strings.Join(args, " "), code, s)
		}
	}
	_, data := failure(t, h.run(t, "setup", "--check", "--json"))
	if f := finding(t, data, "setup:shelf"); f["status"] != "warn" ||
		!strings.Contains(f["message"].(string), "study setup cannot register Shelf as a Knowledge base plugin") ||
		!strings.Contains(f["message"].(string), "is damaged") {
		t.Errorf("--check over a damaged registry says of Shelf: %v", f)
	}
	doctor := decodeData(t, h.run(t, "doctor", "--json"))
	if f := finding(t, doctor, "knowledge_base"); f["status"] != "warn" || !strings.Contains(f["message"].(string), "is damaged") {
		t.Errorf("doctor says of a damaged registry: %v", f)
	}
	if f := finding(t, doctor, "setup"); f["status"] != "ok" || strings.Contains(f["message"].(string), "damaged") {
		t.Errorf("doctor's setup Finding says of the registry what its own Finding says: %v", f)
	}

	for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}} {
		code, data := failure(t, h.run(t, args...))
		if s := shelfOf(t, data); code != "corrupt" || s["status"] != "failed" ||
			!strings.Contains(s["note"].(string), "study setup did not remove Shelf from the Knowledge base plugin registry") ||
			agentStatus(t, data, "codex")["status"] != "removed" {
			t.Errorf("study %s over a damaged registry: %s, of Shelf %v", strings.Join(args, " "), code, s)
		}
	}
	if readText(t, h.registry()) != "{" {
		t.Error("setup or --remove wrote a registry it could not read")
	}
	// For people: what was undone, that Shelf was not removed, and why, once.
	if again := h.run(t, "setup", "--json"); errorCode(t, again) != "corrupt" {
		t.Fatalf("setup over a damaged registry: %s", again.stdout)
	}
	r := h.run(t, "setup", "--remove")
	for _, want := range []string{"Removed what study setup did:\n", "  codex   removed", "  shelf   not removed\n"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("for people, --remove does not say %q:\n%s", want, r.stdout)
		}
	}
	if r.code != cli.ExitError || strings.Contains(r.stdout, "stopped part-way") || strings.Contains(r.stdout, "is damaged") ||
		strings.Count(r.stderr, "study setup did not remove Shelf from the Knowledge base plugin registry") != 1 {
		t.Errorf("for people: exit %d, and why is not said once, after what was undone\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// What a Finding of the doctor's holds, and what setup says of Shelf, can be
// a folder's name, a Source's title, a program's path or what a plugin
// wrote. None of it reaches a terminal as it is: an escape sequence in it is
// shown, not obeyed.
func TestWhatTheDoctorAndSetupPrintIsShownNotObeyed(t *testing.T) {
	const clear, title, reorder, bell = "\x1b[2J", "\x1b]0;owned\x07", "\u202e", "\x07beep"
	doctor := cli.RenderDiagnosis(core.Diagnosis{Findings: []core.Finding{{Name: "indexing:a" + clear + "b", Status: core.FindingWarn,
		Message: "not indexed: " + title, Fix: "study source index " + reorder + "x"}}})
	for field, raw := range map[string]string{"name": clear, "message": title, "fix": reorder} {
		if strings.Contains(doctor, raw) {
			t.Errorf("study doctor prints a Finding's %s as it is: %q", field, doctor)
		}
	}
	for _, shown := range []string{`\x1b[2J`, `\x1b]0;owned\a`, `\u202e`} {
		if !strings.Contains(doctor, shown) {
			t.Errorf("study doctor does not show %s: %q", shown, doctor)
		}
	}
	for status, fields := range map[string][2]string{
		"registered": {"/opt/a" + clear + "b/study-shelf", "moved from " + title},
		"current":    {"/opt/a" + clear + "b/study-shelf", ""},
		"kept":       {"", "it runs " + bell},
	} {
		setup := cli.RenderSetupShelf(status, fields[0], fields[1])
		for _, raw := range []string{clear, title, bell} {
			if strings.Contains(setup, raw) {
				t.Errorf("study setup prints what it says of Shelf, %s, as it is: %q", status, setup)
			}
		}
	}
}
