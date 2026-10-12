package xdg_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/xdg"
)

func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestDirIsTheVariableOnlyWhenItIsAbsolute(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	for _, f := range []struct {
		folder   xdg.Folder
		fallback string
	}{
		{xdg.ConfigHome, ".config"},
		{xdg.DataHome, ".local/share"},
		{xdg.StateHome, ".local/state"},
		{xdg.CacheHome, ".cache"},
	} {
		byDefault := filepath.Join(home, filepath.FromSlash(f.fallback))
		for _, tc := range []struct {
			name, value, want string
		}{
			{"unset", "", byDefault},
			{"absolute", elsewhere, elsewhere},
			{"relative", "rel", byDefault},
			{"relative, from here", "./rel", byDefault},
			{"relative, from above", "../rel", byDefault},
			{"the home folder by its sign", "~/rel", byDefault},
		} {
			got, err := f.folder.Dir(envOf(map[string]string{"HOME": home, f.folder.Var: tc.value}))
			if err != nil || got != tc.want {
				t.Errorf("%s %s (%q) = %q, %v; want %q", f.folder.Var, tc.name, tc.value, got, err, tc.want)
			}
		}
		// An absolute folder needs no home folder; the default does.
		if got, err := f.folder.Dir(envOf(map[string]string{"HOME": "rel", f.folder.Var: elsewhere})); err != nil || got != elsewhere {
			t.Errorf("%s absolute, HOME relative = %q, %v", f.folder.Var, got, err)
		}
		if got, err := f.folder.Dir(envOf(map[string]string{"HOME": "rel", f.folder.Var: "rel"})); err == nil {
			t.Errorf("%s relative, HOME relative = %q; want an error, not a folder", f.folder.Var, got)
		}
	}
}

func TestARelativeHomeIsNoHomeFolder(t *testing.T) {
	home := t.TempDir()
	if got, err := xdg.Home(envOf(map[string]string{"HOME": home})); err != nil || got != home {
		t.Errorf("Home = %q, %v; want %q", got, err, home)
	}
	for _, value := range []string{"rel", "./rel", "../rel", "~"} {
		got, err := xdg.Home(envOf(map[string]string{"HOME": value}))
		if err == nil || !strings.Contains(err.Error(), "HOME") || !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("Home with HOME=%q = %q, %v; want an error that names HOME", value, got, err)
		}
	}
	// Unset, the operating system is asked: its answer is this test's HOME.
	t.Setenv("HOME", home)
	if got, err := xdg.Home(envOf(nil)); err != nil || got != home {
		t.Errorf("Home with HOME unset = %q, %v; want %q", got, err, home)
	}
	t.Setenv("HOME", "rel")
	if got, err := xdg.Home(envOf(nil)); err == nil {
		t.Errorf("Home with HOME unset and a relative answer = %q; want an error", got)
	}
}

func TestIgnoredNamesTheRelativeVariables(t *testing.T) {
	abs := t.TempDir()
	if got := xdg.Ignored(envOf(map[string]string{"HOME": abs, "XDG_CONFIG_HOME": abs, "XDG_DATA_HOME": ""})); len(got) != 0 {
		t.Errorf("Ignored with absolute and unset variables = %v", got)
	}
	got := xdg.Ignored(envOf(map[string]string{
		"HOME": "rel", "XDG_CACHE_HOME": "c", "XDG_STATE_HOME": "s", "XDG_DATA_HOME": abs, "XDG_CONFIG_HOME": "./c",
		"XDG_RUNTIME_DIR": "run", "STUDY_HOME": "study",
	}))
	if want := []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}; !slices.Equal(got, want) {
		t.Errorf("Ignored = %v, want %v", got, want)
	}
}

func TestEnvironLeavesOutTheRelativeVariables(t *testing.T) {
	got := xdg.Environ([]string{
		"PATH=/usr/bin", "HOME=rel", "XDG_CONFIG_HOME=rel", "XDG_DATA_HOME=/data", "XDG_STATE_HOME=", "XDG_CACHE_HOME=../c",
		"XDG_RUNTIME_DIR=run", "EDITOR=vi", "XDG_CONFIG_HOME", "PWD=rel",
	})
	want := []string{"PATH=/usr/bin", "XDG_DATA_HOME=/data", "XDG_STATE_HOME=", "XDG_RUNTIME_DIR=run", "EDITOR=vi", "XDG_CONFIG_HOME", "PWD=rel"}
	if !slices.Equal(got, want) {
		t.Errorf("Environ = %v, want %v", got, want)
	}
	if got := xdg.Environ([]string{"HOME=/home/ada", "XDG_CONFIG_HOME=/home/ada/.config"}); len(got) != 2 {
		t.Errorf("Environ with absolute variables = %v", got)
	}
}
