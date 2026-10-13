package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/registrar"
)

// study setup registers Shelf, the Knowledge base plugin that is installed
// with study, in this computer's registry (ADR-0012), so that a Topic can
// name it. It goes through the registrar, as study knowledge-base add does,
// and is held to the same checks: a study-shelf inside the Study home, or
// reached through it, is refused.
//
// A plugin the learner registered under Shelf's name, to whatever program,
// is theirs: setup looks for one first, and then leaves everything as it is.
// Otherwise the registry decides what setup does, under its lock: setup asks
// it to register Shelf, and to replace only the registration setup's record
// says it made. So a registration the learner makes meanwhile is not
// replaced either, and --remove removes only the one setup made, while it is
// still that one.

const (
	// shelfName is the name Shelf is registered under, which a Topic names.
	shelfName = "shelf"
	// shelfProgram is the program that is Shelf.
	shelfProgram = "study-shelf"
)

// shelfRecord is Shelf as study setup registered it.
type shelfRecord struct {
	Name string `json:"name"`
	// Program is the study-shelf that was registered, by its absolute path.
	Program string `json:"program"`
}

func (r shelfRecord) plugin() core.Plugin {
	return core.Plugin{Name: r.Name, Command: []string{r.Program}}
}

// is reports whether p is the registration the record describes: the name,
// the program alone as its command, and nothing allowed.
func (r shelfRecord) is(p core.Plugin) bool {
	return p.Name == r.Name && p.URL == "" && slices.Equal(p.Command, []string{r.Program}) && !p.AllowStudyHomeArguments
}

// shelfResult says what happened to Shelf's registration as a Knowledge base
// plugin.
type shelfResult struct {
	// Name is the name Shelf is registered under.
	Name   string `json:"name"`
	Status string `json:"status"`
	// Program is the study-shelf that is registered, or would be, by its
	// absolute path.
	Program string `json:"program,omitempty"`
	Note    string `json:"note,omitempty"`
}

// shelfCommand is the command study setup registers Shelf by, or nil when
// study-shelf is not installed. It is the study-shelf in the folder of the
// study that agents run: a release installs the two side by side, and they
// are one version. Without one there, it is the bare name, which the
// registry finds on PATH as study knowledge-base add shelf -- study-shelf
// does.
//
// Nothing is decided here about where a study-shelf leads. Whatever has the
// name beside study, a link included, is handed to the registry, which
// follows it and refuses what it cannot follow.
func (a *app) shelfCommand(study string) []string {
	beside := filepath.Join(filepath.Dir(study), shelfProgram)
	if info, err := os.Lstat(beside); err == nil && !info.IsDir() {
		return []string{beside}
	}
	if a.lookPath(shelfProgram) != "" {
		return []string{shelfProgram}
	}
	return nil
}

// shelfPlan is what study setup would do about Shelf on this computer.
type shelfPlan struct {
	// status is what the run comes to: statusSkipped when study-shelf is
	// not installed, statusRegistered, statusReregistered when setup's own
	// registration ran study-shelf from elsewhere, statusCurrent when Shelf
	// is registered as setup registered it, and statusKept when the name
	// stands for a registration setup did not make.
	status string
	// program is the study-shelf that is registered, or would be, and from
	// where setup's own registration ran it before.
	program, from string
	// runs is what the name stands for when that is not setup's doing.
	runs string
	// register makes the registration, when the status says there is one to
	// make.
	register func() error
}

