//go:build !race

package core

// raceEnabled: the tests run without -race. See race_on_test.go.
const raceEnabled = false
