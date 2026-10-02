package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Diagnosis is what study doctor found: every Finding, and whether the setup
// is healthy enough to study.
type Diagnosis struct {
	// StudyHome is empty when the Study home cannot be resolved.
	StudyHome string    `json:"study_home,omitempty"`
	Healthy   bool      `json:"healthy"`
	Findings  []Finding `json:"findings"`
}

// Finding is one part of the setup that Diagnose looked at. Findings are not
// Checks: a Check belongs to a Lesson.
type Finding struct {
	// Name identifies what was examined, such as "git" or "topic:physics".
	Name    string        `json:"name"`
	Status  FindingStatus `json:"status"`
	Message string        `json:"message"`
	// Fix tells the learner what to do about a warning or a failure.
	Fix string `json:"fix,omitempty"`
}

// FindingStatus says how a Finding affects studying.
type FindingStatus string

// Finding statuses. Only a failure makes the setup unhealthy.
const (
	FindingOK   FindingStatus = "ok"
	FindingWarn FindingStatus = "warn"
	FindingFail FindingStatus = "fail"
)

// Add records f and updates Healthy.
func (d *Diagnosis) Add(f Finding) {
	d.Findings = append(d.Findings, f)
	if f.Status == FindingFail {
		d.Healthy = false
	}
}

// Failed returns the names of the failed Findings.
func (d Diagnosis) Failed() []string {
	var names []string
	for _, f := range d.Findings {
		if f.Status == FindingFail {
			names = append(names, f.Name)
		}
	}
	return names
}

// minGit is the oldest git Lamplight supports: git init --initial-branch
// arrived in 2.28.
var minGit = [2]int{2, 28}

// Diagnose examines the setup without needing it to work: an unresolvable
// Study home, a missing git or a damaged file each become a Finding instead of
// an error. It writes nothing.
func Diagnose(ctx context.Context, opts Options) Diagnosis {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	d := Diagnosis{Healthy: true, Findings: []Finding{}}
	config := diagnoseConfig(getenv)
	d.Add(config)

	home, err := resolveHome(getenv)
	switch {
	case err != nil && config.Status == FindingFail:
		// The config Finding already says what is wrong with config.toml.
		d.Add(Finding{Name: "study_home", Status: FindingFail,
			Message: "unknown, because config.toml cannot be read",
			Fix:     "fix config.toml as the config Finding says, or set STUDY_HOME"})
	case err != nil:
		d.Add(Finding{Name: "study_home", Status: FindingFail, Message: err.Error(),
			Fix: "set STUDY_HOME, or study_home in config.toml, to the absolute path of your Study home"})
	default:
		d.StudyHome = home
		d.Add(diagnoseStudyHome(home))
	}

	git := diagnoseGit(ctx)
	d.Add(git)
	if git.Status == FindingOK {
		d.Add(diagnoseGitIdentity(ctx))
	}

	if err != nil {
		return d
	}
	c := &Core{home: home, dir: home, now: time.Now, newID: randomID, log: opts.Logger, gating: isGating}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	d.Add(c.diagnoseLocalState())
	c.diagnoseTopics(ctx, &d)
	d.Add(c.diagnoseLibrary())
	return d
}

func diagnoseConfig(getenv func(string) string) Finding {
	f := Finding{Name: "config"}
	path, err := configPath(getenv)
	if err != nil {
		f.Status, f.Message, f.Fix = FindingWarn, "cannot locate config.toml: "+err.Error(), "set HOME or XDG_CONFIG_HOME"
		return f
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		f.Status, f.Message = FindingOK, fmt.Sprintf("no %s; using the defaults", path)
		return f
	}
	if _, err := loadConfig(path); err != nil {
		f.Status, f.Message = FindingFail, err.Error()
		switch CodeOf(err) {
		case CodeNewerFormat:
			f.Fix = "upgrade study"
		case CodeCorrupt:
			f.Fix = "fix the TOML in " + path + ", or delete it to use the defaults"
		default:
			f.Fix = "make " + path + " readable"
		}
		return f
	}
	f.Status, f.Message = FindingOK, path
	return f
}