// planShelf decides what study setup does about Shelf, and changes nothing.
// What it would register is decided by the registry's own dry run of the
// registration, which looks at everything the real one needs. ours is the
// registration setup's record says it made.
func (a *app) planShelf(ctx context.Context, study string, ours *shelfRecord) (shelfPlan, error) {
	// Without a study-shelf there is nothing to register, whatever else is
	// wrong: setup then goes on as it did before there was a Shelf.
	command := a.shelfCommand(study)
	notInstalled := shelfPlan{status: statusSkipped}
	c, err := core.Open(a.opts)
	if err != nil && command != nil {
		return shelfPlan{}, err
	}
	if err != nil {
		return notInstalled, nil
	}
	// A plugin the learner registered under the name is theirs, whatever it
	// runs, this study-shelf too, and whatever study-shelf is installed: it
	// is looked at first, and then nothing else is.
	list, err := c.Plugins(ctx)
	if err != nil && command != nil {
		return shelfPlan{}, err
	}
	for _, p := range list.Plugins {
		if p.Name == shelfName && (ours == nil || !ours.is(p)) {
			return shelfPlan{status: statusKept, runs: describePlugin(p)}, nil
		}
	}
	if command == nil {
		return notInstalled, nil
	}
	env := c.PluginRegistryEnv()
	spec := registrar.Spec{Name: shelfName, Command: command, DryRun: true}
	if ours != nil {
		was := ours.plugin()
		spec.Replacing = &was
	}
	planned, err := registrar.Add(ctx, env, spec)
	plan := shelfPlan{status: statusRegistered}
	if len(planned.Plugin.Command) > 0 {
		plan.program = planned.Plugin.Command[0]
	}
	switch {
	case core.CodeOf(err) == core.CodeNotFound:
		// What a look at PATH took for it is no program the registry finds.
		return notInstalled, nil
	case core.CodeOf(err) == core.CodeAlreadyExists:
		// The learner registered their own since the look.
		return shelfPlan{status: statusKept}, nil
	case err != nil:
		return shelfPlan{}, err
	case !planned.Changed:
		plan.status = statusCurrent
		return plan, nil
	case planned.Replaced != nil && ours != nil:
		// Only the registration setup made is ever replaced.
		plan.status, plan.from = statusReregistered, ours.Program
	}
	plan.register = func() error {
		spec.DryRun = false
		_, err := registrar.Add(ctx, env, spec)
		return err
	}
	return plan, nil
}

// What study setup says it could not do about Shelf's registration, before
// the reason.
const (
	shelfNotRegistered = "study setup did not register Shelf as a Knowledge base plugin"
	shelfNotRemoved    = "study setup did not remove Shelf from the Knowledge base plugin registry"
	shelfCannotBe      = "study setup cannot register Shelf as a Knowledge base plugin"
)

// shelfError is why study setup could not do something to Shelf's
// registration: what says so, then the reason. What the registry refuses of
// a program is a mistake of whoever names one to study knowledge-base add;
// study setup found the program itself, so to its caller it is the computer
// that is not ready.
func shelfError(what string, err error) error {
	code := core.CodeOf(err)
	if slices.Contains([]core.ErrorCode{core.CodeInvalidArgument, core.CodeNotFound, core.CodeAlreadyExists}, code) {
		code = core.CodeFailedPrecondition
	}
	return &core.Error{Code: code, Err: err, Message: what + ": " + err.Error()}
}

// keptShelfNote says why a registration under Shelf's name is left alone.
func keptShelfNote(runs string) string {
	note := "a Knowledge base plugin named " + shelfName + " is registered already, not by study setup, so it is left alone"
	if runs != "" {
		note += " (it runs " + runs + ")"
	}
	return note
}

