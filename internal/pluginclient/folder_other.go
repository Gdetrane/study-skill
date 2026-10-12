//go:build !linux && !darwin

package pluginclient

import "os"

// chmodFolder changes the mode of a folder that is open.
func chmodFolder(folder *os.File, mode os.FileMode) error { return folder.Chmod(mode) }