func diagnoseStudyHome(home string) Finding {
	f := Finding{Name: "study_home"}
	info, err := os.Stat(home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Status, f.Message = FindingOK, home+" (created when you create your first Topic)"
		return f
	case err != nil:
		f.Status, f.Message, f.Fix = FindingFail, err.Error(), "make "+home+" readable"
		return f
	case !info.IsDir():
		f.Status, f.Message, f.Fix = FindingFail, home+" is not a folder",
			"move the file away, or set STUDY_HOME to another folder"
		return f
	}
	if !canWrite(home) {
		f.Status, f.Message, f.Fix = FindingFail, "cannot write to "+home,
			"give yourself write permission on "+home
		return f
	}
	f.Status, f.Message = FindingOK, home
	return f
}

var gitVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)(\.\d+)?`)

func diagnoseGit(ctx context.Context) Finding {
	f := Finding{Name: "git"}
	if _, err := exec.LookPath("git"); err != nil {
		f.Status, f.Message, f.Fix = FindingFail, "git is not installed, or not on PATH",
			fmt.Sprintf("install git %d.%d or newer", minGit[0], minGit[1])
		return f
	}
	out, err := gitCommand(ctx, "", "--version").Output()
	if err != nil {
		f.Status, f.Message, f.Fix = FindingFail, "running git --version: "+err.Error(), "reinstall git"
		return f
	}
	m := gitVersionPattern.FindStringSubmatch(string(out))
	if m == nil {
		f.Status, f.Message = FindingWarn, "cannot read the git version from "+strings.TrimSpace(string(out))
		return f
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minGit[0] || major == minGit[0] && minor < minGit[1] {
		f.Status, f.Message, f.Fix = FindingFail,
			fmt.Sprintf("git %s is too old: Lamplight needs %d.%d or newer", m[0], minGit[0], minGit[1]), "upgrade git"
		return f
	}
	f.Status, f.Message = FindingOK, "git "+m[0]
	return f
}

// diagnoseGitIdentity reports whether Checkpoints can be saved: they are git
// commits, made with the learner's own name and email from their global or
// system git configuration.
func diagnoseGitIdentity(ctx context.Context) Finding {
	f := Finding{Name: "git_identity"}
	values := map[string]string{}
	var missing, fixes []string
	for _, key := range []string{"user.name", "user.email"} {
		cmd := gitCommand(ctx, os.TempDir(), "config", "--get", key)
		// Read the system configuration too, as Checkpoints do.
		env := cmd.Env[:0]
		for _, kv := range cmd.Env {
			if !strings.HasPrefix(kv, "GIT_CONFIG_NOSYSTEM=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			// git config exits with 1 when the key is not set.
		case err != nil:
			f.Status, f.Message = FindingWarn, fmt.Sprintf("reading %s from git: %v %s", key, err, strings.TrimSpace(stderr.String()))
			return f
		}
		if v := strings.TrimSpace(string(out)); v != "" {
			values[key] = v
			continue
		}
		missing = append(missing, key)
		example := `"Your Name"`
		if key == "user.email" {
			example = "you@example.com"
		}
		fixes = append(fixes, "git config --global "+key+" "+example)
	}
	if len(missing) > 0 {
		f.Status, f.Fix = FindingWarn, strings.Join(fixes, " && ")
		f.Message = "git has no " + strings.Join(missing, " or ") + ", so Checkpoints cannot be saved"
		return f
	}
	f.Status, f.Message = FindingOK, fmt.Sprintf("Checkpoints are saved as %s <%s>", values["user.name"], values["user.email"])
	return f
}

func (c *Core) diagnoseLocalState() Finding {
	f := Finding{Name: "local_state"}
	path := filepath.Join(c.home, localDir, stateFile)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Status, f.Message = FindingOK, "no local state yet"
		return f
	case err != nil:
		f.Status, f.Message, f.Fix = FindingWarn, err.Error(), "make "+path+" readable, or delete it"
		return f
	}
	var state localState
	if _, err := toml.Decode(string(data), &state); err != nil {
		f.Status, f.Message = FindingWarn, path+" is damaged, so study ignores it: "+err.Error()
		f.Fix = "delete " + path + "; study writes it again"
		return f
	}
	if state.Format > FormatVersion {
		f.Status, f.Message, f.Fix = FindingFail, newerFormat(path, state.Format).Error(), "upgrade study"
		return f
	}
	f.Status, f.Message = FindingOK, path
	return f
}

func (c *Core) diagnoseTopics(ctx context.Context, d *Diagnosis) {
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		d.Add(Finding{Name: "topics", Status: FindingOK, Message: "no Topics yet"})
		return
	}
	if err != nil {
		d.Add(Finding{Name: "topics", Status: FindingFail, Message: err.Error(), Fix: "make " + c.home + " readable"})
		return
	}
	defer home.Close()
	topics, problems, err := c.listTopics(ctx, home)
	if err != nil {
		d.Add(Finding{Name: "topics", Status: FindingFail, Message: err.Error(), Fix: "make " + c.home + " readable"})
		return
	}
	summary := Finding{Name: "topics", Status: FindingOK}
	switch n := len(topics); n {
	case 0:
		summary.Message = "no Topics yet"
	case 1:
		summary.Message = "1 Topic"
	default:
		summary.Message = fmt.Sprintf("%d Topics", n)
	}
	if len(problems) > 0 {
		summary.Message += fmt.Sprintf(", %d unreadable", len(problems))
	}
	d.Add(summary)
	for _, p := range problems {
		f := Finding{Name: "topic:" + p.ID, Status: FindingFail, Message: p.Message}
		switch p.Code {
		case CodeNewerFormat:
			f.Fix = "upgrade study"
		case CodeInternal:
			f.Fix = "make " + filepath.Join(c.home, p.ID) + " and its files readable"
		default:
			f.Fix = "fix the file by hand, or restore it from the Topic's git history"
		}
		d.Add(f)
	}
	for _, t := range topics {
		info, err := home.Lstat(filepath.Join(t.ID, ".git"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			d.Add(Finding{Name: "topic:" + t.ID, Status: FindingWarn,
				Message: t.ID + " is not a git repository, so Checkpoints cannot be saved",
				Fix:     "git -C " + t.Path + " init --initial-branch=main"})
		case err != nil:
			d.Add(Finding{Name: "topic:" + t.ID, Status: FindingWarn,
				Message: "cannot read " + t.ID + "/.git: " + err.Error(), Fix: "make " + t.Path + "/.git readable"})
		case !info.IsDir():
			// Checkpoints refuse a .git file or symlink: it could point them at
			// another repository.
			d.Add(Finding{Name: "topic:" + t.ID, Status: FindingWarn,
				Message: t.ID + "/.git is a file or a symlink, not a folder, so Checkpoints refuse to save",
				Fix:     "move " + t.Path + "/.git away, then run git -C " + t.Path + " init --initial-branch=main"})
		}
	}
}

func (c *Core) diagnoseLibrary() Finding {
	f := Finding{Name: "library"}
	path := filepath.Join(c.home, libraryIndex)
	ix, err := library.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Status, f.Message = FindingOK, "no index yet: study library build <folder> indexes your books"
		return f
	case errors.Is(err, library.ErrUnsupportedFormat):
		f.Status, f.Message = FindingWarn, path+" was written by a newer version of study"
		f.Fix = "upgrade study, or rebuild the index with study library build <folder>"
		return f
	case err != nil:
		f.Status, f.Message, f.Fix = FindingWarn, "cannot read "+path+": "+err.Error(),
			"rebuild the index with study library build <folder>"
		return f
	}
	if _, err := os.Stat(ix.Root); err != nil {
		f.Status, f.Message = FindingWarn, "the indexed folder "+ix.Root+" is gone"
		f.Fix = "rebuild the index with study library build <folder>"
		return f
	}
	f.Status, f.Message = FindingOK, fmt.Sprintf("%d books indexed from %s", len(ix.Books), ix.Root)
	return f
}
