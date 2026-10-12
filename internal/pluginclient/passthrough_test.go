//go:build linux || darwin

package pluginclient_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// installed puts the test plugin's program at path, as a file of its own
// there and no link, and returns the path.
func installed(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(plugintest.Command()[0])
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A plugin is started below folders the learner may pass through and not
// list, as /home is on many shared computers (#92): above the registry's
// folder, above its program and around it, above the Study home, and above
// the folder it is given for its index. It is started as it is anywhere.
func TestAPluginIsStartedBelowFoldersThatMayNotBeListed(t *testing.T) {
	c := plugintest.NewComputer(t)
	bin := filepath.Join(c.Base(), "opt", "kb", "bin")
	program := installed(t, filepath.Join(bin, "notes-kb"))
	record := filepath.Join(t.TempDir(), "record.json")
	plugin := c.Register(t, "kb", program, plugintest.Arg, "--record", record, "--name", "below")
	// A folder nobody may pass through: what a variable names there, study
	// cannot follow, and does not hand on.
	shut := filepath.Join(c.Base(), "shut")
	if err := os.MkdirAll(filepath.Join(shut, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	plugintest.Shut(t, shut)
	plugintest.PassThrough(t, c.Base(), c.Home, filepath.Dir(c.ConfigDir), filepath.Join(c.Base(), "opt"), filepath.Dir(bin), bin)

	share := filepath.Join(c.Home, ".local", "share")
	s, err := pluginclient.Start(context.Background(), c.Env(""), plugin,
		quick(c, "XDG_DATA_HOME="+share, "XDG_CACHE_HOME="+filepath.Join(shut, "cache"), "TMPDIR="+c.StudyHome))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if s.Server().Name != "below" || s.Contract().Version != 1 {
		t.Errorf("Server = %+v, Contract = %+v", s.Server(), s.Contract())
	}
	got := plugintest.ReadRecord(t, record)
	defer plugintest.Kill(got.PID)

	if got.Dir != c.ConfigDir {
		t.Errorf("the plugin was started in %s, want the registry's folder, %s", got.Dir, c.ConfigDir)
	}
	folder := c.DataFolder("kb")
	if s.DataFolder() != folder || got.Env[plugincontract.DataFolderEnv] != folder {
		t.Errorf("the plugin's folder is %s, and it was told %s; want %s", s.DataFolder(), got.Env[plugincontract.DataFolderEnv], folder)
	}
	if info, err := os.Stat(folder); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the plugin's folder: %v, %v; want one for the learner alone", info, err)
	}
	// What is below folders that may not be listed is outside the Study
	// home like anything else there; what study cannot follow, and what is
	// in the Study home, are left out.
	if got.Env["HOME"] != c.Home || got.Env["XDG_DATA_HOME"] != share {
		t.Errorf("the plugin was given HOME=%q and XDG_DATA_HOME=%q; want %q and %q", got.Env["HOME"], got.Env["XDG_DATA_HOME"], c.Home, share)
	}
	for _, name := range []string{"XDG_CACHE_HOME", "TMPDIR"} {
		if value, given := got.Env[name]; given {
			t.Errorf("the plugin was given %s=%s", name, value)
		}
	}
	if runtime.GOOS == "linux" {
		// It runs the program that was checked, by its name in the folder
		// that holds it, which study opened to pass through.
		if got.Exe != program || got.Files[3] != bin {
			t.Errorf("the program that runs is %q, from the folder %q; want %s in %s", got.Exe, got.Files[3], program, bin)
		}
	}
}

// Below a folder the learner may not even pass through, study cannot tell
// where a path leads, and so not whether it is the agent's to change. A
// plugin that needs anything there is not started: its program, its
// script's interpreter, an argument the learner did not allow, the
// registry's folder, or the folder it would be given.
func TestAPluginIsNotStartedWhenStudyCannotFollowWhatItNeeds(t *testing.T) {
	for name, tc := range map[string]struct {
		// plugin registers the plugin, given a folder that will be shut and
		// where the plugin records being started; vars are the variables
		// study has, given the same folder.
		plugin func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin
		vars   func(shut string) map[string]string
		want   string
	}{
		"its program": {
			plugin: func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin {
				return c.Register(t, "kb", installed(t, filepath.Join(shut, "bin", "kb")), plugintest.Arg, "--record", record)
			},
			want: "Knowledge base plugin kb cannot be started: study cannot follow",
		},
		"its script's interpreter": {
			plugin: func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin {
				interpreter := installed(t, filepath.Join(shut, "bin", "interpreter"))
				script := filepath.Join(c.Home, "bin", "kb")
				if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(script, []byte("#!"+interpreter+" "+plugintest.Arg+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				return c.Register(t, "kb", script, "--record", record)
			},
			want: "the interpreter its first line names is not one study runs. study cannot follow",
		},
		"an argument": {
			plugin: func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin {
				return c.Register(t, "kb", plugintest.Command("--record", record, "--config="+filepath.Join(shut, "kb.toml"))...)
			},
			want: "names a path study cannot follow to its end (permission denied)",
		},
		"the registry's folder": {
			plugin: func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin {
				if err := os.MkdirAll(filepath.Join(shut, "config", "lamplight"), 0o755); err != nil {
					t.Fatal(err)
				}
				return c.Register(t, "kb", plugintest.Command("--record", record)...)
			},
			vars: func(shut string) map[string]string {
				return map[string]string{"XDG_CONFIG_HOME": filepath.Join(shut, "config")}
			},
			want: "study cannot reach Lamplight's configuration folder",
		},
		"its folder": {
			plugin: func(t *testing.T, c *plugintest.Computer, shut, record string) pluginregistry.Plugin {
				if err := os.MkdirAll(filepath.Join(shut, "state"), 0o755); err != nil {
					t.Fatal(err)
				}
				return c.Register(t, "kb", plugintest.Command("--record", record)...)
			},
			vars: func(shut string) map[string]string {
				return map[string]string{"XDG_STATE_HOME": filepath.Join(shut, "state")}
			},
			want: "study cannot make the folder Knowledge base plugin kb keeps its index in",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := plugintest.NewComputer(t)
			shut := filepath.Join(c.Base(), "shut")
			if err := os.MkdirAll(shut, 0o755); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "record.json")
			plugin := tc.plugin(t, c, shut, record)
			plugintest.Shut(t, shut)
			env := c.Env("")
			if tc.vars != nil {
				vars := tc.vars(shut)
				env.Getenv = func(key string) string {
					if value, ok := vars[key]; ok {
						return value
					}
					return c.Getenv(key)
				}
			}
			s, err := pluginclient.Start(context.Background(), env, plugin, quick(c))
			if err == nil {
				s.Close()
			}
			if codeOf(err) != pluginclient.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "permission denied") {
				t.Errorf("Start = %v; want failed_precondition, saying %q and why", err, tc.want)
			}
			if _, err := os.Stat(record); err == nil {
				t.Error("a program was started")
			}
		})
	}
}
