//go:build linux

package pluginclient

import (
	"os"
	"strconv"
	"sync"
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
// On Linux both are the files that were opened and checked, named by their
// descriptors, so nothing is resolved by name again and no name can come to
// lead elsewhere between the check and the start:
//
//   - The program is inherited as descriptor 3 and run as /proc/self/fd/3,
//     which the child resolves once its descriptors are in place.
//   - The working folder is named by the descriptor it has in this process.
//     The child changes folder before it rearranges its descriptors, while
//     its table is still a copy of this one, so the number means the same
//     folder there. (A test starts a plugin after its folder's name was
//     made to lead elsewhere, and fails if the Go runtime ever changes that
//     order.) The folder itself is not inherited.
//
// Without /proc the names are used, as on other systems.
func execNames(program *os.File, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	if !procFD() {
		return byName(program, programPath, dir, dirPath)
	}
	return "/proc/self/fd/3", "/proc/self/fd/" + strconv.Itoa(int(dir.Fd())), []*os.File{program}, nil
}
