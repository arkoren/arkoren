// Package measurement supplies allocation measurements and race-mode metadata
// for tests. It is not imported by production queue execution.
package measurement

import "testing"

// AllocsPerRun measures allocation cost with testing's warmup and serial runner.
// RaceEnabled lets callers retain their explicit pool-variance tolerance.
func AllocsPerRun(runs int, operation func()) float64 {
	return testing.AllocsPerRun(runs, operation)
}
