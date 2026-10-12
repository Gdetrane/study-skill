//go:build !unix

package plugintest

import (
	"context"
	"os"
	"time"
)

// Lamplight supports Linux and macOS. Elsewhere the test plugin only
// speaks the contract: it cannot be made to misbehave with signals, and
// tells nothing of how it was started.

func ignoreStops() {}

func stopsCancel(ctx context.Context, _ bool) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

func stopDead() { forever() }

func streamOf(*os.File) Stream { return Stream{} }

func leadsSession() bool { return false }

func hasTerminal() bool { return false }

func inheritedFiles() map[int]string { return nil }

func processName() string { return "" }

// Gone reports whether a process is gone: here, it cannot tell.
func Gone(int, time.Duration) bool { return false }

// Kill kills a process a test left behind.
func Kill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
