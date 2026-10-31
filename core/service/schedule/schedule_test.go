package schedule_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/scheduletest"
)

// wrapperDriver wraps another driver.
type wrapperDriver struct {
	// Driver forwards scheduling operations to the wrapped implementation.
	schedule.Driver
}

// Unwrap exposes the embedded driver so DriverAs can traverse the test wrapper.
// It does not change ownership or provide a different implementation.
func (driver wrapperDriver) Unwrap() schedule.Driver {
	return driver.Driver
}

// TestSchedulerConstructionPoliciesRunOnceInOrder checks all setting receivers
// and publication without starting a scheduler or running a handler.
func TestSchedulerConstructionPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *schedule.Builder
	calls := 0
	scheduler := schedule.New(scheduletest.New(), func(scheduler *schedule.Builder) {
		calls++
		retained = scheduler

		if scheduler.Logger(nil) != scheduler {
			t.Fatal("Logger changed receiver")
		}

		if scheduler.Telemetry(nil) != scheduler {
			t.Fatal("Telemetry changed receiver")
		}

		if scheduler.Events(nil) != scheduler {
			t.Fatal("Events changed receiver")
		}
	}, func(scheduler *schedule.Builder) {
		calls++

		if retained != scheduler {
			t.Fatal("callbacks received different drafts")
		}
	})
	retained.Logger(slog.New(slog.DiscardHandler))

	if calls != 2 || len(scheduler.Entries()) != 0 {
		t.Fatalf("callbacks=%d entries=%v", calls, scheduler.Entries())
	}
}

// TestSchedulerConstructionEmptyPoliciesUseFreshDrafts covers zero callbacks
// from omitted arguments and nil/empty expansion independently of nil entries.
func TestSchedulerConstructionEmptyPoliciesUseFreshDrafts(t *testing.T) {
	t.Parallel()

	var policies []func(*schedule.Builder)
	schedule.New(nil, policies...)
	schedule.New(nil, []func(*schedule.Builder){}...)
	var first *schedule.Builder
	schedule.New(nil, func(scheduler *schedule.Builder) {
		first = scheduler
	})
	schedule.New(nil, func(scheduler *schedule.Builder) {
		if scheduler == first {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestSchedulerConstructionNilPolicyPanics proves callback misuse stops
// subsequent policies without being translated into scheduler runtime errors.
func TestSchedulerConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	schedule.New(nil, func(scheduler *schedule.Builder) {
		calls++
	}, nil, func(scheduler *schedule.Builder) {
		calls++
	})
}

// TestSchedulerConstructionApplicationPanicPropagates keeps application panic
// identity distinct from the runtime handler recovery boundary.
func TestSchedulerConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	schedule.New(nil, func(scheduler *schedule.Builder) {
		panic(failure)
	})
}

// quiet returns a construction policy that discards scheduler logs without
// changing task execution or telemetry behavior.
func quiet() func(*schedule.Builder) {
	return func(scheduler *schedule.Builder) {
		scheduler.Logger(slog.New(slog.DiscardHandler))
	}
}

// start runs scheduler on a driver that returns once it has recorded the
// entries, as the fake does when its context is already done.
func start(t *testing.T, scheduler *schedule.Scheduler) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

// dispatch hands a job to the dispatch function the scheduler gave the fake,
// and returns the error the job produced.
func dispatch(t *testing.T, fake *scheduletest.Fake, name string, payload []byte) error {
	t.Helper()

	if _, err := fake.Defer(t.Context(), schedule.Job{
		Name:    name,
		Payload: payload,
		At:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	return fake.Drain(t.Context())
}

// daily declares a 03:00 UTC entry, then applies local construction policy.
// The handler is retained, not invoked, and additional callbacks run in order.
func daily(name string, fn func(context.Context) error, configure ...func(*schedule.EntryBuilder)) schedule.Entry {
	return schedule.NewEntry(name, fn, append([]func(*schedule.EntryBuilder){func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	}}, configure...)...)
}

// reminder is the typed payload used to exercise scheduling and dispatch.
// Its JSON field also makes encoded-payload assertions independent of Go names.
type reminder struct {
	// UserID identifies the recipient carried through task encoding.
	UserID int `json:"user_id"`
}

// TestSchedulerAddRejectsDuplicates checks the shared entry/task namespace
// and ensures failed batch registration does not leave partial entries.
func TestSchedulerAddRejectsDuplicates(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())
	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Add(daily("a", noop), daily("b", noop)); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.Add(daily("a", noop)); !errors.Is(err, schedule.ErrDuplicateName) {
		t.Fatalf("got %v, want ErrDuplicateName", err)
	}

	if err := scheduler.Add(daily("c", noop), daily("c", noop)); !errors.Is(err, schedule.ErrDuplicateName) {
		t.Fatalf("got %v, want ErrDuplicateName within one call", err)
	}

	if got := len(scheduler.Entries()); got != 2 {
		t.Fatalf("got %d entries, want 2: a failed Add must add nothing", got)
	}

	handler := func(context.Context, reminder) error {
		return nil
	}

	if err := scheduler.Handle(remind, handler); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.Handle(remind, handler); !errors.Is(err, schedule.ErrDuplicateName) {
		t.Fatalf("got %v, want ErrDuplicateName", err)
	}

	if err := scheduler.Handle(schedule.NewTask[reminder]("a"), handler); !errors.Is(err, schedule.ErrDuplicateName) {
		t.Fatalf("got %v, want ErrDuplicateName across entries and tasks", err)
	}

	if err := scheduler.Add(daily("remind", noop)); !errors.Is(err, schedule.ErrDuplicateName) {
		t.Fatalf("got %v, want ErrDuplicateName across tasks and entries", err)
	}
}

// TestSchedulerAddRejectsInvalidEntry checks that a missing timing declaration
// is reported at registration rather than passed to the driver.
func TestSchedulerAddRejectsInvalidEntry(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	err := scheduler.Add(schedule.NewEntry("no-spec", noop))

	if !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Fatalf("got %v, want ErrInvalidEntry", err)
	}
}

// TestSchedulerAddValidatesWholeBatchBeforeRegistration ensures a missing spec
// in a later entry does not register earlier valid entries from the same call.
func TestSchedulerAddValidatesWholeBatchBeforeRegistration(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	if err := scheduler.Add(daily("existing", noop)); err != nil {
		t.Fatal(err)
	}

	err := scheduler.Add(daily("valid", noop), schedule.NewEntry("missing-spec", noop))

	if !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Fatalf("Add error = %v", err)
	}

	entries := scheduler.Entries()

	if len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("failed batch changed registration: %v", entries)
	}
}

