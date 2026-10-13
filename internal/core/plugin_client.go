package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// study is the MCP client of a Knowledge base plugin (ADR-0012). The client
// is internal/pluginclient; the core is what starts it, with the registry
// and the Study home of this run, and the adapters reach a plugin only
// through the core.
//
// Four things start a plugin, each through startPlugin:
//
//   - CheckPlugin, for study knowledge-base check, which asks which contract
//     the plugin speaks and then has it test what it depends on, and
//     CheckPlugins, for study doctor, which asks every registered plugin the
//     same, the first with the short deadline of a glance. Both are for the
//     learner and CLI-only, and they are the one thing that asks a plugin's
//     diagnose tool (checkPlugin): what it answers is the learner's to read,
//     and it may take ten seconds.
//   - IndexSources, for study source index, which asks the Topic's plugin to
//     index. It is CLI-only too, and the one thing that asks a plugin to
//     index (source_index.go).
//   - askPlugins, for status and for a Topic's Sources, which asks the
//     plugin what it has indexed and nothing else, with a short deadline.
//     An MCP tool reaches it.
//   - startTopicPlugin, for a search of a Topic's Sources and for Evidence
//     recorded with its Passages, which asks the plugin what it has indexed
//     and then to search, or for Passages again (evidence_search.go). An MCP
//     tool reaches it too.
//
// So an MCP tool can have a plugin started, to ask its status, search and
// passage tools, and can never have one index, or test what it depends on.
// The MCP server runs outside the agent's sandbox, and a plugin that indexed
// for it would read any file the agent named as a Source there: a search
// reads the plugin's index and never a Source. And what a plugin finds of
// what it depends on is the learner's question, which no agent needs the
// answer to, and no tool waits ten seconds for a plugin. Tests in
// internal/mcpserver keep it so: two call the tools with plugins registered
// that record what they are asked, one of them where a plugin fails and a
// Study home is not in order; one reads this package for whatever could ask
// a plugin to index from anywhere but IndexSources, to search from anywhere
// but the two functions that do, or to diagnose from anywhere but
// checkPlugin; and one reads every other package the server is built from,
// which may ask a plugin none of it and name none of the three operations
// above that are the command line's.
//
// Nothing is kept between two of them: each starts the plugin and stops it
// again before it returns.

// PluginOptions says how Knowledge base plugins are started and how long
// they have. Zero values select the production defaults.
type PluginOptions struct {
	// Environ returns the environment study has, of which a plugin started
	// by a command is given a few variables and no more
	// (pluginclient.StartCommand). Defaults to os.Environ.
	Environ func() []string
	// StartTimeout is how long a plugin has to start, or be reached, and say
	// which contract it speaks; CallTimeout how long to answer one call;
	// SearchTimeout how long a search may take; and StopGrace how long a
	// command has to exit when asked, and again when signalled, before it is
	// killed. Each defaults to the constant of internal/pluginclient.
	StartTimeout  time.Duration
	CallTimeout   time.Duration
	SearchTimeout time.Duration
	StopGrace     time.Duration
	// GlanceTimeout is how long status and a Topic's Sources give the
	// Topic's plugin to say what it has indexed: finding it, starting it
	// and its answers, all of it. Defaults to PluginGlanceTimeout.
	GlanceTimeout time.Duration
	// IndexPoll is how often study source index asks a plugin how far it
	// is with a Source. Defaults to DefaultIndexPoll.
	IndexPoll time.Duration
	// CancelTimeout is how long a plugin has to stop indexing when study
	// source index is stopped. Defaults to DefaultIndexCancelTimeout.
	CancelTimeout time.Duration
	// EvidenceTimeout is how long a search of a Topic's Sources, and the
	// reading of the Passages a quote is checked against, give the Topic's
	// plugin: finding it, starting it and its answers, all of it. Defaults
	// to PluginEvidenceTimeout.
	EvidenceTimeout time.Duration
	// DiagnoseTimeout is how long study knowledge-base check and study
	// doctor give a plugin to test what it depends on and say what it
	// found, once it has said which contract it speaks. Defaults to the
	// contract's own time for it (plugincontract.DiagnoseTime), which the
	// client gives a plugin when nothing else is said.
	DiagnoseTimeout time.Duration
}

