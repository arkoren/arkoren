// Package health owns check execution and cached background monitoring.
package health

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"arkoren.dev/core/application/internal/lifecycle"
)

// check records one named health callback.
type check struct {
	// Name labels the check's failures.
	Name string

	// fn evaluates the condition.
	fn func(context.Context) error
}

// Result is the outcome of one check in a health pass.
type Result struct {
	// Name is the check's registered name.
	Name string

	// Err is the check's failure, or nil when it passed.
	Err error

	// changedAt is when the check last changed between passing and failing, or
	// when it first completed. [readiness.store] sets it; runChecks leaves it
	// zero.
	ChangedAt time.Time
}

// Add appends a validated health callback under the registry lock.
// Duplicate names are programming errors; the lifecycle scope owns admission.
func (monitor *Monitor) Add(name string, fn func(context.Context) error) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()

	for _, item := range monitor.checks {
		if item.Name == name {
			panic(fmt.Errorf("app: Check %q: %w", name, ErrDuplicateCheck))
		}
	}

	monitor.checks = append(monitor.checks, check{Name: name, fn: fn})
}

// Status is the cached state of one health check as of the latest
// completed background pass. Monitor.Checks returns detached records;
// error values remain shared, and callbacks do not run when reading the cache.
type Status struct {
	// Name is the name the check was registered with.
	// Example: for _, status := range app.Checks() { fmt.Println(status.Name) }
	Name string

	// Err is the failure of the latest pass, or nil when the check passed. A
	// panicking check reports the recovered value.
	// Example: if status.Err != nil { logger.Warn("dependency unavailable", "err", status.Err) }
	Err error

	// Since is when the check last changed between passing and failing, or
	// when it first completed if it never changed. It is the time of the pass
	// that observed the change.
	// Example: fmt.Println(status.Since.UTC())
	Since time.Time

	// CheckedAt is when the latest pass that ran the check finished.
	// Example: age := time.Since(status.CheckedAt); fmt.Println(age)
	CheckedAt time.Time
}

// Checks returns the state of every health check as of the latest completed
// background pass, in registration order. It never runs a check, so it is as
// cheap as the readiness probe and shows what the probe sees. It returns nil
// until the first pass completes: before the monitor runs, when no check
// is registered, and for a short time after Start. Use [Monitor.CheckHealth]
// to run the checks now. The result is a copy.
//
// Example:
func (monitor *Monitor) Checks() []Status {
	snapshot := monitor.cache.snapshot()

	if snapshot.Results == nil {
		return nil
	}

	statuses := make([]Status, len(snapshot.Results))

	for index, result := range snapshot.Results {
		statuses[index] = Status{
			Name:      result.Name,
			Err:       result.Err,
			Since:     result.ChangedAt,
			CheckedAt: snapshot.CompletedAt,
		}
	}

	return statuses
}

// RunChecks runs every registered check now, concurrently and without
// holding any lock, and returns one [Status] per check in registration
// order, passing and failing alike. Each callback receives a context deadline
// from [Builder.ReadinessTimeout] and ctx. Callbacks must honor cancellation;
// RunChecks waits for all of them and cannot forcibly interrupt user code.
// Since is zero, because
// a single run has no history, and CheckedAt is when the run finished. It
// returns nil when no check is registered. A panicking check reports the
// recovered value. Use [Monitor.Checks] for the cached state instead.
//
// Example:
func (monitor *Monitor) RunChecks(ctx context.Context) []Status {
	ctx, cancel := context.WithTimeout(ctx, monitor.timeout)
	defer cancel()

	results := monitor.runChecks(ctx)

	if len(results) == 0 {
		return nil
	}

	checkedAt := time.Now()
	statuses := make([]Status, len(results))

	for index, result := range results {
		statuses[index] = Status{Name: result.Name, Err: result.Err, CheckedAt: checkedAt}
	}

	return statuses
}

// CheckHealth runs every registered check as [Monitor.RunChecks] does and
// returns their failures joined in registration order, each prefixed with its
// check name. It returns nil when every check passes. ctx is passed to each
// check with the RunChecks timeout; checks must cooperate with cancellation.
// This explicit run does not update the cached readiness result.
func (monitor *Monitor) CheckHealth(ctx context.Context) error {
	var errs []error

	for _, status := range monitor.RunChecks(ctx) {
		if status.Err != nil {
			errs = append(errs, fmt.Errorf("health %s: %w", status.Name, status.Err))
		}
	}

	return errors.Join(errs...)
}

// JoinFailures joins the failures in results in order, each prefixed with
// its check name. It returns nil when every check passed.
func JoinFailures(results []Result) error {
	errs := make([]error, 0, len(results))

	for _, result := range results {
		if result.Err != nil {
			errs = append(errs, fmt.Errorf("health %s: %w", result.Name, result.Err))
		}
	}

	return errors.Join(errs...)
}

// runChecks runs every registered check concurrently without holding any
// lock and returns one result per check in registration order. A panicking
// check fails with the recovered value.
func (monitor *Monitor) runChecks(ctx context.Context) []Result {
	monitor.mu.Lock()

	checks := monitor.checks
	monitor.mu.Unlock()

	results := make([]Result, len(checks))
	var group sync.WaitGroup

	for index, item := range checks {
		group.Go(func() {
			results[index] = Result{
				Name: item.Name,
				Err: lifecycle.Protect(func() error {
					return item.fn(ctx)
				}),
			}
		})
	}

	group.Wait()

	return results
}

// Passed reports whether every result in results has no failure.
func Passed(results []Result) bool {
	for _, result := range results {
		if result.Err != nil {
			return false
		}
	}

	return true
}

// Monitor owns registered checks and their latest completed background pass.
type Monitor struct {
	// mu guards the append-only registry.
	mu sync.Mutex
	// checks retains callbacks in registration order.
	checks []check
	// cache owns completed background observations.
	cache readiness
	// interval controls the nonoverlapping background cadence.
	interval time.Duration
	// timeout bounds each cooperative pass.
	timeout time.Duration
	// transitions reports completed passes outside all locks.
	transitions func(context.Context, []Result, []Result)
}

// ErrDuplicateCheck identifies duplicate registered names.
var ErrDuplicateCheck = errors.New("duplicate check name")

// New constructs monitoring without executing any check.
func New(interval, timeout time.Duration, transitions func(context.Context, []Result, []Result)) *Monitor {
	return &Monitor{interval: interval, timeout: timeout, transitions: transitions}
}

// Snapshot observes registered checks without running them.
func (monitor *Monitor) Snapshot() Snapshot {
	monitor.mu.Lock()

	registered := len(monitor.checks) > 0
	monitor.mu.Unlock()

	snapshot := monitor.cache.snapshot()
	snapshot.Enabled = snapshot.Enabled || registered

	return snapshot
}

// Reset discards registrations after startup fails, without running callbacks.
func (monitor *Monitor) Reset() {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()

	monitor.checks = nil
}
