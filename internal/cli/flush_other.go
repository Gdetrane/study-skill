//go:build !linux && !darwin

package cli

// flushInput does nothing where the terminal's input queue cannot be
// flushed; input already read is still discarded.
func flushInput(uintptr) {}
