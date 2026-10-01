// Package checkpoint takes Checkpoints: snapshots of a Topic, stored as git
// commits, taken whenever the turn passes between the agent and the learner
// so the learner's own work can be seen on its own. It also computes the
// snapshot hash of one folder that an Attempt records.
//
// # Security
//
// The agent can edit a Topic's .git/config and .gitattributes, and this code
// runs outside the agent's sandbox, so it never runs a program that
// repository configuration names (ADR-0009): hooks, clean, smudge and process
// filters, fsmonitor, textconv and external diff drivers, or signing programs.
// It uses only plumbing that cannot start one:
//
//   - ls-files lists tracked and untracked files without reading them;
//   - hash-object -w --no-filters writes blobs from files this process opened
//     through an os.Root, so nothing outside the Topic is read;
//   - update-index --index-info, write-tree, commit-tree --no-gpg-sign and
//     update-ref record them;
//   - diff-tree --no-textconv --no-ext-diff finds large files.
//
// Every call runs with GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1,
// GIT_DIR and GIT_WORK_TREE pinned to the Topic, no inherited GIT_* variable,
// stdin from the null device and --no-pager. Command-scope configuration sets
// core.hooksPath=/dev/null, core.fsmonitor=false, commit.gpgSign=false and
// protocol.allow=never, and switches off every hook event and every hook named
// in configuration (hook.<name>.command), which core.hooksPath alone does not
// stop. Read-only calls set GIT_OPTIONAL_LOCKS=0. The only calls that see the
// learner's global and system configuration are plain `git config --get`
// reads of user.name, user.email and core.excludesFile, made beforehand; the
// identity is then passed explicitly to commit-tree.
//
// The Topic's .git must be a real folder inside it, not a .git file, a
// symbolic link or a linked worktree, so a Checkpoint never writes to another
// repository.
//
// # Raw bytes
//
// Checkpoints store files exactly as they are on disk. Line-ending conversion
// (core.autocrlf, the text and eol attributes), ident, clean filters and Git
// LFS are not applied, and modes come from the file system (core.fileMode is
// not consulted). Symbolic links are stored as links (mode 120000) and never
// followed; nested repositories are left out. A learner whose own git applies
// conversions may see such files as modified after a Checkpoint.
//
// # The index
//
// Take holds the Topic's index.lock for the whole operation, as git commands
// do, waiting briefly while an editor holds it. It stages into a private copy
// of the index (GIT_INDEX_FILE), so a refusal or failure leaves the learner's
// index untouched. Once the commit object exists, the copy replaces the index
// by an atomic rename and the branch is moved. The learner's index then
// matches the Checkpoint, so `git status` stays clean, and entries whose
// contents did not change keep their cached file stats. Snapshot only reads
// the learner's index.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Errors returned by Take and Snapshot. Refusals leave the Topic unchanged.
var (
	ErrNotRepository        = errors.New("not the top folder of a git repository")
	ErrDetachedHead         = errors.New("HEAD is not on a branch; check out a branch first")
	ErrMergeInProgress      = errors.New("a merge is in progress; finish or abort it first")
	ErrRebaseInProgress     = errors.New("a rebase (or git am) is in progress; finish or abort it first")
	ErrCherryPickInProgress = errors.New("a cherry-pick is in progress; finish or abort it first")
	ErrRevertInProgress     = errors.New("a revert is in progress; finish or abort it first")
	ErrUnmergedPaths        = errors.New("some files have unresolved conflicts; resolve them first")
	ErrNoIdentity           = errors.New(`no git identity; set one with "git config --global user.name" and "git config --global user.email"`)
	ErrIndexLocked          = errors.New("another git process holds the index (index.lock exists)")
	ErrHeadMoved            = errors.New("the branch moved while the checkpoint was being taken")
	ErrInvalidRole          = errors.New(`role must be "agent" or "learner"`)
	ErrInvalidPath          = errors.New("path must name a folder inside the Topic")
)

// Role says whose turn just ended: whose work the Checkpoint saves.
type Role string

// Roles, rendered as a "[agent]" or "[learner]" prefix on the commit message.
const (
	Agent   Role = "agent"
	Learner Role = "learner"
)

// Defaults for Options.
const (
	DefaultLargeFileThreshold int64 = 10 << 20
	DefaultLockTimeout              = 5 * time.Second
)

