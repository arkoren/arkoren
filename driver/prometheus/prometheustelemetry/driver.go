package prometheustelemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"arkoren.dev/core/service/telemetry"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

// Driver is the Prometheus implementation of the telemetry MetricDriver. Create
// it with [New] and serve its [Driver.Handler]; it records nothing until the
// application starts it. Combine it with a trace driver through
// [telemetry.Compose].
//
// Instruments are translated following the OpenTelemetry to Prometheus
// compatibility specification:
//
//   - Characters that are not valid in a Prometheus name, such as dots and
//     dashes, become underscores, and runs of underscores collapse. Label keys
//     are translated the same way.
//   - The unit adds a suffix: s becomes _seconds, ms _milliseconds, By _bytes,
//     and so on; By/s becomes _bytes_per_second; the unit 1 becomes _ratio for
//     gauges only. Units in curly braces, such as {request}, and unknown units
//     add nothing.
//   - Counters end in _total. Up-down counters and gauges are Prometheus
//     gauges, and histograms are histograms.
//   - Histograms use the declared buckets, or [prometheus.DefBuckets] when none
//     are declared.
//   - The instrumentation scope is ignored: no series carries a scope label.
//   - An empty label value means the label is absent, which is how Prometheus
//     reads it.
//
// So http.server.request.duration with unit s becomes
// http_server_request_duration_seconds. Two instruments that translate to the
// same Prometheus name, or a name already held by another collector in the
// registry, make [Driver.Instrument] return an error; the telemetry facade then
// disables the later instrument and logs a warning.
//
// When a recording happens inside a sampled span, the driver attaches the
// trace_id and span_id of the span as an exemplar to counters and histograms.
// Exemplars are exposed when the scraper negotiates OpenMetrics.
//
// A Driver is safe for concurrent use.
//
//	driver := prometheustelemetry.New()
//	service := telemetry.New(driver)
//	mux := http.NewServeMux()
//	mux.Handle("/metrics", driver.Handler())
//	_ = service
type Driver struct {
	// config is the resolved configuration given to New.
	config settings

	// active reports whether recordings are accepted, that is between Start and
	// Stop.
	active atomic.Bool

	// logger reports handler errors. It is nil until Start.
	logger atomic.Pointer[slog.Logger]

	// mutex guards phase, owned, and instruments, and serializes Start,
	// Stop, and Instrument.
	mutex sync.Mutex

	// phase is the lifecycle phase.
	phase phase

	// owned are the collectors the driver registered and removes on Stop.
	owned []prometheus.Collector

	// instruments are the created instruments by Prometheus name.
	instruments map[string]*instrument
}

// New returns a Driver that applies configure. It only stores the configuration;
// nothing is registered until Start.
// Callbacks run directly once, synchronously in order on one fresh draft. Nil
// entries panic; nil or empty expansions omit edits. Recommended booleans are
// initialized before callbacks, so false wins; zero limits normalize afterwards
// to 1000. Publication copies settings but shares caller-owned native registries.
//
//	driver := prometheustelemetry.New(func(metrics *prometheustelemetry.Builder) {
//		metrics.Namespace("shop")
//	})
//	service := telemetry.New(driver)
//	_ = service
func New(configure ...BuilderFunc) *Driver {
	return &Driver{
		config:      newConfig(configure...).resolved(),
		instruments: map[string]*instrument{},
	}
}

// Start registers the runtime collectors and target_info according to the
// configuration. A collector the registry already holds is left alone. A failed
// Start unregisters what it registered and leaves the driver unstarted. Invalid
// target label values and typed-nil native pointers return errors, not panics.
// Cancellation is checked before and after synchronous registry calls; custom
// native registry methods must not block indefinitely because they take no ctx.
//
// A second Start returns an error wrapping [telemetry.ErrStarted], and Start
// after Stop one wrapping [telemetry.ErrStopped].
//
//	driver := prometheustelemetry.New()
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
		return fmt.Errorf("prometheus telemetry: %w", telemetry.ErrStarted)
	case phaseStopped:
		return fmt.Errorf("prometheus telemetry: %w", telemetry.ErrStopped)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start prometheus telemetry: %w", err)
	}

	if err := driver.config.validate(); err != nil {
		return err
	}

	if err := errors.Join(driver.registerBuiltins(setup.Resource), ctx.Err()); err != nil {
		driver.unregisterOwned()

		return fmt.Errorf("start prometheus telemetry: %w", err)
	}

	driver.logger.Store(setup.Logger)
	driver.phase = phaseStarted
	driver.active.Store(true)

	return nil
}

// Stop unregisters every collector the driver registered, so the registry
// can be reused, and stops recording. It is idempotent and safe without Start.
// It waits for native Unregister calls even if ctx is canceled; those methods
// have no cancellation API. Borrowed collectors and native registry ownership
// remain with the caller. The HTTP handler can still expose borrowed collectors.
//
// After the application's metric-producing work has drained:
//
//	if err := driver.Stop(ctx); err != nil {
//		return err
//	}
func (driver *Driver) Stop(context.Context) error {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	driver.phase = phaseStopped
	driver.active.Store(false)
	driver.unregisterOwned()
	clear(driver.instruments)

	return nil
}

// Handler returns the handler that serves the gatherer in the Prometheus text
// format, or OpenMetrics when the scraper asks for it. Mount it where the
// scraper expects it:
//
//	r.Get("/metrics", router.HTTP(prom.Handler()))
//
// It works before Start and after Stop, serving whatever the registry
// holds. Gathering errors are logged to the application logger and the
// metrics that could be gathered are still served. When the configuration has
// no gatherer, the handler answers 501 Not Implemented.
// A typed-nil native registry pointer instead answers a fixed 503 response,
// including before Start, without invoking the invalid native handle. Native
// collectors are caller-managed; their error text can reach protected local logs
// or the metrics error response when no metrics can be served.
func (driver *Driver) Handler() http.Handler {
	if err := driver.config.validate(); err != nil {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "prometheus telemetry: invalid registry configuration", http.StatusServiceUnavailable)
		})
	}

	if driver.config.Gatherer == nil {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "prometheus telemetry: no gatherer configured", http.StatusNotImplemented)
		})
	}

	return promhttp.HandlerFor(driver.config.Gatherer, promhttp.HandlerOpts{
		ErrorLog: errorLog{
			driver: driver,
		},
		ErrorHandling:     promhttp.ContinueOnError,
		EnableOpenMetrics: true,
	})
}

// errorLog adapts the application logger to the promhttp error log.
type errorLog struct {
	// driver provides the logger, which only exists after Start.
	driver *Driver
}

// Println logs the arguments as an error on the application logger.
func (errorLog errorLog) Println(values ...any) {
	logger := errorLog.driver.logger.Load()

	if logger == nil {
		return
	}

	logger.ErrorContext(context.Background(), strings.TrimSpace(fmt.Sprintln(values...)))
}
