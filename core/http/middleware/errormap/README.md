# Error Map

`arkoren.dev/core/http/middleware/errormap` translates errors returned by handlers into RFC 9457 problem details.
Arkoren's own HTTP errors are already `problem.Details`; domain and contract sentinels such as
`database.ErrNoRows` or `storage.ErrNotFound` are plain errors that the router renders as `500`. Map them once
here instead of in every handler.

On an existing `r` router, map missing rows and objects to HTTP 404. In addition
to this package, import `arkoren.dev/core/service/database`,
`arkoren.dev/core/service/storage`, `arkoren.dev/core/http/request`, and
`arkoren.dev/foundation/problem`:

```go
r.Use(errormap.Middleware(
	errormap.Map(database.ErrNoRows, problem.NotFound()),
	errormap.Map(storage.ErrNotFound, problem.NotFound()),
	errormap.Func(request.ValidationProblem),
))
```

Mappings run in order and the first one that handles the error wins. `Map` wraps the original
error, so logging and tracing still see the cause and `errors.Is` still matches the original sentinel.
Custom `Func` mappings own cause preservation. Register
the middleware first so it sees errors returned by later middleware. The router's
own unmatched 404 and 405 errors pass through router-wide middleware, including
this mapper. Middleware scoped only to a matched group or route does not wrap
an unmatched request.

`errormap.Func(request.ValidationProblem)` renders a raw `validate.Errors` as the `422` validation problem. A
mapping that returns nil or its input unchanged does not handle the error, so it works in any position.
Unchanged detection compares values and retained references, not `errors.Is` categories: a derived
problem with different detail, headers, or cause is a replacement even if its type, status, and title match.

Return nil when you need to decline without relying on equality. Comparable errors use Go equality;
non-comparable structs and arrays compare their fields and elements recursively. Maps retain storage
identity, and slices retain backing-storage, length, and capacity identity rather than comparing contents.
Separate value errors with equal fields are indistinguishable; non-nil functions and NaNs cannot compare
equal. Pointer errors preserve identity even when their fields contain those values. Do not mutate a
mapping's input: the middleware does not snapshot errors before invoking mappings.

The middleware copies the ordered mapping slice at construction, so changing the caller's slice cannot
replace published mappings. Functions and their captured values remain shared.

See the [error handling guide](../../../../cmd/arkoren/skills/arkoren/references/guides/http-fundamentals/error-handling.md)
for application error responses.
