package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
	"arkoren.dev/core/service/workflow"
	"arkoren.dev/core/service/workflow/memoryworkflow"
	"arkoren.dev/core/service/workflow/workflowtest"
)

// errJSONPanic is a stable identity for panics raised by JSON extension hooks.
// Its sensitive text must never enter the automatically recorded observation.
var errJSONPanic = errors.New("secret-json-panic")

// panickingJSON exercises the standard codec's application-defined JSON hooks,
// rather than a custom workflow.Codec implementation.
type panickingJSON struct{}

// MarshalJSON interrupts encoding with a known identity. Tests verify the
// facade propagates it unchanged instead of treating the operation as success.
func (panickingJSON) MarshalJSON() ([]byte, error) {
	panic(errJSONPanic)
}

// UnmarshalJSON interrupts decoding with the same known identity, including
// when Result creates a fresh zero output value before invoking the hook.
func (*panickingJSON) UnmarshalJSON([]byte) error {
	panic(errJSONPanic)
}

// panickingCodec exercises both custom codec boundaries independently of JSON.
type panickingCodec struct {
	// cause is the exact value each codec call must propagate.
	cause error
}

// Marshal interrupts custom input encoding before any driver operation.
// The panic must not be confused with a returned encoding error.
func (codec panickingCodec) Marshal(any) ([]byte, error) {
	panic(codec.cause)
}

// Unmarshal interrupts custom output decoding after the driver's result call.
// A previously nil driver error must not turn this panic into a success.
func (codec panickingCodec) Unmarshal([]byte, any) error {
	panic(codec.cause)
}

// panickingDriver interrupts every contract method with one exact panic value.
// Each test invokes a different facade operation, so no behavior flag is needed.
type panickingDriver struct {
	// cause is deliberately sensitive and must never be passed to telemetry.
	cause error
}

// Start panics after the facade has encoded input and begun its observation.
// This detects a nil named error left by successful encoding.
func (driver panickingDriver) Start(context.Context, string, string, []byte, workflow.StartOptions) error {
	panic(driver.cause)
}

// Signal panics after encoding, independently of any actual engine run.
// Its observation must be a bounded failure, not delivery acceptance.
func (driver panickingDriver) Signal(context.Context, string, string, string, []byte) error {
	panic(driver.cause)
}

// Result panics instead of returning a value or an execution failure.
// Its observation must close even though the wait did not return normally.
func (driver panickingDriver) Result(context.Context, string, string) ([]byte, error) {
	panic(driver.cause)
}

// Status panics instead of reporting an engine state. No state is inferred
// from this interrupted query.
func (driver panickingDriver) Status(context.Context, string, string) (workflow.Status, error) {
	panic(driver.cause)
}

// Cancel panics before acknowledging the cancellation request. The facade must
// not report successful cancellation merely because its named error is nil.
func (driver panickingDriver) Cancel(context.Context, string, string) error {
	panic(driver.cause)
}

// observedPanicClient builds a client with an independently started recording
// service. Optional policies select codecs without replacing that service.
func observedPanicClient(t *testing.T, driver workflow.Driver, configure ...workflow.ClientBuilderFunc) (*workflow.Client, *telemetrytest.Fake) {
	t.Helper()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	policies := append([]workflow.ClientBuilderFunc{func(client *workflow.ClientBuilder) {
		client.Telemetry(service)
	}}, configure...)

	return workflow.NewClient(driver, policies...), recording
}

// assertPanickedObservation requires exact panic identity and a finished error
// span/count with only a fixed class. No recovery text/type is exported.
func assertPanickedObservation(t *testing.T, recording *telemetrytest.Fake, cause any, call func()) {
	t.Helper()

	func() {
		defer func() {
			// Reflection compares the original values, not errors.Is equivalence:
			// wrapping the panic would change the caller's recovery contract.
			if value := recover(); !reflect.ValueOf(value).Equal(reflect.ValueOf(cause)) {
				t.Fatalf("panic identity = %v, want original value", value)
			}
		}()
		call()
		t.Error("call returned instead of propagating panic")
	}()

	recording.AssertSpanCount(t, 1)

	span := recording.Spans()[0]

	if span.Status != telemetry.Error || span.StatusDescription != "workflow.panic" {
		t.Fatalf("panic span status: %v %q", span.Status, span.StatusDescription)
	}

	recording.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.outcome", "error"), telemetry.String("error.type", "workflow.panic"))
	recording.AssertCounter(t, "arkoren.workflow.operation.count", 1)
	recording.AssertHistogram(t, "arkoren.workflow.operation.duration", 1)

	assertRedactedObservations(t, recording)
}

