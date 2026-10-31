package application

import "arkoren.dev/core/event"

// FailedEvent describes the original startup or runner failure reported by Failed.
// Cleanup and telemetry-finalization errors are returned by lifecycle methods,
// not added to this payload. Error values remain shared with listeners.
//
//	onFailed := func(ctx context.Context, failure application.FailedEvent) error {
//		logger.ErrorContext(ctx, "application failed", "err", failure.Err)
//
//		return nil
//	}
type FailedEvent struct {
	// Err is the error that failed the application.
	// Example: fmt.Println(failure.Err)
	Err error
}

// UnreadyEvent names checks in the background pass that made the app unready.
// It carries no backend error text. The names are in registration order;
// listeners must not mutate the shared payload's slice.
//
//	onUnready := func(_ context.Context, report application.UnreadyEvent) error {
//		fmt.Println(report.Failing)
//
//		return nil
//	}
type UnreadyEvent struct {
	// Failing names the health checks that failed the pass, in registration
	// order.
	// Example: fmt.Println(report.Failing)
	Failing []string
}

// CheckEvent describes a background check transition published by CheckFailed
// or CheckRecovered. Err is nil for recovery; names identify dependencies without
// parsing error text. Error values are shared, not copied or redacted for listeners.
//
//	onRecovered := func(_ context.Context, report application.CheckEvent) error {
//		fmt.Println(report.Name, report.Err == nil)
//
//		return nil
//	}
type CheckEvent struct {
	// Name is the health check name given to [Composer.Check], so listeners
	// can identify the dependency without inspecting its error text.
	//
	// Example: fmt.Println(failure.Name)
	Name string

	// Err is the error the check returned; nil for [CheckRecovered].
	// Example: fmt.Println(report.Err == nil) // true for a recovery
	Err error
}

var (
	// Starting is dispatched after composition and successful telemetry startup,
	// before user Startup callbacks. Listeners registered during composition can
	// log through the application's active telemetry. Composition or telemetry
	// startup failures dispatch Failed without Starting.
	//
	//	compose.MustDefer(events.Listen(application.Starting, onStarting))
	Starting = event.NewSignal("app.starting")

	// Started is dispatched once every runner has been launched.
	// The app is running, but background health can still be pending.
	//
	//	compose.MustDefer(events.Listen(application.Started, onStarted))
	Started = event.NewSignal("app.started")

	// Stopping is dispatched when shutdown begins.
	// It precedes runner cancellation and cleanup. Configured or diagnostic
	// telemetry is still active for the listener's final records.
	//
	//	compose.MustDefer(events.Listen(application.Stopping, onStopping))
	Stopping = event.NewSignal("app.stopping")

	// Stopped is dispatched after runner waiting and cleanup, before telemetry
	// finalization. State is still StateStopping, so listeners can emit their
	// final telemetry; Shutdown publishes StateStopped after finalization.
	// Runner waiting or cleanup may have failed; inspect Shutdown's result.
	//
	//	unsubscribe, err := app.Events().Listen(application.Stopped, onStopped)
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe() // Keep the subscription until Run has returned.
	//	return app.Run(ctx, compose)
	Stopped = event.NewSignal("app.stopped")

	// Failed is dispatched when startup fails or a runner fails.
	// Its payload carries the original failure; listeners cannot change lifecycle
	// outcomes. Listener errors and panics are logged by the event bus.
	//
	//	unsubscribe, err := app.Events().Listen(application.Failed, onFailed)
	//	if err != nil {
	//		return err
	//	}
	//	defer unsubscribe() // Rollback dispatches Failed after acquired cleanup.
	//	return app.Run(ctx, compose)
	Failed = event.NewTopic[FailedEvent]("app.failed")

	// Ready is dispatched when the first readiness pass succeeds and when the
	// verdict turns from not ready to ready.
	// It is a background-health observation, not a replacement for the live probe.
	//
	//	compose.MustDefer(events.Listen(application.Ready, onReady))
	Ready = event.NewSignal("app.ready")

	// Unready is dispatched when the first readiness pass fails and when the
	// verdict turns from ready to not ready.
	// The payload contains failing names, never backend error text.
	//
	//	compose.MustDefer(events.Listen(application.Unready, onUnready))
	Unready = event.NewTopic[UnreadyEvent]("app.unready")

	// CheckFailed is dispatched when a health check fails after passing, or
	// on its first readiness pass.
	// The payload's error is intended for trusted in-process listeners.
	//
	//	compose.MustDefer(events.Listen(application.CheckFailed, onCheckFailed))
	CheckFailed = event.NewTopic[CheckEvent]("app.check.failed")

	// CheckRecovered is dispatched when a health check passes after failing.
	// Its CheckEvent retains the name and has a nil Err.
	//
	//	compose.MustDefer(events.Listen(application.CheckRecovered, onRecovered))
	CheckRecovered = event.NewTopic[CheckEvent]("app.check.recovered")
)
