package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Where a file Source is differs from one computer to the next, so it is
// local state, never synced: .lamplight/sources/<topic>.json in the Study
// home. A file inside the Topic needs no entry to be found, but one is kept
// for it too, so its content is only hashed again when its size or
// modification time changes.
//
// The file is a cache that Lamplight can always rebuild, by finding files in
// the Topic and in the Library by their content. It is written without the
// Topic lock, so two processes updating it at once may lose one entry, which
// the next listing finds again.

// sourceLocations is the content of .lamplight/sources/<topic>.json.
type sourceLocations struct {
	Format int `json:"format"`
	// Files maps a Source id to where its file was last found.
	Files map[string]sourceLocation `json:"files"`
}

type sourceLocation struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size_bytes"`
	ModTime time.Time `json:"mtime"`
}

func locationsPath(topicID string) string {
	return filepath.Join(localDir, "sources", topicID+".json")
}

// readLocations reads the Topic's local state. A missing or damaged file is
// an empty one; a file in a newer format is read as empty and never
// rewritten, so a newer study keeps its own state.
func (c *Core) readLocations(home *os.Root, topicID string) (locs sourceLocations, writable bool) {
	locs = sourceLocations{Format: FormatVersion, Files: map[string]sourceLocation{}}
	data, err := home.ReadFile(locationsPath(topicID))
	if err != nil {
		return locs, true
	}
	var read sourceLocations
	if err := json.Unmarshal(data, &read); err != nil || read.Format < 1 {
		c.log.Warn("ignored damaged local state; Lamplight finds the files again", "file", locationsPath(topicID))
		return locs, true
	}
	if read.Format > FormatVersion {
		return locs, false
	}
	if read.Files != nil {
		locs.Files = read.Files
	}
	return locs, true
}

// writeLocations writes the Topic's local state, best effort: it is a cache.
func (c *Core) writeLocations(home *os.Root, topicID string, locs sourceLocations) {
	path := locationsPath(topicID)
	data, err := json.MarshalIndent(locs, "", "  ")
	if err == nil {
		if err = home.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = writeFileAtomic(home, path, append(data, '\n'))
		}
	}
	if err != nil {
		c.log.Warn("could not record where a Source's file is", "file", path, "err", err)
	}
}

// rememberLocation records where a file Source is on this computer.
func (c *Core) rememberLocation(topicID, sourceID string, f foundFile) {
	home, err := os.OpenRoot(c.home)
	if err != nil {
		return
	}
	defer home.Close()
	locs, writable := c.readLocations(home, topicID)
	if !writable {
		return
	}
	locs.Files[sourceID] = sourceLocation{Path: f.path, Size: f.size, ModTime: f.modTime}
	c.writeLocations(home, topicID, locs)
}

// knownLocation returns where a file Source was last found on this
// computer, or "".
func (c *Core) knownLocation(topicID, sourceID string) string {
	home, err := os.OpenRoot(c.home)
	if err != nil {
		return ""
	}
	defer home.Close()
	locs, _ := c.readLocations(home, topicID)
	return locs.Files[sourceID].Path
}

// foundFile is a file read to be a Source.
type foundFile struct {
	// path is where the file is on this computer, absolute.
	path string
	// topicPath is its path inside the Topic, with forward slashes, or "".
	topicPath string
	hash      string
	size      int64
	modTime   time.Time
}

// findSourceFile resolves a file the learner or agent named and hashes it:
// ~ is the home folder, and a relative path is relative to the folder study
// started in. A file inside the Topic is noted with its path there.
func (c *Core) findSourceFile(ctx context.Context, topicID, file string) (foundFile, error) {
	path, err := cleanText("path", file, maxPathRunes)
	if err != nil {
		return foundFile{}, err
	}
	if path == "" {
		return foundFile{}, invalidf("give the Source's file")
	}
	path = c.expandPath(path)
	hash, size, modTime, err := hashFile(ctx, path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return foundFile{}, &Error{Code: CodeNotFound, Message: path + " does not exist"}
	case errors.Is(err, errNotRegular):
		return foundFile{}, invalidf("%s is not a regular file", path)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return foundFile{}, err
	case err != nil:
		return foundFile{}, internalError("reading "+path, err)
	}
	f := foundFile{path: path, hash: hash, size: size, modTime: modTime}
	if rel, ok := insideFolder(filepath.Join(c.home, topicID), path); ok {
		slashed := filepath.ToSlash(rel)
		for _, elem := range strings.Split(slashed, "/") {
			if elem == ".git" {
				return foundFile{}, invalidf("%s is inside the Topic's git repository, which is never a Source", path)
			}
		}
		f.topicPath = slashed
	}
	return f, nil
}

