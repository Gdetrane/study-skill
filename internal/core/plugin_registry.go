package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The Knowledge base plugin registry (ADR-0012): the Knowledge base plugins
// this computer has, each under a name, with the command that starts it or
// the URL it answers at. A Topic names a plugin and never says what the name
// stands for, so nothing a Topic holds, which syncs between computers and
// which the agent can write, decides which program study starts.
//
// The registry is one file in Lamplight's configuration folder, beside
// config.toml and outside the Study home. It is this computer's and the
// learner's to change (study knowledge-base): it is no Topic's state, so
// changing it records no Event, and agents have no tool for it, which a test
// in internal/mcpserver enforces. A registry the agent could write, because
// the configuration folder is inside the Study home, is not read at all.

const (
	// pluginRegistryFile is the registry, and pluginRegistryLock the file
	// beside it whose lock a change holds. The lock cannot be the registry's
	// own: a change replaces the registry with a new file.
	pluginRegistryFile = "knowledge-base-plugins.json"
	pluginRegistryLock = "knowledge-base-plugins.lock"

	// maxPluginArgs is how many arguments a plugin's command may have, and
	// maxRegistryBytes how large the registry may be: far more than either
	// needs.
	maxPluginArgs    = 100
	maxRegistryBytes = 1 << 20

	// crashRegistryLocked is a point where a test can interrupt a change to
	// the registry, as a crash would (see Core.crash): the lock is held and
	// the registry read, nothing written.
	crashRegistryLocked = "registry-locked"
)

// reservedPluginNames cannot name a plugin. They are the kinds of Knowledge
// base a Topic records, "none" and "plugin" (with the plugin's name beside
// it), so a name is never mistaken for a kind.
var reservedPluginNames = []string{KnowledgeBaseNone, "plugin"}

// Plugin is a Knowledge base plugin registered on this computer: a name, and
// either the command that starts it or the URL it answers at.
type Plugin struct {
	// Name is what a Topic calls the plugin: lowercase letters, digits and
	// single hyphens, up to 64 characters, like a Topic's id.
	Name string `json:"name"`
	// Command is the program, as an absolute path, and its arguments. It is
	// an argument list, never a shell string.
	Command []string `json:"command,omitempty"`
	// URL is the http or https address of a plugin the learner runs as a
	// service.
	URL string `json:"url,omitempty"`
	// Problem says, in a listing, why study does not use the plugin as it
	// is registered, and what to do. It is never stored.
	Problem string `json:"problem,omitempty"`
}

// PluginList is the Knowledge base plugins registered on this computer,
// ordered by name.
type PluginList struct {
	// Registry is the file that holds them, whether or not it exists yet.
	Registry string   `json:"registry"`
	Plugins  []Plugin `json:"plugins"`
}

// PluginSpec describes a Knowledge base plugin to register.
type PluginSpec struct {
	Name string
	// Command is the program and its arguments. The program is a path, taken
	// from the folder study started in when it is relative, or a bare name
	// to find on PATH. Give a Command or a URL.
	Command []string
	URL     string
	// Replace allows the name to be registered already with another command
	// or URL, which this one then replaces.
	Replace bool
	DryRun  bool
}

