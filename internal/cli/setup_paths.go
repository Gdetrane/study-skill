package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// What study setup may touch. It acts only on what its record says it
// created, and only while that is still what it created: a real folder, not
// a symlink, that leads where it led then; a regular file with the content
// setup wrote; a symlink with the target setup gave it. Anything else, such
// as a folder moved into a dotfiles repository and linked back, is the
// learner's and is left alone.

// dirRecord is a folder study setup created.
type dirRecord struct {
	Path string `json:"path"`
	// Real is where Path led when setup created it, with every symlink
	// resolved.
	Real string `json:"real"`
}

// owned reports whether d is still the folder setup created.
func (d dirRecord) owned() bool { return realDir(d.Path, d.Real) }

// realDir reports whether p is a folder, not a symlink to one, and leads to
// real.
func realDir(p, real string) bool {
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() {
		return false
	}
	got, err := filepath.EvalSymlinks(p)
	return err == nil && got == real
}

// planDirs returns the folders missing on the way to dir, outermost first,
// with the real path each will have. Only folders below floor are created:
// floor itself must exist.
func planDirs(dir, floor string) ([]dirRecord, error) {
	var missing []string
	d := dir
	for {
		_, err := os.Lstat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", d, err)
		}
		if !pathWithin(floor, d) || d == floor {
			return nil, fmt.Errorf("%s does not exist", floor)
		}
		missing = append(missing, d)
		d = filepath.Dir(d)
	}
	if info, err := os.Stat(d); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", d)
	}
	base, err := filepath.EvalSymlinks(d)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", d, err)
	}
	plan := make([]dirRecord, 0, len(missing))
	for i := len(missing) - 1; i >= 0; i-- {
		rel, err := filepath.Rel(d, missing[i])
		if err != nil {
			return nil, err
		}
		plan = append(plan, dirRecord{Path: missing[i], Real: filepath.Join(base, rel)})
	}
	return plan, nil
}

// makeDirs creates the planned folders, outermost first, and returns how
// many it created. A folder that appears meanwhile is not setup's: it stops
// there.
func makeDirs(plan []dirRecord) (int, error) {
	for i, d := range plan {
		if err := os.Mkdir(d.Path, 0o755); err != nil {
			return i, fmt.Errorf("creating %s: %w", d.Path, err)
		}
	}
	return len(plan), nil
}

// removeDirs removes the folders setup created that are still its own and
// empty, deepest first; anything someone put in them keeps them.
func removeDirs(dirs []dirRecord) {
	sorted := slices.Clone(dirs)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Path) > len(sorted[j].Path) })
	for _, d := range sorted {
		if d.owned() {
			_ = os.Remove(d.Path) // fails, as it should, unless d is empty
		}
	}
}

// pathWithin reports whether p is root or inside it, by their names.
func pathWithin(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && filepath.IsAbs(p) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sameFolder reports whether a and b lead to the same place, so a relative
// link made by another tool counts as leading to the skill.
func sameFolder(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}

// What a path in the skill folder is.
const (
	fileAbsent  = iota
	fileRegular // a regular file, reached through real folders only
	fileOther   // a folder, a symlink, or anything under a symlinked folder
)

// skillFolder is the skill folder setup created, opened as an os.Root so
// every file operation stays inside it, and checked part by part so none
// goes through a symlink.
type skillFolder struct {
	root *os.Root
	dir  string
}

// beforeSkillWrite, when set, runs before each skill file is written. Tests
// use it to make a write fail part-way.
var beforeSkillWrite func(rel string)

// openSkillFolder opens the skill folder while it is still the one setup
// created.
func openSkillFolder(s *skillRecord) (*skillFolder, error) {
	if !realDir(s.Dir, s.Real) {
		return nil, fmt.Errorf("%s is no longer the folder study setup created", s.Dir)
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", s.Dir, err)
	}
	return &skillFolder{root: root, dir: s.Dir}, nil
}

func (f *skillFolder) close() { _ = f.root.Close() }

func (f *skillFolder) path(rel string) string { return filepath.Join(f.dir, filepath.FromSlash(rel)) }

// realParents reports whether every folder on the way to rel is a real
// folder; absent is true when one is missing.
func (f *skillFolder) realParents(rel string) (ok, absent bool, err error) {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		info, err := f.root.Lstat(filepath.Join(parts[:i]...))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return true, true, nil
		case err != nil:
			return false, false, fmt.Errorf("reading %s: %w", f.path(path.Join(parts[:i]...)), err)
		case !info.IsDir():
			return false, false, nil
		}
	}
	return true, false, nil
}

