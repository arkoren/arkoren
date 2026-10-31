package scheduletest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/schedule"
)

// ErrNotRunning is returned by [Fake.Drain] when [Fake.Run] was never called,
// so there is no dispatch function to hand the jobs to.
var ErrNotRunning = errors.New("scheduletest: fake is not running")

// ErrDrainLimit is returned by [Fake.Drain] when jobs keep deferring new due
// jobs, so it stopped instead of looping forever.
var ErrDrainLimit = errors.New("scheduletest: drain limit reached")

// maxDrained caps the jobs one [Fake.Drain] dispatches.
const maxDrained = 10_000

// DeferredJob is one recorded [schedule.Driver.Defer].
type DeferredJob struct {
	// ID is the identifier the fake returned.
	ID string

	// Job is the job as given to Defer.
	Job schedule.Job
}

// DrainOption changes one [Fake.Drain] call.
type DrainOption func(*drainOptions)

// drainOptions are the settings of one [Fake.Drain].
type drainOptions struct {
	// scheduled includes future jobs as well as those already due.
	scheduled bool
}

// WithScheduled makes [Fake.Drain] dispatch jobs that are not due yet too,
// ordered by their time, so a test need not wait for the clock.
func WithScheduled() DrainOption {
	return func(options *drainOptions) {
		options.scheduled = true
	}
}

// Fake is a [schedule.Driver] for unit tests. It records the calls of the code
// under test and fires nothing by itself.
//
// [Fake.Run] records the entries and the dispatch function, then blocks until
// its context is done and returns nil; entries never fire, so test an entry
// body with [schedule.Scheduler.RunNow]. [Fake.Defer] records the job and
// keeps it pending until [Fake.Drain] dispatches it or [Fake.Cancel] removes
// it. Because it fires nothing, Fake does not run the [Run] suite.
//
// A Fake is safe for concurrent use. Create it with [New].
type Fake struct {
	// mu guards registrations, recorded calls, pending jobs, and failures.
	mu sync.Mutex

	// entries records the most recent Run registration in its original order.
	entries []schedule.Entry

	// dispatch is the callback installed by the most recent Run.
	dispatch schedule.Dispatch

	// deferred records every successful Defer call, including drained jobs.
	deferred []DeferredJob

	// pending contains jobs that have not been canceled or dispatched.
	pending []DeferredJob

	// cancels records successful Cancel calls in arrival order.
	cancels []string

	// nextID increases for each successfully deferred job until Reset.
	nextID int

	// failWith is the persistent failure injected into driver operations.
	failWith error

	// failNext overrides failWith once, then is cleared by injected.
	failNext error
}

var _ schedule.Driver = (*Fake)(nil)

// New returns an empty Fake.
//
// # Examples
//
//	fake := scheduletest.New()
//	scheduler := schedule.New(fake)
//	_ = scheduler.Handle(Remind, remind)
//	_, err := scheduler.After(ctx, Remind, Reminder{
//		UserID: 42,
//	}, time.Hour)
//	fake.AssertDeferred(t, Remind, func(r Reminder) bool {
//		return r.UserID == 42
//	})
func New() *Fake {
	return &Fake{}
}

// FailWith makes every following driver call return err, until FailWith(nil).
// Failed calls are not recorded.
func (fake *Fake) FailWith(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failWith = err
}

// FailNext makes the next driver call return err. It takes precedence over
// [Fake.FailWith] for that call.
func (fake *Fake) FailNext(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.failNext = err
}

// Reset forgets entries, the dispatch function, recorded and pending jobs, and
// injected failures.
func (fake *Fake) Reset() {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	fake.entries, fake.dispatch = nil, nil
	fake.deferred, fake.pending, fake.cancels = nil, nil, nil
	fake.nextID = 0
	fake.failWith, fake.failNext = nil, nil
}

// injected returns the failure for the current call. The caller holds the
// lock.
func (fake *Fake) injected() error {
	if err := fake.failNext; err != nil {
		fake.failNext = nil

		return err
	}

	return fake.failWith
}

// Entries returns the entries of the last [Fake.Run].
func (fake *Fake) Entries() []schedule.Entry {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.entries)
}

// Deferred returns every recorded Defer call in order, including jobs that were
// canceled or drained since.
func (fake *Fake) Deferred() []DeferredJob {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.deferred)
}

// Pending returns the deferred jobs that were neither drained nor canceled.
func (fake *Fake) Pending() []DeferredJob {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.pending)
}

// Canceled returns the identifiers given to Cancel in order.
func (fake *Fake) Canceled() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	return slices.Clone(fake.cancels)
}

// Run records entries and dispatch, then blocks until ctx is done. It returns
// nil on cancellation.
func (fake *Fake) Run(ctx context.Context, entries []schedule.Entry, dispatch schedule.Dispatch) error {
	fake.mu.Lock()

	if err := fake.injected(); err != nil {
		fake.mu.Unlock()

		return err
	}

	fake.entries = slices.Clone(entries)
	fake.dispatch = dispatch
	fake.mu.Unlock()

	<-ctx.Done()

	return nil
}

// Defer records job and returns a new identifier. The job stays pending until
// [Fake.Drain] or [Fake.Cancel].
func (fake *Fake) Defer(_ context.Context, job schedule.Job) (string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if err := fake.injected(); err != nil {
		return "", err
	}

	fake.nextID++
	job.Payload = slices.Clone(job.Payload)
	deferred := DeferredJob{
		ID:  "job-" + strconv.Itoa(fake.nextID),
		Job: job,
	}
	fake.deferred = append(fake.deferred, deferred)
	fake.pending = append(fake.pending, deferred)

	return deferred.ID, nil
}

