package ratelimit

import (
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrRateLimited is returned when a request exceeds its configured limit.
//
// Example:
//
//	failure := ratelimit.ErrRateLimited.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrRateLimited = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/middleware/ratelimit:rate-limited")
	details.Title("Too Many Requests")
	details.Detail("Rate limit exceeded. Please slow down and retry later.")
	details.Status(http.StatusTooManyRequests)
})

// ErrorResponse is returned when a request exceeds Limit. A completely zero
// problem selects ErrRateLimited. A nonzero problem is preserved even when its
// Status is zero: the HTTP renderer completes absent members, including status
// 500, without changing the configured identity, detail, headers, or extensions.
// Denial and event publication do not depend on that rendered status.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	failure := (problem.Details{}).Derive(func(details *problem.Builder) {
//		details.Type("urn:example:budget-exhausted")
//		details.Title("Budget exhausted")
//	})
//	routes.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.ErrorResponse(failure)
//	}))
func (limiter *Builder) ErrorResponse(response problem.Details) *Builder {
	limiter.settings.ErrorResponse = response

	return limiter
}
