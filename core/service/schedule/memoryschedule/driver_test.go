package memoryschedule_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/core/service/schedule/memoryschedule"
	"arkoren.dev/core/service/schedule/scheduletest"
)

// TestConstructionRetainedDraftCannotReplaceLogger exercises the selected
// logger through a timer dispatch failure, with virtual time and joined work.
func TestConstructionRetainedDraftCannotReplaceLogger(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		var retained *memoryschedule.Builder
		driver := memoryschedule.New(func(scheduler *memoryschedule.Builder) {
			retained = scheduler
			scheduler.Logger(slog.New(slog.NewTextHandler(&logs, nil)))
		})
		retained.Logger(slog.New(slog.DiscardHandler))

		entry := schedule.NewEntry("tick", noop, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(time.Second))
		})

		stop, done := start(t, driver, []schedule.Entry{entry}, func(context.Context, string, []byte) error {
			return errors.New("dispatch failure")
		})
		synctest.Sleep(time.Second + time.Millisecond)
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(logs.String(), "schedule dispatch failed") {
			t.Fatalf("selected logger lost: %s", logs.String())
		}
	})
}

// TestConstructionPoliciesRunOnceInOrder checks fresh owned drafts and Logger
// receiver identity without starting timers or dispatching scheduled work.
func TestConstructionPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *memoryschedule.Builder
	calls := 0
	memoryschedule.New(func(scheduler *memoryschedule.Builder) {
		retained = scheduler
		calls++

		if scheduler.Logger(slog.New(slog.DiscardHandler)) != scheduler {
			t.Fatal("Logger changed receiver")
		}
	}, func(scheduler *memoryschedule.Builder) {
		calls++

		if scheduler != retained {
			t.Fatal("callbacks received different drafts")
		}

		scheduler.Logger(nil)
	})
	retained.Logger(nil)

	if calls != 2 {
		t.Fatalf("callbacks=%d", calls)
	}

	memoryschedule.New(func(scheduler *memoryschedule.Builder) {
		if scheduler == retained {
			t.Fatal("construction reused a draft")
		}
	})
}

// TestConstructionEmptyPolicies preserves defaults for omitted callbacks and
// expanded nil or empty lists, without starting runtime timer goroutines.
func TestConstructionEmptyPolicies(t *testing.T) {
	t.Parallel()

	var policies []func(*memoryschedule.Builder)
	memoryschedule.New(policies...)
	memoryschedule.New([]func(*memoryschedule.Builder){}...)
	memoryschedule.New(func(scheduler *memoryschedule.Builder) {
	})
}

// TestConstructionNilPolicyPanics preserves direct invocation and prevents
// later callbacks from running after a supplied nil entry.
func TestConstructionNilPolicyPanics(t *testing.T) {
	t.Parallel()

	calls := 0
	defer func() {
		if recover() == nil || calls != 1 {
			t.Fatalf("nil policy did not interrupt in order: calls=%d", calls)
		}
	}()
	memoryschedule.New(func(scheduler *memoryschedule.Builder) {
		calls++
	}, nil, func(scheduler *memoryschedule.Builder) {
		calls++
	})
}

// TestConstructionApplicationPanicPropagates preserves application panic
// identity, independently of the driver's runtime scheduling behavior.
func TestConstructionApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	failure := "construction policy"
	defer func() {
		if recover() != failure {
			t.Fatal("application panic was replaced")
		}
	}()
	memoryschedule.New(func(scheduler *memoryschedule.Builder) {
		panic(failure)
	})
}

// TestConformance runs the shared scheduling contract with a fresh in-process
// driver per scenario and virtual time for calendar and deferred execution.
func TestConformance(t *testing.T) {
	scheduletest.Run(t, func(*testing.T) schedule.Driver {
		return memoryschedule.New()
	}, scheduletest.WithSynctest())
}

// recorder collects dispatched names with their fake time offsets.
type recorder struct {
	// mu guards the recorded calls and their offsets.
	mu sync.Mutex

	// start anchors offsets to the beginning of the synctest scenario.
	start time.Time

	// calls records dispatched names in arrival order.
	calls []string

	// times stores one elapsed offset for each element of calls.
	times []time.Duration
}

// dispatch records one call at the current fake time and always succeeds.
// Payloads are ignored because these tests assert firing and ordering only.
func (recorder *recorder) dispatch(_ context.Context, name string, _ []byte) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	recorder.calls = append(recorder.calls, name)
	recorder.times = append(recorder.times, time.Since(recorder.start))

	return nil
}

// count reads the number of dispatches while callbacks may still be running.
// The lock keeps assertions independent of the callback goroutine's schedule.
func (recorder *recorder) count() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return len(recorder.calls)
}

