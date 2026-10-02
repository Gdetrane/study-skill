package cli_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// zshFallback is where zsh completions go when no folder on $fpath is
// writable; .zshrc then sources them.
func zshFallback(home string) string {
	return filepath.Join(home, ".local", "share", "lamplight", "completions", "_study")
}

// shown is path as runEnv's output shows it, with $HOME for home.
func shown(home, path string) string { return "$HOME" + strings.TrimPrefix(path, home) }

func completionRecord(home string) string {
	return filepath.Join(home, ".local", "state", "lamplight", "completions.json")
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// decodeData returns the data of a successful --json result.
func decodeData(t *testing.T, r result) map[string]any {
	t.Helper()
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || !env.OK {
		t.Fatalf("not a successful JSON result (exit %d): %v\n%s%s", r.code, err, r.stdout, r.stderr)
	}
	return env.Data
}

func errorCode(t *testing.T, r result) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatalf("not a JSON result: %v\n%s", err, r.stdout)
	}
	return env.Error.Code
}

func stringList(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// tree lists every path under root, so a test can prove nothing was written.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// TestCompletionKeepsASymlinkedZshrc covers dotfile managers such as stow and
// chezmoi: study edits the file .zshrc points to and leaves the link alone.
func TestCompletionKeepsASymlinkedZshrc(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	dotfile := filepath.Join(home, "dotfiles", "zshrc")
	original := "export EDITOR=vi\n"
	writeFile(t, dotfile, original)
	zshrc := filepath.Join(home, ".zshrc")
	if err := os.Symlink(dotfile, zshrc); err != nil {
		t.Fatal(err)
	}

	if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
		t.Fatalf("install: exit %d, %s", r.code, r.stderr)
	}
	if !strings.Contains(readText(t, dotfile), "&& source ") {
		t.Errorf("the linked file did not get the line:\n%s", readText(t, dotfile))
	}
	if r := runEnv(t, env, home, nil, "completion", "uninstall"); r.code != cli.ExitOK {
		t.Fatalf("uninstall: exit %d, %s", r.code, r.stderr)
	}
	if info, err := os.Lstat(zshrc); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf(".zshrc is no longer a symlink (%v)", err)
	}
	if got := readText(t, dotfile); got != original {
		t.Errorf("the linked file was not restored: %q", got)
	}
}

// TestCompletionNeverRewritesAHardLinkedZshrc: replacing a hard-linked file
// would break the link, so study asks the learner to edit it by hand.
func TestCompletionNeverRewritesAHardLinkedZshrc(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	dotfile := filepath.Join(home, "dotfiles", "zshrc")
	original := "export EDITOR=vi\n"
	writeFile(t, dotfile, original)
	zshrc := filepath.Join(home, ".zshrc")
	if err := os.Link(dotfile, zshrc); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	install := decodeData(t, runEnv(t, env, home, nil, "completion", "install", "--yes", "--json"))
	manual := stringList(install["manual"])
	if len(manual) != 1 || !strings.Contains(manual[0], "hard links") || !strings.Contains(manual[0], "&& source ") {
		t.Errorf("manual = %q, want the line to add by hand", manual)
	}
	if got := readText(t, zshrc); got != original {
		t.Errorf("study edited a hard-linked .zshrc: %q", got)
	}
	if !exists(zshFallback(home)) {
		t.Error("the script was not installed")
	}

	uninstall := decodeData(t, runEnv(t, env, home, nil, "completion", "uninstall", "--json"))
	if len(stringList(uninstall["manual"])) != 1 || exists(zshFallback(home)) {
		t.Errorf("uninstall = %v", uninstall)
	}
	if got := readText(t, zshrc); got != original {
		t.Errorf("uninstall edited a hard-linked .zshrc: %q", got)
	}
}

