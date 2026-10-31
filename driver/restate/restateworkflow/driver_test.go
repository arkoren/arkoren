package restateworkflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkoren.dev/core/service/workflow"
)

// call records one gateway call.
type call struct {
	// method records which gateway operation the driver invoked.
	method string

	// service identifies the target Restate service recorded by the fake transport.
	service string

	// id identifies the engine resource represented by this record.
	id string
	// input retains the invocation input supplied by the driver.
	input []byte
	// start retains the submission settings supplied with a recorded start call.
	start startRequest
	// signal retains the signal request supplied with a recorded signal call.
	signal signalRequest
}

// fakeGateway scripts the answers of a gateway and records the calls.
type fakeGateway struct {
	// calls records gateway operations in call order for protocol assertions.
	calls []call

	// accepted scripts whether a start request creates a new invocation.
	accepted bool
	// startErr scripts the error returned by the fake submission API.
	startErr error
	// attachOut scripts the output returned by the blocking result API.
	attachOut []byte
	// attachErr scripts the failure returned by the blocking result API.
	attachErr error
	// outputOut scripts the output returned by the nonblocking result API.
	outputOut []byte
	// outputErr scripts the failure returned by the nonblocking result API.
	outputErr error
	// signalErr scripts the error returned by the fake signal API.
	signalErr error
	// info scripts the invocation identity and completion state returned by
	// lookup.
	info invocationInfo
	// lookupErr scripts the error returned by the fake invocation lookup.
	lookupErr error
	// cancelErr scripts the error returned by the fake cancellation API.
	cancelErr error
	// canceled records the identifiers sent to the cancellation API.
	canceled []string
	// signalSeen records that the signal method was called, including a forced
	// delivery that bypasses the scripted error.
	signalSeen bool
}

// start records submission arguments and returns the scripted acceptance or
// error result without contacting an engine.
func (fake *fakeGateway) start(_ context.Context, service, id string, in []byte, request startRequest) (bool, error) {
	fake.calls = append(fake.calls, call{
		method:  "start",
		service: service,
		id:      id,
		input:   in,
		start:   request,
	})

	return fake.accepted, fake.startErr
}

// attach records the blocking result request and returns the scripted output
// and failure.
func (fake *fakeGateway) attach(_ context.Context, service, id string) ([]byte, error) {
	fake.calls = append(fake.calls, call{
		method:  "attach",
		service: service,
		id:      id,
	})

	return fake.attachOut, fake.attachErr
}

// output records the nonblocking result request and returns the scripted output
// and failure.
func (fake *fakeGateway) output(_ context.Context, service, id string) ([]byte, error) {
	fake.calls = append(fake.calls, call{
		method:  "output",
		service: service,
		id:      id,
	})

	return fake.outputOut, fake.outputErr
}

// signal records the signal request and returns the scripted signal failure,
// including any modeled initial not-running response.
func (fake *fakeGateway) signal(_ context.Context, service, id string, request signalRequest) error {
	fake.calls = append(fake.calls, call{
		method:  "signal",
		service: service,
		id:      id,
		signal:  request,
	})
	fake.signalSeen = true

	if request.Force {
		return nil
	}

	return fake.signalErr
}

// interrupt records the durable-wait interruption request and succeeds without
// contacting an engine.
func (fake *fakeGateway) interrupt(_ context.Context, service, id string) error {
	fake.calls = append(fake.calls, call{
		method:  "interrupt",
		service: service,
		id:      id,
	})

	return nil
}

// lookup records the invocation lookup and returns its scripted identity and
// completion state or lookup failure.
func (fake *fakeGateway) lookup(_ context.Context, service, id string) (invocationInfo, error) {
	fake.calls = append(fake.calls, call{
		method:  "lookup",
		service: service,
		id:      id,
	})

	return fake.info, fake.lookupErr
}

// cancel records the invocation identifier before returning cancelErr. Recording
// failed requests lets tests distinguish a skipped operation from an API failure.
func (fake *fakeGateway) cancel(_ context.Context, invocation string) error {
	fake.canceled = append(fake.canceled, invocation)

	return fake.cancelErr
}

