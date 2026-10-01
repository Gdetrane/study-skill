package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

const (
	completionRecordFile   = "completions.json"
	completionRecordFormat = 1
)

var shells = []string{"bash", "zsh", "fish"}

// systemCompletionRoots are the share folders where packages install
// completion scripts. Tests replace them so results do not depend on the
// machine.
var systemCompletionRoots = []string{"/usr/share", "/usr/local/share"}

// rcMarker ends the line study adds to .zshrc, so uninstall can tell the line
// apart even after the learner edits it.
const rcMarker = "study completion: remove with study completion uninstall"

// beforeRCReplace, when set by a test, runs just before an rc file is
// replaced, to simulate another program editing it at that moment.
var beforeRCReplace func(path string)

// completionRecord is what study completion install did for one shell, so
// uninstall can undo exactly that and nothing else.
type completionRecord struct {
	Shell string `json:"shell"`
	File  string `json:"file,omitempty"`
	// SHA256 is the hash of the script study wrote: uninstall removes the
	// file only while it still has this content.
	SHA256 string `json:"sha256,omitempty"`
	RCFile string `json:"rc_file,omitempty"`
	RCLine string `json:"rc_line,omitempty"`
	// RCNewline is set when install also added a newline to end the rc
	// file's last line.
	RCNewline bool `json:"rc_newline,omitempty"`
	// RCCreated is set when install created the rc file.
	RCCreated bool `json:"rc_created,omitempty"`
	// RCManual is set when study could not edit the rc file safely and asked
	// the learner to add RCLine by hand. study never edits the file then.
	RCManual bool `json:"rc_manual,omitempty"`
}

type completionRecords struct {
	Format    int                `json:"format"`
	Installed []completionRecord `json:"installed"`
}

func (r completionRecords) find(shell string) *completionRecord {
	for i := range r.Installed {
		if r.Installed[i].Shell == shell {
			return &r.Installed[i]
		}
	}
	return nil
}

// owns reports whether path is a script study installed and nobody changed.
func (r completionRecords) owns(path string) bool {
	for _, rec := range r.Installed {
		if rec.File == path && rec.SHA256 != "" && fileSHA256(path) == rec.SHA256 {
			return true
		}
	}
	return false
}

func (r *completionRecords) put(rec completionRecord) {
	kept := []completionRecord{}
	for _, old := range r.Installed {
		if old.Shell != rec.Shell {
			kept = append(kept, old)
		}
	}
	r.Installed = append(kept, rec)
}

// installResult is the --json data of study completion install.
type installResult struct {
	Shell  string `json:"shell"`
	File   string `json:"file"`
	RCFile string `json:"rc_file,omitempty"`
	RCLine string `json:"rc_line,omitempty"`
	// ProvidedBy is a package's completion script. When it is set, study
	// installed nothing.
	ProvidedBy string `json:"provided_by,omitempty"`
	// Manual lists what is left for the learner to do by hand.
	Manual []string `json:"manual"`
	DryRun bool     `json:"dry_run"`
	Note   string   `json:"note"`
}

// uninstallResult is the --json data of study completion uninstall.
type uninstallResult struct {
	Shells []string `json:"shells"`
	// Removed lists the scripts removed; Kept the ones left because they
	// changed since study installed them; AlreadyGone the ones that were
	// no longer there.
	Removed     []string   `json:"removed"`
	Kept        []string   `json:"kept"`
	AlreadyGone []string   `json:"already_gone"`
	RCLines     []rcResult `json:"rc_lines"`
	Manual      []string   `json:"manual"`
	DryRun      bool       `json:"dry_run"`
	Note        string     `json:"note,omitempty"`
}

// rcResult says what happened to a line study added to an rc file.
type rcResult struct {
	File   string `json:"file"`
	Line   string `json:"line"`
	Status string `json:"status"`
}