// TestCompletionUninstallFindsAnEditedLine covers an editor that changed line
// endings, a line the learner edited, and a line the learner removed.
func TestCompletionUninstallFindsAnEditedLine(t *testing.T) {
	setup := func(t *testing.T) (string, map[string]string, string) {
		home := t.TempDir()
		env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
		zshrc := filepath.Join(home, ".zshrc")
		writeFile(t, zshrc, "export EDITOR=vi\n")
		if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
			t.Fatalf("install: exit %d, %s", r.code, r.stderr)
		}
		return home, env, zshrc
	}
	rcStatus := func(t *testing.T, data map[string]any) string {
		t.Helper()
		lines := data["rc_lines"].([]any)
		if len(lines) != 1 {
			t.Fatalf("rc_lines = %v", lines)
		}
		return lines[0].(map[string]any)["status"].(string)
	}

	t.Run("CRLF", func(t *testing.T) {
		home, env, zshrc := setup(t)
		writeFile(t, zshrc, strings.ReplaceAll(readText(t, zshrc), "\n", "\r\n"))
		got := decodeData(t, runEnv(t, env, home, nil, "completion", "uninstall", "--json"))
		if status := rcStatus(t, got); status != "removed" {
			t.Errorf("status = %s", status)
		}
		if text := readText(t, zshrc); text != "export EDITOR=vi\r\n" {
			t.Errorf(".zshrc = %q", text)
		}
	})

	t.Run("edited", func(t *testing.T) {
		home, env, zshrc := setup(t)
		edited := strings.Replace(readText(t, zshrc), "(( $+functions[compdef] )) && ", "", 1)
		writeFile(t, zshrc, edited)
		for range 2 {
			r := runEnv(t, env, home, nil, "completion", "uninstall", "--json")
			got := decodeData(t, r)
			if status := rcStatus(t, got); status != "not_removed" {
				t.Errorf("status = %s", status)
			}
			if manual := stringList(got["manual"]); len(manual) != 1 || !strings.Contains(manual[0], "remove this line") {
				t.Errorf("manual = %q", manual)
			}
			if !exists(completionRecord(home)) {
				t.Fatal("the record of the line was dropped, so it can never be removed")
			}
		}
		if readText(t, zshrc) != edited {
			t.Error("uninstall changed a line it did not recognise")
		}

		writeFile(t, zshrc, "export EDITOR=vi\n")
		got := decodeData(t, runEnv(t, env, home, nil, "completion", "uninstall", "--json"))
		if status := rcStatus(t, got); status != "already_gone" {
			t.Errorf("after the learner removed it: status = %s", status)
		}
		if exists(completionRecord(home)) {
			t.Error("the record outlived everything it described")
		}
	})
}

// TestCompletionCreatedZshrcIsRemoved: uninstall removes a .zshrc that install
// created, once it is empty again.
func TestCompletionCreatedZshrcIsRemoved(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
		t.Fatalf("install: exit %d, %s", r.code, r.stderr)
	}
	if !exists(filepath.Join(home, ".zshrc")) {
		t.Fatal("install did not create .zshrc")
	}
	if r := runEnv(t, env, home, nil, "completion", "uninstall"); r.code != cli.ExitOK {
		t.Fatalf("uninstall: exit %d, %s", r.code, r.stderr)
	}
	for _, path := range []string{filepath.Join(home, ".zshrc"), zshFallback(home), completionRecord(home)} {
		if exists(path) {
			t.Errorf("uninstall left %s", path)
		}
	}
}

// TestCompletionNeverReplacesForeignFiles: a completion file study did not
// write is kept unless --force, and uninstall removes only an unchanged script.
func TestCompletionNeverReplacesForeignFiles(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/usr/bin/fish"}
	target := filepath.Join(home, ".config", "fish", "completions", "study.fish")
	writeFile(t, target, "# my own completions\n")

	refused := runEnv(t, env, home, nil, "completion", "install", "--json")
	if refused.code != cli.ExitError || errorCode(t, refused) != "already_exists" {
		t.Fatalf("install over a foreign file: exit %d, %s", refused.code, refused.stdout)
	}
	if readText(t, target) != "# my own completions\n" {
		t.Fatal("install replaced a file it did not write")
	}

	if r := runEnv(t, env, home, nil, "completion", "install", "--force"); r.code != cli.ExitOK {
		t.Fatalf("install --force: exit %d, %s", r.code, r.stderr)
	}
	if !strings.HasPrefix(readText(t, target), "# fish completion for study") {
		t.Fatal("--force did not install the script")
	}
	if r := runEnv(t, env, home, nil, "completion", "install"); r.code != cli.ExitOK {
		t.Fatalf("reinstalling study's own script: exit %d, %s", r.code, r.stdout)
	}

	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("# my tweak\n")
	_ = f.Close()
	got := decodeData(t, runEnv(t, env, home, nil, "completion", "uninstall", "--json"))
	if kept := stringList(got["kept"]); len(kept) != 1 || kept[0] != shown(home, target) || !exists(target) {
		t.Errorf("uninstall of a changed script: kept %v, exists %v", kept, exists(target))
	}
}

