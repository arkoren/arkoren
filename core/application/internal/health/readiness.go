package health

import (
	"context"
	"sync"
	"time"
)

const (
	// readinessStaleIntervals is how many intervals a result stays valid.
	// A loop that has not completed a pass for longer is treated as wedged
	// and the monitor as not ready.
	readinessStaleIntervals = 3
)

// readiness holds the latest background health result. The zero value has
// no completed pass and is not ready.
type readiness struct {
	// mu guards every field below.
	mu sync.Mutex

	// enabled reports whether a background loop was started.
	enabled bool

	// results holds the per-check outcomes of the latest completed pass in
	// registration order. It is never modified after being stored.
	results []Result

	// completedAt is when the latest pass finished; zero before the first.
	completedAt time.Time
}

// Snapshot is a consistent copy of the readiness state.
type Snapshot struct {
	// Enabled reports whether a background loop was started.
	Enabled bool

	// Results holds the per-check outcomes of the latest completed pass.
	Results []Result

	// CompletedAt is when the latest pass finished; zero before the first.
	CompletedAt time.Time
}

// enable marks the background loop as running.
func (state *readiness) enable() {
	state.mu.Lock()
	defer state.mu.Unlock()

	state.enabled = true
}

// store records the outcome of a pass and returns the results of the
// previous one, nil before the first pass. Each result's changedAt retains its
// previous timestamp when the verdict is unchanged, or completedAt otherwise.
func (state *readiness) store(results []Result, completedAt time.Time) (previous []Result) {
	state.mu.Lock()
	defer state.mu.Unlock()

	previous = state.results

	for index := range results {
		results[index].ChangedAt = completedAt

		for _, before := range previous {
			if before.Name == results[index].Name && (before.Err != nil) == (results[index].Err != nil) {
				results[index].ChangedAt = before.ChangedAt
			}
		}
	}

	state.results, state.completedAt = results, completedAt

	return previous
}

// snapshot returns a consistent copy of the readiness state.
func (state *readiness) snapshot() Snapshot {
	state.mu.Lock()
	defer state.mu.Unlock()

	return Snapshot{
		Enabled:     state.enabled,
		Results:     state.results,
		CompletedAt: state.completedAt,
	}
}

// Fresh reports whether a pass has completed and finished no longer than
// maxAge ago.
func (snapshot Snapshot) Fresh(maxAge time.Duration) bool {
	return !snapshot.CompletedAt.IsZero() && time.Since(snapshot.CompletedAt) <= maxAge
}

// HealthStatus evaluates cached health independently of lifecycle liveness.
func (snapshot Snapshot) HealthStatus(maxAge time.Duration) string {
	if !snapshot.Enabled {
		return "ok"
	}

	if snapshot.CompletedAt.IsZero() {
		return "pending"
	}

	if !snapshot.Fresh(maxAge) {
		return "stale"
	}

	if !Passed(snapshot.Results) {
		return "failing"
	}

	return "ok"
}

// MaxAge is how long a completed pass stays valid.
func (monitor *Monitor) MaxAge() time.Duration {
	return monitor.interval * readinessStaleIntervals
}

// Runner returns the runner that keeps the readiness result
// current. It is launched with the other runners and ends with them, so
// shutdown waits for it.
func (monitor *Monitor) Runner() func(context.Context) error {
	monitor.mu.Lock()

	registered := len(monitor.checks) > 0
	monitor.mu.Unlock()

	if !registered {
		return nil
	}

	monitor.cache.enable()

	return func(ctx context.Context) error {
		ticker := time.NewTicker(monitor.interval)
		defer ticker.Stop()

		for {
			monitor.pass(ctx)

			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}
}

// pass runs checks with the pass deadline and waits for their
// cooperative completion. It stores results and publishes check transitions,
// the first pass's verdict, and later verdict changes. A pass interrupted by
// shutdown is discarded; callbacks are never forcibly interrupted.
func (monitor *Monitor) pass(ctx context.Context) {
	passContext, cancel := context.WithTimeout(ctx, monitor.timeout)
	defer cancel()

	results := monitor.runChecks(passContext)

	if ctx.Err() != nil {
		return
	}

	previous := monitor.cache.store(results, time.Now())
	monitor.transitions(ctx, previous, results)
}

// Failing returns the names of the failed checks in results.
func Failing(results []Result) []string {
	var names []string

	for _, result := range results {
		if result.Err != nil {
			names = append(names, result.Name)
		}
	}

	return names
}