// PluginContract is what a Knowledge base plugin says of itself: the
// version of Lamplight's contract it speaks, and what it can do.
type PluginContract = plugincontract.Contract

// PluginServer is what a Knowledge base plugin calls itself.
type PluginServer = pluginclient.Server

// PluginFinding is one thing a Knowledge base plugin depends on, as the
// plugin itself found it when it tested it: the plugin's name for it,
// whether it is all right, and a sentence of the plugin's. The sentence is
// the plugin's text, on one line and with what a terminal would interpret
// escaped: print it as text from a file all the same.
type PluginFinding = plugincontract.Finding

// PluginCheck reports a Knowledge base plugin that started, or was reached,
// and speaks the contract.
type PluginCheck struct {
	// Name is the name the plugin is registered under.
	Name string `json:"name"`
	// Command is the command that starts it, as it is registered, when it
	// is started by one.
	Command []string `json:"command,omitempty"`
	// Address is where a plugin reached by URL answers: the scheme and host
	// of its registered address. The rest of that address, a path or a
	// query, may be a key, and is shown by study knowledge-base list alone.
	Address string `json:"address,omitempty"`
	// Contract is the contract version it speaks and what it can do.
	Contract PluginContract `json:"contract"`
	// Server is what it calls itself. The text is the plugin's, with what a
	// terminal would interpret escaped.
	Server PluginServer `json:"server"`
	// DataFolder is the folder a plugin started by a command is given to
	// keep its index in: in Lamplight's state folder on this computer,
	// outside the Study home.
	DataFolder string `json:"data_folder,omitempty"`
	// Findings are what the plugin found when it tested what it depends on,
	// an endpoint its settings name say: none for a plugin that depends on
	// nothing of the kind. One that is not all right is no failure of the
	// check: the plugin starts and speaks the contract, and says what it
	// lacks.
	Findings []PluginFinding `json:"findings"`
}

// CheckPlugin starts the Knowledge base plugin registered under name, or
// reaches it at its URL, asks which contract it speaks, has it test what it
// depends on, and stops it again.
//
// A name that is not registered is not_found. A plugin that cannot be
// started, does not start, does not answer in time, is no MCP server or
// does not speak the contract is failed_precondition, with what to do; one
// that speaks a newer contract than this study knows is newer_format; and
// canceled means the command was stopped first. In every case nothing is
// left running.
//
// A plugin that started and speaks the contract, and then did not say what
// it found of what it depends on, in time or as the contract has it, is
// failed_precondition too, whatever code the plugin itself refused with:
// the check did not come to its end. The error says what happened and that
// study uses the plugin all the same, and names no command: there is none
// that would say more (pluginUntested).
//
// What a plugin finds of what it depends on is the learner's question, and
// answering it may take the ten seconds the contract gives: this is for
// the learner at a command line, and no MCP tool reaches it.
//
// A command plugin gets its folder in Lamplight's state folder, outside the
// Study home, which is made when it is not there. Nothing else is written,
// and no Event is recorded: the registry and a plugin's index are this
// computer's.
func (c *Core) CheckPlugin(ctx context.Context, name string) (PluginCheck, error) {
	if len(name) > 64 || !topicIDPattern.MatchString(name) {
		return PluginCheck{}, invalidf("%q is not the name of a Knowledge base plugin: a name is lowercase letters, digits and "+
			"single hyphens, up to 64 characters", name)
	}
	if ctx.Err() != nil {
		return PluginCheck{}, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base " +
			"plugin " + name + " was started"}
	}
	plugin, began, err := c.pluginInTime(ctx, name)
	if err != nil {
		return PluginCheck{}, err
	}
	check, undiagnosed, err := c.checkPlugin(ctx, plugin, began, pluginUse{})
	switch {
	case err != nil:
		return PluginCheck{}, err
	case undiagnosed != nil:
		return PluginCheck{}, undiagnosed
	}
	return check, nil
}