// TestSchedulerDriverReceivesEntriesInRegistrationOrder preserves order across
// separate Add calls and the handoff to the raw driver, not alphabetic order.
func TestSchedulerDriverReceivesEntriesInRegistrationOrder(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())

	if err := scheduler.Add(daily("z", noop), daily("a", noop)); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.Add(daily("m", noop)); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	entries := driver.Entries()

	if len(entries) != 3 {
		t.Fatalf("driver got %d entries", len(entries))
	}

	for index, name := range []string{"z", "a", "m"} {
		if entries[index].Name() != name {
			t.Fatalf("entry %d = %q, want %q", index, entries[index].Name(), name)
		}
	}
}

// TestSchedulerDriverReceivesPublishedEntry freezes declaration policy before
// registration and driver handoff, despite edits through a retained draft.
func TestSchedulerDriverReceivesPublishedEntry(t *testing.T) {
	t.Parallel()

	var retained *schedule.EntryBuilder
	entry := schedule.NewEntry("report", noop, func(entry *schedule.EntryBuilder) {
		retained = entry
		entry.When(schedule.UTC().Hour(3))
		entry.Timeout(time.Hour)
	})

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())

	if err := scheduler.Add(entry); err != nil {
		t.Fatal(err)
	}

	retained.When(schedule.UTC().Hour(4))
	retained.Timeout(-time.Second)
	retained.WithoutOverlapping()
	start(t, scheduler)

	entries := driver.Entries()

	if len(entries) != 1 {
		t.Fatalf("driver got %d entries", len(entries))
	}

	if err := entries[0].Err(); err != nil {
		t.Fatal(err)
	}

	if entries[0].Spec().Expression() != "0 3 * * *" || entries[0].TimeoutValue() != time.Hour {
		t.Fatalf("driver received changed policy: %+v", entries[0])
	}

	if !entries[0].Overlapping() {
		t.Fatal("retained draft changed overlap policy")
	}
}

