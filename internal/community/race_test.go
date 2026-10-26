//go:build race

package community

// raceEnabled: built with -race, which slows regexp matching about 15x.
const raceEnabled = true
