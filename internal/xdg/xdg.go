// Package xdg finds the home folder and the base folders of the XDG Base
// Directory Specification, for study and for the programs it starts.
//
// The specification says a path in one of its variables must be absolute,
// and that a relative one is invalid and to be ignored. It is ignored here:
// the default under the home folder is used instead. Used, the folder would
// be wherever study is started, inside a Topic too, and a file the agent
// wrote there would be read as the learner's own configuration.
//
// HOME is read the same way. A relative one is not used, and since it has no
// default to fall back on, whatever needs the home folder fails and says so.
//
// The Knowledge base plugin registry is stricter, and has its own rule: it
// refuses to be read from a configuration folder that is not an absolute
// path (internal/pluginregistry).
package xdg

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Folder is one of the specification's base folders.
type Folder struct {
	// Var is the environment variable that names the folder.
	Var string
	// fallback is the folder under the home folder when Var does not name
	// one.
	fallback string
}

// The base folders study reads.
var (
	ConfigHome = Folder{"XDG_CONFIG_HOME", ".config"}
	DataHome   = Folder{"XDG_DATA_HOME", filepath.Join(".local", "share")}
	StateHome  = Folder{"XDG_STATE_HOME", filepath.Join(".local", "state")}
	CacheHome  = Folder{"XDG_CACHE_HOME", ".cache"}
)

// home is the variable that names the home folder.
const home = "HOME"

// variables are the ones this package reads, in the order Ignored reports
// them.
var variables = []string{home, ConfigHome.Var, DataHome.Var, StateHome.Var, CacheHome.Var}

// Dir returns the folder: what its variable says when that is an absolute
// path, and its default under the home folder when the variable is unset,
// empty or relative. It fails only when the default is needed and the home
// folder cannot be found.
func (f Folder) Dir(getenv func(string) string) (string, error) {
	if dir := getenv(f.Var); filepath.IsAbs(dir) {
		return dir, nil
	}
	h, err := Home(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(h, f.fallback), nil
}

// Home returns the home folder: HOME, or the operating system's answer when
// HOME is unset. A relative HOME is an error, never a folder: the operating
// system is not asked instead, so what is under the home folder is not
// looked for somewhere the caller did not name.
func Home(getenv func(string) string) (string, error) {
	h := getenv(home)
	if h == "" {
		var err error
		if h, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(h) {
		return "", fmt.Errorf("%s is %q, which is not an absolute path", home, h)
	}
	return h, nil
}

// Ignored returns the variables this package reads that are set to a
// relative path, and so are not used: HOME first, then the base folders'.
func Ignored(getenv func(string) string) []string {
	var names []string
	for _, name := range variables {
		if relative(getenv(name)) {
			names = append(names, name)
		}
	}
	return names
}

// Environ returns environ, a list of "key=value" as os.Environ gives it,
// without the variables Ignored would report. It is the environment of a
// program study starts that reads these variables itself and takes a
// relative one as it is, as git does: the program then finds the folders
// study does.
func Environ(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if slices.Contains(variables, name) && relative(value) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// relative reports whether value is set and is not an absolute path.
func relative(value string) bool { return value != "" && !filepath.IsAbs(value) }
