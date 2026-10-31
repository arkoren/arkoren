// Package protocol owns Restate handler identities, raw JSON and propagation.
package protocol

import (
	"fmt"
	"strings"
	"time"

	restatesdk "github.com/restatedev/sdk-go"

	"arkoren.dev/core/service/workflow"
)

const (
	// RunHandler is the name of the workflow run handler.
	RunHandler = "Run"

	// SignalHandler is the name of the shared handler that resolves signals.
	SignalHandler = "Signal"

	// CancelHandler is the name of the shared handler that stops the local
	// attempt of a run.
	CancelHandler = "Cancel"

	// SignalPromise prefixes the durable promise of a signal.
	SignalPromise = "sig:"

	// RunningKey is the state key that is true while the Run handler runs.
	RunningKey = "running"

	// AlreadyCompleted is part of the message of the failure Restate gives
	// for a second resolve of a promise.
	AlreadyCompleted = "already completed"

	// InvocationTimeout is the inactivity and abort timeout of the Run
	// handler. It is generous so a long step is not aborted by the server
	// defaults; MaxDuration is enforced by the runtime, not by these.
	InvocationTimeout = 24 * time.Hour

	// SpawnSettle is the durable timer after a Spawn.
	SpawnSettle = time.Millisecond

	// MetadataHeader prefixes the headers that carry [workflow.StartOptions]
	// metadata.
	MetadataHeader = "arkoren-meta-"
)

// Payload carries the facade bytes, which are always JSON, as a raw JSON value
// on the wire. Restate then sees plain application/json and a caller without
// the SDK can use the ingress.
type Payload []byte

// MarshalJSON returns the bytes as they are, or null when empty.
func (value Payload) MarshalJSON() ([]byte, error) {
	if len(value) == 0 {
		return []byte("null"), nil
	}

	return value, nil
}

// UnmarshalJSON keeps a copy of the raw value.
func (value *Payload) UnmarshalJSON(data []byte) error {
	*value = append((*value)[:0], data...)

	return nil
}

// SignalRequest is the input of the Signal handler.
type SignalRequest struct {
	// Name selects the application signal promise within the addressed workflow.
	// It is separate from the native Signal handler name used by the gateway.
	Name string `json:"name"`
	// Payload contains the opaque JSON value forwarded to application dispatch.
	Payload Payload `json:"payload"`

	// Force resolves the promise even when the run is not running yet, for
	// example while it waits for its delay.
	Force bool `json:"force,omitempty"`
}

// CodeCanceled is the Restate failure code of a canceled invocation.
const CodeCanceled = 409

// TraceHeader separates propagation fields from the user metadata namespace.
// Restate persists these invocation headers across retries and suspension.
const TraceHeader = "arkoren-trace-"

// TraceHeaders encodes carrier fields into application-owned invocation headers.
// No telemetry runs here: spawn forwarding must be deterministic on replay.
func TraceHeaders(carrier map[string]string) map[string]string {
	headers := make(map[string]string, len(carrier))

	for key, value := range carrier {
		headers[TraceHeader+key] = value
	}

	return headers
}

// TraceCarrier reads only the explicit start carrier, never attempt headers or
// arbitrary metadata. The result is owned by the caller and safe to forward.
func TraceCarrier(ctx restatesdk.RunContext) map[string]string {
	carrier := map[string]string{}

	for key, value := range ctx.Request().Headers.Iter() {
		if field, ok := strings.CutPrefix(strings.ToLower(key), TraceHeader); ok {
			carrier[field] = value
		}
	}

	return carrier
}

// FromFailure decodes the retained terminal envelope without numeric coercion.
func FromFailure(code int, message string) error {
	if code == CodeCanceled {
		return workflow.ErrCanceled
	}

	if message == "" {
		return workflow.NewError("", fmt.Sprintf("restate failure %d", code))
	}

	return workflow.DecodeError([]byte(message))
}
