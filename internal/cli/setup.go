package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// study setup installs the lamplight skill and registers study's MCP server
// with Claude Code and Codex (ADR-0008). It records everything it writes in
// setup.json, so --remove undoes exactly that and --check finds what went
// stale, and it never replaces or deletes a file or folder it did not
// create.

const (
	setupRecordFile   = "setup.json"
	setupRecordFormat = 1
	// mcpServerName is the name study's MCP server is registered under.
	mcpServerName = "lamplight"
)

// Agents study setup knows how to set up.
const (
	agentClaude = "claude"
	agentCodex  = "codex"
)

var setupAgents = []string{agentClaude, agentCodex}

// executable returns the running study's path. Tests replace it.
var executable = os.Executable

// setupRecord is what study setup did, so --remove can undo exactly that.
type setupRecord struct {
	Format        int                  `json:"format"`
	Skill         *skillRecord         `json:"skill,omitempty"`
	Link          *linkRecord          `json:"claude_skill_link,omitempty"`
	Registrations []registrationRecord `json:"registrations"`
}

// skillRecord is the skill folder study setup created.
type skillRecord struct {
	Dir string `json:"dir"`
	// Files maps each file setup wrote, by slash-separated path inside Dir,
	// to the sha256 of what it wrote: it replaces or removes a file only
	// while it still has that content.
	Files map[string]string `json:"files"`
	// Dirs are the folders setup created, parents first, Dir included.
	Dirs []string `json:"dirs"`
}

// linkRecord is the link to the skill folder that study setup made where
// Claude Code finds skills.
type linkRecord struct {
	Path   string   `json:"path"`
	Target string   `json:"target"`
	Dirs   []string `json:"dirs,omitempty"`
}

