package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// fakeClaude records its arguments and keeps user-scope MCP servers in
// $HOME/.claude.json, as Claude Code does. It never runs the real CLI.
const fakeClaude = `#!/bin/sh
echo "claude $*" >> "$HOME/agent-calls.log"
case "$1 $2" in
"mcp add")
  shift 2
  [ "$1" = "--scope" ] && shift 2
  name=$1; shift
  [ "$1" = "--" ] && shift
  cmd=$1; shift
  args=""
  for a in "$@"; do args="$args${args:+,}\"$a\""; done
  printf '{"mcpServers":{"%s":{"type":"stdio","command":"%s","args":[%s]}}}\n' "$name" "$cmd" "$args" > "$HOME/.claude.json" ;;
"mcp remove")
  printf '{"mcpServers":{}}\n' > "$HOME/.claude.json" ;;
*) exit 2 ;;
esac
`

// fakeCodex records its arguments and keeps one server in a file, answering
// codex mcp get --json as Codex does.
const fakeCodex = `#!/bin/sh
echo "codex $*" >> "$HOME/agent-calls.log"
reg="$HOME/.codex-fake.json"
case "$1 $2" in
"mcp add")
  shift 2
  name=$1; shift
  [ "$1" = "--" ] && shift
  cmd=$1; shift
  args=""
  for a in "$@"; do args="$args${args:+,}\"$a\""; done
  printf '{"name":"%s","enabled":true,"transport":{"type":"stdio","command":"%s","args":[%s]}}\n' "$name" "$cmd" "$args" > "$reg" ;;
"mcp remove") rm -f "$reg" ;;
"mcp get")
  if [ -f "$reg" ]; then cat "$reg"; else echo "Error: No MCP server named '$3' found." >&2; exit 1; fi ;;
*) exit 2 ;;
esac
`

// forbiddenAgent stands in for claude and codex on the test process's own
// PATH, which study never uses for agents: anything that looks an agent up
// there finds this, which records the call and fails, instead of the real
// program.
const forbiddenAgent = `#!/bin/sh
echo "$0 $*" >> '%s'
exit 97
`

// startPATH is the PATH the test binary started with. Setup tests resolve the
// few programs they need from it (setupTools), and never run anything with it.
var startPATH = os.Getenv("PATH")

// setupTools are the only programs from the machine that setup tests put on
// a PATH, each linked into the test's own folder: what the fake agents and
// git-backed Topics need. Never claude or codex.
var setupTools = []string{"sh", "cat", "rm", "sleep", "git"}

// setupHome is a temporary HOME whose PATH holds only fake claude, codex and
// study commands (bin/) and links to setupTools (tools/): no real agent can
// run. study sees bin/study as its own binary.
type setupHome struct {
	home, bin string
	env       map[string]string
}

