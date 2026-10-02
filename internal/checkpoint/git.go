package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// hookEvents lists every hook event in githooks(5) as of git 2.55. Each one is
// switched off with hook.<event>.enabled=false, which stops both the hooks
// directory and configuration-defined hooks (hook.<name>.command) for that
// event. core.hooksPath=/dev/null alone does not stop the latter: with git
// 2.55, update-index fires post-index-change and update-ref fires
// reference-transaction for hooks defined in .git/config.
var hookEvents = []string{
	"applypatch-msg", "commit-msg", "fsmonitor-watchman", "p4-changelist",
	"p4-post-changelist", "p4-pre-submit", "p4-prepare-changelist",
	"post-applypatch", "post-checkout", "post-commit", "post-index-change",
	"post-merge", "post-receive", "post-rewrite", "post-update",
	"pre-applypatch", "pre-auto-gc", "pre-commit", "pre-merge-commit",
	"pre-push", "pre-rebase", "pre-receive", "prepare-commit-msg",
	"proc-receive", "push-to-checkout", "reference-transaction",
	"sendemail-validate", "update",
}

// emptyTrees holds the ID of the empty tree for each object format.
var emptyTrees = map[string]string{
	"sha1":   "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
	"sha256": "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321",
}

// repo runs git against one Topic. Every invocation goes through git, which
// applies the same hardening to all of them.
//
// The Topic folder and its .git are pinned when the repo is opened: root and
// gitRoot hold them open, so every file the core reads or writes itself (the
// index, index.lock, the temporary index) stays in the folders that were
// checked, even if a name is swapped for a symbolic link afterwards. On Linux
// git is pinned too: it inherits both folders as descriptors and gets
// GIT_DIR=/proc/self/fd/3, which git keeps as the repository path when it runs
// outside the work tree, so its writes follow the pinned folder rather than
// the name. verify re-checks the names before every step in which git writes.
type repo struct {
	dir       string      // the Topic's working tree, absolute
	gitDir    string      // dir/.git, always a real directory
	format    string      // object format: sha1 or sha256
	overrides [][2]string // configuration forced at command scope

	root    *os.Root    // the Topic folder, pinned
	gitRoot *os.Root    // its .git folder, pinned
	dirInfo fs.FileInfo // identity of the pinned Topic folder
	gitInfo fs.FileInfo // identity of the pinned .git
	// pins are .git and the Topic folder as open files, inherited by git as
	// descriptors 3 and 4 where /proc/self/fd can name them (Linux). Empty
	// elsewhere, where git gets the names.
	pins []*os.File
}

// call describes one git invocation.
type call struct {
	readOnly bool       // sets GIT_OPTIONAL_LOCKS=0
	index    string     // GIT_INDEX_FILE; empty means the Topic's own index
	stdin    io.Reader  // nil reads from the null device, never our stdin
	env      []string   // extra environment, such as the commit identity
	files    []*os.File // inherited after the pins, from descriptor fdBase
	// learnerScopes keeps the learner's global and system configuration
	// visible. Only plain `git config --get` reads use it.
	learnerScopes bool
}

// gitError is a failed git invocation with its standard error.
type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	msg := fmt.Sprintf("git %s: %v", e.args[0], e.err)
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

func (e *gitError) Unwrap() error { return e.err }

// exitCode returns git's exit status, or -1 when err is not an exit.
func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// canPin reports whether git can be handed the pinned folders as
// /proc/self/fd paths.
var canPin = sync.OnceValue(func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	info, err := os.Stat("/proc/self/fd")
	return err == nil && info.IsDir()
})

