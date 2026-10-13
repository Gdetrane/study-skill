package core_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// CheckPlugin has the plugin test what it depends on, once it has said which
// contract it speaks, and reports what the plugin found: a finding that is
// not all right is the plugin's answer, and no failure of the check.
//
// A plugin that does not say in time, or not as the contract has it, fails
// the check with failed_precondition, whatever code the plugin itself
// refused with: invalid_argument would be said of what the learner typed,
// and not_found of a name that is not registered. The error says what
// happened to that tool and that study uses the plugin all the same, never
// that the plugin does not speak the contract, and names no command: the
// one that shows more of a plugin is this check.
func TestCheckPluginReportsWhatThePluginFound(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record,
		"--finding", "embedding=ok:the embedding endpoint 127.0.0.1:8080 gave a vector of 512 numbers",
		"--finding", "reranking=wrong:the reranking endpoint 127.0.0.1:8081 could not be reached")...)
	got, err := pluginCore(t, c, "", 20*time.Second).CheckPlugin(ctx, "kb")
	want := []core.PluginFinding{
		{Name: "embedding", OK: true, Message: "the embedding endpoint 127.0.0.1:8080 gave a vector of 512 numbers"},
		{Name: "reranking", Message: "the reranking endpoint 127.0.0.1:8081 could not be reached"},
	}
	if err != nil || !reflect.DeepEqual(got.Findings, want) || got.Contract.Version != 1 {
		t.Fatalf("CheckPlugin = %+v, %v; want the two findings, in the plugin's order", got, err)
	}
	if tools := plugintest.Tools(plugintest.ReadCalls(t, record)); !slices.Equal(tools, []string{"contract", "diagnose"}) {
		t.Errorf("the plugin was asked %v; want its contract, and then what it found of what it depends on", tools)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}

	// A plugin reached by URL is asked the same.
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Findings: want[:1]})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	defer service.Close()
	c.RegisterURL(t, "remote", server.URL+"/mcp")
	if got, err := pluginCore(t, c, "", 20*time.Second).CheckPlugin(ctx, "remote"); err != nil || !reflect.DeepEqual(got.Findings, want[:1]) {
		t.Errorf("CheckPlugin of a service = %+v, %v", got, err)
	}

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"it says nothing": {[]string{"--hang", "diagnose", "--stderr", "testing the endpoint"},
			"Knowledge base plugin silent did not answer its diagnose tool within 400ms, so study stopped it. The last it wrote on " +
				"its standard error: testing the endpoint. So study does not know"},
		"it says what is no finding": {[]string{"--result", `diagnose={"findings": [{"name": "Embedding", "ok": true, "message": "x"}]}`},
			"the diagnose tool of Knowledge base plugin silent gave an answer that breaks the contract: " +
				`findings[0] is named "Embedding", which is no name`},
		"it fails": {[]string{"--refuse", "diagnose=internal:Shelf's settings file cannot be read"},
			"Knowledge base plugin silent failed in its diagnose tool: Shelf's settings file cannot be read. So study does not know"},
		"it finds what it was given wrong": {[]string{"--refuse", "diagnose=invalid_argument:Shelf's settings file cannot be read"},
			"Knowledge base plugin silent does not take what its diagnose tool was given: Shelf's settings file cannot be read. So"},
		"it finds what it was asked for missing": {[]string{"--refuse", "diagnose=not_found:Shelf's settings file cannot be read"},
			"Knowledge base plugin silent does not have what its diagnose tool was asked for: Shelf's settings file cannot be read. So"},
		"it cannot": {[]string{"--refuse", "diagnose=unsupported:Shelf's settings file cannot be read"},
			"Knowledge base plugin silent cannot do what its diagnose tool was asked: Shelf's settings file cannot be read. So"},
		"it fails with a code of its own": {[]string{"--refuse", "diagnose=newer_format:Shelf's settings file cannot be read"},
			"Knowledge base plugin silent failed in its diagnose tool: Shelf's settings file cannot be read. So study does not know"},
		"it fails in words": {[]string{"--plain-error", "diagnose=Shelf's settings file cannot be read"},
			"Knowledge base plugin silent failed in its diagnose tool: Shelf's settings file cannot be read. So study does not know"},
	} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			record := filepath.Join(t.TempDir(), "record.json")
			c.Register(t, "silent", plugintest.Command(append([]string{"--record", record}, tc.args...)...)...)
			k := studyOn(t, c, "", core.PluginOptions{StartTimeout: 20 * time.Second, DiagnoseTimeout: 400 * time.Millisecond})
			began := time.Now()
			got, err := k.CheckPlugin(ctx, "silent")
			if took := time.Since(began); took > busy {
				t.Errorf("CheckPlugin took %s", took)
			}
			if core.CodeOf(err) != core.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) || !reflect.DeepEqual(got, core.PluginCheck{}) {
				t.Fatalf("CheckPlugin = %+v, %v (%s); want failed_precondition with %q, and no check", got, err, core.CodeOf(err), tc.want)
			}
			if !strings.HasSuffix(err.Error(), "So study does not know whether what the plugin depends on is all right, and uses the "+
				"plugin all the same") {
				t.Errorf("the error does not end with what it means for the learner: %v", err)
			}
			for _, not := range []string{"does not speak Lamplight's contract", "study knowledge-base", "Run its command", ".."} {
				if strings.Contains(err.Error(), not) {
					t.Errorf("the error says %q, of a plugin that starts and that study goes on using: %v", not, err)
				}
			}
			if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
				plugintest.Kill(started.PID)
				t.Errorf("the plugin, process %d, was left running", started.PID)
			}
		})
	}
}

