package pluginregistry

import (
	"os"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regfile"
)

// What starting a plugin needs besides its program (OpenProgram): the
// folder to start it in, and the same question the registry asks of a
// program, asked of whatever else the plugin is started with. study starts a
// plugin outside the agent's sandbox, so nothing it is started with may be
// the agent's to change.

// OpenFolder opens the registry's folder, Lamplight's configuration folder,
// to start a plugin from: a plugin is started there, and never in the Topic
// or the Study home study was started in, where a command such as
// python -m would load what the agent left (ADR-0012).
//
// The folder is found and checked as Read finds it, at that moment: it is
// not the Study home, not inside it and not reached through it. It returns
// the folder open for reading, and its path with every symbolic link
// followed, which is for messages and for the plugin's PWD. Start the plugin
// in the folder that is returned, by its descriptor where the system can
// name one (/proc/self/fd on Linux), not in the path again. The caller
// closes the file.
//
// The folder is opened to be a working folder and nothing more: what
// changes the registry in it stays in the registrar package.
func OpenFolder(env Env) (*os.File, string, error) {
	folder, err := regfile.Open(env)
	if err != nil {
		return nil, "", err
	}
	defer folder.Close()
	dir := folder.Dir()
	if dir == nil {
		return nil, "", regfile.Errorf(regfile.CodeFailedPrecondition, "Lamplight's configuration folder, %s, which holds the "+
			"Knowledge base plugin registry, is not there, so no Knowledge base plugin is registered and none is started",
			folder.Path)
	}
	_, path := folder.Nearest()
	file, err := dir.Open(".")
	if err != nil {
		return nil, "", &regfile.Error{Code: regfile.CodeFailedPrecondition, Err: err, Message: "study cannot open Lamplight's " +
			"configuration folder, " + folder.Path + ", to start a Knowledge base plugin from it (" + regfile.Reason(err) +
			"): check that the folder is yours to read"}
	}
	return file, path, nil
}

// Guard tells what the agent could change: what is the Study home, is
// inside it, or is reached through it at any hop. It decides as the
// registry does for a plugin's program, by file identity and one hop of a
// path at a time, never by name. The caller closes it.
type Guard struct {
	walker *regfile.Walker
}

// NewGuard returns a Guard for the Study home this study uses.
func NewGuard(env Env) *Guard { return &Guard{walker: regfile.NewWalker(env.StudyHome)} }

// Close lets go of the Study home.
func (g *Guard) Close() { g.walker.Close() }

// Inside reports whether an absolute path leads through the Study home. A
// path that cannot be followed for another reason does not: what stops the
// walk stops whoever uses the path too. When the Study home cannot be
// looked at, every path counts as inside it.
func (g *Guard) Inside(path string) bool { return g.walker.Inside(path) }

// CheckProgram checks a program that starting a plugin would run without
// its being the plugin's own, such as the interpreter a script's first line
// names: it is a regular file that can be run, reached without going
// through the Study home at any hop.
func (g *Guard) CheckProgram(path string) error { return g.walker.CheckProgram(path) }
