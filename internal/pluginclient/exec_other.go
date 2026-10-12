//go:build !linux

package pluginclient

import (
	"os"

	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

// execNames gives what to start a plugin by. Only Linux can name a file
// through a folder that is already open, by the folder's descriptor under
// /proc/self/fd. macOS has no /proc, its /dev/fd names a descriptor for
// reading and not a folder to look a name up in, and it has no fexecve. So
// here the program is started by the path it is registered by, and in the
// folder's path, after a last look that both still name what was opened
// and checked (byName).
func execNames(command *pluginregistry.Command, programPath string, dir *os.File, dirPath string) (path, cwd string, inherited []*os.File, err error) {
	return byName(command, programPath, dir, dirPath)
}
