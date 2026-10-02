//go:build race

package e2e

// raceEnabled: the tests run with -race, so the walkthrough builds study
// with it too and reuses the packages the test build compiled.
const raceEnabled = true