// Statuses of an rc line in uninstallResult.
const (
	rcRemoved     = "removed"
	rcAlreadyGone = "already_gone"
	rcNotRemoved  = "not_removed"
)

// completionCommands adds install and uninstall to cobra's completion
// command, which keeps generating scripts with study completion <shell>.
func (a *app) completionCommands(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	var completion *cobra.Command
	for _, cmd := range root.Commands() {
		if cmd.Name() == "completion" {
			completion = cmd
		}
	}
	if completion == nil {
		return
	}
	completion.Short = "Install shell completions, or print a completion script"
	completion.Args = noArgs
	completion.RunE = a.groupHelp

	var shell, dir string
	var yes, force, dryRun bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install completions for your shell",
		Long: "Install completions for bash, zsh or fish, for your user only.\n\n" +
			"fish and bash need no changes to your shell configuration. zsh uses a writable folder\n" +
			"already on $fpath when it can find one (pass --dir to name it); otherwise, with your\n" +
			"consent, it adds one line to .zshrc. study completion uninstall undoes it all.\n\n" +
			"study never replaces a completion file it did not write, and installs nothing when a\n" +
			"package already provides completions, unless you pass --force.",
		Example: `  study completion install
  study completion install --shell zsh --dir ~/.zfunc
  study completion install --shell zsh --yes`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := a.installCompletion(cmd.Root(), shell, dir, yes, force, dryRun)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeInstallResult(a.out, res)
		},
	}
	install.Flags().StringVar(&shell, "shell", "", "bash, zsh or fish; detected from $SHELL when omitted")
	install.Flags().StringVar(&dir, "dir", "", "zsh only: a folder on your $fpath to install into")
	install.Flags().BoolVar(&yes, "yes", false, "zsh only: allow adding one line to .zshrc when needed")
	install.Flags().BoolVar(&force, "force", false, "replace a completion file study did not write, even when a package provides completions")
	install.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be written without writing anything")

	var uninstallShell string
	var uninstallDryRun bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the completions that study completion install added",
		Long: "Remove what study completion install added, for every shell or only for --shell.\n" +
			"A script you changed since it was installed is kept, and so is an rc line study can no\n" +
			"longer recognise; study tells you what to remove by hand.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := a.uninstallCompletion(uninstallShell, uninstallDryRun)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeUninstallResult(a.out, res)
		},
	}
	uninstall.Flags().StringVar(&uninstallShell, "shell", "", "bash, zsh or fish; every shell when omitted")
	uninstall.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "show what would be removed without removing anything")

	shellNames := cobra.FixedCompletions(shells, cobra.ShellCompDirectiveNoFileComp)
	_ = install.RegisterFlagCompletionFunc("shell", shellNames)
	_ = uninstall.RegisterFlagCompletionFunc("shell", shellNames)
	_ = install.MarkFlagDirname("dir")
	completion.AddCommand(install, uninstall)
}

// pickShell returns the --shell value, or the shell named by $SHELL.
func (a *app) pickShell(flag string) (string, error) {
	shell := flag
	if shell == "" {
		shell = filepath.Base(a.opts.Getenv("SHELL"))
	}
	for _, s := range shells {
		if shell == s {
			return s, nil
		}
	}
	if flag != "" {
		return "", usageError{fmt.Errorf("--shell must be bash, zsh or fish, not %q", flag)}
	}
	return "", usageError{errors.New("cannot tell your shell from $SHELL: pass --shell bash, zsh or fish")}
}