// TestStartOptionPanicPreservesNonErrorValue checks pointer identity for panic
// values that do not implement error, without exposing their sensitive contents.
func TestStartOptionPanicPreservesNonErrorValue(t *testing.T) {
	t.Parallel()

	client, recording := observedPanicClient(t, workflowtest.New())
	cause := &[]string{"secret-non-error-panic"}
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Start(t.Context(), definition, "run", "input", func(*workflow.StartOptions) {
			panic(cause)
		})
	})
}

// TestStartOptionPanicClosesFailedObservation covers per-call policy execution
// before encoding or driver submission.
func TestStartOptionPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	client, recording := observedPanicClient(t, workflowtest.New())
	cause := errors.New("secret-option-panic")
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Start(t.Context(), definition, "run", "input", func(*workflow.StartOptions) {
			panic(cause)
		})
	})
}

// TestStartCodecPanicClosesFailedObservation covers a custom encoder panic.
// The exact codec panic remains visible to the caller only.
func TestStartCodecPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-codec-panic")
	client, recording := observedPanicClient(t, workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Codec(panickingCodec{cause: cause})
	})

	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Start(t.Context(), definition, "run", "input")
	})
}

// TestStartJSONPanicClosesFailedObservation covers application MarshalJSON hooks
// reached through the ordinary default JSON codec.
func TestStartJSONPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	client, recording := observedPanicClient(t, workflowtest.New())
	definition := workflow.New("panic", func(_ *workflow.Context, in panickingJSON) (string, error) {
		return "", nil
	})

	assertPanickedObservation(t, recording, errJSONPanic, func() {
		_, _ = client.Start(t.Context(), definition, "run", panickingJSON{})
	})
}

// TestSignalCodecPanicClosesFailedObservation covers signal custom encoding;
// a run need not exist because the codec fails before transport.
func TestSignalCodecPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-codec-panic")
	client, recording := observedPanicClient(t, workflowtest.New(), func(client *workflow.ClientBuilder) {
		client.Codec(panickingCodec{cause: cause})
	})

	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_ = client.Signal(t.Context(), definition, "run", workflow.NewSignal[string]("value"), "input")
	})
}

// TestSignalJSONPanicClosesFailedObservation covers signal MarshalJSON hooks
// without any custom codec or backend behavior.
func TestSignalJSONPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	client, recording := observedPanicClient(t, workflowtest.New())
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, errJSONPanic, func() {
		_ = client.Signal(t.Context(), definition, "run", workflow.NewSignal[panickingJSON]("value"), panickingJSON{})
	})
}

// completedPanicDriver supplies a completed JSON result without invoking the
// client codec. Result tests can therefore isolate decoding panics.
func completedPanicDriver(t *testing.T) *workflowtest.Fake {
	t.Helper()

	driver := workflowtest.New()

	if err := driver.Start(t.Context(), "panic", "run", []byte(`{"v":"input"}`), workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	driver.Complete("panic", "run", "output")

	return driver
}

// TestResultCodecPanicClosesFailedObservation covers a decoding panic after
// successful driver retrieval has assigned nil to the named result error.
func TestResultCodecPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-codec-panic")
	client, recording := observedPanicClient(t, completedPanicDriver(t), func(client *workflow.ClientBuilder) {
		client.Codec(panickingCodec{cause: cause})
	})

	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Run(definition, "run").Result(t.Context())
	})
}

// TestResultJSONPanicClosesFailedObservation covers output UnmarshalJSON hooks
// while retaining the normal default codec and a successful driver response.
func TestResultJSONPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	client, recording := observedPanicClient(t, completedPanicDriver(t))
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (panickingJSON, error) {
		return panickingJSON{}, nil
	})

	assertPanickedObservation(t, recording, errJSONPanic, func() {
		_, _ = client.Run(definition, "run").Result(t.Context())
	})
}

