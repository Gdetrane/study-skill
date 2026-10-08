package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// The files of an import: what the copy holds and leaves out, the moves to
// v2's places, the copy itself and the Topic assembled from it.

// entryKind is what a path in the workspace is.
type entryKind uint8

const (
	entryFile entryKind = iota + 1
	entryDir
	entryLink
	entryOther
)

// workspaceScan is what a walk of the v1 workspace found.
type workspaceScan struct {
	kinds map[string]entryKind
	// skipped are the paths the copy leaves out, with why; a folder's whole
	// content goes with it, unwalked.
	skipped map[string]string
	// files and bytes are what the copy holds.
	files int
	bytes int64
	// gitLocks are the lock files git leaves while it writes.
	gitLocks []string
}

// skippedAt reports whether the copy leaves p out, itself or with a folder
// above it.
func (w *workspaceScan) skippedAt(p string) bool {
	for ; p != "." && p != "/"; p = path.Dir(p) {
		if w.skipped[p] != "" {
			return true
		}
	}
	return false
}

// checkV1Git refuses a git repository a copy would not carry whole: a
// linked worktree or submodule (.git is a file), or one that borrows
// objects from another (alternates), whose objects would be missing.
func checkV1Git(src *os.Root) (hasGit bool, err error) {
	info, err := src.Lstat(".git")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, internalError("reading .git", err)
	}
	if !info.IsDir() {
		return false, &Error{Code: CodeFailedPrecondition, Message: ".git is not a folder (a linked worktree or a submodule): import the main repository's folder"}
	}
	if _, err := src.Lstat(".git/objects/info/alternates"); err == nil {
		return false, &Error{Code: CodeFailedPrecondition, Message: "the repository borrows objects from another one " +
			"(.git/objects/info/alternates), which a copy would lose. Make it hold its own: run git repack -a -d in the " +
			"workspace, then delete .git/objects/info/alternates, and import again"}
	}
	return true, nil
}

// scanWorkspace walks the workspace without following links. It leaves out
// of the copy links that lead outside the workspace, files that are neither
// regular nor folders nor links, v1's cards, and folders the language's
// tools rebuild, unless the git history tracks them.
func scanWorkspace(ctx context.Context, src *os.Root, srcPath string, hasGit bool) (*workspaceScan, error) {
	w := &workspaceScan{kinds: map[string]entryKind{}, skipped: map[string]string{}}
	var rebuildable []string
	walk := func(start string, keepAll bool) error {
		return fs.WalkDir(src.FS(), start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return internalError("reading the workspace", err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if p == "." || p == start && keepAll {
				return nil
			}
			inGit := p == ".git" || strings.HasPrefix(p, ".git/")
			switch t := d.Type(); {
			case t.IsDir():
				w.kinds[p] = entryDir
				if p == v1CardsDir {
					w.skipped[p] = "v1's cards"
					return fs.SkipDir
				}
				if !inGit && !keepAll {
					if why := rebuildableFolder(src, p); why != "" {
						w.skipped[p] = why
						rebuildable = append(rebuildable, p)
						return fs.SkipDir
					}
				}
			case t&fs.ModeSymlink != 0:
				w.kinds[p] = entryLink
				if why := escapingLink(src, p); why != "" {
					w.skipped[p] = why
				}
			case t.IsRegular():
				w.kinds[p] = entryFile
				if p == v1CardsDir {
					w.skipped[p] = "v1's cards"
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return internalError("reading "+p, err)
				}
				w.files++
				w.bytes += info.Size()
				if inGit && strings.HasSuffix(p, ".lock") && (path.Dir(p) == ".git" || strings.HasPrefix(p, ".git/refs/")) {
					w.gitLocks = append(w.gitLocks, p)
				}
			default:
				w.kinds[p] = entryOther
				w.skipped[p] = "not a file, folder or link: not copied"
			}
			return nil
		})
	}
	if err := walk(".", false); err != nil {
		return nil, err
	}
	// A rebuildable folder the learner committed is part of their work, and
	// leaving it out would make the next Checkpoint delete it.
	if len(rebuildable) > 0 && hasGit {
		tracked, err := checkpoint.Tracked(ctx, srcPath, rebuildable)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracked = map[string]bool{}
			for _, p := range rebuildable {
				tracked[p] = true
			}
		}
		for _, p := range rebuildable {
			if tracked[p] {
				delete(w.skipped, p)
				if err := walk(p, true); err != nil {
					return nil, err
				}
			}
		}
	}
	return w, nil
}