// PluginRegistration reports a Knowledge base plugin registered.
type PluginRegistration struct {
	// Plugin is the plugin as registered: a command's program is the
	// absolute path it was resolved to.
	Plugin   Plugin `json:"plugin"`
	Registry string `json:"registry"`
	// Replaced is what the name stood for before, when Replace changed it.
	Replaced *Plugin `json:"replaced,omitempty"`
	// Changed is false when the plugin was registered that way already, so
	// nothing was written.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// PluginRemoval reports a Knowledge base plugin removed from the registry.
type PluginRemoval struct {
	// Plugin is what was registered under the name.
	Plugin   Plugin `json:"plugin"`
	Registry string `json:"registry"`
	DryRun   bool   `json:"dry_run,omitempty"`
}

// Plugins lists the Knowledge base plugins registered on this computer. A
// plugin whose program is inside the Study home this study uses, which can
// happen when the Study home was another when it was registered, carries a
// Problem. It reads only.
func (c *Core) Plugins(ctx context.Context) (PluginList, error) {
	if err := ctx.Err(); err != nil {
		return PluginList{}, err
	}
	reg, err := c.locatePluginRegistry()
	if err != nil {
		return PluginList{}, err
	}
	plugins, err := reg.read()
	if err != nil {
		return PluginList{}, err
	}
	list := PluginList{Registry: reg.path, Plugins: []Plugin{}}
	for _, p := range plugins {
		if len(p.Command) > 0 && c.insideStudyHome(p.Command[0]) {
			p.Problem = "its program is inside the Study home, " + c.home + ", where your agent can write, so study does not " +
				"use this plugin: install the program outside the Study home, then register it again with " +
				"study knowledge-base add " + p.Name + " --replace -- <command>"
		}
		list.Plugins = append(list.Plugins, p)
	}
	return list, nil
}

// AddPlugin registers a Knowledge base plugin on this computer under a name.
// A command's program is resolved to an absolute path, which is what the
// registry holds, and refused when it is inside the Study home: study starts
// a plugin outside the agent's sandbox, so the program must be one the agent
// cannot change. A URL must be http or https.
//
// Registering a plugin the way it is registered already changes nothing. A
// name that stands for something else is refused, unless spec.Replace says to
// replace it. The change is made under the registry's lock, so two changes
// at once never lose each other's.
//
// It records no Event, is for the learner, and agents have no tool for it.
func (c *Core) AddPlugin(ctx context.Context, spec PluginSpec) (PluginRegistration, error) {
	reg, err := c.locatePluginRegistry()
	if err != nil {
		return PluginRegistration{}, err
	}
	plugin, err := c.newPlugin(spec)
	if err != nil {
		return PluginRegistration{}, err
	}
	out := PluginRegistration{Plugin: plugin, Registry: reg.path, DryRun: spec.DryRun}
	err = c.changePlugins(ctx, reg, spec.DryRun, func(plugins []Plugin) ([]Plugin, bool, error) {
		out.Replaced, out.Changed = nil, false
		i := slices.IndexFunc(plugins, func(p Plugin) bool { return p.Name == plugin.Name })
		switch {
		case i < 0:
			out.Changed = true
			return append(plugins, plugin), true, nil
		case plugins[i].URL == plugin.URL && slices.Equal(plugins[i].Command, plugin.Command):
			return nil, false, nil
		case !spec.Replace:
			return nil, false, &Error{Code: CodeAlreadyExists, Message: "a Knowledge base plugin named " + plugin.Name +
				" is registered already, with another command or URL (study knowledge-base list shows it): " +
				"pass --replace to register this one in its place, or give this one another name"}
		}
		replaced := plugins[i]
		out.Replaced, out.Changed = &replaced, true
		plugins[i] = plugin
		return plugins, true, nil
	})
	if err != nil {
		return PluginRegistration{}, err
	}
	if out.Changed && !spec.DryRun {
		c.log.Info("registered a Knowledge base plugin", "name", plugin.Name, "registry", out.Registry)
	}
	return out, nil
}

// RemovePlugin takes a Knowledge base plugin out of this computer's
// registry, under the registry's lock. A Topic that names it is afterwards
// treated as one without a Knowledge base. Like AddPlugin it records no
// Event, is for the learner, and agents have no tool for it.
func (c *Core) RemovePlugin(ctx context.Context, name string, dryRun bool) (PluginRemoval, error) {
	reg, err := c.locatePluginRegistry()
	if err != nil {
		return PluginRemoval{}, err
	}
	if err := checkPluginName(name); err != nil {
		return PluginRemoval{}, err
	}
	out := PluginRemoval{Registry: reg.path, DryRun: dryRun}
	err = c.changePlugins(ctx, reg, dryRun, func(plugins []Plugin) ([]Plugin, bool, error) {
		i := slices.IndexFunc(plugins, func(p Plugin) bool { return p.Name == name })
		if i < 0 {
			return nil, false, &Error{Code: CodeNotFound, Message: "no Knowledge base plugin named " + name +
				" is registered on this computer: study knowledge-base list shows the ones that are"}
		}
		out.Plugin = plugins[i]
		return slices.Delete(plugins, i, i+1), true, nil
	})
	if err != nil {
		return PluginRemoval{}, err
	}
	if !dryRun {
		c.log.Info("removed a Knowledge base plugin", "name", name, "registry", out.Registry)
	}
	return out, nil
}

// changePlugins makes one change to the registry. plan is given the plugins
// registered, by name, and returns what the registry should hold instead, or
// false when nothing changes.
//
// It is planned once without the lock, so a change that cannot be made, one
// that changes nothing and a dry run all answer at once, and leave neither a
// folder nor a lock file behind. A change is then planned again under the
// lock, against the registry as it is by then, and written by replacing the
// file, so a crash leaves the registry as it was or as it should be.
func (c *Core) changePlugins(ctx context.Context, reg pluginRegistry, dryRun bool, plan func([]Plugin) ([]Plugin, bool, error)) error {
	err := c.planAndChangePlugins(ctx, reg, dryRun, plan)
	if err != nil && ctx.Err() != nil {
		return &Error{Code: CodeCanceled, Err: ctx.Err(),
			Message: "the command was stopped before it changed the Knowledge base plugin registry, so nothing was changed"}
	}
	return err
}

func (c *Core) planAndChangePlugins(ctx context.Context, reg pluginRegistry, dryRun bool, plan func([]Plugin) ([]Plugin, bool, error)) error {
	plugins, err := reg.read()
	if err != nil {
		return err
	}
	if _, changed, err := plan(plugins); err != nil || !changed || dryRun {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// The XDG Base Directory Specification asks for 0700 on a configuration
	// folder that has to be created.
	if err := os.MkdirAll(reg.dir, 0o700); err != nil {
		return internalError("creating Lamplight's configuration folder "+reg.dir, err)
	}
	dir, err := os.OpenRoot(reg.dir)
	if err != nil {
		return internalError("opening Lamplight's configuration folder "+reg.dir, err)
	}
	defer dir.Close()
	unlock, err := lockFile(ctx, dir, pluginRegistryLock, "the Knowledge base plugin registry",
		"changing the Knowledge base plugin registry")
	if err != nil {
		return err
	}
	defer unlock()

	// And again under the lock, which is the plan that counts: another study
	// may have changed the registry while this one waited.
	if plugins, err = reg.readFrom(dir); err != nil {
		return err
	}
	next, changed, err := plan(plugins)
	if err != nil || !changed {
		return err
	}
	if err := c.crashAt(crashRegistryLocked); err != nil {
		return err
	}
	// Only the holder of the lock writes here, so a temporary file of the
	// registry's is what a study that crashed left.
	if entries, err := fs.ReadDir(dir.FS(), "."); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), tempPrefix(pluginRegistryFile)) {
				_ = dir.Remove(e.Name())
			}
		}
	}
	return reg.write(dir, next)
}

