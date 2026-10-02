package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
// with Claude Code and Codex (ADR-0008). It records each step in setup.json
// before or as it takes it, under a lock, so --remove undoes exactly what was
// done even when a run stopped part-way, and --check finds what went stale.
// It never replaces or deletes a file or folder it did not create (see
// setup_paths.go).

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
	// Real is where Dir led when setup created it, with symlinks resolved.
	Real string `json:"real"`
	// Files maps each file setup wrote, by slash-separated path inside Dir,
	// to the sha256 of what it wrote: it replaces or removes a file only
	// while it still has that content.
	Files map[string]string `json:"files"`
	// Parents are the folders above Dir that setup created for it.
	Parents []dirRecord `json:"parents,omitempty"`
}

// linkRecord is the link to the skill folder that study setup made where
// Claude Code finds skills.
type linkRecord struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	// Parent is where the link's folder led when setup made the link, with
	// symlinks resolved.
	Parent string `json:"parent"`
	// Parents are the folders setup created for the link.
	Parents []dirRecord `json:"parents,omitempty"`
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

func (r *setupRecord) setRegistration(g registrationRecord) {
	r.dropRegistration(g.Agent)
	r.Registrations = append(r.Registrations, g)
}

func (r *setupRecord) empty() bool {
	return r.Skill == nil && r.Link == nil && len(r.Registrations) == 0
}

// setupResult is the --json data of study setup and study setup --remove.
type setupResult struct {
	// Study is the path agents run study by.
	Study string `json:"study,omitempty"`
	// StudyNote says how agents reach Study, or why the path may not last.
	StudyNote string        `json:"study_note,omitempty"`
	Skill     skillResult   `json:"skill"`
	Agents    []agentResult `json:"agents"`
	DryRun    bool          `json:"dry_run"`
	Remove    bool          `json:"remove"`
	Note      string        `json:"note,omitempty"`
	// Manual lists what is left for the learner to do by hand.
	Manual []string `json:"manual"`
}

func newSetupResult(dryRun, remove bool) setupResult {
	return setupResult{DryRun: dryRun, Remove: remove, Agents: []agentResult{}, Manual: []string{},
		Skill: skillResult{Written: []string{}, Removed: []string{}, Kept: []string{}}}
}