// TestStartDriverPanicClosesFailedObservation covers a transport panic after
// successful input encoding, rather than a returned transport error.
func TestStartDriverPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-driver-panic")
	client, recording := observedPanicClient(t, panickingDriver{cause: cause})
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Start(t.Context(), definition, "run", "input")
	})
}

// TestSignalDriverPanicClosesFailedObservation covers interrupted delivery.
// The signal's successful encoding does not imply successful transport.
func TestSignalDriverPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-driver-panic")
	client, recording := observedPanicClient(t, panickingDriver{cause: cause})
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_ = client.Signal(t.Context(), definition, "run", workflow.NewSignal[string]("value"), "input")
	})
}

// TestResultDriverPanicClosesFailedObservation covers an interrupted wait.
// No workflow outcome is inferred from the transport panic.
func TestResultDriverPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-driver-panic")
	client, recording := observedPanicClient(t, panickingDriver{cause: cause})
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Run(definition, "run").Result(t.Context())
	})
}

// TestStatusDriverPanicClosesFailedObservation covers an interrupted query.
// Its finished observation must not imply a successful status response.
func TestStatusDriverPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-driver-panic")
	client, recording := observedPanicClient(t, panickingDriver{cause: cause})
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_, _ = client.Run(definition, "run").Status(t.Context())
	})
}

// TestCancelDriverPanicClosesFailedObservation covers an interrupted cancel.
// No successful cancellation is observed before the driver acknowledges it.
func TestCancelDriverPanicClosesFailedObservation(t *testing.T) {
	t.Parallel()

	cause := errors.New("secret-driver-panic")
	client, recording := observedPanicClient(t, panickingDriver{cause: cause})
	definition := workflow.New("panic", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	assertPanickedObservation(t, recording, cause, func() {
		_ = client.Run(definition, "run").Cancel(t.Context())
	})
}

// TestClientTelemetryPreservesStartOutcomes covers accepted, rejected and
// duplicate calls without exposing input or overwriting user metadata.
func TestClientTelemetryPreservesStartOutcomes(t *testing.T) {
	t.Parallel()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	driver := workflowtest.New()
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(service)
	})

	definition := workflow.New("observed", func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	run, err := client.Start(t.Context(), definition, "secret-run-id", "secret-payload",
		workflow.Metadata(map[string]string{"traceparent": "user-metadata"}))

	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Start(t.Context(), definition, run.ID(), "ignored"); !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("duplicate: %v", err)
	}

	if _, err := client.Start(t.Context(), definition, "", "ignored"); !errors.Is(err, workflow.ErrInvalidID) {
		t.Fatalf("invalid: %v", err)
	}

	recording.AssertSpanCount(t, 3)
	recording.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.operation", "start"),
		telemetry.String("arkoren.workflow.outcome", "already_started"))

	started := driver.Started()[0]

	if started.Options.Trace["traceparent"] == "" || started.Options.Metadata["traceparent"] != "user-metadata" {
		t.Fatalf("carrier/metadata ownership: %#v", started.Options)
	}

	assertRedactedObservations(t, recording)
}

// TestResultObservesWaitWithoutChangingRun verifies wait cancellation is not
// workflow cancellation, and returned handles retain the client's service.
func TestResultObservesWaitWithoutChangingRun(t *testing.T) {
	t.Parallel()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	driver := workflowtest.New()
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(service)
	})

	definition := workflow.New("observed", func(_ *workflow.Context, input string) (string, error) {
		return input, nil
	})

	run, err := client.Start(t.Context(), definition, "secret-run-id", "secret-payload")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := run.Result(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}

	if status, err := run.Status(t.Context()); err != nil || status != workflow.Running {
		t.Fatalf("wait changed run outcome: %v %v", status, err)
	}

	if err := client.Signal(t.Context(), definition, run.ID(), workflow.NewSignal[string]("approval"), "secret-signal"); err != nil {
		t.Fatal(err)
	}

	if err := run.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}

	recording.AssertSpanCount(t, 5)

	assertRedactedObservations(t, recording)
}

// assertRedactedObservations checks complete span records and the metric schema,
// including status descriptions/events, for accidental input or ID exposure.
func assertRedactedObservations(t *testing.T, recording *telemetrytest.Fake) {
	t.Helper()

	for _, span := range recording.Spans() {
		if strings.Contains(fmt.Sprint(span), "secret") {
			t.Fatal("sensitive span data recorded")
		}
	}

	for _, metric := range recording.Metrics() {
		if !slices.Equal(metric.Keys, []string{"arkoren.workflow.operation", "arkoren.workflow.outcome", "error.type"}) {
			t.Fatalf("unexpected metric label schema: %v", metric.Keys)
		}
	}
}