// rebuildableFolder says why the copy leaves out a folder the language's
// tools rebuild, or "" for any other. The list is kept short: anything else
// is copied, git-ignored or not, since ignored files can be the learner's
// data.
func rebuildableFolder(src *os.Root, p string) string {
	const rebuild = ": not copied, rebuild it with the language's tools"
	switch name := path.Base(p); name {
	case "node_modules":
		return "Node packages" + rebuild + " (npm install)"
	case "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".tox":
		return "a Python cache" + rebuild
	case ".venv", "venv":
		return "a Python virtual environment" + rebuild + " (python -m venv, uv sync)"
	case "target":
		for _, manifest := range []string{"Cargo.toml", "pom.xml"} {
			if isRegularFile(src, path.Join(path.Dir(p), manifest)) {
				return "build output" + rebuild + " (cargo build, mvn package)"
			}
		}
	}
	if isRegularFile(src, path.Join(p, "pyvenv.cfg")) {
		return "a Python virtual environment" + rebuild + " (python -m venv, uv sync)"
	}
	return ""
}

// escapingLink says why the copy leaves out a link, or "" for one it keeps:
// a link is kept when it leads to a place inside the workspace, following
// every link on the way, or to nothing yet.
func escapingLink(src *os.Root, p string) string {
	target, err := src.Readlink(p)
	if err != nil {
		return "a link whose target cannot be read: not copied"
	}
	resolved := path.Join(path.Dir(p), filepath.ToSlash(target))
	if filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Sprintf("a link leading outside the workspace (to %s): not copied", clip(target, 120))
	}
	if _, err := src.Stat(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("a link that cannot be followed inside the workspace (to %s), as one leading out "+
			"through another link: not copied", clip(target, 120))
	}
	return ""
}

// movePlan plans the files an import moves to their v2 place, each checked
// against the workspace as scanned, so that the dry run and the import
// agree and no move can fail halfway or replace a file.
type movePlan struct {
	scan  *workspaceScan
	moves []ImportMove
	// from maps a moved file to the Lesson that moves it, or "-".
	from map[string]string
	to   map[string]bool
}

func newMovePlan(scan *workspaceScan) *movePlan {
	return &movePlan{scan: scan, moves: []ImportMove{}, from: map[string]string{}, to: map[string]bool{}}
}

// problem says why from cannot move to to, or "": nothing under .git moves,
// a file moves once, a target is new, and each folder above it is a folder
// or will be created.
func (m *movePlan) problem(from, to string) string {
	switch {
	case from == ".git" || strings.HasPrefix(from, ".git/"):
		return from + " is inside .git"
	case m.scan.kinds[from] == 0 || m.scan.skippedAt(from):
		return from + " is not in the copy"
	case m.from[from] != "":
		return from + " moves already"
	case m.scan.kinds[to] != 0 && !m.scan.skippedAt(to) || m.to[to]:
		return to + " is taken"
	}
	for dir := path.Dir(to); dir != "."; dir = path.Dir(dir) {
		if k := m.scan.kinds[dir]; k != 0 && k != entryDir && !m.scan.skippedAt(dir) {
			return dir + " is not a folder"
		}
	}
	return ""
}

func (m *movePlan) add(from, to, by string) {
	if by == "" {
		by = "-"
	}
	m.moves = append(m.moves, ImportMove{From: from, To: to})
	m.from[from], m.to[to] = by, true
}

// lessonFile plans moving a v1 Lesson's file to lessons/<id>.md, and
// reports whether v1 names one: a regular file under lessons/, where v1
// writes them. A file elsewhere stays where it is; a file two Lessons share
// moves with the first.
func (m *movePlan) lessonFile(lessonID, v1File string, drop func(what, detail string)) bool {
	what := "lesson file of " + lessonID
	file, err := v1RelPath(v1File)
	switch {
	case err != nil:
		drop(what, "v1 names no file inside the workspace for it: "+err.Error())
		return false
	case !strings.HasPrefix(file, "lessons/"):
		drop(what, file+" is not under lessons/, where v1 writes Lesson files: it stays where it is, and is not the Lesson's file")
		return false
	case m.scan.kinds[file] != entryFile || m.scan.skippedAt(file):
		drop(what, "v1 names "+clip(file, 80)+", which is not a file in the workspace")
		return false
	case m.from[file] != "":
		drop(what, file+" is shared with "+m.from[file]+", which it moves with: it stays there")
		return true
	}
	target := lessonFile(lessonID)
	if file == target {
		return true
	}
	if why := m.problem(file, target); why != "" {
		drop("moving "+file, why+": it stays where it is")
		return true
	}
	m.add(file, target, lessonID)
	return true
}

