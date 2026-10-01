package checkpoint

import "testing"

// SetTestHook runs f at Take's named points ("locked", "update-ref") until the
// test ends.
func SetTestHook(t *testing.T, f func(point string)) {
	testHook = f
	t.Cleanup(func() { testHook = nil })
}

// CanPin reports whether git is pinned to the opened repository through
// /proc/self/fd on this system.
func CanPin() bool { return canPin() }
