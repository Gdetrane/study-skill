package claudeplugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/claudeplugin"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

func options() claudeplugin.Options {
	return claudeplugin.Options{Study: "/usr/bin/study", Version: "2.0.0", Skill: lamplight.FS()}
}

func TestFilesHoldTheSkillTheServerAndTheHook(t *testing.T) {
	files, err := claudeplugin.Files(options())
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name       string `json:"name"`
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(files[".claude-plugin/plugin.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	s := manifest.MCPServers["lamplight"]
	if manifest.Name != "lamplight" || s.Command != "/usr/bin/study" || len(s.Args) != 1 || s.Args[0] != "mcp" {
		t.Errorf("manifest = %+v", manifest)
	}
	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type, Command string
				Args          []string
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(files["hooks/hooks.json"], &hooks); err != nil {
		t.Fatal(err)
	}
	h := hooks.Hooks["SessionStart"]
	if len(h) != 1 || len(h[0].Hooks) != 1 || h[0].Hooks[0].Command != "/usr/bin/study" ||
		len(h[0].Hooks[0].Args) != 2 || h[0].Hooks[0].Args[0] != "claude-hook" {
		t.Errorf("SessionStart hook = %+v", h)
	}
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", "lamplight", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(files["skills/lamplight/SKILL.md"]) != string(skill) {
		t.Error("the plugin's skill is not the embedded skill")
	}
	if _, ok := files["skills/lamplight/references/lesson-loop.md"]; !ok {
		t.Error("the plugin's skill has no references")
	}
}

func TestFilesNeedAnAbsoluteStudy(t *testing.T) {
	opts := options()
	opts.Study = "study"
	if _, err := claudeplugin.Files(opts); err == nil {
		t.Error("a bare study name was accepted; GUI editors would not find it")
	}
}

func TestWriteReusesAFolderAndRemovesOldOnes(t *testing.T) {
	parent := t.TempDir()
	first, err := claudeplugin.Write(parent, options())
	if err != nil {
		t.Fatal(err)
	}
	again, err := claudeplugin.Write(parent, options())
	if err != nil || again != first {
		t.Errorf("writing the same plugin again = %s, %v; want %s", again, err, first)
	}
	opts := options()
	opts.Study = "/opt/homebrew/bin/study"
	moved, err := claudeplugin.Write(parent, opts)
	if err != nil || moved == first {
		t.Fatalf("a plugin for another study = %s, %v", moved, err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("the old plugin folder is still there (err = %v)", err)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Errorf("%d folders under the parent, want 1", len(entries))
	}
}