// TestSchedulerHandleRejectsInvalid checks registration errors for missing
// task bodies and invalid task identities on the same empty scheduler.
func TestSchedulerHandleRejectsInvalid(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	err := scheduler.Handle(schedule.NewTask[reminder]("remind"), nil)

	if !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Fatalf("got %v, want ErrInvalidEntry for nil handler", err)
	}

	err = scheduler.Handle(schedule.NewTask[reminder]("Bad Name"), func(context.Context, reminder) error {
		return nil
	})

	if !errors.Is(err, schedule.ErrInvalidName) {
		t.Fatalf("got %v, want ErrInvalidName for bad name", err)
	}
}

// TestSchedulerRegistrationAfterRun checks that running freezes registrations
// and rejects a second Run without changing the driver's original entries.
func TestSchedulerRegistrationAfterRun(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())

	if err := scheduler.Add(daily("a", noop)); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	if err := scheduler.Add(daily("b", noop)); !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("got %v, want ErrRunning", err)
	}

	err := scheduler.Handle(schedule.NewTask[reminder]("remind"), func(context.Context, reminder) error {
		return nil
	})

	if !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("got %v, want ErrRunning", err)
	}

	if err := scheduler.Run(t.Context()); !errors.Is(err, schedule.ErrRunning) {
		t.Fatalf("got %v, want ErrRunning on second run", err)
	}

	if len(driver.Entries()) != 1 || driver.Entries()[0].Name() != "a" {
		t.Fatalf("driver got %v", driver.Entries())
	}
}

// TestSchedulerEntriesReturnsClone checks that mutating the returned slice
// cannot replace a registered entry inside the scheduler.
func TestSchedulerEntriesReturnsClone(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	if err := scheduler.Add(daily("a", noop)); err != nil {
		t.Fatal(err)
	}

	entries := scheduler.Entries()
	entries[0] = daily("changed", noop)

	if scheduler.Entries()[0].Name() != "a" {
		t.Fatal("Entries must return a copy")
	}
}

// TestSchedulerDispatchRecoversPanic checks that execution panics become
// returned errors containing the panic value and a diagnostic stack.
func TestSchedulerDispatchRecoversPanic(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	if err := scheduler.Add(daily("boom", func(context.Context) error {
		panic("kaboom")
	})); err != nil {
		t.Fatal(err)
	}

	err := scheduler.RunNow(t.Context(), "boom")

	if err == nil || !strings.Contains(err.Error(), "panic: kaboom") || !strings.Contains(err.Error(), "goroutine") {
		t.Fatalf("got %v, want a panic error with a stack", err)
	}
}

// TestSchedulerDispatchAppliesTimeout checks that entry policy supplies a
// cancelable deadline to the execution body.
func TestSchedulerDispatchAppliesTimeout(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())
	slow := func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}

	if err := scheduler.Add(daily("slow", slow, func(entry *schedule.EntryBuilder) {
		entry.Timeout(10 * time.Millisecond)
	})); err != nil {
		t.Fatal(err)
	}

	err := scheduler.RunNow(t.Context(), "slow")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want DeadlineExceeded", err)
	}
}

// TestSchedulerDispatchSkipped checks that a wrapped skip sentinel returns
// success and is logged as a skip rather than an execution failure.
func TestSchedulerDispatchSkipped(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	scheduler := schedule.New(scheduletest.New(), func(scheduler *schedule.Builder) {
		scheduler.Logger(logger)
	})

	skip := func(context.Context) error {
		return fmt.Errorf("locked: %w", schedule.ErrSkipped)
	}

	if err := scheduler.Add(daily("skip", skip)); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "skip"); err != nil {
		t.Fatalf("got %v, want nil for a skipped run", err)
	}

	if !strings.Contains(logs.String(), "skipped") || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("unexpected logs: %s", logs.String())
	}
}