// Points where a test can interrupt an import, as a crash would.
const (
	crashImportCopying = "import-copying"  // the staging folder exists, the copy starts
	crashImportCopied  = "import-copied"   // the copy is in staging, no Event is written
	crashImportStaged  = "import-staged"   // the Topic is complete in staging, not moved into place
	crashImportInPlace = "import-in-place" // the Topic is in place, not yet saved by a Checkpoint
)

// staleStaging is how old a staging folder must be before an import removes
// it: no import or Topic creation runs that long without its lock.
const staleStaging = time.Hour

// runImport carries out a planned import. The Topic is assembled under
// .lamplight/tmp and moved into place only once complete, as CreateTopic
// does: an interrupted import leaves no Topic behind, and importing again
// starts over. The caller holds the import lock.
func (c *Core) runImport(ctx context.Context, home, src *os.Root, p *importPlan) (TopicImport, error) {
	scratch := filepath.Join(localDir, "tmp")
	if err := home.MkdirAll(scratch, 0o755); err != nil {
		return TopicImport{}, internalError("creating "+scratch, err)
	}
	// The Topic's lock marks the staging folder as in use.
	unlockTopic, err := lockTopic(ctx, home, p.id)
	if err != nil {
		return TopicImport{}, err
	}
	locked := true
	release := func() {
		if locked {
			unlockTopic()
			locked = false
		}
	}
	defer release()
	staging := filepath.Join(scratch, p.id+"."+randomID())
	if err := home.Mkdir(staging, 0o755); err != nil {
		return TopicImport{}, internalError("creating "+staging, err)
	}
	stagingPath := filepath.Join(c.home, staging)
	fail := func(err error) (TopicImport, error) {
		_ = home.RemoveAll(staging)
		return TopicImport{}, err
	}
	root, err := home.OpenRoot(staging)
	if err != nil {
		return fail(internalError("opening "+staging, err))
	}
	defer root.Close()

	if err := c.crashAt(crashImportCopying); err != nil {
		return TopicImport{}, err
	}
	if err := copyWorkspace(ctx, src, root, p.scan); err != nil {
		return fail(err)
	}
	if err := c.crashAt(crashImportCopied); err != nil {
		return TopicImport{}, err
	}
	for _, m := range p.data.Moved {
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(m.To)), 0o755); err != nil {
			return fail(internalError("creating "+path.Dir(m.To), err))
		}
		if err := root.Rename(filepath.FromSlash(m.From), filepath.FromSlash(m.To)); err != nil {
			return fail(internalError("moving "+m.From, err))
		}
	}
	if err := materializeLink(root, gitattributes); err != nil {
		return fail(err)
	}

	wall := c.now()
	created := nextEventTime(wall, time.Time{})
	ev, contents, err := c.prepareEvent(&topicView{root: root}, change{
		Type:  eventTopicCreated,
		Data:  topicCreatedData{Title: p.title, Goal: p.goal},
		Items: []string{topicFile},
	}, created, wall)
	if err != nil {
		return fail(err)
	}
	if err := c.appendEvent(root, p.id, ev); err != nil {
		return fail(err)
	}
	for i, it := range ev.Items {
		if err := writeItem(root, it.Item, contents[i]); err != nil {
			return fail(err)
		}
	}
	if err := mergeGitignore(root); err != nil {
		return fail(err)
	}

	var steps []plan
	if p.report.Level != "" {
		steps = append(steps, planImportedLevel(p.id, p.report.Level))
	}
	if p.report.Approach != "" {
		steps = append(steps, planApproach(p.id, p.report.Approach))
	}
	for _, source := range p.report.Sources {
		steps = append(steps, func(*replayed, *topicView) (*change, error) {
			return &change{Type: eventSourceAdded, Data: source, Items: []string{sourceItem(source.ID)}}, nil
		})
	}
	steps = append(steps, func(*replayed, *topicView) (*change, error) {
		return &change{Type: eventTopicImported, Data: p.data, Items: []string{gitattributes}}, nil
	})
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if _, err := c.stagedWrite(root, p.id, step); err != nil {
			return fail(err)
		}
	}
	if p.data.Head == "" && p.scan.kinds[".git"] == 0 {
		if err := gitInit(ctx, stagingPath); err != nil {
			return fail(err)
		}
	}
	if err := c.crashAt(crashImportStaged); err != nil {
		return TopicImport{}, err
	}
	root.Close()
	if err := home.Rename(staging, p.id); err != nil {
		_ = home.RemoveAll(staging)
		if _, statErr := home.Lstat(p.id); statErr == nil {
			return TopicImport{}, alreadyExists(p.id)
		}
		return TopicImport{}, internalError("moving the imported Topic into place", err)
	}
	release()
	for id, f := range p.outside {
		c.rememberLocation(p.id, id, f)
	}
	if err := c.setRecentTopic(home, p.id); err != nil {
		c.log.Warn("could not record the most recent Topic", "topic", p.id, "err", err)
	}
	c.log.Info("topic imported from v1", "topic", p.id, "from", p.report.From)

	r := p.report
	if err := c.crashAt(crashImportInPlace); err != nil {
		return TopicImport{}, err
	}
	if res, err := c.Checkpoint(ctx, CheckpointSpec{Topic: p.id, Role: "agent", Message: "Imported from v1"}); err != nil {
		r.CheckpointError = err.Error()
	} else {
		r.Checkpoint = res.Commit
	}
	if topic, err := c.loadTopic(home, p.id); err == nil {
		r.Topic = topic
	}
	return r, nil
}

