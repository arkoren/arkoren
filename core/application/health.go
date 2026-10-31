package application

import (
	"context"
	"time"

	"arkoren.dev/core/application/internal/health"
)

// CheckStatus is the cached state of one health check as of the latest
// completed background pass. Application.Checks returns detached records;
// error values remain shared, and callbacks do not run when reading the cache.
//
//	for _, status := range app.Checks() {
//		fmt.Println(status.Name, status.Err, status.CheckedAt)
//	}
type CheckStatus struct {
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
// until the first pass completes: before the application runs, when no check
// is registered, and for a short time after Start. Use [Application.CheckHealth]
// to run the checks now. The result is a copy.
//
// Example:
//
//	for _, status := range app.Checks() {
//		fmt.Println(status.Name, status.Err, status.Since)
//	}
func (application *Application) Checks() []CheckStatus {
	return checkStatuses(application.health.Checks())
}

// RunChecks runs every registered check now, concurrently and without
// holding any lock, and returns one [CheckStatus] per check in registration
// order, passing and failing alike. Each callback receives a context deadline
// from [Builder.ReadinessTimeout] and ctx. Callbacks must honor cancellation;
// RunChecks waits for all of them and cannot forcibly interrupt user code.
// Since is zero, because
// a single run has no history, and CheckedAt is when the run finished. It
// returns nil when no check is registered. A panicking check reports the
// recovered value. Use [Application.Checks] for the cached state instead.
//
// Example:
//
//	for _, status := range app.RunChecks(ctx) {
//		fmt.Println(status.Name, status.Err)
//	}
func (application *Application) RunChecks(ctx context.Context) []CheckStatus {
	return checkStatuses(application.health.RunChecks(ctx))
}

// CheckHealth runs every registered check as [Application.RunChecks] does and
// returns their failures joined in registration order, each prefixed with its
// check name. It returns nil when every check passes. ctx is passed to each
// check with the RunChecks timeout; checks must cooperate with cancellation.
// This explicit run does not update the cached readiness result.
//
//	if err := app.CheckHealth(ctx); err != nil {
//		return fmt.Errorf("dependencies unavailable: %w", err)
//	}
func (application *Application) CheckHealth(ctx context.Context) error {
	return application.health.CheckHealth(ctx)
}

// healthTransitions logs and dispatches each check that failed after passing or before any
// pass, and each check that passed after failing. previous is nil before the
// first pass.
func (application *Application) healthTransitions(ctx context.Context, previous, results []health.Result) {
	failedBefore := make(map[string]bool, len(previous))

	for _, result := range previous {
		failedBefore[result.Name] = result.Err != nil
	}

	for _, result := range results {
		switch failed := result.Err != nil; {
		case failed && !failedBefore[result.Name]:
			application.logger.WarnContext(ctx, "health check failed", "check", result.Name, "err", result.Err)
			application.events.Emit(ctx, CheckFailed, CheckEvent{Name: result.Name, Err: result.Err})
		case !failed && failedBefore[result.Name]:
			application.logger.InfoContext(ctx, "health check recovered", "check", result.Name)
			application.events.Emit(ctx, CheckRecovered, CheckEvent{Name: result.Name})
		}
	}

	passing := health.Passed(results)

	if previous != nil && passing == health.Passed(previous) {
		return
	}

	if passing {
		application.logger.InfoContext(ctx, "app ready")
		application.events.Signal(ctx, Ready)

		return
	}

	application.logger.WarnContext(ctx, "app not ready", "err", health.JoinFailures(results))
	application.events.Emit(ctx, Unready, UnreadyEvent{Failing: health.Failing(results)})
}

// checkStatuses detaches public records while preserving nil for absent checks.
func checkStatuses(values []health.Status) []CheckStatus {
	if values == nil {
		return nil
	}

	statuses := make([]CheckStatus, len(values))

	for index, value := range values {
		statuses[index] = CheckStatus(value)
	}

	return statuses
}
