---
title: Responses
description: Write JSON, text, HTML, and binary responses, redirect safely, stream data and server-sent events, and know when a response is written.
order: 30
---

# Responses

## Introduction

To send a result from a handler, write to the `http.ResponseWriter` it receives,
as in any Go program. The `response` package provides one helper per common kind
of response. Most helpers set `Content-Type` and write the status code and body;
they return an `error`, so you can end a handler with `return response.JSON(...)`.

Finish work that can fail before writing the response, and make the response
helper the handler's last call. Writing the final status commits the response:
the router can no longer render a returned error in its place. See
[when a response counts as written](#when-a-response-counts-as-written) for the
buffering and network-write limits.

Import `arkoren.dev/core/http/response` and `net/http`. These fragments assume
Arkoren's error-returning router handlers, with response writer `w` and incoming
request `r`. The values `user`, `name`, and `png` are your existing response
value, user-supplied name, and encoded PNG bytes, respectively.

## JSON

To send a value as JSON, use `response.JSON`:

```go
return response.JSON(w, http.StatusOK, user)
```

The helper accepts any value and sets `Content-Type: application/json`. It
encodes the value before writing anything. If encoding fails, the helper
returns the error before committing the response, so the router can render an
error response rather than leave the client with half a document.

Native collection fields, including pagination items, use this same helper;
no collection-specific response function is needed. A lazy collection is
materialized before encoding, so it must be finite and fit in memory. A source
error leaves the response uncommitted, but successful source work has already
happened. To deliver values as they arrive, use [streaming](#streaming) instead.

## Text and HTML

To send plain text, use `response.String`:

```go
return response.String(w, http.StatusOK, "pong")
```

For HTML, choose the helper according to where the content comes from:

- `response.HTML` writes HTML as is and does not escape anything. Use it only
  for HTML you produced yourself.
- `response.HTMLTemplate` renders an `html/template` template, which escapes
  values for you. Use it for a page that contains user data.

With an existing parsed `*html/template.Template` named `page`, pass the
user-supplied name as template data:

```go
return response.HTMLTemplate(w, http.StatusOK, page, name)
```

The template runs completely before the response starts. If rendering fails,
the helper returns the error before committing the response, so the router can
render an error response rather than a broken page.

On successful execution, `HTMLTemplate` explicitly sets
`Content-Type: text/html; charset=utf-8`. `StringTemplate`, which uses
`text/template` without HTML escaping, sets `text/plain; charset=utf-8`.
Both replace a pre-existing media type; they do not infer it from the output.
For a different explicit media type, render to your own buffer and send it with
[`response.Raw`](#binary-data).

## Status-only responses

To send a response without a body, such as `204 No Content` after a delete,
use `response.Status`:

```go
return response.Status(w, http.StatusNoContent)
```

The helper writes only the status. An informational `1xx` other than `101`
does not finish the response; send a final status afterward. If an Arkoren
error-returning handler returns `nil` with no final response, the router answers
`204`, including after informational-only writes. This is router behavior, not
the default for all `net/http` handlers.

## Binary data

`response.Bytes` writes bytes as `application/octet-stream`. If you set your
own `Content-Type` first, such as `image/png`, use `response.Raw`, which keeps
it:

```go
w.Header().Set("Content-Type", "image/png")
return response.Raw(w, http.StatusOK, png)
```

## Redirects

To send the client to a URL you control, use `response.Redirect`:

```go
return response.Redirect(w, http.StatusSeeOther, "/dashboard")
```

If the target comes from the user, use `response.SafeRedirect` instead. To
prevent an attacker from using your login page to send users to another site,
the helper accepts only relative paths that start with a single `/`. It rejects
absolute URLs, network-path targets such as `//example.com`, and paths containing
backslashes. An invalid target returns a `400` problem error for the router to
render; the helper does not write the error response itself.

For a user-supplied `?next=` parameter after login, import
`arkoren.dev/core/http/request` and read the target with `request.QueryOr`:

```go
next := request.QueryOr(r, "next", "/") // arkoren.dev/core/http/request
return response.SafeRedirect(w, http.StatusSeeOther, next)
```

## Streaming

When a result arrives gradually, such as a language model's reply or progress
updates, send each piece without waiting for the whole result. Choose the
helper according to the client format and how your producer supplies data.

### Choose bytes or server-sent events

`response.Stream` writes byte slices from a Go iterator
(`iter.Seq2[[]byte, error]`) and flushes each one to the client.

For browsers using `EventSource`, use server-sent events instead.
`response.SSE` frames each byte slice as an event; `response.SSEJSON` also
encodes each value as JSON. With an existing `updates` iterator
(`iter.Seq2[Progress, error]`) in your handler:

```go
return response.SSEJSON(w, r, updates)
```

For a native `collection.TryLazySlice[Progress]` named `updates`, pass
`updates.Iter()` instead. The standard iterator input also accepts producers
that do not use Arkoren collections. Converting with `Iter()` consumes nothing
and does not change the source's cancellation or cleanup responsibilities.

`SSE` and `SSEChan` turn every payload line into a `data:` field. CRLF and bare
CR normalize to LF, and all LF characters, including trailing newlines, survive
in the client's `EventSource` data. For example, a payload `"first\n"` still ends
in a newline at the client; an extra empty `data:` field preserves it. An empty
payload emits an empty event. Payload line breaks cannot inject separate SSE
fields such as `event:` or `id:`.

`Stream` and `SSE` copy each yielded byte slice before `yield` returns, so your
producer can reuse its buffer afterward. `SSEJSON` finishes encoding each value
before releasing the producer's yield callback.

A language model's reply is a typical stream; see [Streaming
Replies](../llm/streaming.md) for sending one to a browser.

### End a stream and handle errors

The helper stops consuming values when the sequence ends, the request is
canceled, the server starts shutting down, or a write or flush fails. For an
iterator, the helper makes `yield` return false and waits for the producer to
exit before returning. `Stream`, `SSE`, and `SSEJSON` always join the producer
goroutine they start rather than abandoning it. This requires
[producer cooperation](#stop-producers-on-cancellation-and-shutdown).

If the sequence yields an error, the helper returns it. Write and flush failures
are also returned without losing their cause. The initial flush follows the
`200 OK` status, so even a failure flushing the headers occurs after the response
is committed. The router cannot replace the response or append a problem body;
bytes already accepted by a write remain accepted even if their flush fails. Install
[request logging](../observability/request-logging.md) to record the returned error.

Streaming requires the passed writer to implement `http.Flusher`; otherwise,
the helper returns `response.ErrNonFlushableWriter` before committing or starting
an iterator producer. An advertised capability can still fail later. When the
writer exposes `FlushError() error`, the helper preserves reported flush failures,
including errors matching `http.ErrNotSupported`. Unlike an unsupported write deadline,
an unsupported flush ends the stream. A writer exposing only `Flush()` cannot
report flush errors.

Producer panics are re-raised on the request goroutine after the producer exits,
so [panic recovery](error-handling.md#panics) still applies. They are not converted
into returned iterator errors.

### Stop producers on cancellation and shutdown

Because the helper waits for the loop to exit, a producer that blocks outside
`yield` must watch the request context for cancellation and
`server.ShuttingDown(r.Context())` for shutdown, or have another bounded way to
finish. The helper cannot interrupt arbitrary iterator code; without cooperation,
it can wait indefinitely. A write or flush failure does not cancel the captured
request context either, so give blocking work its own bound rather than relying
on request cancellation alone. The shutdown helper is in `arkoren.dev/core/http/server`.

When `yield` returns false, the producer must return without yielding again,
including any final value or error from cleanup.

For a live timestamp feed, import `time` and
`arkoren.dev/core/http/server` alongside `net/http` and
`arkoren.dev/core/http/response`. This error-returning handler blocks between
ticks but releases its ticker when the request is canceled, the serving Arkoren
server starts draining, or the streaming helper stops iteration:

```go
func timestamps(w http.ResponseWriter, r *http.Request) error {
	ticks := func(yield func(time.Time, error) bool) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			var tick time.Time
			select {
			case <-r.Context().Done():
				return
			case <-server.ShuttingDown(r.Context()):
				return
			case tick = <-ticker.C:
			}
			if !yield(tick, nil) {
				return
			}
		}
	}
	return response.SSEJSON(w, r, ticks)
}
```

Register `timestamps` on your router and serve it through the Arkoren server
to supply the shutdown signal. Each yielded tick becomes a JSON timestamp
event. Graceful shutdown does not cancel the request
context, so the separate shutdown case is necessary even when cancellation is
already handled. Returning when `yield` is false lets the helper finish waiting
for the producer.

If your producer is a goroutine with a channel, use `response.StreamChan` or
`response.SSEChan` instead. These helpers also end when the channel closes.
They do not start or join your producer goroutine: you own its cancellation and
cleanup. The producer must stop on cancellation or server shutdown itself, and
needs a way to end if the consumer stops after an I/O error; otherwise, it can
remain blocked sending to a channel the handler no longer reads.

Hijacked connections such as WebSockets need their own shutdown handling;
these helpers do not manage them.

### Set the per-write timeout

The streaming helpers renew the write deadline before the initial header flush
and each write. When the writer supports and enforces these deadlines, renewal
lets a long stream outlive the server's write timeout while bounding how long
a write or flush can block on a client that stops reading. This bounds network
I/O, not the wait for an iterator producer to exit.

The per-write timeout defaults to 30 seconds. To change it, use
`response.WithWriteTimeout(d)`. Zero or negative values use the default.

If setting a deadline returns an error matching `http.ErrNotSupported`,
including a wrapped error, streaming continues with the writer's existing
timeout policy. This fallback does not guarantee a bound on stalled writes.
Other deadline-setting errors are returned unchanged before the affected
flush or write:

- An initial failure leaves the response uncommitted, so the router can render
  the error.
- A later failure stops the stream without adding a problem response to data
  already sent.

## When a response counts as written

The router can only render a returned error while the final response is pending.
A final status commits it, whether written explicitly or implicitly by a body
write or flush. Informational `1xx` responses other than `101 Switching Protocols`
leave it pending; `101` is final for this purpose. Once committed, an ordinary
returned error still reaches outer middleware and after-response hooks, but the
router neither replaces the response nor appends a problem body.

A successful connection hijack also suppresses error rendering and the router's
default `204`; a failed hijack does not itself commit an unwritten response.
You own a successfully hijacked connection and must close it and arrange its
shutdown. The router and streaming helpers do not take it back. Normal HTTP
writes must use the supplied writer: writing directly through an unwrapped
native writer bypasses lifecycle hooks and commitment tracking.

Finish all work that can fail before writing the response, and make the
response helper the handler's last call. Buffered helpers such as JSON and
templates prepare their output before writing the status, so preparation
errors leave the router able to render an error response.

Network writes can still fail, and streaming helpers can fail after starting
the response. Neither can undo bytes already accepted by a write, even when a
later flush fails. After commitment, a recovered panic aborts the HTTP response
with `http.ErrAbortHandler` rather than appending a problem; it does not transfer
ownership of a hijacked connection. See
[error handling after commitment](error-handling.md#after-the-response-has-started)
for recovery and privacy boundaries.

For every helper, see the [`response` API reference](https://pkg.go.dev/arkoren.dev/core/http/response).
