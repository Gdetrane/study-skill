package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Git file modes.
const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
	modeGitlink = "160000"
)

// blobBatch bounds how many files one hash-object call holds open.
const blobBatch = 128

// indexEntry is one line of `git ls-files --stage -t`.
type indexEntry struct {
	skipWorktree bool
	mode         string
	oid          string
	stage        string
	path         string
}

// fileState is what the working tree holds at one path, as written to the
// object database.
type fileState struct {
	mode string
	oid  string
	size int64
}

// listIndex lists the entries of an index, optionally under one folder.
//
// ls-files only prints what the index already holds; it reads no file in the
// working tree, so no filter, textconv or fsmonitor can run. -t marks entries
// that a sparse checkout keeps out of the working tree.
func (r *repo) listIndex(ctx context.Context, index, sub string) ([]indexEntry, error) {
	args := []string{"ls-files", "-z", "--cached", "--stage", "-t"}
	if sub != "" {
		args = append(args, "--", sub)
	}
	out, err := r.git(ctx, call{readOnly: true, index: index}, args...)
	if err != nil {
		return nil, err
	}
	var entries []indexEntry
	for _, record := range splitNUL(out) {
		meta, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 {
			return nil, fmt.Errorf("git ls-files: unexpected entry %q", record)
		}
		entries = append(entries, indexEntry{
			skipWorktree: fields[0] == "S",
			mode:         fields[1],
			oid:          fields[2],
			stage:        fields[3],
			path:         path,
		})
	}
	return entries, nil
}

