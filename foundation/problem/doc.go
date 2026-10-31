// Package problem implements RFC 9457 Problem Details for HTTP APIs.
//
// The package centers on [Details], an error-compatible value type that can be
// served directly as an HTTP response. Construct with [New] and customize an
// occurrence with [Details.Derive]; standard members have read-only accessors.
//
//	failure := problem.New(func(details *problem.Builder) {
//		details.Status(http.StatusNotFound)
//		details.Title("Order not found")
//	})
//	_ = failure.Status()
//
// # Content negotiation
//
// Problem responses negotiate application/problem+json, application/json, and
// text/plain in that server order. When no syntactically valid Accept value is
// supplied, that order applies. For valid headers, the most-specific matching
// range controls each representation and q=0 excludes it. When every
// representation is explicitly excluded, [Details.ServeHTTP] returns 406 without
// rendering the original problem. If JSON encoding an extension fails, it renders
// plain text only when text/plain is acceptable; otherwise it returns 406.
// Malformed quality values invalidate their range, rather than defaulting its
// quality to one. Media parameters are requirements: text/plain;charset=utf-8
// matches the text renderer, while other charsets and unprovided JSON profiles
// do not. Parameterized matches outrank otherwise equal unparameterized ranges.
// Responses retain existing Vary tokens and add Accept unless already covered.
//
// # Immutability
//
// [New] and [Details.Derive] invoke callbacks directly, synchronously, once in
// argument order on one temporary [Builder]. Omitted or expanded empty callback
// slices supply no edits; supplied nil callbacks panic. Both publication and
// derivation detach outer extension maps and header slices, so a retained builder
// cannot edit published values. Arbitrary nested extension values remain shared.
//
// Application code should define [Details] values as package-level variables
// constructed with [New], then derive per-request instances with
// [Details.Derive]. [Builder.Error] retains a cause without serializing it.
// [Details.Extension] distinguishes an absent member from a stored nil value;
// [Details.Extensions] returns a copy of the outer map.
//
// # Defaults and decoding
//
// [New] resolves zero status to 500, empty type to about:blank, then an empty
// title from the final status. [Details.Derive] preserves stored fields without
// normalization, including inherited titles when status changes. JSON decoding
// preserves absent status as zero, replaces old state only on success, and clears
// previous causes and headers on success. Plain encoding omits zero status;
// HTTP rendering completes defaults and emits the effective status in the body.
// Stored nonzero statuses are not validated by construction or decoding. Only
// ServeHTTP replaces statuses outside net/http's 100–999 range with 500; plain
// JSON, accessors, Defaulted, and identity matching retain the original value.
//
// # Matching
//
// [errors.Is] matches a [Details] against a template with the same Type, Status
// and Title; Detail, extension members, headers and the wrapped cause are
// ignored. See [Details.Is].
//
//	occurrence := ErrUserNotFound.Derive(func(details *problem.Builder) {
//		details.Detail("no user 42")
//	})
//	errors.Is(occurrence, ErrUserNotFound) // true
//
// # Request response context
//
// Middleware can attach a [ResponseContext] for client-facing RFC 9457
// extension members and HTTP response headers. [Details.ServeHTTP] snapshots
// it without modifying the original [Details] value. Explicit [Builder.Extension]
// extension members and [Details] header builders take precedence over matching
// response-context fields. Renderers always own Content-Type.
// Snapshot independence applies to outer property maps and header slices, not
// arbitrary nested property values. Coordinate nested mutation with encoding.
//
// Example
//
//	responseContext := problem.NewResponseContext()
//	responseContext.SetProperty("request_id", "123")
//	responseContext.AddHeader("Vary", "Accept-Language")
//	request = request.WithContext(problem.NewContext(request.Context(), responseContext))
//
//	ErrNotFound.ServeHTTP(w, request)
package problem
