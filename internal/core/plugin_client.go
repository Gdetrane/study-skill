package core

import (
	"context"
	"os"
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
// plugin yet, and a test in internal/mcpserver keeps it so until a Topic can
// name one.

// PluginOptions says how Knowledge base plugins are started and how long
// they have. Zero values select the production defaults.
type PluginOptions struct {
	// Environ returns the environment a plugin started by a command gets,
	// before study takes out of it what a plugin must not get. Defaults to
	// os.Environ: the learner's environment.
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
	// Plugin is the plugin as it is registered.
	Plugin Plugin `json:"plugin"`
	// Contract is the contract version it speaks and what it can do.
	Contract PluginContract `json:"contract"`
	// Server is what it calls itself. The text is the plugin's, with what a
	// terminal would interpret escaped.
	Server PluginServer `json:"server"`
	// DataFolder is the folder a plugin started by a command is given to
	// keep its index in, under the Study home's .lamplight.
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
// A command plugin gets its folder under the Study home's .lamplight, which
// is made when it is not there. Nothing else is written, and no Event is
// recorded: the registry and a plugin's index are this computer's.
func (c *Core) CheckPlugin(ctx context.Context, name string) (PluginCheck, error) {
	if len(name) > 64 || !topicIDPattern.MatchString(name) {
		return PluginCheck{}, invalidf("%q is not the name of a Knowledge base plugin: a name is lowercase letters, digits and "+
			"single hyphens, up to 64 characters", name)
	}
	if ctx.Err() != nil {
		return PluginCheck{}, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base " +
			"plugin " + name + " was started"}
	}
	plugin, err := c.plugin(ctx, name)
	if err != nil {
		return PluginCheck{}, err
	}
	session, err := c.startPlugin(ctx, plugin)
	if err != nil {
		return PluginCheck{}, err
	}
	defer session.Close()
	return PluginCheck{Plugin: plugin, Contract: session.Contract(), Server: session.Server(), DataFolder: session.DataFolder()}, nil
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