// registerShelf makes sure Shelf is registered as a Knowledge base plugin
// when study-shelf is installed. It records the registration before making
// it, as an agent's is recorded, and never replaces one it did not make.
func (s *setupRun) registerShelf(study string) (shelfResult, error) {
	res := shelfResult{Name: shelfName}
	failed := func(err error) (shelfResult, error) {
		err = shelfError(shelfNotRegistered, err)
		res.Status, res.Note = statusFailed, err.Error()
		return res, err
	}
	ours := s.rec.Shelf
	plan, err := s.a.planShelf(s.ctx, study, ours)
	if err != nil {
		return failed(err)
	}
	res.Status, res.Program = plan.status, plan.program
	switch plan.status {
	case statusSkipped:
		res.Note = shelfProgram + " is neither beside study nor on your PATH; run study setup again once it is installed"
		return res, nil
	case statusKept:
		res.Note = keptShelfNote(plan.runs)
		if ours != nil {
			s.rec.Shelf = nil
			if err := s.save(); err != nil {
				return failed(err)
			}
		}
		return res, nil
	case statusCurrent:
		return res, nil
	case statusReregistered:
		res.Note = shelfProgram + " moved from " + plan.from
	default:
		res.Note = "a Topic uses it once it names it: study topic update <topic> --knowledge-base " + shelfName
	}
	s.rec.Shelf = &shelfRecord{Name: shelfName, Program: plan.program}
	if err := s.save(); err != nil {
		return failed(err)
	}
	if s.dryRun {
		return res, nil
	}
	if err := plan.register(); err != nil {
		// The registry is replaced whole or not at all: nothing was made.
		s.rec.Shelf = ours
		if core.CodeOf(err) != core.CodeAlreadyExists {
			return failed(errors.Join(err, s.save()))
		}
		// The learner registered their own since the look.
		s.rec.Shelf = nil
		res.Status, res.Note = statusKept, keptShelfNote("")
		if err := s.save(); err != nil {
			return failed(err)
		}
	}
	return res, nil
}

// unregisterShelf takes Shelf out of the registry while its registration is
// still the one study setup made, and updates the record.
func (s *setupRun) unregisterShelf() (shelfResult, error) {
	ours := *s.rec.Shelf
	res := shelfResult{Name: ours.Name, Program: ours.Program}
	failed := func(err error) (shelfResult, error) {
		err = shelfError(shelfNotRemoved, err)
		res.Status, res.Note = statusFailed, err.Error()
		return res, err
	}
	c, err := core.Open(s.a.opts)
	if err != nil {
		return failed(err)
	}
	removed, err := registrar.RemoveExactly(s.ctx, c.PluginRegistryEnv(), ours.plugin(), s.dryRun)
	switch {
	case err != nil:
		return failed(err)
	case removed:
		res.Status = statusRemoved
	default:
		res.Status = statusAlreadyGone
		if list, err := c.Plugins(s.ctx); err == nil && slices.ContainsFunc(list.Plugins, func(p core.Plugin) bool { return p.Name == ours.Name }) {
			res.Status, res.Note = statusKept, "its registration changed since study setup made it, so it is left alone"
		}
	}
	s.rec.Shelf = nil
	if err := s.save(); err != nil {
		return failed(err)
	}
	return res, nil
}

// checkShelf is study setup --check's Finding about Shelf: whether setup
// would register it, or register it anew.
func (a *app) checkShelf(ctx context.Context, study string, rec setupRecord) core.Finding {
	f := core.Finding{Name: "setup:shelf", Status: core.FindingOK}
	plan, err := a.planShelf(ctx, study, rec.Shelf)
	switch {
	case err != nil:
		f.Status, f.Message = core.FindingWarn, shelfError(shelfCannotBe, err).Error()
	case plan.status == statusSkipped:
		f.Message = shelfProgram + " is not installed, so Shelf is not registered as a Knowledge base plugin"
	case plan.status == statusKept:
		f.Message = "a Knowledge base plugin named " + shelfName + " is registered, not by study setup"
	case plan.status == statusCurrent:
		f.Message = "Shelf is registered as Knowledge base plugin " + shelfName + ": " + plan.program
	case plan.status == statusReregistered:
		f.Status, f.Fix = core.FindingWarn, "study setup"
		f.Message = "Knowledge base plugin " + shelfName + " runs " + plan.from + ", but " + shelfProgram + " is now " + plan.program
	default:
		f.Status, f.Fix = core.FindingWarn, "study setup"
		f.Message = plan.program + " is installed, and Shelf is not registered as a Knowledge base plugin"
	}
	return f
}
