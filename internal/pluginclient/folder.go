package pluginclient

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/xdg"
)

// FoldersName is the folder, in Lamplight's state folder, that holds the
// folder of each plugin started by a command.
const FoldersName = plugincontract.DataFoldersName

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

// sandboxed is what a message about a plugin's folder says to whoever runs
// study inside a sandbox, as an agent's shell does: the folder is outside
// the Study home, on purpose, and a sandbox that allows the Study home alone
// refuses it. There are two ways out, and the message gives both.
const sandboxed = "Inside a sandbox, as in an agent's shell, either allow that folder in the sandbox, or run the " +
	"command in a shell of your own"

// makeDataFolder finds the plugin's folder, makes what is missing of it for
// the learner alone to read, and returns its path with every symbolic link
// on the way followed, which is what the plugin is told. For a plugin that
// is started to index (writes), it also checks that the folder can be
// written in.
//
// The registry's rule decides where it may be: a state folder that is the
// Study home, is inside it or is reached through it is refused, and nothing
// is made there.
func makeDataFolder(guard *pluginregistry.Guard, env pluginregistry.Env, name string, writes bool) (string, error) {
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
			"you may pass through, and that you can write in your state folder. " + sandboxed}
	}
	defer folder.Close()
	// The folder is the learner's alone: an index holds the text of their
	// books, and whoever can write it writes what a search finds. A folder
	// that was there already is made so, and one that cannot be, as one
	// another user made in a shared state folder cannot, is not used.
	if err := private(folder); err != nil {
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "the folder Knowledge base plugin " + name +
			" keeps its index in, " + resolved + ", " + err.Error() + ". An index holds the text of your books, and whoever " +
			"writes it writes what a search finds: remove the folder so that study makes it anew, or set XDG_STATE_HOME " +
			"to a folder that is yours alone"}
	}
	// A plugin asked to index writes its index here. Where it cannot, it
	// could only say that its index cannot be written, each plugin in its
	// own words, and the one who reads that would not learn that a sandbox
	// decided it. Only writing tells: a sandbox refuses what the folder's
	// mode allows.
	if writes {
		if err := (*checkWritable.Load())(folder); err != nil {
			return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + name + " cannot keep " +
				"an index: nothing can be written in its folder, " + resolved + " (" + pathReason(err) + "). " + sandboxed +
				". Outside a sandbox, check that the folder is yours to write in"}
		}
	}
	return resolved, nil
}

// checkWritable is how makeDataFolder learns whether a folder can be
// written in. A test replaces it, to have a folder refuse as a sandbox
// refuses: no folder of the test's own does.
var checkWritable atomic.Pointer[func(folder *os.File) error]

func init() {
	check := writable
	checkWritable.Store(&check)
}

// probeName is how the name of the file writable makes and removes begins.
// A plugin passes over a name in its folder that begins so.
const probeName = ".lamplight-write-check-"

// private makes an open folder one that only its owner reads and writes,
// and fails, with what is wrong, when it is not one afterwards.
func private(folder *os.File) error {
	info, err := folder.Stat()
	if err != nil {
		return errors.New("cannot be looked at (" + pathReason(err) + ")")
	}
	if info.Mode().Perm() == 0o700 {
		return nil
	}
	changed := chmodFolder(folder, 0o700)
	if now, err := folder.Stat(); err == nil {
		if info = now; info.Mode().Perm()&0o077 == 0 {
			return nil
		}
	}
	if changed != nil {
		return fmt.Errorf("can be read or written by others (mode %04o), and study cannot change that (%s)",
			info.Mode().Perm(), pathReason(changed))
	}
	return fmt.Errorf("can be read or written by others (mode %04o), and stays so when study changes it", info.Mode().Perm())
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