// requireServiceName verifies a namer with prefix names the workflow service want.
func requireServiceName(t *testing.T, prefix, name, want string) {
	t.Helper()

	if got := newServiceNamer(prefix).service(name); got != want {
		t.Errorf("service(%q, %q) = %q, want %q", prefix, name, got, want)
	}
}

// TestServiceNamesPlainPrefix verifies that a plain prefix produces the
// expected native workflow-service name.
func TestServiceNamesPlainPrefix(t *testing.T) {
	t.Parallel()

	requireServiceName(t, "arkoren", "onboard", "ArkorenWf_onboard")
}

// TestServiceNamesPunctuatedPrefix verifies that punctuation is normalized in
// the native prefix without changing the application workflow name.
func TestServiceNamesPunctuatedPrefix(t *testing.T) {
	t.Parallel()

	requireServiceName(t, "my-app.v2", "send-mail", "MyAppV2Wf_send-mail")
}

// TestServiceNamesUnderscorePrefix verifies that underscores in the configured
// prefix normalize consistently for native service registration.
func TestServiceNamesUnderscorePrefix(t *testing.T) {
	t.Parallel()

	requireServiceName(t, "already_ok", "a1", "AlreadyOkWf_a1")
}

// TestServiceNamesEmptyPrefix verifies that an empty prefix still produces a
// valid deterministic native workflow-service name.
func TestServiceNamesEmptyPrefix(t *testing.T) {
	t.Parallel()

	requireServiceName(t, "", "x", "Wf_x")
}

// TestPrefixDefault verifies that omitting the prefix uses the documented
// default in submitted service names.
func TestPrefixDefault(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		accepted: true,
	}

	driver := newDriver(fake, driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("")
	}))

	if err := driver.Start(t.Context(), "w", "1", nil, workflow.StartOptions{}); err != nil {
		t.Fatal(err)
	}

	if fake.calls[0].service != "ArkorenWf_w" {
		t.Fatalf("service = %q", fake.calls[0].service)
	}
}

// TestStart verifies that checks that the gateway receives the configured
// service identity, payload, delay, and submission headers.
func TestStart(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		accepted: true,
	}
	driver := newDriver(fake, driverSettings(func(driver *DriverBuilder) {
		driver.Prefix("acme")
	}))

	options := workflow.StartOptions{
		Delay:    time.Minute,
		Metadata: map[string]string{"team": "a"},
	}

	if err := driver.Start(t.Context(), "w", "id-1", []byte(`{"v":1}`), options); err != nil {
		t.Fatal(err)
	}

	got := fake.calls[0]

	if got.service != "AcmeWf_w" || got.id != "id-1" || string(got.input) != `{"v":1}` || got.start.delay != time.Minute || got.start.headers["arkoren-meta-team"] != "a" {
		t.Fatalf("call = %+v", got)
	}
}

// requireStartError verifies Start against fake fails with want.
func requireStartError(t *testing.T, fake *fakeGateway, want error) {
	t.Helper()

	err := newDriver(fake, driverSettings()).Start(t.Context(), "w", "1", nil, workflow.StartOptions{})

	if !errors.Is(err, want) {
		t.Fatalf("Start = %v, want %v", err, want)
	}
}

// TestStartErrorPreviouslyAccepted verifies that an already accepted invocation
// maps to ErrAlreadyStarted.
func TestStartErrorPreviouslyAccepted(t *testing.T) {
	t.Parallel()

	requireStartError(t, &fakeGateway{
		accepted: false,
	}, workflow.ErrAlreadyStarted)
}

// TestStartErrorUnknownService verifies that a missing native service maps to
// the workflow not-found sentinel.
func TestStartErrorUnknownService(t *testing.T) {
	t.Parallel()

	requireStartError(t, &fakeGateway{
		startErr: errMissing,
	}, workflow.ErrNotFound)
}

// TestStartErrorTransport verifies that an unrelated transport failure remains
// discoverable through the driver's error wrapping.
func TestStartErrorTransport(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	requireStartError(t, &fakeGateway{
		startErr: boom,
	}, boom)
}

