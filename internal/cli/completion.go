package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

const completionRecordFile = "completions.json"

var shells = []string{"bash", "zsh", "fish"}

// completionRecord is what study completion install did for one shell, so
// uninstall can undo exactly that.
type completionRecord struct {
	Shell  string `json:"shell"`
	File   string `json:"file"`
	RCFile string `json:"rc_file,omitempty"`
	RCLine string `json:"rc_line,omitempty"`
	// RCNewline is set when install also added a newline to end the rc
	// file's last line.
	RCNewline bool `json:"rc_newline,omitempty"`
}

type completionRecords struct {
	Format    int                `json:"format"`
	Installed []completionRecord `json:"installed"`
}

// installResult is the --json data of study completion install.
type installResult struct {
	Shell  string `json:"shell"`
	File   string `json:"file"`
	RCFile string `json:"rc_file,omitempty"`
	RCLine string `json:"rc_line,omitempty"`
	DryRun bool   `json:"dry_run"`
	Note   string `json:"note"`
}

// uninstallResult is the --json data of study completion uninstall.
type uninstallResult struct {
	Shell   string   `json:"shell"`
	Removed []string `json:"removed"`
	RCFile  string   `json:"rc_file,omitempty"`
	RCLine  string   `json:"rc_line,omitempty"`
	DryRun  bool     `json:"dry_run"`
	Note    string   `json:"note,omitempty"`
}

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
	completion.RunE = showHelp

	var shell, dir string
	var yes, dryRun bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install completions for your shell",
		Long: "Install completions for bash, zsh or fish, for your user only.\n\n" +
			"fish and bash need no changes to your shell configuration. zsh uses a writable folder\n" +
			"already on $fpath when it can find one (pass --dir to name it); otherwise, with your\n" +
			"consent, it adds one line to .zshrc. study completion uninstall undoes it all.",
		Example: `  study completion install
  study completion install --shell zsh --dir ~/.zfunc
  study completion install --shell zsh --yes`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := a.installCompletion(cmd.Root(), shell, dir, yes, dryRun)
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
	install.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be written without writing anything")

	var uninstallShell string
	var uninstallDryRun bool
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the completions that study completion install added",
		Args:  noArgs,
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
	uninstall.Flags().StringVar(&uninstallShell, "shell", "", "bash, zsh or fish; detected from $SHELL when omitted")
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

func (a *app) installCompletion(root *cobra.Command, flagShell, dir string, yes, dryRun bool) (installResult, error) {
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
	rec, err := a.completionTarget(shell, dir)
	if err != nil {
		return installResult{}, err
	}
	res := installResult{Shell: shell, File: rec.File, RCFile: rec.RCFile, RCLine: rec.RCLine, DryRun: dryRun,
		Note: "open a new shell to use them"}
	if shell == "bash" {
		res.Note = "open a new shell to use them; bash needs the bash-completion package"
	}
	if rec.RCLine != "" && !dryRun && !yes && !a.confirm(rec) {
		return installResult{}, usageError{fmt.Errorf(
			"no writable folder on zsh's $fpath was found: pass --dir with one, or --yes to let study add one line to %s", rec.RCFile)}
	}
	if dryRun {
		return res, nil
	}

	records, err := a.readCompletionRecords()
	if err != nil {
		return installResult{}, err
	}
	kept := records.Installed[:0]
	for _, r := range records.Installed {
		if r.Shell != shell {
			kept = append(kept, r)
		} else if r.RCFile == rec.RCFile && r.RCLine == rec.RCLine {
			// A reinstall: remember what the first install added.
			rec.RCNewline = r.RCNewline
		}
	}
	if err := writeFile(rec.File, script.Bytes()); err != nil {
		return installResult{}, err
	}
	if rec.RCLine != "" {
		added, err := addLine(rec.RCFile, rec.RCLine)
		if err != nil {
			return installResult{}, err
		}
		rec.RCNewline = rec.RCNewline || added
	}
	records.Installed = append(kept, rec)
	if err := a.writeCompletionRecords(records); err != nil {
		return installResult{}, err
	}
	a.logs.logger.Info("completions installed", "shell", shell, "file", rec.File)
	return res, nil
}

func (a *app) uninstallCompletion(flagShell string, dryRun bool) (uninstallResult, error) {
	shell, err := a.pickShell(flagShell)
	if err != nil {
		return uninstallResult{}, err
	}
	res := uninstallResult{Shell: shell, DryRun: dryRun, Removed: []string{}}
	records, err := a.readCompletionRecords()
	if err != nil {
		return res, err
	}
	var rec *completionRecord
	kept := []completionRecord{}
	for i := range records.Installed {
		if records.Installed[i].Shell == shell {
			rec = &records.Installed[i]
		} else {
			kept = append(kept, records.Installed[i])
		}
	}
	if rec == nil {
		res.Note = "study completion install has not installed " + shell + " completions"
		return res, nil
	}
	res.RCFile, res.RCLine = rec.RCFile, rec.RCLine
	if _, err := os.Lstat(rec.File); err == nil {
		res.Removed = append(res.Removed, rec.File)
	}
	if dryRun {
		return res, nil
	}
	if err := os.Remove(rec.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("removing %s: %w", rec.File, err)
	}
	if rec.RCLine != "" {
		if err := removeLine(rec.RCFile, rec.RCLine, rec.RCNewline); err != nil {
			return res, err
		}
	}
	records.Installed = kept
	if err := a.writeCompletionRecords(records); err != nil {
		return res, err
	}
	a.logs.logger.Info("completions uninstalled", "shell", shell, "file", rec.File)
	return res, nil
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
		base := getenv("BASH_COMPLETION_USER_DIR")
		if !filepath.IsAbs(base) {
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
		rec.RCLine = fmt.Sprintf("(( $+functions[compdef] )) && source %s  # study completion: remove with study completion uninstall",
			shellQuote(rec.File))
	}
	return rec, nil
}