// TestWorkerTelemetryIsSeparateAndParentsRetries verifies process-equivalent
// telemetry services join only through a serialized carrier, not caller context.
func TestWorkerTelemetryIsSeparateAndParentsRetries(t *testing.T) {
	t.Parallel()

	producer, worker := telemetrytest.New(), telemetrytest.New()
	producerService, workerService := producer.Telemetry(t), worker.Telemetry(t)
	var calls atomic.Int32
	step := workflow.NewStep("observed-step", func(ctx context.Context, input string) (string, error) {
		_, child := workerService.Tracer("dependencies").Start(ctx, "dependency")
		child.End()

		if calls.Add(1) == 1 {
			return "", errors.New("secret-retry-error")
		}

		return input, nil
	}, func(step *workflow.StepBuilder) {
		step.Retry(2)
		step.Backoff(time.Nanosecond, 1, time.Nanosecond)
	})

	definition := workflow.New("observed-flow", func(ctx *workflow.Context, input string) (string, error) {
		return ctx.Step(step, input)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.NewRegistry([]workflow.Definition{definition, step}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(workerService)
	})

	if err != nil {
		t.Fatal(err)
	}

	driver := memoryworkflow.New(registry)
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(producerService)
	})

	run, err := client.Start(t.Context(), definition, "independent-worker", "secret-input")

	if err != nil {
		t.Fatal(err)
	}

	if output, err := run.Result(t.Context()); err != nil || output != "secret-input" {
		t.Fatalf("result %q: %v", output, err)
	}

	producer.AssertSpanCount(t, 2)
	worker.AssertSpanCount(t, 4)

	assertWorkerParentage(t, producer, worker)
	worker.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.operation", "step_attempt"),
		telemetry.String("arkoren.workflow.outcome", "error"))

	assertRedactedObservations(t, worker)
}

// assertWorkerParentage verifies distributed identity independently of call
// results: every worker span joins the trace, and attempts parent to Start.
func assertWorkerParentage(t *testing.T, producer, worker *telemetrytest.Fake) {
	t.Helper()

	start, ok := producer.Span("workflow start")

	if !ok {
		t.Fatal("missing start")
	}

	for _, span := range worker.Spans() {
		if span.Context.TraceID != start.Context.TraceID {
			t.Fatal("worker did not extract start trace")
		}

		if span.Name == "workflow step_attempt" && span.Parent.SpanID != start.Context.SpanID {
			t.Fatal("attempt did not retain injected parent")
		}
	}
}

// TestClientTelemetryFollowsResetAndDynamicDefault proves services are resolved
// at call time and cached instruments continue recording after capture reset.
func TestClientTelemetryFollowsResetAndDynamicDefault(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	client := workflow.NewClient(workflowtest.New())
	definition := workflow.New("default-observed", func(_ *workflow.Context, in int) (int, error) {
		return in, nil
	})

	run := client.Run(definition, "one")
	first, second := telemetrytest.New(), telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))

	if _, err := run.Status(t.Context()); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("first default: %v", err)
	}

	first.AssertSpanCount(t, 1)
	telemetry.SetDefault(second.Telemetry(t))

	if _, err := run.Status(t.Context()); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("replacement default: %v", err)
	}

	second.AssertSpanCount(t, 1)
	second.Reset()

	if _, err := run.Status(t.Context()); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("reset capture: %v", err)
	}

	second.AssertSpanCount(t, 1)
	first.AssertSpanCount(t, 1)
	second.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.operation", "status"))
}

