package core_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// CheckPlugins checks every plugin the registry holds, as study doctor has
// it: one that starts, or answers, and speaks the contract is reported with
// what it said of itself, and every other with why not and what to do. Each
// is asked its contract and nothing else, and none is left running.
func TestCheckPluginsChecksEveryRegisteredPlugin(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	good := c.Register(t, "good", plugintest.Command("--record", record, "--name", "a-plugin", "--version", "2.0.0")...)
	service := plugintest.New(plugintest.Config{Folder: t.TempDir(), Name: "a-service"})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	defer service.Close()
	c.RegisterURL(t, "remote", server.URL+"/t0ken-in-the-path/mcp")
	// A program that was there when it was registered, and is gone.
	gone := filepath.Join(c.Home, "bin", "gone-kb")
	c.Register(t, "gone", gone)
	c.Register(t, "newer", plugintest.Command("--contract", `{"version": 2}`)...)
	c.Register(t, "no-mcp", plugintest.Command("--garbage")...)
	// A program the agent can write is not started.
	inside := filepath.Join(c.StudyHome, "linear-algebra", "kb")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	insideRecord := filepath.Join(t.TempDir(), "inside.json")
	if err := os.WriteFile(inside, []byte("#!"+plugintest.Command()[0]+" "+plugintest.Arg+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Register(t, "inside", inside, "--record", insideRecord)

	began := time.Now()
	found, err := studyOn(t, c, "", core.PluginOptions{}).CheckPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > busy {
		t.Errorf("CheckPlugins took %s", took)
	}
	if found.Registry != filepath.Join(c.ConfigDir, "knowledge-base-plugins.json") || found.Problem != "" {
		t.Errorf("CheckPlugins = %+v; want the registry, and no problem with it", found)
	}
	got := map[string]core.PluginGlance{}
	var names []string
	for _, glance := range found.Plugins {
		got[glance.Plugin.Name] = glance
		names = append(names, glance.Plugin.Name)
	}
	if want := []string{"gone", "good", "inside", "newer", "no-mcp", "remote"}; !slices.Equal(names, want) {
		t.Fatalf("CheckPlugins checked %v, want %v, in the registry's order", names, want)
	}

	wantGood := &core.PluginCheck{Name: "good", Command: good.Command, DataFolder: c.DataFolder("good"),
		Contract: core.PluginContract{Version: 1, Sources: []string{"file", "url"}, Search: []string{"keyword"}, Locations: []string{"pages"}},
		Server:   core.PluginServer{Name: "a-plugin", Version: "2.0.0"}, Findings: []core.PluginFinding{}}
	if g := got["good"]; g.Problem != "" || g.Fix != "" || !reflect.DeepEqual(g.Check, wantGood) {
		t.Errorf("a plugin that speaks the contract = %+v (%+v); want %+v", g, g.Check, wantGood)
	}
	// A service is reported by its scheme and host: the rest may be a key.
	if g := got["remote"]; g.Problem != "" || g.Check == nil || g.Check.Address != server.URL || g.Check.Server.Name != "a-service" {
		t.Errorf("a plugin reached by URL = %+v (%+v)", g, g.Check)
	}
	for name, want := range map[string]struct{ problem, fix string }{
		"gone": {`Knowledge base plugin gone cannot be started: there is no program at "` + gone + `"`,
			"install the program again, or register the plugin where its program is now with study knowledge-base add gone --replace -- <command>"},
		"newer":  {"speaks version 2 of Lamplight's contract, and this version of study knows version 1", "upgrade study"},
		"no-mcp": {"Knowledge base plugin no-mcp", "study knowledge-base check no-mcp"},
		"inside": {"Knowledge base plugin inside is registered on this computer, and study does not use it: its program is inside the Study home",
			"study knowledge-base list"},
	} {
		if g := got[name]; g.Check != nil || !strings.Contains(g.Problem, want.problem) || g.Fix != want.fix {
			t.Errorf("%s = %+v\nwant no check, a problem with %q and the fix %q", name, g, want.problem, want.fix)
		}
	}

	// The one that was started was asked which contract it speaks and what
	// it found of what it depends on, and is gone; the one inside the Study
	// home was never started.
	if tools := plugintest.Tools(plugintest.ReadCalls(t, record)); !slices.Equal(tools, []string{"contract", "diagnose"}) {
		t.Errorf("the plugin was asked %v, want its contract, what it found of what it depends on, and nothing else", tools)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
	if _, err := os.Stat(insideRecord); err == nil {
		t.Error("a plugin whose program is inside the Study home was started")
	}
	if left := tree(t, c.StudyHome); !reflect.DeepEqual(left, []string{"linear-algebra/", "linear-algebra/kb"}) {
		t.Errorf("checking the plugins wrote in the Study home: %v", left)
	}
}

// A plugin that says nothing is not waited for: it has the time of a glance,
// is stopped, and is reported with the command that gives it more.
func TestCheckPluginsWaitsForNoPlugin(t *testing.T) {
	const patience = 500 * time.Millisecond
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "start")...)
	// Thirty seconds to start are what study knowledge-base check gives it.
	k := studyOn(t, c, "", core.PluginOptions{GlanceTimeout: patience, StartTimeout: 30 * time.Second})
	began := time.Now()
	found, err := k.CheckPlugins(context.Background())
	if took := time.Since(began); took > busy {
		t.Errorf("CheckPlugins took %s over a plugin that says nothing, with %s for it", took, patience)
	}
	if err != nil || len(found.Plugins) != 1 || found.Plugins[0].Check != nil ||
		found.Plugins[0].Problem != "Knowledge base plugin kb did not say which contract it speaks within 500ms, and study did not wait for it" ||
		found.Plugins[0].Fix != "study knowledge-base check kb" {
		t.Errorf("CheckPlugins = %+v, %v", found, err)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
}

// A plugin that was only asked which contract it speaks has nothing to
// finish: one that stays when its input closes is given a quarter of a
// second, and again once it is signalled, whatever a plugin that indexes is
// given.
func TestCheckPluginsGivesAPluginLittleTimeToLeave(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record, "--linger")...)
	k := studyOn(t, c, "", core.PluginOptions{StopGrace: 15 * time.Second})
	began := time.Now()
	found, err := k.CheckPlugins(context.Background())
	if took := time.Since(began); took > busy {
		t.Errorf("CheckPlugins took %s over a plugin that stays when its input closes: it is given %s to leave, twice",
			took, core.PluginGlanceGrace)
	}
	if err != nil || len(found.Plugins) != 1 || found.Plugins[0].Check == nil || found.Plugins[0].Problem != "" {
		t.Errorf("CheckPlugins = %+v, %v; want the plugin's answer", found, err)
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
}

// Stopped while a plugin is being waited for, CheckPlugins says so, at once,
// and reports nothing of a check that was cut short.
func TestCheckPluginsStoppedWhileItWaits(t *testing.T) {
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record, "--hang", "start")...)
	k := studyOn(t, c, "", core.PluginOptions{})
	ctx, stop := context.WithCancel(context.Background())
	// Once the plugin was started, which its record says.
	go func() {
		for range 400 {
			if _, err := os.Stat(record); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		stop()
	}()
	began := time.Now()
	found, err := k.CheckPlugins(ctx)
	if took := time.Since(began); took > busy {
		t.Errorf("CheckPlugins took %s to end once the command was stopped", took)
	}
	if core.CodeOf(err) != core.CodeCanceled || !strings.Contains(err.Error(), "the command was stopped before every Knowledge base plugin") ||
		len(found.Plugins) != 0 {
		t.Errorf("CheckPlugins, stopped while it waited = %+v, %v (%s); want canceled, and nothing reported", found, err, core.CodeOf(err))
	}
	if started := plugintest.ReadRecord(t, record); !plugintest.Gone(started.PID, 5*time.Second) {
		plugintest.Kill(started.PID)
		t.Errorf("the plugin, process %d, was left running", started.PID)
	}
}