// requireResult verifies Result against fake returns out and fails with want:
// nil for success, otherwise an error that is want or has want's message.
func requireResult(t *testing.T, fake *fakeGateway, want error, out string) {
	t.Helper()

	got, err := newDriver(fake, driverSettings()).Result(t.Context(), "w", "1")

	switch {
	case want == nil && err != nil:
		t.Fatalf("Result error = %v", err)
	case want != nil && err == nil:
		t.Fatalf("Result error = nil, want %v", want)
	case want != nil && want.Error() != "" && !errors.Is(err, want) && err.Error() != want.Error():
		t.Fatalf("Result error = %v, want %v", err, want)
	}

	if string(got) != out {
		t.Fatalf("Result = %q, want %q", got, out)
	}
}

// TestResultSuccess verifies that the blocking gateway output is returned
// unchanged as the workflow result.
func TestResultSuccess(t *testing.T) {
	t.Parallel()

	requireResult(t, &fakeGateway{
		attachOut: []byte(`{"v":1}`),
	}, nil, `{"v":1}`)
}

// TestResultCanceled verifies that a native canceled terminal failure maps to
// ErrCanceled.
func TestResultCanceled(t *testing.T) {
	t.Parallel()

	requireResult(t, &fakeGateway{
		attachErr: &failure{
			code:    409,
			message: "canceled",
		},
	}, workflow.ErrCanceled, "")
}

// TestResultEnvelope verifies that a terminal native failure preserves an
// encoded application error kind.
func TestResultEnvelope(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		attachErr: &failure{
			code:    500,
			message: string(workflow.EncodeError(workflow.NewError("custom", "bad"))),
		},
	}

	requireResult(t, fake, workflow.NewError("custom", ""), "")
}

// TestResultTimeout verifies that an encoded workflow timeout remains
// recognizable after terminal-failure translation.
func TestResultTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		attachErr: &failure{
			code:    500,
			message: string(workflow.EncodeError(workflow.ErrTimeout)),
		},
	}

	requireResult(t, fake, workflow.ErrTimeout, "")
}

// TestResultPlainMessage verifies that ordinary terminal failure text is
// exposed as an error rather than an application envelope.
func TestResultPlainMessage(t *testing.T) {
	t.Parallel()

	requireResult(t, &fakeGateway{
		attachErr: &failure{
			code:    500,
			message: "killed",
		},
	}, errors.New("killed"), "")
}

// TestResultEmptyMessage verifies that an empty native failure message still
// yields a non-nil workflow failure.
func TestResultEmptyMessage(t *testing.T) {
	t.Parallel()

	requireResult(t, &fakeGateway{
		attachErr: &failure{
			code: 500,
		},
	}, errors.New("restate failure 500"), "")
}

// TestResultUnknownRun verifies that missing native invocation output maps to
// the workflow not-found sentinel.
func TestResultUnknownRun(t *testing.T) {
	t.Parallel()

	requireResult(t, &fakeGateway{
		attachErr: errMissing,
	}, workflow.ErrNotFound, "")
}

// requireStatus verifies Status for a run whose output lookup yields err
// reports want, or fails when fail is set.
func requireStatus(t *testing.T, err error, want workflow.Status, fail bool) {
	t.Helper()

	got, gotErr := newDriver(&fakeGateway{
		outputErr: err,
		lookupErr: errMissing,
	}, driverSettings()).Status(t.Context(), "w", "1")

	if (gotErr != nil) != fail || got != want {
		t.Fatalf("Status = %v, %v; want %v (fail %v)", got, gotErr, want, fail)
	}
}

// TestStatusCompleted verifies that available output makes the invocation
// Completed.
func TestStatusCompleted(t *testing.T) {
	t.Parallel()

	requireStatus(t, nil, workflow.Completed, false)
}

// TestStatusRunning verifies that pending native output makes the invocation
// Running.
func TestStatusRunning(t *testing.T) {
	t.Parallel()

	requireStatus(t, errNotReady, workflow.Running, false)
}

// TestStatusFailed verifies that a terminal application failure makes the
// invocation Failed.
func TestStatusFailed(t *testing.T) {
	t.Parallel()

	requireStatus(t, &failure{
		code:    500,
		message: "x",
	}, workflow.Failed, false)
}