func (a *app) installCompletion(root *cobra.Command, flagShell, dir string, yes, force, dryRun bool) (installResult, error) {
	shell, err := a.pickShell(flagShell)
	if err != nil {
		return installResult{}, err
	}
	if dir != "" && shell != "zsh" {
		return installResult{}, usageError{errors.New("--dir is only for zsh")}
	}
	var script bytes.Buffer
	switch shell {
	case "bash":
		err = root.GenBashCompletionV2(&script, true)
	case "zsh":
		err = root.GenZshCompletion(&script)
	case "fish":
		err = root.GenFishCompletion(&script, true)
	}
	if err != nil {
		return installResult{}, fmt.Errorf("generating the %s completion script: %w", shell, err)
	}
	records, err := a.readCompletionRecords()
	if err != nil {
		return installResult{}, err
	}
	var prev *completionRecord
	if p := records.find(shell); p != nil {
		saved := *p
		prev = &saved
	}

	rec, err := a.completionTarget(shell, dir)
	if err != nil {
		return installResult{}, err
	}
	res := installResult{Shell: shell, File: rec.File, RCFile: rec.RCFile, RCLine: rec.RCLine, DryRun: dryRun,
		Manual: []string{}, Note: "open a new shell to use them"}
	if shell == "bash" {
		res.Note = "open a new shell to use them; bash needs the bash-completion package"
	}
	if !force {
		if pkg := a.packageCompletion(shell, records); pkg != "" {
			res.File, res.RCFile, res.RCLine = "", "", ""
			res.ProvidedBy = pkg
			res.Note = "a package already provides " + shell + " completions, so study installed nothing; pass --force to install your own"
			return res, nil
		}
	}

	// study replaces only a script that is missing, identical, or its own.
	sum := sha256Hex(script.Bytes())
	state, err := scriptState(rec.File, sum, prev)
	if err != nil {
		return installResult{}, err
	}
	if state == scriptForeign && !force {
		return installResult{}, &core.Error{Code: core.CodeAlreadyExists,
			Message: rec.File + " exists and was not written by study completion install: pass --force to replace it"}
	}

	var rc rcFile
	if rec.RCLine != "" {
		if rc, err = inspectRC(rec.RCFile); err != nil {
			return installResult{}, err
		}
		if rc.reason != "" {
			rec.RCManual = true
			res.Manual = append(res.Manual, fmt.Sprintf("add this line to %s yourself (%s):\n  %s", rec.RCFile, rc.reason, rec.RCLine))
		} else if !dryRun && !yes && !a.confirm(rec) {
			return installResult{}, usageError{fmt.Errorf(
				"no writable folder on zsh's $fpath was found: pass --dir with one, or --yes to let study add one line to %s", rec.RCFile)}
		}
	}
	relocating := prev != nil && (prev.File != rec.File || prev.RCFile != rec.RCFile || prev.RCLine != rec.RCLine)
	if dryRun {
		if relocating && prev.File != "" {
			res.Note += "; the earlier install at " + prev.File + " would be removed"
		}
		return res, nil
	}

	if relocating {
		undone, err := a.undoRecord(*prev, false)
		if err != nil {
			return installResult{}, err
		}
		res.Manual = append(res.Manual, undone.manual...)
		prev = nil
	}

	// Write the script, then the rc line, then the record; undo the earlier
	// steps when a later one fails, so a failed install leaves nothing behind.
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	if state != scriptSame {
		old, readErr := os.ReadFile(rec.File)
		hadOld := readErr == nil
		if err := writeFile(rec.File, script.Bytes()); err != nil {
			return installResult{}, err
		}
		undo = append(undo, func() {
			if hadOld {
				_ = writeFile(rec.File, old)
			} else {
				_ = os.Remove(rec.File)
			}
		})
	}
	if rec.RCLine != "" && !rec.RCManual {
		added, newline, err := addLine(rc, rec.RCLine)
		if err != nil {
			rollback()
			return installResult{}, err
		}
		rec.RCNewline, rec.RCCreated = newline, !rc.exists
		if prev != nil && !added {
			rec.RCNewline, rec.RCCreated = prev.RCNewline, prev.RCCreated
		}
		if added {
			undo = append(undo, func() {
				_, _, _ = removeLine(rec.RCFile, rec.RCLine, rec.File, rec.RCNewline, rec.RCCreated, false)
			})
		}
	}
	rec.SHA256 = sum
	records.put(rec)
	if err := a.writeCompletionRecords(records); err != nil {
		rollback()
		return installResult{}, err
	}
	a.logs.logger.Info("completions installed", "shell", shell, "file", rec.File)
	return res, nil
}