// Options describe one Checkpoint.
type Options struct {
	Role    Role
	Message string    // summary after the role prefix; "Checkpoint" when empty
	Time    time.Time // author and committer time; now when zero

	// LargeFileThreshold is the size in bytes from which an added or
	// changed file is reported in Result.LargeFiles. Zero means
	// DefaultLargeFileThreshold; a negative value reports nothing.
	LargeFileThreshold int64
	// LockTimeout bounds the wait for another process's index.lock. Zero
	// means DefaultLockTimeout.
	LockTimeout time.Duration
	// DryRun computes the Checkpoint, including the refusals and the large
	// files, without committing or touching the learner's index.
	DryRun bool
}

// Result describes a Checkpoint.
type Result struct {
	// Committed is false when nothing changed since HEAD, so no commit
	// was made. In a dry run it says whether a commit would be made.
	Committed bool
	// Commit is the new commit, or HEAD when skipped ("" before the first
	// commit). In a dry run that would commit, it is "".
	Commit string
	// Tree is the tree of the working tree as it was snapshotted.
	Tree string
	// LargeFiles lists files at or above the threshold that this
	// Checkpoint added or changed, by path.
	LargeFiles []LargeFile
}

// LargeFile is a file a Checkpoint added or changed that is at least as large
// as the threshold.
type LargeFile struct {
	Path string
	Size int64
}

// Take commits the Topic's working tree in dir, which must be the top folder
// of its git repository: every tracked and untracked file that .gitignore
// does not exclude, including deletions. It skips the commit when the tree
// matches HEAD, and refuses during a merge, rebase, cherry-pick or revert, on
// a detached HEAD, or with unresolved conflicts.
func Take(ctx context.Context, dir string, opts Options) (Result, error) {
	if opts.Role != Agent && opts.Role != Learner {
		return Result{}, fmt.Errorf("%w, not %q", ErrInvalidRole, opts.Role)
	}
	when := opts.Time
	if when.IsZero() {
		when = time.Now()
	}
	threshold := opts.LargeFileThreshold
	if threshold == 0 {
		threshold = DefaultLargeFileThreshold
	}
	lockTimeout := opts.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = DefaultLockTimeout
	}

	r, err := open(ctx, dir)
	if err != nil {
		return Result{}, err
	}
	identity, err := r.identity(ctx, when)
	if err != nil {
		return Result{}, err
	}
	unlock, err := lockIndex(ctx, r.gitDir, lockTimeout)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	if err := r.checkState(ctx); err != nil {
		return Result{}, err
	}
	head, headTree, err := r.head(ctx)
	if err != nil {
		return Result{}, err
	}

	tmp, err := os.MkdirTemp(r.gitDir, "lamplight-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	realIndex := filepath.Join(r.gitDir, "index")
	if err := copyIndex(realIndex, index); err != nil {
		return Result{}, err
	}

	tree, states, err := r.stageWorktree(ctx, index)
	if err != nil {
		return Result{}, err
	}
	if tree == headTree {
		return Result{Commit: head, Tree: tree}, nil
	}
	large, err := r.largeFiles(ctx, headTree, tree, states, threshold)
	if err != nil {
		return Result{}, err
	}
	if opts.DryRun {
		return Result{Committed: true, Tree: tree, LargeFiles: large}, nil
	}

	msg := message(opts.Role, opts.Message)
	args := []string{"commit-tree", "--no-gpg-sign"}
	if head != "" {
		args = append(args, "-p", head)
	}
	out, err := r.git(ctx, call{stdin: strings.NewReader(msg), env: identity}, append(args, tree)...)
	if err != nil {
		return Result{}, err
	}
	commit := strings.TrimSpace(out)

	// Still under index.lock. If the branch then fails to move, the index
	// merely holds the working tree, as after `git add -A`.
	if err := os.Rename(index, realIndex); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Result{}, err
	}
	subject, _, _ := strings.Cut(msg, "\n")
	if _, err := r.git(ctx, call{}, "update-ref", "-m", "checkpoint: "+subject, "HEAD", commit, head); err != nil {
		if now, _, headErr := r.head(ctx); headErr == nil && now != head {
			return Result{}, fmt.Errorf("%w: %v", ErrHeadMoved, err)
		}
		return Result{}, err
	}
	return Result{Committed: true, Commit: commit, Tree: tree, LargeFiles: large}, nil
}