// TestCompletionDefersToPackages: when a package provides completions, install
// does nothing unless --force, and doctor credits the package.
func TestCompletionDefersToPackages(t *testing.T) {
	roots := t.TempDir()
	saved := *cli.SystemCompletionRoots
	*cli.SystemCompletionRoots = []string{roots}
	t.Cleanup(func() { *cli.SystemCompletionRoots = saved })
	pkg := filepath.Join(roots, "zsh", "site-functions", "_study")
	writeFile(t, pkg, "#compdef study\n")

	home := t.TempDir()
	zfunc := filepath.Join(home, ".zfunc")
	if err := os.MkdirAll(zfunc, 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh", "FPATH": zfunc}

	got := decodeData(t, runEnv(t, env, home, nil, "completion", "install", "--json"))
	if got["provided_by"] != pkg || exists(filepath.Join(zfunc, "_study")) || exists(completionRecord(home)) {
		t.Errorf("install with package completions = %v", got)
	}
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	doctor := decodeData(t, runEnv(t, env, home, nil, "doctor", "--json"))
	if msg := finding(t, doctor, "completion")["message"]; msg != "zsh completions are provided by a package at "+pkg {
		t.Errorf("doctor: %v", msg)
	}

	if r := runEnv(t, env, home, nil, "completion", "install", "--force"); r.code != cli.ExitOK || !exists(filepath.Join(zfunc, "_study")) {
		t.Errorf("install --force: exit %d, %s", r.code, r.stderr)
	}
	if readText(t, pkg) != "#compdef study\n" {
		t.Error("install touched the package's script")
	}
}

func finding(t *testing.T, data map[string]any, name string) map[string]any {
	t.Helper()
	for _, f := range data["findings"].([]any) {
		if f := f.(map[string]any); f["name"] == name {
			return f
		}
	}
	t.Fatalf("no %s finding in %v", name, data)
	return nil
}

// TestCompletionReinstallElsewhereUndoesTheFirst: a second install with a
// different target removes the first one's script and .zshrc line.
func TestCompletionReinstallElsewhereUndoesTheFirst(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	zshrc := filepath.Join(home, ".zshrc")
	original := "export EDITOR=vi\n"
	writeFile(t, zshrc, original)
	if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
		t.Fatalf("install --yes: exit %d, %s", r.code, r.stderr)
	}
	if err := os.MkdirAll(filepath.Join(home, ".zfunc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := runEnv(t, env, home, nil, "completion", "install", "--dir", "~/.zfunc"); r.code != cli.ExitOK {
		t.Fatalf("install --dir: exit %d, %s", r.code, r.stderr)
	}
	if readText(t, zshrc) != original || exists(zshFallback(home)) || !exists(filepath.Join(home, ".zfunc", "_study")) {
		t.Errorf("after moving: .zshrc %q, old script %v", readText(t, zshrc), exists(zshFallback(home)))
	}
	if r := runEnv(t, env, home, nil, "completion", "uninstall"); r.code != cli.ExitOK {
		t.Fatalf("uninstall: exit %d, %s", r.code, r.stderr)
	}
	if exists(filepath.Join(home, ".zfunc", "_study")) || exists(completionRecord(home)) {
		t.Error("uninstall left the second install behind")
	}
}

// TestCompletionFailedInstallLeavesNothing: when the record cannot be
// written, install takes back the script and the .zshrc line.
func TestCompletionFailedInstallLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	zshrc := filepath.Join(home, ".zshrc")
	original := "export EDITOR=vi\n"
	writeFile(t, zshrc, original)
	state := filepath.Dir(completionRecord(home))
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o755) })

	r := runEnv(t, env, home, nil, "completion", "install", "--yes", "--json")
	if r.code != cli.ExitError {
		t.Fatalf("exit %d, %s", r.code, r.stdout)
	}
	if readText(t, zshrc) != original || exists(zshFallback(home)) {
		t.Errorf("a failed install left .zshrc %q and the script %v", readText(t, zshrc), exists(zshFallback(home)))
	}
}

