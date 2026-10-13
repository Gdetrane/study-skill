package cli

import (
	"context"
	"errors"
	"io/fs"
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
// Which registration is setup's own is said by the registry, and by nothing
// else: setup registers Shelf with a mark (Plugin.BySetup), and replaces or
// removes a plugin named shelf only when it carries the mark. One without
// it is the learner's, whatever it runs, the same study-shelf included, and
// is left exactly as it is. The registrar decides it under the registry's
// lock, so a registration the learner makes meanwhile is theirs too.
//
// setup.json says nothing of Shelf. It is in the state folder, which may be
// inside the Study home, and is above the plugins' own folder, which a
// learner may have opened to an agent's sandbox: a record there that said
// which registration is setup's would be the agent's to write. The registry
// is where the agent cannot write, or it is not read at all.

const (
	// shelfName is the name Shelf is registered under, which a Topic names.
	shelfName = "shelf"
	// shelfProgram is the program that is Shelf.
	shelfProgram = "study-shelf"
)

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
// study that agents run, or else in the folder of the running study itself,
// which a link on PATH may lead to: a release installs the two side by
// side, and they are one version. Without one there, it is the bare name,
// which the registry finds on PATH as study knowledge-base add shelf --
// study-shelf does.
//
// Beside study, a study-shelf is a file the learner could run: a folder of
// that name, or a file that is no program, is passed over, as a shell passes
// it over. Nothing is decided here about where one leads. What has the name
// and cannot be looked at, as a link through a folder study may not pass
// through, is handed to the registry, which says what stops it.
func (a *app) shelfCommand(loc studyLocation) []string {
	for _, study := range []string{loc.Path, loc.Real} {
		if !filepath.IsAbs(study) {
			continue
		}
		beside := filepath.Join(filepath.Dir(study), shelfProgram)
		switch info, err := os.Stat(beside); {
		case err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0:
			return []string{beside}
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return []string{beside}
		}
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
	// is registered as setup registers it, and statusKept when the name
	// stands for a registration that is the learner's.
	status string
	// program is the study-shelf that is registered, or would be, and from
	// where setup's own registration ran it before.
	program, from string
	// runs is what the learner's own registration runs.
	runs string
	// remains says, when there is no study-shelf to register, that setup's
	// registration is in the registry all the same, running program, and
	// gone that this program is no longer there.
	remains, gone bool
	// register makes the registration, when the status says there is one to
	// make.
	register func() error
}

// unreadRegistry is the error of a registry that could not be read at all:
// what study doctor reports once, for all that depends on the registry.
type unreadRegistry struct{ error }

func (e unreadRegistry) Unwrap() error { return e.error }

// planShelf decides what study setup does about Shelf, and changes nothing.
// What it would register is decided by the registry's own dry run of the
// registration, which looks at everything the real one needs.
func (a *app) planShelf(ctx context.Context, loc studyLocation, force bool) (shelfPlan, error) {
	// Without a study-shelf there is nothing to register, whatever else is
	// wrong: setup then goes on as it did before there was a Shelf.
	command := a.shelfCommand(loc)
	notInstalled := shelfPlan{status: statusSkipped}
	c, err := core.Open(a.opts)
	if err != nil && command != nil {
		return shelfPlan{}, err
	}
	if err != nil {
		return notInstalled, nil
	}
	list, err := c.Plugins(ctx)
	if err != nil && command != nil {
		return shelfPlan{}, unreadRegistry{err}
	}
	// A plugin the learner registered under the name is theirs, whatever it
	// runs and whatever study-shelf is installed: it is looked at first, and
	// then nothing else is. The registration that carries setup's mark is
	// setup's.
	var setups *core.Plugin
	for _, p := range list.Plugins {
		switch {
		case p.Name != shelfName:
		case !p.BySetup:
			return shelfPlan{status: statusKept, runs: describeBriefly(p)}, nil
		default:
			setups = &p
		}
	}
	// With no study-shelf to register, setup's registration is as it was.
	remaining := func() shelfPlan {
		plan := notInstalled
		if setups != nil {
			plan.program, plan.remains = setups.Command[0], true
			_, err := os.Stat(plan.program)
			plan.gone = errors.Is(err, fs.ErrNotExist)
		}
		return plan
	}
	if command == nil {
		return remaining(), nil
	}
	env := c.PluginRegistryEnv()
	spec := registrar.Spec{Name: shelfName, Command: command, BySetup: true, DryRun: true}
	planned, err := registrar.Add(ctx, env, spec)
	switch {
	case core.CodeOf(err) == core.CodeNotFound:
		// What a look at PATH took for it is no program the registry finds.
		return remaining(), nil
	case core.CodeOf(err) == core.CodeAlreadyExists:
		// The learner registered their own since the look.
		return shelfPlan{status: statusKept}, nil
	case err != nil:
		return shelfPlan{}, err
	}
	plan := shelfPlan{status: statusRegistered, program: planned.Plugin.Command[0]}
	switch {
	case !planned.Changed:
		plan.status = statusCurrent
		return plan, nil
	case planned.Replaced != nil:
		// Only a registration with setup's mark is ever replaced.
		plan.status, plan.from = statusReregistered, planned.Replaced.Command[0]
	}
	// What setup registers lasts no longer than the folder it is in, as the
	// study it registers with agents: PATH is whoever started study's to
	// arrange, a temporary folder first on it too.
	if !force && a.temporary(plan.program) {
		return shelfPlan{}, &core.Error{Code: core.CodeFailedPrecondition, Message: plan.program + " is in a temporary folder, " +
			"which will soon be gone: install " + shelfProgram + " where it stays, beside study, or pass --force to register " +
			"this one anyway"}
	}
	plan.register = func() error {
		spec.DryRun = false
		_, err := registrar.Add(ctx, env, spec)
		return err
	}
	return plan, nil
}

// describeBriefly is what a plugin's name stands for, as study shows it
// anywhere but in the registry's own listing: its command, or the scheme
// and host of its address. The rest of an address may be a key.
func describeBriefly(p core.Plugin) string {
	if p.URL != "" {
		return printable(core.PluginAddress(p.URL))
	}
	return describePlugin(p)
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

// beforeShelfRegister, when a test sets it, is called once setup has decided
// to register Shelf and before it does: where a registration of the
// learner's can reach the registry between setup's look and its write.
var beforeShelfRegister func()

// registerShelf makes sure Shelf is registered as a Knowledge base plugin
// when study-shelf is installed. It never replaces a registration that does
// not carry setup's mark, and records nothing: the registry holds the mark.
func (a *app) registerShelf(ctx context.Context, loc studyLocation, dryRun, force bool) (shelfResult, error) {
	res := shelfResult{Name: shelfName}
	failed := func(err error) (shelfResult, error) {
		err = shelfError(shelfNotRegistered, err)
		res.Status, res.Note = statusFailed, err.Error()
		return res, err
	}
	plan, err := a.planShelf(ctx, loc, force)
	if err != nil {
		return failed(err)
	}
	res.Status, res.Program = plan.status, plan.program
	switch {
	case plan.status == statusSkipped && plan.remains && !plan.gone:
		// Setup has no study-shelf to register, and its registration runs
		// one that is there.
		res.Status = statusCurrent
	case plan.status == statusSkipped && plan.remains:
		res.Note = shelfProgram + " is neither beside study nor on your PATH. The registration study setup made remains, and " +
			"runs " + plan.program + ", which is gone: install " + shelfProgram + " again and run study setup, or run study " +
			"setup --remove, which takes the registration away"
	case plan.status == statusSkipped:
		res.Note = shelfProgram + " is neither beside study nor on your PATH; run study setup again once it is installed"
	case plan.status == statusKept:
		res.Note = keptShelfNote(plan.runs)
	case plan.status == statusReregistered:
		res.Note = shelfProgram + " moved from " + plan.from
	case plan.status == statusRegistered:
		res.Note = "a Topic uses it once it names it: study topic update <topic> --knowledge-base " + shelfName
	}
	if plan.register == nil || dryRun {
		return res, nil
	}
	if beforeShelfRegister != nil {
		beforeShelfRegister()
	}
	switch err := plan.register(); {
	case core.CodeOf(err) == core.CodeAlreadyExists:
		// The learner registered their own since the look: the registry
		// refused under its lock, and nothing was written.
		res.Status, res.Program, res.Note = statusKept, "", keptShelfNote("")
	case err != nil:
		return failed(err)
	}
	return res, nil
}

// unregisterShelf is what study setup --remove does about Shelf. Undoing all
// of setup takes the registration that carries setup's mark out of the
// registry, and leaves one that does not. Shelf is no agent's, so undoing
// one agent's setup leaves it registered, and says so.
func (a *app) unregisterShelf(ctx context.Context, dryRun, everything bool) (shelfResult, error) {
	res := shelfResult{Name: shelfName, Status: statusNotInstalled}
	failed := func(err error) (shelfResult, error) {
		err = shelfError(shelfNotRemoved, err)
		res.Status, res.Note = statusFailed, err.Error()
		return res, err
	}
	c, err := core.Open(a.opts)
	if err != nil && everything {
		return failed(err)
	}
	if err != nil {
		return res, nil
	}
	if !everything {
		// Nothing is to be removed, so what cannot be read is not this
		// run's to report.
		list, _ := c.Plugins(ctx)
		for _, p := range list.Plugins {
			if p.Name == shelfName && p.BySetup {
				res.Status, res.Program = statusKept, p.Command[0]
				res.Note = "Shelf is no agent's, so it stays registered: study setup --remove without --agent removes it"
			}
		}
		return res, nil
	}
	removal, removed, err := registrar.RemoveBySetup(ctx, c.PluginRegistryEnv(), shelfName, dryRun)
	switch {
	case err != nil:
		return failed(err)
	case removed:
		res.Status, res.Program = statusRemoved, removal.Plugin.Command[0]
	}
	return res, nil
}

// checkShelf is study setup --check's Finding about Shelf: whether setup
// would register it, or register it anew. elsewhere says that study doctor
// reports what the Finding says on its own, where it reports the registry
// and each plugin in it, so that the doctor does not say it twice.
func (a *app) checkShelf(ctx context.Context, loc studyLocation) (f core.Finding, elsewhere bool) {
	f = core.Finding{Name: "setup:shelf", Status: core.FindingOK}
	plan, err := a.planShelf(ctx, loc, false)
	switch {
	case err != nil:
		f.Status, f.Message = core.FindingWarn, shelfError(shelfCannotBe, err).Error()
		// A registry that cannot be read is the doctor's own Finding.
		return f, errors.As(err, new(unreadRegistry))
	case plan.status == statusSkipped && plan.remains && plan.gone:
		f.Status, f.Message = core.FindingWarn, "Knowledge base plugin "+shelfName+" runs "+plan.program+", which is gone"
		f.Fix = "install " + shelfProgram + " again and run study setup, or run study setup --remove"
		return f, true
	case plan.status == statusSkipped && plan.remains, plan.status == statusCurrent:
		f.Message = "Shelf is registered as Knowledge base plugin " + shelfName + ": " + plan.program
	case plan.status == statusSkipped:
		f.Message = shelfProgram + " is not installed, so study setup registers no Knowledge base plugin"
	case plan.status == statusKept:
		f.Message = "a Knowledge base plugin named " + shelfName + " is registered, not by study setup"
	case plan.status == statusReregistered:
		f.Status, f.Fix = core.FindingWarn, "study setup"
		f.Message = "Knowledge base plugin " + shelfName + " runs " + plan.from + ", but " + shelfProgram + " is now " + plan.program
	default:
		f.Status, f.Fix = core.FindingWarn, "study setup"
		f.Message = plan.program + " is installed, and Shelf is not registered as a Knowledge base plugin"
	}
	return f, false
}