func newSetupHome(t *testing.T, agents ...string) setupHome {
	t.Helper()
	home := t.TempDir()
	h := setupHome{home: home, bin: filepath.Join(home, "bin")}
	writeExec(t, filepath.Join(h.bin, "study"), "#!/bin/sh\n")
	for _, agent := range agents {
		writeExec(t, filepath.Join(h.bin, agent), map[string]string{"claude": fakeClaude, "codex": fakeCodex}[agent])
	}
	tools := h.path("tools")
	if err := os.Mkdir(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range setupTools {
		real, err := lookTool(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	absent := h.path("absent")
	for _, agent := range []string{"claude", "codex"} {
		writeExec(t, filepath.Join(absent, agent), fmt.Sprintf(forbiddenAgent, h.path("forbidden-calls.log")))
	}
	// git, run by the core for Topics, looks programs up on the process's
	// PATH: give it the tools and the failing agents only.
	t.Setenv("PATH", absent+string(os.PathListSeparator)+tools)
	t.Cleanup(func() {
		if data, err := os.ReadFile(h.path("forbidden-calls.log")); err == nil {
			t.Errorf("an agent was looked up outside study's PATH and run:\n%s", data)
		}
	})
	t.Cleanup(cli.SetExecutable(filepath.Join(h.bin, "study")))
	h.env = map[string]string{"HOME": home, "PATH": h.bin + string(os.PathListSeparator) + tools, "TMPDIR": h.path("tmp")}
	h.guard(t)
	return h
}

// guard fails the test unless every PATH it runs with stays inside its own
// folder and claude and codex resolve to its fakes or to nothing: never to
// /usr/bin, ~/.local/bin or the PATH the tests started with.
func (h setupHome) guard(t *testing.T) {
	t.Helper()
	for _, path := range []string{h.env["PATH"], os.Getenv("PATH")} {
		for _, dir := range filepath.SplitList(path) {
			if !inside(h.home, dir) {
				t.Fatalf("PATH holds %s, outside the test's folder %s", dir, h.home)
			}
		}
	}
	for _, agent := range []string{"claude", "codex"} {
		if p, err := exec.LookPath(agent); err == nil && !inside(h.home, p) {
			t.Fatalf("%s resolves to %s, outside the test's fakes", agent, p)
		}
		for _, dir := range filepath.SplitList(h.env["PATH"]) {
			p := filepath.Join(dir, agent)
			if real, err := filepath.EvalSymlinks(p); err == nil && !inside(h.home, real) {
				t.Fatalf("study's PATH finds %s at %s, outside the test's fakes", agent, real)
			}
		}
	}
	entries, _ := os.ReadDir(h.path("tools"))
	for _, e := range entries {
		real, _ := filepath.EvalSymlinks(h.path("tools", e.Name()))
		if base := filepath.Base(real); base == "claude" || base == "codex" {
			t.Fatalf("tools/%s is %s", e.Name(), real)
		}
	}
}

// inside reports whether p is root or below it.
func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && filepath.IsAbs(p) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// lookTool finds a program on the PATH the tests started with.
func lookTool(name string) (string, error) {
	for _, dir := range filepath.SplitList(startPATH) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("setup tests need %s, which is not on PATH", name)
}

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (h setupHome) run(t *testing.T, args ...string) result {
	t.Helper()
	h.guard(t)
	return h.runIn(t, h.home, nil, args...)
}

func (h setupHome) runIn(t *testing.T, dir string, stdin io.Reader, args ...string) result {
	t.Helper()
	h.guard(t)
	return runEnv(t, h.env, dir, stdin, args...)
}

func (h setupHome) path(parts ...string) string {
	return filepath.Join(append([]string{h.home}, parts...)...)
}

func (h setupHome) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(h.path("agent-calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), h.home, "$HOME")), "\n")
}

// tree256 maps every path under root to its content's hash, or its link's
// target, so a test can prove nothing changed.
func tree256(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			dest, _ := os.Readlink(p)
			out[rel] = "-> " + dest
		case d.IsDir():
			out[rel] = "dir"
		default:
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out[rel] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func agentStatus(t *testing.T, data map[string]any, agent string) map[string]any {
	t.Helper()
	for _, a := range data["agents"].([]any) {
		if m := a.(map[string]any); m["agent"] == agent {
			return m
		}
	}
	t.Fatalf("no result for %s in %v", agent, data["agents"])
	return nil
}

func TestSetupInstallsChecksAndRemoves(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	before := tree256(t, h.home)

	got := decodeData(t, h.run(t, "setup", "--json"))
	if got["study"] != "$HOME/bin/study" {
		t.Errorf("study = %v, want the study on PATH", got["study"])
	}
	skill := h.path(".agents", "skills", "lamplight")
	if !exists(filepath.Join(skill, "SKILL.md")) || !exists(filepath.Join(skill, "references", "lesson-loop.md")) {
		t.Fatalf("the skill was not installed in %s", skill)
	}
	if dest, err := os.Readlink(h.path(".claude", "skills", "lamplight")); err != nil || dest != skill {
		t.Errorf("Claude Code's link = %q, %v; want it to point to %s", dest, err, skill)
	}
	wantCalls := []string{
		"claude mcp add --scope user lamplight -- $HOME/bin/study mcp",
		"codex mcp get lamplight --json",
		"codex mcp add lamplight -- $HOME/bin/study mcp",
	}
	if calls := h.calls(t); !slices.Equal(calls, wantCalls) {
		t.Errorf("agent commands:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	for _, agent := range []string{"claude", "codex"} {
		if s := agentStatus(t, got, agent)["status"]; s != "registered" {
			t.Errorf("%s: %v, want registered", agent, s)
		}
	}

	again := decodeData(t, h.run(t, "setup", "--json"))
	if again["skill"].(map[string]any)["status"] != "current" {
		t.Errorf("a second setup changed the skill: %v", again["skill"])
	}
	for _, agent := range []string{"claude", "codex"} {
		if s := agentStatus(t, again, agent)["status"]; s != "current" {
			t.Errorf("second setup, %s: %v, want current", agent, s)
		}
	}
	if n := len(h.calls(t)); n != len(wantCalls)+1 {
		t.Errorf("a second setup ran more than codex mcp get: %v", h.calls(t))
	}

	check := h.run(t, "setup", "--check", "--json")
	if check.code != cli.ExitOK || decodeData(t, check)["up_to_date"] != true {
		t.Errorf("--check after setup: exit %d, %s", check.code, check.stdout)
	}
	if d := h.run(t, "doctor", "--json"); !strings.Contains(d.stdout, `"message": "agents run study from $HOME/bin/study"`) {
		t.Errorf("doctor's setup Finding:\n%s", d.stdout)
	}

	removed := decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if removed["skill"].(map[string]any)["status"] != "removed" {
		t.Errorf("remove: %v", removed["skill"])
	}
	if exists(h.path(".agents")) || exists(h.path(".claude")) {
		t.Error("--remove left the folders setup created")
	}
	if exists(filepath.Join(h.home, ".local", "state", "lamplight", "setup.json")) {
		t.Error("--remove left setup.json")
	}
	calls := h.calls(t)
	if !slices.Contains(calls, "claude mcp remove --scope user lamplight") || !slices.Contains(calls, "codex mcp remove lamplight") {
		t.Errorf("--remove did not unregister: %v", calls)
	}
	after := tree256(t, h.home)
	for p := range after {
		if _, ok := before[p]; !ok && !slices.Contains([]string{"agent-calls.log", ".claude.json", ".local", ".local/state", ".local/state/lamplight"}, p) {
			t.Errorf("--remove left %s", p)
		}
	}
}

func TestSetupNeverTouchesV1OrFoldersItDidNotCreate(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	writeFile(t, h.path(".agents", "skills", "study", "SKILL.md"), "---\nname: study\n---\nv1\n")
	v1 := tree256(t, h.path(".agents", "skills", "study"))
	writeFile(t, h.path(".agents", "skills", "lamplight", "SKILL.md"), "installed with npx skills add\n")
	before := tree256(t, h.home)

	r := h.run(t, "setup", "--json")
	if r.code != cli.ExitError || errorCode(t, r) != "already_exists" {
		t.Errorf("setup over a folder it did not create: exit %d, %s", r.code, r.stdout)
	}
	if after := tree256(t, h.home); !maps.Equal(after, before) {
		t.Errorf("a refused setup changed files:\nbefore %v\nafter  %v", before, after)
	}
	if h.calls(t) != nil {
		t.Errorf("a refused setup ran agent commands: %v", h.calls(t))
	}

	if err := os.RemoveAll(h.path(".agents", "skills", "lamplight")); err != nil {
		t.Fatal(err)
	}
	decodeData(t, h.run(t, "setup", "--json"))
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if got := tree256(t, h.path(".agents", "skills", "study")); !maps.Equal(got, v1) {
		t.Errorf("setup changed the v1 study skill: %v", got)
	}
	if !exists(h.path(".agents", "skills")) {
		t.Error("--remove removed ~/.agents/skills, which it did not create")
	}
}

func TestSetupKeepsFilesChangedByHand(t *testing.T) {
	h := newSetupHome(t, "codex")
	decodeData(t, h.run(t, "setup", "--json"))
	skill := h.path(".agents", "skills", "lamplight")
	writeFile(t, filepath.Join(skill, "SKILL.md"), "my own notes\n")

	// An older version of study wrote lesson-loop.md: setup's record holds
	// the older content's hash, and the file still has it.
	old := "an older lesson loop\n"
	writeFile(t, filepath.Join(skill, "references", "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)

	got := decodeData(t, h.run(t, "setup", "--json"))
	s := got["skill"].(map[string]any)
	if s["status"] != "updated" || !slices.Equal(stringList(s["written"]), []string{"references/lesson-loop.md"}) ||
		!slices.Equal(stringList(s["kept"]), []string{"SKILL.md"}) {
		t.Errorf("setup over a changed skill: %v", s)
	}
	if readText(t, filepath.Join(skill, "SKILL.md")) != "my own notes\n" {
		t.Error("setup replaced a file changed by hand")
	}
	if readText(t, filepath.Join(skill, "references", "lesson-loop.md")) == old {
		t.Error("setup left an older file it wrote")
	}

	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if readText(t, filepath.Join(skill, "SKILL.md")) != "my own notes\n" {
		t.Error("--remove deleted a file changed by hand")
	}
	if exists(filepath.Join(skill, "references")) {
		t.Error("--remove left the files it wrote")
	}
}

// setRecordedHash makes setup.json say setup wrote content at rel.
func setRecordedHash(t *testing.T, h setupHome, rel, content string) {
	t.Helper()
	p := filepath.Join(h.home, ".local", "state", "lamplight", "setup.json")
	var rec map[string]any
	if err := json.Unmarshal([]byte(readText(t, p)), &rec); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	rec["skill"].(map[string]any)["files"].(map[string]any)[rel] = hex.EncodeToString(sum[:])
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(data))
}

func TestSetupDryRunsAndChecksWriteNothing(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	before := tree256(t, h.home)
	dry := decodeData(t, h.run(t, "setup", "--dry-run", "--json"))
	if dry["dry_run"] != true || agentStatus(t, dry, "claude")["status"] != "registered" {
		t.Errorf("dry run: %v", dry)
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitError || decodeData(t, r)["up_to_date"] != false {
		t.Errorf("--check before setup: exit %d, %s", r.code, r.stdout)
	}
	for p, sum := range tree256(t, h.home) {
		if before[p] != sum && p != "agent-calls.log" {
			t.Errorf("a dry run or --check wrote %s", p)
		}
	}
	for _, c := range h.calls(t) {
		if !strings.Contains(c, "mcp get") {
			t.Errorf("a dry run or --check ran %q", c)
		}
	}

	decodeData(t, h.run(t, "setup", "--json"))
	before = tree256(t, h.home)
	for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--check", "--json"}, {"doctor", "--json"}} {
		h.run(t, args...)
	}
	for p, sum := range tree256(t, h.home) {
		if before[p] != sum && p != "agent-calls.log" {
			t.Errorf("a dry run, --check or doctor changed %s", p)
		}
	}
	if r := h.run(t, "setup", "--check", "--remove", "--json"); r.code != cli.ExitUsage {
		t.Errorf("--check with --remove: exit %d", r.code)
	}
}

func TestSetupLeavesClaudeCodeToThePlugin(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))

	// While setup provides Lamplight to Claude Code, the plugin refuses.
	r := h.run(t, "claude-plugin-path", "--json")
	if r.code != cli.ExitError || errorCode(t, r) != "failed_precondition" {
		t.Errorf("claude-plugin-path after setup: exit %d, %s", r.code, r.stdout)
	}

	// Once the plugin is enabled, setup undoes its own Claude Code setup.
	writeFile(t, h.path(".claude", "settings.json"), `{"enabledPlugins": {"lamplight@lamplight": true}}`)
	got := decodeData(t, h.run(t, "setup", "--json"))
	if c := agentStatus(t, got, "claude"); c["status"] != "plugin" || !strings.Contains(c["note"].(string), "removed its MCP registration and its skill link") {
		t.Errorf("claude with the plugin: %v", c)
	}
	if exists(h.path(".claude", "skills", "lamplight")) {
		t.Error("setup kept its Claude Code skill link next to the plugin")
	}
	if !slices.Contains(h.calls(t), "claude mcp remove --scope user lamplight") {
		t.Errorf("setup kept its Claude Code registration next to the plugin: %v", h.calls(t))
	}
	if c := agentStatus(t, got, "codex"); c["status"] != "current" {
		t.Errorf("codex: %v", c)
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
		t.Errorf("--check with the plugin: exit %d, %s", r.code, r.stdout)
	}

	// Now the plugin can be written.
	plugin := h.run(t, "claude-plugin-path")
	if plugin.code != cli.ExitOK || strings.Count(plugin.stdout, "\n") != 1 {
		t.Fatalf("claude-plugin-path: exit %d, %q, %s", plugin.code, plugin.stdout, plugin.stderr)
	}
	dir := strings.Replace(strings.TrimSpace(plugin.stdout), "$HOME", h.home, 1)
	for _, f := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "skills/lamplight/SKILL.md"} {
		if !exists(filepath.Join(dir, f)) {
			t.Errorf("the plugin has no %s", f)
		}
	}
	if !strings.Contains(readText(t, filepath.Join(dir, ".claude-plugin", "plugin.json")), `"command": "`+filepath.Join(h.bin, "study")+`"`) {
		t.Error("the plugin's MCP server does not run study by its absolute path")
	}
}

func TestSetupReregistersAStudyThatMoved(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))

	moved := h.path("newbin")
	writeFile(t, filepath.Join(moved, "study"), "#!/bin/sh\n# upgraded\n")
	if err := os.Chmod(filepath.Join(moved, "study"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cli.SetExecutable(filepath.Join(moved, "study")))
	h.env["PATH"] = moved + string(os.PathListSeparator) + h.env["PATH"]

	check := h.run(t, "setup", "--check", "--json")
	if check.code != cli.ExitError || !strings.Contains(check.stdout, "but this study is $HOME/newbin/study") {
		t.Errorf("--check after study moved: exit %d, %s", check.code, check.stdout)
	}
	got := decodeData(t, h.run(t, "setup", "--json"))
	for _, agent := range []string{"claude", "codex"} {
		if s := agentStatus(t, got, agent)["status"]; s != "re-registered" {
			t.Errorf("%s: %v, want re-registered", agent, s)
		}
	}
	if !slices.Contains(h.calls(t), "claude mcp add --scope user lamplight -- $HOME/newbin/study mcp") {
		t.Errorf("claude was not re-registered: %v", h.calls(t))
	}
}

func TestSetupSkipsMissingAgentsAndRegistrationsItDidNotMake(t *testing.T) {
	h := newSetupHome(t, "claude")
	writeFile(t, h.path(".claude.json"), `{"mcpServers": {"lamplight": {"type": "stdio", "command": "study", "args": ["mcp"]}}}`)
	got := decodeData(t, h.run(t, "setup", "--json"))
	if c := agentStatus(t, got, "claude"); c["status"] != "kept" {
		t.Errorf("claude with a registration made by hand: %v", c)
	}
	if c := agentStatus(t, got, "codex"); c["status"] != "skipped" {
		t.Errorf("codex not installed: %v", c)
	}
	if h.calls(t) != nil {
		t.Errorf("setup ran agent commands: %v", h.calls(t))
	}
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if !strings.Contains(readText(t, h.path(".claude.json")), `"lamplight"`) {
		t.Error("--remove removed a registration setup did not make")
	}
}

func TestStudyMCPRefreshesTheSkillSetupInstalled(t *testing.T) {
	h := newSetupHome(t, "codex")
	decodeData(t, h.run(t, "setup", "--json"))
	skill := h.path(".agents", "skills", "lamplight")
	old := "an older lesson loop\n"
	writeFile(t, filepath.Join(skill, "references", "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)
	writeFile(t, filepath.Join(skill, "SKILL.md"), "my own notes\n")

	h.env["STUDY_HOME"] = h.path("study")
	if r := h.run(t, "mcp"); r.code != cli.ExitOK || r.stdout != "" {
		t.Fatalf("study mcp: exit %d, stdout %q, stderr %s", r.code, r.stdout, r.stderr)
	}
	if readText(t, filepath.Join(skill, "references", "lesson-loop.md")) == old {
		t.Error("study mcp left an older skill file that setup wrote")
	}
	if readText(t, filepath.Join(skill, "SKILL.md")) != "my own notes\n" {
		t.Error("study mcp replaced a file changed by hand")
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
		t.Errorf("--check after the refresh: exit %d, %s", r.code, r.stdout)
	}
}

func TestSetupRecordDamagedOrNewer(t *testing.T) {
	h := newSetupHome(t)
	p := filepath.Join(h.home, ".local", "state", "lamplight", "setup.json")
	writeFile(t, p, "{not json")
	if r := h.run(t, "setup", "--json"); errorCode(t, r) != "corrupt" {
		t.Errorf("damaged setup.json: %s", r.stdout)
	}
	writeFile(t, p, `{"format": 9, "registrations": []}`)
	if r := h.run(t, "setup", "--remove", "--json"); errorCode(t, r) != "newer_format" {
		t.Errorf("newer setup.json: %s", r.stdout)
	}
	if readText(t, p) != `{"format": 9, "registrations": []}` {
		t.Error("study rewrote a newer setup.json")
	}
}

func TestSessionStartHookPrintsStatusOnlyInTheStudyHome(t *testing.T) {
	h := newSetupHome(t)
	study := h.path("study")
	h.env["STUDY_HOME"] = study
	if r := h.run(t, "topic", "create", "--title", "Linear algebra"); r.code != cli.ExitOK {
		t.Fatalf("topic create: %s", r.stderr)
	}
	input := func(cwd string) *strings.Reader {
		return strings.NewReader(`{"session_id":"s","hook_event_name":"SessionStart","source":"startup","cwd":"` + cwd + `"}`)
	}
	in := h.runIn(t, h.home, input(filepath.Join(study, "linear-algebra")), "claude-hook", "session-start")
	if in.code != cli.ExitOK || !strings.Contains(in.stdout, "linear-algebra") || !strings.Contains(in.stdout, "Lamplight") {
		t.Errorf("inside the Study home: exit %d, %q", in.code, in.stdout)
	}
	outside := h.runIn(t, h.home, input(h.home), "claude-hook", "session-start")
	if outside.code != cli.ExitOK || outside.stdout != "" {
		t.Errorf("outside the Study home: exit %d, %q", outside.code, outside.stdout)
	}
	garbage := h.runIn(t, h.home, strings.NewReader("not json"), "claude-hook", "session-start")
	if garbage.code != cli.ExitOK {
		t.Errorf("unreadable input: exit %d, %s", garbage.code, garbage.stderr)
	}
	if strings.Contains(h.run(t, "--help").stdout, "claude-hook") {
		t.Error("the hook command shows in help")
	}
}

func TestSetupHumanOutput(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	golden(t, "setup.txt", h.run(t, "setup").stdout)
	golden(t, "setup_check.txt", h.run(t, "setup", "--check").stdout)
	golden(t, "setup_remove.txt", h.run(t, "setup", "--remove").stdout)
}
