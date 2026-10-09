package pluginclient

import "time"

// SetBeforeStart installs a hook that runs after a plugin's program and the
// registry's folder were opened and checked and before the program is
// started, and returns a function that removes it.
func SetBeforeStart(hook func()) func() {
	beforeStart = hook
	return func() { beforeStart = nil }
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