// stat says what is at rel, with the sha256 of a regular file's content.
func (f *skillFolder) stat(rel string) (kind int, sum string, err error) {
	ok, absent, err := f.realParents(rel)
	switch {
	case err != nil:
		return 0, "", err
	case !ok:
		return fileOther, "", nil
	case absent:
		return fileAbsent, "", nil
	}
	info, err := f.root.Lstat(filepath.FromSlash(rel))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fileAbsent, "", nil
	case err != nil:
		return 0, "", fmt.Errorf("reading %s: %w", f.path(rel), err)
	case !info.Mode().IsRegular():
		return fileOther, "", nil
	}
	data, err := f.root.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		return 0, "", fmt.Errorf("reading %s: %w", f.path(rel), err)
	}
	return fileRegular, sha256Hex(data), nil
}

// write replaces rel with data atomically, creating the folders it needs.
// Callers check with stat first that rel is absent or setup's own file.
func (f *skillFolder) write(rel string, data []byte) error {
	if beforeSkillWrite != nil {
		beforeSkillWrite(rel)
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dir := filepath.Join(parts[:i]...)
		info, err := f.root.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := f.root.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("creating %s: %w", f.path(path.Join(parts[:i]...)), err)
			}
		case err != nil:
			return fmt.Errorf("reading %s: %w", f.path(path.Join(parts[:i]...)), err)
		case !info.IsDir():
			return fmt.Errorf("writing %s: %s is not a folder", f.path(rel), f.path(path.Join(parts[:i]...)))
		}
	}
	name := filepath.FromSlash(rel)
	tmp := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp-"+rand.Text()[:12])
	fh, err := f.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("writing %s: %w", f.path(rel), err)
	}
	_, werr := fh.Write(data)
	if werr == nil {
		werr = fh.Sync()
	}
	if cerr := fh.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = f.root.Chmod(tmp, 0o644)
	}
	if werr == nil {
		werr = f.root.Rename(tmp, name)
	}
	if werr != nil {
		_ = f.root.Remove(tmp)
		return fmt.Errorf("writing %s: %w", f.path(rel), werr)
	}
	return nil
}

// remove deletes rel, which stat found to be setup's own file.
func (f *skillFolder) remove(rel string) error {
	if err := f.root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", f.path(rel), err)
	}
	return nil
}

// removeEmptyDirs removes the folders holding rels, deepest first, while they
// are real folders and empty.
func (f *skillFolder) removeEmptyDirs(rels []string) {
	dirs := map[string]bool{}
	for _, rel := range rels {
		for d := path.Dir(rel); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	sorted := sortedKeys(dirs)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, d := range sorted {
		ok, absent, err := f.realParents(d + "/x")
		if err != nil || !ok || absent {
			continue
		}
		if info, err := f.root.Lstat(filepath.FromSlash(d)); err == nil && info.IsDir() {
			_ = f.root.Remove(filepath.FromSlash(d)) // fails unless empty
		}
	}
}

// lockWaitSetup is how long study setup waits for another setup, or for
// study mcp refreshing the skill, before giving up with CodeBusy.
const lockWaitSetup = 30 * time.Second

// lockSetup takes the lock that serialises everything that changes the
// setup record: study setup, --remove and study mcp's refresh. It is a flock
// on setup.lock next to the record, which the system releases if the
// process dies.
func (a *app) lockSetup(ctx context.Context, wait time.Duration) (func(), error) {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return nil, &core.Error{Code: core.CodeInternal, Message: "cannot find study's state folder: " + err.Error(), Err: err}
	}
	unwritable := func(err error) error {
		return &core.Error{Code: core.CodeInternal, Err: err, Message: fmt.Sprintf(
			"study setup records what it does in %s, and cannot write there (%v): it changed nothing", dir, err)}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, unwritable(err)
	}
	p := filepath.Join(dir, "setup.lock")
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, unwritable(err)
	}
	deadline := time.Now().Add(wait)
	pause := time.Millisecond
	for {
		locked, err := flockTry(f)
		if err != nil {
			_ = f.Close()
			return nil, &core.Error{Code: core.CodeInternal, Message: "locking " + p + ": " + err.Error(), Err: err}
		}
		if locked {
			return func() {
				_ = flockRelease(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, &core.Error{Code: core.CodeBusy, Message: "another study setup, or study mcp updating the skill, has held " +
				p + " for over " + wait.String() + ": try again"}
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(pause):
		}
		pause = min(2*pause, 50*time.Millisecond)
	}
}

// probeStateDir proves the state folder takes new files before setup changes
// anything, so a read-only or full disk stops it before the first step
// rather than after.
func (a *app) probeStateDir() error {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".setup-probe-*")
	if err == nil {
		_, err = f.Write([]byte("lamplight\n"))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		_ = os.Remove(f.Name())
	}
	if err != nil {
		return &core.Error{Code: core.CodeInternal, Err: err, Message: fmt.Sprintf(
			"study setup records what it does in %s, and cannot write there (%v): it changed nothing", dir, err)}
	}
	return nil
}