// TestStatusCanceled verifies that a terminal cancellation makes the invocation
// Canceled.
func TestStatusCanceled(t *testing.T) {
	t.Parallel()

	requireStatus(t, &failure{
		code:    409,
		message: "canceled",
	}, workflow.Canceled, false)
}

// TestStatusUnknown verifies that a missing invocation returns an error instead
// of a fabricated status.
func TestStatusUnknown(t *testing.T) {
	t.Parallel()

	requireStatus(t, errMissing, "", true)
}

// TestStatusTransportError verifies that unexpected gateway failures remain
// errors during status lookup.
func TestStatusTransportError(t *testing.T) {
	t.Parallel()

	requireStatus(t, errors.New("down"), "", true)
}

// TestStatusUnknownIsNotFound verifies that absence from both output and
// invocation lookup yields ErrNotFound.
func TestStatusUnknownIsNotFound(t *testing.T) {
	t.Parallel()

	_, err := newDriver(&fakeGateway{
		outputErr: errMissing,
		lookupErr: errMissing,
	}, driverSettings()).Status(t.Context(), "w", "1")

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Status error = %v", err)
	}
}

// TestCancelRunning verifies that an active invocation is interrupted and
// administratively canceled by physical identifier.
func TestCancelRunning(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		info: invocationInfo{
			id: "inv_1",
		},
	}

	if err := newDriver(fake, driverSettings()).Cancel(t.Context(), "w", "1"); err != nil {
		t.Fatal(err)
	}

	if len(fake.canceled) != 1 || fake.canceled[0] != "inv_1" {
		t.Fatalf("canceled = %v", fake.canceled)
	}
}

// TestCancelFinished verifies that a completed invocation does not require
// administrative cancellation.
func TestCancelFinished(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		info: invocationInfo{
			id:        "inv_1",
			completed: true,
		},
	}

	if err := newDriver(fake, driverSettings()).Cancel(t.Context(), "w", "1"); err != nil || len(fake.canceled) != 0 {
		t.Fatalf("Cancel = %v, canceled %v", err, fake.canceled)
	}
}

// TestCancelUnknown verifies that canceling an unknown invocation returns the
// workflow not-found sentinel.
func TestCancelUnknown(t *testing.T) {
	t.Parallel()

	err := newDriver(&fakeGateway{
		lookupErr: errMissing,
	}, driverSettings()).Cancel(t.Context(), "w", "1")

	if !errors.Is(err, workflow.ErrNotFound) {
		t.Fatalf("Cancel = %v", err)
	}
}

// TestCancelGoneBetweenLookupAndCancel verifies that an invocation disappearing
// after lookup is treated as successful cancellation.
func TestCancelGoneBetweenLookupAndCancel(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		info: invocationInfo{
			id: "inv_1",
		},
		cancelErr: errMissing,
	}

	if err := newDriver(fake, driverSettings()).Cancel(t.Context(), "w", "1"); err != nil {
		t.Fatal(err)
	}
}

// TestCancelAdminFailure verifies that unexpected administrative cancellation
// failures reach the caller.
func TestCancelAdminFailure(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		info: invocationInfo{
			id: "inv_1",
		},
		cancelErr: errors.New("down"),
	}

	if err := newDriver(fake, driverSettings()).Cancel(t.Context(), "w", "1"); err == nil {
		t.Fatal("expected error")
	}
}

// TestStatusAcceptedNotStarted verifies that an accepted invocation without
// output is Running even before its handler starts.
func TestStatusAcceptedNotStarted(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{
		outputErr: errMissing,
		info: invocationInfo{
			id: "inv_1",
		},
	}

	if got, err := newDriver(fake, driverSettings()).Status(t.Context(), "w", "1"); err != nil || got != workflow.Running {
		t.Fatalf("Status = %v, %v; want running", got, err)
	}
}

// TestSignalRunning verifies that an active invocation receives the requested
// signal through the gateway.
func TestSignalRunning(t *testing.T) {
	t.Parallel()

	fake := &fakeGateway{}

	if err := newDriver(fake, driverSettings()).Signal(t.Context(), "w", "1", "go", []byte(`{"v":"x"}`)); err != nil {
		t.Fatal(err)
	}

	if len(fake.calls) != 1 || fake.calls[0].method != "signal" || fake.calls[0].signal.Name != "go" ||
		string(fake.calls[0].signal.Payload) != `{"v":"x"}` || fake.calls[0].signal.Force {
		t.Fatalf("calls = %+v", fake.calls)
	}
}

