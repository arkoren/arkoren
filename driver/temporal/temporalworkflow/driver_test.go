package temporalworkflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"arkoren.dev/core/service/workflow"
)

// fakeBackend records calls and returns canned results.
type fakeBackend struct {
	// started retains the most recent workflow submission for native-option
	// assertions.
	started startRequest
	// startErr scripts the error returned by the fake submission API.
	startErr error
	// resultOut scripts the payload returned by the fake workflow result API.
	resultOut payload
	// resultErr scripts the error returned by the fake workflow result API.
	resultErr error
	// status scripts the native execution status returned by describe.
	status enumspb.WorkflowExecutionStatus
	// descErr scripts the error returned by the fake describe API.
	descErr error
	// cancelErr scripts the error returned by the fake cancellation API.
	cancelErr error
	// signalErr scripts the error returned by the fake signal API.
	signalErr error
	// signaled records the most recent native signal name sent by the driver.
	signaled string
}

// start records submission arguments and returns the scripted acceptance or
// error result without contacting an engine.
func (fake *fakeBackend) start(_ context.Context, request startRequest) error {
	fake.started = request

	return fake.startErr
}

// result returns the scripted workflow payload and failure without waiting for
// a real execution.
func (fake *fakeBackend) result(context.Context, string) (payload, error) {
	return fake.resultOut, fake.resultErr
}

// describe returns the scripted native status and describe failure for
// contract-mapping assertions.
func (fake *fakeBackend) describe(context.Context, string, string) (enumspb.WorkflowExecutionStatus, error) {
	return fake.status, fake.descErr
}

// cancel returns the scripted cancellation result without contacting an engine.
// Driver tests use native error values here to check contract translation.
func (fake *fakeBackend) cancel(context.Context, string) error {
	return fake.cancelErr
}

// signal records the signal request and returns the scripted signal failure,
// including any modeled initial not-running response.
func (fake *fakeBackend) signal(_ context.Context, _, name string, _ payload) error {
	fake.signaled = name

	return fake.signalErr
}

// newTestDriver constructs a driver around the supplied fake backend and
// construction policies. The fake owns its scripted results and recorded calls.
func newTestDriver(t *testing.T, fake *fakeBackend, opts ...func(*DriverBuilder)) *Driver {
	t.Helper()

	return newDriver(fake, driverSettings(opts...))
}

// TestStartMapsOptions verifies that workflow identity, delay, metadata, task
// queue, and duration policies reach the native start request.
func TestStartMapsOptions(t *testing.T) {
	t.Parallel()

	w := workflow.New("order", func(*workflow.Context, string) (string, error) {
		return "", nil
	}, func(definition *workflow.Builder) {
		definition.MaxDuration(time.Hour)
	})

	registry, err := workflow.Handlers(w)

	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeBackend{}
	driver := newTestDriver(t, fake, func(driver *DriverBuilder) {
		driver.Registry(registry)
	}, func(driver *DriverBuilder) {
		driver.Prefix("acme")
	})

	err = driver.Start(t.Context(), "order", "run-1", []byte(`{"v":1}`), workflow.StartOptions{
		Delay:    time.Minute,
		Metadata: map[string]string{"a": "b"},
	})

	if err != nil {
		t.Fatal(err)
	}

	got := fake.started

	if got.name != "acme-wf-order" || got.id != "run-1" || got.timeout != time.Hour+time.Minute || got.delay != time.Minute || got.memo["a"] != "b" {
		t.Fatalf("start request = %+v", got)
	}
}

// TestStartErrorAlreadyStarted verifies that a native identifier conflict maps
// to ErrAlreadyStarted.
func TestStartErrorAlreadyStarted(t *testing.T) {
	t.Parallel()

	fake := &fakeBackend{
		startErr: serviceerror.NewWorkflowExecutionAlreadyStarted("exists", "", ""),
	}

	err := newTestDriver(t, fake).Start(t.Context(), "order", "run-1", nil, workflow.StartOptions{})

	if !errors.Is(err, workflow.ErrAlreadyStarted) {
		t.Fatalf("error = %v; want %v", err, workflow.ErrAlreadyStarted)
	}
}

