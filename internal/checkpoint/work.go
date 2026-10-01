package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Errors returned by SnapshotWork for work whose snapshot would have a
// blind spot: something a Check could read or depend on that the snapshot
// would not cover. An Attempt on such work is errored.
var (
	// ErrHiddenEntries: the index marks files in the folder skip-worktree
	// or assume-unchanged, so git would report the index's version.
	ErrHiddenEntries = errors.New("git is told to look away from some files")
	// ErrNestedRepository: a git repository or submodule inside the folder,
	// whose files git leaves out.
	ErrNestedRepository = errors.New("the folder holds another git repository")
	// ErrLinkOutside: a symbolic link that leads outside the folder.
	ErrLinkOutside = errors.New("a symbolic link leads outside the folder")
	// ErrAllIgnored: the folder holds files, but every one is ignored.
	ErrAllIgnored = errors.New("every file in the folder is ignored")
)

// Work is a snapshot of one folder of a Topic, as an Attempt records it.
type Work struct {
	// Hash covers every file in the folder that is not ignored (its path,
	// mode and content) and every ignore rule that applies inside the
	// folder, so ignoring a file after a pass changes it too.
	Hash string
	// Files maps each file, relative to the folder, to its mode and blob
	// hash.
	Files map[string]string
	// Rules is the digest of the ignore rules that apply.
	Rules string
}

