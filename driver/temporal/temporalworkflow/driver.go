package temporalworkflow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"

	enumspb "go.temporal.io/api/enums/v1"

	"arkoren.dev/core/service/workflow"
	temporalhost "arkoren.dev/driver/temporal"
)

// errNotStarted reports a driver call before the host started.
var errNotStarted = errors.New("temporal host is not started")

// Driver is a [workflow.Driver] that starts and controls runs through the
// Temporal client of a host. It runs no workflow code; the [Runner] of the
// worker process does. A Driver is safe for concurrent use.
// Typed observations and commands verify the persisted native workflow type;
// namespace-wide ID uniqueness does not permit a wrong-name handle to use it.
//
// Example:
//
//	driver, err := NewDriver(temporalhost.New())
//	if err != nil {
//		return err
//	}
//	_ = workflow.NewClient(driver)
type Driver struct {
	// config is the configuration with defaults applied.
	config settings

	// backend reaches Temporal.
	backend backend
}

var _ workflow.Driver = (*Driver)(nil)

// NewDriver returns a Driver that talks to Temporal through host. The host
// needs no worker for that; use [temporalhost.Builder.NoWorker] in a process that
// only starts runs. Call it before or after host.Start; calls fail until the
// host has a native client. A NewFrom client is available immediately, whereas
// a dialing host acquires its client during Start. It performs no I/O.
// Callbacks run once in order on one fresh [DriverBuilder]; nil entries panic.
// Published settings are independent of a retained draft. [DriverBuilder.Registry]
// optionally supplies timeout and registration knowledge to a client-only driver.
//
// Example:
//
//	driver, err := NewDriver(temporalhost.New(), func(driver *DriverBuilder) {
//		driver.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func NewDriver(host *temporalhost.Host, configure ...DriverBuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new temporal workflow driver: nil host")
	}

	return newDriver(temporalBackend{
		client:    host.Client,
		taskQueue: host.TaskQueue(),
	}, driverSettings(configure...)), nil
}

// newDriver builds a Driver on backend.
func newDriver(backend backend, config settings) *Driver {
	config.Prefix = cmp.Or(config.Prefix, DefaultPrefix)
	config.Logger = cmp.Or(config.Logger, slog.Default())

	return &Driver{
		config:  config,
		backend: backend,
	}
}

// workflowType returns the Temporal workflow type of workflow name.
func (config settings) workflowType(name string) string {
	return config.Prefix + "-wf-" + name
}

// stepType returns the Temporal activity type of step name.
func (config settings) stepType(name string) string {
	return config.Prefix + "-step-" + name
}

// joinType returns the Temporal activity type that joins a run.
func (config settings) joinType() string {
	return config.Prefix + "-join"
}

// signalName returns the Temporal signal name of signal.
func (config settings) signalName(signal string) string {
	return config.Prefix + ".sig." + signal
}

// Start starts run id of the workflow called name with a workflow ID of id. It
// returns [workflow.ErrAlreadyStarted] when Temporal knows a run with this ID,
// also a finished one, and [workflow.ErrNotFound] when the configured registry
// lacks the workflow. StartOptions.Delay is the Temporal start delay and
// StartOptions.Metadata becomes the memo.
//
// Example:
//
//	err := driver.Start(ctx, "onboard", "user-42", []byte(`{"v":"Ada"}`), workflow.StartOptions{})
//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context, name, id string, in []byte, options workflow.StartOptions) error {
	input, err := encodeInput(payload(in), options.Trace)

	if err != nil {
		return fmt.Errorf("encode workflow input: %w", err)
	}

	request := startRequest{
		name:  driver.config.workflowType(name),
		id:    id,
		input: input,
		delay: options.Delay,
	}

	if driver.config.Registry != nil {
		handler, ok := driver.config.Registry.Workflow(name)

		if !ok {
			return fmt.Errorf("start %s %s: workflow is not registered: %w", name, id, workflow.ErrNotFound)
		}

		request.timeout = handler.Options.MaxDuration + options.Delay
	}

	if len(options.Metadata) > 0 {
		request.memo = make(map[string]any, len(options.Metadata))

		for key, value := range options.Metadata {
			request.memo[key] = value
		}
	}

	if err := driver.backend.start(ctx, request); err != nil {
		return driver.fail("start", name, id, err)
	}

	return nil
}

