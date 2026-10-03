//go:build !unix

package cli

import (
	"os"
	"os/exec"
	"time"
)

// Windows packages come later (see the design's Scope); until then study
// setup does not lock its record there.
func flockTry(*os.File) (bool, error) { return true, nil }

func flockRelease(*os.File) error { return nil }

// agentProcessGroup only bounds the wait on systems without process groups;
// Lamplight supports Linux and macOS.
func agentProcessGroup(cmd *exec.Cmd) { cmd.WaitDelay = 2 * time.Second }

func killAgentGroup(*exec.Cmd) {}
