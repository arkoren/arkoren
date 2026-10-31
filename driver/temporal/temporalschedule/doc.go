// Package temporalschedule provides a Temporal-backed schedule driver. Recurring
// entries become Temporal Schedules and deferred jobs become workflows that
// start after a delay, so both survive restarts and coordinate timing across
// processes. This does not guarantee exactly-once application side effects.
//
// Construction callbacks edit one fresh [Builder], once in order; nil entries
// panic, while expanded nil or empty slices omit edits. Published settings detach
// from retained drafts; the host and logger remain shared dependencies.
//
//	driver, err := temporalschedule.New(temporal.New(), func(scheduler *temporalschedule.Builder) {
//		scheduler.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = schedule.New(driver)
//
// [New] registers a workflow and an activity on a shared [temporal.Host]. The
// Host owns the client and the worker; call [temporal.Host.Start] after
// [New] and before [Driver.Run]. A NewFrom client is available immediately.
// Run, Defer, and Cancel return [ErrNotStarted] without a native client, rather
// than invoking an unconnected SDK adapter. Every schedule fires the workflow, which runs
// one activity that calls the dispatch function given to [Driver.Run]. The
// activity makes one attempt; a failed run is not retried.
// An early delivery waits for the local Run within its activity timeout, so
// Defer before Run does not lose the job to an absent dispatcher. Run removes
// its dispatcher on every exit and cancels active dispatch contexts. It returns
// nil on context cancellation, including during reconciliation, but does not
// wait for callbacks to return or forcibly stop callbacks that ignore context.
// Only one Run may be active locally; another returns [schedule.ErrRunning].
//
// [Driver.Run] reconciles Temporal with the declared entries: it creates new
// schedules, updates existing ones, and deletes schedules whose ID starts with
// the prefix and that are no longer declared. Deferred jobs use the separate
// "<prefix>-deferred-" workflow ID namespace and are never pruned.
//
// A six-field cron expression with seconds is converted to Temporal's
// seven-field form. Specs must use a named zone; the Local zone is rejected
// with [schedule.ErrUnsupported].
//
// Daylight saving time: Temporal fires twice inside an autumn overlap and
// skips a time inside a spring gap, unlike a driver that runs once in an
// overlap.
package temporalschedule