func (a *app) uninstallCompletion(flagShell string, dryRun bool) (uninstallResult, error) {
	res := uninstallResult{Shells: []string{}, Removed: []string{}, Kept: []string{}, AlreadyGone: []string{},
		RCLines: []rcResult{}, Manual: []string{}, DryRun: dryRun}
	if flagShell != "" {
		if _, err := a.pickShell(flagShell); err != nil {
			return res, err
		}
	}
	records, err := a.readCompletionRecords()
	if err != nil {
		return res, err
	}
	remaining := []completionRecord{}
	for _, rec := range records.Installed {
		if flagShell != "" && rec.Shell != flagShell {
			remaining = append(remaining, rec)
			continue
		}
		res.Shells = append(res.Shells, rec.Shell)
		undone, err := a.undoRecord(rec, dryRun)
		if err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, undone.removed...)
		res.Kept = append(res.Kept, undone.kept...)
		res.AlreadyGone = append(res.AlreadyGone, undone.gone...)
		if undone.rc != nil {
			res.RCLines = append(res.RCLines, *undone.rc)
		}
		res.Manual = append(res.Manual, undone.manual...)
		if undone.remaining != nil {
			remaining = append(remaining, *undone.remaining)
		}
	}
	if len(res.Shells) == 0 {
		if flagShell != "" {
			res.Note = "study completion install has not installed " + flagShell + " completions"
		} else {
			res.Note = "study completion install has not installed any completions"
		}
		return res, nil
	}
	if dryRun {
		return res, nil
	}
	records.Installed = remaining
	if err := a.writeCompletionRecords(records); err != nil {
		return res, err
	}
	a.logs.logger.Info("completions uninstalled", "shells", strings.Join(res.Shells, ","))
	return res, nil
}

// undoOutcome is what undoing one install did, and what is left to do.
type undoOutcome struct {
	removed, kept, gone, manual []string
	rc                          *rcResult
	// remaining is the part of the record to keep: an rc line study could
	// not remove, so a later uninstall can try again.
	remaining *completionRecord
}

// undoRecord removes what one install added, as long as it is still what
// study wrote: a changed script is kept, and an rc line is removed only when
// study can recognise it and edit the file safely.
func (a *app) undoRecord(rec completionRecord, dryRun bool) (undoOutcome, error) {
	var out undoOutcome
	if rec.File != "" {
		info, err := os.Lstat(rec.File)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out.gone = append(out.gone, rec.File)
		case err != nil:
			return out, fmt.Errorf("reading %s: %w", rec.File, err)
		case info.Mode().IsRegular() && rec.SHA256 != "" && fileSHA256(rec.File) == rec.SHA256:
			if !dryRun {
				if err := os.Remove(rec.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return out, fmt.Errorf("removing %s: %w", rec.File, err)
				}
			}
			out.removed = append(out.removed, rec.File)
		default:
			out.kept = append(out.kept, rec.File)
			out.manual = append(out.manual, "delete "+rec.File+" yourself if you no longer need it: it changed since study installed it")
		}
	}
	if rec.RCLine == "" {
		return out, nil
	}
	r := rcResult{File: rec.RCFile, Line: rec.RCLine}
	if rec.RCManual {
		r.Status = rcNotRemoved
		out.manual = append(out.manual, fmt.Sprintf("remove this line from %s if you added it:\n  %s", rec.RCFile, rec.RCLine))
	} else {
		status, reason, err := removeLine(rec.RCFile, rec.RCLine, rec.File, rec.RCNewline, rec.RCCreated, dryRun)
		if err != nil {
			return out, err
		}
		r.Status = status
		if status == rcNotRemoved {
			out.manual = append(out.manual, fmt.Sprintf("remove this line from %s by hand (%s):\n  %s", rec.RCFile, reason, rec.RCLine))
			out.remaining = &completionRecord{Shell: rec.Shell, RCFile: rec.RCFile, RCLine: rec.RCLine,
				RCNewline: rec.RCNewline, RCCreated: rec.RCCreated}
		}
	}
	out.rc = &r
	return out, nil
}