// Result waits for the run to end. A canceled run gives
// [workflow.ErrCanceled], a run that hit its execution timeout gives
// [workflow.ErrTimeout], and other failures are decoded from the error
// envelope the Runner stored. A wrong workflow name returns ErrNotFound before
// reading output; a native timeout outranks its last application-failure cause.
//
// Example:
//
//	output, err := driver.Result(ctx, "onboard", "user-42")
//	if err != nil {
//		return err
//	}
//	_ = output // Encoded facade output, not a native Temporal payload wrapper.
func (driver *Driver) Result(ctx context.Context, name, id string) ([]byte, error) {
	if _, err := driver.backend.describe(ctx, id, driver.config.workflowType(name)); err != nil {
		return nil, driver.fail("result", name, id, err)
	}

	out, err := driver.backend.result(ctx, id)

	if err != nil {
		return nil, driver.fail("result", name, id, err)
	}

	return out, nil
}

// Status describes the run. Completed, failed, timed out, and terminated runs
// map to [workflow.Completed] or [workflow.Failed], a canceled run to
// [workflow.Canceled], and a run that continued as new to
// workflow.Running. Missing IDs and mismatched workflow names return ErrNotFound.
//
// Example:
//
//	status, err := driver.Status(ctx, "onboard", "user-42")
//	if err != nil {
//		return err
//	}
//	_ = status == workflow.Running
func (driver *Driver) Status(ctx context.Context, name, id string) (workflow.Status, error) {
	status, err := driver.backend.describe(ctx, id, driver.config.workflowType(name))

	if err != nil {
		return "", driver.fail("status", name, id, err)
	}

	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return workflow.Completed, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return workflow.Canceled, nil
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return workflow.Failed, nil
	default:
		return workflow.Running, nil
	}
}

// Cancel verifies the named native run before requesting cancellation. A
// finished known run is not an error, including when it finishes between the
// description and cancellation request. Unknown IDs or mismatched workflow names
// return workflow.ErrNotFound without issuing a cancellation request.
//
// Example:
//
//	if err := driver.Cancel(ctx, "onboard", "user-42"); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, name, id string) error {
	if _, err := driver.backend.describe(ctx, id, driver.config.workflowType(name)); err != nil {
		return driver.fail("cancel", name, id, err)
	}

	err := driver.backend.cancel(ctx, id)

	if err == nil {
		return nil
	}

	if !errors.Is(fromTemporal(err), workflow.ErrNotFound) {
		return driver.fail("cancel", name, id, err)
	}

	return nil
}

// Signal sends the signal to the run. It returns [workflow.ErrNotFound] for an
// unknown or finished run. Temporal buffers a signal until the workflow reads
// it. A mismatched workflow name is rejected before delivery; description and
// command permissions are both required on the native client.
//
// Example:
//
//	if err := driver.Signal(ctx, "onboard", "user-42", "approved", []byte(`{"v":true}`)); err != nil {
//		return err
//	}
func (driver *Driver) Signal(ctx context.Context, name, id, signal string, v []byte) error {
	if _, err := driver.backend.describe(ctx, id, driver.config.workflowType(name)); err != nil {
		return driver.fail("signal", name, id, err)
	}

	if err := driver.backend.signal(ctx, id, driver.config.signalName(signal), payload(v)); err != nil {
		return driver.fail("signal", name, id, err)
	}

	return nil
}

// fail maps native failures and adds operation context without comparing error
// interface values. Arbitrary native errors may contain slices or maps and must
// remain wrappable and inspectable rather than panic during identity checking.
func (driver *Driver) fail(operation, name, id string, err error) error {
	return fmt.Errorf("temporal %s %s %s: %w", operation, name, id, fromTemporal(err))
}
