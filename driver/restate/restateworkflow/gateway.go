package restateworkflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arkoren.dev/driver/restate"
	"arkoren.dev/driver/restate/restateworkflow/internal/protocol"
)

// codeCanceled is the Restate failure code of a canceled invocation.
const codeCanceled = protocol.CodeCanceled

// Errors a [gateway] returns for conditions the driver maps to workflow
// errors.
var (
	// errMissing reports an unknown service or run.
	errMissing = errors.New("restate: not found")

	// errNotReady reports a run without an output yet.
	errNotReady = errors.New("restate: not ready")
)

// failure is the terminal failure of a run, as the ingress reports it.
type failure struct {
	// code is the Restate failure code. 409 means canceled.
	code int

	// message is the failure message, an [workflow.EncodeError] envelope for
	// failures made by this driver.
	message string
}

// Error formats the native failure code and message for diagnostic reporting.
// The original fields remain available for contract error translation.
func (failure *failure) Error() string {
	return fmt.Sprintf("restate failure %d: %s", failure.code, failure.message)
}

// startRequest is one start of a run.
type startRequest struct {
	// delay postpones the run.
	delay time.Duration

	// headers are sent with the invocation.
	headers map[string]string
}

// invocationInfo describes the run invocation of a workflow.
type invocationInfo struct {
	// id is the invocation identifier.
	id string

	// completed is true once the invocation ended.
	completed bool
}

// gateway reaches the Restate ingress and admin API from outside a handler.
type gateway interface {
	// start submits the run. It returns false when the run was already
	// accepted.
	start(ctx context.Context, service, id string, in []byte, request startRequest) (bool, error)

	// attach waits for the output of the run.
	attach(ctx context.Context, service, id string) ([]byte, error)

	// output returns the output of the run, or [errNotReady].
	output(ctx context.Context, service, id string) ([]byte, error)

	// signal calls the Signal handler.
	signal(ctx context.Context, service, id string, request signalRequest) error

	// interrupt calls the Cancel handler of the run.
	interrupt(ctx context.Context, service, id string) error

	// lookup finds the run invocation, or returns [errMissing].
	lookup(ctx context.Context, service, id string) (invocationInfo, error)

	// cancel cancels an invocation.
	cancel(ctx context.Context, invocation string) error
}

// hostGateway is the [gateway] backed by a [restate.Host].
type hostGateway struct {
	// host provides the native clients and lifecycle shared with the other
	// drivers.
	host *restate.Host
}

// signalRequest is the shared signal protocol record.
type signalRequest = protocol.SignalRequest
