package hatchetworkflow

import (
	"math"
	"time"
)

// stepGrace lets a cooperative step report its own timeout before the engine's
// whole-second backstop expires. Neither native timeout nor local cancellation
// can forcibly terminate a Go callback that ignores its context.
const stepGrace = time.Second

// stepTimeout adds reporting grace to a validated finite application timeout.
// Registration rejects unlimited policies before this native conversion.
func stepTimeout(d time.Duration) time.Duration {
	return wholeSeconds(d + stepGrace)
}

// wholeSeconds rounds d up to whole seconds, at least one, because Hatchet
// timeouts have a granularity of one second.
func wholeSeconds(d time.Duration) time.Duration {
	return time.Duration(max(ceilSeconds(d), 1)) * time.Second
}

// ceilSeconds rounds d up to whole seconds. Zero and below give zero.
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}

	return int(math.Ceil(d.Seconds()))
}
