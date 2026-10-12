package pluginclient

import "time"

// SetBeforeStart installs a hook that runs after a plugin's program and the
// registry's folder were opened and checked and before the program is
// started, and returns a function that removes it.
func SetBeforeStart(hook func()) func() {
	beforeStart.Store(&hook)
	return func() { beforeStart.Store(nil) }
}

// SetUnstick changes how long the MCP library has to return once a deadline
// passed, and returns a function that restores the usual time.
func SetUnstick(d time.Duration) func() {
	old := unstick
	unstick = d
	return func() { unstick = old }
}

// Clean is how text a plugin wrote is cleaned for a message.
func Clean(s string, max int) string { return clean(s, max) }

// CleanEnd is how the end of what a plugin wrote is kept for a message.
func CleanEnd(s string, max int) string { return cleanEnd(s, max) }

// Without is a text without the parts of a registered address that may be
// a key.
func Without(text, address string) string { return without(text, address) }

// ChildEnviron is the environment a plugin is started with, of the one
// study has.
func ChildEnviron(environ []string, outside func(path string) bool, dataFolder string) []string {
	return childEnviron(environ, outside, dataFolder)
}