// TestSchedulerDispatchLogsFailureAndSuccess checks the common execution log
// stream for successful and failed entries, including duration and error text.
func TestSchedulerDispatchLogsFailureAndSuccess(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	scheduler := schedule.New(scheduletest.New(), func(scheduler *schedule.Builder) {
		scheduler.Logger(logger)
	})

	failure := errors.New("disk full")

	if err := scheduler.Add(
		daily("ok", noop),
		daily("bad", func(context.Context) error {
			return failure
		}),
	); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "ok"); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "bad"); !errors.Is(err, failure) {
		t.Fatalf("got %v, want the failure", err)
	}

	for _, want := range []string{"level=DEBUG", "level=INFO", "duration=", "level=ERROR", "disk full"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs miss %q: %s", want, logs.String())
		}
	}
}

// TestSchedulerUnknownName checks that both driver dispatch and direct RunNow
// reject missing registrations with the same sentinel.
func TestSchedulerUnknownName(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())
	start(t, scheduler)

	if err := dispatch(t, driver, "missing", nil); !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("got %v, want ErrUnknownName", err)
	}

	if err := scheduler.RunNow(t.Context(), "missing"); !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("got %v, want ErrUnknownName", err)
	}
}

// TestSchedulerEntryDispatchedByDriver checks the registered dispatch callback
// path rather than invoking the entry body directly.
func TestSchedulerEntryDispatchedByDriver(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())

	calls := 0

	if err := scheduler.Add(daily("count", func(context.Context) error {
		calls++

		return nil
	})); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	if err := dispatch(t, driver, "count", nil); err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("got %d calls, want 1", calls)
	}
}

// TestSchedulerTaskRoundTrip checks typed payload encoding, driver storage,
// and decoding into the registered task body during a scheduled drain.
func TestSchedulerTaskRoundTrip(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())
	remind := schedule.NewTask[reminder]("remind")

	var received reminder

	err := scheduler.Handle(remind, func(_ context.Context, value reminder) error {
		received = value

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := scheduler.After(t.Context(), remind, reminder{
		UserID: 42,
	}, time.Minute); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	if err := driver.Drain(t.Context(), scheduletest.WithScheduled()); err != nil {
		t.Fatal(err)
	}

	if received.UserID != 42 {
		t.Fatalf("got %+v, want user 42", received)
	}
}

// TestSchedulerTaskBadPayload checks that a driver-delivered payload with an
// incompatible JSON field type returns a decode error.
func TestSchedulerTaskBadPayload(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())
	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	start(t, scheduler)

	if err := dispatch(t, driver, "remind", []byte(`{"user_id":"x"}`)); err == nil {
		t.Fatal("expected a decode error")
	}
}

// TestSchedulerAtAndAfterDefer checks the two timing entry points against one
// recorded driver history, including encoded payload and absolute due times.
func TestSchedulerAtAndAfterDefer(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())
	remind := schedule.NewTask[reminder]("remind")

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	when := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	id, err := scheduler.At(t.Context(), remind, reminder{
		UserID: 7,
	}, when)

	if err != nil {
		t.Fatal(err)
	}

	if id != "job-1" {
		t.Fatalf("got id %q", id)
	}

	before := time.Now()

	if _, err := scheduler.After(t.Context(), remind, reminder{
		UserID: 8,
	}, 10*time.Minute); err != nil {
		t.Fatal(err)
	}

	first, second := driver.Deferred()[0].Job, driver.Deferred()[1].Job

	if first.Name != "remind" || string(first.Payload) != `{"user_id":7}` || !first.At.Equal(when) {
		t.Fatalf("unexpected job %+v", first)
	}

	if second.At.Before(before.Add(10*time.Minute)) || second.At.After(time.Now().Add(10*time.Minute)) {
		t.Fatalf("after scheduled at %v", second.At)
	}
}

