package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// fakeGit puts a git on PATH that reports version and the given user.name;
// user.email is never set.
func fakeGit(t *testing.T, version, name string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"--version) echo \"git version " + version + "\" ;;\n" +
		"config) [ \"$3\" = user.name ] && [ -n \"$FAKE_GIT_NAME\" ] && echo \"$FAKE_GIT_NAME\" && exit 0; exit 1 ;;\n" +
		"*) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_GIT_NAME", name)
}

func findings(d core.Diagnosis) map[string]core.Finding {
	m := map[string]core.Finding{}
	for _, f := range d.Findings {
		m[f.Name] = f
	}
	return m
}

func TestDiagnoseGit(t *testing.T) {
	home := filepath.Join(t.TempDir(), "study")
	opts := core.Options{Getenv: envOf(map[string]string{"STUDY_HOME": home, "HOME": t.TempDir()})}

	fakeGit(t, "2.20.1", "")
	d := core.Diagnose(context.Background(), opts)
	got := findings(d)
	if got["git"].Status != core.FindingFail || d.Healthy {
		t.Errorf("git 2.20 = %+v, healthy %v; want a failure", got["git"], d.Healthy)
	}
	if _, ok := got["git_identity"]; ok {
		t.Error("the identity was checked with an unusable git")
	}
	if got["study_home"].Status != core.FindingOK {
		t.Errorf("a Study home that does not exist yet = %+v, want ok", got["study_home"])
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Error("Diagnose created the Study home")
	}

	fakeGit(t, "2.47.1.windows.1", "Ada Lovelace")
	got = findings(core.Diagnose(context.Background(), opts))
	if got["git"].Status != core.FindingOK || got["git"].Message != "git 2.47.1" {
		t.Errorf("git = %+v", got["git"])
	}
	identity := got["git_identity"]
	if identity.Status != core.FindingWarn || identity.Fix != "git config --global user.email you@example.com" {
		t.Errorf("identity without user.email = %+v", identity)
	}
}