// completionTarget decides where a shell's completions go. Only zsh may need
// a line in its rc file: it has no per-user completion folder of its own.
func (a *app) completionTarget(shell, dir string) (completionRecord, error) {
	getenv := a.opts.Getenv
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return completionRecord{}, fmt.Errorf("cannot find your home folder: %w", err)
		}
	}
	xdg := func(key, fallback string) string {
		if v := getenv(key); filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	rec := completionRecord{Shell: shell}
	switch shell {
	case "fish":
		rec.File = filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "fish", "completions", "study.fish")
	case "bash":
		// bash-completion reads BASH_COMPLETION_USER_DIR as a list of
		// folders; the first one is ours.
		base := ""
		for _, d := range filepath.SplitList(getenv("BASH_COMPLETION_USER_DIR")) {
			if filepath.IsAbs(d) {
				base = d
				break
			}
		}
		if base == "" {
			base = filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "bash-completion")
		}
		rec.File = filepath.Join(base, "completions", "study")
	case "zsh":
		if dir != "" {
			abs, err := filepath.Abs(expandHome(dir, home))
			if err != nil {
				return rec, err
			}
			rec.File = filepath.Join(abs, "_study")
			return rec, nil
		}
		if found := zshFpathDir(getenv); found != "" {
			rec.File = filepath.Join(found, "_study")
			return rec, nil
		}
		rec.File = filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "lamplight", "completions", "_study")
		zdotdir := getenv("ZDOTDIR")
		if !filepath.IsAbs(zdotdir) {
			zdotdir = home
		}
		rec.RCFile = filepath.Join(zdotdir, ".zshrc")
		rec.RCLine = fmt.Sprintf("(( $+functions[compdef] )) && source %s  # %s", shellQuote(rec.File), rcMarker)
	}
	return rec, nil
}

// zshFpathDir finds a writable folder that zsh already has on $fpath. A Go
// program cannot read zsh's fpath, so it looks where it can: an exported
// FPATH, Oh My Zsh's completion cache, and Homebrew's site-functions. It
// writes nothing: install creates Oh My Zsh's folder only when it is chosen.
func zshFpathDir(getenv func(string) string) string {
	type candidate struct {
		dir string
		// mayCreate is set for a folder zsh's setup creates and puts on
		// fpath itself; the others must exist already.
		mayCreate bool
	}
	var candidates []candidate
	for _, dir := range filepath.SplitList(getenv("FPATH")) {
		if filepath.IsAbs(dir) {
			candidates = append(candidates, candidate{dir: dir})
		}
	}
	if omz := getenv("ZSH"); filepath.IsAbs(omz) {
		if _, err := os.Stat(filepath.Join(omz, "oh-my-zsh.sh")); err == nil {
			cache := getenv("ZSH_CACHE_DIR")
			if !filepath.IsAbs(cache) {
				cache = filepath.Join(omz, "cache")
			}
			// Oh My Zsh puts this folder on fpath, creating it if needed.
			candidates = append(candidates, candidate{dir: filepath.Join(cache, "completions"), mayCreate: true})
		}
	}
	if brew := getenv("HOMEBREW_PREFIX"); filepath.IsAbs(brew) {
		candidates = append(candidates, candidate{dir: filepath.Join(brew, "share", "zsh", "site-functions")})
	}
	for _, c := range candidates {
		info, err := os.Stat(c.dir)
		exists := err == nil && info.IsDir()
		if (exists || c.mayCreate) && writableDir(c.dir) {
			return c.dir
		}
	}
	return ""
}