// Snapshot returns the tree hash of the folder subpath (such as
// "practice/<lesson-id>") as the working tree holds it now, by the same rules
// as Take: tracked and untracked files that are not ignored, stored as raw
// bytes. It equals that folder's tree in a Checkpoint taken at the same
// moment, and it neither commits nor touches the learner's index. A missing
// folder hashes as the empty tree; "" or "." means the whole Topic.
func Snapshot(ctx context.Context, dir, subpath string) (string, error) {
	sub, err := cleanSubpath(subpath)
	if err != nil {
		return "", err
	}
	r, err := open(ctx, dir)
	if err != nil {
		return "", err
	}
	if exists, err := r.folderExists(sub); err != nil {
		return "", err
	} else if !exists {
		return emptyTrees[r.format], nil
	}

	entries, err := r.listIndex(ctx, "", sub)
	if err != nil {
		return "", err
	}
	untracked, err := r.listUntracked(ctx, "", sub)
	if err != nil {
		return "", err
	}
	relative := func(p string) (string, bool) {
		if sub == "" {
			return p, true
		}
		return strings.CutPrefix(p, sub+"/")
	}

	var info strings.Builder
	var candidates []string
	seen := map[string]bool{}
	for _, e := range entries {
		rel, ok := relative(e.path)
		if !ok || seen[e.path] {
			continue // unmerged paths repeat once per stage
		}
		seen[e.path] = true
		if e.stage == "0" && (e.skipWorktree || e.mode == modeGitlink) {
			fmt.Fprintf(&info, "%s %s\t%s\x00", e.mode, e.oid, rel)
			continue
		}
		candidates = append(candidates, e.path)
	}
	for _, p := range untracked {
		if _, ok := relative(p); ok {
			candidates = append(candidates, p)
		}
	}
	states, err := r.hashWorktree(ctx, candidates)
	if err != nil {
		return "", err
	}
	for _, p := range candidates {
		if st, ok := states[p]; ok {
			rel, _ := relative(p)
			fmt.Fprintf(&info, "%s %s\t%s\x00", st.mode, st.oid, rel)
		}
	}

	tmp, err := os.MkdirTemp(r.gitDir, "lamplight-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	return r.writeTree(ctx, filepath.Join(tmp, "index"), info.String())
}

// stageWorktree updates the index copy at index to match the working tree
// and writes it as a tree. Entries whose mode and blob are unchanged are not
// rewritten, so they keep their cached stats.
func (r *repo) stageWorktree(ctx context.Context, index string) (string, map[string]fileState, error) {
	entries, err := r.listIndex(ctx, index, "")
	if err != nil {
		return "", nil, err
	}
	tracked := make(map[string]indexEntry, len(entries))
	var candidates []string
	for _, e := range entries {
		if e.stage != "0" {
			return "", nil, fmt.Errorf("%w (%s)", ErrUnmergedPaths, e.path)
		}
		// A sparse checkout keeps skip-worktree files out of the working
		// tree, and a submodule's commit is not ours to change.
		if e.skipWorktree || e.mode == modeGitlink {
			continue
		}
		tracked[e.path] = e
		candidates = append(candidates, e.path)
	}
	untracked, err := r.listUntracked(ctx, index, "")
	if err != nil {
		return "", nil, err
	}
	candidates = append(candidates, untracked...)
	states, err := r.hashWorktree(ctx, candidates)
	if err != nil {
		return "", nil, err
	}

	var info strings.Builder
	for _, p := range candidates {
		st, present := states[p]
		e, isTracked := tracked[p]
		switch {
		case present && (!isTracked || st.mode != e.mode || st.oid != e.oid):
			fmt.Fprintf(&info, "%s %s\t%s\x00", st.mode, st.oid, p)
		case !present && isTracked:
			fmt.Fprintf(&info, "0 %s\t%s\x00", r.nullOID(), p)
		}
	}
	tree, err := r.writeTree(ctx, index, info.String())
	return tree, states, err
}

// writeTree applies update-index --index-info lines to an index, then writes
// the index as a tree. update-index only records the given modes and IDs;
// write-tree only reads the index. Neither reads the working tree.
func (r *repo) writeTree(ctx context.Context, index, info string) (string, error) {
	if info != "" {
		if _, err := r.git(ctx, call{index: index, stdin: strings.NewReader(info)},
			"update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	out, err := r.git(ctx, call{index: index}, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// identity reads the learner's user.name and user.email and returns them, with
// the time, as the commit-tree environment.
func (r *repo) identity(ctx context.Context, when time.Time) ([]string, error) {
	name, err := r.learnerConfig(ctx, "user.name", false)
	if err != nil {
		return nil, err
	}
	email, err := r.learnerConfig(ctx, "user.email", false)
	if err != nil {
		return nil, err
	}
	if name == "" || email == "" {
		return nil, ErrNoIdentity
	}
	date := fmt.Sprintf("@%d %s", when.Unix(), when.Format("-0700"))
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
	}, nil
}

// checkState refuses while another git operation is unfinished or HEAD is not
// on a branch.
func (r *repo) checkState(ctx context.Context) error {
	for _, s := range []struct {
		name string
		err  error
	}{
		{"MERGE_HEAD", ErrMergeInProgress},
		{"rebase-merge", ErrRebaseInProgress},
		{"rebase-apply", ErrRebaseInProgress},
		{"CHERRY_PICK_HEAD", ErrCherryPickInProgress},
		{"REVERT_HEAD", ErrRevertInProgress},
	} {
		if _, err := os.Lstat(filepath.Join(r.gitDir, s.name)); err == nil {
			return s.err
		}
	}
	out, err := r.git(ctx, call{readOnly: true}, "symbolic-ref", "-q", "HEAD")
	if exitCode(err) == 1 {
		return ErrDetachedHead
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "refs/heads/") {
		return fmt.Errorf("%w (HEAD points at %s)", ErrDetachedHead, strings.TrimSpace(out))
	}
	return nil
}

// head returns HEAD's commit and tree. Before the first commit, the commit is
// "" and the tree is the empty tree.
func (r *repo) head(ctx context.Context) (commit, tree string, err error) {
	out, err := r.git(ctx, call{readOnly: true}, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if exitCode(err) == 1 {
		return "", emptyTrees[r.format], nil
	}
	if err != nil {
		return "", "", err
	}
	commit = strings.TrimSpace(out)
	out, err = r.git(ctx, call{readOnly: true}, "rev-parse", "-q", "--verify", commit+"^{tree}")
	if err != nil {
		return "", "", err
	}
	return commit, strings.TrimSpace(out), nil
}

// largeFiles lists files added or changed between two trees whose size is at
// least threshold. --raw output needs no blob contents, and the flags rule out
// textconv and external diff drivers in any case.
func (r *repo) largeFiles(ctx context.Context, from, to string, states map[string]fileState, threshold int64) ([]LargeFile, error) {
	if threshold < 0 {
		return nil, nil
	}
	out, err := r.git(ctx, call{readOnly: true}, "diff-tree", "-r", "-z", "--raw",
		"--no-renames", "--no-textconv", "--no-ext-diff", "--diff-filter=AMT", from, to)
	if err != nil {
		return nil, err
	}
	fields := splitNUL(out)
	var large []LargeFile
	for i := 1; i < len(fields); i += 2 {
		st, ok := states[fields[i]]
		if ok && st.mode != modeSymlink && st.size >= threshold {
			large = append(large, LargeFile{Path: fields[i], Size: st.size})
		}
	}
	sort.Slice(large, func(i, j int) bool { return large[i].Path < large[j].Path })
	return large, nil
}

// folderExists reports whether sub is a real folder in the Topic. It is
// false when sub or a parent is missing, and an error when one is a file or
// a symbolic link.
func (r *repo) folderExists(sub string) (bool, error) {
	if sub == "" {
		return true, nil
	}
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return false, err
	}
	defer root.Close()
	parts := strings.Split(sub, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(p)
		if missing(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%w: %s is not a folder", ErrInvalidPath, p)
		}
	}
	return true, nil
}

// cleanSubpath validates a folder path relative to the Topic and returns it
// in git's slash form, or "" for the whole Topic.
func cleanSubpath(p string) (string, error) {
	if p == "" || p == "." {
		return "", nil
	}
	clean := path.Clean(filepath.ToSlash(p))
	first, _, _ := strings.Cut(clean, "/")
	// EqualFold: on a case-insensitive file system .GIT is the same folder.
	if !filepath.IsLocal(clean) || strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, p)
	}
	return clean, nil
}

// message renders the commit message with its role prefix.
func message(role Role, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "Checkpoint"
	}
	return "[" + string(role) + "] " + msg + "\n"
}

// lockIndex takes git's index.lock the way git does (exclusive create),
// retrying with backoff while another process holds it. The returned
// function releases it.
func lockIndex(ctx context.Context, gitDir string, timeout time.Duration) (func(), error) {
	lock := filepath.Join(gitDir, "index.lock")
	deadline := time.Now().Add(timeout)
	wait := 20 * time.Millisecond
	for {
		f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			f.Close()
			return func() { os.Remove(lock) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("%w after waiting %s", ErrIndexLocked, timeout)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		wait = min(wait*2, 200*time.Millisecond)
	}
}

// copyIndex copies the learner's index to dst, keeping its modification time
// so git's check for racily clean entries still works. A missing index is
// left missing; a symbolic link is refused rather than followed.
func copyIndex(src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the index: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
