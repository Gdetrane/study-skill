package pluginclient

import (
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/xdg"
)

// FoldersName is the folder, in Lamplight's state folder, that holds the
// folder of each plugin started by a command.
const FoldersName = "knowledge-base-plugins"

// DataFolder is the path of the folder study gives the plugin called name
// to keep its index in, whether or not it is there yet:
// knowledge-base-plugins/<name> in Lamplight's state folder, which is
// $XDG_STATE_HOME/lamplight, or ~/.local/state/lamplight when
// XDG_STATE_HOME is not set or is not an absolute path (internal/xdg). It
// fails when that default is needed and there is no home folder to put it
// under.
//
// It is this computer's state, never synced, and rebuilt by indexing again.
// It is outside the Study home, on purpose. The agent writes in the Study
// home, and a plugin runs outside the agent's sandbox and is told this
// folder by its name: a folder in the Study home the agent could exchange
// for a link, at any moment of the plugin's life, to have the plugin write
// where the link leads; and an index the agent can write is one whose
// Passages the agent can write, which a quote is later checked against.
func DataFolder(getenv func(string) string, name string) (string, error) {
	state, err := xdg.StateHome.Dir(getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "lamplight", FoldersName, name), nil
}

// makeDataFolder finds the plugin's folder, makes what is missing of it for
// the learner alone to read, and returns its path with every symbolic link
// on the way followed, which is what the plugin is told.
//
// The registry's rule decides where it may be: a state folder that is the
// Study home, is inside it or is reached through it is refused, and nothing
// is made there.
func makeDataFolder(guard *pluginregistry.Guard, env pluginregistry.Env, name string) (string, error) {
	path, err := DataFolder(env.Getenv, name)
	if err != nil {
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "study cannot find the state folder Knowledge base " +
			"plugin " + name + " keeps its index in (" + err.Error() + "): set HOME, or XDG_STATE_HOME, to an absolute path"}
	}
	folder, resolved, err := guard.MakeFolder(path)
	switch {
	case pluginregistry.IsInside(err):
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "the folder Knowledge base plugin " + name +
			" keeps its index in, " + path + ", is inside the Study home, " + env.StudyHome + ", or is reached through it. " +
			"Your agent can write there, and a plugin runs outside the agent's sandbox, so study gives a plugin no folder " +
			"there: set XDG_STATE_HOME to a folder outside the Study home that no link in the Study home leads to"}
	case err != nil:
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "study cannot make the folder Knowledge base plugin " +
			name + " keeps its index in, " + path + " (" + pathReason(err) + "). Check that every folder on the way is one " +
			"you can read, and that you can write in your state folder"}
	}
	defer folder.Close()
	// The folder is the learner's alone: an index holds the text of their
	// books.
	if info, err := folder.Stat(); err == nil && info.Mode().Perm() != 0o700 {
		_ = folder.Chmod(0o700)
	}
	return resolved, nil
}

// pathReason is an operating system error without the path it names: the
// message already says which path.
func pathReason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}