// TestCompletionDryRunAndDoctorWriteNothing covers folders zsh would use,
// probes, and the Log.
func TestCompletionDryRunAndDoctorWriteNothing(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".oh-my-zsh", "oh-my-zsh.sh"), "# omz\n")
	zfunc := filepath.Join(home, ".zfunc")
	if err := os.MkdirAll(zfunc, 0o755); err != nil {
		t.Fatal(err)
	}
	before := tree(t, home)

	omz := map[string]string{"HOME": home, "SHELL": "/bin/zsh", "ZSH": filepath.Join(home, ".oh-my-zsh")}
	got := decodeData(t, runEnv(t, omz, home, nil, "completion", "install", "--dry-run", "--json"))
	if got["file"] != shown(home, filepath.Join(home, ".oh-my-zsh", "cache", "completions", "_study")) {
		t.Errorf("dry run target = %v", got["file"])
	}
	fpath := map[string]string{"HOME": home, "SHELL": "/bin/zsh", "FPATH": zfunc}
	decodeData(t, runEnv(t, fpath, home, nil, "completion", "install", "--dry-run", "--json"))

	fakeGit(t, "Ada Lovelace", "ada@example.com")
	for _, env := range []map[string]string{omz, fpath} {
		if r := runEnv(t, env, home, nil, "doctor", "--json"); r.code != cli.ExitOK {
			t.Errorf("doctor: exit %d, %s", r.code, r.stdout)
		}
	}
	if after := tree(t, home); !slices.Equal(after, before) {
		t.Errorf("dry runs and doctor wrote files:\nbefore %v\nafter  %v", before, after)
	}
}

func TestCompletionPromptNeedsATerminal(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("y\n")
	_ = w.Close()
	t.Cleanup(func() { _ = r.Close() })

	got := runEnv(t, env, home, r, "completion", "install")
	if got.code != cli.ExitUsage || strings.Contains(got.stderr, "[y/N]") {
		t.Errorf("exit %d, stderr %q: a pipe is not a person, so study must not ask", got.code, got.stderr)
	}
	if exists(filepath.Join(home, ".zshrc")) || exists(zshFallback(home)) {
		t.Error("install wrote files without consent")
	}
}

func TestCompletionRecordDamagedOrNewer(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/usr/bin/fish"}
	record := completionRecord(home)
	fakeGit(t, "Ada Lovelace", "ada@example.com")

	writeFile(t, record, "{broken")
	for _, args := range [][]string{{"completion", "install", "--json"}, {"completion", "uninstall", "--json"}} {
		r := runEnv(t, env, home, nil, args...)
		if r.code != cli.ExitError || errorCode(t, r) != "corrupt" || !strings.Contains(r.stdout, "delete it") {
			t.Errorf("%v with a damaged record: exit %d, %s", args, r.code, r.stdout)
		}
	}
	doctor := decodeData(t, runEnv(t, env, home, nil, "doctor", "--json"))
	if f := finding(t, doctor, "completion"); f["status"] != "warn" || f["fix"] != "delete "+shown(home, record)+", then run study completion install" {
		t.Errorf("doctor with a damaged record: %v", f)
	}

	newer := `{"format": 2, "installed": [], "added_later": true}`
	writeFile(t, record, newer)
	r := runEnv(t, env, home, nil, "completion", "install", "--json")
	if r.code != cli.ExitError || errorCode(t, r) != "newer_format" {
		t.Errorf("install with a newer record: exit %d, %s", r.code, r.stdout)
	}
	if readText(t, record) != newer {
		t.Error("install rewrote a newer record")
	}
}

