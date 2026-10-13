//go:build !unix

package cli

// mayRun reports whether whoever runs study may run the file at path. See
// mayrun_unix.go: here the file's mode is all there is to go by.
func mayRun(string) bool { return true }
