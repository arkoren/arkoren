package restateworkflow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// Driver is a [workflow.Driver] that starts and controls runs through the
// Restate ingress and admin API. It runs no workflow code; the [Runner] of
// the worker process does. A Driver is safe for concurrent use.
// Workflow names and logical IDs remain separate; IDs are escaped only at the
// ingress transport boundary and remain unchanged in introspection queries.
//
// Example:
//
//	driver, err := NewDriver(restate.New())
//	if err != nil {
//		panic(err)
//	}
//	_ = workflow.NewClient(driver)
type Driver struct {
	// config is the configuration with defaults applied.
	config settings

	// gateway reaches the ingress and admin API.
	gateway gateway

	// namer maps workflow names to service names.
	namer serviceNamer
}

var _ workflow.Driver = (*Driver)(nil)

// NewDriver returns a Driver that talks to the Restate server of host. It
// binds nothing and performs no I/O, so a host used only by drivers can be
// client-only; see [restate.Builder.ClientOnly].
// Callbacks run once in order on one fresh [DriverBuilder]; nil entries panic.
// Published settings are independent of a retained draft.
//
// Example:
//
//	driver, err := NewDriver(restate.New(), func(driver *DriverBuilder) {
//		driver.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func NewDriver(host *restate.Host, configure ...DriverBuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new restate workflow driver: nil host")
	}

	return newDriver(hostGateway{
		host: host,
	}, driverSettings(configure...)), nil
}

// newDriver builds a Driver on gateway from completed construction settings.
// It normalizes defaults without invoking application callbacks again.
func newDriver(gateway gateway, config settings) *Driver {
	config.Prefix = cmp.Or(config.Prefix, "arkoren")
	config.Logger = cmp.Or(config.Logger, slog.Default())

	return &Driver{
		config:  config,
		gateway: gateway,
		namer:   newServiceNamer(config.Prefix),
	}
}

// Start submits run id of the workflow called name with a one-way send to the
// Run handler of its service. It returns [workflow.ErrAlreadyStarted] when
// Restate already accepted a run with this id and [workflow.ErrNotFound] when
// no deployed service has that name. StartOptions.Delay becomes a delayed
// send; StartOptions.Metadata becomes request headers with the prefix
// "arkoren-meta-".
// Trace uses separate arkoren-trace-* headers. Both maps are encoded into an
// independently owned header map before submission; neither is used as labels.
//
// Example:
//
//	if err := driver.Start(ctx, "onboard", "user/42", input, workflow.StartOptions{}); err != nil {
//		return err
//	}
func (driver *Driver) Start(ctx context.Context, name, id string, in []byte, options workflow.StartOptions) error {
	request := startRequest{
		delay:   options.Delay,
		headers: traceHeaders(options.Trace),
	}

	if len(options.Metadata) > 0 {
		for key, value := range options.Metadata {
			request.headers[metadataHeader+key] = value
		}
	}

	accepted, err := driver.gateway.start(ctx, driver.namer.service(name), id, in, request)

	if err != nil {
		return driver.fail("start", name, id, err)
	}

	if !accepted {
		return fmt.Errorf("start %s %s: %w", name, id, workflow.ErrAlreadyStarted)
	}

	return nil
}

// Result waits on the output of the run. A run canceled through Restate or
// [Driver.Cancel] gives [workflow.ErrCanceled]; other failures are decoded
// from the error envelope the [Runner] stored.
// In Restate's legacy profile, cancellation before execution discards the run:
// Result then reports ErrNotFound, not ErrCanceled. Retaining that result needs
// the server's persisted preflight_invocation_termination_retention feature and
// positive effective workflow retention. The driver does not infer that feature
// from a version string or maintain a process-local cancellation cache.
//
// Example:
//
//	output, err := driver.Result(ctx, "onboard", "user/42")
//	if err != nil {
//		return err
//	}
//	_ = output
func (driver *Driver) Result(ctx context.Context, name, id string) ([]byte, error) {
	out, err := driver.gateway.attach(ctx, driver.namer.service(name), id)

	if err != nil {
		return nil, driver.fail("result", name, id, err)
	}

	return out, nil
}

// Status reads the output of the run without waiting. A run without an output
// is [workflow.Running], including one that is delayed, suspended, or
// retrying. A failure with the Restate code 409 is [workflow.Canceled].
// Missing output needs an admin lookup to distinguish a delayed run from an
// unknown one. Admin failures remain errors, not evidence of absence. A run
// canceled before execution can disappear with ErrNotFound in the legacy server
// profile; see Result for the native retention requirement.
//
// Example:
//
//	status, err := driver.Status(ctx, "onboard", "user/42")
//	if err != nil {
//		return err
//	}
//	_ = status
func (driver *Driver) Status(ctx context.Context, name, id string) (workflow.Status, error) {
	_, err := driver.output(ctx, name, id)

	var terminal *failure

	switch {
	case err == nil:
		return workflow.Completed, nil
	case errors.Is(err, errNotReady):
		return workflow.Running, nil
	case errors.As(err, &terminal):
		if terminal.code == codeCanceled {
			return workflow.Canceled, nil
		}

		return workflow.Failed, nil
	default:
		return "", driver.fail("status", name, id, err)
	}
}

