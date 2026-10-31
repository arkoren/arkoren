// Package protocol owns Temporal raw JSON and native failure envelopes.
package protocol

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"arkoren.dev/core/service/workflow"
)

// ErrorType is the Temporal application error type of a failure that carries
// the [workflow.EncodeError] envelope as its first detail.
const ErrorType = "arkoren.error"

// Payload carries the facade bytes, which are always JSON, as a raw JSON value
// in the Temporal payload, so the Temporal UI shows plain JSON. A named type
// keeps the SDK from using its binary converter.
type Payload []byte

// tracedInput separates the propagation carrier from user metadata and facade
// payloads. It is only used when tracing is injected; legacy inputs stay valid.
type tracedInput struct {
	// Trace is opaque serialized propagation state, never observed as labels.
	Trace map[string]string `json:"arkoren_trace"`
	// Input retains the original facade envelope without decoding its contents.
	Input Payload `json:"input"`
}

// EncodeInput attaches a carrier without modifying the facade envelope. JSON
// encoding errors are returned rather than submitting a partially encoded run.
func EncodeInput(in Payload, carrier map[string]string) (Payload, error) {
	if len(carrier) == 0 {
		return in, nil
	}

	return json.Marshal(tracedInput{Trace: carrier, Input: in}, json.Deterministic(true))
}

// DecodeInput accepts both traced and legacy inputs. Decoding is deterministic
// and does not create spans, read the clock, or change orchestration history.
func DecodeInput(in Payload) (Payload, map[string]string, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(in), []byte("{")) {
		return in, nil, nil
	}

	var value tracedInput

	if err := json.Unmarshal(in, &value); err != nil {
		return nil, nil, err
	}

	if value.Trace == nil {
		return in, nil, nil
	}

	return value.Input, value.Trace, nil
}

// MarshalJSON returns the bytes as they are, or null when empty.
// Nonempty results alias the retained payload and must be treated as read-only
// by native converters; the method never changes the persisted envelope shape.
//
// Example:
//
//	value := payload(`{"v":true}`)
//	encoded, err := value.MarshalJSON()
//	if err != nil {
//		return err
//	}
//	_ = string(encoded) // {"v":true}
func (value Payload) MarshalJSON() ([]byte, error) {
	if len(value) == 0 {
		return []byte("null"), nil
	}

	return value, nil
}

// UnmarshalJSON keeps a copy of the raw value.
// It may reuse the receiver's storage, but never retains the input slice.
// Syntax validation belongs to the surrounding native JSON decoder.
//
// Example:
//
//	var value payload
//	input := []byte(`{"v":true}`)
//	if err := value.UnmarshalJSON(input); err != nil {
//		return err
//	}
//	clear(input) // The decoded payload remains unchanged.
func (value *Payload) UnmarshalJSON(data []byte) error {
	*value = append((*value)[:0], data...)

	return nil
}

// ErrTerminated reports a run that an operator terminated. Status reports such
// a run as [workflow.Failed].
var ErrTerminated = workflow.NewError("temporal.terminated", "temporal: workflow terminated")

// Failure returns the Temporal error that carries err and its envelope.
func Failure(err error) error {
	return temporal.NewApplicationErrorWithOptions(err.Error(), ErrorType, temporal.ApplicationErrorOptions{
		NonRetryable: errors.Is(err, workflow.ErrPermanent),
		Details:      []any{Payload(workflow.EncodeError(err))},
	})
}

// FromTemporal maps an error of the Temporal client or SDK to a workflow
// error. It walks the SDK's single-cause chain in order: an outer timeout must
// not be masked by its previous application failure, and an outer application
// failure must not become its timeout cause. Unknown errors, including composite
// errors outside the native failure chain, pass through without value equality.
func FromTemporal(err error) error {
	original := err

	for err != nil {
		// Match this node only. errors.As would descend into its cause and
		// could select a previous attempt instead of the terminal SDK failure.
		switch failure := any(err).(type) {
		case *temporal.TerminatedError:
			return fmt.Errorf("%w: %w", ErrTerminated, original)
		case *temporal.CanceledError:
			return fmt.Errorf("%w: %w", workflow.ErrCanceled, original)
		case *temporal.TimeoutError:
			return fmt.Errorf("%w: %w", workflow.ErrTimeout, original)
		case *temporal.ApplicationError:
			return applicationFailure(failure)
		case *serviceerror.NotFound:
			return fmt.Errorf("%w: %w", workflow.ErrNotFound, original)
		case *serviceerror.WorkflowExecutionAlreadyStarted, *temporal.ChildWorkflowExecutionAlreadyStartedError:
			return fmt.Errorf("%w: %w", workflow.ErrAlreadyStarted, original)
		}

		err = errors.Unwrap(err)
	}

	return original
}

// applicationFailure decodes the shipped Arkoren error envelope when present;
// otherwise it preserves the native application type and safe message. Malformed
// native details fall back to that same type/message rather than hiding failure.
func applicationFailure(app *temporal.ApplicationError) error {
	if app.Type() == ErrorType && app.HasDetails() {
		var envelope Payload

		if app.Details(&envelope) == nil {
			return workflow.DecodeError(envelope)
		}
	}

	return workflow.NewError(app.Type(), app.Message())
}