// checkPlugin starts a registered plugin, or reaches it, takes what it says
// of itself, has it test what it depends on, and stops it again. It asks
// the plugin's contract tool, as starting it does, and then its diagnose
// tool, and nothing else.
//
// err is why the plugin did not start, or does not speak the contract: no
// check is returned then. undiagnosed is why a plugin that started and
// speaks the contract did not say what it found of what it depends on: the
// check is whole then, but for its findings (pluginUntested).
//
// This is the one place a plugin's diagnose tool is asked, and only
// CheckPlugin and CheckPlugins lead here, which no MCP tool can name.
func (c *Core) checkPlugin(ctx context.Context, plugin Plugin, began time.Time, use pluginUse) (check PluginCheck, undiagnosed, err error) {
	session, err := c.startPlugin(ctx, plugin, began, use)
	if err != nil {
		return PluginCheck{}, nil, err
	}
	defer session.Close()
	check = PluginCheck{Name: plugin.Name, Command: plugin.Command, Contract: session.Contract(), Server: session.Server(),
		DataFolder: session.DataFolder(), Findings: []PluginFinding{}}
	if plugin.URL != "" {
		check.Address = pluginclient.Address(plugin.URL)
	}
	findings, undiagnosed := session.Diagnose(ctx)
	if undiagnosed != nil {
		return check, pluginUntested(undiagnosed), nil
	}
	check.Findings = findings
	return check, nil, nil
}

// pluginUntested is the error of a plugin that started and speaks the
// contract, and did not say what it found of what it depends on: the
// client's words for what happened to its diagnose tool, and what that
// means for the learner, which is little. status, study source index and a
// search never ask that tool, and go on using the plugin as before.
//
// It names no command. The one that shows more of a plugin is study
// knowledge-base check, which asks the same tool and would say the same.
// A command that was stopped is that, and nothing else.
func pluginUntested(err error) error {
	if CodeOf(err) == CodeCanceled {
		return err
	}
	return &Error{Code: CodeFailedPrecondition, Err: err, Message: strings.TrimSuffix(err.Error(), ".") +
		". So study does not know whether what the plugin depends on is all right, and uses the plugin all the same"}
}

// answeredLate reports whether err is of a plugin that did not answer in the
// time it had, and was given up.
func answeredLate(err error) bool {
	var failed *pluginclient.Error
	return errors.As(err, &failed) && failed.Late
}

// PluginGlance is what a quick check of one registered Knowledge base
// plugin came to.
type PluginGlance struct {
	// Plugin is the plugin as it is registered.
	Plugin Plugin
	// Check is what the plugin said of itself, when it started, or was
	// reached, and speaks the contract, with what it found of what it
	// depends on.
	Check *PluginCheck
	// Problem says why it did not, and Fix what to do about it: the command
	// that puts it right, or the one that says what is wrong with the
	// plugin, having more time for it.
	Problem, Fix string
	// Undiagnosed says why a plugin that started and speaks the contract did
	// not say what it found of what it depends on: it did not answer in the
	// time it has for that, or not as the contract has it. Check is set all
	// the same, without findings. There is no Fix for it: no command says
	// more.
	Undiagnosed string
	// Late says that the plugin did not answer in time when it was asked
	// what it found, and was stopped, or given up. Whoever asks it something
	// else in the same run, what it has indexed say, would wait for it
	// again: study doctor does not (StatusWithout).
	Late bool
}

// PluginGlances is what CheckPlugins found on this computer.
type PluginGlances struct {
	// Registry is the file that holds the registrations, whether or not it
	// exists yet. It is empty when Problem says why the registry could not
	// be read, and what to do about that.
	Registry string
	Problem  string
	// Plugins are the registered plugins, ordered by name.
	Plugins []PluginGlance
}

