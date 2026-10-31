// Package oteltelemetry provides an OpenTelemetry telemetry Driver that exports
// traces, metrics, and logs over OTLP.
//
// The package name carries the vendor prefix, so it does not clash with the
// telemetry contract package:
//
//	import "arkoren.dev/driver/otel/oteltelemetry"
//
//	app := application.New(func(app *application.Builder) {
//		app.Telemetry(telemetry.New(oteltelemetry.New()))
//	})
//
// With no construction callbacks the driver uses the standard OTEL_*
// environment variables when the application starts. See [Driver] for the
// variables it honors.
//
// When native and facade instrumentation share a context, a valid native parent
// wins, including a non-recording remote parent. Otherwise StartSpan adopts a
// valid facade parent. The recorder's mandatory [telemetry.SpanRecorder.Parent]
// snapshots that selected parent before SDK Start, so facade observations agree
// with native parentage; roots and inactive recorders return zero. The returned
// context still carries the native child for descendants and propagation.
//
// Custom recorder implementations and decorators must implement or forward
// Parent() telemetry.SpanContext. It is not an optional capability. Preserve
// the selected parent's metadata, not a later context or the child's identity.
// See [Driver.StartSpan] for a caller example.
//
// Driver-owned pipelines always complete native teardown, even after the flush
// context expires. Supplied providers remain caller-owned; runtime callbacks the
// driver installs on them are still removed on Stop or failed startup. See
// [Driver.Stop] for the native cancellation limits. Explicit logs, exception
// text, baggage, resources, and labels are not automatically redacted.
package oteltelemetry
