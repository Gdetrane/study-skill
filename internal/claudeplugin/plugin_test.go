package claudeplugin_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestWriteReusesAFolderAndNeverRemovesOne(t *testing.T) {
	parent := t.TempDir()
	first, err := claudeplugin.Write(parent, options())
	if err != nil {
		t.Fatal(err)
	}
	again, err := claudeplugin.Write(parent, options())
	if err != nil || again != first {
		t.Errorf("writing the same plugin again = %s, %v; want %s", again, err, first)
	}
	moved, err := claudeplugin.Write(parent, other())
	if err != nil || moved == first {
		t.Fatalf("a plugin for another study = %s, %v", moved, err)
	}
	// Another study may have just printed the first folder for Claude Code
	// to copy.
	if !intact(t, first, options()) || !intact(t, moved, other()) {
		t.Error("writing a plugin removed or changed another plugin's folder")
	}
}

func TestWritePrunesOnlyStaleTemporaryFolders(t *testing.T) {
	parent := t.TempDir()
	stale := filepath.Join(parent, ".plugin-tmp-123")
	fresh := filepath.Join(parent, ".plugin-tmp-456")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(filepath.Join(d, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-claudeplugin.StaleAfter - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeplugin.Write(parent, options()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a stale temporary folder was kept (err = %v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a temporary folder another Write may still be filling was removed: %v", err)
	}
}

func TestWriteSetsADamagedFolderAside(t *testing.T) {
	parent := t.TempDir()
	dir, err := claudeplugin.Write(parent, options())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "lamplight", "SKILL.md"), []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := claudeplugin.Write(parent, options())
	if err != nil || again != dir || !intact(t, dir, options()) {
		t.Errorf("writing over a damaged folder = %s, %v", again, err)
	}
}

func TestWriteIsSafeToRunConcurrently(t *testing.T) {
	const writers = 16
	for round := range 5 {
		parent := t.TempDir()
		var wg sync.WaitGroup
		start := make(chan struct{})
		dirs := make([]string, writers)
		errs := make([]error, writers)
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				opts := options()
				if i%2 == 1 {
					opts = other() // another study, as during an upgrade
				}
				<-start
				dirs[i], errs[i] = claudeplugin.Write(parent, opts)
			}()
		}
		close(start)
		wg.Wait()
		for i := range writers {
			if errs[i] != nil {
				t.Fatalf("round %d, Write %d: %v", round, i, errs[i])
			}
			if dirs[i] != dirs[i%2] {
				t.Errorf("round %d: Write %d returned %s, want %s", round, i, dirs[i], dirs[i%2])
			}
		}
		if !intact(t, dirs[0], options()) || !intact(t, dirs[1], other()) {
			t.Errorf("round %d: a concurrent Write removed or damaged a plugin folder another one returned", round)
		}
	}
}

func other() claudeplugin.Options {
	opts := options()
	opts.Study = "/opt/homebrew/bin/study"
	return opts
}

// intact reports whether dir holds exactly the plugin opts describes.
func intact(t *testing.T, dir string, opts claudeplugin.Options) bool {
	t.Helper()
	files, err := claudeplugin.Files(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if want, ok := files[filepath.ToSlash(rel)]; !ok || string(want) != string(data) {
			return fmt.Errorf("%s differs", rel)
		}
		found++
		return nil
	})
	return err == nil && found == len(files)
}