// The time a plugin has to test what it depends on is the contract's, and
// not the time it has to start or to answer another call: a plugin that
// takes a moment over it, with everything else short, is waited for.
func TestThePluginsTimeToDiagnoseIsItsOwn(t *testing.T) {
	c := plugintest.NewComputer(t)
	c.Register(t, "kb", plugintest.Command("--delay", "diagnose=700ms", "--finding", "embedding=ok:it answers")...)
	k := studyOn(t, c, "", core.PluginOptions{StartTimeout: 20 * time.Second, CallTimeout: 100 * time.Millisecond,
		SearchTimeout: 100 * time.Millisecond, GlanceTimeout: 20 * time.Second})
	if got, err := k.CheckPlugin(context.Background(), "kb"); err != nil || len(got.Findings) != 1 {
		t.Errorf("CheckPlugin of a plugin that takes 700ms to say what it found = %+v, %v", got, err)
	}
	// And for study doctor, whose glance is short: half a second for the
	// plugin to start and say which contract it speaks, and its own time for
	// the rest.
	hurried := studyOn(t, c, "", core.PluginOptions{GlanceTimeout: 500 * time.Millisecond})
	found, err := hurried.CheckPlugins(context.Background())
	if err != nil || len(found.Plugins) != 1 || found.Plugins[0].Check == nil || found.Plugins[0].Undiagnosed != "" ||
		len(found.Plugins[0].Check.Findings) != 1 {
		t.Errorf("CheckPlugins, with a glance shorter than the plugin's 700ms = %+v, %v; want its finding", found, err)
	}
	if plugincontract.DiagnoseTime <= core.PluginGlanceTimeout {
		t.Errorf("a plugin has %s to test what it depends on, and %s for a glance: testing asks over a network, and needs more",
			plugincontract.DiagnoseTime, core.PluginGlanceTimeout)
	}
}

// study doctor asks every registered plugin what it found, all of them side
// by side, each once it has said which contract it speaks. However many of
// them say nothing, the wait is the time one of them has, and what each said
// of itself stands: a plugin that does not say what it found is reported as
// that, and no other plugin's findings are lost for it.
func TestCheckPluginsAsksEveryPluginAtOnceWhatItFound(t *testing.T) {
	const patience, silent = time.Second, 6
	c := plugintest.NewComputer(t)
	records := map[string]string{}
	for n := range silent {
		name := fmt.Sprintf("silent%d", n)
		records[name] = filepath.Join(t.TempDir(), "record.json")
		c.Register(t, name, plugintest.Command("--record", records[name], "--hang", "diagnose", "--stderr", "testing the endpoint")...)
	}
	c.Register(t, "broken", plugintest.Command("--result", `diagnose={"findings": [{"name": "embedding", "ok": true, "message": " "}]}`)...)
	c.Register(t, "fine", plugintest.Command("--finding", "embedding=wrong:the embedding endpoint 127.0.0.1:8080 answered 503")...)
	c.Register(t, "nothing", plugintest.Command()...)
	c.Register(t, "unsaid", plugintest.Command("--hang", "start")...)

	// Two seconds for a plugin to start and say which contract it speaks,
	// which one of them never does, and then its time to say what it found.
	k := studyOn(t, c, "", core.PluginOptions{GlanceTimeout: 2 * time.Second, DiagnoseTimeout: patience, StartTimeout: 30 * time.Second})
	began := time.Now()
	found, err := k.CheckPlugins(context.Background())
	took := time.Since(began)
	if err != nil || len(found.Plugins) != silent+4 {
		t.Fatalf("CheckPlugins = %+v, %v", found, err)
	}
	// The glance, and one plugin's time after it: asked one after the
	// other, the six alone would take six times that.
	if most := 2*time.Second + patience + time.Second; took > most {
		t.Errorf("CheckPlugins took %s over %d plugins that say nothing of what they found, each with %s: want one wait for all, "+
			"within %s", took, silent, patience, most)
	}
	got := map[string]core.PluginGlance{}
	for _, glance := range found.Plugins {
		got[glance.Plugin.Name] = glance
	}
	for name, record := range records {
		g := got[name]
		if g.Check == nil || g.Problem != "" || g.Fix != "" || len(g.Check.Findings) != 0 || g.Check.Contract.Version != 1 || !g.Late ||
			g.Undiagnosed != "Knowledge base plugin "+name+" did not answer its diagnose tool within 1s, so study stopped it. The last it "+
				"wrote on its standard error: testing the endpoint. So study does not know whether what the plugin depends on is all "+
				"right, and uses the plugin all the same" {
			t.Errorf("%s = %+v (%+v); want what it said of itself, and that it was late to say what it found within 1s", name, g, g.Check)
		}
		if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
			plugintest.Kill(started.PID)
			t.Errorf("%s, process %d, was left running", name, started.PID)
		}
	}
	if g := got["broken"]; g.Check == nil || g.Problem != "" || g.Fix != "" || g.Late ||
		!strings.HasPrefix(g.Undiagnosed, "the diagnose tool of Knowledge base plugin broken gave an answer that breaks the contract: "+
			"the finding embedding has no message. docs/knowledge-base-contract.md describes the contract. So study does not know") {
		t.Errorf("a plugin whose findings break the contract = %+v; want that said, and the plugin not late: it answered", g)
	}
	want := []core.PluginFinding{{Name: "embedding", Message: "the embedding endpoint 127.0.0.1:8080 answered 503"}}
	if g := got["fine"]; g.Check == nil || g.Problem != "" || g.Undiagnosed != "" || g.Late || !reflect.DeepEqual(g.Check.Findings, want) {
		t.Errorf("a plugin with a finding that is not all right = %+v (%+v); want the finding, and nothing wrong with the plugin", g, g.Check)
	}
	if g := got["nothing"]; g.Check == nil || g.Problem != "" || g.Undiagnosed != "" || g.Check.Findings == nil || len(g.Check.Findings) != 0 {
		t.Errorf("a plugin that depends on nothing = %+v (%+v); want no finding, and nothing wrong", g, g.Check)
	}
	// One that never said which contract it speaks was asked nothing more.
	if g := got["unsaid"]; g.Check != nil || g.Undiagnosed != "" || !strings.Contains(g.Problem, "did not say which contract it speaks within 2s") {
		t.Errorf("a plugin that never said which contract it speaks = %+v", g)
	}
}

