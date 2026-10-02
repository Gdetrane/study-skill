//go:build !unix

package core

import "os/exec"

// ownProcessGroup only bounds the wait on systems without process groups;
// Lamplight supports Linux and macOS.
func ownProcessGroup(cmd *exec.Cmd) { cmd.WaitDelay = checkGrace }

func stopGroup(int) bool { return false }