// pluginRegistry is where the registry is on this computer.
type pluginRegistry struct {
	dir  string // Lamplight's configuration folder
	path string // the registry in it
}

// locatePluginRegistry finds the registry, and refuses one the agent could
// write: the registry decides which programs study starts, outside the
// agent's sandbox, so it is read only from a folder outside the Study home,
// and never from one whose place depends on the folder study started in.
func (c *Core) locatePluginRegistry() (pluginRegistry, error) {
	dir, err := configDir(c.getenv)
	if err != nil {
		return pluginRegistry{}, &Error{Code: CodeFailedPrecondition, Err: err, Message: "cannot find Lamplight's " +
			"configuration folder, which holds the Knowledge base plugin registry: set HOME or XDG_CONFIG_HOME"}
	}
	if !filepath.IsAbs(dir) {
		return pluginRegistry{}, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lamplight's configuration "+
			"folder is %q, which is not an absolute path, so the Knowledge base plugin registry would be wherever study "+
			"is started, inside a Topic too. It is not read: give XDG_CONFIG_HOME, or HOME when XDG_CONFIG_HOME is not "+
			"set, as an absolute path", dir)}
	}
	dir = filepath.Clean(dir)
	if c.insideStudyHome(dir) {
		return pluginRegistry{}, &Error{Code: CodeFailedPrecondition, Message: "Lamplight's configuration folder, " + dir +
			", is inside the Study home, " + c.home + ", where your agent can write, so the Knowledge base plugin " +
			"registry in it is not read: it decides which programs study starts. Keep the two apart: set " +
			"XDG_CONFIG_HOME to a folder outside the Study home, or move the Study home"}
	}
	return pluginRegistry{dir: dir, path: filepath.Join(dir, pluginRegistryFile)}, nil
}

