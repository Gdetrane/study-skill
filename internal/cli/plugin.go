package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/claudeplugin"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// claudePluginPathCommand is the command source of the Claude Code plugin in
// the repository's marketplace: Claude Code runs it when the plugin is
// installed and once per session, and copies the folder it prints.
func (a *app) claudePluginPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "claude-plugin-path",
		Short: "Write the Claude Code plugin and print its folder (run by the plugin marketplace)",
		Long: "Write Lamplight's Claude Code plugin, made from this study, and print its folder. The\n" +
			"marketplace in the Lamplight repository runs this command when the plugin is installed and\n" +
			"when a session starts, so the plugin always matches the installed study.\n\n" +
			"It refuses while study setup provides Lamplight to Claude Code, so the two never both register.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rec, err := a.readSetupRecord()
			if err != nil {
				return a.fail(err)
			}
			if rec.registration(agentClaude) != nil || rec.Link != nil {
				return a.fail(&core.Error{Code: core.CodeFailedPrecondition, Message: "study setup already provides " +
					"Lamplight to Claude Code: run study setup --remove --agent claude, then install the plugin"})
			}
			study, _, err := a.studyPath()
			if err != nil {
				return a.fail(err)
			}
			parent, err := cacheDir(a.opts.Getenv)
			if err != nil {
				return a.fail(err)
			}
			dir, err := claudeplugin.Write(filepath.Join(parent, "claude-plugin"), claudeplugin.Options{
				Study: study, Version: strings.TrimPrefix(version(), "v"), Skill: lamplight.FS(),
			})
			if err != nil {
				return a.fail(&core.Error{Code: core.CodeInternal, Message: "writing the Claude Code plugin: " + err.Error(), Err: err})
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: map[string]string{"path": dir}})
			}
			_, err = fmt.Fprintln(a.stdout, dir)
			return err
		},
	}
}

// claudeHookCommand holds the hooks the Claude Code plugin runs. They are
// for Claude Code, not for people, so the group is hidden.
func (a *app) claudeHookCommand() *cobra.Command {
	group := &cobra.Command{
		Use:    "claude-hook",
		Short:  "Hooks run by the Claude Code plugin",
		Hidden: true,
		Args:   noArgs,
		RunE:   a.groupHelp,
	}
	group.AddCommand(&cobra.Command{
		Use:   "session-start",
		Short: "Print where the learner stands when a Claude Code session starts in the Study home",
		Long: "Read the SessionStart hook's input on stdin and, when the session starts inside the Study home,\n" +
			"print study status for the agent. Anywhere else it prints nothing. It never fails the session.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.sessionStartHook(cmd)
			return nil
		},
	})
	return group
}

func (a *app) sessionStartHook(cmd *cobra.Command) {
	var input struct {
		Cwd string `json:"cwd"`
	}
	data, _ := io.ReadAll(io.LimitReader(a.stdin, 1<<20))
	_ = json.Unmarshal(data, &input)
	cwd := input.Cwd
	if !filepath.IsAbs(cwd) {
		cwd = a.opts.Dir
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	opts := a.opts
	opts.Dir = cwd
	c, err := core.Open(opts)
	if err != nil || !within(cwd, c.Home()) {
		return
	}
	status, err := c.Status(cmd.Context())
	if err != nil {
		a.logWarn("the session-start hook could not read status", "error", err)
		return
	}
	fmt.Fprintln(a.out, "Lamplight (study status) — where the learner stands:")
	fmt.Fprintln(a.out)
	_ = writeStatus(a.out, status, c.Now())
}

// within reports whether dir is home or inside it, comparing real paths.
func within(dir, home string) bool {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(real, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// cacheDir is study's cache folder: $XDG_CACHE_HOME/lamplight, or
// ~/.cache/lamplight.
func cacheDir(getenv func(string) string) (string, error) {
	if dir := getenv("XDG_CACHE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "lamplight"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, ".cache", "lamplight"), nil
}
