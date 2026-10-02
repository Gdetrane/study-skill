// Package claudeplugin builds Lamplight's Claude Code plugin. The repository's
// marketplace (.claude-plugin/marketplace.json) lists the plugin with a
// command source, `study claude-plugin-path`, which writes the plugin with
// this package and prints its folder, so the plugin always matches the
// installed study: its skill, its MCP server and its session-start hook all
// come from the binary.
package claudeplugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Name is the plugin's name, in its manifest and in the marketplace entry, so
// it installs as lamplight@<marketplace>.
const Name = "lamplight"

// Options describes the plugin to write.
type Options struct {
	// Study is the absolute path of the study binary the plugin's MCP server
	// and hook run. GUI editors do not inherit the shell's PATH, so it is
	// never a bare name.
	Study string
	// Version is study's version, shown as the plugin's version.
	Version string
	// Skill holds the lamplight skill: SKILL.md and references/.
	Skill fs.FS
}

// Files returns the plugin's files by slash-separated path, ready to write.
func Files(opts Options) (map[string][]byte, error) {
	if !filepath.IsAbs(opts.Study) {
		return nil, fmt.Errorf("the plugin needs study's absolute path, not %q", opts.Study)
	}
	files := map[string][]byte{}
	manifest := map[string]any{
		"name":        Name,
		"displayName": "Lamplight",
		"version":     opts.Version,
		"description": "Study with Lamplight: the lamplight teaching skill, study's MCP server, and where you " +
			"stand whenever a session starts in your Study home.",
		"author":     map[string]string{"name": "mordor-forge"},
		"homepage":   "https://github.com/mordor-forge/lamplight",
		"repository": "https://github.com/mordor-forge/lamplight",
		"license":    "MIT",
		"keywords":   []string{"study", "learning", "tutor", "spaced-repetition"},
		"mcpServers": map[string]any{
			Name: map[string]any{"command": opts.Study, "args": []string{"mcp"}},
		},
	}
	hooks := map[string]any{
		"description": "Show where the learner stands when a session starts in the Study home",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": opts.Study,
					"args":    []string{"claude-hook", "session-start"},
					"timeout": 30,
				}},
			}},
		},
	}
	for name, v := range map[string]any{".claude-plugin/plugin.json": manifest, "hooks/hooks.json": hooks} {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, err
		}
		files[name] = append(data, '\n')
	}
	err := fs.WalkDir(opts.Skill, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(opts.Skill, p)
		if err != nil {
			return err
		}
		files[path.Join("skills", Name, p)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the skill: %w", err)
	}
	if _, ok := files[path.Join("skills", Name, "SKILL.md")]; !ok {
		return nil, errors.New("the skill has no SKILL.md")
	}
	return files, nil
}

// Hash is a short digest of the files, so each distinct plugin gets its own
// folder.
func Hash(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Write writes the plugin into a folder under parent named after its content,
// and returns that folder. A folder that already holds the same files is
// reused; other plugin folders under parent, left by earlier versions, are
// removed. Claude Code copies the folder when it installs or updates the
// plugin, so removing old ones is safe.
func Write(parent string, opts Options) (string, error) {
	files, err := Files(opts)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "plugin-"+Hash(files))
	if !same(dir, files) {
		tmp, err := os.MkdirTemp(parent, ".plugin-tmp-*")
		if err != nil {
			return "", err
		}
		for name, data := range files {
			p := filepath.Join(tmp, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				_ = os.RemoveAll(tmp)
				return "", err
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				_ = os.RemoveAll(tmp)
				return "", err
			}
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
		_ = os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
	}
	entries, err := os.ReadDir(parent)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() && name != filepath.Base(dir) &&
				(strings.HasPrefix(name, "plugin-") || strings.HasPrefix(name, ".plugin-tmp-")) {
				_ = os.RemoveAll(filepath.Join(parent, name))
			}
		}
	}
	return dir, nil
}

// same reports whether dir holds exactly files.
func same(dir string, files map[string][]byte) bool {
	found := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		want, ok := files[filepath.ToSlash(rel)]
		if !ok || !d.Type().IsRegular() {
			return errors.New("unexpected file")
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			return errors.New("different content")
		}
		found++
		return nil
	})
	return err == nil && found == len(files)
}