// packageCompletionPaths lists where packages put study's completion script
// for shell.
func (a *app) packageCompletionPaths(shell string) []string {
	roots := append([]string{}, systemCompletionRoots...)
	if brew := a.opts.Getenv("HOMEBREW_PREFIX"); filepath.IsAbs(brew) {
		roots = append(roots, filepath.Join(brew, "share"))
	}
	var paths []string
	for _, root := range roots {
		switch shell {
		case "bash":
			paths = append(paths, filepath.Join(root, "bash-completion", "completions", "study"))
		case "zsh":
			paths = append(paths, filepath.Join(root, "zsh", "site-functions", "_study"),
				filepath.Join(root, "zsh", "vendor-completions", "_study"))
		case "fish":
			paths = append(paths, filepath.Join(root, "fish", "vendor_completions.d", "study.fish"))
		}
	}
	return paths
}

// packageCompletion returns a package's completion script for shell, if one
// is installed. A script study installed itself does not count.
func (a *app) packageCompletion(shell string, records completionRecords) string {
	for _, path := range a.packageCompletionPaths(shell) {
		if _, err := os.Stat(path); err == nil && !records.owns(path) {
			return path
		}
	}
	return ""
}

// confirm asks before editing an rc file, only when a person is at the
// terminal. Agents and scripts must pass --yes.
func (a *app) confirm(rec completionRecord) bool {
	f, ok := a.stdin.(*os.File)
	if a.json || !ok || !term.IsTerminal(f.Fd()) {
		return false
	}
	fmt.Fprintf(a.stderr, "No writable folder on zsh's $fpath was found. study can add this line to %s:\n\n  %s\n\nAdd it? [y/N] ",
		rec.RCFile, rec.RCLine)
	answer, _ := bufio.NewReader(f).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// writableDir reports whether files can be created in dir, or in dir once it
// is created, without writing anything.
func writableDir(dir string) bool {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			return info.IsDir() && canWrite(dir)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func expandHome(path, home string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fileSHA256 is the hash of path's content, or "" when it cannot be read.
func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha256Hex(data)
}

// What is at a completion script's target before install.
const (
	scriptAbsent  = "absent"
	scriptSame    = "same"    // already the script study would write
	scriptOurs    = "ours"    // an earlier script of study's, unchanged
	scriptForeign = "foreign" // anything else: never replaced without --force
)

func scriptState(path, sum string, prev *completionRecord) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return scriptAbsent, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return scriptForeign, nil
	}
	switch got := fileSHA256(path); {
	case got == sum:
		return scriptSame, nil
	case prev != nil && prev.File == path && prev.SHA256 != "" && got == prev.SHA256:
		return scriptOurs, nil
	}
	return scriptForeign, nil
}

// writeFile replaces path atomically, creating its folder.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return replaceFile(path, data, 0o644)
}

// replaceFile writes data to a temporary file next to path and renames it
// over path, so readers see the old or the new content, never a mix.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), perm)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	return nil
}

// rcFile is a shell rc file as study found it. study edits the file a
// symlink such as ~/.zshrc points to, so dotfile managers (stow, chezmoi)
// keep their link. It never rewrites a file with other hard links, which
// replacing it would break, nor a read-only one: reason says why not, and the
// learner edits it by hand.
type rcFile struct {
	path   string // as named, such as ~/.zshrc
	target string // the file to edit, with symlinks resolved
	exists bool
	reason string // why study must not edit it; "" when it may
}