// snapshot returns detached, corresponding name and offset slices so callers
// can inspect ordering without retaining mutable recorder storage.
func (recorder *recorder) snapshot() ([]string, []time.Duration) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	return slices.Clone(recorder.calls), slices.Clone(recorder.times)
}

// noop supplies a successful entry body for driver-only timing tests. The
// registered dispatch callback, rather than this body, records each firing.
func noop(context.Context) error {
	return nil
}

// start launches Run inside the current synctest bubble and waits for it to
// block. The caller must invoke stop and receive done to finish the driver.
func start(t *testing.T, driver *memoryschedule.Driver, entries []schedule.Entry, dispatch schedule.Dispatch) (stop func(), done chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done = make(chan error, 1)

	go func() {
		done <- driver.Run(ctx, entries, dispatch)
	}()

	synctest.Wait()

	return cancel, done
}

// TestDriverEveryFiresOnTick checks exact aligned execution offsets across
// several interval ticks using only virtual elapsed time.
func TestDriverEveryFiresOnTick(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		recorder := &recorder{
			start: time.Now(),
		}
		entry := schedule.NewEntry("tick", noop, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(10 * time.Second))
		})

		stop, done := start(t, memoryschedule.New(), []schedule.Entry{entry}, recorder.dispatch)

		synctest.Sleep(30*time.Second + time.Millisecond)
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if got := recorder.count(); got != 3 {
			t.Fatalf("got %d runs, want 3", got)
		}

		_, times := recorder.snapshot()

		for index, offset := range times {
			if want := time.Duration(index+1) * 10 * time.Second; offset != want {
				t.Fatalf("run %d at %s, want %s", index, offset, want)
			}
		}
	})
}

// TestDriverSkipsOverlappingRun checks that a busy non-overlapping entry skips
// intermediate ticks and becomes eligible again after its invocation ends.
func TestDriverSkipsOverlappingRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int32

		dispatch := func(context.Context, string, []byte) error {
			runs.Add(1)
			time.Sleep(25 * time.Second)

			return nil
		}

		entry := schedule.NewEntry("slow", noop, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(10 * time.Second))
			entry.WithoutOverlapping()
		})

		stop, done := start(t, memoryschedule.New(), []schedule.Entry{entry}, dispatch)

		synctest.Sleep(40*time.Second + time.Millisecond)
		stop()
		<-done

		// Runs start at 10s and 40s; ticks at 20s and 30s are skipped.
		if got := runs.Load(); got != 2 {
			t.Fatalf("got %d runs, want 2", got)
		}
	})
}

// TestDriverDeferFires checks that a deferred job dispatches at its absolute
// due instant rather than at registration or the next calendar tick.
func TestDriverDeferFires(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		recorder := &recorder{
			start: time.Now(),
		}
		driver := memoryschedule.New()
		stop, done := start(t, driver, nil, recorder.dispatch)

		_, err := driver.Defer(t.Context(), schedule.Job{
			Name: "job",
			At:   time.Now().Add(5 * time.Second),
		})

		if err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(6 * time.Second)

		calls, times := recorder.snapshot()

		if len(calls) != 1 || times[0] != 5*time.Second {
			t.Fatalf("got calls %v at %v", calls, times)
		}

		stop()
		<-done
	})
}

// TestDriverCancelPreventsFiring checks removal of a pending timer and the
// contract's successful no-op cancellation of unknown identifiers.
func TestDriverCancelPreventsFiring(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		recorder := &recorder{
			start: time.Now(),
		}
		driver := memoryschedule.New()
		stop, done := start(t, driver, nil, recorder.dispatch)

		id, err := driver.Defer(t.Context(), schedule.Job{
			Name: "job",
			At:   time.Now().Add(5 * time.Second),
		})

		if err != nil {
			t.Fatal(err)
		}

		if err := driver.Cancel(t.Context(), id); err != nil {
			t.Fatal(err)
		}

		if err := driver.Cancel(t.Context(), "unknown"); err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(10 * time.Second)

		if got := recorder.count(); got != 0 {
			t.Fatalf("got %d runs, want 0", got)
		}

		stop()
		<-done
	})
}

// TestDriverDeferOwnsPayload checks raw Driver submissions retain accepted
// bytes independently of the caller, including jobs queued before Run starts.
func TestDriverDeferOwnsPayload(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()
		payload := []byte(`"original"`)

		if _, err := driver.Defer(t.Context(), schedule.Job{
			Name:    "work",
			Payload: payload,
			At:      time.Now(),
		}); err != nil {
			t.Fatal(err)
		}

		copy(payload, `"modified"`)

		received := make(chan string, 1)
		stop, done := start(t, driver, nil, func(_ context.Context, _ string, payload []byte) error {
			received <- string(payload)

			return nil
		})

		got := <-received
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if got != `"original"` {
			t.Fatalf("accepted payload = %q, want original", got)
		}
	})
}

