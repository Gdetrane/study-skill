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

// hashWorktree writes a blob for every path that holds a regular file or a
// symbolic link, and returns what it found. Paths that are missing, that are
// folders or special files, or that lie beyond a symbolic link (as git would
// see them) are left out of the result, which records them as deleted.
//
// Files are opened by this process through an os.Root, so nothing outside the
// Topic is ever read, and handed to git as already-open descriptors
// (/dev/fd/N). git therefore hashes exactly the file that was checked, with
// no window in which a folder could be swapped for a symbolic link.
// --no-filters means the raw bytes are stored: no clean or process filter,
// line-ending conversion or Git LFS.
func (r *repo) hashWorktree(ctx context.Context, paths []string) (map[string]fileState, error) {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	s := scanner{root: root, dirs: map[string]bool{}}
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
			state, err := r.hashSymlink(ctx, p)
			if err != nil {
				return nil, err
			}
			states[p] = state
		}
	}
	for start := 0; start < len(files); start += blobBatch {
		batch := files[start:min(start+blobBatch, len(files))]
		if err := r.hashFiles(ctx, root, batch, states); err != nil {
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

// hashFiles writes one batch of regular files as blobs.
func (r *repo) hashFiles(ctx context.Context, root *os.Root, batch []pending, states map[string]fileState) error {
	opened := make([]*os.File, 0, len(batch))
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	args := []string{"hash-object", "-w", "--no-filters", "--"}
	for i, p := range batch {
		// O_NONBLOCK keeps a FIFO swapped in after the check from blocking.
		f, err := root.OpenFile(p.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", p.path, err)
		}
		opened = append(opened, f)
		now, err := f.Stat()
		if err != nil {
			return err
		}
		if !now.Mode().IsRegular() || !os.SameFile(p.info, now) {
			return fmt.Errorf("%s changed while the checkpoint was being taken; try again", p.path)
		}
		args = append(args, "/dev/fd/"+strconv.Itoa(3+i))
	}
	out, err := r.git(ctx, call{files: opened}, args...)
	if err != nil {
		return err
	}
	oids := strings.Fields(out)
	if len(oids) != len(batch) {
		return fmt.Errorf("git hash-object: got %d object IDs for %d files", len(oids), len(batch))
	}
	for i, p := range batch {
		mode := modeFile
		if p.info.Mode()&0o100 != 0 {
			mode = modeExec
		}
		states[p.path] = fileState{mode: mode, oid: oids[i], size: p.info.Size()}
	}
	return nil
}

// hashSymlink stores a symbolic link as git does: a blob holding its target,
// with mode 120000. The target is never followed.
func (r *repo) hashSymlink(ctx context.Context, path string) (fileState, error) {
	// Go 1.24 has no Root.Readlink. scanner.lstat has just checked that no
	// leading component is a link, so this reads a link inside the Topic;
	// a concurrent swap could at worst expose a link's target text, never a
	// file's contents.
	target, err := os.Readlink(filepath.Join(r.dir, filepath.FromSlash(path)))
	if err != nil {
		return fileState{}, err
	}
	out, err := r.git(ctx, call{stdin: strings.NewReader(target)}, "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		return fileState{}, err
	}
	return fileState{mode: modeSymlink, oid: strings.TrimSpace(out), size: int64(len(target))}, nil
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
