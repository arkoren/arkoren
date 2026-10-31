---
title: Fakes
description: Test code that sends mail or push notifications, dispatches jobs, publishes messages, schedules tasks, starts workflows, or calls models, with fakes.
order: 40
---

# Fakes

## Introduction

Some services have effects that leave your process: an email is sent, a job is
put on a queue, a workflow starts on another machine. In a test you do not want
those effects to happen, but you do want to know that your code asked for them,
with the right content.

A **fake** is a driver that records what it is asked to do instead of doing
it, and offers assertions on what it recorded. You pass it to the real facade,
so the code under test runs unchanged, through the same validation and
encoding as in production. Fakes live in each service's `<service>test`
package.

The fragments run inside application tests with `t *testing.T` and an operation
context `ctx` (usually `t.Context()`). Import the facade from
`arkoren.dev/core/service/<service>` and its fake from
`arkoren.dev/core/service/<service>/<service>test`, for example
`arkoren.dev/core/service/mail` and `arkoren.dev/core/service/mail/mailtest`.
The telemetry example also imports `arkoren.dev/core/service/telemetry`.

| Service | Fake | Records |
| --- | --- | --- |
| Mail | `mailtest.New()` | sent messages |
| Push notifications | `pushtest.New()` | accepted messages and completed send settings |
| Queues | `queuetest.New()` | dispatched, pending, failed, and rejected jobs |
| Broadcasting | `broadcasttest.New()` | published messages |
| Task scheduling | `scheduletest.New()` | entries, deferred and canceled tasks |
| Workflows | `workflowtest.New()` | started, signaled, and canceled runs |
| Payments | `paymenttest.New()` | attempted and accepted requests, with operation-specific scripted replies |
| Language models | `llmtest.New()` | generated and embedded requests; replies are scripted |
| Telemetry | `telemetrytest.New()` | spans, metrics, and logs |

Services whose effects stay in the process, such as the cache, feature flags,
file storage, and secrets, need no fake: test them with their memory drivers.

For payment scripts and ambiguous outcomes, use
[Testing Payments](../payments/testing.md). Recording a request proves what
your application asked the fake to do; it does not prove provider delivery,
settlement, or durable storage.

## Asserting on sent mail

Build the mailer with the fake, pass it to your application's signup operation,
and assert on the message. Here `signup.Register` is application code, not an
Arkoren helper; it must use the supplied `mailer`:

```go
fake := mailtest.New()
mailer := mail.New(fake, func(mailer *mail.Builder) {
	mailer.From(mail.Address{Email: "no-reply@example.com"})
})

if err := signup.Register(ctx, mailer, "ada@example.com"); err != nil {
	t.Fatal(err)
}

fake.AssertSent(t, func(message mail.Message) bool {
	to := message.To()
	return len(to) == 1 && to[0].Email == "ada@example.com" && message.Subject() == "Welcome"
})
```

Each assertion takes predicates, plain functions that receive a recorded item.
`AssertSent` passes when at least one message matches all of them. There are
counterparts for the other cases you usually check: `AssertSentCount`,
`AssertNotSent`, and `AssertNothingSent`. When you need more than an
assertion, `Sent` returns the recorded messages.

## Asserting on push notifications

Use the push fake when an application operation should notify a browser or
device without contacting a push service. Import `push` and `pushtest` from
`arkoren.dev/core/service/push` and `arkoren.dev/core/service/push/pushtest`,
and import `time` for retention. This fragment runs inside a test with `t`:

```go
fake := pushtest.New()
sender := push.New(fake)
message := push.NewMessage(push.Token("test-device"), "Order shipped")

if _, err := sender.Send(t.Context(), message, push.TTL(time.Hour)); err != nil {
	t.Fatal(err)
}
fake.AssertSent(t, func(submission pushtest.Submission) bool {
	return submission.Message.Title() == "Order shipped" && submission.Config.TTL == time.Hour
})
```

The assertion checks the accepted message and the retention settings completed
by the real facade. `Sent()` returns those submissions; the fake does not deliver
notifications or need a drain. The test token is interpreted only by the fake,
not by the Web Push driver. For browser setup and expiry handling, read
[Push Notifications](../push/overview.md).

## Asserting on dispatched jobs

The queue fake works the same way, with assertions typed by the job, so
predicates receive the decoded payload. Here `billing.Close` is your application
operation, `SendInvoice` is its declared job, and `InvoicePayload` is that job's
payload type. See [producing jobs](../queues/producing-jobs.md) for declarations:

```go
fake := queuetest.New()
dispatcher := queue.NewDispatcher(fake)

if err := billing.Close(ctx, dispatcher, invoiceID); err != nil {
	t.Fatal(err)
}

fake.AssertDispatched(t, SendInvoice, func(invoice InvoicePayload) bool {
	return invoice.InvoiceID == invoiceID
})
```

### Running the dispatched jobs

Sometimes the test should also run the jobs, for example to check what a
handler does with them. Here `sendInvoice` is your handler for `SendInvoice`.
`Drain` hands every pending job to a real consumer, so
your handlers, retry decisions, and failure handling run through the real
consumer. It does not simulate broker persistence or delivery after a process crash:

```go
consumer := queue.NewConsumer(fake)
if err := consumer.Handle(SendInvoice, sendInvoice); err != nil {
	t.Fatal(err)
}

if err := fake.Drain(ctx, consumer); err != nil {
	t.Fatal(err)
}
```

Jobs dispatched with a delay stay pending until they become due or you drain
with `queuetest.WithScheduled()`. That option skips the delays; it does not
advance the clock. A drain is bounded to 100 deliveries per message by default
and reports `queuetest.ErrDrainLimit` if retries exceed that bound.

The broadcast fake has the same `Drain`, and the schedule fake runs due tasks
with its own `Drain`. The workflow fake does not run workflow code; instead,
`Complete` and `Fail` decide how a started run ends, so you can test the code
that waits for its result.

## Scripting model replies

The language model fake works the other way around: it records requests, but
you also script the replies it gives, so the code under test gets a
deterministic answer instead of a live model's:

```go
fake := llmtest.New()
fake.Reply("Your order shipped yesterday.")
model := llm.New(fake, "test-model")

_, err := model.Generate(ctx, llm.Request{
	Messages: []llm.Message{llm.UserText("When did my order ship?")},
})
if err != nil {
	t.Fatal(err)
}
fake.AssertGenerated(t, func(request llm.Request) bool {
	return request.Model == "test-model" && len(request.Messages) == 1 && request.Messages[0].Role == llm.RoleUser
})
```

[Testing Language Models](../llm/testing.md) covers tool calls, streaming, and
failures.

## Testing failures

Your code should handle a mail server that is down or a full queue. The mail,
push, queue, broadcast, schedule, workflow, LLM, and payment fakes offer `FailNext`
and `FailWith`; telemetry records observations instead of injecting operation
failures. `FailNext` injects one failure, and `FailWith` continues injecting it
until cleared with `nil`. Exact validation and recording order is fake-specific.
With the mail `fake` and `mailer` from the signup example, import `errors` and
verify that the injected error reaches the caller:

```go
failure := errors.New("smtp: connection refused")
fake.FailNext(failure)

err := signup.Register(ctx, mailer, "ada@example.com")
if !errors.Is(err, failure) {
	t.Fatalf("error = %v, want injected failure", err)
}
```

`Reset` clears everything a fake recorded, which is useful when one fake is
shared by several steps of a test.

## Asserting on telemetry

The telemetry fake records spans, metrics, and log records. `Telemetry`
returns a started telemetry handle for the code under test, stopped
automatically when the test ends. Here `payments.Charge` is your application
operation, which uses the supplied telemetry handle to record a `charge` span
and a `billing.charges` counter:

```go
fake := telemetrytest.New()
tel := fake.Telemetry(t)

if err := payments.Charge(ctx, tel, "EUR"); err != nil {
	t.Fatal(err)
}

fake.AssertSpan(t, "charge", telemetry.String("currency", "EUR"))
fake.AssertCounter(t, "billing.charges", 1, telemetry.String("currency", "EUR"))
```

`AssertLog` checks a log record, and `AssertHistogram` and `AssertGauge` the
other metric kinds. [Telemetry](../observability/telemetry.md) explains
spans and metrics.

Call `fake.DumpOnFailure(t)` once after construction when a failed test needs
the trace tree and correlated logs. It registers failure-only output and stays
silent when the test passes. Readbacks return spans, metrics, logs, and resource
data; `Reset` clears recorded state. Start the fake either with `Telemetry(t)`
or through an application, not both: it records nothing until started.

`telemetrytest.New` accepts the memory driver's construction callbacks; these
configure recording limits, not conformance-suite options. Use `go doc
arkoren.dev/core/service/telemetry/telemetrytest.New` for the exact constructor.

For exact fake APIs, run `go doc` for the corresponding `<service>test` package
from your application's module directory. The service-specific testing guides
above explain scripting, drains, and interpretation of recorded results.