func (a *app) setupRecordPath() (string, error) {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, setupRecordFile), nil
}

func (a *app) readSetupRecord() (setupRecord, error) {
	rec := setupRecord{Format: setupRecordFormat, Registrations: []registrationRecord{}}
	p, err := a.setupRecordPath()
	if err != nil {
		return rec, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, fmt.Errorf("reading %s: %w", p, err)
	}
	damaged := func(why string) error {
		return &core.Error{Code: core.CodeCorrupt, Message: fmt.Sprintf(
			"%s is damaged (%s): delete it, then run study setup; study cannot undo earlier setups without it", p, why)}
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return setupRecord{}, damaged(err.Error())
	}
	switch {
	case rec.Format > setupRecordFormat:
		return setupRecord{}, &core.Error{Code: core.CodeNewerFormat, Message: fmt.Sprintf(
			"%s has format %d, but this version of study only understands format %d: upgrade study",
			p, rec.Format, setupRecordFormat)}
	case rec.Format < 1:
		return setupRecord{}, damaged("it has no format")
	}
	if rec.Registrations == nil {
		rec.Registrations = []registrationRecord{}
	}
	if rec.Skill != nil && rec.Skill.Files == nil {
		rec.Skill.Files = map[string]string{}
	}
	if why := a.invalidSetupRecord(rec); why != "" {
		return setupRecord{}, damaged(why)
	}
	return rec, nil
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// invalidSetupRecord says why rec names something study setup would never
// have created, or "". Setup removes what its record names, so a record that
// names anything else is refused before anything is touched.
func (a *app) invalidSetupRecord(rec setupRecord) string {
	home, err := a.homeDir()
	if err != nil {
		return err.Error()
	}
	skill, err := a.skillDir()
	if err != nil {
		return err.Error()
	}
	claude, err := a.claudeDir()
	if err != nil {
		return err.Error()
	}
	link := filepath.Join(claude, "skills", lamplight.Name)
	if s := rec.Skill; s != nil {
		if s.Dir != skill {
			return fmt.Sprintf("it names the skill folder %s, but study setup installs it in %s", s.Dir, skill)
		}
		if !filepath.IsAbs(s.Real) {
			return "the skill folder has no real path"
		}
		for rel, sum := range s.Files {
			if rel != path.Clean(rel) || strings.Contains(rel, `\`) || !filepath.IsLocal(filepath.FromSlash(rel)) {
				return fmt.Sprintf("it names the file %q, which is not inside the skill folder", rel)
			}
			if !sha256Pattern.MatchString(sum) {
				return fmt.Sprintf("the file %q has no sha256", rel)
			}
		}
		for _, d := range s.Parents {
			if d.Path == skill || !pathWithin(d.Path, skill) || d.Path == home || !pathWithin(home, d.Path) || !filepath.IsAbs(d.Real) {
				return fmt.Sprintf("it names the folder %s, which study setup never creates for the skill", d.Path)
			}
		}
	}
	if l := rec.Link; l != nil {
		if l.Path != link || l.Target != skill || !filepath.IsAbs(l.Parent) {
			return fmt.Sprintf("it names the link %s to %s, but study setup links %s to %s", l.Path, l.Target, link, skill)
		}
		for _, d := range l.Parents {
			if (d.Path != claude && d.Path != filepath.Dir(link)) || !filepath.IsAbs(d.Real) {
				return fmt.Sprintf("it names the folder %s, which study setup never creates for the link", d.Path)
			}
		}
	}
	seen := map[string]bool{}
	for _, g := range rec.Registrations {
		if !slices.Contains(setupAgents, g.Agent) || seen[g.Agent] || g.Name != mcpServerName ||
			!filepath.IsAbs(g.Command) || !slices.Equal(g.Args, []string{"mcp"}) {
			return fmt.Sprintf("it names a registration study setup never makes (%s %s %s %v)", g.Agent, g.Name, g.Command, g.Args)
		}
		seen[g.Agent] = true
	}
	return ""
}

func (a *app) writeSetupRecord(rec setupRecord) error {
	p, err := a.setupRecordPath()
	if err != nil {
		return err
	}
	if rec.empty() {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", p, err)
		}
		return nil
	}
	rec.Format = setupRecordFormat
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(p, append(data, '\n')); err != nil {
		return &core.Error{Code: core.CodeInternal, Err: err, Message: "recording what study setup did: " + err.Error()}
	}
	return nil
}