// TestCompletionUninstallEveryShell: without --shell, uninstall undoes every
// install, whatever $SHELL is, and says which scripts were already gone.
func TestCompletionUninstallEveryShell(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	for _, shell := range []string{"fish", "bash"} {
		if r := runEnv(t, env, home, nil, "completion", "install", "--shell", shell); r.code != cli.ExitOK {
			t.Fatalf("install %s: exit %d, %s", shell, r.code, r.stderr)
		}
	}
	fish := filepath.Join(home, ".config", "fish", "completions", "study.fish")
	bash := filepath.Join(home, ".local", "share", "bash-completion", "completions", "study")
	if err := os.Remove(bash); err != nil {
		t.Fatal(err)
	}

	human := runEnv(t, env, home, nil, "completion", "uninstall")
	if human.code != cli.ExitOK || !strings.Contains(human.stdout, "Removed "+shown(home, fish)) ||
		!strings.Contains(human.stdout, shown(home, bash)+" was already gone") {
		t.Errorf("uninstall: exit %d\n%s", human.code, human.stdout)
	}
	if exists(fish) || exists(completionRecord(home)) {
		t.Error("uninstall left the fish script or the record")
	}
}

func TestCompletionBashUserDirIsAList(t *testing.T) {
	home := t.TempDir()
	first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
	env := map[string]string{"HOME": home, "BASH_COMPLETION_USER_DIR": first + ":" + second}
	got := decodeData(t, runEnv(t, env, home, nil, "completion", "install", "--shell", "bash", "--json"))
	if got["file"] != shown(home, filepath.Join(first, "completions", "study")) {
		t.Errorf("file = %v", got["file"])
	}
}

// TestCompletionWaitsForConcurrentEdits: when another program changes .zshrc
// while uninstall is removing the line, uninstall starts again from the new
// content instead of losing the change.
func TestCompletionWaitsForConcurrentEdits(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/bin/zsh"}
	zshrc := filepath.Join(home, ".zshrc")
	writeFile(t, zshrc, "export EDITOR=vi\n")
	if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
		t.Fatalf("install: exit %d, %s", r.code, r.stderr)
	}

	edits := 0
	appendAlias := func(path string) {
		edits++
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = f.WriteString("alias ll='ls -l'\n")
		_ = f.Close()
	}
	once := func(path string) {
		if edits == 0 {
			appendAlias(path)
		}
	}
	restore := cli.SetBeforeRCReplace(once)
	r := runEnv(t, env, home, nil, "completion", "uninstall")
	restore()
	if r.code != cli.ExitOK {
		t.Fatalf("uninstall: exit %d, %s", r.code, r.stderr)
	}
	if got := readText(t, zshrc); got != "export EDITOR=vi\nalias ll='ls -l'\n" {
		t.Errorf(".zshrc = %q: the concurrent edit or the removal was lost", got)
	}

	if r := runEnv(t, env, home, nil, "completion", "install", "--yes"); r.code != cli.ExitOK {
		t.Fatalf("reinstall: exit %d, %s", r.code, r.stderr)
	}
	restore = cli.SetBeforeRCReplace(appendAlias)
	r = runEnv(t, env, home, nil, "completion", "uninstall", "--json")
	restore()
	if r.code != cli.ExitError || errorCode(t, r) != "busy" {
		t.Errorf("an rc file that never settles: exit %d, %s", r.code, r.stdout)
	}
}

func TestLogLevelsAreNames(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"STUDY_HOME": home, "HOME": home}
	for _, level := range []string{"error+8", "DEBUG-4", "verbose"} {
		if r := runEnv(t, env, home, nil, "status", "--log-level", level, "--json"); r.code != cli.ExitUsage {
			t.Errorf("--log-level %s: exit %d", level, r.code)
		}
	}
	if r := runEnv(t, env, home, nil, "status", "--log-level", "DEBUG", "--json"); r.code != cli.ExitOK {
		t.Errorf("--log-level DEBUG: exit %d, %s", r.code, r.stdout)
	}
	env["STUDY_LOG"] = "DEBUG-4"
	if r := runEnv(t, env, home, nil, "status", "--json"); r.code != cli.ExitOK || !strings.Contains(r.stderr, "STUDY_LOG") {
		t.Errorf("STUDY_LOG=DEBUG-4: exit %d, stderr %q; want a warning", r.code, r.stderr)
	}
}

