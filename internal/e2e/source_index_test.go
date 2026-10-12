//go:build unix

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// run runs the built study to its end and returns what it printed.
func (c *pluginComputer) run(group string, args ...string) (stdout, stderr string, err error) {
	c.t.Helper()
	cmd := c.command(group, args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err = cmd.Run()
	return out.String(), errs.String(), err
}

// must runs the built study and fails the test unless it succeeds.
func (c *pluginComputer) must(group string, args ...string) string {
	c.t.Helper()
	stdout, stderr, err := c.run(group, args...)
	if err != nil {
		c.t.Fatalf("study %s %s: %v\nstdout: %s\nstderr: %s", group, strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// indexed are the ids of the Sources the plugin was asked to index, in
// order.
func indexedBy(calls []plugintest.Call) []string {
	var out []string
	for _, call := range calls {
		if call.Tool == "index" {
			out = append(out, call.Source())
		}
	}
	return out
}

// A built study, run as an agent runs it in its shell: a Topic names the
// plugin, its Sources are indexed with study source index, and the command
// is stopped halfway by a signal, as Ctrl-C and a harness that cancels a
// turn stop it. The plugin is told to stop and is gone when study has
// exited; what was indexed stays, and the same command carries on. Then the
// plugin is taken off the computer, and the Topic works without it.
func TestABuiltStudyIndexesIsStoppedAndCarriesOn(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			c := newPluginComputer(t, "--page-delay", "25ms")
			c.must("topic", "create", "--title", "C", "--json")
			c.must("topic", "update", "c", "--knowledge-base", "kb", "--json")
			ids := map[string]string{}
			for _, book := range []struct {
				name  string
				pages int
			}{{"first", 2}, {"long", 400}, {"third", 2}} {
				name := book.name
				var text strings.Builder
				for i := range book.pages {
					if i > 0 {
						text.WriteString("\f")
					}
					fmt.Fprintf(&text, "Page %d of the %s book.\n", i+1, name)
				}
				path := filepath.Join(c.home, "Books", name+".txt")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				var added struct {
					Data core.SourceResult `json:"data"`
				}
				if err := json.Unmarshal([]byte(c.must("source", "add", "c", "--file", path, "--json")), &added); err != nil {
					t.Fatal(err)
				}
				ids[name] = added.Data.Source.ID
			}
			// listed is the Topic's Sources as study source list gives them.
			listed := func() core.SourceList {
				t.Helper()
				var list struct {
					Data core.SourceList `json:"data"`
				}
				if err := json.Unmarshal([]byte(c.must("source", "list", "c", "--json")), &list); err != nil {
					t.Fatal(err)
				}
				return list.Data
			}
			// sources asks the plugin what it has indexed, as the dry run of
			// study source index does, with the time a plugin has to start.
			// study source list gives it two seconds, which a computer busy
			// with every other test may not leave it: when it answered in
			// time, it says the same.
			sources := func() []string {
				t.Helper()
				var dry struct {
					Data core.IndexResult `json:"data"`
				}
				if err := json.Unmarshal([]byte(c.must("source", "index", "c", "--dry-run", "--json")), &dry); err != nil {
					t.Fatal(err)
				}
				states := map[string]string{}
				for _, src := range dry.Data.Sources {
					states[src.ID] = src.State
				}
				// A computer busy with every other test may not leave the
				// plugin its two seconds once. It does not fail to five
				// times running: a study that never hears its plugin does.
				list := listed()
				for tries := 1; tries < 5 && list.Indexing != nil && list.Indexing.Problem != ""; tries++ {
					t.Logf("study source list did not hear the plugin in time: %s", list.Indexing.Problem)
					list = listed()
				}
				switch {
				case list.Indexing == nil:
					t.Errorf("study source list says nothing of the Topic's plugin: %+v", list)
				case list.Indexing.Problem != "":
					t.Errorf("study source list did not hear the plugin, five times running: %s", list.Indexing.Problem)
				default:
					action := ""
					for _, src := range list.Sources {
						if src.Indexed == nil || src.Indexed.State != states[src.ID] {
							t.Errorf("study source list says %s is %+v, and the plugin says %s", src.ID, src.Indexed, states[src.ID])
						}
						if states[src.ID] != core.IndexIndexed {
							action = "study source index c"
						}
					}
					if list.Indexing.Action != action {
						t.Errorf("study source list gives the action %q, want %q", list.Indexing.Action, action)
					}
				}
				var out []string
				for _, name := range []string{"first", "long", "third"} {
					out = append(out, name+" "+states[ids[name]])
				}
				return out
			}
			if states := sources(); !slices.Equal(states, []string{"first not_indexed", "long not_indexed", "third not_indexed"}) {
				t.Fatalf("before indexing: %v", states)
			}

			// Stopped once the plugin was asked for the long book, and is in
			// the middle of it.
			cmd := c.command("source", "index", "c")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				if slices.Contains(indexedBy(plugintest.ReadCalls(t, c.record)), ids["long"]) {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					t.Fatalf("the plugin was never asked for the long book; stderr: %s", stderr.String())
				}
			}
			time.Sleep(150 * time.Millisecond)
			plugin := plugintest.ReadRecord(t, c.record)
			defer plugintest.Kill(plugin.PID)
			began := time.Now()
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			if took := time.Since(began); took > 8*time.Second {
				t.Errorf("study took %s to stop after %s: the long book alone takes 10s to index", took, sig)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Errorf("exit = %v, want status 1", err)
			}
			for _, want := range []string{"study source index was stopped before it finished",
				"Knowledge base plugin kb was told to stop indexing Source " + ids["long"] + ", and stopped",
				"study source index c carries on from there"} {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("a stopped command printed a result: %s", stdout.String())
			}
			// The plugin was told to stop the Source it was indexing, last of
			// all, and is gone with study: nothing indexes behind it.
			calls := plugintest.ReadCalls(t, c.record)
			if got := indexedBy(calls); !slices.Equal(got, []string{ids["first"], ids["long"]}) {
				t.Errorf("the plugin was asked to index %v, want the first two books", got)
			}
			if last := calls[len(calls)-1]; last.Tool != "cancel" || last.Source() != ids["long"] {
				t.Errorf("the last thing the plugin was asked is %s of %s, want to stop indexing the long book", last.Tool, last.Source())
			}
			if !plugintest.Gone(plugin.PID, 5*time.Second) {
				t.Errorf("the plugin, process %d, outlived study", plugin.PID)
			}
			if states := sources(); !slices.Equal(states, []string{"first indexed", "long not_indexed", "third not_indexed"}) {
				t.Errorf("after the stop the Sources are %v", states)
			}

			// The same command again, with a plugin that takes no time over a
			// page: it asks for what is not indexed, and for nothing that is.
			command := plugintest.Command("--record", c.record)
			c.must("knowledge-base", append([]string{"add", "kb", "--replace", "--"}, command...)...)
			asked := len(plugintest.ReadCalls(t, c.record))
			var done struct {
				Data core.IndexResult `json:"data"`
			}
			if err := json.Unmarshal([]byte(c.must("source", "index", "c", "--json")), &done); err != nil {
				t.Fatal(err)
			}
			for _, src := range done.Data.Sources {
				if src.State != core.IndexIndexed || src.Asked != (src.ID != ids["first"]) {
					t.Errorf("after the second run: %+v", src)
				}
			}
			if got := indexedBy(plugintest.ReadCalls(t, c.record)[asked:]); !slices.Equal(got, []string{ids["long"], ids["third"]}) {
				t.Errorf("the second run had the plugin index %v, want what was left", got)
			}
			if states := sources(); !slices.Equal(states, []string{"first indexed", "long indexed", "third indexed"}) {
				t.Errorf("after the second run the Sources are %v", states)
			}
			// status names the Topic's plugin and its Sources, and nothing is
			// left running.
			var status struct {
				Data core.Status `json:"data"`
			}
			for tries := 1; ; tries++ {
				status.Data = core.Status{}
				if err := json.Unmarshal([]byte(c.must("status", "--json")), &status); err != nil {
					t.Fatal(err)
				}
				// As above: a busy computer may be late once, not five
				// times.
				if x := status.Data.Topics[0].Indexing; x == nil || x.Problem == "" || tries == 5 {
					break
				}
			}
			topic := status.Data.Topics[0]
			if topic.Indexing == nil || topic.Indexing.Plugin != "kb" || len(topic.Indexing.Sources) != 3 {
				t.Fatalf("status = %+v", topic.Indexing)
			}
			if topic.Indexing.Problem != "" {
				t.Errorf("status did not hear the plugin, five times running: %s", topic.Indexing.Problem)
			}
			for _, src := range topic.Indexing.Sources {
				if src.State == core.IndexUnknown {
					t.Errorf("status does not know whether %s is indexed, though the plugin answered", src.ID)
				}
			}
			if last := plugintest.ReadRecord(t, c.record); !plugintest.Gone(last.PID, 5*time.Second) {
				plugintest.Kill(last.PID)
				t.Errorf("the plugin, process %d, outlived study", last.PID)
			}

			// The plugin is taken off this computer. The Topic still names
			// it, and works as one without a Knowledge base.
			c.must("knowledge-base", "remove", "kb")
			list := listed()
			if list.Indexing == nil || !strings.Contains(list.Indexing.Problem, "no Knowledge base plugin named kb is registered") ||
				list.Indexing.Action != "study knowledge-base add kb -- <command>" || len(list.Sources) != 3 {
				t.Errorf("without the plugin: %+v, %+v", list.Indexing, list.Sources)
			}
			for _, src := range list.Sources {
				if src.Indexed == nil || src.Indexed.State != core.IndexUnknown || src.State != core.SourceOK {
					t.Errorf("without the plugin, Source %s = %+v, index %+v", src.ID, src.State, src.Indexed)
				}
			}
			c.must("session", "open", "c", "--energy", "full", "--json")
			out, errs, err := c.run("source", "index", "c")
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || out != "" ||
				!strings.Contains(errs, "Topic c names the Knowledge base plugin kb, which is not registered on this computer") {
				t.Errorf("study source index without the plugin: %v\nstdout: %s\nstderr: %s", err, out, errs)
			}
		})
	}
}