// zshFpathDir finds a writable folder that zsh already has on $fpath. A Go
// program cannot read zsh's fpath, so it looks where it can: an exported
// FPATH, Oh My Zsh's completion cache, and Homebrew's site-functions.
func zshFpathDir(getenv func(string) string) string {
	var candidates []string
	for _, dir := range filepath.SplitList(getenv("FPATH")) {
		if filepath.IsAbs(dir) {
			candidates = append(candidates, dir)
		}
	}
	if omz := getenv("ZSH"); filepath.IsAbs(omz) {
		if _, err := os.Stat(filepath.Join(omz, "oh-my-zsh.sh")); err == nil {
			cache := getenv("ZSH_CACHE_DIR")
			if !filepath.IsAbs(cache) {
				cache = filepath.Join(omz, "cache")
			}
			// Oh My Zsh puts this folder on fpath, creating it if needed.
			if err := os.MkdirAll(filepath.Join(cache, "completions"), 0o755); err == nil {
				candidates = append(candidates, filepath.Join(cache, "completions"))
			}
		}
	}
	if brew := getenv("HOMEBREW_PREFIX"); filepath.IsAbs(brew) {
		candidates = append(candidates, filepath.Join(brew, "share", "zsh", "site-functions"))
	}
	for _, dir := range candidates {
		if writableDir(dir) {
			return dir
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

func writableDir(dir string) bool {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	probe, err := os.CreateTemp(dir, ".study-probe-*")
	if err != nil {
		return false
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return true
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

// writeFile replaces path atomically, creating its folder.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
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

// addLine appends line to the file unless it is already there. It reports
// whether it also had to end the file's last line with a newline.
func addLine(path, line string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l == line {
			return false, nil
		}
	}
	newline := len(data) > 0 && !bytes.HasSuffix(data, []byte("\n"))
	var add string
	if newline {
		add = "\n"
	}
	add += line + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return false, fmt.Errorf("opening %s: %w", path, err)
	}
	_, werr := f.WriteString(add)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("writing %s: %w", path, werr)
	}
	return newline, nil
}

// removeLine removes the line that addLine added, and the newline it added
// before it, keeping everything else in the file as it is.
func removeLine(path, line string, newline bool) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	content := string(data)
	switch {
	case newline && strings.HasSuffix(content, "\n"+line+"\n"):
		content = strings.TrimSuffix(content, "\n"+line+"\n")
	case strings.HasPrefix(content, line+"\n"):
		content = strings.TrimPrefix(content, line+"\n")
	case strings.Contains(content, "\n"+line+"\n"):
		content = strings.Replace(content, "\n"+line+"\n", "\n", 1)
	default:
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := writeFile(path, []byte(content)); err != nil {
		return err
	}
	return os.Chmod(path, info.Mode().Perm())
}

func (a *app) completionRecordPath() (string, error) {
	dir, err := stateDir(a.opts.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, completionRecordFile), nil
}

func (a *app) readCompletionRecords() (completionRecords, error) {
	records := completionRecords{Format: 1, Installed: []completionRecord{}}
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
	if err := json.Unmarshal(data, &records); err != nil {
		return records, fmt.Errorf("%s is damaged: %w", path, err)
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
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

// installedCompletion reports where completions for shell are installed:
// by study completion install, or by a package.
func (a *app) installedCompletion(shell string) string {
	if records, err := a.readCompletionRecords(); err == nil {
		for _, r := range records.Installed {
			if r.Shell == shell {
				if _, err := os.Stat(r.File); err == nil {
					return r.File
				}
			}
		}
	}
	var system []string
	switch shell {
	case "bash":
		system = []string{"/usr/share/bash-completion/completions/study", "/usr/local/share/bash-completion/completions/study"}
	case "zsh":
		system = []string{"/usr/share/zsh/site-functions/_study", "/usr/local/share/zsh/site-functions/_study"}
		if brew := a.opts.Getenv("HOMEBREW_PREFIX"); filepath.IsAbs(brew) {
			system = append(system, filepath.Join(brew, "share", "zsh", "site-functions", "_study"))
		}
	case "fish":
		system = []string{"/usr/share/fish/vendor_completions.d/study.fish", "/usr/local/share/fish/vendor_completions.d/study.fish"}
	}
	if rec, err := a.completionTarget(shell, ""); err == nil && rec.RCLine == "" {
		system = append([]string{rec.File}, system...)
	}
	for _, path := range system {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