// Cancel looks up the run invocation in sys_invocation, calls the Cancel
// handler of the run, and cancels the invocation through the admin API. The
// handler closes Done at once when the attempt runs in the process of the
// handler; Restate delivers the admin cancellation at the next durable
// operation of the workflow. A run that already ended is not an error.
// The admin API must be reachable. Cancellation is cooperative at native
// invocation boundaries; it cannot forcibly stop arbitrary handler code.
// Before execution the legacy Restate profile drops the invocation rather than
// retaining a canceled result, as documented by Result. A successful admin
// cancellation can acknowledge an appended request before terminal completion;
// use Result to wait for the outcome.
//
// Example:
//
//	if err := driver.Cancel(ctx, "onboard", "user/42"); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, name, id string) error {
	info, err := driver.gateway.lookup(ctx, driver.namer.service(name), id)

	if err != nil {
		return driver.fail("cancel", name, id, err)
	}

	if info.completed {
		return nil
	}

	// Wake a run that waits on Done in this or another process; the admin
	// cancel below reaches it at its next durable operation anyway.
	if err := driver.gateway.interrupt(ctx, driver.namer.service(name), id); err != nil && ctx.Err() != nil {
		return driver.fail("cancel", name, id, err)
	}

	if err := driver.gateway.cancel(ctx, info.id); err != nil && !errors.Is(err, errMissing) {
		return driver.fail("cancel", name, id, err)
	}

	return nil
}

// Signal resolves the durable promise of the signal through the Signal handler
// of the run. The handler answers 404 unless the Run handler is running, which
// is [workflow.ErrNotFound] for an unknown or finished run. A run that was
// accepted but has not started yet, for example one that waits for its delay,
// also gets 404 from the handler; the driver then sees that the run has no
// output and sends the signal again with Force, and the promise keeps it
// until the run waits. Only the first value of a signal counts; later values
// are dropped without error.
// A failed readiness/result probe remains its original transport or admin
// failure; it is not converted to ErrNotFound and does not force delivery.
//
// Example:
//
//	if err := driver.Signal(ctx, "onboard", "user/42", "approved", approval); err != nil {
//		return err
//	}
func (driver *Driver) Signal(ctx context.Context, name, id, signal string, v []byte) error {
	service := driver.namer.service(name)
	request := signalRequest{
		Name:    signal,
		Payload: v,
	}

	err := driver.gateway.signal(ctx, service, id, request)

	var terminal *failure
	missing := errors.Is(err, errMissing)

	if errors.As(err, &terminal) {
		missing = terminal.code == http.StatusNotFound
	}

	if missing {
		err = driver.signalPending(ctx, name, id, request)
	}

	if err != nil {
		return driver.fail("signal", name, id, err)
	}

	return nil
}

// signalPending retries a signal only when the result probe proves the run is
// pending. Completed output and terminal failures mean the run ended; transport,
// context, and admin failures do not establish that fact and remain visible.
func (driver *Driver) signalPending(ctx context.Context, name, id string, request signalRequest) error {
	_, err := driver.output(ctx, name, id)

	if errors.Is(err, errNotReady) {
		request.Force = true

		return driver.gateway.signal(ctx, driver.namer.service(name), id, request)
	}

	var terminal *failure

	if err == nil || errors.As(err, &terminal) {
		return errMissing
	}

	return err
}

// output reads the result or determines whether an accepted invocation has not
// started yet. Only a missing output needs the admin lookup. Query failures
// remain failures rather than being misreported as absence.
func (driver *Driver) output(ctx context.Context, name, id string) ([]byte, error) {
	out, err := driver.gateway.output(ctx, driver.namer.service(name), id)

	if !errors.Is(err, errMissing) {
		return out, err
	}

	info, err := driver.gateway.lookup(ctx, driver.namer.service(name), id)

	if err != nil {
		return nil, err
	}

	if !info.completed {
		return nil, errNotReady
	}

	return nil, errMissing
}

// fail maps a gateway error to the workflow error of operation.
func (driver *Driver) fail(operation, name, id string, err error) error {
	var terminal *failure

	switch {
	case errors.Is(err, errMissing):
		return fmt.Errorf("%s %s %s: %w", operation, name, id, workflow.ErrNotFound)
	case errors.As(err, &terminal):
		return fromFailure(terminal)
	default:
		return fmt.Errorf("restate %s %s %s: %w", operation, name, id, err)
	}
}

// fromFailure translates the native gateway record through the shared protocol.
func fromFailure(terminal *failure) error {
	return protocol.FromFailure(terminal.code, terminal.message)
}