func inspectRC(path string) (rcFile, error) {
	rc := rcFile{path: path, target: path}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !writableDir(filepath.Dir(path)) {
			rc.reason = "its folder is not writable"
		}
		return rc, nil
	}
	if err != nil {
		return rc, fmt.Errorf("reading %s: %w", path, err)
	}
	rc.exists = true
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			rc.reason = "it is a symlink to a file that does not exist"
			return rc, nil
		}
		rc.target = target
		if info, err = os.Stat(target); err != nil {
			return rc, fmt.Errorf("reading %s: %w", target, err)
		}
	}
	switch {
	case !info.Mode().IsRegular():
		rc.reason = "it is not a regular file"
	case linkCount(info) > 1:
		rc.reason = "it has other hard links, which rewriting it would break"
	case !canWrite(rc.target):
		rc.reason = "it is read-only"
	case !canWrite(filepath.Dir(rc.target)):
		rc.reason = "its folder is read-only"
	}
	return rc, nil
}

// findLine returns the index of line among lines, ignoring surrounding
// whitespace and a trailing \r, so an editor that changed line endings does
// not hide it.
func findLine(lines []string, line string) int {
	want := strings.TrimSpace(line)
	for i, l := range lines {
		if strings.TrimSpace(l) == want {
			return i
		}
	}
	return -1
}

// addLine appends line to the rc file unless it is already there. It reports
// whether it added the line, and whether it also had to end the file's last
// line with a newline. Appending cannot lose another program's edits; a
// program that replaces the file at the same moment can still drop the line,
// which uninstall then reports as already gone.
func addLine(rc rcFile, line string) (added, newline bool, err error) {
	data, err := os.ReadFile(rc.target)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, false, fmt.Errorf("reading %s: %w", rc.path, err)
	}
	if findLine(strings.SplitAfter(string(data), "\n"), line) >= 0 {
		return false, false, nil
	}
	newline = len(data) > 0 && !bytes.HasSuffix(data, []byte("\n"))
	var add string
	if newline {
		add = "\n"
	}
	add += line + "\n"
	f, err := os.OpenFile(rc.target, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return false, false, fmt.Errorf("opening %s: %w", rc.path, err)
	}
	_, werr := f.WriteString(add)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, false, fmt.Errorf("writing %s: %w", rc.path, werr)
	}
	return true, newline, nil
}

// removeLine removes the line addLine added, and the newline it added before
// it, keeping everything else in the file as it is. It reports rcRemoved,
// rcAlreadyGone when no line mentions the script any more, or rcNotRemoved
// with the reason when the line was changed or the file must not be edited.
//
// The file is replaced atomically. If another program changes it while study
// is working, study starts again from the new content, and gives up with busy
// after a few tries. A change in the instant between the last check and the
// rename can still be lost; the window is a few microseconds.
func removeLine(path, line, script string, newline, created, dryRun bool) (status, reason string, err error) {
	for range 3 {
		rc, err := inspectRC(path)
		if err != nil {
			return "", "", err
		}
		if !rc.exists {
			return rcAlreadyGone, "", nil
		}
		before, err := os.Stat(rc.target)
		if errors.Is(err, fs.ErrNotExist) {
			return rcAlreadyGone, "", nil
		}
		if err != nil {
			return "", "", fmt.Errorf("reading %s: %w", path, err)
		}
		data, err := os.ReadFile(rc.target)
		if err != nil {
			return "", "", fmt.Errorf("reading %s: %w", path, err)
		}
		lines := strings.SplitAfter(string(data), "\n")
		i := findLine(lines, line)
		if i < 0 {
			if strings.Contains(string(data), rcMarker) || script != "" && strings.Contains(string(data), script) {
				return rcNotRemoved, "the line was changed since study added it", nil
			}
			return rcAlreadyGone, "", nil
		}
		if rc.reason != "" {
			return rcNotRemoved, rc.reason, nil
		}
		if dryRun {
			return rcRemoved, "", nil
		}
		last := true
		for _, l := range lines[i+1:] {
			if l != "" {
				last = false
			}
		}
		content := strings.Join(lines[:i], "") + strings.Join(lines[i+1:], "")
		if newline && last {
			// Restore the missing newline at the end of the learner's last line.
			content = strings.TrimSuffix(strings.TrimSuffix(content, "\n"), "\r")
		}
		if beforeRCReplace != nil {
			beforeRCReplace(rc.target)
		}
		if now, err := os.Stat(rc.target); err != nil || now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) {
			continue
		}
		if created && content == "" {
			if err := os.Remove(rc.target); err != nil {
				return "", "", fmt.Errorf("removing %s: %w", path, err)
			}
			return rcRemoved, "", nil
		}
		if err := replaceFile(rc.target, []byte(content), before.Mode().Perm()); err != nil {
			return "", "", err
		}
		return rcRemoved, "", nil
	}
	return "", "", &core.Error{Code: core.CodeBusy, Message: path + " kept changing while study was editing it: try again"}
}

