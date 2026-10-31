package oteltelemetry

import (
	"context"
	"fmt"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"
)

// phase is where a [Driver] is in its lifecycle.
type phase uint8

const (
	// _ is the state before Start; it is the zero value.
	_ phase = iota

	// phaseStarted is the state between Start and Stop.
	phaseStarted

	// phaseStopped is the state after Stop.
	phaseStopped
)

// Start builds the resource, exporters, and providers described by the
// configuration and environment. It fails on invalid configuration, such as an
// unsupported protocol, and a failed Start leaves the driver unstarted. The ctx
// bounds startup only.
//
// A second Start returns an error wrapping [telemetry.ErrStarted], and Start
// after Stop one wrapping [telemetry.ErrStopped]. A canceled ctx prevents
// publication; partial startup releases owned resources before returning.
// Typed-nil native pointers are configuration errors, not fallback settings.
// Native hooks run synchronously and must cooperate; cancellation cannot
// interrupt arbitrary application-supplied provider methods.
//
// With a constructed driver and the application's startup context:
//
//	if err := driver.Start(ctx, telemetry.Setup{
//		Resource: telemetry.Resource{Service: "billing"},
//	}); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context, setup telemetry.Setup) error {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	switch driver.phase {
	case phaseStarted:
		return fmt.Errorf("otel telemetry: %w", telemetry.ErrStarted)
	case phaseStopped:
		return fmt.Errorf("otel telemetry: %w", telemetry.ErrStopped)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start otel telemetry: %w", err)
	}

	config := driver.config.native()

	if err := config.Validate(); err != nil {
		return err
	}

	run, err := sdk.Build(ctx, config, setup)

	if err != nil {
		return fmt.Errorf("start otel telemetry: %w", err)
	}

	driver.state.Store(run)
	driver.phase = phaseStarted

	return nil
}

// Stop flushes buffered telemetry and shuts down the providers the driver
// created, in reverse order of creation. Providers supplied by the caller are
// flushed but not shut down. Driver-installed runtime callbacks are unregistered.
// After it returns new driver operations are no-ops. It is idempotent and safe
// without Start. Previously obtained native spans retain their native lifetime.
//
// The ctx bounds ForceFlush; its errors are retained. Owned provider shutdown
// then uses an uncanceled context because the SDK can otherwise skip processor
// cleanup permanently. Stop waits for native teardown, including possible final
// exports, even beyond ctx's deadline. Native exporter timeouts still apply.
// Borrowed provider flushes and lifecycle locking are synchronous; uncooperative
// native methods cannot be forcibly interrupted. The driver does not abandon
// teardown in a background goroutine. Caller-owned providers remain the caller's
// responsibility.
//
// After application operations have drained:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(ctx context.Context) error {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if driver.phase == phaseStopped {
		return nil
	}

	driver.phase = phaseStopped

	run := driver.state.Swap(nil)

	if run == nil {
		return nil
	}

	return run.Shutdown(ctx)
}