// TestSchedulerAtRejectsBadInput checks that unknown tasks and zero due times
// are rejected before a deferred job can reach the driver.
func TestSchedulerAtRejectsBadInput(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())
	remind := schedule.NewTask[reminder]("remind")

	_, err := scheduler.At(t.Context(), remind, reminder{}, time.Now())

	if !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("got %v, want ErrUnknownName", err)
	}

	if err := scheduler.Handle(remind, func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := scheduler.At(t.Context(), remind, reminder{}, time.Time{}); !errors.Is(err, schedule.ErrInvalidEntry) {
		t.Fatalf("got %v, want ErrInvalidEntry for zero time", err)
	}

	if len(driver.Deferred()) != 0 {
		t.Fatal("driver must not receive rejected jobs")
	}
}

// TestSchedulerCancel checks that the facade forwards the requested job ID
// to the driver's cancellation operation.
func TestSchedulerCancel(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(driver, quiet())

	if err := scheduler.Cancel(t.Context(), "job-1"); err != nil {
		t.Fatal(err)
	}

	if canceled := driver.Canceled(); len(canceled) != 1 || canceled[0] != "job-1" {
		t.Fatalf("got %v", canceled)
	}
}

// TestSchedulerDriverAs checks recursive wrapper traversal and rejects a type
// that does not occur anywhere in the driver chain.
func TestSchedulerDriverAs(t *testing.T) {
	t.Parallel()

	driver := scheduletest.New()
	scheduler := schedule.New(wrapperDriver{
		Driver: wrapperDriver{
			Driver: driver,
		},
	}, quiet())

	found, ok := scheduler.DriverAs[*scheduletest.Fake]()

	if !ok || found != driver {
		t.Fatal("concrete driver not found through wrappers")
	}

	if _, ok := scheduler.DriverAs[*bytes.Buffer](); ok {
		t.Fatal("unexpected match")
	}
}

// TestSchedulerRunNowIgnoresTasks checks that manual calendar execution cannot
// invoke a payload-bearing task with an invented input.
func TestSchedulerRunNowIgnoresTasks(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(scheduletest.New(), quiet())

	if err := scheduler.Handle(schedule.NewTask[reminder]("remind"), func(context.Context, reminder) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "remind"); !errors.Is(err, schedule.ErrUnknownName) {
		t.Fatalf("got %v, want ErrUnknownName", err)
	}
}

// TestSchedulerLoggerReset verifies that the final nil selection restores
// normal logging without leaving construction state attached to the scheduler.
func TestSchedulerLoggerReset(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	var retained *schedule.Builder
	scheduler := schedule.New(scheduletest.New(), func(scheduler *schedule.Builder) {
		retained = scheduler
		scheduler.Logger(logger)
		scheduler.Logger(nil)
	})
	retained.Logger(logger)

	if err := scheduler.Add(daily("reset", noop)); err != nil {
		t.Fatal(err)
	}

	if err := scheduler.RunNow(t.Context(), "reset"); err != nil {
		t.Fatal(err)
	}
}

// TestEntriesSnapshotOperations checks registration order, nil preservation,
// and filtering without exposing the scheduler's registration container.
func TestEntriesSnapshotOperations(t *testing.T) {
	t.Parallel()

	scheduler := schedule.New(nil)

	if scheduler.Entries() != nil {
		t.Fatal("empty inventory must remain nil")
	}

	first := schedule.NewEntry("zeta", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(3))
	})

	second := schedule.NewEntry("alpha", func(context.Context) error {
		return nil
	}, func(entry *schedule.EntryBuilder) {
		entry.When(schedule.UTC().Hour(4))
	})

	if err := scheduler.Add(first); err != nil {
		t.Fatal(err)
	}

	snapshot := scheduler.Entries()

	if err := scheduler.Add(second); err != nil {
		t.Fatal(err)
	}

	selected := snapshot.Filter(func(_ int, entry schedule.Entry) bool {
		return entry.Name() == "zeta"
	})

	if selected.Len() != 1 || scheduler.Entries()[1].Name() != "alpha" {
		t.Fatalf("snapshot=%v current=%v", selected, scheduler.Entries())
	}

	snapshot[0] = second

	if scheduler.Entries()[0].Name() != "zeta" {
		t.Fatal("snapshot changed registered entry")
	}
}
