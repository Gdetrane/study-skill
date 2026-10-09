//go:build !linux

package pluginclient

import "os"

// execNames gives what to start a plugin by. Only Linux can run a file that
// is already open, by its descriptor under /proc/self/fd. macOS has no
// /proc, its /dev/fd names a descriptor for reading and not for running, and
// it has no fexecve. So here the program is started by the path it is
// registered by, and in the folder's path, after a last look that both
// still name the files that were opened and checked (byName).
func execNames(program *os.File, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	return byName(program, programPath, dir, dirPath)
}
