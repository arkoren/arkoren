---
title: Controlling Time
description: Test process-local expiry and timers deterministically, synchronize background work, and pin validation time.
order: 50
summary:
  - Advance process-local time to assert cache expiry without real delays.
  - Synchronize timer-driven work and assert its effect before leaving a synctest bubble.
  - Pin validation time while keeping server-owned clocks in real-backend tests.
---

# Controlling Time

An expiry test should verify a lifetime, not spend that lifetime waiting. Go's `testing/synctest` runs a test inside a **bubble**, a group of goroutines that share a fake clock. Construct time-dependent drivers inside the bubble so their timestamps and background work use that clock.

Time advances when all goroutines in the bubble are durably blocked: they are waiting on work that can only be unblocked from within the bubble, such as its timers or bubble-local channels. A long `time.Sleep` can therefore finish without that much real time passing. Network I/O and mutex waits do not provide that same durable-blocking guarantee.

## Test Expiry

For a temporary cache entry, assert that the value is available first, then advance time and check absence. Import `errors`, `testing`, `testing/synctest`, `time`, `arkoren.dev/core/service/cache`, and `arkoren.dev/core/service/cache/memorycache`:

```go
func TestPreviewExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		key := cache.NewKey[string]("preview:report", func(entry *cache.KeyBuilder) {
			entry.TTL(time.Minute)
		})
		if err := store.Put(t.Context(), key, "draft"); err != nil {
			t.Fatal(err)
		}
		if value, err := store.Get(t.Context(), key); err != nil || value != "draft" {
			t.Fatalf("value = %q, error = %v; want draft and nil", value, err)
		}

		time.Sleep(time.Minute)

		if _, err := store.Get(t.Context(), key); !errors.Is(err, cache.ErrNotFound) {
			t.Fatalf("error = %v, want cache.ErrNotFound", err)
		}
	})
}
```

The memory cache treats the entry as expired at its expiry instant. This test does not start a janitor: reads check expiry independently of background cleanup. It verifies this process-local driver, not a server's expiry precision. [Cache Keys](../cache/keys.md) owns TTL configuration and key definitions.

If you start driver background work inside a bubble, register its shutdown inside that bubble too. The bubble must not finish with a worker left waiting indefinitely.

## Wait for Work

Advancing time and finishing work are separate events. `synctest.Wait` waits until the other goroutines in the bubble are durably blocked; it does not assert that an application operation succeeded.

This complete test demonstrates the distinction using a timer-driven notification. A buffered channel captures the observable result without blocking the producer:

```go
func TestTimerNotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		completed := make(chan string, 1)
		go func() {
			time.Sleep(time.Hour)
			completed <- "ready"
		}()
		synctest.Wait()
		time.Sleep(time.Hour)
		synctest.Wait()

		select {
		case value := <-completed:
			if value != "ready" {
				t.Fatalf("notification = %q, want ready", value)
			}
		default:
			t.Fatal("timer produced no notification")
		}
	})
}
```

The first wait lets the worker reach its timer; the second lets work made runnable by the elapsed hour settle before the assertion. The nonblocking receive fails rather than waiting for an absent effect.

For a long-running consumer, cancel its context, wait for it to return, and check unexpected runner errors before leaving the bubble. Assert successful handler effects separately from runner shutdown. Use the [workflow testing guide](../workflows/testing.md) for executing workflows and the [scheduler guide](../scheduled-tasks/overview.md) for scheduled execution rather than substituting recording fakes for engines.

## Pin Validation

When the test concerns a `Future` or `Past` rule rather than timers, set validation's context clock. Inside a test, import `arkoren.dev/foundation/validate` and construct a scalar rule with an explicit reference instant:

```go
now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
ctx := validate.NewClockContext(t.Context(), func() time.Time {
	return now
})
rule := validate.Time(func(moment *validate.TimeBuilder) {
	moment.Future()
})
if err := rule.Validate(ctx, now.Add(time.Minute)); err != nil {
	t.Fatal(err)
}
if err := rule.Validate(ctx, now.Add(-time.Minute)); err == nil {
	t.Fatal("past time passed a Future rule")
}
```

Both assertions use the same instant regardless of the date on the machine. This context controls validation rules, not the process clock or another service's timers. For structured input, pass it to your existing [validation schema](../validation/overview.md).

## Respect Server Time

A synctest bubble cannot advance Redis, a database server, or a remote workflow engine. Use ordinary time in [integration tests](integration-tests.md) for server-owned expiry and deadlines. Allow for the backend's documented precision and bound how long the test waits; repeatedly sleeping does not establish that a write or delivery succeeded.

Keep most policy and timer tests in process, and retain real-backend cases for behavior that depends on that backend. The [conformance clock options](conformance-suites.md#declare-capabilities) make the same distinction.

See Go's [`testing/synctest` reference](https://pkg.go.dev/testing/synctest) and the [`validate` API reference](https://pkg.go.dev/arkoren.dev/foundation/validate).
