// Package errormap provides middleware that translates errors returned by
// handlers into HTTP problems.
//
// Arkoren's own errors that carry HTTP meaning are already [problem.Details].
// Domain and contract sentinels such as database.ErrNoRows or
// storage.ErrNotFound are plain errors, so the router renders them as 500.
// errormap is where an application decides what they mean over HTTP:
//
//	r.Use(errormap.Middleware(
//		errormap.Map(database.ErrNoRows, problem.NotFound()),
//		errormap.Map(storage.ErrNotFound, problem.NotFound()),
//		errormap.Func(request.ValidationProblem),
//	))
//
// Mappings run in order and the first one that handles the error wins. Map
// keeps the original error in the replacement's chain, so logging and tracing
// still see the cause; Func mappings own their cause preservation.
// A mapping that returns nil or its input unchanged, such as
// request.ValidationProblem for other errors, passes the error on to the next one.
//
// Unchanged detection compares values and retained references, not error
// categories. A derived problem with different detail, headers, or cause is
// handled even when errors.Is matches the input. See [Mapping] for limits of
// detecting unchanged value errors; returning nil always declines a mapping.
// Middleware copies the ordered mapping slice at construction, but functions
// and their captured values remain shared.
//
// # Placement
//
// Register errormap first with Use, so it wraps every later middleware and
// sees the errors they return. Errors the router produces for unmatched
// routes (404) and wrong methods (405) pass through router-wide middleware,
// including this mapper. Middleware attached only to a matched group or route
// does not wrap unmatched requests on the parent router.
package errormap