// Changed lists what differs between two snapshots of one folder: the
// files added, removed or changed, and "ignore rules" when those changed.
func (w Work) Changed(other Work) []string {
	var changed []string
	for p, v := range w.Files {
		if other.Files[p] != v {
			changed = append(changed, p)
		}
	}
	for p := range other.Files {
		if _, ok := w.Files[p]; !ok {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	if w.Rules != other.Rules {
		changed = append(changed, "ignore rules")
	}
	return changed
}

// SnapshotWork snapshots the folder subpath (such as "practice/<lesson-id>")
// for an Attempt. Like Snapshot, it covers tracked and untracked files that
// are not ignored, as raw bytes, so build outputs can be ignored. Unlike
// Snapshot, it writes nothing to .git, not even objects, and it refuses work
// whose snapshot would have a blind spot: files the index tells git to look
// away from, a nested repository, a symbolic link leading outside the
// folder, or a folder whose files are all ignored. A missing folder is
// empty work.
func SnapshotWork(ctx context.Context, dir, subpath string) (Work, error) {
	sub, err := cleanSubpath(subpath)
	if err != nil {
		return Work{}, err
	}
	if sub == "" {
		return Work{}, fmt.Errorf("%w: name a folder inside the Topic", ErrInvalidPath)
	}
	r, err := open(ctx, dir)
	if err != nil {
		return Work{}, err
	}
	defer r.close()
	w := Work{Files: map[string]string{}}
	if exists, err := r.folderExists(sub); err != nil {
		return Work{}, err
	} else if !exists {
		w.Rules = r.ignoreDigest(sub, nil)
		w.Hash = w.digest()
		return w, nil
	}

	out, err := r.git(ctx, call{readOnly: true}, "ls-files", "-z", "--cached", "--stage", "-v", "--", sub)
	if err != nil {
		return Work{}, err
	}
	var candidates []string
	seen := map[string]bool{}
	for _, record := range splitNUL(out) {
		meta, p, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 {
			return Work{}, fmt.Errorf("git ls-files: unexpected entry %q", record)
		}
		tag, mode := fields[0], fields[1]
		switch {
		case tag == "S" || tag == "s" || tag != strings.ToUpper(tag):
			return Work{}, fmt.Errorf("%w: %s is marked skip-worktree or assume-unchanged in the index", ErrHiddenEntries, p)
		case mode == modeGitlink:
			return Work{}, fmt.Errorf("%w: %s is a submodule", ErrNestedRepository, p)
		}
		if !seen[p] {
			seen[p] = true
			candidates = append(candidates, p)
		}
	}
	out, err = r.git(ctx, call{readOnly: true}, "ls-files", "-z", "--others", "--exclude-standard", "--", sub)
	if err != nil {
		return Work{}, err
	}
	for _, p := range splitNUL(out) {
		if strings.HasSuffix(p, "/") {
			return Work{}, fmt.Errorf("%w: %s", ErrNestedRepository, strings.TrimSuffix(p, "/"))
		}
		candidates = append(candidates, p)
	}

	states, err := r.hashWorktree(ctx, candidates, false)
	if err != nil {
		return Work{}, err
	}
	for p, st := range states {
		rel := strings.TrimPrefix(p, sub+"/")
		if st.mode == modeSymlink {
			target, err := r.root.Readlink(p)
			if err != nil {
				return Work{}, fmt.Errorf("%s %w: %v", p, errChanged, err)
			}
			if !insideFolder(sub, p, target) {
				return Work{}, fmt.Errorf("%w: %s points to %s", ErrLinkOutside, rel, target)
			}
		}
		w.Files[rel] = st.mode + " " + st.oid
	}
	if len(w.Files) == 0 {
		nonEmpty, err := r.holdsFiles(sub)
		if err != nil {
			return Work{}, err
		}
		if nonEmpty {
			return Work{}, fmt.Errorf("%w (%s)", ErrAllIgnored, sub)
		}
	}
	ignored, err := r.ignoredFolders(ctx, sub)
	if err != nil {
		return Work{}, err
	}
	w.Rules = r.ignoreDigest(sub, ignored)
	w.Hash = w.digest()
	return w, nil
}

// ignoredFolders lists the folders inside sub that git ignores as a whole and
// never looks into.
func (r *repo) ignoredFolders(ctx context.Context, sub string) (map[string]bool, error) {
	out, err := r.git(ctx, call{readOnly: true}, "ls-files", "-z", "--others", "--ignored", "--exclude-standard",
		"--directory", "--", sub)
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	for _, p := range splitNUL(out) {
		if d, ok := strings.CutSuffix(p, "/"); ok {
			dirs[d] = true
		}
	}
	return dirs, nil
}

// insideFolder reports whether a symbolic link at link, inside the folder
// sub, leads to a path inside sub. Links are judged by their text, as git
// stores them; a link to a link is judged when that link is.
func insideFolder(sub, link, target string) bool {
	if path.IsAbs(target) || filepath.IsAbs(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(link), target))
	return resolved == sub || strings.HasPrefix(resolved, sub+"/")
}

// holdsFiles reports whether the folder sub holds any file or link, outside
// nested .git folders. It stops at the first one.
func (r *repo) holdsFiles(sub string) (bool, error) {
	found := false
	err := fs.WalkDir(r.root.FS(), sub, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case !d.IsDir():
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// ignoreDigest hashes every ignore rule that can apply inside the folder
// sub: each .gitignore from the Topic's top folder down to sub, each inside
// it that git reads (ignored folders are never looked into), even one that
// ignores itself, .git/info/exclude, and the learner's global excludes file.
// Files are read through the pinned Topic; a .gitignore that is a symbolic
// link is recorded by its target, since git does not follow it.
func (r *repo) ignoreDigest(sub string, ignored map[string]bool) string {
	h := sha256.New()
	add := func(name string, data []byte) {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(data))
		h.Write(data)
	}
	read := func(root *os.Root, name string) []byte {
		info, err := root.Lstat(name)
		if err != nil {
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := root.Readlink(name)
			return []byte("link:" + target)
		}
		data, _ := root.ReadFile(name)
		return data
	}
	var names []string
	for dir := sub; ; dir = path.Dir(dir) {
		if dir == "." {
			names = append(names, ".gitignore")
			break
		}
		names = append(names, dir+"/.gitignore")
	}
	_ = fs.WalkDir(r.root.FS(), sub, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir() && (d.Name() == ".git" || ignored[p]):
			return fs.SkipDir
		case !d.IsDir() && d.Name() == ".gitignore" && p != sub+"/.gitignore":
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	for _, name := range names {
		add(name, read(r.root, name))
	}
	add(".git/info/exclude", read(r.gitRoot, "info/exclude"))
	// The rules count, not where the file is.
	data, _ := os.ReadFile(r.globalExcludes())
	add("global excludes", data)
	return hex.EncodeToString(h.Sum(nil))
}

// globalExcludes is the learner's global ignore file: core.excludesFile, or
// git's default under XDG_CONFIG_HOME or HOME.
func (r *repo) globalExcludes() string {
	for _, o := range r.overrides {
		if o[0] == "core.excludesFile" {
			return o[1]
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "git", "ignore")
	}
	return ""
}

func (w Work) digest() string {
	paths := make([]string, 0, len(w.Files))
	for p := range w.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\t%s\x00", w.Files[p], p)
	}
	fmt.Fprintf(h, "rules %s\x00", w.Rules)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