// TestStartErrorUnregistered verifies that a definition missing from the
// configured registry is rejected before native submission.
func TestStartErrorUnregistered(t *testing.T) {
	t.Parallel()

	registry, _ := workflow.Handlers()

	err := newTestDriver(t, &fakeBackend{}, func(driver *DriverBuilder) {
		driver.Registry(registry)
	}).Start(t.Context(), "order", "run-1", nil, workflow.StartOptions{})

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("error = %v; want %v", err, workflow.ErrNotFound)
	}
}

// TestStartNotStartedHost verifies that submission preserves the host readiness
// failure when no native client exists.
func TestStartNotStartedHost(t *testing.T) {
	t.Parallel()

	fake := &fakeBackend{
		startErr: errNotStarted,
	}

	if err := newTestDriver(t, fake).Start(t.Context(), "order", "run-1", nil, workflow.StartOptions{}); !errors.Is(err, errNotStarted) {
		t.Fatalf("error = %v", err)
	}
}

// requireResultError verifies Result against a backend failing with backendErr fails with want.
func requireResultError(t *testing.T, backendErr, want error) {
	t.Helper()

	_, err := newTestDriver(t, &fakeBackend{
		resultErr: backendErr,
	}).Result(t.Context(), "order", "run-1")

	if !errors.Is(err, want) {
		t.Fatalf("error = %v; want %v", err, want)
	}
}

// TestResultMapsEnvelope verifies that native application failure wrapping
// retains the facade's encoded application error kind.
func TestResultMapsEnvelope(t *testing.T) {
	t.Parallel()

	envelope := failure(workflow.Permanent(workflow.NewError("custom", "boom")))

	requireResultError(t, fmt.Errorf("workflow failed: %w", envelope), workflow.NewError("custom", ""))
}

// TestResultMapsCanceled verifies that native cancellation maps to the facade
// cancellation sentinel.
func TestResultMapsCanceled(t *testing.T) {
	t.Parallel()

	requireResultError(t, temporal.NewCanceledError(), workflow.ErrCanceled)
}

// TestResultMapsTimeout verifies that native timeout failures map to the facade
// timeout sentinel.
func TestResultMapsTimeout(t *testing.T) {
	t.Parallel()

	requireResultError(t, temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), workflow.ErrTimeout)
}

// TestResultMapsNotFound verifies that a missing native workflow maps to the
// facade not-found sentinel.
func TestResultMapsNotFound(t *testing.T) {
	t.Parallel()

	requireResultError(t, serviceerror.NewNotFound("no run"), workflow.ErrNotFound)
}

// TestResultOutput verifies that successful native workflow output is returned
// unchanged as the opaque application result.
func TestResultOutput(t *testing.T) {
	t.Parallel()

	out, err := newTestDriver(t, &fakeBackend{
		resultOut: payload(`{"v":"x"}`),
	}).Result(t.Context(), "order", "run-1")

	if err != nil || string(out) != `{"v":"x"}` {
		t.Fatalf("Result = %s, %v", out, err)
	}
}

// requireStatus verifies Status maps the Temporal execution status to want.
func requireStatus(t *testing.T, status enumspb.WorkflowExecutionStatus, want workflow.Status) {
	t.Helper()

	got, err := newTestDriver(t, &fakeBackend{
		status: status,
	}).Status(t.Context(), "order", "run-1")

	if err != nil || got != want {
		t.Errorf("Status(%v) = %v, %v; want %v", status, got, err, want)
	}
}

// TestStatusMappingRunning verifies that an active native execution is exposed
// as Running.
func TestStatusMappingRunning(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, workflow.Running)
}

// TestStatusMappingCompleted verifies that a successful native execution is
// exposed as Completed.
func TestStatusMappingCompleted(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, workflow.Completed)
}

// TestStatusMappingFailed verifies that a failed native execution is exposed as
// Failed.
func TestStatusMappingFailed(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, workflow.Failed)
}