// TestDriverCanceledDeferDoesNotSchedule verifies a canceled submission has
// no identifier or later dispatch, using only the public Driver boundary.
func TestDriverCanceledDeferDoesNotSchedule(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		id, err := driver.Defer(ctx, schedule.Job{Name: "work", At: time.Now()})

		if !errors.Is(err, context.Canceled) || id != "" {
			t.Errorf("canceled Defer = %q, %v", id, err)
		}

		runs := 0
		stop, done := start(t, driver, nil, func(context.Context, string, []byte) error {
			runs++

			return nil
		})
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if runs != 0 {
			t.Fatalf("canceled submission dispatched %d times", runs)
		}
	})
}

// TestDriverDeferOwnsPayloadWhileRunning covers the armed-timer submission
// path independently of pending-before-Run payload capture.
func TestDriverDeferOwnsPayloadWhileRunning(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()
		received := make(chan string, 1)
		stop, done := start(t, driver, nil, func(_ context.Context, _ string, payload []byte) error {
			received <- string(payload)

			return nil
		})

		payload := []byte(`"original"`)

		if _, err := driver.Defer(t.Context(), schedule.Job{
			Name:    "work",
			Payload: payload,
			At:      time.Now().Add(time.Second),
		}); err != nil {
			stop()
			<-done
			t.Fatal(err)
		}

		copy(payload, `"modified"`)
		synctest.Sleep(time.Second)
		synctest.Wait()
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if got := <-received; got != `"original"` {
			t.Fatalf("armed payload = %q, want original", got)
		}
	})
}

// TestDriverCanceledCancelKeepsJob verifies a rejected cancellation leaves
// the accepted job available for dispatch rather than silently removing it.
func TestDriverCanceledCancelKeepsJob(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()

		id, err := driver.Defer(t.Context(), schedule.Job{Name: "work", At: time.Now()})

		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if err := driver.Cancel(ctx, id); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled Cancel = %v", err)
		}

		runs := 0
		stop, done := start(t, driver, nil, func(context.Context, string, []byte) error {
			runs++

			return nil
		})
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if runs != 1 {
			t.Fatalf("accepted job dispatched %d times, want 1", runs)
		}
	})
}

// TestDriverDeferBeforeRun checks that pre-registered jobs survive until Run,
// with overdue jobs firing before jobs whose due times are still ahead.
func TestDriverDeferBeforeRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		recorder := &recorder{
			start: time.Now(),
		}
		driver := memoryschedule.New()

		_, err := driver.Defer(t.Context(), schedule.Job{
			Name: "later",
			At:   time.Now().Add(10 * time.Second),
		})

		if err != nil {
			t.Fatal(err)
		}

		_, err = driver.Defer(t.Context(), schedule.Job{
			Name: "past",
			At:   time.Now().Add(-time.Hour),
		})

		if err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(5 * time.Second)

		stop, done := start(t, driver, nil, recorder.dispatch)

		synctest.Sleep(time.Second)

		if calls, _ := recorder.snapshot(); !slices.Equal(calls, []string{"past"}) {
			t.Fatalf("got calls %v, want [past]", calls)
		}

		synctest.Sleep(10 * time.Second)

		if calls, _ := recorder.snapshot(); !slices.Equal(calls, []string{"past", "later"}) {
			t.Fatalf("got calls %v, want [past later]", calls)
		}

		stop()
		<-done
	})
}

// TestDriverShutdownWaitsForRun checks that canceling Run stops scheduling but
// does not return before an already executing dispatch has finished.
func TestDriverShutdownWaitsForRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var finished atomic.Bool

		dispatch := func(context.Context, string, []byte) error {
			time.Sleep(20 * time.Second)
			finished.Store(true)

			return nil
		}

		entry := schedule.NewEntry("slow", noop, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(10 * time.Second))
		})

		stop, done := start(t, memoryschedule.New(), []schedule.Entry{entry}, dispatch)

		synctest.Sleep(11 * time.Second)
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if !finished.Load() {
			t.Fatal("Run returned before the in-flight run finished")
		}
	})
}

// TestDriverRunTwiceReturnsError checks that a second concurrent Run cannot
// replace the active driver's dispatch callback or execution state.
func TestDriverRunTwiceReturnsError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()
		stop, done := start(t, driver, nil, (&recorder{}).dispatch)

		err := driver.Run(t.Context(), nil, (&recorder{}).dispatch)

		if !errors.Is(err, memoryschedule.ErrAlreadyRunning) {
			t.Fatalf("got %v, want ErrAlreadyRunning", err)
		}

		stop()
		<-done
	})
}

