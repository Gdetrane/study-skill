package pluginregistry

import (
	"os"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry/internal/regfile"
)

// What starting a plugin needs: its program, in the folder that holds it;
// the folder to start it in; and the question the registry asks of a
// program, asked of whatever else the plugin is started with. study starts
// a plugin outside the agent's sandbox, so nothing it is started with may be
// the agent's to change.

// Command is the program of a plugin registered by a command, opened and
// checked at one moment, to start it.
type Command struct {
	// Program is the program: the file that was checked.
	Program *os.File
	// Folder is the folder that really holds the program, open, and Name
	// the program's name in it: every symbolic link on the way was followed
	// when it was checked, so the name is the file's own and no link.
	Folder *os.File
	Name   string
}

// Close closes the program and its folder.
func (c *Command) Close() {
	_ = c.Program.Close()
	_ = c.Folder.Close()
}

// OpenCommand opens the program of a plugin registered by a command, as
// OpenProgram does, and the folder that holds it.
//
// Start the program through what is returned and never through the path the
// plugin is registered by: by its name in the open folder, named by the
// folder's descriptor (/proc/self/fd on Linux), or by the open file itself.
// Either way no folder on the way to the program is looked up again, so no
// name there can come to lead elsewhere between the check and the start.
// The caller closes the Command.
func OpenCommand(env Env, p Plugin) (*Command, error) {
	if len(p.Command) == 0 {
		return nil, regfile.Errorf(regfile.CodeInvalidArgument, "Knowledge base plugin %s is reached by URL: it has no "+
			"program to start", p.Name)
	}
	w := regfile.NewWalker(env.StudyHome)
	defer w.Close()
	program, err := w.OpenProgramIn(p.Command[0])
	if err != nil {
		return nil, err
	}
	if why := problem(w, env, p); why != "" {
		program.Close()
		return nil, regfile.Errorf(regfile.CodeFailedPrecondition, "Knowledge base plugin %s is not started: %s", p.Name, why)
	}
	return &Command{Program: program.File, Folder: program.Folder, Name: program.Name}, nil
}

// OpenFolder opens the registry's folder, Lamplight's configuration folder,
// to start a plugin from: a plugin is started there, and never in the Topic
// or the Study home study was started in, where a command such as
// python -m would load what the agent left (ADR-0012).
//
// The folder is found and checked as Read finds it, at that moment: it is
// not the Study home, not inside it and not reached through it. It returns
// the folder open, and its path with every symbolic link followed, which is
// for messages. Start the plugin in the folder that is returned, by its
// descriptor where the system can name one (/proc/self/fd on Linux), not in
// the path again. The caller closes the file.
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
	file, err := dir.File()
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

// Outside reports whether an absolute path is known to stay outside the
// Study home at every hop. What cannot be decided is not outside: a path
// that cannot be followed, because study may not pass through a folder on
// the way or because the Study home itself cannot be looked at, may lead
// anywhere. A path that ends in something that is not there yet is outside
// when what is there of it is.
func (g *Guard) Outside(path string) bool { return g.walker.Follow(path) == nil }

// Follow is Outside with why not: nil for a path known to stay outside the
// Study home at every hop, an error IsInside tells for one that leads
// through it, and any other error for one that could not be followed, of
// which nothing is known. It is for whoever has to say which.
func (g *Guard) Follow(path string) error { return g.walker.Follow(path) }

// CheckProgram checks a program that starting a plugin would run without
// its being the plugin's own, such as the interpreter a script's first line
// names: it is a regular file that the user study runs as may run, reached
// without going through the Study home at any hop.
func (g *Guard) CheckProgram(path string) error { return g.walker.CheckProgram(path) }

// OpenProgram checks a program as CheckProgram does and returns it open: the
// file the walk looked at, and not whatever its path names a moment later.
// It is for reading the program, to see whether it is a script. The caller
// closes it.
func (g *Guard) OpenProgram(path string) (*os.File, error) { return g.walker.OpenProgram(path) }

// MakeFolder finds the folder at an absolute path outside the Study home,
// makes what is missing of it for the learner alone to read, and returns it
// open, with its path as every symbolic link on the way leads. A path that
// is the Study home, is inside it or is reached through it is refused, as
// IsInside tells, and nothing is made. The caller closes the folder.
func (g *Guard) MakeFolder(path string) (*os.File, string, error) { return g.walker.MakeFolder(path) }

// IsInside reports whether an error of a Guard says that a path leads
// through the Study home.
func IsInside(err error) bool { return regfile.IsInside(err) }