// registryFile is the content of the registry.
type registryFile struct {
	Format  int             `json:"format"`
	Plugins []registryEntry `json:"plugins"`
}

type registryEntry struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	URL     string   `json:"url,omitempty"`
}

// read returns the plugins the registry holds, ordered by name. A registry
// that is not there yet holds none.
func (reg pluginRegistry) read() ([]Plugin, error) {
	dir, err := os.OpenRoot(reg.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("opening Lamplight's configuration folder "+reg.dir, err)
	}
	defer dir.Close()
	return reg.readFrom(dir)
}

// readFrom is read through the configuration folder, opened. The registry
// is a regular file there: a symbolic link is not followed, since study
// replaces the registry when it changes and the link could lead anywhere.
//
// Whatever the file holds was checked like a registration before it is
// returned, so nothing from it reaches a terminal or a program unchecked. A
// file this version cannot vouch for whole is damaged, and none of it is
// used.
func (reg pluginRegistry) readFrom(dir *os.Root) ([]Plugin, error) {
	damaged := func(format string, args ...any) error {
		return corruptf("%s is damaged (%s): fix it by hand, or delete it and register your Knowledge base plugins "+
			"again with study knowledge-base add", reg.path, fmt.Sprintf(format, args...))
	}
	info, err := dir.Lstat(pluginRegistryFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading "+reg.path, err)
	}
	switch {
	case !info.Mode().IsRegular():
		return nil, corruptf("%s is not a regular file, so study does not read it as the Knowledge base plugin registry "+
			"(a symbolic link is not followed): put the file itself there, or delete it and register your Knowledge base "+
			"plugins again with study knowledge-base add", reg.path)
	case info.Size() > maxRegistryBytes:
		return nil, damaged("it is larger than %d bytes", maxRegistryBytes)
	}
	data, err := dir.ReadFile(pluginRegistryFile)
	if err != nil {
		return nil, internalError("reading "+reg.path, err)
	}

	// The format is read first, on its own: a newer version of study may have
	// changed the shape of everything else, and a registry this version
	// cannot decode for that reason is newer, not damaged. Only once the
	// format is one this version knows is the rest decoded.
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, damaged("%v", err)
	}
	switch {
	case header.Format < 1:
		return nil, damaged("it has no format number")
	case header.Format > FormatVersion:
		return nil, newerFormat(reg.path, header.Format)
	}
	var file registryFile
	dec := json.NewDecoder(bytes.NewReader(data))
	// A key this version does not know could change what an entry means.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, damaged("%v", err)
	}

	plugins := make([]Plugin, 0, len(file.Plugins))
	seen := map[string]bool{}
	for _, e := range file.Plugins {
		if e.Name == "" {
			return nil, damaged("an entry has no name")
		}
		if err := checkPluginName(e.Name); err != nil {
			return nil, damaged("%v", err)
		}
		if seen[e.Name] {
			return nil, damaged("it registers %s twice", e.Name)
		}
		seen[e.Name] = true
		p := Plugin{Name: e.Name}
		switch {
		case len(e.Command) > 0 && e.URL != "":
			return nil, damaged("%s has both a command and a URL", e.Name)
		case len(e.Command) > 0:
			if !filepath.IsAbs(e.Command[0]) {
				return nil, damaged("the program of %s is not an absolute path", e.Name)
			}
			if err := checkCommand(e.Command); err != nil {
				return nil, damaged("%s: %v", e.Name, err)
			}
			p.Command = e.Command
		case e.URL != "":
			if p.URL, err = cleanURL(e.URL); err != nil {
				return nil, damaged("%s: %v", e.Name, err)
			}
		default:
			return nil, damaged("%s has neither a command nor a URL", e.Name)
		}
		plugins = append(plugins, p)
	}
	slices.SortFunc(plugins, func(a, b Plugin) int { return strings.Compare(a.Name, b.Name) })
	return plugins, nil
}

