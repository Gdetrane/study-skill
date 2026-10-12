//go:build race

package core

// raceEnabled: the tests run with -race, which makes the core many times
// slower. A test that times work allows for it.
const raceEnabled = true