// A plugin that depends on nothing costs study doctor no wait: it answers
// that it has nothing to test, and is gone.
func TestAPluginWithNothingToTestCostsNoWait(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	began := time.Now()
	found, err := studyOn(t, c, "", core.PluginOptions{}).CheckPlugins(context.Background())
	if took := time.Since(began); took > core.PluginGlanceTimeout {
		t.Errorf("CheckPlugins took %s over one plugin with nothing to test; want it within the %s of a glance", took, core.PluginGlanceTimeout)
	}
	if err != nil || len(found.Plugins) != 1 || found.Plugins[0].Check == nil || len(found.Plugins[0].Check.Findings) != 0 ||
		found.Plugins[0].Undiagnosed != "" {
		t.Errorf("CheckPlugins = %+v, %v", found, err)
	}
	if tools := plugintest.Tools(plugintest.ReadCalls(t, record)); !slices.Equal(tools, []string{"contract", "diagnose"}) {
		t.Errorf("the plugin was asked %v", tools)
	}
}

// Stopped while a plugin tests what it depends on, CheckPlugins says so, and
// CheckPlugin too: neither reports half a check.
func TestCheckingAPluginStoppedWhileItTests(t *testing.T) {
	for name, check := range map[string]func(ctx context.Context, k *core.Core) error{
		"study knowledge-base check": func(ctx context.Context, k *core.Core) error { _, err := k.CheckPlugin(ctx, "kb"); return err },
		"study doctor": func(ctx context.Context, k *core.Core) error {
			found, err := k.CheckPlugins(ctx)
			if len(found.Plugins) != 0 {
				return fmt.Errorf("it reports %+v of a check that was stopped (%w)", found.Plugins, err)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			record := filepath.Join(t.TempDir(), "record.json")
			c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "diagnose")...)
			k := studyOn(t, c, "", core.PluginOptions{StartTimeout: 20 * time.Second, GlanceTimeout: 20 * time.Second})
			ctx, stop := context.WithCancel(context.Background())
			// Once the plugin was asked what it found.
			go func() {
				for range 800 {
					if slices.Contains(plugintest.Tools(plugintest.ReadCalls(t, record)), "diagnose") {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				stop()
			}()
			began := time.Now()
			err := check(ctx, k)
			if took := time.Since(began); took > busy {
				t.Errorf("it took %s to end once the command was stopped", took)
			}
			if core.CodeOf(err) != core.CodeCanceled {
				t.Errorf("stopped while the plugin tested what it depends on: %v (%s); want canceled", err, core.CodeOf(err))
			}
			if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
				plugintest.Kill(started.PID)
				t.Errorf("the plugin, process %d, was left running", started.PID)
			}
		})
	}
}