// stagedWrite records one change in a Topic being assembled under
// .lamplight/tmp: no other process can see it yet, so it needs neither the
// lock nor an intent marker; a crash leaves only the staging folder.
func (c *Core) stagedWrite(root *os.Root, topicID string, p plan) (*event, error) {
	h, err := readHistory(root, topicID)
	if err != nil {
		return nil, err
	}
	s := replayHistory(h)
	ch, err := p(s, newView(root, s))
	if err != nil || ch == nil {
		return nil, err
	}
	wall := c.now()
	ev, contents, err := c.prepareEvent(newView(root, s), *ch, nextEventTime(wall, s.latest), wall)
	if err != nil {
		return nil, err
	}
	if err := c.appendEvent(root, topicID, ev); err != nil {
		return nil, err
	}
	for i, it := range ev.Items {
		if err := writeItem(root, it.Item, contents[i]); err != nil {
			return nil, err
		}
	}
	return &ev, nil
}

// lamplightStateFiles are the files at the top of a Topic Lamplight keeps
// its state in, which a v1 .gitignore must not leave out of Checkpoints.
var lamplightStateFiles = []string{topicFile, historyFile, syllabusFile, cardsFile, sourcesFile, tasksFile,
	gitattributes, gitignore}

// heldOutLines are the last lines of Lamplight's default .gitignore: the
// .heldout folder itself, which a learner's line such as .* may leave out
// (and git never looks inside a folder it leaves out), then what it holds.
var heldOutLines = []string{"!/.heldout/", "!.heldout/**"}