// TestGroupsWithoutSubcommandAreUsageErrorsInJSON: help is text, so --json on
// a command group reports a usage error instead.
func TestGroupsWithoutSubcommandAreUsageErrorsInJSON(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"STUDY_HOME": home, "HOME": home}
	for _, group := range []string{"completion", "topic", "library"} {
		r := runEnv(t, env, home, nil, group, "--json")
		if r.code != cli.ExitUsage || errorCode(t, r) != "usage" {
			t.Errorf("study %s --json: exit %d, %s", group, r.code, r.stdout)
		}
	}
	if r := runEnv(t, env, home, nil, "topic"); r.code != cli.ExitOK || !strings.Contains(strings.ToLower(r.stdout), "usage") {
		t.Errorf("study topic: exit %d, %q; want help", r.code, r.stdout)
	}
}

func TestHumanErrorsAreNotRecased(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"STUDY_HOME": home, "HOME": home}
	r := runEnv(t, env, home, nil, "status", "--log-level", "loud")
	if r.code != cli.ExitUsage || !strings.Contains(r.stderr, `--log-level must be debug, info, warn or error, not "loud"`) {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
}

// TestDoctorReportsTopicsItCannotRead covers a Topic folder without
// permission and a .git that is a file, which Checkpoints refuse.
func TestDoctorReportsTopicsItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	home := t.TempDir()
	study := filepath.Join(home, "study")
	env := map[string]string{"HOME": home, "STUDY_HOME": study, "SHELL": "/usr/bin/fish"}
	for _, title := range []string{"C", "Go"} {
		if r := runEnv(t, env, home, nil, "topic", "create", "--title", title); r.code != cli.ExitOK {
			t.Fatalf("setup: %s", r.stderr)
		}
	}
	if err := os.RemoveAll(filepath.Join(study, "go", ".git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(study, "go", ".git"), "gitdir: /elsewhere/.git\n")
	locked := filepath.Join(study, "c")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	status := decodeData(t, runEnv(t, env, home, nil, "status", "--json"))
	problems := status["problems"].([]any)
	if len(problems) != 1 || problems[0].(map[string]any)["id"] != "c" {
		t.Errorf("status problems = %v, want the unreadable Topic c", problems)
	}

	fakeGit(t, "Ada Lovelace", "ada@example.com")
	r := runEnv(t, env, home, nil, "doctor", "--json")
	if r.code != cli.ExitError {
		t.Errorf("doctor: exit %d, want 1 for an unreadable Topic", r.code)
	}
	var env2 struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env2); err != nil {
		t.Fatal(err)
	}
	if f := finding(t, env2.Data, "topic:c"); f["status"] != "fail" {
		t.Errorf("topic:c = %v", f)
	}
	if f := finding(t, env2.Data, "topic:go"); f["status"] != "warn" || !strings.Contains(f["message"].(string), "file or a symlink") {
		t.Errorf("topic:go = %v", f)
	}
}

// A completion file that cannot be checked is reported with its own error,
// never as gone with advice to reinstall.
func TestDoctorReportsAnUncheckableCompletionFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	home := t.TempDir()
	env := map[string]string{"HOME": home, "SHELL": "/usr/bin/fish"}
	if r := runEnv(t, env, home, nil, "completion", "install"); r.code != cli.ExitOK {
		t.Fatalf("install: exit %d, %s", r.code, r.stderr)
	}
	folder := filepath.Join(home, ".config", "fish", "completions")
	if err := os.Chmod(folder, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o755) })
	fakeGit(t, "Ada Lovelace", "ada@example.com")
	doctor := decodeData(t, runEnv(t, env, home, nil, "doctor", "--json"))
	got := finding(t, doctor, "completion")
	if msg, _ := got["message"].(string); !strings.HasPrefix(msg, "cannot check ") || strings.Contains(msg, "gone") {
		t.Errorf("doctor's completion Finding = %v, want the check's own error", got)
	}
}