// TestWorkerTelemetryFollowsDynamicDefault ensures default worker registration
// never captures a stale service and existing instruments survive capture reset.
func TestWorkerTelemetryFollowsDynamicDefault(t *testing.T) {
	previous := telemetry.Default()
	t.Cleanup(func() {
		telemetry.SetDefault(previous)
	})

	step := workflow.NewStep("dynamic-attempt", func(_ context.Context, in string) (string, error) {
		return in, nil
	})

	registry, err := workflow.Handlers(step)

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Step(step.Name())
	first, second := telemetrytest.New(), telemetrytest.New()
	telemetry.SetDefault(first.Telemetry(t))

	if _, err := handler.Handle(t.Context(), []byte(`{"v":"input"}`)); err != nil {
		t.Fatal(err)
	}

	telemetry.SetDefault(second.Telemetry(t))

	if _, err := handler.Handle(t.Context(), []byte(`{"v":"input"}`)); err != nil {
		t.Fatal(err)
	}

	second.Reset()

	if _, err := handler.Handle(t.Context(), []byte(`{"v":"input"}`)); err != nil {
		t.Fatal(err)
	}

	first.AssertSpanCount(t, 1)
	second.AssertSpanCount(t, 1)
	second.AssertCounter(t, "arkoren.workflow.operation.count", 1,
		telemetry.String("arkoren.workflow.operation", "step_attempt"))
}

// TestResultDeadlineIsWaitFailure verifies an already-expired deadline produces
// an observation without waiting, terminating the run, or synthesizing events.
func TestResultDeadlineIsWaitFailure(t *testing.T) {
	t.Parallel()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	driver := workflowtest.New()
	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(service)
	})

	definition := workflow.New("deadline-observed", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	run, err := client.Start(t.Context(), definition, "run", "input")

	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
	t.Cleanup(cancel)

	if _, err := run.Result(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait deadline identity: %v", err)
	}

	if status, err := run.Status(t.Context()); err != nil || status != workflow.Running {
		t.Fatalf("wait changed run outcome: %v %v", status, err)
	}

	recording.AssertHistogram(t, "arkoren.workflow.operation.duration", 1,
		telemetry.String("arkoren.workflow.operation", "result"),
		telemetry.String("arkoren.workflow.outcome", "error"))
}

// TestRejectedCallPreservesCauseAndRedactsError exercises raw provider error
// text at the client boundary without recording it in spans or metric labels.
func TestRejectedCallPreservesCauseAndRedactsError(t *testing.T) {
	t.Parallel()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	driver := workflowtest.New()
	cause := errors.New("secret-provider-detail")
	driver.FailNext(cause)

	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
		client.Telemetry(service)
	})

	definition := workflow.New("rejected", func(_ *workflow.Context, in string) (string, error) {
		return in, nil
	})

	if _, err := client.Start(t.Context(), definition, "secret-id", "secret-input"); !errors.Is(err, cause) {
		t.Fatalf("lost cause: %v", err)
	}

	recording.AssertSpanCount(t, 1)

	assertRedactedObservations(t, recording)
}

// recordedStepRuntime models orchestration restoration: Step returns an existing
// durable result without invoking the registered handler or creating an attempt.
type recordedStepRuntime struct {
	// stubRuntime supplies unrelated deterministic runtime operations.
	stubRuntime
}

// Step returns the already-recorded envelope unchanged. The identity result
// keeps this replay test independent of encoding or transport behavior.
func (*recordedStepRuntime) Step(_ string, input []byte, _ workflow.StepOptions) ([]byte, error) {
	return input, nil
}

// TestRestoredOrchestrationDoesNotObserveAttempts proves replaying coordination
// with recorded step results does not produce execution spans or metric points.
func TestRestoredOrchestrationDoesNotObserveAttempts(t *testing.T) {
	t.Parallel()

	recording := telemetrytest.New()
	service := recording.Telemetry(t)
	step := workflow.NewStep("recorded-step", func(_ context.Context, in string) (string, error) {
		t.Error("recorded step unexpectedly executed")

		return in, nil
	})

	definition := workflow.New("restored-flow", func(ctx *workflow.Context, in string) (string, error) {
		return ctx.Step(step, in)
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Minute)
	})

	registry, err := workflow.NewRegistry([]workflow.Definition{definition, step}, func(worker *workflow.RegistryBuilder) {
		worker.Telemetry(service)
	})

	if err != nil {
		t.Fatal(err)
	}

	handler, _ := registry.Workflow(definition.Name())

	for range 2 {
		if _, err := handler.Handle(&recordedStepRuntime{}, []byte(`{"v":"input"}`)); err != nil {
			t.Fatal(err)
		}
	}

	recording.AssertSpanCount(t, 0)

	if len(recording.Metrics()) != 0 {
		t.Fatal("orchestration replay produced metric points")
	}
}