// mergeGitignore merges the workspace's .gitignore with Lamplight's: the
// learner's lines first, then the default lines they lack, then lines that
// keep Lamplight's state files tracked. In a .gitignore the last matching
// line wins, so a v1 line such as *.jsonl cannot leave the History out of
// Checkpoints. Without a .gitignore, the Topic gets Lamplight's default.
func mergeGitignore(root *os.Root) error {
	current, err := root.ReadFile(gitignore)
	if errors.Is(err, fs.ErrNotExist) {
		return writeFileAtomic(root, gitignore, []byte(checkpoint.DefaultGitignore()))
	}
	if err != nil {
		return internalError("reading "+gitignore, err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(current), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, line := range strings.Split(checkpoint.DefaultGitignore(), "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") && !slices.Contains(heldOutLines, t) && !have[t] {
			add = append(add, t)
		}
	}
	out := string(current)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n# Added by Lamplight when the v1 workspace was imported. These lines come last, so they win.\n"
	if len(add) > 0 {
		out += strings.Join(add, "\n") + "\n"
	}
	out += strings.Join(heldOutLines, "\n") + "\n# Lamplight's state files are always saved by Checkpoints.\n"
	for _, f := range lamplightStateFiles {
		out += "!/" + f + "\n"
	}
	return writeFileAtomic(root, gitignore, []byte(out))
}

// mergeAttributes is an imported Topic's .gitattributes: the workspace's
// lines, if any, then Lamplight's. In a .gitattributes the last matching
// line wins, so Lamplight's come last, and reset every attribute that would
// change how git stores or merges its state files: a v1 line such as
// "*.jsonl filter=lfs merge=lfs -text" then leaves them alone.
func mergeAttributes(current []byte, exists bool) []byte {
	lamplight := strings.Join(unionFiles, " merge=union\n") + " merge=union\n"
	if !exists || strings.TrimSpace(string(current)) == "" {
		return []byte(lamplight)
	}
	out := string(current)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n# Lamplight's state files. These lines come last, so they win over the ones above.\n"
	const reset = " !filter !text !eol !ident !working-tree-encoding"
	for _, f := range unionFiles {
		out += f + " merge=union" + reset + "\n"
	}
	for _, f := range []string{topicFile, syllabusFile} {
		out += f + " !merge" + reset + "\n"
	}
	return []byte(out)
}

// materializeLink replaces a link inside the copy with a regular file of
// the content it leads to, so the file can be recorded; a link leading to
// nothing is removed.
func materializeLink(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	data, readErr := root.ReadFile(name)
	if err := root.Remove(name); err != nil {
		return internalError("replacing the link "+name, err)
	}
	if readErr != nil {
		return nil
	}
	return writeFileAtomic(root, name, data)
}

// copyWorkspace copies the workspace, .git included, into dst as the scan
// planned it: folders, regular files with their permissions and times, and
// links that stay inside the workspace, as links. A link is checked again
// as it is copied, in case the workspace changed since the scan.
func copyWorkspace(ctx context.Context, src, dst *os.Root, scan *workspaceScan) error {
	return fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return internalError("reading the workspace", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if scan.skipped[p] != "" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := filepath.FromSlash(p)
		switch t := d.Type(); {
		case t.IsDir():
			info, err := d.Info()
			if err != nil {
				return internalError("reading "+p, err)
			}
			if err := dst.Mkdir(name, info.Mode().Perm()|0o700); err != nil {
				return internalError("copying "+p, err)
			}
		case t&fs.ModeSymlink != 0:
			if escapingLink(src, p) != "" {
				return nil
			}
			target, err := src.Readlink(name)
			if err != nil {
				return internalError("reading the link "+p, err)
			}
			if err := dst.Symlink(target, name); err != nil {
				return internalError("copying the link "+p, err)
			}
		case t.IsRegular():
			if err := copyFileInRoots(ctx, src, dst, name); err != nil {
				return err
			}
		}
		return nil
	})
}

func copyFileInRoots(ctx context.Context, src, dst *os.Root, name string) error {
	in, err := src.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return internalError("reading "+name, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return internalError("reading "+name, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	out, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return internalError("copying "+name, err)
	}
	if _, err := io.Copy(out, ctxReader{ctx: ctx, r: in}); err != nil {
		out.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return internalError("copying "+name, err)
	}
	if err := out.Close(); err != nil {
		return internalError("copying "+name, err)
	}
	return dst.Chtimes(name, info.ModTime(), info.ModTime())
}

// hashInRoot hashes a regular file inside root, never following a link out
// of it and never blocking on a FIFO.
func hashInRoot(ctx context.Context, root *os.Root, rel string) (string, int64, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errNotRegular
	}
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx: ctx, r: f})
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// readSmallFile reads a regular file inside root, refusing one bigger than
// max and never blocking on a FIFO.
func readSmallFile(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, internalError("reading "+name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, invalidf("%s is not a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, internalError("reading "+name, err)
	}
	if int64(len(data)) > max {
		return nil, invalidf("%s is bigger than %d bytes", name, max)
	}
	return data, nil
}

// sweepStaging removes what interrupted imports and Topic creations left in
// .lamplight/tmp. The caller holds the import lock.
func (c *Core) sweepStaging(home *os.Root) {
	for _, name := range staleStagingFolders(home) {
		rel := filepath.Join(localDir, "tmp", name)
		if err := home.RemoveAll(rel); err != nil {
			c.log.Warn("could not remove a stale staging folder", "folder", rel, "err", err)
			continue
		}
		c.log.Info("removed a stale staging folder", "folder", rel)
	}
}

// staleStagingFolders lists the folders in .lamplight/tmp older than
// staleStaging whose Topic no process holds the lock of. It writes nothing.
func staleStagingFolders(home *os.Root) []string {
	entries, err := fs.ReadDir(home.FS(), path.Join(filepath.ToSlash(localDir), "tmp"))
	if err != nil {
		return nil
	}
	var stale []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleStaging {
			continue
		}
		id := e.Name()
		if i := strings.LastIndexByte(id, '.'); i > 0 {
			id = id[:i]
		}
		if validateTopicID(id) == nil && lockHeld(home, id) {
			continue
		}
		stale = append(stale, e.Name())
	}
	return stale
}