// open checks that dir is the top folder of a git repository whose metadata
// lives inside it, pins both folders and prepares the hardened configuration.
// The caller closes the repo.
func open(ctx context.Context, dir string) (_ *repo, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// A symbolic link in place of the Topic folder or its .git, or a .git
	// file, could point the core's writes at another repository of the
	// learner's, outside the agent's sandbox.
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a folder", ErrNotRepository, abs)
	}
	r := &repo{dir: abs, gitDir: filepath.Join(abs, ".git"), overrides: baseOverrides()}
	defer func() {
		if err != nil {
			r.close()
		}
	}()
	if r.root, err = os.OpenRoot(abs); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	if r.dirInfo, err = r.root.Stat("."); err != nil {
		return nil, err
	}
	if !os.SameFile(info, r.dirInfo) {
		return nil, fmt.Errorf("%w: %s was replaced while it was being opened", ErrRepositoryChanged, abs)
	}
	gitInfo, err := r.root.Lstat(".git")
	if err != nil {
		return nil, fmt.Errorf("%w: no .git folder in %s", ErrNotRepository, abs)
	}
	if !gitInfo.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a folder", ErrNotRepository, r.gitDir)
	}
	if r.gitRoot, err = r.root.OpenRoot(".git"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	if r.gitInfo, err = r.gitRoot.Stat("."); err != nil {
		return nil, err
	}
	if !os.SameFile(gitInfo, r.gitInfo) {
		return nil, fmt.Errorf("%w: %s was replaced while it was being opened", ErrRepositoryChanged, r.gitDir)
	}
	if canPin() {
		for _, root := range []*os.Root{r.gitRoot, r.root} {
			f, err := root.Open(".")
			if err != nil {
				return nil, err
			}
			r.pins = append(r.pins, f)
		}
	}
	if err := r.verify(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotRepository, err)
	}

	out, err := r.git(ctx, call{readOnly: true}, "rev-parse",
		"--show-object-format", "--path-format=absolute", "--git-common-dir")
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%w: %v", ErrGitNotFound, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		return nil, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	r.format = lines[0]
	if _, ok := emptyTrees[r.format]; !ok {
		return nil, fmt.Errorf("unsupported object format %q", r.format)
	}
	// A commondir file would send refs and objects to another repository.
	if common, err := os.Stat(lines[1]); err != nil || !os.SameFile(common, r.gitInfo) {
		return nil, fmt.Errorf("%w: %s shares refs with %s", ErrNotRepository, r.gitDir, lines[1])
	}

	names, err := r.configuredHooks(ctx)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		r.overrides = append(r.overrides, [2]string{"hook." + name + ".enabled", "false"})
	}

	// The learner's own ignore file, usually set in their global
	// configuration, which every other call below leaves out.
	excludes, err := r.learnerConfig(ctx, "core.excludesFile", true)
	if err != nil {
		return nil, err
	}
	if excludes != "" {
		r.overrides = append(r.overrides, [2]string{"core.excludesFile", excludes})
	}
	return r, nil
}

// close releases the pinned folders.
func (r *repo) close() {
	for _, f := range r.pins {
		f.Close()
	}
	if r.gitRoot != nil {
		r.gitRoot.Close()
	}
	if r.root != nil {
		r.root.Close()
	}
}

// verify checks that the Topic folder and its .git are still the pinned
// folders under their names, and that nothing in .git that git writes
// through by name leads elsewhere: a commondir file, or a symbolic link for
// HEAD, the refs, logs, objects or reftable folders, or for an entry in
// objects or reftable. A
// sandboxed agent can edit the Topic while a Checkpoint waits for the index
// lock, so Take runs it again before every step in which git writes.
func (r *repo) verify() error {
	changed := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrRepositoryChanged}, args...)...)
	}
	if info, err := os.Lstat(r.dir); err != nil || !info.IsDir() || !os.SameFile(info, r.dirInfo) {
		return changed("%s is no longer the Topic folder that was opened", r.dir)
	}
	if info, err := os.Lstat(r.gitDir); err != nil || !info.IsDir() || !os.SameFile(info, r.gitInfo) {
		return changed("%s is no longer the .git folder that was opened", r.gitDir)
	}
	if _, err := r.gitRoot.Lstat("commondir"); !missing(err) {
		return changed(".git/commondir would send refs and objects to another repository")
	}
	if info, err := r.gitRoot.Lstat("HEAD"); err == nil && !info.Mode().IsRegular() {
		return changed(".git/HEAD is not a regular file")
	}
	// A reftable repository keeps its refs in .git/reftable and leaves
	// .git/refs/heads as a stub file, so that stub is accepted only when
	// .git/reftable is a real folder.
	reftable := false
	if info, err := r.gitRoot.Lstat("reftable"); err == nil {
		if !info.IsDir() {
			return changed(".git/reftable is not a folder")
		}
		reftable = true
	}
	for _, name := range []string{"refs", "refs/heads", "logs", "logs/refs", "logs/refs/heads", "objects"} {
		info, err := r.gitRoot.Lstat(name)
		if err != nil || info.IsDir() {
			continue
		}
		if reftable && name == "refs/heads" && info.Mode().IsRegular() {
			continue
		}
		return changed(".git/%s is not a folder", name)
	}
	for _, dir := range []string{"objects", "reftable"} {
		entries, err := fs.ReadDir(r.gitRoot.FS(), dir)
		if err != nil && !missing(err) {
			return err
		}
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink != 0 {
				return changed(".git/%s/%s is a symbolic link", dir, e.Name())
			}
		}
	}
	return nil
}

