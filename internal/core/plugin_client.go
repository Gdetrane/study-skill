package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
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
//     the plugin speaks, and CheckPlugins, for study doctor, which asks
//     every registered plugin the same with the short deadline of a glance.
//     Both are for the learner and CLI-only.
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
// passage tools, and can never have one index: the MCP server runs outside
// the agent's sandbox, and a plugin that indexed for it would read any file
// the agent named as a Source there. A search reads the plugin's index and
// never a Source. Tests in internal/mcpserver keep it so: one calls every
// tool with plugins registered that record what they are asked, and one
// reads this package for whatever could ask a plugin to index from anywhere
// but IndexSources, or to search from anywhere but the two functions that
// do.
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
}

// PluginContract is what a Knowledge base plugin says of itself: the
// version of Lamplight's contract it speaks, and what it can do.
type PluginContract = plugincontract.Contract

// PluginServer is what a Knowledge base plugin calls itself.
type PluginServer = pluginclient.Server

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
}

// CheckPlugin starts the Knowledge base plugin registered under name, or
// reaches it at its URL, asks which contract it speaks, and stops it again.
//
// A name that is not registered is not_found. A plugin that cannot be
// started, does not start, does not answer in time, is no MCP server or
// does not speak the contract is failed_precondition, with what to do; one
// that speaks a newer contract than this study knows is newer_format; and
// canceled means the command was stopped first. In every case nothing is
// left running.
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
	return c.checkPlugin(ctx, plugin, began, pluginUse{})
}

// checkPlugin starts a registered plugin, or reaches it, takes what it says
// of itself, and stops it again. It asks the plugin's contract tool, as
// starting it does, and nothing else.
func (c *Core) checkPlugin(ctx context.Context, plugin Plugin, began time.Time, use pluginUse) (PluginCheck, error) {
	session, err := c.startPlugin(ctx, plugin, began, use)
	if err != nil {
		return PluginCheck{}, err
	}
	defer session.Close()
	check := PluginCheck{Name: plugin.Name, Command: plugin.Command, Contract: session.Contract(), Server: session.Server(),
		DataFolder: session.DataFolder()}
	if plugin.URL != "" {
		check.Address = pluginclient.Address(plugin.URL)
	}
	return check, nil
}

// PluginGlance is what a quick check of one registered Knowledge base
// plugin came to.
type PluginGlance struct {
	// Plugin is the plugin as it is registered.
	Plugin Plugin
	// Check is what the plugin said of itself, when it started, or was
	// reached, and speaks the contract.
	Check *PluginCheck
	// Problem says why it did not, and Fix what to do about it: the command
	// that puts it right, or the one that says what is wrong with the
	// plugin, having more time for it.
	Problem, Fix string
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
// and its answer share one deadline (PluginOptions.GlanceTimeout). The
// plugins are checked side by side, and one that misses the deadline is
// stopped and reported with the command that gives it more time. A plugin
// study does not use as it is registered is not started.
//
// A plugin is asked which contract it speaks and nothing else: nothing is
// indexed and nothing searched. Nothing is written but a command plugin's
// own folder, which is made when it is not there, and no Event is recorded.
// It fails only when the command was stopped.
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
			"had said which contract it speaks"}
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
	check, err := c.checkPlugin(ctx, plugin, began, pluginUse{within: limit, grace: PluginGlanceGrace})
	switch {
	case err == nil:
		glance.Check = &check
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
	// passes it with each call.
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
		Version:       c.version,
		Environ:       environ,
		Logger:        c.log,
		Began:         began,
		Indexes:       use.indexes,
		StartTimeout:  c.plugins.StartTimeout,
		CallTimeout:   c.plugins.CallTimeout,
		SearchTimeout: c.plugins.SearchTimeout,
		StopGrace:     c.plugins.StopGrace,
	}
	if use.within > 0 {
		opts.StartTimeout, opts.CallTimeout, opts.SearchTimeout = use.within, use.within, use.within
	}
	if use.grace > 0 && (opts.StopGrace <= 0 || opts.StopGrace > use.grace) {
		opts.StopGrace = use.grace
	}
	return pluginclient.Start(ctx, c.PluginRegistryEnv(), plugin, opts)
}
