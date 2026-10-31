package telemetry

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
)

// Selection assigns a driver to one signal in [Compose]. Create one with
// [Traces], [Metrics], or [Logs].
// A zero selection contributes no driver; later selections override one signal.
//
//	selection := telemetry.Metrics(memorytelemetry.New())
//	service := telemetry.New(telemetry.Compose(selection))
//	fmt.Println(service.Enabled()) // false until Start
type Selection struct {
	// lifecycle is the selected driver, nil for an empty selection.
	lifecycle Lifecycle

	// traces, metrics, and logs hold the driver under the signal it was
	// selected for. At most one is set.
	traces TraceDriver
	// metrics selects the metric driver, or nil when this signal is absent.
	metrics MetricDriver
	// logs selects the log exporter, or nil when this signal is absent.
	logs LogDriver
}

// Traces selects driver for traces and propagation. A nil driver selects
// nothing.
//
//	traces := memorytelemetry.New()
//	service := telemetry.New(telemetry.Compose(telemetry.Traces(traces)))
//	fmt.Println(service.Enabled()) // false until Start
func Traces(driver TraceDriver) Selection {
	if driver == nil {
		return Selection{}
	}

	return Selection{
		lifecycle: driver,
		traces:    driver,
	}
}

// Metrics selects driver for metrics. A nil driver selects nothing.
// Trace and log routing are unchanged by this selection.
//
//	metrics := memorytelemetry.New()
//	service := telemetry.New(telemetry.Compose(telemetry.Metrics(metrics)))
//	fmt.Println(service.Enabled()) // false until Start
func Metrics(driver MetricDriver) Selection {
	if driver == nil {
		return Selection{}
	}

	return Selection{
		lifecycle: driver,
		metrics:   driver,
	}
}

// Logs selects driver for log export. A nil driver selects nothing.
// This does not change a caller's ordinary slog handler or its local output.
//
//	logs := memorytelemetry.New()
//	service := telemetry.New(telemetry.Compose(telemetry.Logs(logs)))
//	logger := slog.New(service.Handler(slog.DiscardHandler))
//	_ = logger // Start service before exporting records.
func Logs(driver LogDriver) Selection {
	if driver == nil {
		return Selection{}
	}

	return Selection{
		lifecycle: driver,
		logs:      driver,
	}
}

// Composite is a [Lifecycle] that routes each signal to its own driver. Build
// it with [Compose]; pass it to [New] or to the application. It serializes its
// lifecycle and must not be copied after use.
//
//	composite := telemetry.Compose(telemetry.Metrics(memorytelemetry.New()))
//	if err := composite.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
//	if err := composite.Stop(ctx); err != nil {
//		return err
//	}
type Composite struct {
	// traces, metrics, and logs are the selected drivers, nil when absent.
	traces TraceDriver
	// metrics is the selected driver used to bind instruments.
	metrics MetricDriver
	// logs is the selected exporter used by the facade's log handler.
	logs LogDriver

	// drivers are the distinct selected drivers in start order.
	drivers []Lifecycle

	// mu guards state and started.
	mu sync.Mutex

	// state is the lifecycle position.
	state lifecycleState

	// started counts drivers that were started and not yet shut down.
	started int
}