// registrationRecord is an MCP server study setup registered with an agent.
type registrationRecord struct {
	Agent   string   `json:"agent"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func (r *setupRecord) registration(agent string) *registrationRecord {
	for i := range r.Registrations {
		if r.Registrations[i].Agent == agent {
			return &r.Registrations[i]
		}
	}
	return nil
}

func (r *setupRecord) dropRegistration(agent string) {
	r.Registrations = slices.DeleteFunc(r.Registrations, func(g registrationRecord) bool { return g.Agent == agent })
}

func (r *setupRecord) empty() bool {
	return r.Skill == nil && r.Link == nil && len(r.Registrations) == 0
}

// setupResult is the --json data of study setup and study setup --remove.
type setupResult struct {
	// Study is the path agents run study by.
	Study string `json:"study,omitempty"`
	// StudyNote explains a Study path that may not survive upgrades.
	StudyNote string        `json:"study_note,omitempty"`
	Skill     skillResult   `json:"skill"`
	Agents    []agentResult `json:"agents"`
	DryRun    bool          `json:"dry_run"`
	Remove    bool          `json:"remove"`
	Note      string        `json:"note,omitempty"`
	// Manual lists what is left for the learner to do by hand.
	Manual []string `json:"manual"`
	// Findings and UpToDate are set by --check.
	Findings []core.Finding `json:"findings,omitempty"`
	UpToDate *bool          `json:"up_to_date,omitempty"`
}

// skillResult says what happened to the skill folder.
type skillResult struct {
	Dir    string `json:"dir"`
	Status string `json:"status"`
	// Written, Removed and Kept list files by their path inside Dir. Kept
	// are files changed by hand since setup wrote them: setup leaves them.
	Written []string `json:"written"`
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
}

// agentResult says what happened for one agent.
type agentResult struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
	// Link and LinkStatus describe Claude Code's link to the skill folder.
	Link       string `json:"link,omitempty"`
	LinkStatus string `json:"link_status,omitempty"`
}

// Statuses in setupResult.
const (
	statusInstalled    = "installed"
	statusUpdated      = "updated"
	statusCurrent      = "current"
	statusRegistered   = "registered"
	statusReregistered = "re-registered"
	statusKept         = "kept"
	statusSkipped      = "skipped"
	statusPlugin       = "plugin"
	statusLinked       = "linked"
	statusRemoved      = "removed"
	statusAlreadyGone  = "already_gone"
	statusNotInstalled = "not_installed"
)

func (a *app) setupCommand() *cobra.Command {
	var agent string
	var dryRun, check, remove bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the lamplight skill and register study with Claude Code and Codex",
		Long: "Install the lamplight skill in ~/.agents/skills/lamplight, link it from ~/.claude/skills/lamplight,\n" +
			"and register study's MCP server at user scope with each agent's own command\n" +
			"(claude mcp add --scope user, codex mcp add), by study's absolute path.\n\n" +
			"study setup never replaces or deletes a file or folder it did not create, and records what\n" +
			"it did: --remove undoes exactly that, and --check reports what went stale. When the Claude\n" +
			"Code plugin is enabled, setup leaves Claude Code to the plugin.\n\n" +
			"Other agents: add an MCP server that runs \"study mcp\" (see docs/cli.md).",
		Example: `  study setup
  study setup --agent codex --dry-run
  study setup --check
  study setup --remove`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agents, err := pickAgents(agent)
			if err != nil {
				return a.fail(err)
			}
			if check && (remove || dryRun) {
				return a.fail(usageError{errors.New("--check writes nothing: give it without --remove or --dry-run")})
			}
			var res setupResult
			switch {
			case check:
				res, err = a.setupCheck(cmd.Context(), agents)
				if err == nil && res.UpToDate != nil && !*res.UpToDate {
					a.exit = ExitError
				}
			case remove:
				res, err = a.setupRemove(cmd.Context(), agents, dryRun)
			default:
				res, err = a.setupInstall(cmd.Context(), agents, dryRun)
			}
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSetupResult(a.out, res)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "all", "claude, codex or all")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&check, "check", false, "report what is missing or stale, without changing anything; exit 1 when setup is needed")
	cmd.Flags().BoolVar(&remove, "remove", false, "undo what study setup did")
	_ = cmd.RegisterFlagCompletionFunc("agent", cobra.FixedCompletions(
		append(slices.Clone(setupAgents), "all"), cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func pickAgents(flag string) ([]string, error) {
	switch flag {
	case "", "all":
		return slices.Clone(setupAgents), nil
	case agentClaude, agentCodex:
		return []string{flag}, nil
	}
	return nil, usageError{fmt.Errorf("--agent must be claude, codex or all, not %q", flag)}
}

func (a *app) setupInstall(ctx context.Context, agents []string, dryRun bool) (setupResult, error) {
	rec, err := a.readSetupRecord()
	if err != nil {
		return setupResult{}, err
	}
	study, onPath, err := a.studyPath()
	if err != nil {
		return setupResult{}, err
	}
	res := setupResult{Study: study, DryRun: dryRun, Agents: []agentResult{}, Manual: []string{}}
	if !onPath {
		res.StudyNote = "this study is not on your PATH, so agents will run it from " + study +
			": install study with a package or go install so the path survives upgrades"
	}
	files, err := skillFiles()
	if err != nil {
		return res, err
	}
	skill, err := a.installSkill(&rec, files, dryRun)
	res.Skill = skill
	if err != nil {
		return res, err
	}
	plugin := a.claudePluginEnabled()
	var firstErr error
	for _, agent := range agents {
		r, err := a.setupAgent(ctx, agent, study, plugin, &rec, dryRun)
		res.Agents = append(res.Agents, r)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if !dryRun {
		if err := a.writeSetupRecord(rec); err != nil && firstErr == nil {
			firstErr = err
		}
		res.Note = "Start a new conversation in your agent to use Lamplight."
	}
	return res, firstErr
}

// installSkill installs or updates the skill folder setup owns, and refuses
// one it did not create before anything is written.
func (a *app) installSkill(rec *setupRecord, files map[string][]byte, dryRun bool) (skillResult, error) {
	dir, err := a.skillDir()
	if err != nil {
		return skillResult{}, err
	}
	res := skillResult{Dir: dir, Written: []string{}, Removed: []string{}, Kept: []string{}}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		res.Status = statusInstalled
		next := &skillRecord{Dir: dir, Files: map[string]string{}, Dirs: []string{}}
		for _, rel := range sortedKeys(files) {
			res.Written = append(res.Written, rel)
			if dryRun {
				continue
			}
			if err := writeRecorded(filepath.Join(dir, filepath.FromSlash(rel)), files[rel], &next.Dirs); err != nil {
				return res, err
			}
			next.Files[rel] = sha256Hex(files[rel])
		}
		if !dryRun {
			rec.Skill = next
		}
		return res, nil
	case err != nil:
		return res, fmt.Errorf("reading %s: %w", dir, err)
	case !info.IsDir() || rec.Skill == nil || rec.Skill.Dir != dir:
		res.Status = statusKept
		return res, &core.Error{Code: core.CodeAlreadyExists, Message: dir +
			" already exists and study setup did not create it: move it away or remove it, then run study setup again"}
	}

	prev := rec.Skill
	next := &skillRecord{Dir: dir, Files: map[string]string{}, Dirs: slices.Clone(prev.Dirs)}
	for _, rel := range sortedKeys(files) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		want := sha256Hex(files[rel])
		got := fileSHA256(p)
		switch {
		case got == want:
			next.Files[rel] = want
		case got == "" && !pathExists(p), got != "" && got == prev.Files[rel]:
			res.Written = append(res.Written, rel)
			next.Files[rel] = want
			if !dryRun {
				if err := writeRecorded(p, files[rel], &next.Dirs); err != nil {
					return res, err
				}
			}
		default:
			// Changed by hand since setup wrote it, or never written by
			// setup: the learner's now.
			res.Kept = append(res.Kept, rel)
			if h, ok := prev.Files[rel]; ok {
				next.Files[rel] = h
			}
		}
	}
	for _, rel := range sortedKeys(prev.Files) {
		if _, still := files[rel]; still {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if fileSHA256(p) == prev.Files[rel] {
			res.Removed = append(res.Removed, rel)
			if !dryRun {
				if err := os.Remove(p); err != nil {
					return res, err
				}
			}
		} else if pathExists(p) {
			res.Kept = append(res.Kept, rel)
		}
	}
	res.Status = statusCurrent
	if len(res.Written) > 0 || len(res.Removed) > 0 {
		res.Status = statusUpdated
	}
	if !dryRun {
		rec.Skill = next
	}
	return res, nil
}

// setupAgent registers study with one agent, and for Claude Code links the
// skill where it finds skills, unless the Claude Code plugin provides both.
func (a *app) setupAgent(ctx context.Context, agent, study string, plugin bool, rec *setupRecord, dryRun bool) (agentResult, error) {
	res := agentResult{Agent: agent}
	if agent == agentClaude && plugin {
		res.Status = statusPlugin
		res.Note = "the Claude Code plugin provides Lamplight, so study setup leaves Claude Code to it"
		// Undo what an earlier setup did for Claude Code, so the two never
		// both register.
		var undone []string
		if g := rec.registration(agentClaude); g != nil {
			if out, err := a.unregister(ctx, agentClaude, *g, rec, dryRun); err != nil {
				return res, err
			} else if out == statusRemoved {
				undone = append(undone, "its MCP registration")
			}
		}
		if rec.Link != nil {
			if out, err := a.removeLink(rec, dryRun); err != nil {
				return res, err
			} else if out == statusRemoved {
				undone = append(undone, "its skill link")
			}
		}
		if len(undone) > 0 {
			res.Note += "; removed " + strings.Join(undone, " and ") + " from an earlier study setup"
		}
		return res, nil
	}
	var linkNote string
	if agent == agentClaude {
		res.Link, res.LinkStatus, linkNote = a.installLink(rec, dryRun)
	}
	defer func() {
		if linkNote != "" {
			if res.Note != "" {
				res.Note += "; "
			}
			res.Note += linkNote
		}
	}()
	bin := a.lookPath(agent)
	if bin == "" {
		res.Status = statusSkipped
		res.Note = agent + " is not on your PATH; run study setup again once it is installed"
		return res, nil
	}
	current, found, err := a.currentRegistration(ctx, agent, bin)
	if err != nil {
		res.Status = statusSkipped
		res.Note = err.Error()
		return res, nil
	}
	want := registrationRecord{Agent: agent, Name: mcpServerName, Command: study, Args: []string{"mcp"}}
	ours := rec.registration(agent)
	switch {
	case !found:
		res.Status = statusRegistered
		if !dryRun {
			if err := a.register(ctx, bin, want); err != nil {
				res.Status = statusSkipped
				return res, err
			}
			rec.dropRegistration(agent)
			rec.Registrations = append(rec.Registrations, want)
		}
	case ours != nil && sameRegistration(current, *ours):
		if current.Command == study && slices.Equal(current.Args, want.Args) {
			res.Status = statusCurrent
			return res, nil
		}
		res.Status = statusReregistered
		res.Note = "study moved from " + current.Command
		if !dryRun {
			if err := a.unregisterCommand(ctx, bin, agent); err != nil {
				return res, err
			}
			rec.dropRegistration(agent)
			if err := a.register(ctx, bin, want); err != nil {
				return res, err
			}
			rec.Registrations = append(rec.Registrations, want)
		}
	default:
		res.Status = statusKept
		res.Note = "a server named " + mcpServerName + " is already registered, not by study setup, so it is left alone"
		if current.Command != "" {
			res.Note += " (it runs " + strings.Join(append([]string{current.Command}, current.Args...), " ") + ")"
		}
		if ours != nil && !dryRun {
			rec.dropRegistration(agent)
		}
	}
	return res, nil
}

func sameRegistration(current, recorded registrationRecord) bool {
	return current.Command == recorded.Command && slices.Equal(current.Args, recorded.Args)
}

// installLink links Claude Code's skills folder to the skill folder. It
// returns the link's path, what happened, and why when it did not link.
func (a *app) installLink(rec *setupRecord, dryRun bool) (string, string, string) {
	link, err := a.claudeSkillLink()
	if err != nil {
		return link, statusSkipped, err.Error()
	}
	if rec.Skill == nil && !dryRun {
		return link, statusSkipped, "the skill is not installed"
	}
	target, err := a.skillDir()
	if err != nil {
		return link, statusSkipped, err.Error()
	}
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if dryRun {
			return link, statusLinked, ""
		}
		next := &linkRecord{Path: link, Target: target}
		if err := mkdirRecorded(filepath.Dir(link), &next.Dirs); err != nil {
			return link, statusSkipped, "cannot link the skill: " + err.Error()
		}
		if err := os.Symlink(target, link); err != nil {
			removeEmptyDirs(next.Dirs)
			return link, statusSkipped, "cannot link the skill: " + err.Error()
		}
		rec.Link = next
		return link, statusLinked, ""
	case err != nil:
		return link, statusSkipped, "cannot read " + link + ": " + err.Error()
	case info.Mode()&fs.ModeSymlink != 0:
		if dest, err := os.Readlink(link); err == nil && dest == target {
			return link, statusCurrent, ""
		}
	}
	return link, statusKept, link + " exists and study setup did not make it, so Claude Code loads that skill instead"
}

// setupRemove undoes what study setup did, for agents, and removes the skill
// folder once no agent needs it.
func (a *app) setupRemove(ctx context.Context, agents []string, dryRun bool) (setupResult, error) {
	rec, err := a.readSetupRecord()
	if err != nil {
		return setupResult{}, err
	}
	res := setupResult{DryRun: dryRun, Remove: true, Agents: []agentResult{},
		Skill: skillResult{Written: []string{}, Removed: []string{}, Kept: []string{}}, Manual: []string{}}
	if rec.empty() {
		res.Skill.Status = statusNotInstalled
		res.Note = "study setup has not installed anything"
		return res, nil
	}
	var firstErr error
	for _, agent := range agents {
		r := agentResult{Agent: agent, Status: statusNotInstalled}
		if g := rec.registration(agent); g != nil {
			out, err := a.unregister(ctx, agent, *g, &rec, dryRun)
			r.Status = out
			switch {
			case err != nil:
				r.Note = err.Error()
				if firstErr == nil {
					firstErr = err
				}
			case out == statusKept:
				r.Note = "its registration changed since study setup made it, so it is left alone"
			case out == statusSkipped:
				r.Note = agent + " is not on your PATH, so the registration stays"
				res.Manual = append(res.Manual, removeHint(agent))
			}
		}
		if agent == agentClaude && rec.Link != nil {
			r.Link = rec.Link.Path
			out, err := a.removeLink(&rec, dryRun)
			r.LinkStatus = out
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		res.Agents = append(res.Agents, r)
	}
	if rec.Skill != nil {
		res.Skill.Dir = rec.Skill.Dir
		res.Skill.Status = statusKept
		if len(rec.Registrations) == 0 && rec.Link == nil {
			if err := a.removeSkill(&rec, &res.Skill, dryRun); err != nil && firstErr == nil {
				firstErr = err
			}
		} else {
			res.Note = "the skill folder stays while another agent uses it"
		}
	} else {
		res.Skill.Status = statusNotInstalled
	}
	if !dryRun {
		if err := a.writeSetupRecord(rec); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return res, firstErr
}

func removeHint(agent string) string {
	if agent == agentClaude {
		return "claude mcp remove --scope user " + mcpServerName
	}
	return "codex mcp remove " + mcpServerName
}

// unregister removes a registration study setup made, while it is still the
// one setup made, and updates rec.
func (a *app) unregister(ctx context.Context, agent string, g registrationRecord, rec *setupRecord, dryRun bool) (string, error) {
	bin := a.lookPath(agent)
	if bin == "" {
		return statusSkipped, nil
	}
	current, found, err := a.currentRegistration(ctx, agent, bin)
	if err != nil {
		return statusSkipped, err
	}
	switch {
	case !found:
		if !dryRun {
			rec.dropRegistration(agent)
		}
		return statusAlreadyGone, nil
	case !sameRegistration(current, g):
		if !dryRun {
			rec.dropRegistration(agent)
		}
		return statusKept, nil
	}
	if !dryRun {
		if err := a.unregisterCommand(ctx, bin, agent); err != nil {
			return statusKept, err
		}
		rec.dropRegistration(agent)
	}
	return statusRemoved, nil
}

// removeLink removes Claude Code's link to the skill folder while it is the
// link setup made.
func (a *app) removeLink(rec *setupRecord, dryRun bool) (string, error) {
	l := rec.Link
	info, err := os.Lstat(l.Path)
	status := statusKept
	switch {
	case errors.Is(err, fs.ErrNotExist):
		status = statusAlreadyGone
	case err != nil:
		return statusKept, err
	case info.Mode()&fs.ModeSymlink != 0:
		if dest, err := os.Readlink(l.Path); err == nil && dest == l.Target {
			status = statusRemoved
			if !dryRun {
				if err := os.Remove(l.Path); err != nil {
					return statusKept, err
				}
			}
		}
	}
	if !dryRun {
		removeEmptyDirs(l.Dirs)
		rec.Link = nil
	}
	return status, nil
}

// removeSkill removes the skill files setup wrote and nobody changed, and
// the folders it created once they are empty.
func (a *app) removeSkill(rec *setupRecord, res *skillResult, dryRun bool) error {
	s := rec.Skill
	for _, rel := range sortedKeys(s.Files) {
		p := filepath.Join(s.Dir, filepath.FromSlash(rel))
		switch got := fileSHA256(p); {
		case got == s.Files[rel]:
			res.Removed = append(res.Removed, rel)
			if !dryRun {
				if err := os.Remove(p); err != nil {
					return err
				}
			}
		case pathExists(p):
			res.Kept = append(res.Kept, rel)
		}
	}
	res.Status = statusRemoved
	if len(res.Kept) > 0 {
		res.Status = statusKept
	}
	if !dryRun {
		removeEmptyDirs(s.Dirs)
		rec.Skill = nil
	}
	return nil
}

// setupCheck reports what is missing or stale without changing anything.
func (a *app) setupCheck(ctx context.Context, agents []string) (setupResult, error) {
	rec, err := a.readSetupRecord()
	if err != nil {
		return setupResult{}, err
	}
	study, _, err := a.studyPath()
	if err != nil {
		return setupResult{}, err
	}
	res := setupResult{Study: study, Agents: []agentResult{}, Manual: []string{},
		Skill: skillResult{Written: []string{}, Removed: []string{}, Kept: []string{}}}
	files, err := skillFiles()
	if err != nil {
		return res, err
	}
	res.Findings = append(res.Findings, a.checkSkill(rec, files))
	plugin := a.claudePluginEnabled()
	for _, agent := range agents {
		res.Findings = append(res.Findings, a.checkAgent(ctx, agent, study, plugin, rec)...)
	}
	upToDate := true
	for _, f := range res.Findings {
		if f.Status != core.FindingOK {
			upToDate = false
		}
	}
	res.UpToDate = &upToDate
	return res, nil
}

func (a *app) checkSkill(rec setupRecord, files map[string][]byte) core.Finding {
	f := core.Finding{Name: "setup:skill"}
	if rec.Skill == nil {
		f.Status, f.Message, f.Fix = core.FindingWarn, "study setup has not installed the lamplight skill", "study setup"
		return f
	}
	if _, err := os.Stat(rec.Skill.Dir); err != nil {
		f.Status, f.Message, f.Fix = core.FindingWarn, rec.Skill.Dir+" is gone", "study setup"
		return f
	}
	var stale, changed int
	for rel, data := range files {
		want := sha256Hex(data)
		got := fileSHA256(filepath.Join(rec.Skill.Dir, filepath.FromSlash(rel)))
		switch {
		case got == want:
		case got != "" && got != rec.Skill.Files[rel]:
			changed++
		default:
			stale++
		}
	}
	switch {
	case stale > 0:
		f.Status, f.Fix = core.FindingWarn, "study setup (study mcp also refreshes it when it starts)"
		f.Message = fmt.Sprintf("the skill in %s is from another version of study (%d %s to update)",
			rec.Skill.Dir, stale, plural(stale, "file", "files"))
	case changed > 0:
		f.Status = core.FindingOK
		f.Message = fmt.Sprintf("the skill is installed in %s; %d %s changed by hand, which study keeps",
			rec.Skill.Dir, changed, plural(changed, "file", "files"))
	default:
		f.Status, f.Message = core.FindingOK, "the skill is installed in "+rec.Skill.Dir
	}
	return f
}

func (a *app) checkAgent(ctx context.Context, agent, study string, plugin bool, rec setupRecord) []core.Finding {
	f := core.Finding{Name: "setup:" + agent}
	ours := rec.registration(agent)
	if agent == agentClaude && plugin {
		if ours != nil || rec.Link != nil {
			f.Status, f.Fix = core.FindingWarn, "study setup --remove --agent claude"
			f.Message = "both the Claude Code plugin and study setup provide Lamplight to Claude Code"
		} else {
			f.Status, f.Message = core.FindingOK, "the Claude Code plugin provides Lamplight"
		}
		return []core.Finding{f}
	}
	bin := a.lookPath(agent)
	if bin == "" {
		if ours != nil {
			f.Status, f.Message = core.FindingWarn, agent+" is no longer on your PATH, but study setup registered study with it"
			f.Fix = "reinstall " + agent + ", or run study setup --remove --agent " + agent
		} else {
			f.Status, f.Message = core.FindingOK, agent+" is not installed"
		}
		return []core.Finding{f}
	}
	current, found, err := a.currentRegistration(ctx, agent, bin)
	switch {
	case err != nil:
		f.Status, f.Message = core.FindingWarn, err.Error()
	case !found:
		f.Status, f.Message, f.Fix = core.FindingWarn, "study is not registered with "+agent, "study setup --agent "+agent
	case ours == nil || !sameRegistration(current, *ours):
		f.Status, f.Message = core.FindingOK, "a server named "+mcpServerName+" is registered with "+agent+", not by study setup"
	case current.Command != study:
		f.Status, f.Fix = core.FindingWarn, "study setup --agent "+agent
		f.Message = agent + " runs study from " + current.Command + ", but this study is " + study
		if _, err := os.Stat(current.Command); err != nil {
			f.Message = agent + " runs study from " + current.Command + ", which is gone"
		}
	default:
		f.Status, f.Message = core.FindingOK, "study is registered with "+agent
	}
	out := []core.Finding{f}
	if agent == agentClaude && rec.Skill != nil {
		l := core.Finding{Name: "setup:claude_skill_link"}
		link, _ := a.claudeSkillLink()
		info, err := os.Lstat(link)
		switch {
		case err != nil:
			l.Status, l.Message, l.Fix = core.FindingWarn, "Claude Code does not see the skill: "+link+" is missing", "study setup --agent claude"
		case info.Mode()&fs.ModeSymlink != 0 && readlink(link) == rec.Skill.Dir:
			l.Status, l.Message = core.FindingOK, link+" links to the skill"
		default:
			l.Status, l.Message = core.FindingOK, link+" exists, not made by study setup"
		}
		out = append(out, l)
	}
	return out
}

func readlink(p string) string {
	dest, _ := os.Readlink(p)
	return dest
}

// diagnoseSetup is study doctor's setup Finding: one line that says whether
// agents can reach study.
func (a *app) diagnoseSetup(ctx context.Context) core.Finding {
	f := core.Finding{Name: "setup"}
	rec, err := a.readSetupRecord()
	if err != nil {
		f.Status, f.Message = core.FindingWarn, err.Error()
		if core.CodeOf(err) == core.CodeNewerFormat {
			f.Fix = "upgrade study"
		} else if p, perr := a.setupRecordPath(); perr == nil {
			f.Fix = "delete " + p + ", then run study setup"
		}
		return f
	}
	plugin := a.claudePluginEnabled()
	found := []string{}
	for _, agent := range setupAgents {
		if a.lookPath(agent) != "" {
			found = append(found, agent)
		}
	}
	if rec.empty() && !plugin && len(found) == 0 {
		f.Status, f.Message = core.FindingOK, "no Claude Code or Codex found; other agents need an MCP server that runs study mcp"
		return f
	}
	if rec.empty() && !plugin {
		f.Status, f.Message, f.Fix = core.FindingWarn, "Lamplight is not set up for "+strings.Join(found, " or "), "study setup"
		return f
	}
	res, err := a.setupCheck(ctx, setupAgents)
	if err != nil {
		f.Status, f.Message = core.FindingWarn, err.Error()
		return f
	}
	for _, g := range res.Findings {
		if g.Status != core.FindingOK {
			f.Status, f.Message, f.Fix = core.FindingWarn, g.Message, g.Fix
			return f
		}
	}
	f.Status, f.Message = core.FindingOK, "agents run study from "+res.Study
	if plugin && rec.empty() {
		f.Message = "the Claude Code plugin provides Lamplight"
	}
	return f
}

// refreshSkill updates the skill files study setup wrote to this version's,
// when nobody changed them since. study mcp calls it when it starts, so an
// upgrade reaches the skill without running setup again. It never writes
// anything else, and never fails the server: problems go to the Log.
func (a *app) refreshSkill() {
	rec, err := a.readSetupRecord()
	if err != nil || rec.Skill == nil {
		return
	}
	info, err := os.Lstat(rec.Skill.Dir)
	if err != nil || !info.IsDir() {
		return
	}
	files, err := skillFiles()
	if err != nil {
		return
	}
	changed := false
	for _, rel := range sortedKeys(files) {
		want := sha256Hex(files[rel])
		recorded, ours := rec.Skill.Files[rel]
		if recorded == want {
			continue
		}
		p := filepath.Join(rec.Skill.Dir, filepath.FromSlash(rel))
		got := fileSHA256(p)
		switch {
		case got == want:
			rec.Skill.Files[rel] = want
			changed = true
		case ours && got == recorded, !ours && !pathExists(p):
			if err := writeRecorded(p, files[rel], &rec.Skill.Dirs); err != nil {
				a.logWarn("refreshing the lamplight skill", "file", p, "error", err)
				continue
			}
			rec.Skill.Files[rel] = want
			changed = true
		}
	}
	for _, rel := range sortedKeys(rec.Skill.Files) {
		if _, still := files[rel]; still {
			continue
		}
		p := filepath.Join(rec.Skill.Dir, filepath.FromSlash(rel))
		if fileSHA256(p) == rec.Skill.Files[rel] {
			if err := os.Remove(p); err != nil {
				a.logWarn("refreshing the lamplight skill", "file", p, "error", err)
				continue
			}
		}
		delete(rec.Skill.Files, rel)
		changed = true
	}
	if changed {
		if err := a.writeSetupRecord(rec); err != nil {
			a.logWarn("recording the refreshed lamplight skill", "error", err)
		}
	}
}

func (a *app) logWarn(msg string, args ...any) {
	if a.logs != nil {
		a.logs.logger.Warn(msg, args...)
	}
}

// skillFiles returns the embedded skill's files by slash-separated path.
func skillFiles() (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(lamplight.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(lamplight.FS(), p)
		if err != nil {
			return err
		}
		files[path.Clean(p)] = data
		return nil
	})
	return files, err
}

// studyPath returns the path agents should run study by. The running binary
// is often a versioned path that the next upgrade removes (Homebrew's
// Cellar or Caskroom, for example), while the study on PATH (Homebrew's bin
// link, ~/go/bin, /usr/bin) stays put. So when the study on PATH is this
// same program, its PATH entry is used as written; otherwise the running
// binary's real path, and onPath is false.
func (a *app) studyPath() (string, bool, error) {
	exe, err := executable()
	if err != nil {
		return "", false, &core.Error{Code: core.CodeInternal, Message: "cannot find the study binary: " + err.Error(), Err: err}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return exe, false, nil
	}
	for _, dir := range filepath.SplitList(a.opts.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		cand := filepath.Join(dir, "study")
		if info, err := os.Stat(cand); err == nil && os.SameFile(info, exeInfo) {
			return cand, true, nil
		}
	}
	return exe, false, nil
}

// lookPath finds an agent's command on the PATH study was given, or "".
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

func (a *app) runAgent(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = a.agentEnv()
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (a *app) register(ctx context.Context, bin string, g registrationRecord) error {
	args := []string{"mcp", "add", g.Name, "--", g.Command}
	if g.Agent == agentClaude {
		args = []string{"mcp", "add", "--scope", "user", g.Name, "--", g.Command}
	}
	args = append(args, g.Args...)
	if out, err := a.runAgent(ctx, bin, args...); err != nil {
		return agentError(g.Agent, args, out, err)
	}
	return nil
}

func (a *app) unregisterCommand(ctx context.Context, bin, agent string) error {
	args := []string{"mcp", "remove", mcpServerName}
	if agent == agentClaude {
		args = []string{"mcp", "remove", "--scope", "user", mcpServerName}
	}
	if out, err := a.runAgent(ctx, bin, args...); err != nil {
		return agentError(agent, args, out, err)
	}
	return nil
}

func agentError(agent string, args []string, out string, err error) error {
	msg := strings.TrimSpace(out)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return &core.Error{Code: core.CodeInternal, Err: err, Message: fmt.Sprintf("%s %s failed: %v %s",
		agent, strings.Join(args, " "), err, printable(msg))}
}

// currentRegistration reads the server named lamplight that an agent has
// at user scope. Claude Code's is read from its configuration file, because
// claude mcp get may start the server to check it; Codex's from codex mcp
// get --json, which only reads.
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
			return g, false, fmt.Errorf("cannot read Claude Code's MCP servers in %s: %v", p, err)
		}
		s, ok := cfg.MCPServers[mcpServerName]
		if !ok {
			return g, false, nil
		}
		g.Command, g.Args = s.Command, s.Args
		return g, true, nil
	}
	out, err := a.runAgent(ctx, bin, "mcp", "get", mcpServerName, "--json")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "no mcp server") {
			return g, false, nil
		}
		return g, false, agentError(agent, []string{"mcp", "get", mcpServerName, "--json"}, out, err)
	}
	var s struct {
		Transport struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"transport"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return g, true, nil
	}
	g.Command, g.Args = s.Transport.Command, s.Transport.Args
	return g, true, nil
}

// claudePluginEnabled reports whether Claude Code has Lamplight's plugin
// enabled at user scope, in enabledPlugins of its settings.
func (a *app) claudePluginEnabled() bool {
	dir, err := a.claudeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return false
	}
	var settings struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return false
	}
	for id, on := range settings.EnabledPlugins {
		if strings.HasPrefix(id, mcpServerName+"@") && on == true {
			return true
		}
	}
	return false
}

func (a *app) homeDir() (string, error) {
	if home := a.opts.Getenv("HOME"); filepath.IsAbs(home) {
		return home, nil
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
		return dir, nil
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

// writeRecorded writes data to p atomically, creating and recording the
// folders it needs.
func writeRecorded(p string, data []byte, dirs *[]string) error {
	if err := mkdirRecorded(filepath.Dir(p), dirs); err != nil {
		return err
	}
	return replaceFile(p, data, 0o644)
}

// mkdirRecorded creates dir and its missing parents, appending each folder
// it creates to dirs, parents first.
func mkdirRecorded(dir string, dirs *[]string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if !slices.Contains(*dirs, missing[i]) {
			*dirs = append(*dirs, missing[i])
		}
	}
	return nil
}

// removeEmptyDirs removes the folders in dirs that are empty, deepest
// first; anything someone put in them keeps them.
func removeEmptyDirs(dirs []string) {
	sorted := slices.Clone(dirs)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, d := range sorted {
		_ = os.Remove(d) // fails, as it should, unless d is empty
	}
}

// pathExists reports whether anything, a link included, is at p.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (a *app) setupRecordPath() (string, error) {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, setupRecordFile), nil
}

func (a *app) readSetupRecord() (setupRecord, error) {
	rec := setupRecord{Format: setupRecordFormat, Registrations: []registrationRecord{}}
	p, err := a.setupRecordPath()
	if err != nil {
		return rec, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("reading %s: %w", p, err)
	}
	damaged := func(why string) error {
		return &core.Error{Code: core.CodeCorrupt, Message: fmt.Sprintf(
			"%s is damaged (%s): delete it, then run study setup; study cannot undo earlier setups without it", p, why)}
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, damaged(err.Error())
	}
	switch {
	case rec.Format > setupRecordFormat:
		return rec, &core.Error{Code: core.CodeNewerFormat, Message: fmt.Sprintf(
			"%s has format %d, but this version of study only understands format %d: upgrade study",
			p, rec.Format, setupRecordFormat)}
	case rec.Format < 1:
		return rec, damaged("it has no format")
	}
	if rec.Registrations == nil {
		rec.Registrations = []registrationRecord{}
	}
	if rec.Skill != nil && rec.Skill.Files == nil {
		rec.Skill.Files = map[string]string{}
	}
	return rec, nil
}

func (a *app) writeSetupRecord(rec setupRecord) error {
	p, err := a.setupRecordPath()
	if err != nil {
		return err
	}
	if rec.empty() {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	rec.Format = setupRecordFormat
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p, append(data, '\n'))
}

// writeSetupResult renders study setup, --remove and --check for people.
func writeSetupResult(w io.Writer, r setupResult) error {
	var b strings.Builder
	if r.UpToDate != nil {
		names := make([]string, len(r.Findings))
		for i, f := range r.Findings {
			names[i] = f.Name
		}
		width := widest(names) + 2
		for _, f := range r.Findings {
			mark := styleOK.Render("✓")
			if f.Status != core.FindingOK {
				mark = styleWarn.Render("!")
			}
			fmt.Fprintf(&b, "%s %s%s\n", mark, styleLabel.Render(pad(f.Name, width)), printable(f.Message))
			if f.Fix != "" {
				fmt.Fprintf(&b, "  %s%s %s\n", strings.Repeat(" ", width), styleDim.Render("fix:"), f.Fix)
			}
		}
		b.WriteString("\n")
		if *r.UpToDate {
			b.WriteString(styleOK.Render("study setup is up to date.") + "\n")
		} else {
			b.WriteString(styleWarn.Render("study setup has something to do: see the fixes above.") + "\n")
		}
		_, err := io.WriteString(w, b.String())
		return err
	}
	would := func(done, planned string) string {
		if r.DryRun {
			return planned
		}
		return done
	}
	nothing := r.Remove && r.Skill.Status == statusNotInstalled && len(r.Agents) == 0
	switch {
	case nothing:
		b.WriteString("study setup has not installed anything, so there is nothing to remove.\n")
	case r.Remove:
		b.WriteString(would("Removed what study setup did:", "study setup --remove would undo:") + "\n")
	default:
		fmt.Fprintf(&b, "%s %s\n", would("Lamplight is set up with study at", "study setup would set up Lamplight with study at"),
			styleAccent.Render(printable(r.Study)))
		if r.StudyNote != "" {
			fmt.Fprintf(&b, "%s\n", styleWarn.Render(printable(r.StudyNote)))
		}
	}
	if r.Skill.Dir != "" {
		in := "in"
		if r.Remove {
			in = "from"
		}
		fmt.Fprintf(&b, "  %s%s %s %s", styleLabel.Render(pad("skill", 8)), r.Skill.Status, in, printable(r.Skill.Dir))
		if n := len(r.Skill.Written); n > 0 && !r.Remove {
			fmt.Fprintf(&b, " (%d %s %s)", n, plural(n, "file", "files"), would("written", "to write"))
		}
		if n := len(r.Skill.Removed); n > 0 {
			fmt.Fprintf(&b, " (%d %s %s)", n, plural(n, "file", "files"), would("removed", "to remove"))
		}
		b.WriteString("\n")
		for _, k := range r.Skill.Kept {
			fmt.Fprintf(&b, "  %s%s\n", pad("", 8), styleDim.Render("kept "+printable(k)+": changed since study setup wrote it"))
		}
	}
	for _, g := range r.Agents {
		fmt.Fprintf(&b, "  %s%s", styleLabel.Render(pad(g.Agent, 8)), g.Status)
		if g.Link != "" && g.LinkStatus != "" {
			fmt.Fprintf(&b, "; skill link %s %s", printable(g.Link), g.LinkStatus)
		}
		b.WriteString("\n")
		if g.Note != "" {
			fmt.Fprintf(&b, "  %s%s\n", pad("", 8), styleDim.Render(printable(g.Note)))
		}
	}
	for _, m := range r.Manual {
		fmt.Fprintf(&b, "By hand: %s\n", m)
	}
	if r.Note != "" && !nothing {
		fmt.Fprintf(&b, "%s\n", printable(r.Note))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
