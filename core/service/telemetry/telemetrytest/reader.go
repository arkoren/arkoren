package telemetrytest

import (
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/memorytelemetry"
	"arkoren.dev/foundation/collection"
)

// Reader exposes what a driver recorded so [Run] can assert on it. A driver
// that records into memory, like [Fake] and the memory driver, is its own
// Reader. A driver that exports to a backend provides one that reads an
// in-memory exporter and converts the data to [memorytelemetry.Span] and
// [memorytelemetry.Metric] eager snapshots. Collection wrappers do not detach
// storage: implementations must copy owned containers before returning them.
//
// Run calls the methods after it ended spans or recorded measurements. An
// implementation that buffers must flush first. Spans lists finished spans in
// any order; Metrics lists instruments under the names they were created
// with, so a driver that translates names must translate them back.
//
//	var reader telemetrytest.Reader = memorytelemetry.New()
//	fmt.Println(len(reader.Spans()), len(reader.Metrics())) // 0 0 before recording
type Reader interface {
	// Spans returns the finished spans.
	// Exporter-specific implementations may return them in any order.
	//
	//	var reader telemetrytest.Reader = memorytelemetry.New()
	//	for _, span := range reader.Spans() {
	//		fmt.Println(span.Name, span.Parent.IsValid())
	//	}
	Spans() collection.Slice[memorytelemetry.Span]

	// Metrics returns the instruments and their aggregates.
	// Names must be translated back to the portable declaration spelling.
	//
	//	var reader telemetrytest.Reader = memorytelemetry.New()
	//	for _, metric := range reader.Metrics() {
	//		fmt.Println(metric.Name, len(metric.Points))
	//	}
	Metrics() collection.Slice[memorytelemetry.Metric]
}

// Option changes how [Run] exercises a driver.
// Obtain values from WithReader rather than depending on the private suite
// configuration representation.
//
//	option := telemetrytest.WithReader(func(driver telemetry.Lifecycle) telemetrytest.Reader {
//		return driver.(*memorytelemetry.Driver)
//	})
//	telemetrytest.Run(t, func(*testing.T) telemetry.Lifecycle {
//		return memorytelemetry.New()
//	}, option)
type Option func(*suiteConfig)

// suiteConfig holds the [Run] options.
type suiteConfig struct {
	// reader finds the [Reader] of a driver. Nil falls back to the driver
	// itself.
	reader func(driver telemetry.Lifecycle) Reader
}

// WithReader tells [Run] how to read back what a driver recorded. The
// function receives each driver created by the suite and returns its
// [Reader], or nil when it cannot be read. Without a Reader, and when the
// driver is not a Reader itself, the suite checks behavior through the API
// and skips the checks on recorded content.
//
//	telemetrytest.Run(t, func(*testing.T) telemetry.Lifecycle {
//		return memorytelemetry.New()
//	}, telemetrytest.WithReader(func(driver telemetry.Lifecycle) telemetrytest.Reader {
//		return driver.(*memorytelemetry.Driver)
//	}))
func WithReader(reader func(driver telemetry.Lifecycle) Reader) Option {
	return func(config *suiteConfig) {
		config.reader = reader
	}
}
