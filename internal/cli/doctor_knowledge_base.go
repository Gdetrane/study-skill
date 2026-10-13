package cli

import (
	"context"
	"fmt"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// diagnoseKnowledgeBase is what study doctor says of the Knowledge base
// plugins (ADR-0012): the registry, each plugin registered in it, what each
// plugin found when it tested what it depends on, and, for each Topic that
// names a plugin, what the plugin says of the Topic's Sources when there is
// something to say.
//
// A plugin is checked as study knowledge-base check checks it, and asked
// what it has indexed as study status asks it, each time with the couple of
// seconds of a glance (core.PluginGlanceTimeout): the doctor waits for no
// plugin, and has none index. A plugin that failed its check is not asked
// what it has indexed: one that does not answer is waited for once. What is
// said of a Topic's Sources, and the one command named for them, are
// status's own (core.Indexing): a Source whose indexing failed is shown with
// its reason, and is nothing to run a command for here either.
//
// A plugin that speaks the contract is asked, on the same connection, to
// test what it depends on, an endpoint its settings name say, and has the
// contract's own time for that (core.PluginDiagnoseTimeout): all plugins at
// once, so the doctor waits that long once, however many are registered.
// Each finding of a plugin is a Finding of the doctor's, under the plugin's
// name and the finding's, with the plugin's sentence. One that is not all
// right is a warning and has no fix: the words are the plugin's, and study
// adds no command to them. A plugin with nothing to test adds no Finding.
//
// None of it makes the setup unhealthy. A Topic whose plugin cannot be
// asked works as one without a Knowledge base, and one whose embedding
// endpoint is down still searches by keyword.
func (a *app) diagnoseKnowledgeBase(ctx context.Context) []core.Finding {
	c, err := core.Open(a.opts)
	if err != nil {
		// The Study home cannot be found, and the doctor has said why.
		return nil
	}
	registry := core.Finding{Name: "knowledge_base", Status: core.FindingOK}
	glances, err := c.CheckPlugins(ctx)
	switch {
	case err != nil:
		registry.Status, registry.Message = core.FindingWarn, err.Error()
		return []core.Finding{registry}
	case glances.Problem != "":
		// The registry's own words say what to do, and every Topic that
		// names a plugin would repeat them.
		registry.Status, registry.Message = core.FindingWarn, glances.Problem
		return []core.Finding{registry}
	case len(glances.Plugins) == 0:
		registry.Message = "no Knowledge base plugin is registered on this computer: study setup registers Shelf when " +
			shelfProgram + " is installed"
	default:
		registry.Message = glances.Registry
	}
	out := []core.Finding{registry}
	// wanting are the plugins whose own Finding says what is wrong, and what
	// to do. A Topic that names one has nothing to add, and the plugin is not
	// asked a second time: one that did not answer would be waited for again.
	var wanting []string
	for _, g := range glances.Plugins {
		f := core.Finding{Name: "knowledge_base:" + g.Plugin.Name, Status: core.FindingOK}
		switch {
		case g.Check == nil:
			f.Status, f.Message, f.Fix = core.FindingWarn, g.Problem, g.Fix
			wanting = append(wanting, g.Plugin.Name)
		case g.Undiagnosed != "":
			// It starts and speaks the contract, and did not say what it
			// found of what it depends on: its Sources are still asked for.
			f.Status, f.Message, f.Fix = core.FindingWarn, g.Undiagnosed, "study knowledge-base check "+g.Plugin.Name
		default:
			f.Message = describeCheck(*g.Check)
		}
		out = append(out, f)
		if g.Check == nil {
			continue
		}
		for _, found := range g.Check.Findings {
			tested := core.Finding{Name: f.Name + ":" + found.Name, Status: core.FindingOK, Message: found.Message}
			if !found.OK {
				tested.Status = core.FindingWarn
			}
			out = append(out, tested)
		}
	}
	status, err := c.StatusWithout(ctx, wanting)
	if err != nil {
		// What keeps the Topics from being read is the doctor's to say, and
		// it has.
		return out
	}
	for _, t := range status.Topics {
		x := t.Indexing
		if x == nil {
			continue
		}
		f := core.Finding{Name: "indexing:" + t.ID, Status: core.FindingOK, Message: x.Problem, Fix: x.Action}
		if f.Message == "" {
			if f.Message = describeIndexing(x.Sources); f.Message == "" {
				// Every Source is indexed.
				continue
			}
		}
		if x.Problem != "" || x.Action != "" {
			f.Status = core.FindingWarn
		}
		out = append(out, f)
	}
	return out
}

// describeCheck says of a plugin that was checked how study starts it, or
// where it reaches it, and which contract it speaks.
func describeCheck(r core.PluginCheck) string {
	how := "started by " + describePlugin(core.Plugin{Command: r.Command})
	if r.Address != "" {
		// Its scheme and host: the rest of the address may be a key.
		how = "reached at " + printable(r.Address)
	}
	return fmt.Sprintf("%s, speaks version %d of Lamplight's contract", how, r.Contract.Version)
}