// CheckPlugins does for every Knowledge base plugin registered on this
// computer what CheckPlugin does for one, and waits for none: it is for
// study doctor. Reading the registry, starting each plugin or reaching it,
// and its saying which contract it speaks share one deadline
// (PluginOptions.GlanceTimeout). The plugins are checked side by side, and
// one that misses the deadline is stopped and reported with the command
// that gives it more time. A plugin study does not use as it is registered
// is not started.
//
// A plugin that has said which contract it speaks is then asked, at once
// and on the same connection, to test what it depends on, and has the time
// the contract gives it for that (PluginOptions.DiagnoseTimeout). Every
// plugin is asked no later than the glance's deadline, so all of them have
// answered, or been stopped, by one moment: that deadline and the time of
// one diagnose after it, however many plugins there are. One that does not
// say in time is reported as that (PluginGlance.Undiagnosed and Late), and
// what it said of itself stands.
//
// A plugin is asked which contract it speaks and what it found of what it
// depends on, and nothing else: nothing is indexed and nothing searched.
// Nothing is written but a command plugin's own folder, which is made when
// it is not there, and no Event is recorded. It fails only when the command
// was stopped.
func (c *Core) CheckPlugins(ctx context.Context) (PluginGlances, error) {
	limit := c.plugins.GlanceTimeout
	if limit <= 0 {
		limit = PluginGlanceTimeout
	}
	stoppedFirst := func() error {
		if !stopped(ctx) {
			return ctx.Err()
		}
		return &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before every Knowledge base plugin " +
			"had said which contract it speaks, and what it found of what it depends on"}
	}
	list, began, err := c.registryInTime(ctx, limit)
	switch {
	case ctx.Err() != nil:
		return PluginGlances{}, stoppedFirst()
	case err != nil:
		// The registry's own words say what to do.
		return PluginGlances{Problem: err.Error()}, nil
	}
	found := PluginGlances{Registry: list.Registry, Plugins: make([]PluginGlance, len(list.Plugins))}
	var wg sync.WaitGroup
	for i := range list.Plugins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found.Plugins[i] = c.glancePlugin(ctx, list, list.Plugins[i], began, limit)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return PluginGlances{}, stoppedFirst()
	}
	return found, nil
}

// glancePlugin checks one plugin of a reading of the registry, which began
// at began, within limit of that.
func (c *Core) glancePlugin(ctx context.Context, list PluginList, plugin Plugin, began time.Time, limit time.Duration) PluginGlance {
	glance := PluginGlance{Plugin: plugin}
	if _, problem, action := unusable(list, nil, plugin.Name); problem != "" {
		glance.Problem, glance.Fix = problem, action
		return glance
	}
	check, undiagnosed, err := c.checkPlugin(ctx, plugin, began, pluginUse{within: limit, grace: PluginGlanceGrace})
	switch {
	case err == nil:
		glance.Check = &check
		if undiagnosed != nil {
			// Which tool was not answered, in how long, or what of its
			// answer is not the contract's.
			glance.Undiagnosed, glance.Late = undiagnosed.Error(), answeredLate(undiagnosed)
		}
	case ctx.Err() == nil && !time.Now().Before(began.Add(limit)):
		glance.Problem = "Knowledge base plugin " + plugin.Name + " did not say which contract it speaks within " + limit.String() +
			", and study did not wait for it"
		glance.Fix = "study knowledge-base check " + plugin.Name
	case CodeOf(err) == CodeNewerFormat:
		glance.Problem, glance.Fix = err.Error(), "upgrade study"
	case len(plugin.Command) > 0 && errors.Is(err, fs.ErrNotExist):
		// Its program is gone: checking it again says the same.
		glance.Problem = err.Error()
		glance.Fix = "install the program again, or register the plugin where its program is now with study knowledge-base add " +
			plugin.Name + " --replace -- <command>"
	default:
		glance.Problem, glance.Fix = err.Error(), "study knowledge-base check "+plugin.Name
	}
	return glance
}

// registered finds the plugin registered under name among the ones a
// reading of the registry gave.
func registered(list PluginList, name string) (Plugin, error) {
	for _, p := range list.Plugins {
		if p.Name == name {
			return p, nil
		}
	}
	return Plugin{}, &Error{Code: CodeNotFound, Message: "no Knowledge base plugin named " + name + " is registered on this " +
		"computer: study knowledge-base list shows the ones that are, and study knowledge-base add registers one"}
}