// requireSignalFinished verifies signaling a run that is no longer running,
// whose output lookup yields outputErr, fails with [workflow.ErrNotFound]
// after checking the run's state.
func requireSignalFinished(t *testing.T, outputErr error) {
	t.Helper()

	notRunning := &failure{
		code:    404,
		message: "run is not running",
	}
	fake := &fakeGateway{
		signalErr: notRunning,
		outputErr: outputErr,
		lookupErr: errMissing,
	}

	err := newDriver(fake, driverSettings()).Signal(t.Context(), "w", "1", "go", nil)

	if !errors.Is(err, workflow.ErrNotFound) || len(fake.calls) < 2 {
		t.Fatalf("Signal = %v, calls %+v", err, fake.calls)
	}
}

// TestSignalFinishedCompleted verifies that a completed invocation rejects late
// signals with the workflow not-found sentinel.
func TestSignalFinishedCompleted(t *testing.T) {
	t.Parallel()

	requireSignalFinished(t, nil)
}

// TestSignalFinishedFailed verifies that a failed invocation rejects late
// signals instead of accepting new durable state.
func TestSignalFinishedFailed(t *testing.T) {
	t.Parallel()

	requireSignalFinished(t, &failure{
		code:    500,
		message: "x",
	})
}

// TestSignalFinishedCanceled verifies that a canceled invocation rejects late
// signals with the workflow not-found sentinel.
func TestSignalFinishedCanceled(t *testing.T) {
	t.Parallel()

	requireSignalFinished(t, &failure{
		code: 409,
	})
}

// TestSignalFinishedUnknown verifies that an unknown invocation cannot accept a
// signal.
func TestSignalFinishedUnknown(t *testing.T) {
	t.Parallel()

	requireSignalFinished(t, errMissing)
}

// TestSignalNotStartedYet verifies that an accepted delayed invocation can
// receive its first signal before the Run handler starts.
func TestSignalNotStartedYet(t *testing.T) {
	t.Parallel()

	notRunning := &failure{
		code:    404,
		message: "run is not running",
	}
	fake := &fakeGateway{
		signalErr: notRunning,
		outputErr: errNotReady,
	}

	if err := newDriver(fake, driverSettings()).Signal(t.Context(), "w", "1", "go", nil); err != nil {
		t.Fatal(err)
	}

	last := fake.calls[len(fake.calls)-1]

	if last.method != "signal" || !last.signal.Force {
		t.Fatalf("calls = %+v", fake.calls)
	}
}

// TestSignalOtherError verifies that unexpected signal transport failures are
// returned without being mistaken for an ended invocation.
func TestSignalOtherError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	if err := newDriver(&fakeGateway{
		signalErr: boom,
	}, driverSettings()).Signal(t.Context(), "w", "1", "go", nil); !errors.Is(err, boom) {
		t.Fatalf("Signal = %v", err)
	}
}

// TestStartSeparatesTraceHeadersFromMetadata verifies the driver sends an
// explicit carrier in a namespace user metadata cannot overwrite.
func TestStartSeparatesTraceHeadersFromMetadata(t *testing.T) {
	t.Parallel()

	gateway := &fakeGateway{accepted: true}
	driver := newDriver(gateway, driverSettings())

	err := driver.Start(t.Context(), "flow", "run", []byte(`{"v":"input"}`), workflow.StartOptions{
		Trace:    map[string]string{"traceparent": "injected"},
		Metadata: map[string]string{"traceparent": "user-owned", "arkoren-trace-traceparent": "also-user-owned"},
	})

	if err != nil {
		t.Fatal(err)
	}

	headers := gateway.calls[0].start.headers

	if headers["arkoren-trace-traceparent"] != "injected" || headers["arkoren-meta-traceparent"] != "user-owned" {
		t.Fatalf("headers: %#v", headers)
	}
}
