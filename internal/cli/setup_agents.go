package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// executable returns the running study's path. Tests replace it.
var executable = os.Executable

// How long an agent's command may run. Reading a registration is quick;
// adding or removing one may wait on the agent's own start-up. Tests shorten
// them.
var (
	agentReadTimeout  = 10 * time.Second
	agentWriteTimeout = 60 * time.Second
)

// studyLocation is the path agents run study by, and what the learner should
// know about it.
type studyLocation struct {
	Path string
	Note string
	// Temporary is set when Path is in a temporary build folder, such as the
	// one go run builds in: gone soon, so setup refuses it without --force.
	Temporary bool
}

// studyPath returns the path agents should run study by. The running binary
// is often a versioned path that the next upgrade removes (Homebrew's
// Cellar, a version manager's installs folder), while the study on PATH
// (Homebrew's bin link, ~/go/bin, /usr/bin) stays put. So:
//
//   - when the first study on PATH is a version manager's shim (mise, asdf),
//     agents run the shim, which picks the version itself;
//   - when a study on PATH is this same program, its PATH entry is used as
//     written, or the version manager's shim for it when there is one;
//   - otherwise the running binary's real path, with a note.
func (a *app) studyPath() (studyLocation, error) {
	exe, err := executable()
	if err != nil {
		return studyLocation{}, &core.Error{Code: core.CodeInternal, Message: "cannot find the study binary: " + err.Error(), Err: err}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	exeInfo, exeErr := os.Stat(exe)
	first := true
	for _, dir := range filepath.SplitList(a.opts.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		cand := filepath.Join(dir, "study")
		info, err := os.Stat(cand)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		if first {
			if tool := shimTool(dir); tool != "" {
				return studyLocation{Path: cand, Note: shimNote(tool, cand)}, nil
			}
			first = false
		}
		if exeErr == nil && os.SameFile(info, exeInfo) {
			if shim, tool := installShim(cand); shim != "" {
				return studyLocation{Path: shim, Note: shimNote(tool, shim)}, nil
			}
			return studyLocation{Path: cand}, nil
		}
	}
	if a.temporaryPath(exe) {
		return studyLocation{Path: exe, Temporary: true,
			Note: exe + " is in a temporary build folder, which will soon be gone"}, nil
	}
	return studyLocation{Path: exe, Note: "this study is not on your PATH, so agents will run it from " + exe +
		": install study with a package or go install so the path survives upgrades"}, nil
}

func shimNote(tool, shim string) string {
	return "agents will run study through " + tool + "'s shim " + shim +
		", which runs the version " + tool + " picks for the folder the agent starts in"
}

// shimTool names the version manager whose shims folder dir is, such as
// ~/.local/share/mise/shims or ~/.asdf/shims, or returns "".
func shimTool(dir string) string {
	if filepath.Base(dir) != "shims" {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(filepath.Dir(dir)), ".")
}

// installShim returns the version manager's shim for a study inside its
// installs folder, such as ~/.local/share/mise/installs/<tool>/<version>/bin/
// study, when the shim exists.
func installShim(p string) (shim, tool string) {
	for d := filepath.Dir(p); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if filepath.Base(d) != "installs" {
			continue
		}
		shim = filepath.Join(filepath.Dir(d), "shims", "study")
		if info, err := os.Stat(shim); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return shim, shimTool(filepath.Dir(shim))
		}
		return "", ""
	}
	return "", ""
}

// temporaryPath reports whether p is in a temporary folder: a Go build
// folder (go run, go test), or under TMPDIR (/tmp when it is not set).
func (a *app) temporaryPath(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	tmp := a.opts.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	return filepath.IsAbs(tmp) && pathWithin(tmp, p)
}

// lookPath finds an agent's command on the PATH study was given, or "". It
// never uses the process's own PATH, so tests decide what it finds.
func (a *app) lookPath(name string) string {
	for _, dir := range filepath.SplitList(a.opts.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// agentEnv is the environment for an agent's command: study's own, with the
// variables that decide where agents keep their configuration taken from
// study's view of the environment, so tests and wrappers control them.
func (a *app) agentEnv() []string {
	keys := []string{"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(keys, k)
	})
	for _, k := range keys {
		if v := a.opts.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// runAgent runs an agent's command with stdin closed, in a process group of
// its own that is stopped when timeout passes. It returns stdout and stderr
// apart: only stdout is ever parsed.
func (a *app) runAgent(ctx context.Context, timeout time.Duration, bin string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = a.agentEnv()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	agentProcessGroup(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		killAgentGroup(cmd)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("it did not finish within %s, so study stopped it", timeout)
		} else {
			err = ctx.Err()
		}
	}
	return out.Bytes(), errOut.Bytes(), err
}

func (a *app) register(ctx context.Context, bin string, g registrationRecord) error {
	args := []string{"mcp", "add", g.Name, "--", g.Command}
	if g.Agent == agentClaude {
		args = []string{"mcp", "add", "--scope", "user", g.Name, "--", g.Command}
	}
	args = append(args, g.Args...)
	if stdout, stderr, err := a.runAgent(ctx, agentWriteTimeout, bin, args...); err != nil {
		return agentError(g.Agent, args, stdout, stderr, err)
	}
	return nil
}

func (a *app) unregisterCommand(ctx context.Context, bin, agent string) error {
	args := []string{"mcp", "remove", mcpServerName}
	if agent == agentClaude {
		args = []string{"mcp", "remove", "--scope", "user", mcpServerName}
	}
	if stdout, stderr, err := a.runAgent(ctx, agentWriteTimeout, bin, args...); err != nil {
		return agentError(agent, args, stdout, stderr, err)
	}
	return nil
}

func agentError(agent string, args []string, stdout, stderr []byte, err error) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(stdout))
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg != "" {
		msg = ": " + printable(msg)
	}
	return &core.Error{Code: core.CodeInternal, Err: err, Message: fmt.Sprintf("%s %s failed (%v)%s",
		agent, strings.Join(args, " "), err, msg)}
}