// pluginInTime finds the plugin registered under name, within the time a
// plugin has to start, and no longer than the command lasts. It returns the
// plugin and when the look began, which is when the time to start it began
// too: finding a plugin and starting it share one deadline.
func (c *Core) pluginInTime(ctx context.Context, name string) (Plugin, time.Time, error) {
	limit := c.plugins.StartTimeout
	if limit <= 0 {
		limit = pluginclient.DefaultStartTimeout
	}
	list, began, err := c.registryInTime(ctx, limit)
	switch {
	case err != nil && ctx.Err() != nil:
		return Plugin{}, began, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base " +
			"plugin " + name + " was started"}
	case err != nil:
		return Plugin{}, began, err
	}
	plugin, err := registered(list, name)
	return plugin, began, err
}

// registryInTime reads the Knowledge base plugin registry within limit, and
// no longer than the command lasts. It returns what the registry holds and
// when the reading began.
//
// Reading it looks at the program of every plugin in it, each on whatever
// drive it is. A drive that does not answer holds such a look for as long
// as it likes, and nothing in this program can interrupt it: so the look is
// left to finish by itself, and the command does not wait for it.
func (c *Core) registryInTime(ctx context.Context, limit time.Duration) (PluginList, time.Time, error) {
	began := time.Now()
	type read struct {
		list PluginList
		err  error
	}
	done := make(chan read, 1)
	go func() {
		if hook := beforePluginLookup.Load(); hook != nil {
			(*hook)()
		}
		list, err := c.Plugins(ctx)
		done <- read{list, err}
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.list, began, r.err
	case <-ctx.Done():
		return PluginList{}, began, ctx.Err()
	case <-timer.C:
		return PluginList{}, began, &Error{Code: CodeFailedPrecondition, Message: "study could not read the Knowledge base " +
			"plugin registry within " + limit.String() + ": reading it, or looking at the program of a plugin registered in " +
			"it, took that long. Check that Lamplight's configuration folder and the programs study knowledge-base list " +
			"shows are on drives that answer"}
	}
}

// beforePluginLookup, when a test sets it, is called before the registry is
// read to find a plugin: where a drive that does not answer would hold the
// look.
var beforePluginLookup atomic.Pointer[func()]

// pluginUse is what a plugin is started for, where that changes how it is
// started. The zero value is a plugin with the time PluginOptions give it.
type pluginUse struct {
	// within, when it is not zero, is how long the plugin has to start and
	// then to answer any one call, a search among them, in place of what
	// PluginOptions say: for whoever keeps one deadline for all of it, and
	// passes it with each call. The time to test what it depends on is not
	// among them: that is the contract's own, whatever else is short.
	within time.Duration
	// grace, when it is not zero, is the most a command has to exit when it
	// is asked, and again when it is signalled, in place of a longer
	// StopGrace.
	grace time.Duration
	// indexes says that the plugin is started to be asked to index, so its
	// folder must be one that can be written in.
	indexes bool
}

// startPlugin starts a registered plugin, or reaches it, and returns the
// Connection with it, which the caller closes: that is what stops a command.
// The time the plugin has to start counts from began, when study set out to
// find it.
func (c *Core) startPlugin(ctx context.Context, plugin pluginregistry.Plugin, began time.Time, use pluginUse) (*pluginclient.Connection, error) {
	// An Environ that returns nothing is an empty environment, not the
	// default.
	environ := []string{}
	if c.plugins.Environ == nil {
		environ = os.Environ()
	} else if given := c.plugins.Environ(); given != nil {
		environ = given
	}
	opts := pluginclient.Options{
		Version:         c.version,
		Environ:         environ,
		Logger:          c.log,
		Began:           began,
		Indexes:         use.indexes,
		StartTimeout:    c.plugins.StartTimeout,
		CallTimeout:     c.plugins.CallTimeout,
		SearchTimeout:   c.plugins.SearchTimeout,
		DiagnoseTimeout: c.plugins.DiagnoseTimeout,
		StopGrace:       c.plugins.StopGrace,
	}
	if use.within > 0 {
		opts.StartTimeout, opts.CallTimeout, opts.SearchTimeout = use.within, use.within, use.within
	}
	if use.grace > 0 && (opts.StopGrace <= 0 || opts.StopGrace > use.grace) {
		opts.StopGrace = use.grace
	}
	return pluginclient.Start(ctx, c.PluginRegistryEnv(), plugin, opts)
}