// TestDriverWithSchedulerRunsDeferredTask checks typed task delivery alongside
// calendar entries through the real scheduler-to-memory-driver boundary.
func TestDriverWithSchedulerRunsDeferredTask(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		scheduler := schedule.New(memoryschedule.New())
		task := schedule.NewTask[string]("greet")

		var got atomic.Value

		if err := scheduler.Handle(task, func(_ context.Context, name string) error {
			got.Store(name)

			return nil
		}); err != nil {
			t.Fatal(err)
		}

		var ticks atomic.Int32

		entry := schedule.NewEntry("tick", func(context.Context) error {
			ticks.Add(1)

			return nil
		}, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(10 * time.Second))
		})

		if err := scheduler.Add(entry); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			done <- scheduler.Run(ctx)
		}()

		synctest.Wait()

		if _, err := scheduler.After(ctx, task, "world", 15*time.Second); err != nil {
			t.Fatal(err)
		}

		synctest.Sleep(21 * time.Second)
		cancel()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if got.Load() != "world" || ticks.Load() != 2 {
			t.Fatalf("got payload %v and %d ticks", got.Load(), ticks.Load())
		}
	})
}

// TestDriverRejectsRunWhileDraining checks the concurrent-Run guard remains
// active until admitted executions finish, not merely until timers stop.
func TestDriverRejectsRunWhileDraining(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()

		if _, err := driver.Defer(t.Context(), schedule.Job{Name: "work", At: time.Now()}); err != nil {
			t.Fatal(err)
		}

		release := make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)

		go func() {
			done <- driver.Run(ctx, nil, func(context.Context, string, []byte) error {
				<-release

				return nil
			})
		}()

		synctest.Wait()
		cancel()
		synctest.Wait()

		second := make(chan error, 1)

		go func() {
			second <- driver.Run(ctx, nil, func(context.Context, string, []byte) error {
				return nil
			})
		}()

		synctest.Wait()

		completed := false

		select {
		case err := <-second:
			completed = true

			if !errors.Is(err, memoryschedule.ErrAlreadyRunning) {
				t.Errorf("concurrent Run = %v, want ErrAlreadyRunning", err)
			}
		default:
			t.Error("second Run was admitted while the first was draining")
		}

		close(release)

		if err := <-done; err != nil {
			t.Error(err)
		}

		if !completed {
			<-second
		}
	})
}

// TestSchedulerDrainRetainsEntryDeadline characterizes intentional draining:
// runner cancellation leaves admitted execution live until its own timeout.
func TestSchedulerDrainRetainsEntryDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		scheduler := schedule.New(memoryschedule.New())
		started := make(chan context.Context, 1)

		if err := scheduler.Add(schedule.NewEntry("work", func(ctx context.Context) error {
			started <- ctx
			<-ctx.Done()

			return ctx.Err()
		}, func(entry *schedule.EntryBuilder) {
			entry.When(schedule.UTC().Every(time.Second))
			entry.Timeout(5 * time.Second)
		})); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		done := make(chan error, 1)

		go func() {
			done <- scheduler.Run(ctx)
		}()

		synctest.Wait()
		synctest.Sleep(time.Second)

		execution := <-started
		cancel()
		synctest.Wait()

		if err := execution.Err(); err != nil {
			t.Errorf("runner cancellation reached admitted execution: %v", err)
		}

		synctest.Sleep(5 * time.Second)

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if !errors.Is(execution.Err(), context.DeadlineExceeded) {
			t.Fatalf("execution ended without its own deadline: %v", execution.Err())
		}
	})
}

// TestDriverDefersDuringDrainForNextRun verifies submissions accepted after
// timer admission closes remain pending for a later raw-driver Run.
func TestDriverDefersDuringDrainForNextRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memoryschedule.New()

		if _, err := driver.Defer(t.Context(), schedule.Job{Name: "first", At: time.Now()}); err != nil {
			t.Fatal(err)
		}

		release := make(chan struct{})
		stop, done := start(t, driver, nil, func(context.Context, string, []byte) error {
			<-release

			return nil
		})
		stop()
		synctest.Wait()

		_, err := driver.Defer(t.Context(), schedule.Job{Name: "next", At: time.Now()})
		close(release)

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if err != nil {
			t.Fatal(err)
		}

		received := make(chan string, 1)
		stop, done = start(t, driver, nil, func(_ context.Context, entryName string, _ []byte) error {
			received <- entryName

			return nil
		})
		stop()

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if got := <-received; got != "next" {
			t.Fatalf("next Run received %q, want next", got)
		}
	})
}