// currentRegistration reads the server named lamplight that an agent has
// at user scope. Claude Code's is read from its configuration file, because
// claude mcp get may start the server to check it; Codex's from codex mcp
// get --json, which only reads. Output study cannot read is an error, never
// taken for a registration made by hand.
func (a *app) currentRegistration(ctx context.Context, agent, bin string) (registrationRecord, bool, error) {
	g := registrationRecord{Agent: agent, Name: mcpServerName}
	if agent == agentClaude {
		p, err := a.claudeJSONPath()
		if err != nil {
			return g, false, err
		}
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			return g, false, nil
		}
		if err != nil {
			return g, false, fmt.Errorf("reading %s: %w", p, err)
		}
		var cfg struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return g, false, &core.Error{Code: core.CodeInternal, Err: err,
				Message: fmt.Sprintf("cannot read Claude Code's MCP servers in %s: %v", p, err)}
		}
		s, ok := cfg.MCPServers[mcpServerName]
		if !ok {
			return g, false, nil
		}
		g.Command, g.Args = s.Command, s.Args
		return g, true, nil
	}
	args := []string{"mcp", "get", mcpServerName, "--json"}
	stdout, stderr, err := a.runAgent(ctx, agentReadTimeout, bin, args...)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && strings.Contains(strings.ToLower(string(stderr)+string(stdout)), "no mcp server named") {
			return g, false, nil
		}
		return g, false, agentError(agent, args, stdout, stderr, err)
	}
	var s struct {
		Transport *struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(stdout, &s); err != nil || s.Transport == nil {
		why := "it has no transport"
		if err != nil {
			why = err.Error()
		}
		shown := strings.TrimSpace(string(stdout))
		if len(shown) > 200 {
			shown = shown[:200] + "…"
		}
		return g, false, &core.Error{Code: core.CodeInternal, Err: err, Message: fmt.Sprintf(
			"codex %s printed something study cannot read (%s): %q", strings.Join(args, " "), why, printable(shown))}
	}
	g.Command, g.Args = s.Transport.Command, s.Transport.Args
	return g, true, nil
}

// claudePluginID is Lamplight's Claude Code plugin as the marketplace in the
// Lamplight repository, named lamplight, installs it.
const claudePluginID = "lamplight@lamplight"

// pluginState says whether Lamplight's Claude Code plugin is enabled where
// study setup can see it, and in which settings file.
type pluginState struct {
	enabled bool
	file    string
}

// claudePlugin reads enabledPlugins where Claude Code keeps it: the user's
// settings, which reach every project, and the settings of the project study
// runs in, local over shared. A user-scope registration reaches every
// project, so a plugin enabled in either place would load next to it. Other
// projects' settings, and managed settings, are not visible from here.
func (a *app) claudePlugin() pluginState {
	if project := a.claudeProject(); project != "" {
		for _, f := range []string{
			filepath.Join(project, ".claude", "settings.local.json"),
			filepath.Join(project, ".claude", "settings.json"),
		} {
			if on, set := pluginSetting(f); set {
				if on {
					return pluginState{enabled: true, file: f}
				}
				break
			}
		}
	}
	if dir, err := a.claudeDir(); err == nil {
		f := filepath.Join(dir, "settings.json")
		if on, _ := pluginSetting(f); on {
			return pluginState{enabled: true, file: f}
		}
	}
	return pluginState{}
}

// pluginSetting reads the plugin's entry in a settings file's enabledPlugins:
// whether it is enabled, and whether the file sets it at all.
func pluginSetting(file string) (on, set bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return false, false
	}
	var settings struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return false, false
	}
	v, ok := settings.EnabledPlugins[claudePluginID]
	if !ok {
		return false, false
	}
	return v == true, true
}

// claudeProject is the folder a Claude Code session started where study runs
// would take as its project: the nearest folder, from the working folder up
// and short of HOME, that has a .claude folder.
func (a *app) claudeProject() string {
	dir := a.opts.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if !filepath.IsAbs(dir) {
		return ""
	}
	home, _ := a.homeDir()
	user, _ := a.claudeDir()
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if d == home {
			return ""
		}
		if p := filepath.Join(d, ".claude"); p != user {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				return d
			}
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

func (a *app) homeDir() (string, error) {
	if home := a.opts.Getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Clean(home), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", &core.Error{Code: core.CodeInternal, Message: "cannot find your home folder: set HOME", Err: err}
	}
	return home, nil
}

// skillDir is where study setup installs the skill: the Agent Skills
// folder that Codex reads and Claude Code's link points to.
func (a *app) skillDir() (string, error) {
	home, err := a.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agents", "skills", lamplight.Name), nil
}

// claudeDir is Claude Code's configuration folder: CLAUDE_CONFIG_DIR, or
// ~/.claude.
func (a *app) claudeDir() (string, error) {
	if dir := a.opts.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	home, err := a.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// claudeJSONPath is the file where Claude Code keeps user-scope MCP servers.
func (a *app) claudeJSONPath() (string, error) {
	if dir := a.opts.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := a.homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

func (a *app) claudeSkillLink() (string, error) {
	dir, err := a.claudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skills", lamplight.Name), nil
}
