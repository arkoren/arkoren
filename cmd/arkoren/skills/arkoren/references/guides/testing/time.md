---
title: Controlling Time
description: Test expiry, timeouts, retries, and schedules in zero real time with testing/synctest, and pin the clock used by validation.
order: 50
---

# Controlling Time

## Introduction

A lot of behavior depends on time: cache entries expire, sessions time out,
jobs retry after a backoff, scheduled tasks run every hour. Testing it by
actually waiting makes tests slow and flaky. Testing it with a mocked clock
means threading a clock through code that should not care.

Go's `testing/synctest` package solves this without changes to your code. Code
running inside a synctest **bubble** sees a fake clock. It advances when every
goroutine in the bubble is *durably blocked*: waiting on work that can be
unblocked only within that bubble, such as a bubble-local channel or timer.
A one-hour sleep can therefore finish without a real hour passing. Arkoren's
memory drivers and fakes work inside bubbles; network I/O and mutex waits do
not count as durable blocking.

## Testing expiry

Wrap the test body in `synctest.Test`, and use `time.Sleep` to move time
forward:

In a `_test.go` file, import `errors`, `testing`, `testing/synctest`, `time`,
`arkoren.dev/core/service/cache`, and `arkoren.dev/core/service/cache/memorycache`:

```go
func TestEntryExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		key := cache.NewKey[string]("codes.v1.login", func(entry *cache.KeyBuilder) {
			entry.TTL(time.Minute)
		})
		if err := store.Put(t.Context(), key, "1234"); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Minute + time.Second)

		if _, err := store.Get(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
			t.Fatalf("err = %v, want cache.ErrNotFound", err)
		}
	})
}
```

No real minute elapses. This example does not call the memory cache's `Start`,
so it has no janitor goroutine; reads still reject expired entries. If you start
a driver's background work in a bubble, stop it before the bubble ends.

## Waiting for background work

When code under test starts goroutines, such as a consumer processing a job,
`synctest.Wait` blocks until all other goroutines in the bubble are durably blocked. Call it
before your assertions to let runnable work finish. It proves the goroutines
are blocked, not that a job succeeded; assert the job's observable effect too.
Inside a bubble, use your configured `consumer`, `dispatcher`, and `SendInvoice`
job, and cancel the consumer before the bubble exits:

Also import `context`. Here `payload` has an integer `InvoiceID` field and the
type expected by `SendInvoice`. Before this fragment, register a handler that
sends the processed invoice ID to a buffered `completed` channel
(`make(chan int, 1)`) only after its work succeeds:

```go
ctx, cancel := context.WithCancel(t.Context())
t.Cleanup(cancel)
go func() {
	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}()
if err := dispatcher.Dispatch(ctx, SendInvoice, payload); err != nil {
	t.Fatal(err)
}

synctest.Wait()

select {
case id := <-completed:
	if id != payload.InvoiceID {
		t.Fatalf("processed invoice = %d, want %d", id, payload.InvoiceID)
	}
default:
	t.Fatal("invoice handler did not finish")
}
```

The same applies to retries with backoff and to scheduled tasks: advance time
with `time.Sleep`, then `synctest.Wait`, then assert.

## Time in validation

Rules such as `Future` and `Past` compare a value with the current time. To
make them deterministic, set the clock on the context. Here `booking` is your
[validation schema](../validation/overview.md) and `input` is the booking value:

Import `arkoren.dev/foundation/validate` and `time` for this fragment:

```go
ctx := validate.NewClockContext(t.Context(), func() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
})

err := booking.Validate(ctx, input)
```

The schema now evaluates `Future` and `Past` against midnight UTC on January 1,
2026, regardless of when the test runs. Assert `err` according to the timestamp
and rules in your booking fixture; this context does not change the process clock.

## Real infrastructure

Bubbles only control time inside the process. A driver whose expiry is measured
by a server, such as Redis, still follows the server's clock, so integration
tests against it have to wait in real time. Keep those few and short, and test
time-dependent logic with the memory drivers. The conformance suites follow
the same split, as described in [Conformance suites](conformance-suites.md).

For the clock context helper, see the
[`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