// write replaces the registry with one holding plugins, ordered by name. It
// is created for the learner alone to read: a plugin's URL may carry a key.
func (reg pluginRegistry) write(dir *os.Root, plugins []Plugin) error {
	file := registryFile{Format: FormatVersion, Plugins: make([]registryEntry, 0, len(plugins))}
	for _, p := range plugins {
		file.Plugins = append(file.Plugins, registryEntry{Name: p.Name, Command: p.Command, URL: p.URL})
	}
	slices.SortFunc(file.Plugins, func(a, b registryEntry) int { return strings.Compare(a.Name, b.Name) })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(file); err != nil {
		return internalError("encoding the Knowledge base plugin registry", err)
	}
	return writeFileAtomicMode(dir, pluginRegistryFile, buf.Bytes(), 0o600)
}

// newPlugin checks a registration and returns the plugin as the registry
// will hold it.
func (c *Core) newPlugin(spec PluginSpec) (Plugin, error) {
	if err := checkPluginName(spec.Name); err != nil {
		return Plugin{}, err
	}
	p := Plugin{Name: spec.Name}
	switch {
	case len(spec.Command) > 0 && spec.URL != "":
		return Plugin{}, invalidf("give Knowledge base plugin %s a command or a URL, not both", spec.Name)
	case len(spec.Command) > 0:
		program, err := c.resolveProgram(spec.Command[0])
		if err != nil {
			return Plugin{}, err
		}
		p.Command = append([]string{program}, spec.Command[1:]...)
		if err := checkCommand(p.Command); err != nil {
			return Plugin{}, invalidf("%v", err)
		}
	case spec.URL != "":
		url, err := cleanURL(spec.URL)
		if err != nil {
			return Plugin{}, err
		}
		p.URL = url
	default:
		return Plugin{}, invalidf("give Knowledge base plugin %s the command that starts it, or the URL it answers at", spec.Name)
	}
	return p, nil
}

// checkPluginName checks the name a plugin is registered under. The learner
// types it and a Topic's topic.toml holds it, so it follows the rule of a
// Topic's id, and is none of the reserved names.
func checkPluginName(name string) error {
	switch {
	case name == "":
		return invalidf("name the Knowledge base plugin: lowercase letters, digits and single hyphens, such as shelf")
	case len(name) > 64 || !topicIDPattern.MatchString(name):
		return invalidf("%q is not a valid name for a Knowledge base plugin: use lowercase letters, digits and single "+
			"hyphens, up to 64 characters", name)
	case slices.Contains(reservedPluginNames, name):
		return invalidf("%q cannot name a Knowledge base plugin: it is a kind of Knowledge base, which a Topic records "+
			"beside the name of its plugin", name)
	}
	return nil
}

// checkCommand checks a plugin's command as the registry holds it and study
// shows it: the program and each argument are valid UTF-8 without control
// or bidirectional control characters, kept otherwise exactly as given,
// since the program receives them as they are. An argument may be empty.
func checkCommand(command []string) error {
	if len(command)-1 > maxPluginArgs {
		return fmt.Errorf("the command has more than %d arguments", maxPluginArgs)
	}
	for i, word := range command {
		what := fmt.Sprintf("argument %d of the command", i)
		if i == 0 {
			what = "the program's path"
		}
		if err := checkCommandWord(what, word); err != nil {
			return err
		}
	}
	return nil
}