// insideFolder returns path relative to dir when path is inside it, with
// symbolic links resolved, so a symlinked Study home still matches.
func insideFolder(dir, path string) (string, bool) {
	try := func(dir, path string) (string, bool) {
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return rel, true
	}
	if rel, ok := try(dir, path); ok {
		return rel, true
	}
	return try(resolvedPath(dir), resolvedPath(path))
}

// errNotRegular marks a path that is not a regular file.
var errNotRegular = errors.New("not a regular file")

// hashFile returns a file's content hash, in the form the History uses, its
// size and its modification time. The file is opened without blocking and
// must be a regular file once open, so a FIFO or a device is refused and
// nothing can swap the file between the check and the read. Hashing stops
// when ctx is done.
func hashFile(ctx context.Context, path string) (hash string, size int64, modTime time.Time, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, time.Time{}, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, time.Time{}, errNotRegular
	}
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx: ctx, r: f})
	if err != nil {
		return "", 0, time.Time{}, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, info.ModTime(), nil
}

// ctxReader stops reading when its context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// locator finds file Sources on this computer, and keeps the local state up
// to date as it goes.
type locator struct {
	ctx      context.Context
	home     *os.Root
	topicDir string
	topicID  string
	locs     sourceLocations
	writable bool
	dirty    bool
	c        *Core

	index  *library.Index
	loaded bool
}

func (c *Core) newLocator(ctx context.Context, home *os.Root, topicID string) *locator {
	locs, writable := c.readLocations(home, topicID)
	return &locator{ctx: ctx, home: home, topicDir: filepath.Join(c.home, topicID), topicID: topicID,
		locs: locs, writable: writable, c: c}
}

// save writes the local state if locating changed it.
func (l *locator) save() {
	if l.dirty && l.writable {
		l.c.writeLocations(l.home, l.topicID, l.locs)
	}
}

func (l *locator) library() *library.Index {
	if !l.loaded {
		l.loaded = true
		if ix, err := library.Load(filepath.Join(l.c.home, libraryIndex)); err == nil {
			l.index = ix
		}
	}
	return l.index
}

// locate finds a file Source: inside the Topic, where it was last found, or
// in the Library by its content. A file is hashed only when its size or
// modification time differ from what was recorded when it was last found.
func (l *locator) locate(src Source) (path, state string, err error) {
	if src.TopicPath != "" {
		if !fs.ValidPath(src.TopicPath) || strings.Contains(src.TopicPath, `\`) {
			return "", SourceMissing, nil
		}
		path = filepath.Join(l.topicDir, filepath.FromSlash(src.TopicPath))
		switch ok, err := l.check(src, path); {
		case err != nil:
			return "", "", err
		case ok:
			return path, SourceOK, nil
		}
		if _, statErr := os.Stat(path); statErr == nil {
			return path, SourceChanged, nil
		}
		return "", SourceMissing, nil
	}
	changedAt := ""
	if loc, ok := l.locs.Files[src.ID]; ok {
		switch ok, err := l.check(src, loc.Path); {
		case err != nil:
			return "", "", err
		case ok:
			return loc.Path, SourceOK, nil
		}
		if _, statErr := os.Stat(loc.Path); statErr == nil {
			changedAt = loc.Path
		}
	}
	if ix := l.library(); ix != nil {
		for _, book := range ix.Books {
			if book.Size != src.Size || book.Path == changedAt {
				continue
			}
			switch ok, err := l.check(src, book.Path); {
			case err != nil:
				return "", "", err
			case ok:
				return book.Path, SourceOK, nil
			}
		}
	}
	if changedAt != "" {
		return changedAt, SourceChanged, nil
	}
	return "", SourceMissing, nil
}

// check reports whether the file at path is src, hashing it unless its size
// and modification time match what was recorded for src at that path. A
// match is recorded as where src is.
func (l *locator) check(src Source, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != src.Size {
		return false, nil
	}
	if loc, ok := l.locs.Files[src.ID]; ok && loc.Path == path && loc.Size == info.Size() && loc.ModTime.Equal(info.ModTime()) {
		return true, nil
	}
	hash, size, modTime, err := hashFile(l.ctx, path)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, err
	}
	if err != nil || hash != src.Hash {
		return false, nil
	}
	l.locs.Files[src.ID] = sourceLocation{Path: path, Size: size, ModTime: modTime}
	l.dirty = true
	return true, nil
}
