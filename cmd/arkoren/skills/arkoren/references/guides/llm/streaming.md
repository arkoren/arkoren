---
title: Streaming Replies
nav: Streaming
description: Stream model output and agent progress as it arrives, and send it to a browser with response.SSEJSON.
order: 40
---

# Streaming Replies

## Introduction

A long answer leaves the user waiting if you display it only after generation
finishes. Streaming lets you show text as it arrives; it does not promise a
shorter total generation time. Use `Model.Stream` for one turn and
`Model.RunStream` for an agent's turns and tool progress. Both return
`collection.TryLazySlice[llm.Event]`, so you can use `for ... range` or native
collection operations. Breaking out of the loop cancels the request to the vendor.

Construction and lazy operations such as `Filter`, `Map`, and `Take` do no model
I/O. Traversal starts the work. Traversing the same collection again issues new
provider calls, which can be billed again; an agent can also repeat tool effects.
Materialize once if you need to reuse the observed events without another run.

The examples use `llm` from `arkoren.dev/core/service/llm` and the standard
`fmt` package. Reuse `model` and `ctx` from the
[overview setup](overview.md#creating-a-model).

## Streaming one turn

If you want to print an answer as it is written, read text fragments from
`Stream` rather than waiting for `Generate` to return the whole response. Use
your existing `model` and a `request` of type `llm.Request`, with the same
shape as [a single-turn request](overview.md#generating-one-turn):

```go
for event, err := range model.Stream(ctx, request) {
	if delta, ok := event.(llm.TextDelta); ok {
		fmt.Print(delta.Text)
	}
}
```

The events are `TextDelta` and `ReasoningDelta` fragments, `ToolCallStarted` and
`ToolCallDelta` for a tool call being written, and finally `Done`, which carries
the complete `Response`. A consumer that ignores the deltas still gets the
result from `Done`. A stream that ends without `Done` or an error yields
`llm.ErrUnavailable`.

A terminal error means the stream failed; text already shown to the user does
not mean a complete answer was received.

## Streaming an agent

An agent may pause its answer while it looks up an order or calls another tool.
Showing that progress helps the person waiting understand what is happening.
`RunStream` forwards the text from each turn along with tool-progress events.
Use the `support` agent from [Agents and Tools](agents-and-tools.md) and your
stored `history`:

```go
for event, err := range model.RunStream(ctx, support, history) {
	switch e := event.(type) {
	case llm.TextDelta:
		fmt.Print(e.Text)
	case llm.ToolStarted:
		fmt.Println("running", e.Name)
	case llm.ToolFinished:
		fmt.Println("finished", e.Name, "error:", e.IsError)
	case llm.StepCompleted:
		fmt.Println("step", e.Step, "done")
	}
}
```

`ToolStarted` and `ToolFinished` carry the call ID and name; `ToolFinished` also
carries an error flag. Neither carries arguments or results. `ToolStarted`
follows admission to the concurrency slot, not merely the model's request for a
call. A queued call canceled before admission is not announced. A start does not
guarantee handler execution: availability and argument validation still apply.
If the tool phase completes without a fatal error, `ToolFinished` events arrive
in call order after every admitted call has finished.

`RunStream` cannot return a `Result`; use `Run`
when you need the complete conversation to persist or resume. `Done` contains
each model response, not the tool-result messages appended inside the run,
so collecting `Done` events alone does not reconstruct that conversation.

For a [structured-output agent](structured-output.md), streamed text can still
be incomplete or fail final validation. Do not treat a text delta as a validated
typed result; keep reading the stream's error value.

## Stopping a stream

To record a normally completed standalone turn, consume `Model.Stream` through
its natural end. Breaking or returning from a successful event, including
`Done`, cancels the driver context and suppresses both `llm.response.generated`
and `llm.response.failed`. The turn span ends without error. A driver failure
is different: its observation is finalized before its error is yielded, so
stopping on that error does not retract the failure event.

Stopping `RunStream` cancels the run, joins admitted tool calls, and suppresses
the agent outcome event. Completed tool observations remain published. Tool
handlers must honor cancellation; stopping the consumer cannot undo effects or
forcibly terminate a blocked handler. A panic also propagates rather than
becoming a successful stop; the
[observation rules](overview.md#usage-events-and-telemetry) explain its bounded
telemetry and lack of an invented outcome event.

## Sending a stream to the browser

For a chat page, the fragments need to reach the browser as they arrive at your
server. `response.SSEJSON` sends each iterator value as a JSON-encoded
server-sent event. Use `.Iter()` to pass the collection to this standard
`iter.Seq2` consumer. Each event
has a `"type"` tag the browser can use to distinguish text from tool progress.

The iterator must cooperate with cancellation and shutdown; blocking model and
tool work needs a way to finish before the streaming handler can return.

Inside your existing Arkoren handler, with `w`, `req`, and the conversation `history`
already available, import `response` from `arkoren.dev/core/http/response`:

```go
return response.SSEJSON(w, req, model.RunStream(req.Context(), support, history).Iter())
```

In the browser, read the stream with `fetch` for a `POST`, or with `EventSource`
for a `GET`, and parse each event's `data` as JSON.

`SSEJSON` stops consuming values on request cancellation, server shutdown, or a
write or flush failure. It makes `yield` return false and waits for the iterator
producer to exit; it does not abandon that goroutine. Producers must return when
`yield` returns false and must not yield further values, even a final `Done`.

Client disconnects cancel `req.Context()`. Graceful shutdown is different: an
Arkoren `server.Server` announces it through `server.ShuttingDown(req.Context())`
without canceling that request context. The shutdown helper is in
`arkoren.dev/core/http/server`. A write or flush failure does not cancel the
request context either.

The streaming helper cannot forcibly interrupt a model call or tool handler
blocked outside `yield`. Arrange for that work to stop on request cancellation
and the shutdown signal, or give it another bounded way to exit. Passing only
`req.Context()` does not guarantee prompt graceful shutdown while work is blocked;
a noncooperative producer can keep `SSEJSON` waiting indefinitely.
[Responses](../http-fundamentals/responses.md#streaming) explains the write
deadline and cooperative shutdown behavior of streaming responses.

> [!NOTE]
> The headers are sent before the first event. An error that occurs later, such
> as a rate limit, ends the stream and is returned to the handler's caller, but
> cannot change the committed status code. Check what you can before streaming.
> If the browser needs an error event, send an application-approved message from
> a wrapper iterator, not raw provider, tool, or schema diagnostics.

## Selecting and collecting events

To display only text, filter the stream directly; no collection conversion or
additional import is required:

```go
text := model.Stream(ctx, request).Filter(func(_ int, item llm.Event) (bool, error) {
	_, ok := item.(llm.TextDelta)
	return ok, nil
})
for item, err := range text {
	fmt.Print(item.(llm.TextDelta).Text)
}
```

Filtering out `Done` does not redefine completion or bypass the source's
missing-`Done` validation. Reading this filtered stream through its natural end
still completes the source turn. `Take(n)` instead stops early after its limit:
it cancels and joins the source under the same rules as breaking the loop.

For finite streams, `events, err := model.Stream(ctx, request).Items()` collects
events into a Go slice. On failure, `events` contains the successful prefix and
`err` retains the terminal error. That prefix is not a completed response, even
if it contains text or a `Done` before a later error. `RunStream` materialization
does not produce the conversation or typed result returned by `Run`.

Materialization requires a finite source and holds the collected events in
memory. JSON encoding the collection also materializes it; it does not send
events to the browser as they arrive. Use `SSEJSON` with `.Iter()` for that.

[Fallible Collections](../collections/fallible-collections.md) lists what a
`TryLazySlice` can do.

For every function and option, see [the `llm` API reference](https://pkg.go.dev/arkoren.dev/core/service/llm)
and [the `response` API reference](https://pkg.go.dev/arkoren.dev/core/http/response).