// verifyBranch checks, through the pinned .git, that nothing on the way to
// the branch's loose ref or its reflog is a symbolic link, so update-ref
// writes inside this repository.
func (r *repo) verifyBranch(branch string) error {
	for _, base := range []string{"", "logs/"} {
		parts := strings.Split(base+branch, "/")
		for i := 1; i <= len(parts); i++ {
			name := strings.Join(parts[:i], "/")
			info, err := r.gitRoot.Lstat(name)
			if missing(err) {
				break
			}
			if err != nil {
				return err
			}
			if info.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("%w: .git/%s is a symbolic link", ErrRepositoryChanged, name)
			}
		}
	}
	return nil
}

// gitDirArg and workTreeArg are the paths git is given for the repository and
// the work tree: the pinned descriptors where possible, else the names.
func (r *repo) gitDirArg() string {
	if len(r.pins) > 0 {
		return "/proc/self/fd/3"
	}
	return r.gitDir
}

func (r *repo) workTreeArg() string {
	if len(r.pins) > 0 {
		return "/proc/self/fd/4"
	}
	return r.dir
}

// fdBase is the descriptor number git sees for call.files[0].
func (r *repo) fdBase() int { return 3 + len(r.pins) }

// baseOverrides is the configuration forced on every call. It is passed at
// command scope (GIT_CONFIG_COUNT, the same scope as -c), which takes
// precedence over the repository's own configuration.
func baseOverrides() [][2]string {
	o := [][2]string{
		{"core.hooksPath", "/dev/null"},
		{"core.fsmonitor", "false"},
		{"commit.gpgSign", "false"},
		// No transport may run, so a promisor remote cannot start ssh or a
		// remote helper to fetch a missing object.
		{"protocol.allow", "never"},
	}
	for _, event := range hookEvents {
		o = append(o, [2]string{"hook." + event + ".enabled", "false"})
	}
	return o
}

// configuredHooks returns the names of hooks defined in configuration as
// hook.<name>.command, so that each can be switched off by name too. This
// covers events that hookEvents does not list.
func (r *repo) configuredHooks(ctx context.Context) ([]string, error) {
	out, err := r.git(ctx, call{readOnly: true}, "config", "-z", "--get-regexp", `^hook\..*\.command$`)
	if exitCode(err) == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, record := range strings.Split(out, "\x00") {
		key, _, _ := strings.Cut(record, "\n")
		name, ok := strings.CutPrefix(key, "hook.")
		if !ok {
			continue
		}
		if name, ok = strings.CutSuffix(name, ".command"); ok && name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// learnerConfig reads one value from the learner's git configuration,
// including the global and system files. `git config --get` only reads files;
// it never starts a program the configuration names. It returns "" when the
// key is not set.
func (r *repo) learnerConfig(ctx context.Context, key string, isPath bool) (string, error) {
	args := []string{"config"}
	if isPath {
		args = append(args, "--type=path")
	}
	out, err := r.git(ctx, call{readOnly: true, learnerScopes: true}, append(args, "--get", key)...)
	if exitCode(err) == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// git runs one git command in the Topic and returns its standard output.
//
// It runs from /, outside the work tree: git then keeps GIT_DIR exactly as
// given instead of resolving it to a path, which is what keeps a pinned
// /proc/self/fd/3 pinned. Commands that need the work tree change into it
// themselves.
func (r *repo) git(ctx context.Context, c call, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = "/"
	cmd.Env = r.environ(c)
	cmd.Stdin = c.stdin
	cmd.ExtraFiles = append(append([]*os.File{}, r.pins...), c.files...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &gitError{args: args, stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.String(), nil
}

// environ builds a child environment from ours, without any inherited GIT_*
// variable (GIT_DIR, GIT_INDEX_FILE, GIT_EXTERNAL_DIFF, GIT_SSH_COMMAND,
// GIT_CONFIG_PARAMETERS and so on), then adds the hardening.
func (r *repo) environ(c call) []string {
	env := make([]string, 0, len(os.Environ())+len(r.overrides)*2+16)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || name == "LC_ALL" || name == "LANGUAGE" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"LC_ALL=C",
		"GIT_DIR="+r.gitDirArg(),
		// Overrides core.worktree, which could otherwise point the file
		// listing at a folder outside the Topic.
		"GIT_WORK_TREE="+r.workTreeArg(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		// Replace refs could make HEAD^{tree} name another tree.
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_LITERAL_PATHSPECS=1",
	)
	if !c.learnerScopes {
		env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	}
	if c.readOnly {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	if c.index != "" {
		env = append(env, "GIT_INDEX_FILE="+c.index)
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(r.overrides)))
	for i, kv := range r.overrides {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return append(env, c.env...)
}

// nullOID is the all-zero object ID, which update-index reads as "remove".
func (r *repo) nullOID() string {
	return strings.Repeat("0", len(emptyTrees[r.format]))
}