// StatusWithout asks none of the plugins it is told to leave out: a plugin
// that has just failed its check is not waited for a second time. The
// Topics that name it come back without an Indexing, and every other Topic
// as Status gives it.
func TestStatusWithoutAsksNoneOfThePluginsLeftOut(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	silent, answers := filepath.Join(t.TempDir(), "silent.json"), filepath.Join(t.TempDir(), "answers.json")
	c.Register(t, "silent", plugintest.Command("--record", silent, "--hang", "start")...)
	c.Register(t, "answers", plugintest.Command("--record", answers)...)
	k := studyOn(t, c, "", core.PluginOptions{})
	for topic, plugin := range map[string]string{"a": "silent", "b": "answers", "d": "missing"} {
		namedTopic(t, k, topic, plugin)
		addBook(t, k, topic, filepath.Join(c.Home, "Books", topic+".txt"), pages(2, topic))
	}
	began := time.Now()
	status, err := k.StatusWithout(ctx, []string{"silent"})
	if took := time.Since(began); took > busy {
		t.Errorf("StatusWithout took %s: the plugin that never answers has %s, and was to be left out", took, patient)
	}
	if err != nil || len(status.Topics) != 3 {
		t.Fatalf("StatusWithout = %+v, %v", status.Topics, err)
	}
	if x := status.Topics[0].Indexing; x != nil {
		t.Errorf("the Topic whose plugin was left out has %+v; want no Indexing", x)
	}
	if x := status.Topics[1].Indexing; x == nil || x.Problem != "" || x.Action != "study source index b" {
		t.Errorf("the Topic whose plugin answers has %+v", x)
	}
	if x := status.Topics[2].Indexing; x == nil || !strings.Contains(x.Problem, "no Knowledge base plugin named missing is registered") {
		t.Errorf("the Topic that names a plugin this computer lacks has %+v", x)
	}
	if _, err := os.Stat(silent); err == nil {
		t.Error("the plugin that was left out was started")
	}
	if tools := plugintest.Tools(plugintest.ReadCalls(t, answers)); !slices.Equal(tools, []string{"contract", "status"}) {
		t.Errorf("the other plugin was asked %v", tools)
	}
}

