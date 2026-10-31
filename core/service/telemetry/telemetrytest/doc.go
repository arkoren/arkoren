// Package telemetrytest tests code that emits telemetry and drivers that
// implement it. [Fake] is a recording driver built on the memory driver with
// assertions for spans, metrics, and logs. [Run] is the conformance suite
// every telemetry driver calls.
//
// A test of code that uses telemetry:
//
//	fake := telemetrytest.New()
//	service := fake.Telemetry(tb) // Started; shut down when the test ends.
//	ctx, span := service.Tracer("example.com/billing").Start(tb.Context(), "charge",
//		telemetry.Attrs(telemetry.String("currency", "EUR")))
//	charges := service.Meter("example.com/billing").Counter("billing.charges", telemetry.Keys("currency"))
//	charges.Add(ctx, 1, telemetry.String("currency", "EUR"))
//	span.End()
//
//	fake.AssertSpan(tb, "charge", telemetry.String("currency", "EUR"))
//	fake.AssertCounter(tb, "billing.charges", 1, telemetry.String("currency", "EUR"))
//
// Call [Fake.DumpOnFailure] once after creating the fake and a failing test
// logs the recorded spans as an indented tree with their events and logs.
//
// To test the application wiring instead, pass the fake to the application,
// which starts it:
//
//	app := application.New(func(app *application.Builder) {
//		app.Telemetry(telemetry.New(telemetrytest.New()))
//	})
//
// A driver author proves a driver with the suite. A driver that cannot be
// read back through its own methods passes a [Reader]:
//
//	func TestConformance(t *testing.T) {
//		telemetrytest.Run(t, func(t *testing.T) telemetry.Lifecycle {
//			return memorytelemetry.New()
//		})
//	}
//
// The trace suite checks the mandatory [telemetry.SpanRecorder.Parent] protocol
// without requiring a Reader: selected identity, remote/sample flags and trace
// state are preserved, while root and inactive recorders report zero. Fake
// inherits this protocol from memorytelemetry; custom recorder implementations
// must add Parent explicitly and decorators must forward it.
package telemetrytest