// Cancel records id and removes the pending job. An unknown identifier is not
// an error.
func (fake *Fake) Cancel(_ context.Context, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	if err := fake.injected(); err != nil {
		return err
	}

	fake.cancels = append(fake.cancels, id)
	fake.pending = slices.DeleteFunc(fake.pending, func(pending DeferredJob) bool {
		return pending.ID == id
	})

	return nil
}

// Drain hands the pending jobs that are due now to dispatch, in order of their
// time, and removes each before it runs. With [WithScheduled] jobs in the
// future run too. Jobs that dispatch defers meanwhile run in the same call
// when they qualify; [ErrDrainLimit] stops a loop of jobs that defer jobs.
// The jobs go to the dispatch function given to [Fake.Run], so a
// [schedule.Scheduler] that runs on the fake dispatches to its task handlers;
// Drain returns [ErrNotRunning] before Run was called.
//
// Drain continues after a failing job and returns the errors joined.
//
// # Examples
//
//	err := fake.Drain(ctx, scheduletest.WithScheduled())
func (fake *Fake) Drain(ctx context.Context, opts ...DrainOption) error {
	var options drainOptions

	for _, opt := range opts {
		opt(&options)
	}

	fake.mu.Lock()

	dispatch := fake.dispatch
	fake.mu.Unlock()

	if dispatch == nil {
		return ErrNotRunning
	}

	var errs []error

	for range maxDrained {
		job, ok := fake.take(options.scheduled)

		if !ok {
			return errors.Join(errs...)
		}

		if err := dispatch(ctx, job.Name, job.Payload); err != nil {
			errs = append(errs, fmt.Errorf("drain %s: %w", job.Name, err))
		}
	}

	return errors.Join(append(errs, ErrDrainLimit)...)
}

// take removes and returns the earliest qualifying pending job.
func (fake *Fake) take(scheduled bool) (schedule.Job, bool) {
	fake.mu.Lock()
	defer fake.mu.Unlock()

	now := time.Now()
	best := -1

	for index, pending := range fake.pending {
		if !scheduled && pending.Job.At.After(now) {
			continue
		}

		if best < 0 || pending.Job.At.Before(fake.pending[best].Job.At) {
			best = index
		}
	}

	if best < 0 {
		return schedule.Job{}, false
	}

	job := fake.pending[best].Job
	fake.pending = slices.Delete(fake.pending, best, best+1)

	return job, true
}

// matchingJobs returns the recorded jobs called name whose payload decodes as
// T and satisfies every predicate.
func matchingJobs[T any](jobs []DeferredJob, name string, preds []func(T) bool) []DeferredJob {
	return slices.DeleteFunc(slices.Clone(jobs), func(deferred DeferredJob) bool {
		if deferred.Job.Name != name {
			return true
		}

		var payload T

		if err := json.Unmarshal(deferred.Job.Payload, &payload); err != nil {
			return true
		}

		return slices.ContainsFunc(preds, func(pred func(T) bool) bool {
			return !pred(payload)
		})
	})
}

// AssertDeferred fails the test unless a job of task was deferred whose
// payload satisfies every predicate. Without predicates any job of task
// matches.
//
// # Examples
//
//	fake.AssertDeferred(t, Remind, func(r Reminder) bool {
//		return r.UserID == 42
//	})
func (fake *Fake) AssertDeferred[T any](t testing.TB, task schedule.Task[T], preds ...func(T) bool) {
	t.Helper()

	if len(matchingJobs(fake.Deferred(), task.Name(), preds)) == 0 {
		t.Errorf("no job %q was deferred with a matching payload; deferred: %s", task.Name(), describe(fake.Deferred()))
	}
}

// AssertNotDeferred fails the test when a job of task was deferred whose
// payload satisfies every predicate. Without predicates any job of task fails
// the test.
//
// # Examples
//
//	fake.AssertNotDeferred(t, Remind)
func (fake *Fake) AssertNotDeferred[T any](t testing.TB, task schedule.Task[T], preds ...func(T) bool) {
	t.Helper()

	if matched := matchingJobs(fake.Deferred(), task.Name(), preds); len(matched) > 0 {
		t.Errorf("job %q was deferred %d time(s) with a matching payload, want none", task.Name(), len(matched))
	}
}

// AssertDeferredCount fails the test unless jobs of task were deferred
// exactly n times.
//
// # Examples
//
//	fake.AssertDeferredCount(t, Remind, 2)
func (fake *Fake) AssertDeferredCount[T any](t testing.TB, task schedule.Task[T], n int) {
	t.Helper()

	if got := len(matchingJobs[any](fake.Deferred(), task.Name(), nil)); got != n {
		t.Errorf("job %q was deferred %d time(s), want %d", task.Name(), got, n)
	}
}

// AssertNothingDeferred fails the test when any job was deferred.
func (fake *Fake) AssertNothingDeferred(t testing.TB) {
	t.Helper()

	if deferred := fake.Deferred(); len(deferred) > 0 {
		t.Errorf("want no deferred job; deferred: %s", describe(deferred))
	}
}

// AssertCanceled fails the test unless Cancel was called with id.
func (fake *Fake) AssertCanceled(t testing.TB, id string) {
	t.Helper()

	if !slices.Contains(fake.Canceled(), id) {
		t.Errorf("job %q was not canceled", id)
	}
}

// describe lists jobs for failure messages.
func describe(jobs []DeferredJob) string {
	if len(jobs) == 0 {
		return "none"
	}

	described := make([]string, len(jobs))

	for index, deferred := range jobs {
		described[index] = fmt.Sprintf("%s(%s)", deferred.Job.Name, deferred.Job.Payload)
	}

	return fmt.Sprint(described)
}