// Compose routes each signal to the driver chosen by its selection, for
// example OpenTelemetry for traces and Prometheus for metrics:
//
//	driver := telemetry.Compose(
//		telemetry.Traces(otel),
//		telemetry.Metrics(prom),
//	)
//	app := application.New(func(app *application.Builder) {
//		app.Telemetry(telemetry.New(driver))
//	})
//
// When a signal is selected twice, the last selection wins and a driver that
// ends up serving no signal is not started. Each distinct driver is started
// once, in the order of its first selection, and shut down in reverse order.
// Noncomparable driver values have no value identity: each selection is distinct.
// Pass a pointer to share one such driver's lifecycle across signals.
// If a Start fails, the drivers already started are shut down and the errors
// are joined. Rollback uses the startup context, including its cancellation and
// deadline; it attempts every acquired driver's Stop even when that context has
// expired. Drivers remain responsible for releasing resources on expired
// contexts and for returning from their callbacks. Lifecycle calls are serialized
// and must not synchronously reenter the same composite's Start or Stop. The
// result follows the [Lifecycle] rules itself: a second Start fails, Stop without
// Start is safe. Context cancellation does not interrupt the lifecycle mutex or
// forcibly cancel arbitrary backend callbacks.
func Compose(selections ...Selection) *Composite {
	composite := &Composite{}
	// active records the final selection position for each signal. Positions
	// remain comparable even when a selected driver contains slices or maps.
	active := [3]int{-1, -1, -1}

	for index, selection := range selections {
		if selection.traces != nil {
			composite.traces = selection.traces
			active[0] = index
		}

		if selection.metrics != nil {
			composite.metrics = selection.metrics
			active[1] = index
		}

		if selection.logs != nil {
			composite.logs = selection.logs
			active[2] = index
		}
	}

	for index, selection := range selections {
		driver := selection.lifecycle

		if !slices.Contains(active[:], index) && !composite.uses(driver) {
			continue
		}

		if composite.contains(driver) {
			continue
		}

		composite.drivers = append(composite.drivers, driver)
	}

	return composite
}

// uses reports whether driver serves at least one signal.
func (composite *Composite) uses(driver Lifecycle) bool {
	return sameDriver(driver, composite.traces) ||
		sameDriver(driver, composite.metrics) ||
		sameDriver(driver, composite.logs)
}

// contains reports whether driver is already in the start list.
func (composite *Composite) contains(driver Lifecycle) bool {
	return slices.ContainsFunc(composite.drivers, func(other Lifecycle) bool {
		return sameDriver(driver, other)
	})
}

// sameDriver reports whether both values are the same driver. Drivers whose
// dynamic type is not comparable are never the same.
func sameDriver(driver, other any) bool {
	if driver == nil || other == nil {
		return false
	}

	left, right := reflect.ValueOf(driver), reflect.ValueOf(other)

	return left.Comparable() && right.Comparable() && left.Equal(right)
}

// Start starts every distinct driver in order. When one fails, the drivers
// started before it are shut down in reverse order and the errors are joined.
// Both startup and rollback receive ctx without removing cancellation or its
// deadline. A driver whose Start fails owns cleanup of its partial acquisition;
// rollback stops only drivers whose Start succeeded.
//
//	composite := telemetry.Compose(telemetry.Metrics(metrics))
//	if err := composite.Start(ctx, telemetry.Setup{}); err != nil {
//		return err
//	}
func (composite *Composite) Start(ctx context.Context, setup Setup) error {
	composite.mu.Lock()
	defer composite.mu.Unlock()

	switch composite.state {
	case stateStarted:
		return fmt.Errorf("start composite telemetry: %w", ErrStarted)
	case stateStopped:
		return fmt.Errorf("start composite telemetry: %w", ErrStopped)
	}

	for _, driver := range composite.drivers {
		if err := driver.Start(ctx, setup); err != nil {
			rollback := composite.stopStarted(ctx)
			composite.state = stateStopped

			return errors.Join(fmt.Errorf("start composite telemetry: %w", err), rollback)
		}

		composite.started++
	}

	composite.state = stateStarted

	return nil
}

// Stop shuts down the started drivers in reverse order, attempting all of
// them, and joins the errors. It is safe without Start and idempotent.
// ctx is forwarded unchanged, including cancellation; backend callbacks must
// cooperate and release their owned resources even if flushing cannot complete.
//
//	if err := composite.Stop(shutdownContext); err != nil {
//		return err
//	}
func (composite *Composite) Stop(ctx context.Context) error {
	composite.mu.Lock()
	defer composite.mu.Unlock()

	composite.state = stateStopped

	return composite.stopStarted(ctx)
}

// stopStarted shuts down the started drivers in reverse order. The caller
// holds mu.
func (composite *Composite) stopStarted(ctx context.Context) error {
	var errs []error

	for _, driver := range slices.Backward(composite.drivers[:composite.started]) {
		if err := driver.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shut down composite telemetry: %w", err))
		}
	}

	composite.started = 0

	return errors.Join(errs...)
}