// TestStatusMappingTimedOutIsFailed verifies that a native timeout is reported
// as Failed rather than Canceled.
func TestStatusMappingTimedOutIsFailed(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, workflow.Failed)
}

// TestStatusMappingTerminatedIsFailed verifies that a forcibly terminated
// native execution is reported as Failed.
func TestStatusMappingTerminatedIsFailed(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, workflow.Failed)
}

// TestStatusMappingCanceled verifies that a canceled native execution is
// exposed as Canceled.
func TestStatusMappingCanceled(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, workflow.Canceled)
}

// TestStatusMappingContinuedAsNewIsRunning verifies that a continued-as-new
// execution remains Running at the logical workflow boundary.
func TestStatusMappingContinuedAsNewIsRunning(t *testing.T) {
	t.Parallel()

	requireStatus(t, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, workflow.Running)
}

// TestStatusMappingNotFound verifies that a missing native workflow returns
// ErrNotFound instead of a status.
func TestStatusMappingNotFound(t *testing.T) {
	t.Parallel()

	_, err := newTestDriver(t, &fakeBackend{
		descErr: serviceerror.NewNotFound("x"),
	}).Status(t.Context(), "order", "run-1")

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("error = %v; want ErrNotFound", err)
	}
}

// TestDriverCancel verifies that active, completed, missing, and unexpectedly
// failed cancellation responses retain their existing contract mappings.
func TestDriverCancel(t *testing.T) {
	t.Parallel()

	missing := serviceerror.NewNotFound("workflow execution already completed")

	if err := newTestDriver(t, &fakeBackend{}).Cancel(t.Context(), "order", "run-1"); err != nil {
		t.Fatalf("running: %v", err)
	}

	finished := &fakeBackend{
		cancelErr: missing,
		status:    enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
	}

	if err := newTestDriver(t, finished).Cancel(t.Context(), "order", "run-1"); err != nil {
		t.Fatalf("finished: %v", err)
	}

	unknown := &fakeBackend{
		cancelErr: missing,
		descErr:   serviceerror.NewNotFound("no run"),
	}

	if err := newTestDriver(t, unknown).Cancel(t.Context(), "order", "run-1"); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	other := &fakeBackend{
		cancelErr: errors.New("down"),
	}

	if err := newTestDriver(t, other).Cancel(t.Context(), "order", "run-1"); err == nil || errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("other: %v", err)
	}
}

// TestSignal verifies that checks configured native signal naming and missing-
// execution error mapping.
func TestSignal(t *testing.T) {
	t.Parallel()

	fake := &fakeBackend{}

	if err := newTestDriver(t, fake, func(driver *DriverBuilder) {
		driver.Prefix("acme")
	}).Signal(t.Context(), "order", "run-1", "go", nil); err != nil {
		t.Fatal(err)
	}

	if fake.signaled != "acme.sig.go" {
		t.Fatalf("signal name = %q", fake.signaled)
	}

	missing := &fakeBackend{
		signalErr: serviceerror.NewNotFound("no run"),
	}

	if err := newTestDriver(t, missing).Signal(t.Context(), "order", "run-1", "go", nil); !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("error = %v; want ErrNotFound", err)
	}
}

// TestConfigDefaults verifies that default driver prefixes produce the
// documented workflow and activity type names.
func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	if config := driverSettings(); config.Prefix != DefaultPrefix {
		t.Fatalf("prefix = %q", config.Prefix)
	}

	driver := newDriver(&fakeBackend{}, driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("")
	}))

	if driver.config.workflowType("a") != "arkoren-wf-a" || driver.config.stepType("a") != "arkoren-step-a" {
		t.Fatalf("names = %q, %q", driver.config.workflowType("a"), driver.config.stepType("a"))
	}
}

// TestNilArguments verifies that driver and runner constructors reject missing
// required host values.
func TestNilArguments(t *testing.T) {
	t.Parallel()

	if _, err := NewDriver(nil); err == nil {
		t.Fatal("NewDriver(nil) succeeded")
	}

	if _, err := NewRunner(nil, nil); err == nil {
		t.Fatal("NewRunner(nil) succeeded")
	}
}