// listUntracked lists files that are neither tracked nor ignored, optionally
// under one folder.
//
// --others only walks directories and matches names against .gitignore,
// .git/info/exclude and core.excludesFile; it never reads file contents, so
// no filter runs, and core.fsmonitor is forced off. Nested repositories are
// printed as "name/" and are left out: they keep their own history.
func (r *repo) listUntracked(ctx context.Context, index, sub string) ([]string, error) {
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if sub != "" {
		args = append(args, "--", sub)
	}
	out, err := r.git(ctx, call{readOnly: true, index: index}, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range splitNUL(out) {
		if !strings.HasSuffix(p, "/") {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// hashWorktree hashes every path that holds a regular file or a symbolic link,
// writing the blobs when write is set, and returns what it found. Paths that
// are missing, that are folders or special files, or that lie beyond a
// symbolic link (as git would see them) are left out of the result, which
// records them as deleted; so are files that vanish while being hashed.
//
// Files are opened by this process through the pinned Topic root, so nothing
// outside the Topic is ever read, and handed to git as already-open
// descriptors (/dev/fd/N). git therefore hashes exactly the file that was
// checked, with no window in which a folder could be swapped for a symbolic
// link. --no-filters means the raw bytes are stored: no clean or process
// filter, line-ending conversion or Git LFS.
//
// A file that an editor replaces or rewrites while it is being hashed makes
// the whole pass start again once; if files keep changing, it gives up with
// ErrWorktreeChanged.
func (r *repo) hashWorktree(ctx context.Context, paths []string, write bool) (map[string]fileState, error) {
	states, err := r.hashWorktreeOnce(ctx, paths, write)
	if errors.Is(err, errChanged) {
		states, err = r.hashWorktreeOnce(ctx, paths, write)
	}
	if errors.Is(err, errChanged) {
		return nil, fmt.Errorf("%w: %v", ErrWorktreeChanged, err)
	}
	return states, err
}

// errChanged marks a file that changed while it was being hashed.
var errChanged = errors.New("changed while the Checkpoint was being taken")

func (r *repo) hashWorktreeOnce(ctx context.Context, paths []string, write bool) (map[string]fileState, error) {
	s := scanner{root: r.root, dirs: map[string]bool{}}
	states := make(map[string]fileState, len(paths))
	var files []pending
	for _, p := range paths {
		info, err := s.lstat(p)
		if err != nil {
			return nil, err
		}
		switch {
		case info == nil:
		case info.Mode().IsRegular():
			files = append(files, pending{path: p, info: info})
		case info.Mode()&fs.ModeSymlink != 0:
			state, ok, err := r.hashSymlink(ctx, p, write)
			if err != nil {
				return nil, err
			}
			if ok {
				states[p] = state
			}
		}
	}
	for start := 0; start < len(files); start += blobBatch {
		batch := files[start:min(start+blobBatch, len(files))]
		if err := r.hashFiles(ctx, batch, write, states); err != nil {
			return nil, err
		}
	}
	return states, nil
}

// pending is a regular file waiting to be hashed.
type pending struct {
	path string
	info fs.FileInfo
}

// hashFiles hashes one batch of regular files, writing them as blobs when
// write is set.
func (r *repo) hashFiles(ctx context.Context, batch []pending, write bool, states map[string]fileState) error {
	var opened []*os.File
	var kept []pending
	var before []fs.FileInfo
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	args := []string{"hash-object", "--no-filters", "--"}
	if write {
		args = []string{"hash-object", "-w", "--no-filters", "--"}
	}
	hook("hash")
	for _, p := range batch {
		// O_NONBLOCK keeps a FIFO swapped in after the check from blocking;
		// O_NOFOLLOW refuses a symbolic link swapped in for the file.
		f, err := r.root.OpenFile(p.path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if missing(err) {
			continue // deleted since it was listed: record it as deleted
		}
		if err != nil {
			return fmt.Errorf("%s %w: %v", p.path, errChanged, err)
		}
		now, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !now.Mode().IsRegular() || !os.SameFile(p.info, now) {
			f.Close()
			return fmt.Errorf("%s %w", p.path, errChanged)
		}
		args = append(args, "/dev/fd/"+strconv.Itoa(r.fdBase()+len(opened)))
		opened = append(opened, f)
		kept = append(kept, p)
		before = append(before, now)
	}
	if len(kept) == 0 {
		return nil
	}
	out, err := r.git(ctx, call{files: opened}, args...)
	if err != nil {
		return err
	}
	oids := strings.Fields(out)
	if len(oids) != len(kept) {
		return fmt.Errorf("git hash-object: got %d object IDs for %d files", len(oids), len(kept))
	}
	for i, p := range kept {
		// A file rewritten in place while git read it may have been hashed
		// half old, half new.
		after, err := opened[i].Stat()
		if err != nil {
			return err
		}
		if after.Size() != before[i].Size() || !after.ModTime().Equal(before[i].ModTime()) {
			return fmt.Errorf("%s %w", p.path, errChanged)
		}
		mode := modeFile
		if before[i].Mode()&0o100 != 0 {
			mode = modeExec
		}
		states[p.path] = fileState{mode: mode, oid: oids[i], size: before[i].Size()}
	}
	return nil
}

// hashSymlink hashes a symbolic link as git stores it: a blob holding its
// target, with mode 120000. The target is never followed. ok is false when
// the link vanished since it was listed.
func (r *repo) hashSymlink(ctx context.Context, path string, write bool) (_ fileState, ok bool, _ error) {
	// scanner.lstat has just checked that no leading component is a link,
	// and Root.Readlink resolves nothing outside the Topic.
	target, err := r.root.Readlink(path)
	if missing(err) {
		return fileState{}, false, nil
	}
	if err != nil {
		return fileState{}, false, fmt.Errorf("%s %w: %v", path, errChanged, err)
	}
	args := []string{"hash-object", "--no-filters", "--stdin"}
	if write {
		args = []string{"hash-object", "-w", "--no-filters", "--stdin"}
	}
	out, err := r.git(ctx, call{stdin: strings.NewReader(target)}, args...)
	if err != nil {
		return fileState{}, false, err
	}
	return fileState{mode: modeSymlink, oid: strings.TrimSpace(out), size: int64(len(target))}, true, nil
}

// scanner looks at working tree paths the way git does: a path is absent when
// any of its leading components is not a real folder.
type scanner struct {
	root *os.Root
	dirs map[string]bool // leading folder → whether it is a real folder
}

// lstat returns the file at path without following a final symbolic link,
// or nil when git would not see anything there.
func (s *scanner) lstat(path string) (fs.FileInfo, error) {
	if !filepath.IsLocal(path) {
		return nil, fmt.Errorf("git listed a path outside the Topic: %q", path)
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '/' {
			continue
		}
		dir := path[:i]
		isDir, seen := s.dirs[dir]
		if !seen {
			info, err := s.root.Lstat(dir)
			if err != nil && !missing(err) {
				return nil, err
			}
			isDir = err == nil && info.IsDir()
			s.dirs[dir] = isDir
		}
		if !isDir {
			return nil, nil
		}
	}
	info, err := s.root.Lstat(path)
	if missing(err) {
		return nil, nil
	}
	return info, err
}

// missing reports whether err means nothing is at a path.
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// splitNUL splits NUL-terminated output.
func splitNUL(out string) []string {
	out = strings.TrimSuffix(out, "\x00")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\x00")
}