// What keeps the registry from being read is said once, for all of it, and a
// command that was stopped says so.
func TestCheckPluginsWithoutARegistryToRead(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	k := studyOn(t, c, "", core.PluginOptions{})
	// Nothing registered: there is nothing to check, and nothing is made.
	found, err := k.CheckPlugins(ctx)
	if err != nil || found.Problem != "" || len(found.Plugins) != 0 || found.Registry == "" {
		t.Errorf("CheckPlugins with nothing registered = %+v, %v", found, err)
	}
	if left := tree(t, c.Home); !reflect.DeepEqual(left, []string{".config/", ".config/lamplight/"}) {
		t.Errorf("checking no plugin wrote %v in the home folder", left)
	}

	registry := filepath.Join(c.ConfigDir, "knowledge-base-plugins.json")
	// The registry's own words say what to do about it.
	for content, advice := range map[string]string{
		"{":                             "fix it by hand, or delete it and register your Knowledge base plugins",
		`{"format": 99, "plugins": []}`: "upgrade study",
	} {
		if err := os.WriteFile(registry, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		found, err := k.CheckPlugins(ctx)
		if err != nil || !strings.Contains(found.Problem, registry) || !strings.Contains(found.Problem, advice) ||
			found.Registry != "" || len(found.Plugins) != 0 {
			t.Errorf("CheckPlugins over the registry %s = %+v, %v; want a problem that says %q", content, found, err, advice)
		}
	}

	c.Register(t, "kb", plugintest.Command()...)
	stopped, stop := context.WithCancel(ctx)
	stop()
	if found, err := k.CheckPlugins(stopped); core.CodeOf(err) != core.CodeCanceled || len(found.Plugins) != 0 {
		t.Errorf("CheckPlugins, stopped = %+v, %v; want canceled", found, err)
	}
}
