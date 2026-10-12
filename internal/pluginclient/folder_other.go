//go:build !linux && !darwin

package pluginclient

import "os"

// chmodFolder changes the mode of a folder that is open.
func chmodFolder(folder *os.File, mode os.FileMode) error { return folder.Chmod(mode) }

// writable reports whether a file can be made in a folder that is open.
// This system gives no way to make one beside an open folder, and making it
// by the folder's path would write wherever that path leads by now: so
// nothing is asked, and a plugin that cannot write its index says so itself.
func writable(*os.File) error { return nil }
