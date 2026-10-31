package client

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff returns a delay function for [Builder.Backoff]: exponential with
// full jitter. The delay before the first retry (attempt 2) is drawn
// uniformly from [0, initial), and the bound is multiplied by factor for each
// later retry and capped at maximum. The argument order matches
// queue.JobBuilder.Backoff.
//
// Backoff never panics or fails. A negative initial or maximum counts as zero,
// a zero maximum omits the configured cap, and a factor below one (or NaN)
// counts as one.
// With a zero initial every delay is zero, even when exponential growth would
// overflow. Attempts below two use the first retry's bound. Infinite growth
// saturates at maximum, with an internal bound of 1<<62 nanoseconds for safe
// jitter sampling even when maximum is omitted.
//
// Example:
//
//	httpClient := &http.Client{Transport: client.Transport(nil,
//		func(transport *client.Builder) {
//			transport.Backoff(client.Backoff(50*time.Millisecond, 3, time.Second))
//		})}
//
// A zero initial delay remains zero even with unbounded growth:
//
//	backoff := client.Backoff(0, math.Inf(1), 0)
//	delay := backoff(2048) // zero, without overflow or panic
//	_ = delay
func Backoff(initial time.Duration, factor float64, maximum time.Duration) func(attempt int) time.Duration {
	initial, maximum = max(initial, 0), max(maximum, 0)

	if math.IsNaN(factor) || factor < 1 {
		factor = 1
	}

	return func(attempt int) time.Duration {
		if initial == 0 {
			return 0
		}

		bound := float64(initial) * math.Pow(factor, float64(max(attempt, 2)-2))

		if maximum > 0 {
			bound = min(bound, float64(maximum))
		}

		if bound < 1 {
			return 0
		}

		return time.Duration(rand.Int64N(int64(min(bound, 1<<62))))
	}
}
