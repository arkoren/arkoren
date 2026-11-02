---
title: Fakes
description: Record external requests, script failures, and assert application behavior through real service facades.
order: 40
summary:
  - Choose recording and scripted fakes without confusing requested effects with provider delivery.
  - Assert welcome-mail content and detect duplicate sends, then exercise send failures.
  - Distinguish queue drains from engine execution and capture test-owned telemetry.
---

# Fakes

An application test can check that registration requests a welcome email without delivering one. A **fake** replaces the driver at that external boundary: the real facade still applies its defaults, validation, and encoding, while the fake records the request or returns a scripted result.

Use an in-process driver instead when the test needs stored state or an executing engine. A recorded workflow start, for example, is evidence that your code requested a run, not that the workflow executed successfully.

## Choose Fakes

Fakes live in each service's test package. These entry points select different kinds of evidence:

| Package | What the fake provides |
| --- | --- |
| `mail/mailtest` | Accepted messages and send-failure injection. |
| `push/pushtest` | Accepted messages and completed retention settings. |
| `queue/queuetest` | Dispatched jobs, typed assertions, and an explicit consumer drain. |
| `broadcast/broadcasttest` | Published messages and an explicit listener drain. |
| `schedule/scheduletest` | Registered entries, deferred tasks, and a drain for deferred tasks. |
| `workflow/workflowtest` | Run commands and manually completed or failed results, not engine execution. |
| `payment/paymenttest` | Recorded requests and operation-specific scripted replies. |
| `llm/llmtest` | Recorded model requests and scripted model replies. |
| `telemetry/telemetrytest` | Retained spans, metrics, and logs. |

Paths in this table are below `arkoren.dev/core/service/`; each package exposes `New()`. Use the service-specific guides for [payment outcomes](../payments/testing.md), [model replies](../llm/testing.md), [push tests](../push/testing.md), and [workflow tests](../workflows/testing.md). Those guides cover the extra scripting and assertions their features require.

## Assert Mail

Keep the operation being tested real. This small application function constructs a welcome message using `context` and `arkoren.dev/core/service/mail`:

```go
func sendWelcome(ctx context.Context, mailer *mail.Mailer, address string) error {
	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: address}},
	}, mail.Body{Text: "Your account is ready."}, func(letter *mail.MessageBuilder) {
		letter.Subject("Welcome")
	})
	_, err := mailer.Send(ctx, message)
	return err
}
```

`Mailer.Send` checks the completed message, including the sender supplied by facade defaults, before submitting it. The test supplies those defaults and a recording driver. Add `testing` and `arkoren.dev/core/service/mail/mailtest`:

```go
func TestWelcomeMessage(t *testing.T) {
	fake := mailtest.New()
	mailer := mail.New(fake, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "accounts@example.com"})
	})
	if err := sendWelcome(t.Context(), mailer, "reader@example.com"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSentCount(t, 1)
	fake.AssertSent(t, func(message mail.Message) bool {
		recipients := message.To()
		return len(recipients) == 1 && recipients[0].Email == "reader@example.com" &&
			message.Subject() == "Welcome" && message.From().Email == "accounts@example.com"
	})
}
```

`AssertSent` requires at least one message matching every supplied predicate. The separate count assertion catches duplicate sends. `Sent()` returns recorded messages when you need assertions beyond the helpers. `AssertNotSent` checks that no message matches; `AssertNothingSent` checks that there are no accepted messages at all.

This test proves that the application submitted the intended message through the facade. It does not prove SMTP connectivity, provider acceptance, or delivery to an inbox.

## Inject Failures

Use an injected failure to exercise the caller's response, not only the fake's recording. With `errors` imported, this independent test verifies that the operation preserves a send error:

```go
func TestWelcomeFailure(t *testing.T) {
	fake := mailtest.New()
	mailer := mail.New(fake, func(mailer *mail.Builder) {
		mailer.From(mail.Address{Email: "accounts@example.com"})
	})
	failure := errors.New("mail service unavailable")
	fake.FailNext(failure)

	err := sendWelcome(t.Context(), mailer, "reader@example.com")
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v, want injected failure", err)
	}
	fake.AssertNothingSent(t)
}
```

For this mail fake, validation and context checks happen before consuming the injected failure; a failed send is not recorded as accepted. `FailNext` supplies one failure, while `FailWith` persists until cleared with `nil`. `Reset` clears both recorded mail and injected failures. Prefer a fresh fake per independent test so one case cannot consume another's script.

Other fakes have their own validation, recording, and scripting order. Check that package's contract before interpreting an attempted operation as accepted. Telemetry records observations rather than injecting operation failures.

## Separate Execution

A queue dispatch assertion checks a request to enqueue work. To test its handler as well, register that handler on a real consumer and explicitly drain the queue fake. Import `arkoren.dev/core/service/queue` and its `queuetest` subpackage. Inside your test, construct `fake := queuetest.New()` and pass `queue.NewDispatcher(fake)` to the application operation that dispatches your declared `SendReceipt` job. After that operation succeeds, register its `sendReceipt` handler and drain:

```go
consumer := queue.NewConsumer(fake)
if err := consumer.Handle(SendReceipt, sendReceipt); err != nil {
	t.Fatal(err)
}
if err := fake.Drain(t.Context(), consumer); err != nil {
	t.Fatal(err)
}
```

Assert the handler's observable effect afterward. The drain uses the real consumer's decoding, retry, and failure behavior, but not broker persistence or recovery after process crashes. [Producing Jobs](../queues/producing-jobs.md) owns job declarations and typed payloads.

Delayed jobs remain pending until due unless you use `queuetest.WithScheduled()`, which skips the delay rather than advancing time. By default, a drain permits at most 100 deliveries per message and returns `queuetest.ErrDrainLimit` if that bound is exceeded. For actual timer behavior use [Controlling Time](time.md).

Do not assume every fake has this execution model. The broadcast fake drains listeners; the schedule fake drains deferred tasks. The workflow fake uses `Complete` or `Fail` to supply a result without executing workflow code. Test engine behavior using the [workflow testing tools](../workflows/testing.md) instead.

## Capture Telemetry

When an operation emits telemetry, pass it a test-owned [shared handle](../observability/telemetry.md#shared-handle). With `telemetrytest` imported from `arkoren.dev/core/service/telemetry/telemetrytest`, and your application operation named `refreshCatalog`:

```go
fake := telemetrytest.New()
tel := fake.Telemetry(t)
fake.DumpOnFailure(t)

if err := refreshCatalog(t.Context(), tel); err != nil {
	t.Fatal(err)
}
fake.AssertSpan(t, "catalog refresh")
```

`Telemetry(t)` returns a started handle and registers its shutdown with the test. Start the fake this way or through application lifecycle wiring, not both; it records nothing before startup. End application spans before asserting their completed contents, following [Recording Spans](../observability/telemetry.md#recording-spans).

Add attribute assertions when they distinguish the behavior under test, and use counter, histogram, gauge, or log assertions for those signals. `DumpOnFailure` prints retained diagnostic data only when the test fails. Construction callbacks configure memory recording limits, not conformance-suite options.

For the examples above, see the [`mailtest` API reference](https://pkg.go.dev/arkoren.dev/core/service/mail/mailtest), [`queuetest` API reference](https://pkg.go.dev/arkoren.dev/core/service/queue/queuetest), and [`telemetrytest` API reference](https://pkg.go.dev/arkoren.dev/core/service/telemetry/telemetrytest).
