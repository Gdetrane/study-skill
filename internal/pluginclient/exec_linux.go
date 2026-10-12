//go:build linux

package pluginclient

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// procFD reports whether an open file can be named by its descriptor, under
// /proc/self/fd, as internal/checkpoint names the folders it hands git.
var procFD = sync.OnceValue(func() bool {
	info, err := os.Stat("/proc/self/fd")
	return err == nil && info.IsDir()
})

// execNames gives what to start a plugin by: the program's path, its
// working folder, and the files it inherits after its three pipes.
//
// On Linux both are named through folders that were opened when they were
// checked, by the folders' descriptors, so no folder on the way to either is
// looked up again and none can come to lead elsewhere between the check and
// the start:
//
//   - The folder that holds the program is inherited as descriptor 3, and
//     the program is run by its name in it: /proc/self/fd/3/<name>, which
//     the child resolves once its descriptors are in place. The name is the
//     file's own in that folder and no link (the registry's walk followed
//     every link), and a last look checks that it still is the file that
//     was checked. Only someone who can write the program's own folder can
//     put another file under the name after that look, and whoever can do
//     that could have done it before the check.
//
//     The open file itself could be run instead, as /proc/self/fd/3. That
//     pins the file and not only its folder, and it was how this began. But
//     the system then names the process 3, and a script is handed
//     /proc/self/fd/3 as its own name, with nothing beside it: a wrapper
//     that starts the program next to itself works in a terminal and not
//     under study. Run by its name in its folder, a program is named as it
//     is, and a script finds its neighbours.
//
//   - The working folder is named by the descriptor it has in this process.
//     The child changes folder before it rearranges its descriptors, while
//     its table is still a copy of this one, so the number means the same
//     folder there. (A test starts a plugin after its folder's name was
//     made to lead elsewhere, and fails if the Go runtime ever changes that
//     order.) The working folder itself is not inherited.
//
// Without /proc the names are used, as on other systems.
func execNames(command *pluginregistry.Command, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	if !procFD() {
		return byName(command, programPath, dir, dirPath)
	}
	checked, err := command.Program.Stat()
	if err != nil {
		return "", "", nil, err
	}
	named, err := os.Lstat("/proc/self/fd/" + strconv.Itoa(int(command.Folder.Fd())) + "/" + command.Name)
	if err != nil || !os.SameFile(checked, named) {
		return "", "", nil, fmt.Errorf("%s came to name something else while study was starting the plugin: try again", programPath)
	}
	return "/proc/self/fd/3/" + command.Name, "/proc/self/fd/" + strconv.Itoa(int(dir.Fd())), []*os.File{command.Folder}, nil
}
