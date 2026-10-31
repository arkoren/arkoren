# HTTP Response Helpers

Use `arkoren.dev/core/http/response` to write a handler's final response. Once
status or body bytes are accepted, a returned error cannot replace the response.
Template helpers buffer execution before commitment. On success, `StringTemplate`
sets `text/plain; charset=utf-8` and `HTMLTemplate` sets `text/html; charset=utf-8`,
replacing any existing `Content-Type`. Execution failures leave headers unchanged.

For streamed responses:

- SSE normalizes CRLF and bare CR to LF. Trailing line breaks produce empty
  `data:` fields so EventSource retains the complete normalized payload.
- Iterator byte slices are copied before `yield` returns. Producer panics are
  re-raised on the request goroutine after the producer exits.
- Available flush failures are returned, including an unsupported flush. Even
  the initial flush follows response commitment. Errorless flushers cannot
  report their failures.
- Iterator producers are joined, never abandoned. A producer blocked outside
  `yield` must observe cancellation/shutdown or have another bounded exit.
  A write or flush failure does not cancel its captured request context.

See the [package API and examples](https://pkg.go.dev/arkoren.dev/core/http/response)
for stream capability requirements and per-write deadline behavior.
