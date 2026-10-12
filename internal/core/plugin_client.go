package core

import (
	"context"
	"os"
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
// So far one operation does: CheckPlugin, for study knowledge-base check,
// which is for the learner and CLI-only. No MCP tool starts or reaches a
// plugin yet, and tests in internal/mcpserver keep it so until a Topic can
// name one: one calls every tool with plugins registered that record being
// started or reached, and one reads this package and that one for whatever
// could start a plugin from anywhere but CheckPlugin.

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
	plugin, err := c.pluginInTime(ctx, name)
	if err != nil {
		return PluginCheck{}, err
	}
	session, err := c.startPlugin(ctx, plugin)
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

// plugin finds the Knowledge base plugin registered under name on this
// computer.
func (c *Core) plugin(ctx context.Context, name string) (Plugin, error) {
	list, err := c.Plugins(ctx)
	if err != nil {
		return Plugin{}, err
	}
	for _, p := range list.Plugins {
		if p.Name == name {
			return p, nil
		}
	}
	return Plugin{}, &Error{Code: CodeNotFound, Message: "no Knowledge base plugin named " + name + " is registered on this " +
		"computer: study knowledge-base list shows the ones that are, and study knowledge-base add registers one"}
}

// pluginInTime finds the plugin as plugin does, within the time a plugin
// has to start, and no longer than the command lasts.
//
// Finding it reads the registry and looks at the program of every plugin
// in it, each on whatever drive it is. A drive that does not answer holds
// such a look for as long as it likes, and nothing in this program can
// interrupt it: so the look is left to finish by itself, and the command
// does not wait for it.
func (c *Core) pluginInTime(ctx context.Context, name string) (Plugin, error) {
	limit := c.plugins.StartTimeout
	if limit <= 0 {
		limit = pluginclient.DefaultStartTimeout
	}
	type found struct {
		plugin Plugin
		err    error
	}
	done := make(chan found, 1)
	go func() {
		if hook := beforePluginLookup.Load(); hook != nil {
			(*hook)()
		}
		plugin, err := c.plugin(ctx, name)
		done <- found{plugin, err}
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case f := <-done:
		return f.plugin, f.err
	case <-ctx.Done():
		return Plugin{}, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base " +
			"plugin " + name + " was started"}
	case <-timer.C:
		return Plugin{}, &Error{Code: CodeFailedPrecondition, Message: "study could not read the Knowledge base plugin registry " +
			"within " + limit.String() + ": reading it, or looking at the program of a plugin registered in it, took that " +
			"long. Check that Lamplight's configuration folder and the programs study knowledge-base list shows are on " +
			"drives that answer"}
	}
}

// beforePluginLookup, when a test sets it, is called before the registry is
// read to find a plugin: where a drive that does not answer would hold the
// look.
var beforePluginLookup atomic.Pointer[func()]

// startPlugin starts a registered plugin, or reaches it, and returns the
// Connection with it, which the caller closes: that is what stops a command.
func (c *Core) startPlugin(ctx context.Context, plugin pluginregistry.Plugin) (*pluginclient.Connection, error) {
	// An Environ that returns nothing is an empty environment, not the
	// default.
	environ := []string{}
	if c.plugins.Environ == nil {
		environ = os.Environ()
	} else if given := c.plugins.Environ(); given != nil {
		environ = given
	}
	return pluginclient.Start(ctx, c.PluginRegistryEnv(), plugin, pluginclient.Options{
		Version:       c.version,
		Environ:       environ,
		Logger:        c.log,
		StartTimeout:  c.plugins.StartTimeout,
		CallTimeout:   c.plugins.CallTimeout,
		SearchTimeout: c.plugins.SearchTimeout,
		StopGrace:     c.plugins.StopGrace,
	})
}
