package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
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

// setupHome is a temporary HOME with fake claude, codex and study commands
// on PATH; study sees bin/study as its own binary.
type setupHome struct {
	home, bin string
	env       map[string]string
}

func newSetupHome(t *testing.T, agents ...string) setupHome {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	for name, script := range map[string]string{"claude": fakeClaude, "codex": fakeCodex, "study": "#!/bin/sh\n"} {
		if name != "study" && !slices.Contains(agents, name) {
			continue
		}
		writeFile(t, filepath.Join(bin, name), script)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(cli.SetExecutable(filepath.Join(bin, "study")))
	return setupHome{home: home, bin: bin, env: map[string]string{
		"HOME": home, "PATH": bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
	}}
}

func (h setupHome) run(t *testing.T, args ...string) result {
	t.Helper()
	return runEnv(t, h.env, h.home, nil, args...)
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
	home := t.TempDir()
	study := filepath.Join(home, "study")
	env := map[string]string{"HOME": home, "STUDY_HOME": study}
	if r := runEnv(t, env, home, nil, "topic", "create", "--title", "Linear algebra"); r.code != cli.ExitOK {
		t.Fatalf("topic create: %s", r.stderr)
	}
	input := func(cwd string) *strings.Reader {
		return strings.NewReader(`{"session_id":"s","hook_event_name":"SessionStart","source":"startup","cwd":"` + cwd + `"}`)
	}
	inside := runEnv(t, env, home, input(filepath.Join(study, "linear-algebra")), "claude-hook", "session-start")
	if inside.code != cli.ExitOK || !strings.Contains(inside.stdout, "linear-algebra") || !strings.Contains(inside.stdout, "Lamplight") {
		t.Errorf("inside the Study home: exit %d, %q", inside.code, inside.stdout)
	}
	outside := runEnv(t, env, home, input(home), "claude-hook", "session-start")
	if outside.code != cli.ExitOK || outside.stdout != "" {
		t.Errorf("outside the Study home: exit %d, %q", outside.code, outside.stdout)
	}
	garbage := runEnv(t, env, home, strings.NewReader("not json"), "claude-hook", "session-start")
	if garbage.code != cli.ExitOK {
		t.Errorf("unreadable input: exit %d, %s", garbage.code, garbage.stderr)
	}
	if strings.Contains(runEnv(t, env, home, nil, "--help").stdout, "claude-hook") {
		t.Error("the hook command shows in help")
	}
}

func TestSetupHumanOutput(t *testing.T) {
	h := newSetupHome(t, "claude", "codex")
	golden(t, "setup.txt", h.run(t, "setup").stdout)
	golden(t, "setup_check.txt", h.run(t, "setup", "--check").stdout)
	golden(t, "setup_remove.txt", h.run(t, "setup", "--remove").stdout)
}
