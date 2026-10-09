package pluginclient

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The folder study gives a plugin started by a command, to keep its index
// in (ADR-0012): .lamplight/knowledge-base-plugins/<name> in the Study home,
// this computer's state, never synced, and rebuilt by indexing again.
const (
	// localFolder is the Study home's folder of this computer's state, as
	// the core names it.
	localFolder = ".lamplight"
	// FoldersName is the folder in it that holds each plugin's own.
	FoldersName = "knowledge-base-plugins"
)

// DataFolder is the path of the folder study gives the plugin called name,
// whether or not it is there yet.
func DataFolder(studyHome, name string) string {
	return filepath.Join(studyHome, localFolder, FoldersName, name)
}

// makeDataFolder makes the plugin's folder when it is not there, for the
// learner alone to read: an index holds the text of their books.
//
// The folder is in the Study home, where the agent writes, and the plugin
// is told its name. So each folder on the way must be a folder and no
// symbolic link when the plugin is started: a link there would have a
// plugin, started outside the agent's sandbox, write its index wherever the
// link leads. That is a look at one moment and no guarantee, since the
// plugin opens the name again for as long as it runs.
func makeDataFolder(studyHome, name string) (string, error) {
	path := DataFolder(studyHome, name)
	fail := func(what string, err error) (string, error) {
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "study cannot make the folder Knowledge base plugin " +
			name + " keeps its index in, " + path + ": " + what + " (" + pathReason(err) + "). Check that you can write in " +
			"the Study home"}
	}
	if err := os.MkdirAll(studyHome, 0o755); err != nil {
		return fail("the Study home, "+studyHome+", cannot be made", err)
	}
	home, err := os.OpenRoot(studyHome)
	if err != nil {
		return fail("the Study home, "+studyHome+", cannot be opened", err)
	}
	defer home.Close()
	rel := ""
	for i, part := range []string{localFolder, FoldersName, name} {
		rel = filepath.Join(rel, part)
		perm := fs.FileMode(0o700)
		if i == 0 {
			perm = 0o755 // .lamplight, as the core makes it
		}
		if err := home.Mkdir(rel, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			return fail(filepath.Join(studyHome, rel)+" cannot be made", err)
		}
		info, err := home.Lstat(rel)
		if err != nil {
			return fail(filepath.Join(studyHome, rel)+" cannot be looked at", err)
		}
		if !info.IsDir() {
			return "", errorf(CodeFailedPrecondition, "%s is not a folder: it is a symbolic link, or a file. study gives "+
				"Knowledge base plugin %s the folder %s to keep its index in, and follows no link to it, because a plugin "+
				"is started outside your agent's sandbox and your agent can write in the Study home. Move it away, and "+
				"index the Topic's Sources again", filepath.Join(studyHome, rel), name, path)
		}
	}
	return path, nil
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
