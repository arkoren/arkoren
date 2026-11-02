---
title: Streaming Responses
description: Send live bytes or server-sent events while managing producer cancellation, shutdown, and committed failures.
order: 35
summary:
  - Choose raw streaming or server-sent events for an existing iterator or channel.
  - Stop cooperative producers on cancellation, shutdown, and early consumer exit.
  - Bound supported writes with renewed deadlines and handle failures after commitment.
---

# Streaming Responses

A live progress feed should deliver each update without waiting for the entire
job. Streaming helpers write and flush values as they arrive, rather than
preparing one buffered response. That changes failure handling: the initial
`200 OK` is sent before production finishes, so a later error cannot become a
replacement problem response.

Use these helpers inside an existing Arkoren handler with `w` and `r`. Import
`arkoren.dev/core/http/response`; [Responses](responses.md) owns ordinary buffered
results. Authenticate and authorize access before starting a live feed.

## Choose Event Output

For browser `EventSource` clients, use server-sent events (SSE). Each yielded
value becomes one event. With an existing `iter.Seq2[InvoiceProgress, error]`
named `updates`, define the client-visible shape and stream JSON:

```go
type InvoiceProgress struct {
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
}
```

Pass the iterator directly; the helper handles JSON encoding and event framing:

```go
return response.SSEJSON(w, r, updates)
```

The response uses `text/event-stream` and `Cache-Control: no-cache`. A native
fallible lazy collection can supply the iterator with `updates.Iter()` instead;
that conversion consumes nothing and transfers no cancellation responsibility.
[Streaming Replies](../llm/streaming.md) applies this format to model output.

For pre-encoded event data, `response.SSE` accepts
`iter.Seq2[[]byte, error]`. Each payload line becomes a `data:` field. CRLF and
bare CR normalize to LF, including trailing line breaks; empty payloads emit
empty events. Payload line breaks cannot inject `event:` or `id:` fields.

If the client expects raw bytes instead, use `response.Stream`. With an existing
byte iterator `chunks`, select its actual media type before starting:

```go
w.Header().Set("Content-Type", "text/plain; charset=utf-8")
return response.Stream(w, r, chunks)
```

Without an explicit type, raw streaming uses `application/octet-stream`.
`Stream` and `SSE` copy each yielded byte slice before `yield` returns, so the
producer may reuse its buffer. `SSEJSON` completes each value's encoding before
releasing the yield callback.

## Stop Iterator Work

Iterator helpers start a producer goroutine and always wait for it to exit.
When consumption stops, `yield` returns false: return immediately without
yielding again, including from cleanup. A blocked operation outside `yield`
must also have a way to finish, or the handler can wait indefinitely.

For a periodic status feed, this iterator fragment uses `time` and
`arkoren.dev/core/http/server`. It runs inside the handler and captures its
request `r`:

```go
ticks := func(yield func(time.Time, error) bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-server.ShuttingDown(r.Context()):
			return
		case tick := <-ticker.C:
			if !yield(tick, nil) {
				return
			}
		}
	}
}
```

Send the timestamps with the same SSE helper:

```go
return response.SSEJSON(w, r, ticks)
```

Serve through the [Arkoren server](../http-networking/server.md) to supply its
shutdown signal. Graceful shutdown does not cancel the request context, so
watching only `r.Context().Done()` is insufficient. The helper ends cleanly
with nil on server shutdown and returns the context error on request cancellation.

A write or flush failure does not cancel the captured request context either.
The ticker above has a bounded wait even without cancellation. Give network
calls or other blocking producer work their own bounds as well. Network write
deadlines do not interrupt arbitrary producer code. Producer panics, including
cleanup panics, are re-raised on the request goroutine after joining, not
converted to iterator errors; request recovery can then handle them.

## Own Channel Producers

If your application already supplies a `<-chan []byte`, use `StreamChan` or
`SSEChan`. With an existing channel `events`:

```go
return response.SSEChan(w, r, events)
```

Channel closure ends consumption, as do cancellation, shutdown, or I/O failure.
These helpers neither start nor join your goroutine. You own its stop signal,
cleanup, and join. Arrange a consumer-exit signal in addition to request and
server cancellation: an I/O failure can leave the request context active and
the producer blocked on a send nobody will receive. Do not assume that returning
from this helper proves your channel producer has stopped.

## Bound Slow Writes

The helpers renew the write deadline before the initial header flush and every
data write. Supported, enforced deadlines let a long stream outlive the server's
overall write timeout while bounding a stalled client write or flush. They do
not bound how long joining the producer takes.

The per-write timeout defaults to 30 seconds. For a feed that should drop slow
clients sooner, import `time` and set a shorter bound:

```go
return response.SSEJSON(w, r, updates,
	response.WithWriteTimeout(10*time.Second),
)
```

Zero or negative values select the default. A deadline-setting error matching
`http.ErrNotSupported`, even when wrapped, leaves the writer's existing timeout
policy in effect. That fallback does not guarantee bounded writes. Other
deadline-setting errors return unchanged: an initial failure is before
commitment; a later failure stops an already started response.

## Committed Failures

The supplied writer must implement `http.Flusher`. Otherwise the helper returns
`response.ErrNonFlushableWriter` before committing or starting an iterator
producer. Capability advertisement does not guarantee later success: flush
errors are returned, including `http.ErrNotSupported`. A writer exposing
`FlushError() error` can report failures; one exposing only `Flush()` cannot.

The initial flush happens after `WriteHeader(200)`, so even an initial flush
failure is committed. Iterator errors, JSON encoding errors, and write/flush
failures return to outer middleware and hooks without appending a problem body.
Bytes already accepted by a write remain accepted even if flushing them fails.
Use [request logging](../observability/request-logging.md) to record the failure,
and design the client protocol to recognize completion rather than assuming
that a `200` means all updates arrived.

A recovered panic after commitment aborts the HTTP response with
`http.ErrAbortHandler`; see [Error Handling](error-handling.md#committed-failures).
Hijacked connections such as WebSockets are outside these helpers' lifecycle:
you must close them and arrange their shutdown yourself.

For all stream helpers and options, see the
[`response` API reference](https://pkg.go.dev/arkoren.dev/core/http/response).
