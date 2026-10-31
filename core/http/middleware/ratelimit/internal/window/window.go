// Package window computes exact sliding-counter usage and recovery intervals.
package window

import (
	"math/bits"
	"time"
)

// Counter holds nonnegative fixed-window counts at one instant.
type Counter struct {
	// previous and current are the adjacent fixed-window counts.
	previous, current int64
	// elapsed is the time since the current window began.
	elapsed time.Duration
	// length is the fixed-window duration.
	length time.Duration
}

// New captures validated counts and timing without changing their precision.
func New(previous, current int64, elapsed, length time.Duration) Counter {
	return Counter{previous: previous, current: current, elapsed: elapsed, length: length}
}

// Refund removes the denied request's increment from recovery calculations.
func (window *Counter) Refund() {
	window.current--
}

// Remaining subtracts current and upward-rounded previous usage from limit.
// The early guard prevents underflow with two very large stored counters.
func (window Counter) Remaining(limit int64) int64 {
	remaining := limit - window.current

	if remaining < 0 {
		return -1
	}

	previous := ceilRatio(window.previous, int64(window.length-window.elapsed), int64(window.length))

	return remaining - previous
}

// Until returns the wait until usage is at most target without more requests.
// Recovery can cross into the next window, where current usage decays.
func (window Counter) Until(target int64) time.Duration {
	if window.Remaining(target) >= 0 {
		return 0
	}

	if window.current <= target {
		decay := window.previous - (target - window.current)
		elapsed := ceilRatio(int64(window.length), decay, window.previous)

		return time.Duration(elapsed) - window.elapsed
	}

	decay := ceilRatio(int64(window.length), window.current-target, window.current)

	return window.length - window.elapsed + time.Duration(decay)
}

// ceilRatio returns ceil(value*numerator/denominator) with a 128-bit product.
// Inputs are nonnegative, denominator is positive, and numerator <= denominator.
func ceilRatio(value, numerator, denominator int64) int64 {
	high, low := bits.Mul64(uint64(value), uint64(numerator))
	quotient, remainder := bits.Div64(high, low, uint64(denominator))

	if remainder != 0 {
		quotient++
	}

	return int64(quotient)
}
