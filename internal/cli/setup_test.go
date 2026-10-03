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
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// fakeClaude records its arguments and keeps user-scope MCP servers in
// $HOME/.claude.json, as Claude Code does. It never runs the real CLI. With
// $HOME/fail-claude it fails to add or remove a server.
const fakeClaude = `#!/bin/sh
echo "claude $*" >> "$HOME/agent-calls.log"
case "$1 $2" in
"mcp add")
  [ -f "$HOME/fail-claude" ] && { echo "fake claude failure" >&2; exit 1; }
  shift 2
  [ "$1" = "--scope" ] && shift 2
  name=$1; shift
  [ "$1" = "--" ] && shift
  cmd=$1; shift
  args=""
  for a in "$@"; do args="$args${args:+,}\"$a\""; done
  printf '{"mcpServers":{"%s":{"type":"stdio","command":"%s","args":[%s]}}}\n' "$name" "$cmd" "$args" > "$HOME/.claude.json" ;;
"mcp remove")
  [ -f "$HOME/fail-claude" ] && { echo "fake claude failure" >&2; exit 1; }
  printf '{"mcpServers":{}}\n' > "$HOME/.claude.json" ;;
*) exit 2 ;;
esac
`

// fakeCodex records its arguments and keeps one server in a file, answering
// codex mcp get --json as Codex does. Files in $HOME change how it behaves:
// codex-stderr-noise prints a warning on stderr, as Codex 0.160 does;
// codex-garbage answers mcp get with something that is not JSON;
// codex-hang starts a child that sleeps and waits for it; fail-codex fails
// to add or remove a server.
const fakeCodex = `#!/bin/sh
echo "codex $*" >> "$HOME/agent-calls.log"
[ -f "$HOME/codex-stderr-noise" ] && echo "WARNING: proceeding, even though we could not create PATH aliases" >&2
if [ -f "$HOME/codex-hang" ]; then
  sleep 600 &
  echo $! > "$HOME/sleeper.pid"
  wait
fi
reg="$HOME/.codex-fake.json"
case "$1 $2" in
"mcp add")
  [ -f "$HOME/fail-codex" ] && { echo "fake codex failure" >&2; exit 1; }
  shift 2
  name=$1; shift
  [ "$1" = "--" ] && shift
  cmd=$1; shift
  args=""
  for a in "$@"; do args="$args${args:+,}\"$a\""; done
  printf '{"name":"%s","enabled":true,"transport":{"type":"stdio","command":"%s","args":[%s]}}\n' "$name" "$cmd" "$args" > "$reg" ;;
"mcp remove")
  [ -f "$HOME/fail-codex" ] && { echo "fake codex failure" >&2; exit 1; }
  rm -f "$reg" ;;
"mcp get")
  [ -f "$HOME/codex-garbage" ] && { echo "this is not JSON"; exit 0; }
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

func (h setupHome) skill(parts ...string) string {
	return filepath.Join(append([]string{h.home, ".agents", "skills", "lamplight"}, parts...)...)
}

func (h setupHome) recordPath() string {
	return h.path(".local", "state", "lamplight", "setup.json")
}

// record reads setup.json, or returns nil when there is none.
func (h setupHome) record(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(h.recordPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// failure decodes a failed --json result: its error code, and the data of
// what the command did before it failed, when there is any.
func failure(t *testing.T, r result) (string, map[string]any) {
	t.Helper()
	var env struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || env.OK || r.code != cli.ExitError {
		t.Fatalf("not a failed JSON result (exit %d): %v\n%s%s", r.code, err, r.stdout, r.stderr)
	}
	return env.Error.Code, env.Data
}

// leftovers lists the paths in after that were not in before, apart from
// what agents and study keep for themselves.
func leftovers(before, after map[string]string) []string {
	ignore := []string{"agent-calls.log", ".claude.json", ".codex-fake.json", ".local", ".local/state",
		".local/state/lamplight", ".local/state/lamplight/setup.lock"}
	var out []string
	for p := range after {
		if _, ok := before[p]; !ok && !slices.Contains(ignore, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func TestSetupInstallsChecksAndRemoves(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	before := tree256(t, h.home)

	got := decodeData(t, h.run(t, "setup", "--json"))
	if got["study"] != "$HOME/bin/study" || got["study_note"] != nil {
		t.Errorf("study = %v (%v), want the study on PATH", got["study"], got["study_note"])
	}
	if !exists(h.skill("SKILL.md")) || !exists(h.skill("references", "lesson-loop.md")) {
		t.Fatalf("the skill was not installed in %s", h.skill())
	}
	if dest, err := os.Readlink(h.path(".claude", "skills", "lamplight")); err != nil || dest != h.skill() {
		t.Errorf("Claude Code's link = %q, %v; want it to point to %s", dest, err, h.skill())
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
	rec := h.record(t)
	if skill := rec["skill"].(map[string]any); skill["real"] == nil || len(skill["parents"].([]any)) != 2 {
		t.Errorf("the record does not say which folders setup created, and where they led: %v", skill)
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

	check := decodeData(t, h.run(t, "setup", "--check", "--json"))
	if check["up_to_date"] != true || check["study"] != "$HOME/bin/study" {
		t.Errorf("--check after setup: %v", check)
	}
	for _, key := range []string{"skill", "agents", "manual", "dry_run", "remove"} {
		if _, ok := check[key]; ok {
			t.Errorf("--check --json has %q, which only setup and --remove report", key)
		}
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
	if exists(h.recordPath()) {
		t.Error("--remove left setup.json")
	}
	calls := h.calls(t)
	if !slices.Contains(calls, "claude mcp remove --scope user lamplight") || !slices.Contains(calls, "codex mcp remove lamplight") {
		t.Errorf("--remove did not unregister: %v", calls)
	}
	if left := leftovers(before, tree256(t, h.home)); left != nil {
		t.Errorf("--remove left %v", left)
	}
}

func TestSetupNeverTouchesV1OrFoldersItDidNotCreate(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	writeFile(t, h.path(".agents", "skills", "study", "SKILL.md"), "---\nname: study\n---\nv1\n")
	v1 := tree256(t, h.path(".agents", "skills", "study"))
	writeFile(t, h.skill("SKILL.md"), "installed with npx skills add\n")
	before := tree256(t, h.home)

	r := h.run(t, "setup", "--json")
	if code, _ := failure(t, r); code != "already_exists" {
		t.Errorf("setup over a folder it did not create: %s", r.stdout)
	}
	if after := tree256(t, h.home); !maps.Equal(after, before) {
		t.Errorf("a refused setup changed files:\nbefore %v\nafter  %v", before, after)
	}
	if h.calls(t) != nil {
		t.Errorf("a refused setup ran agent commands: %v", h.calls(t))
	}

	if err := os.RemoveAll(h.skill()); err != nil {
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
	writeFile(t, h.skill("SKILL.md"), "my own notes\n")

	// An older version of study wrote lesson-loop.md: setup's record holds
	// the older content's hash, and the file still has it.
	old := "an older lesson loop\n"
	writeFile(t, h.skill("references", "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)

	got := decodeData(t, h.run(t, "setup", "--json"))
	s := got["skill"].(map[string]any)
	if s["status"] != "updated" || !slices.Equal(stringList(s["written"]), []string{"references/lesson-loop.md"}) ||
		!slices.Equal(stringList(s["kept"]), []string{"SKILL.md"}) {
		t.Errorf("setup over a changed skill: %v", s)
	}
	if readText(t, h.skill("SKILL.md")) != "my own notes\n" {
		t.Error("setup replaced a file changed by hand")
	}
	if readText(t, h.skill("references", "lesson-loop.md")) == old {
		t.Error("setup left an older file it wrote")
	}

	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if readText(t, h.skill("SKILL.md")) != "my own notes\n" {
		t.Error("--remove deleted a file changed by hand")
	}
	if exists(h.skill("references")) {
		t.Error("--remove left the files it wrote")
	}
}

// setRecordedHash makes setup.json say setup wrote content at rel.
func setRecordedHash(t *testing.T, h setupHome, rel, content string) {
	t.Helper()
	rec := h.record(t)
	rec["skill"].(map[string]any)["files"].(map[string]any)[rel] = sha(content)
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, h.recordPath(), string(data))
}

func TestSetupDryRunsAndChecksWriteNothing(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	before := tree256(t, h.home)
	dry := decodeData(t, h.run(t, "setup", "--dry-run", "--json"))
	if dry["dry_run"] != true || agentStatus(t, dry, "claude")["status"] != "registered" {
		t.Errorf("dry run: %v", dry)
	}
	r := h.run(t, "setup", "--check", "--json")
	if code, data := failure(t, r); code != "unhealthy" || data["up_to_date"] != false || len(data["findings"].([]any)) != 3 {
		t.Errorf("--check before setup: %s", r.stdout)
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
	for _, args := range [][]string{{"--remove"}, {"--dry-run"}, {"--force"}} {
		if r := h.run(t, append([]string{"setup", "--check", "--json"}, args...)...); r.code != cli.ExitUsage {
			t.Errorf("--check with %s: exit %d", args[0], r.code)
		}
	}
}

func TestSetupDryRunsDecideAsTheRealRun(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	same := func(name string, dryArgs, args []string) {
		t.Helper()
		dry := decodeData(t, h.run(t, dryArgs...))
		real := decodeData(t, h.run(t, args...))
		dry["dry_run"] = false
		delete(real, "note")
		delete(dry, "note")
		if !reflect.DeepEqual(dry, real) {
			d, _ := json.MarshalIndent(dry, "", "  ")
			r, _ := json.MarshalIndent(real, "", "  ")
			t.Errorf("%s: the dry run said\n%s\nbut the real run did\n%s", name, d, r)
		}
	}
	same("setup", []string{"setup", "--dry-run", "--json"}, []string{"setup", "--json"})
	same("--remove --agent codex", []string{"setup", "--remove", "--agent", "codex", "--dry-run", "--json"},
		[]string{"setup", "--remove", "--agent", "codex", "--json"})
	same("--remove", []string{"setup", "--remove", "--dry-run", "--json"}, []string{"setup", "--remove", "--json"})
}

func TestSetupLeavesClaudeCodeToThePlugin(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))

	// While setup provides Lamplight to Claude Code, the plugin refuses.
	r := h.run(t, "claude-plugin-path", "--json")
	if r.code != cli.ExitError || errorCode(t, r) != "failed_precondition" {
		t.Errorf("claude-plugin-path after setup: exit %d, %s", r.code, r.stdout)
	}

	// Enabled by hand, as claude plugin enable would: --check and doctor
	// see both, and setup undoes its own Claude Code setup.
	writeFile(t, h.path(".claude", "settings.json"), `{"enabledPlugins": {"lamplight@lamplight": true}}`)
	if code, data := failure(t, h.run(t, "setup", "--check", "--json")); code != "unhealthy" ||
		!strings.Contains(finding(t, data, "setup:claude")["message"].(string), "both the Claude Code plugin and study setup") {
		t.Errorf("--check with both: %v", data)
	}
	got := decodeData(t, h.run(t, "setup", "--json"))
	if c := agentStatus(t, got, "claude"); c["status"] != "plugin" ||
		!strings.Contains(c["note"].(string), "$HOME/.claude/settings.json") ||
		!strings.Contains(c["note"].(string), "removed its MCP registration and its skill link") {
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

func TestSetupSeesThePluginOnlyWhereItIsEnabled(t *testing.T) {
	for _, c := range []struct {
		name     string
		files    map[string]string
		dir      string
		inPlugin bool
	}{
		{name: "disabled for the user",
			files: map[string]string{".claude/settings.json": `{"enabledPlugins": {"lamplight@lamplight": false}}`}},
		{name: "another marketplace's plugin named lamplight",
			files: map[string]string{".claude/settings.json": `{"enabledPlugins": {"lamplight@someone-elses-themes": true}}`}},
		{name: "enabled in the project setup runs in", dir: "proj/src", inPlugin: true,
			files: map[string]string{"proj/.claude/settings.json": `{"enabledPlugins": {"lamplight@lamplight": true}}`}},
		{name: "enabled in the project, disabled in its local settings", dir: "proj",
			files: map[string]string{
				"proj/.claude/settings.json":       `{"enabledPlugins": {"lamplight@lamplight": true}}`,
				"proj/.claude/settings.local.json": `{"enabledPlugins": {"lamplight@lamplight": false}}`,
			}},
		{name: "enabled for the user, disabled in the project", dir: "proj", inPlugin: true,
			files: map[string]string{
				".claude/settings.json":      `{"enabledPlugins": {"lamplight@lamplight": true}}`,
				"proj/.claude/settings.json": `{"enabledPlugins": {"lamplight@lamplight": false}}`,
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newSetupHome(t, "claude")
			for name, content := range c.files {
				writeFile(t, h.path(name), content)
			}
			dir := h.path(c.dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			got := agentStatus(t, decodeData(t, h.runIn(t, dir, nil, "setup", "--dry-run", "--json")), "claude")
			if want := map[bool]string{true: "plugin", false: "registered"}[c.inPlugin]; got["status"] != want {
				t.Errorf("claude: %v, want %s", got, want)
			}
		})
	}
}

func TestSetupReregistersAStudyThatMoved(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))

	moved := h.path("newbin")
	writeExec(t, filepath.Join(moved, "study"), "#!/bin/sh\n# upgraded\n")
	t.Cleanup(cli.SetExecutable(filepath.Join(moved, "study")))
	h.env["PATH"] = moved + string(os.PathListSeparator) + h.env["PATH"]

	check := h.run(t, "setup", "--check", "--json")
	if _, data := failure(t, check); !strings.Contains(finding(t, data, "setup:codex")["message"].(string), "but this study is $HOME/newbin/study") {
		t.Errorf("--check after study moved: exit %d, %s", check.code, check.stdout)
	}
	got := decodeData(t, h.run(t, "setup", "--json"))
	for _, agent := range []string{"claude", "codex"} {
		if s := agentStatus(t, got, agent)["status"]; s != "re_registered" {
			t.Errorf("%s: %v, want re_registered", agent, s)
		}
	}
	if !slices.Contains(h.calls(t), "claude mcp add --scope user lamplight -- $HOME/newbin/study mcp") {
		t.Errorf("claude was not re-registered: %v", h.calls(t))
	}
}

func TestSetupRegistersStudyByAPathThatLasts(t *testing.T) {
	h := newSetupHome(t, "codex")
	studyOf := func(args ...string) map[string]any {
		t.Helper()
		return decodeData(t, h.run(t, append([]string{"setup", "--dry-run", "--json"}, args...)...))
	}

	// Homebrew: the binary lives in a versioned folder, linked from bin.
	cellar := h.path("Cellar", "study", "1.0", "bin", "study")
	writeExec(t, cellar, "#!/bin/sh\n# 1.0\n")
	if err := os.Remove(h.path("bin", "study")); err != nil {
		t.Fatal(err)
	}
	symlink(t, cellar, h.path("bin", "study"))
	t.Cleanup(cli.SetExecutable(cellar))
	if got := studyOf(); got["study"] != "$HOME/bin/study" || got["study_note"] != nil {
		t.Errorf("a study linked from PATH: %v (%v), want its PATH entry", got["study"], got["study_note"])
	}

	// mise in shim mode: the study the shell runs is a shim.
	shims := h.path(".local", "share", "mise", "shims")
	writeExec(t, filepath.Join(shims, "study"), "#!/bin/sh\nexec mise x -- study \"$@\"\n")
	h.env["PATH"] = shims + string(os.PathListSeparator) + h.env["PATH"]
	if got := studyOf(); got["study"] != "$HOME/.local/share/mise/shims/study" ||
		!strings.Contains(fmt.Sprint(got["study_note"]), "mise's shim") {
		t.Errorf("a study behind a mise shim: %v (%v)", got["study"], got["study_note"])
	}

	// mise with its installs folder on PATH: the shim outlives the version.
	installed := h.path(".local", "share", "mise", "installs", "study", "1.0", "bin")
	writeExec(t, filepath.Join(installed, "study"), "#!/bin/sh\n# mise 1.0\n")
	t.Cleanup(cli.SetExecutable(filepath.Join(installed, "study")))
	h.env["PATH"] = installed + string(os.PathListSeparator) + h.path("tools")
	if got := studyOf(); got["study"] != "$HOME/.local/share/mise/shims/study" {
		t.Errorf("a study in mise's installs folder: %v, want the shim", got["study"])
	}

	// go run builds in a temporary folder that is soon gone.
	calls := len(h.calls(t))
	built := h.path("tmp", "go-build1234", "b001", "exe", "study")
	writeExec(t, built, "#!/bin/sh\n")
	t.Cleanup(cli.SetExecutable(built))
	h.env["PATH"] = h.bin + string(os.PathListSeparator) + h.path("tools")
	if code, _ := failure(t, h.run(t, "setup", "--json")); code != "failed_precondition" {
		t.Errorf("setup from a temporary build folder: %s", code)
	}
	if code, _ := failure(t, h.run(t, "claude-plugin-path", "--json")); code != "failed_precondition" {
		t.Errorf("claude-plugin-path from a temporary build folder: %s", code)
	}
	if exists(h.skill()) || len(h.calls(t)) != calls {
		t.Error("a refused setup changed something")
	}
	if got := decodeData(t, h.run(t, "setup", "--force", "--json")); got["study"] != "$HOME/tmp/go-build1234/b001/exe/study" ||
		!strings.Contains(fmt.Sprint(got["study_note"]), "temporary build folder") {
		t.Errorf("setup --force: %v (%v)", got["study"], got["study_note"])
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

	codexOnly := newSetupHome(t, "codex")
	decodeData(t, codexOnly.run(t, "setup", "--json"))
	if exists(codexOnly.path(".claude")) {
		t.Error("setup created ~/.claude for a Claude Code that is not installed")
	}
}

func TestSetupRemoveLeavesWhatChangedSinceSetup(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))

	// The learner pointed Claude Code's link at their own copy of the skill,
	// and Codex's server at another study.
	mine := h.path("my-lamplight")
	writeFile(t, filepath.Join(mine, "SKILL.md"), "mine\n")
	link := h.path(".claude", "skills", "lamplight")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	symlink(t, mine, link)
	codex := `{"name":"lamplight","enabled":true,"transport":{"type":"stdio","command":"/opt/study","args":["mcp"]}}`
	writeFile(t, h.path(".codex-fake.json"), codex)

	got := decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if c := agentStatus(t, got, "claude"); c["status"] != "removed" || c["link_status"] != "kept" {
		t.Errorf("claude: %v", c)
	}
	if dest, _ := os.Readlink(link); dest != mine {
		t.Errorf("--remove removed a link that no longer leads to the skill (now %q)", dest)
	}
	if c := agentStatus(t, got, "codex"); c["status"] != "kept" {
		t.Errorf("codex: %v", c)
	}
	if slices.Contains(h.calls(t), "codex mcp remove lamplight") || readText(t, h.path(".codex-fake.json")) != codex {
		t.Error("--remove removed a registration that changed since setup made it")
	}
	if exists(h.recordPath()) {
		t.Error("--remove kept a record of what is the learner's now")
	}
}

func TestSetupKeepsTheSkillWhileAnotherAgentUsesIt(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	decodeData(t, h.run(t, "setup", "--json"))
	got := decodeData(t, h.run(t, "setup", "--remove", "--agent", "codex", "--json"))
	if got["skill"].(map[string]any)["status"] != "kept" || !exists(h.skill("SKILL.md")) {
		t.Errorf("--remove --agent codex removed the skill Claude Code still uses: %v", got["skill"])
	}
	got = decodeData(t, h.run(t, "setup", "--remove", "--agent", "claude", "--json"))
	if got["skill"].(map[string]any)["status"] != "removed" || exists(h.skill()) {
		t.Errorf("--remove --agent claude, the last agent, kept the skill: %v", got["skill"])
	}
}

func TestSetupTellsLinksItMadeFromOthers(t *testing.T) {
	h := newSetupHome(t, "claude")
	link := h.path(".claude", "skills", "lamplight")

	// Another skill already sits where Claude Code finds lamplight.
	elsewhere := h.path("elsewhere")
	writeFile(t, filepath.Join(elsewhere, "SKILL.md"), "another\n")
	writeFile(t, h.path(".claude", "skills", ".keep"), "")
	symlink(t, elsewhere, link)
	got := agentStatus(t, decodeData(t, h.run(t, "setup", "--json")), "claude")
	if got["link_status"] != "kept" {
		t.Errorf("a link to another skill: %v", got)
	}
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if dest, _ := os.Readlink(link); dest != elsewhere {
		t.Errorf("--remove removed a link setup did not make (now %q)", dest)
	}

	// npx skills links relatively: one that leads to the skill is current,
	// but still not setup's to remove.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	decodeData(t, h.run(t, "setup", "--json"))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join("..", "..", ".agents", "skills", "lamplight"), link)
	got = agentStatus(t, decodeData(t, h.run(t, "setup", "--json")), "claude")
	if got["link_status"] != "current" || !strings.Contains(got["note"].(string), "did not make it") {
		t.Errorf("a relative link to the skill: %v", got)
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
		t.Errorf("--check with a relative link to the skill: %s", r.stdout)
	}
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if !exists(link) {
		t.Error("--remove removed a link setup did not make")
	}
}

func TestSetupRelinksAndStillRemovesTheFoldersItCreated(t *testing.T) {
	h := newSetupHome(t, "claude")
	before := tree256(t, h.home)
	decodeData(t, h.run(t, "setup", "--json"))
	// The learner deleted the link; setup makes it again.
	if err := os.Remove(h.path(".claude", "skills", "lamplight")); err != nil {
		t.Fatal(err)
	}
	if c := agentStatus(t, decodeData(t, h.run(t, "setup", "--json")), "claude"); c["link_status"] != "linked" {
		t.Errorf("setup after the link was deleted: %v", c)
	}
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if left := leftovers(before, tree256(t, h.home)); left != nil {
		t.Errorf("--remove left %v", left)
	}
}

func TestSetupLeavesFoldersMovedIntoDotfilesAlone(t *testing.T) {
	h := newSetupHome(t, "codex")
	h.env["STUDY_HOME"] = h.path("study")
	decodeData(t, h.run(t, "setup", "--json"))

	// The learner moved references/ into their dotfiles and linked it back,
	// and keeps a note of their own there.
	refs := h.path("dotfiles", "lamplight-refs")
	rename(t, h.skill("references"), refs)
	symlink(t, refs, h.skill("references"))
	writeFile(t, filepath.Join(refs, "my-notes.md"), "my own note\n")
	old := "an older lesson loop\n"
	writeFile(t, filepath.Join(refs, "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)
	dotfiles := tree256(t, h.path("dotfiles"))

	got := decodeData(t, h.run(t, "setup", "--json"))
	if kept := stringList(got["skill"].(map[string]any)["kept"]); !slices.Contains(kept, "references/lesson-loop.md") {
		t.Errorf("setup did not keep files behind the link: %v", got["skill"])
	}
	if r := h.run(t, "mcp"); r.code != cli.ExitOK {
		t.Fatalf("study mcp: %s", r.stderr)
	}
	got = decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if s := got["skill"].(map[string]any); s["status"] != "kept" {
		t.Errorf("--remove: %v", s)
	}
	if after := tree256(t, h.path("dotfiles")); !maps.Equal(after, dotfiles) {
		t.Errorf("setup, study mcp or --remove changed the dotfiles:\nbefore %v\nafter  %v", dotfiles, after)
	}
	if dest, err := os.Readlink(h.skill("references")); err != nil || dest != refs {
		t.Errorf("--remove removed the learner's link: %q, %v", dest, err)
	}
	if exists(h.skill("SKILL.md")) {
		t.Error("--remove kept SKILL.md, which is setup's and unchanged")
	}
}

func TestSetupLeavesAClaudeSkillsFolderLinkedToDotfilesAlone(t *testing.T) {
	h := newSetupHome(t, "claude")
	if err := os.Mkdir(h.path(".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	decodeData(t, h.run(t, "setup", "--json"))
	parents := h.record(t)["claude_skill_link"].(map[string]any)["parents"].([]any)
	if len(parents) != 1 || parents[0].(map[string]any)["path"] != h.path(".claude", "skills") {
		t.Fatalf("setup did not record creating ~/.claude/skills: %v", parents)
	}

	// A dotfiles manager took ~/.claude/skills over, setup's link included.
	skills := h.path("dotfiles", "claude-skills")
	writeFile(t, filepath.Join(skills, "my-skill", "SKILL.md"), "---\n")
	rename(t, h.path(".claude", "skills", "lamplight"), filepath.Join(skills, "lamplight"))
	if err := os.Remove(h.path(".claude", "skills")); err != nil {
		t.Fatal(err)
	}
	symlink(t, skills, h.path(".claude", "skills"))
	dotfiles := tree256(t, h.path("dotfiles"))

	got := decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if c := agentStatus(t, got, "claude"); c["link_status"] != "kept" {
		t.Errorf("claude: %v", c)
	}
	if dest, err := os.Readlink(h.path(".claude", "skills")); err != nil || dest != skills {
		t.Errorf("--remove removed the dotfiles link to ~/.claude/skills: %q, %v", dest, err)
	}
	if after := tree256(t, h.path("dotfiles")); !maps.Equal(after, dotfiles) {
		t.Errorf("--remove changed the dotfiles:\nbefore %v\nafter  %v", dotfiles, after)
	}
}

func TestSetupNeverWorksThroughASymlinkedSkillFolder(t *testing.T) {
	h := newSetupHome(t, "codex")
	h.env["STUDY_HOME"] = h.path("study")
	decodeData(t, h.run(t, "setup", "--json"))
	old := "an older lesson loop\n"
	writeFile(t, h.skill("references", "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)

	// The learner moved the skill folder into their dotfiles and linked it
	// back.
	moved := h.path("dotfiles", "lamplight")
	rename(t, h.skill(), moved)
	symlink(t, moved, h.skill())
	dotfiles := tree256(t, h.path("dotfiles"))

	if code, _ := failure(t, h.run(t, "setup", "--json")); code != "already_exists" {
		t.Errorf("setup through a symlinked skill folder: %s", code)
	}
	if r := h.run(t, "mcp"); r.code != cli.ExitOK {
		t.Fatalf("study mcp: %s", r.stderr)
	}
	if code, _ := failure(t, h.run(t, "setup", "--check", "--json")); code != "unhealthy" {
		t.Errorf("--check with a symlinked skill folder: %s", code)
	}
	got := decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if s := got["skill"].(map[string]any); s["status"] != "kept" {
		t.Errorf("--remove: %v", s)
	}
	if after := tree256(t, h.path("dotfiles")); !maps.Equal(after, dotfiles) {
		t.Errorf("setup, study mcp or --remove changed the dotfiles:\nbefore %v\nafter  %v", dotfiles, after)
	}
	if !exists(h.skill()) {
		t.Error("--remove removed the learner's link to their skill folder")
	}
}

func TestSetupReadsCodexOutputStrictly(t *testing.T) {
	h := newSetupHome(t, "codex")
	// Codex prints warnings on stderr; only stdout is its answer.
	writeFile(t, h.path("codex-stderr-noise"), "")
	decodeData(t, h.run(t, "setup", "--json"))
	if s := agentStatus(t, decodeData(t, h.run(t, "setup", "--json")), "codex")["status"]; s != "current" {
		t.Errorf("codex with a warning on stderr: %v, want current", s)
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
		t.Errorf("--check with a warning on stderr: %s", r.stdout)
	}

	// An answer study cannot read is an error, never a registration made by
	// hand.
	writeFile(t, h.path("codex-garbage"), "")
	rec := readText(t, h.recordPath())
	code, data := failure(t, h.run(t, "setup", "--json"))
	if c := agentStatus(t, data, "codex"); code != "internal" || c["status"] != "failed" || !strings.Contains(c["note"].(string), "cannot read") {
		t.Errorf("setup with unreadable codex output: %s, %v", code, c)
	}
	if code, _ := failure(t, h.run(t, "setup", "--check", "--json")); code != "unhealthy" {
		t.Errorf("--check with unreadable codex output: %s", code)
	}
	if code, data := failure(t, h.run(t, "setup", "--remove", "--json")); code != "internal" || agentStatus(t, data, "codex")["status"] != "failed" {
		t.Errorf("--remove with unreadable codex output: %s, %v", code, data)
	}
	if readText(t, h.recordPath()) != rec {
		t.Error("unreadable codex output changed the record")
	}
	if slices.Contains(h.calls(t), "codex mcp remove lamplight") {
		t.Error("--remove removed a registration it could not read")
	}
}

func TestSetupReportsWhatItDidBeforeAnAgentFailed(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	before := tree256(t, h.home)
	writeFile(t, h.path("fail-codex"), "")
	code, data := failure(t, h.run(t, "setup", "--json"))
	if code != "internal" || data["skill"].(map[string]any)["status"] != "installed" ||
		agentStatus(t, data, "claude")["status"] != "registered" || agentStatus(t, data, "codex")["status"] != "failed" {
		t.Errorf("setup with a failing codex: %s, %v", code, data)
	}
	human := h.run(t, "setup")
	if human.code != cli.ExitError || !strings.Contains(human.stdout, "stopped part-way") ||
		!strings.Contains(human.stdout, "codex   failed") || !strings.Contains(human.stderr, "codex mcp add lamplight") {
		t.Errorf("setup with a failing codex, for people: exit %d\n%s\n%s", human.code, human.stdout, human.stderr)
	}

	if err := os.Remove(h.path("fail-codex")); err != nil {
		t.Fatal(err)
	}
	if s := agentStatus(t, decodeData(t, h.run(t, "setup", "--json")), "codex")["status"]; s != "registered" {
		t.Errorf("setup once codex works: %v", s)
	}
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if left := leftovers(before, tree256(t, h.home)); left != nil {
		t.Errorf("--remove left %v", left)
	}
}

func TestStudyMCPRefreshesTheSkillSetupInstalled(t *testing.T) {
	h := newSetupHome(t, "codex")
	decodeData(t, h.run(t, "setup", "--json"))
	old := "an older lesson loop\n"
	writeFile(t, h.skill("references", "lesson-loop.md"), old)
	setRecordedHash(t, h, "references/lesson-loop.md", old)
	// Stale too, so only the hand edit keeps the refresh away from it.
	writeFile(t, h.skill("SKILL.md"), "my own notes\n")
	setRecordedHash(t, h, "SKILL.md", "an older skill\n")
	// Deleted by the learner: the refresh leaves it deleted.
	if err := os.Remove(h.skill("references", "planning.md")); err != nil {
		t.Fatal(err)
	}

	h.env["STUDY_HOME"] = h.path("study")
	if r := h.run(t, "mcp"); r.code != cli.ExitOK || r.stdout != "" {
		t.Fatalf("study mcp: exit %d, stdout %q, stderr %s", r.code, r.stdout, r.stderr)
	}
	if readText(t, h.skill("references", "lesson-loop.md")) == old {
		t.Error("study mcp left an older skill file that setup wrote")
	}
	if readText(t, h.skill("SKILL.md")) != "my own notes\n" {
		t.Error("study mcp replaced a file changed by hand")
	}
	if exists(h.skill("references", "planning.md")) {
		t.Error("study mcp restored a file the learner deleted")
	}
	r := h.run(t, "setup", "--check", "--json")
	if _, data := failure(t, r); !strings.Contains(finding(t, data, "setup:skill")["message"].(string), "1 file of the skill") ||
		finding(t, data, "setup:skill")["fix"] != "study setup" {
		t.Errorf("--check with a deleted file: %s", r.stdout)
	}
	decodeData(t, h.run(t, "setup", "--json"))
	if !exists(h.skill("references", "planning.md")) {
		t.Error("setup did not restore a file it wrote")
	}
	if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
		t.Errorf("--check after setup: %s", r.stdout)
	}
}

func TestSetupRemoveAndTheSkillRefreshNeverInterleave(t *testing.T) {
	h := newSetupHome(t, "codex")
	h.env["STUDY_HOME"] = h.path("study")
	old := "an older lesson loop\n"
	for round := range 15 {
		decodeData(t, h.run(t, "setup", "--json"))
		writeFile(t, h.skill("references", "lesson-loop.md"), old)
		setRecordedHash(t, h, "references/lesson-loop.md", old)
		done := make(chan result)
		go func() { done <- runEnv(t, h.env, h.home, nil, "mcp") }()
		removed := h.run(t, "setup", "--remove", "--json")
		mcp := <-done
		if removed.code != cli.ExitOK || mcp.code != cli.ExitOK {
			t.Fatalf("round %d: --remove exit %d, study mcp exit %d\n%s%s", round, removed.code, mcp.code, removed.stdout, mcp.stderr)
		}
		if exists(h.skill()) || exists(h.recordPath()) {
			t.Fatalf("round %d: study mcp refreshed the skill while --remove removed it: skill %v, record %v",
				round, exists(h.skill()), exists(h.recordPath()))
		}
	}
}

func TestSetupRecordDamagedOrNewer(t *testing.T) {
	h := newSetupHome(t)
	p := h.recordPath()
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

func TestSetupRefusesARecordNamingWhatItNeverCreates(t *testing.T) {
	h := newSetupHome(t, "codex")
	writeFile(t, h.path("Documents", "thesis", "chapter1.md"), "my thesis\n")
	if err := os.MkdirAll(h.path("Documents", "empty-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.path("dotfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, h.path("dotfiles"), h.path(".vim-link"))
	fill := strings.NewReplacer("@HOME@", h.home, "@SKILL@", h.skill(), "@SUM@", sha("my thesis\n")).Replace
	for name, rec := range map[string]string{
		"a file outside the skill folder": `{"format":1,"skill":{"dir":"@SKILL@","real":"@SKILL@",` +
			`"files":{"../../../Documents/thesis/chapter1.md":"@SUM@"}},"registrations":[]}`,
		"another skill folder": `{"format":1,"skill":{"dir":"@HOME@/Documents/thesis","real":"@HOME@/Documents/thesis",` +
			`"files":{"chapter1.md":"@SUM@"}},"registrations":[]}`,
		"a folder it never creates": `{"format":1,"skill":{"dir":"@SKILL@","real":"@SKILL@","files":{},` +
			`"parents":[{"path":"@HOME@/Documents/empty-project","real":"@HOME@/Documents/empty-project"}]},"registrations":[]}`,
		"a link it never makes": `{"format":1,"claude_skill_link":{"path":"@HOME@/.vim-link","target":"@HOME@/dotfiles",` +
			`"parent":"@HOME@"},"registrations":[]}`,
		"a registration it never makes": `{"format":1,"registrations":[{"agent":"codex","name":"lamplight",` +
			`"command":"study","args":["mcp"]}]}`,
	} {
		writeFile(t, h.recordPath(), fill(rec))
		before := tree256(t, h.home)
		for _, args := range [][]string{{"setup", "--remove", "--dry-run", "--json"}, {"setup", "--remove", "--json"}, {"setup", "--json"}} {
			if code, _ := failure(t, h.run(t, args...)); code != "corrupt" {
				t.Errorf("%s, %v: %s, want corrupt", name, args, code)
			}
		}
		after := tree256(t, h.home)
		if left := leftovers(before, after); left != nil {
			t.Errorf("%s: left %v", name, left)
		}
		for p, sum := range before {
			if after[p] != sum {
				t.Errorf("%s: changed %s", name, p)
			}
		}
	}
	if h.calls(t) != nil {
		t.Errorf("a refused record ran agent commands: %v", h.calls(t))
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
	// Input past the limit is read to the end, so the writer never meets a
	// closed pipe, and ignored.
	huge := strings.NewReader(`{"cwd":"` + study + `","pad":"` + strings.Repeat("x", 3<<20) + `"}`)
	if r := h.runIn(t, h.home, huge, "claude-hook", "session-start"); r.code != cli.ExitOK || r.stdout != "" || huge.Len() != 0 {
		t.Errorf("input past the limit: exit %d, %q, %d bytes unread", r.code, r.stdout, huge.Len())
	}
	if strings.Contains(h.run(t, "--help").stdout, "claude-hook") {
		t.Error("the hook command shows in help")
	}
}

func TestSetupHumanOutput(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	golden(t, "setup_dry_run.txt", h.run(t, "setup", "--dry-run").stdout)
	golden(t, "setup.txt", h.run(t, "setup").stdout)
	golden(t, "setup_check.txt", h.run(t, "setup", "--check").stdout)
	golden(t, "setup_remove.txt", h.run(t, "setup", "--remove").stdout)
}

// A learner who relies on the Claude Code plugin alone, without Codex, is
// set up: doctor says the plugin provides Lamplight, and asks for nothing.
func TestDoctorAcceptsThePluginAlone(t *testing.T) {
	h := newSetupHome(t, "claude")
	writeFile(t, h.path(".claude", "settings.json"), `{"enabledPlugins": {"lamplight@lamplight": true}}`)
	got := finding(t, decodeData(t, h.run(t, "doctor", "--json")), "setup")
	if got["status"] != "ok" || got["message"] != "the Claude Code plugin provides Lamplight" {
		t.Errorf("doctor's setup Finding with the plugin alone = %v", got)
	}
}