func checkCommandWord(what, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s is not valid UTF-8 text", what)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", what)
		}
		if isBidiControl(r) {
			return fmt.Errorf("%s contains a bidirectional control character (U+%04X), which can make text display "+
				"differently from what it says", what, r)
		}
	}
	if utf8.RuneCountInString(s) > maxPathRunes {
		return fmt.Errorf("%s is longer than %d characters", what, maxPathRunes)
	}
	return nil
}

// resolveProgram finds the program a command names and returns its absolute
// path. A bare name is looked up in the folders of PATH, and a path is taken
// from the folder study started in, with a leading ~ for the home folder.
// The path is kept as it was found, symbolic links unresolved, so a link
// that a package manager moves to each new version keeps working.
//
// A program inside the Study home is refused, by its name and by where its
// symbolic links lead: the agent could replace it there, and study would
// then start the agent's program outside the agent's sandbox.
func (c *Core) resolveProgram(program string) (string, error) {
	if program == "" {
		return "", invalidf("the command is empty: give the program that starts the Knowledge base plugin")
	}
	if err := checkCommandWord("the program", program); err != nil {
		return "", invalidf("%v", err)
	}
	var path string
	if program != "~" && !strings.ContainsRune(program, '/') && !strings.ContainsRune(program, filepath.Separator) {
		for _, dir := range filepath.SplitList(c.getenv("PATH")) {
			// Only the absolute folders of PATH: one relative to the folder
			// study started in, as "." and an empty entry are, would let a
			// file in a Topic stand for the program.
			if !filepath.IsAbs(dir) {
				continue
			}
			if candidate := filepath.Join(dir, program); checkProgramFile(candidate) == nil {
				path = candidate
				break
			}
		}
		if path == "" {
			return "", &Error{Code: CodeNotFound, Message: fmt.Sprintf("there is no program named %q in the folders of "+
				"your PATH: install it, or give the path to it", program)}
		}
	} else {
		path = c.expandPath(program)
		if err := checkProgramFile(path); err != nil {
			return "", err
		}
	}
	if err := checkCommandWord("the program's path", path); err != nil {
		return "", invalidf("%v", err)
	}
	if c.insideStudyHome(path) {
		return "", invalidf("%s is inside the Study home, %s, where your agent can write. study starts a Knowledge base "+
			"plugin outside the agent's sandbox, so its program must be one the agent cannot change: install it outside "+
			"the Study home", path, c.home)
	}
	return path, nil
}

// checkProgramFile checks that path is a file the learner can run.
func checkProgramFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Error{Code: CodeNotFound, Err: err, Message: fmt.Sprintf("there is no program at %q", path)}
	case err != nil:
		return internalError("reading "+path, err)
	case info.IsDir():
		return invalidf("%q is a folder, not a program", path)
	case !info.Mode().IsRegular() || !canExecute(path):
		return invalidf("%q is not a program you can run: make it executable, with chmod +x, or name another", path)
	}
	return nil
}

// insideStudyHome reports whether path is the Study home or inside it: by
// its name, or by where it really is once symbolic links are resolved, in
// path and in the Study home's own.
func (c *Core) insideStudyHome(path string) bool {
	path = filepath.Clean(path)
	realHome, real := realPath(c.home), realPath(path)
	return inside(c.home, path) || inside(c.home, real) || inside(realHome, path) || inside(realHome, real)
}

// realPath resolves the symbolic links in an absolute path as far as the
// path exists. What does not exist yet, such as a folder about to be
// created, is kept as named below the part that does, and a link that leads
// to nothing yet is followed to where it would lead.
func realPath(path string) string {
	path = filepath.Clean(path)
	// Each round follows one link by hand; a chain longer than this is a loop.
	for range 40 {
		dir, rest := path, ""
		for {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				return filepath.Join(real, rest)
			}
			if info, err := os.Lstat(dir); err == nil && info.Mode()&fs.ModeSymlink != 0 {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return path
			}
			dir, rest = parent, filepath.Join(filepath.Base(dir), rest)
		}
		target, err := os.Readlink(dir)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dir), target)
		}
		path = filepath.Join(target, rest)
	}
	return path
}