func (a *app) completionRecordPath() (string, error) {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, completionRecordFile), nil
}

func (a *app) readCompletionRecords() (completionRecords, error) {
	records := completionRecords{Format: completionRecordFormat, Installed: []completionRecord{}}
	path, err := a.completionRecordPath()
	if err != nil {
		return records, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return records, fmt.Errorf("reading %s: %w", path, err)
	}
	damaged := func(why string) error {
		return &core.Error{Code: core.CodeCorrupt, Message: fmt.Sprintf(
			"%s is damaged (%s): delete it, then reinstall completions; study cannot undo earlier installs without it", path, why)}
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return records, damaged(err.Error())
	}
	switch {
	case records.Format > completionRecordFormat:
		return records, &core.Error{Code: core.CodeNewerFormat, Message: fmt.Sprintf(
			"%s has format %d, but this version of study only understands format %d: upgrade study",
			path, records.Format, completionRecordFormat)}
	case records.Format < 1:
		return records, damaged("it has no format")
	}
	if records.Installed == nil {
		records.Installed = []completionRecord{}
	}
	return records, nil
}

func (a *app) writeCompletionRecords(records completionRecords) error {
	path, err := a.completionRecordPath()
	if err != nil {
		return err
	}
	if len(records.Installed) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	records.Format = completionRecordFormat
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

// diagnoseCompletion is study doctor's completion Finding.
func (a *app) diagnoseCompletion() core.Finding {
	f := core.Finding{Name: "completion"}
	shell, err := a.pickShell("")
	if err != nil {
		f.Status, f.Message = core.FindingOK, "skipped: $SHELL is not bash, zsh or fish"
		return f
	}
	records, err := a.readCompletionRecords()
	if err != nil {
		f.Status, f.Message = core.FindingWarn, err.Error()
		if core.CodeOf(err) == core.CodeNewerFormat {
			f.Fix = "upgrade study"
		} else if path, perr := a.completionRecordPath(); perr == nil {
			f.Fix = "delete " + path + ", then run study completion install"
		}
		return f
	}
	if rec := records.find(shell); rec != nil && rec.File != "" {
		switch _, err := os.Stat(rec.File); {
		case err != nil:
			f.Status, f.Message = core.FindingWarn, "study completion install wrote "+rec.File+", but it is gone"
			f.Fix = "study completion install"
		case fileSHA256(rec.File) != rec.SHA256:
			f.Status, f.Message = core.FindingOK, shell+" completions are installed at "+rec.File+", changed since study wrote them"
		default:
			f.Status, f.Message = core.FindingOK, shell+" completions are installed at "+rec.File
		}
		return f
	}
	if pkg := a.packageCompletion(shell, records); pkg != "" {
		f.Status, f.Message = core.FindingOK, shell+" completions are provided by a package at "+pkg
		return f
	}
	if rec, err := a.completionTarget(shell, ""); err == nil && rec.RCLine == "" {
		if _, err := os.Stat(rec.File); err == nil {
			f.Status, f.Message = core.FindingOK, shell+" completions found at "+rec.File+", not installed by study"
			return f
		}
	}
	f.Status, f.Message, f.Fix = core.FindingWarn, shell+" completions are not installed", "study completion install"
	return f
}