// skillResult says what happened to the skill folder.
type skillResult struct {
	Dir    string `json:"dir"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
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

// setupCheckResult is the --json data of study setup --check.
type setupCheckResult struct {
	Study    string         `json:"study"`
	UpToDate bool           `json:"up_to_date"`
	Findings []core.Finding `json:"findings"`
}

// Statuses in setupResult.
const (
	statusInstalled    = "installed"
	statusUpdated      = "updated"
	statusCurrent      = "current"
	statusRegistered   = "registered"
	statusReregistered = "re_registered"
	statusKept         = "kept"
	statusSkipped      = "skipped"
	statusPlugin       = "plugin"
	statusLinked       = "linked"
	statusRemoved      = "removed"
	statusAlreadyGone  = "already_gone"
	statusNotInstalled = "not_installed"
	statusFailed       = "failed"
)

func (a *app) setupCommand() *cobra.Command {
	var agent string
	var dryRun, check, remove, force bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the lamplight skill and register study with Claude Code and Codex",
		Long: "Install the lamplight skill in ~/.agents/skills/lamplight, link it from ~/.claude/skills/lamplight,\n" +
			"and register study's MCP server at user scope with each agent's own command\n" +
			"(claude mcp add --scope user, codex mcp add), by study's absolute path.\n\n" +
			"study setup never replaces or deletes a file or folder it did not create, and records each\n" +
			"step: --remove undoes exactly what was done, and --check reports what went stale. When the\n" +
			"Claude Code plugin is enabled, setup leaves Claude Code to the plugin.\n\n" +
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
			if check && (remove || dryRun || force) {
				return a.fail(usageError{errors.New("--check changes nothing: give it without --remove, --dry-run or --force")})
			}
			if check {
				return a.runSetupCheck(cmd.Context(), agents)
			}
			var res setupResult
			if remove {
				res, err = a.setupRemove(cmd.Context(), agents, dryRun)
			} else {
				res, err = a.setupInstall(cmd.Context(), agents, dryRun, force)
			}
			if err != nil {
				return a.failWith(res, err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSetupResult(a.out, res, false)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "all", "claude, codex or all")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without changing anything")
	cmd.Flags().BoolVar(&check, "check", false, "report what is missing or stale, without changing anything; exit 1 when setup is needed")
	cmd.Flags().BoolVar(&remove, "remove", false, "undo what study setup did")
	cmd.Flags().BoolVar(&force, "force", false, "register a study that runs from a temporary build folder anyway")
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

// failWith reports err after a setup that did part of its work: the JSON
// envelope holds what was done as its data, and people see it before the
// error. An error before setup looked at anything is reported alone.
func (a *app) failWith(res setupResult, err error) error {
	if !res.didSomething() && res.Skill.Status == "" {
		return a.fail(err)
	}
	if !a.json {
		if res.didSomething() {
			_ = writeSetupResult(a.out, res, true)
		}
		return err
	}
	if werr := a.writeJSON(envelope{Data: res, Error: &errorBody{Code: string(core.CodeOf(err)), Message: err.Error()}}); werr != nil {
		return werr
	}
	return reported{err}
}

func (r setupResult) didSomething() bool {
	return len(r.Skill.Written) > 0 || len(r.Skill.Removed) > 0 || len(r.Agents) > 0
}

// setupRun is one study setup or --remove. It works on the record and saves
// it after each step, never in a dry run, which instead takes every step on
// its copy of the record only: a dry run decides exactly as the real run.
type setupRun struct {
	a      *app
	ctx    context.Context
	rec    setupRecord
	dryRun bool
}

func (s *setupRun) save() error {
	if s.dryRun {
		return nil
	}
	return s.a.writeSetupRecord(s.rec)
}

// beginSetup locks the record, proves its folder writable and reads it. A
// dry run only reads.
func (a *app) beginSetup(ctx context.Context, dryRun bool) (*setupRun, func(), error) {
	unlock := func() {}
	if !dryRun {
		u, err := a.lockSetup(ctx, lockWaitSetup)
		if err != nil {
			return nil, nil, err
		}
		unlock = u
		if err := a.probeStateDir(); err != nil {
			unlock()
			return nil, nil, err
		}
	}
	rec, err := a.readSetupRecord()
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return &setupRun{a: a, ctx: ctx, rec: rec, dryRun: dryRun}, unlock, nil
}

func (a *app) setupInstall(ctx context.Context, agents []string, dryRun, force bool) (setupResult, error) {
	res := newSetupResult(dryRun, false)
	loc, err := a.studyPath()
	if err != nil {
		return res, err
	}
	res.Study, res.StudyNote = loc.Path, loc.Note
	if loc.Temporary && !force {
		return setupResult{}, &core.Error{Code: core.CodeFailedPrecondition, Message: "this study runs from " + loc.Path +
			", a temporary build folder that will soon be gone (go run builds there): install study with a package " +
			"or go install and run its setup, or pass --force to register this one anyway"}
	}
	files, err := skillFiles()
	if err != nil {
		return res, err
	}
	// A skill folder setup did not create stops it before it writes
	// anything, its lock and state folder included.
	if !dryRun {
		dry, unlock, err := a.beginSetup(ctx, true)
		if err != nil {
			return setupResult{}, err
		}
		unlock()
		if skill, err := dry.installSkill(files); err != nil {
			res.Skill = skill
			res.Skill.Written = []string{}
			return res, err
		}
	}
	run, unlock, err := a.beginSetup(ctx, dryRun)
	if err != nil {
		return setupResult{}, err
	}
	defer unlock()
	res.Skill, err = run.installSkill(files)
	if err != nil {
		return res, err
	}
	plugin := a.claudePlugin()
	var firstErr error
	for _, agent := range agents {
		r, err := run.setupAgent(agent, loc.Path, plugin)
		res.Agents = append(res.Agents, r)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if !dryRun && firstErr == nil {
		res.Note = "Start a new conversation in your agent to use Lamplight."
	}
	return res, firstErr
}

// installSkill installs or updates the skill folder setup owns, and refuses
// one it did not create before anything is written.
func (s *setupRun) installSkill(files map[string][]byte) (skillResult, error) {
	res := skillResult{Written: []string{}, Removed: []string{}, Kept: []string{}}
	dir, err := s.a.skillDir()
	if err != nil {
		return res, err
	}
	res.Dir = dir
	_, err = os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		res.Status = statusInstalled
		return res, s.createSkill(dir, files, &res)
	case err != nil:
		return res, fmt.Errorf("reading %s: %w", dir, err)
	case s.rec.Skill == nil:
		res.Status = statusKept
		return res, &core.Error{Code: core.CodeAlreadyExists, Message: dir +
			" already exists and study setup did not create it: move it away or remove it, then run study setup again"}
	case !realDir(dir, s.rec.Skill.Real):
		res.Status = statusKept
		return res, &core.Error{Code: core.CodeAlreadyExists, Message: dir +
			" is no longer the folder study setup created (it is a symlink now, or it moved): run study setup --remove, " +
			"which leaves it alone, move it away, then run study setup again"}
	}
	if err := s.updateSkill(files, true, &res); err != nil {
		return res, err
	}
	res.Status = statusCurrent
	if len(res.Written) > 0 || len(res.Removed) > 0 {
		res.Status = statusUpdated
	}
	return res, nil
}

// createSkill creates the skill folder: it records the folders it will
// create before creating them, and each file once written.
func (s *setupRun) createSkill(dir string, files map[string][]byte, res *skillResult) error {
	home, err := s.a.homeDir()
	if err != nil {
		return err
	}
	plan, err := planDirs(dir, home)
	if err == nil && len(plan) == 0 {
		err = errors.New("it appeared while study setup was creating it")
	}
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	// Folders an earlier setup created above the skill folder, and still
	// its own, stay recorded for --remove.
	var parents []dirRecord
	if old := s.rec.Skill; old != nil {
		for _, d := range old.Parents {
			if d.owned() {
				parents = append(parents, d)
			}
		}
	}
	above := plan[:len(plan)-1]
	s.rec.Skill = &skillRecord{Dir: dir, Real: plan[len(plan)-1].Real, Files: map[string]string{},
		Parents: slices.Concat(parents, above)}
	if s.dryRun {
		res.Written = sortedKeys(files)
		return nil
	}
	if err := s.save(); err != nil {
		return err
	}
	if made, err := makeDirs(plan); err != nil {
		// Record only the folders it did create.
		s.rec.Skill.Parents = slices.Concat(parents, above[:min(made, len(above))])
		return errors.Join(err, s.save())
	}
	f, err := openSkillFolder(s.rec.Skill)
	if err != nil {
		return err
	}
	defer f.close()
	for _, rel := range sortedKeys(files) {
		if err := f.write(rel, files[rel]); err != nil {
			return err
		}
		res.Written = append(res.Written, rel)
		s.rec.Skill.Files[rel] = sha256Hex(files[rel])
		if err := s.save(); err != nil {
			return err
		}
	}
	return nil
}

// updateSkill brings the skill folder setup owns to this version: it writes
// files that are setup's and unchanged, and files that are absent (with
// restore, also files the learner deleted), and removes setup's files that
// this version no longer has. Files changed by hand are kept.
func (s *setupRun) updateSkill(files map[string][]byte, restore bool, res *skillResult) error {
	sk := s.rec.Skill
	f, err := openSkillFolder(sk)
	if err != nil {
		return err
	}
	defer f.close()
	changed := false
	for _, rel := range sortedKeys(files) {
		want := sha256Hex(files[rel])
		kind, got, err := f.stat(rel)
		if err != nil {
			return err
		}
		recorded, ours := sk.Files[rel]
		switch {
		case kind == fileRegular && got == want:
			if recorded != want {
				sk.Files[rel], changed = want, true
			}
		case kind == fileAbsent && (restore || !ours), kind == fileRegular && ours && got == recorded:
			if !s.dryRun {
				if err := f.write(rel, files[rel]); err != nil {
					return errors.Join(err, s.saveIf(changed))
				}
			}
			res.Written = append(res.Written, rel)
			sk.Files[rel] = want
			if err := s.save(); err != nil {
				return err
			}
			changed = false
		case kind != fileAbsent:
			// Changed by hand since setup wrote it, never written by
			// setup, or reached through a symlink: the learner's.
			res.Kept = append(res.Kept, rel)
		}
	}
	var removed []string
	for _, rel := range sortedKeys(sk.Files) {
		if _, still := files[rel]; still {
			continue
		}
		kind, got, err := f.stat(rel)
		if err != nil {
			return err
		}
		switch {
		case kind == fileRegular && got == sk.Files[rel]:
			if !s.dryRun {
				if err := f.remove(rel); err != nil {
					return errors.Join(err, s.saveIf(changed))
				}
			}
			res.Removed = append(res.Removed, rel)
			removed = append(removed, rel)
		case kind != fileAbsent:
			res.Kept = append(res.Kept, rel)
		}
		// Gone, or the learner's now.
		delete(sk.Files, rel)
		changed = true
	}
	if !s.dryRun {
		f.removeEmptyDirs(removed)
	}
	return s.saveIf(changed)
}

func (s *setupRun) saveIf(changed bool) error {
	if !changed {
		return nil
	}
	return s.save()
}

// setupAgent registers study with one agent, and for Claude Code links the
// skill where it finds skills, unless the Claude Code plugin provides both.
func (s *setupRun) setupAgent(agent, study string, plugin pluginState) (agentResult, error) {
	res := agentResult{Agent: agent}
	if agent == agentClaude && plugin.enabled {
		return s.leaveToPlugin(plugin)
	}
	bin := s.a.lookPath(agent)
	if bin == "" {
		res.Status = statusSkipped
		res.Note = agent + " is not on your PATH; run study setup again once it is installed"
		return res, nil
	}
	var err error
	res.Status, res.Note, err = s.register(agent, bin, study)
	if agent == agentClaude {
		link, status, note, lerr := s.installLink()
		res.Link, res.LinkStatus = link, status
		res.Note = joinNotes(res.Note, note)
		err = errors.Join(err, lerr)
	}
	return res, err
}

// leaveToPlugin undoes what an earlier setup did for Claude Code, so the
// plugin and setup never both register.
func (s *setupRun) leaveToPlugin(plugin pluginState) (agentResult, error) {
	res := agentResult{Agent: agentClaude, Status: statusPlugin,
		Note: "the Claude Code plugin is enabled in " + plugin.file + ", so study setup leaves Claude Code to it"}
	var undone []string
	var err error
	if g := s.rec.registration(agentClaude); g != nil {
		out, uerr := s.unregister(agentClaude, *g)
		switch {
		case uerr != nil:
			err = uerr
			res.Note = joinNotes(res.Note, "could not remove the MCP registration an earlier study setup made: "+uerr.Error())
		case out == statusRemoved:
			undone = append(undone, "its MCP registration")
		case out == statusSkipped:
			res.Note = joinNotes(res.Note, "claude is not on your PATH, so the MCP registration an earlier study setup made stays")
		}
	}
	if s.rec.Link != nil {
		res.Link = s.rec.Link.Path
		out, lerr := s.removeLink()
		res.LinkStatus = out
		switch {
		case lerr != nil:
			err = errors.Join(err, lerr)
		case out == statusRemoved:
			undone = append(undone, "its skill link")
		}
	}
	if len(undone) > 0 {
		res.Note = joinNotes(res.Note, "removed "+strings.Join(undone, " and ")+" from an earlier study setup")
	}
	if err != nil {
		res.Status = statusFailed
	}
	return res, err
}

// register makes sure the agent runs this study. It records a registration
// before making it, so --remove finds it even when the agent fails
// part-way; --remove removes it only while it runs exactly this.
func (s *setupRun) register(agent, bin, study string) (status, note string, err error) {
	current, found, err := s.a.currentRegistration(s.ctx, agent, bin)
	if err != nil {
		return statusFailed, err.Error(), err
	}
	want := registrationRecord{Agent: agent, Name: mcpServerName, Command: study, Args: []string{"mcp"}}
	ours := s.rec.registration(agent)
	switch {
	case !found:
		status = statusRegistered
	case ours != nil && sameRegistration(current, *ours):
		if sameRegistration(current, want) {
			return statusCurrent, "", nil
		}
		status, note = statusReregistered, "study moved from "+current.Command
		if !s.dryRun {
			if err := s.a.unregisterCommand(s.ctx, bin, agent); err != nil {
				return statusFailed, err.Error(), err
			}
		}
	default:
		note = "a server named " + mcpServerName + " is already registered with " + agent +
			", not by study setup, so it is left alone"
		if current.Command != "" {
			note += " (it runs " + strings.Join(append([]string{current.Command}, current.Args...), " ") + ")"
		}
		if ours != nil {
			s.rec.dropRegistration(agent)
			if err := s.save(); err != nil {
				return statusFailed, err.Error(), err
			}
		}
		return statusKept, note, nil
	}
	s.rec.setRegistration(want)
	if err := s.save(); err != nil {
		return statusFailed, err.Error(), err
	}
	if !s.dryRun {
		if err := s.a.register(s.ctx, bin, want); err != nil {
			return statusFailed, joinNotes(note, err.Error()), err
		}
	}
	return status, note, nil
}

func sameRegistration(a, b registrationRecord) bool {
	return a.Command == b.Command && slices.Equal(a.Args, b.Args)
}

// installLink links Claude Code's skills folder to the skill folder. It
// returns the link's path, what happened, and why when it did not link.
func (s *setupRun) installLink() (link, status, note string, err error) {
	link, err = s.a.claudeSkillLink()
	if err != nil {
		return "", statusFailed, err.Error(), err
	}
	skill := s.rec.Skill
	if skill == nil {
		return link, statusSkipped, "the skill is not installed", nil
	}
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return link, statusFailed, "cannot read " + link + ": " + err.Error(), err
	case s.rec.Link != nil && s.rec.Link.owned():
		return link, statusCurrent, "", nil
	case info.Mode()&fs.ModeSymlink != 0 && sameFolder(link, skill.Dir):
		return link, statusCurrent, link + " leads to the skill, but study setup did not make it, so --remove leaves it", nil
	default:
		return link, statusKept, link + " exists and study setup did not make it, so Claude Code loads that skill instead", nil
	}
	claude, err := s.a.claudeDir()
	if err != nil {
		return link, statusFailed, err.Error(), err
	}
	plan, err := planDirs(filepath.Dir(link), filepath.Dir(claude))
	if err != nil {
		return link, statusSkipped, "cannot link the skill: " + err.Error(), nil
	}
	parent := ""
	if len(plan) > 0 {
		parent = plan[len(plan)-1].Real
	} else if parent, err = filepath.EvalSymlinks(filepath.Dir(link)); err != nil {
		return link, statusFailed, "cannot read " + filepath.Dir(link) + ": " + err.Error(), err
	}
	// Folders an earlier setup created for its link, and still its own,
	// stay recorded for --remove.
	var parents []dirRecord
	if old := s.rec.Link; old != nil {
		for _, d := range old.Parents {
			if d.owned() {
				parents = append(parents, d)
			}
		}
	}
	s.rec.Link = &linkRecord{Path: link, Target: skill.Dir, Parent: parent, Parents: slices.Concat(parents, plan)}
	if s.dryRun {
		return link, statusLinked, "", nil
	}
	if err := s.save(); err != nil {
		return link, statusFailed, err.Error(), err
	}
	made, err := makeDirs(plan)
	if err == nil {
		err = os.Symlink(skill.Dir, link)
	}
	if err != nil {
		// Undo the folders created for it; keep recording any that stay.
		s.rec.Link.Parents = slices.Concat(parents, plan[:made])
		removeDirs(s.rec.Link.Parents)
		s.rec.Link.Parents = slices.DeleteFunc(s.rec.Link.Parents, func(d dirRecord) bool { return !pathExists(d.Path) })
		if len(s.rec.Link.Parents) == 0 {
			s.rec.Link = nil
		}
		err = errors.Join(fmt.Errorf("linking %s: %w", link, err), s.save())
		return link, statusFailed, err.Error(), err
	}
	return link, statusLinked, "", nil
}

// owned reports whether l is still the link setup made: a symlink with the
// target setup gave it, in the folder it made it in.
func (l linkRecord) owned() bool {
	info, err := os.Lstat(l.Path)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(l.Path)
	if err != nil || dest != l.Target {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(l.Path))
	return err == nil && parent == l.Parent
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// setupRemove undoes what study setup did, for agents, and removes the skill
// folder once no agent needs it.
func (a *app) setupRemove(ctx context.Context, agents []string, dryRun bool) (setupResult, error) {
	res := newSetupResult(dryRun, true)
	run, unlock, err := a.beginSetup(ctx, dryRun)
	if err != nil {
		return res, err
	}
	defer unlock()
	if run.rec.empty() {
		res.Skill.Status = statusNotInstalled
		res.Note = "study setup has not installed anything"
		return res, nil
	}
	var firstErr error
	for _, agent := range agents {
		r := agentResult{Agent: agent, Status: statusNotInstalled}
		if g := run.rec.registration(agent); g != nil {
			out, err := run.unregister(agent, *g)
			r.Status = out
			switch {
			case err != nil:
				r.Note = err.Error()
				firstErr = firstOf(firstErr, err)
			case out == statusKept:
				r.Note = "its registration changed since study setup made it, so it is left alone"
			case out == statusSkipped:
				r.Note = agent + " is not on your PATH, so its registration stays"
				res.Manual = append(res.Manual, removeHint(agent))
			}
		}
		if agent == agentClaude && run.rec.Link != nil {
			r.Link = run.rec.Link.Path
			out, err := run.removeLink()
			r.LinkStatus = out
			switch {
			case err != nil:
				r.Note = joinNotes(r.Note, err.Error())
				firstErr = firstOf(firstErr, err)
			case out == statusKept:
				r.Note = joinNotes(r.Note, "its skill link changed since study setup made it, so it is left alone")
			}
		}
		res.Agents = append(res.Agents, r)
	}
	switch {
	case run.rec.Skill == nil:
		res.Skill.Status = statusNotInstalled
	case len(run.rec.Registrations) > 0 || run.rec.Link != nil:
		res.Skill.Dir, res.Skill.Status = run.rec.Skill.Dir, statusKept
		res.Note = "the skill folder stays while another agent uses it"
	default:
		if err := run.removeSkill(&res.Skill); err != nil {
			firstErr = firstOf(firstErr, err)
		}
	}
	return res, firstErr
}

// firstOf keeps the first error.
func firstOf(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

func removeHint(agent string) string {
	if agent == agentClaude {
		return "claude mcp remove --scope user " + mcpServerName
	}
	return "codex mcp remove " + mcpServerName
}

// unregister removes a registration study setup made, while it is still the
// one setup made, and updates the record.
func (s *setupRun) unregister(agent string, g registrationRecord) (string, error) {
	bin := s.a.lookPath(agent)
	if bin == "" {
		return statusSkipped, nil
	}
	current, found, err := s.a.currentRegistration(s.ctx, agent, bin)
	if err != nil {
		return statusFailed, err
	}
	status := statusRemoved
	switch {
	case !found:
		status = statusAlreadyGone
	case !sameRegistration(current, g):
		status = statusKept
	case !s.dryRun:
		if err := s.a.unregisterCommand(s.ctx, bin, agent); err != nil {
			return statusFailed, err
		}
	}
	s.rec.dropRegistration(agent)
	return status, s.save()
}

// removeLink removes Claude Code's link to the skill folder while it is the
// link setup made, and the folders setup created for it once empty.
func (s *setupRun) removeLink() (string, error) {
	l := *s.rec.Link
	status := statusKept
	_, err := os.Lstat(l.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		status = statusAlreadyGone
	case err != nil:
		return statusFailed, fmt.Errorf("reading %s: %w", l.Path, err)
	case l.owned():
		status = statusRemoved
		if !s.dryRun {
			if err := os.Remove(l.Path); err != nil {
				return statusFailed, fmt.Errorf("removing %s: %w", l.Path, err)
			}
		}
	}
	if !s.dryRun {
		removeDirs(l.Parents)
	}
	s.rec.Link = nil
	return status, s.save()
}

// removeSkill removes the skill files setup wrote and nobody changed, and
// the folders it created once they are empty.
func (s *setupRun) removeSkill(res *skillResult) error {
	sk := s.rec.Skill
	res.Dir = sk.Dir
	_, err := os.Lstat(sk.Dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		res.Status = statusAlreadyGone
	case err != nil:
		return fmt.Errorf("reading %s: %w", sk.Dir, err)
	case !realDir(sk.Dir, sk.Real):
		res.Status = statusKept
		res.Note = sk.Dir + " is no longer the folder study setup created (it is a symlink now, or it moved), so it is left alone"
	default:
		f, err := openSkillFolder(sk)
		if err != nil {
			return err
		}
		defer f.close()
		var removed []string
		for _, rel := range sortedKeys(sk.Files) {
			kind, got, err := f.stat(rel)
			if err != nil {
				return errors.Join(err, s.save())
			}
			switch {
			case kind == fileRegular && got == sk.Files[rel]:
				if !s.dryRun {
					if err := f.remove(rel); err != nil {
						return errors.Join(err, s.save())
					}
				}
				res.Removed = append(res.Removed, rel)
				removed = append(removed, rel)
				delete(sk.Files, rel)
			case kind != fileAbsent:
				res.Kept = append(res.Kept, rel)
			}
		}
		res.Status = statusRemoved
		if len(res.Kept) > 0 {
			res.Status = statusKept
		}
		if !s.dryRun {
			f.removeEmptyDirs(removed)
			removeDirs([]dirRecord{{Path: sk.Dir, Real: sk.Real}})
		}
	}
	if !s.dryRun {
		removeDirs(sk.Parents)
	}
	s.rec.Skill = nil
	return s.save()
}

// runSetupCheck reports what is missing or stale. With something to do it
// exits 1, and --json reports it as study doctor reports a failure: ok is
// false, the error code is unhealthy, and data holds the full report.
func (a *app) runSetupCheck(ctx context.Context, agents []string) error {
	res, err := a.setupCheck(ctx, agents)
	if err != nil {
		return a.fail(err)
	}
	if !res.UpToDate {
		a.exit = ExitError
		if a.json {
			var pending []string
			for _, f := range res.Findings {
				if f.Status != core.FindingOK {
					pending = append(pending, f.Name)
				}
			}
			msg := fmt.Sprintf("study setup has something to do: %s", strings.Join(pending, ", "))
			return a.writeJSON(envelope{Data: res, Error: &errorBody{Code: codeUnhealthy, Message: msg}})
		}
	} else if a.json {
		return a.writeJSON(envelope{OK: true, Data: res})
	}
	return writeSetupCheck(a.out, res)
}

// setupCheck reports what is missing or stale without changing anything.
func (a *app) setupCheck(ctx context.Context, agents []string) (setupCheckResult, error) {
	rec, err := a.readSetupRecord()
	if err != nil {
		return setupCheckResult{}, err
	}
	loc, err := a.studyPath()
	if err != nil {
		return setupCheckResult{}, err
	}
	files, err := skillFiles()
	if err != nil {
		return setupCheckResult{}, err
	}
	res := setupCheckResult{Study: loc.Path, Findings: []core.Finding{a.checkSkill(rec, files)}}
	plugin := a.claudePlugin()
	for _, agent := range agents {
		res.Findings = append(res.Findings, a.checkAgent(ctx, agent, loc.Path, plugin, rec)...)
	}
	res.UpToDate = true
	for _, f := range res.Findings {
		if f.Status != core.FindingOK {
			res.UpToDate = false
		}
	}
	return res, nil
}

func (a *app) checkSkill(rec setupRecord, files map[string][]byte) core.Finding {
	f := core.Finding{Name: "setup:skill", Status: core.FindingWarn}
	sk := rec.Skill
	if sk == nil {
		f.Message, f.Fix = "study setup has not installed the lamplight skill", "study setup"
		return f
	}
	if _, err := os.Lstat(sk.Dir); err != nil {
		f.Message, f.Fix = sk.Dir+" is gone", "study setup"
		return f
	}
	folder, err := openSkillFolder(sk)
	if err != nil {
		f.Message = sk.Dir + " is no longer the folder study setup created (it is a symlink now, or it moved)"
		f.Fix = "move it away, then run study setup --remove and study setup"
		return f
	}
	defer folder.close()
	var stale, missing, changed int
	for rel, data := range files {
		kind, got, err := folder.stat(rel)
		if err != nil {
			f.Message, f.Fix = err.Error(), "study setup"
			return f
		}
		recorded, ours := sk.Files[rel]
		switch {
		case kind == fileRegular && got == sha256Hex(data):
		case kind == fileAbsent && ours:
			missing++
		case kind == fileAbsent, kind == fileRegular && ours && got == recorded:
			stale++
		default:
			changed++
		}
	}
	for rel, sum := range sk.Files {
		if _, still := files[rel]; !still {
			if kind, got, err := folder.stat(rel); err == nil && kind == fileRegular && got == sum {
				stale++
			}
		}
	}
	switch {
	case missing > 0:
		// study mcp's refresh never restores a file the learner deleted.
		f.Message = fmt.Sprintf("%d %s of the skill in %s %s missing", missing,
			plural(missing, "file", "files"), sk.Dir, plural(missing, "is", "are"))
		f.Fix = "study setup"
	case stale > 0:
		f.Message = fmt.Sprintf("the skill in %s is from another version of study (%d %s to update)",
			sk.Dir, stale, plural(stale, "file", "files"))
		f.Fix = "study setup (study mcp also updates it when it starts)"
	case changed > 0:
		f.Status = core.FindingOK
		f.Message = fmt.Sprintf("the skill is installed in %s; %d %s changed by hand, which study keeps",
			sk.Dir, changed, plural(changed, "file", "files"))
	default:
		f.Status, f.Message = core.FindingOK, "the skill is installed in "+sk.Dir
	}
	return f
}

func (a *app) checkAgent(ctx context.Context, agent, study string, plugin pluginState, rec setupRecord) []core.Finding {
	f := core.Finding{Name: "setup:" + agent}
	ours := rec.registration(agent)
	if agent == agentClaude && plugin.enabled {
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
		case info.Mode()&fs.ModeSymlink != 0 && sameFolder(link, rec.Skill.Dir):
			l.Status, l.Message = core.FindingOK, link+" leads to the skill"
		default:
			l.Status, l.Message = core.FindingOK, link+" exists, not made by study setup"
		}
		out = append(out, l)
	}
	return out
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
	plugin := a.claudePlugin()
	found := []string{}
	for _, agent := range setupAgents {
		if a.lookPath(agent) != "" {
			found = append(found, agent)
		}
	}
	if rec.empty() && !plugin.enabled && len(found) == 0 {
		f.Status, f.Message = core.FindingOK, "no Claude Code or Codex found; other agents need an MCP server that runs study mcp"
		return f
	}
	if rec.empty() && !plugin.enabled {
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
	if plugin.enabled && rec.empty() {
		f.Message = "the Claude Code plugin provides Lamplight"
	}
	return f
}

// lockWaitRefresh is how long study mcp waits to refresh the skill before it
// starts without refreshing.
const lockWaitRefresh = 2 * time.Second

// refreshSkill updates the skill files study setup wrote to this version's,
// when nobody changed them since, under the setup lock. study mcp calls it
// when it starts, so an upgrade reaches the skill without running setup
// again. It never restores a file the learner deleted, never writes
// anywhere else, and never fails the server: problems go to the Log.
func (a *app) refreshSkill(ctx context.Context) {
	if rec, err := a.readSetupRecord(); err != nil || rec.Skill == nil {
		return
	}
	files, err := skillFiles()
	if err != nil {
		return
	}
	unlock, err := a.lockSetup(ctx, lockWaitRefresh)
	if err != nil {
		a.logWarn("not refreshing the lamplight skill", "error", err)
		return
	}
	defer unlock()
	rec, err := a.readSetupRecord()
	if err != nil || rec.Skill == nil || !realDir(rec.Skill.Dir, rec.Skill.Real) {
		return
	}
	run := &setupRun{a: a, ctx: ctx, rec: rec}
	var res skillResult
	if err := run.updateSkill(files, false, &res); err != nil {
		a.logWarn("refreshing the lamplight skill", "error", err)
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

// statusText is a status for people; a dry run says what it would do.
func statusText(status string, dryRun bool) string {
	if dryRun {
		if w, ok := map[string]string{
			statusInstalled: "would install", statusUpdated: "would update", statusRegistered: "would register",
			statusReregistered: "would re-register", statusLinked: "would link", statusRemoved: "would remove",
		}[status]; ok {
			return w
		}
	}
	if status == statusReregistered {
		return "re-registered"
	}
	return strings.ReplaceAll(status, "_", " ")
}

// writeSetupResult renders study setup and --remove for people. failed says
// the run stopped part-way: the error follows on stderr.
func writeSetupResult(w io.Writer, r setupResult, failed bool) error {
	var b strings.Builder
	nothing := r.Remove && r.Skill.Status == statusNotInstalled && len(r.Agents) == 0
	switch {
	case failed && r.Remove:
		b.WriteString("study setup --remove stopped part-way, after this:\n")
	case failed:
		fmt.Fprintf(&b, "study setup stopped part-way, with study at %s, after this:\n", styleAccent.Render(printable(r.Study)))
	case nothing:
		b.WriteString("study setup has not installed anything, so there is nothing to remove.\n")
	case r.Remove && r.DryRun:
		b.WriteString("study setup --remove would undo:\n")
	case r.Remove:
		b.WriteString("Removed what study setup did:\n")
	case r.DryRun:
		fmt.Fprintf(&b, "study setup would set up Lamplight with study at %s\n", styleAccent.Render(printable(r.Study)))
	default:
		fmt.Fprintf(&b, "Lamplight is set up with study at %s\n", styleAccent.Render(printable(r.Study)))
	}
	if r.StudyNote != "" {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(printable(r.StudyNote)))
	}
	if r.Skill.Dir != "" && r.Skill.Status != "" {
		in := "in"
		if r.Skill.Status == statusRemoved {
			in = "from"
		}
		fmt.Fprintf(&b, "  %s%s %s %s", styleLabel.Render(pad("skill", 8)), statusText(r.Skill.Status, r.DryRun), in, printable(r.Skill.Dir))
		if n := len(r.Skill.Written); n > 0 {
			fmt.Fprintf(&b, " (%d %s %s)", n, plural(n, "file", "files"), would(r.DryRun, "written", "to write"))
		}
		if n := len(r.Skill.Removed); n > 0 {
			fmt.Fprintf(&b, " (%d %s %s)", n, plural(n, "file", "files"), would(r.DryRun, "removed", "to remove"))
		}
		b.WriteString("\n")
		if r.Skill.Note != "" {
			fmt.Fprintf(&b, "  %s%s\n", pad("", 8), styleDim.Render(printable(r.Skill.Note)))
		}
		for _, k := range r.Skill.Kept {
			fmt.Fprintf(&b, "  %s%s\n", pad("", 8), styleDim.Render("kept "+printable(k)+": changed, or moved behind a symlink, since study setup wrote it"))
		}
	}
	for _, g := range r.Agents {
		fmt.Fprintf(&b, "  %s%s", styleLabel.Render(pad(g.Agent, 8)), statusText(g.Status, r.DryRun))
		if g.Link != "" && g.LinkStatus != "" {
			fmt.Fprintf(&b, "; %s", linkText(g.LinkStatus, printable(g.Link), r.DryRun, r.Remove))
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

// linkText says what happened to Claude Code's link to the skill.
func linkText(status, link string, dryRun, remove bool) string {
	switch status {
	case statusLinked:
		return would(dryRun, "linked ", "would link ") + link
	case statusRemoved:
		return would(dryRun, "removed ", "would remove ") + link
	case statusCurrent:
		return link + " leads to the skill"
	case statusAlreadyGone:
		return link + " was already gone"
	case statusFailed:
		return would(!remove, "could not remove ", "could not link ") + link
	case statusSkipped:
		return "did not link " + link
	}
	return statusText(status, dryRun) + " " + link
}

func would(dryRun bool, done, planned string) string {
	if dryRun {
		return planned
	}
	return done
}

// writeSetupCheck renders study setup --check for people.
func writeSetupCheck(w io.Writer, r setupCheckResult) error {
	var b strings.Builder
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
	if r.UpToDate {
		b.WriteString(styleOK.Render("study setup is up to date.") + "\n")
	} else {
		b.WriteString(styleWarn.Render("study setup has something to do: see the fixes above.") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
