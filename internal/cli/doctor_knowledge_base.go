package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// diagnoseKnowledgeBase is what study doctor says of the Knowledge base
// plugins (ADR-0012): the registry, each plugin registered in it, what each
// plugin found when it tested what it depends on, and, for each Topic that
// names a plugin, what the plugin says of the Topic's Sources when there is
// something to say.
//
// A plugin is checked as study knowledge-base check checks it, and asked
// what it has indexed as study status asks it, and has none index. It has
// the couple of seconds of a glance (core.PluginGlanceTimeout) to start and
// say which contract it speaks, and the same again, later, to say what it
// has indexed. A plugin that failed its check is not asked what it has
// indexed: one that does not answer is waited for once. What is said of a
// Topic's Sources, and the one command named for them, are status's own
// (core.Indexing): a Source whose indexing failed is shown with its reason,
// and is nothing to run a command for here either.
//
// A plugin that speaks the contract is asked, on the same connection, to
// test what it depends on, an endpoint its settings name say, and has the
// contract's own time for that (plugincontract.DiagnoseTime, ten seconds):
// all plugins at once, so the doctor waits that long once, however many are
// registered. So the doctor waits for plugins fourteen seconds at most: two,
// ten, and two for the Sources.
//
// Each finding of a plugin is a Finding of the doctor's, under the plugin's
// name and the finding's, with the plugin's sentence. One that is not all
// right is a warning and has no fix: the words are the plugin's, and study
// adds no command to them. A plugin with nothing to test adds no Finding.
// study cannot know which finding keeps a Source from being indexed, so the
// Finding of a Topic whose plugin has one that is not all right names it,
// beside the command it gives: that command may fail until it is put right.
//
// A plugin that speaks the contract and did not say what it found, in time
// or as the contract has it, is a warning with no fix either: no command
// says more of it, and study uses it all the same. One that was late is not
// asked for its Sources, which would be waiting for it a second time.
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
	// wanting are the plugins whose own Finding says what is wrong. A Topic
	// that names one has nothing to add, and the plugin is not asked a second
	// time: one that did not answer would be waited for again. wrong are the
	// Findings of what each plugin found not all right.
	var wanting []string
	wrong := map[string][]string{}
	for _, g := range glances.Plugins {
		f := core.Finding{Name: "knowledge_base:" + g.Plugin.Name, Status: core.FindingOK}
		switch {
		case g.Check == nil:
			f.Status, f.Message, f.Fix = core.FindingWarn, g.Problem, g.Fix
			wanting = append(wanting, g.Plugin.Name)
		case g.Undiagnosed != "":
			// It starts and speaks the contract, and did not say what it
			// found of what it depends on. There is nothing to run: the
			// command that shows more of a plugin would say this again.
			f.Status, f.Message = core.FindingWarn, g.Undiagnosed
			if g.Late {
				// Its Sources are asked for only when it answered at all.
				f.Message += ". study doctor did not wait for it again, to ask which Sources it has indexed"
				wanting = append(wanting, g.Plugin.Name)
			}
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
				wrong[g.Plugin.Name] = append(wrong[g.Plugin.Name], tested.Name)
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
		if names := wrong[x.Plugin]; len(names) > 0 {
			// Which of them keeps a Source from being indexed is the
			// plugin's to know: the command stays, with what may stop it.
			f.Message += fmt.Sprintf(". Knowledge base plugin %s found %s not all right: indexing may fail until %s put right",
				printable(x.Plugin), strings.Join(names, " and "), plural(len(names), "it is", "they are"))
		}
		out = append(out, f)
	}
	return out
}

// doctorStopped is the error of a study doctor that was stopped, by SIGTERM
// or Ctrl-C, before it had looked at everything: it is canceled, as any
// command that was stopped, and the doctor gives no verdict. What it found
// until then is not shown either: a plugin it was waiting for would be among
// it as a warning, under "Ready to study".
func doctorStopped(ctx context.Context) error {
	if !errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return &core.Error{Code: core.CodeCanceled, Err: ctx.Err(), Message: "study doctor was stopped before it had looked at " +
		"everything, and says nothing of your setup"}
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
