package pluginclient

import (
	"encoding/json"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

// WithoutOfAStart is Without for a text that is the start of a longer one:
// what it ends in is left out too, when that may be the start of a part.
func WithoutOfAStart(text, address string) string { return partsOf(address).without(text, true) }

// ChildEnviron is the environment a plugin is started with, of the one
// study has.
func ChildEnviron(environ []string, outside func(path string) bool, dataFolder string) []string {
	return childEnviron(environ, outside, dataFolder)
}

// Values is how many values study counts in a message.
func Values(raw []byte) int { return values(raw) }

// Payload is the JSON object study takes from a tool's result.
func Payload(res *mcp.CallToolResult) (json.RawMessage, bool) { return payload(res) }

// SetCheckWritable replaces how study learns whether a plugin's folder can
// be written in, and returns a function that restores it: for a folder that
// refuses as a sandbox refuses, which no folder of a test's own does.
func SetCheckWritable(check func(folder *os.File) error) func() {
	old := checkWritable.Swap(&check)
	return func() { checkWritable.Store(old) }
}

// ProbeName is how the name of the file study makes in a plugin's folder,
// and removes, to learn whether it can be written in begins.
const ProbeName = probeName

// WithDefaults is the options a plugin is started with, given the ones a
// caller set: each time that was left out is the usual one.
func WithDefaults(o Options) Options { return o.withDefaults() }
