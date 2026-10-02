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
	"time"
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

// StaleAfter is how old a temporary folder under parent must be before
// Write removes it: older than any Write still filling it.
const StaleAfter = 10 * time.Minute

// Write writes the plugin into a folder under parent named after its content,
// and returns that folder. A folder that already holds the same files is
// reused. Plugin folders are never removed or replaced, because another
// study (another version, or the same one in another session) may have just
// printed one that Claude Code is about to copy; concurrent Writes of the
// same plugin all succeed with the same folder. Only temporary folders left
// by interrupted Writes are removed, once stale.
func Write(parent string, opts Options) (string, error) {
	files, err := Files(opts)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	defer pruneTemps(parent, time.Now().Add(-StaleAfter))
	dir := filepath.Join(parent, "plugin-"+Hash(files))
	switch _, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case same(dir, files):
		// Another Write's rename makes a folder appear whole, so a folder
		// that is there is complete, or damaged.
		return dir, nil
	default:
		// Damaged, since its name is its content's hash: set it aside
		// rather than delete it, and let pruning remove it once stale.
		aside, err := os.MkdirTemp(parent, ".plugin-tmp-damaged-*")
		if err != nil {
			return "", err
		}
		if err := os.Rename(dir, filepath.Join(aside, "plugin")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s is damaged and cannot be set aside: %w", dir, err)
		}
	}
	tmp, err := os.MkdirTemp(parent, ".plugin-tmp-*")
	if err != nil {
		return "", err
	}
	if err := fill(tmp, files); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		// Another Write got there first: its folder is as good as ours.
		if errors.Is(err, fs.ErrExist) && same(dir, files) {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// fill writes files into dir, a new folder only this Write knows.
func fill(dir string, files map[string][]byte) error {
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	return os.Chmod(dir, 0o755)
}

// pruneTemps removes temporary folders under parent last changed before
// cutoff, left by Writes that were interrupted.
func pruneTemps(parent string, cutoff time.Time) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".plugin-tmp-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
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
