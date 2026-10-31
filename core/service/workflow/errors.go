package workflow

import (
	"errors"
)

// Error is a failure with a stable Kind that survives the wire. Drivers carry
// it with [EncodeError] and [DecodeError], and [errors.Is] matches two errors
// of the same non-empty Kind.
// Message text crosses process boundaries; use a stable, nonsensitive message
// rather than embedding credentials or input values in a failure.
//
//	declined := workflow.NewError("billing.card-declined", "payment declined")
//	if errors.Is(err, declined) {
//		return declined
//	}
type Error struct {
	// Kind classifies the failure, for example "billing.card_declined".
	Kind string

	// Message describes the failure for people.
	Message string
}

// NewError returns an [Error] with the given kind and message. Applications
// use it for failures that callers must recognize with [errors.Is] after the
// error crossed a process boundary. Kinds that start with "workflow." are
// reserved for the sentinels of this package; do not use them.
// Empty kinds do not match other errors by kind. NewError does not retain an
// underlying cause; it deliberately publishes only the supplied safe message.
//
//	declined := workflow.NewError("billing.card-declined", "payment declined")
//	return Receipt{}, declined
func NewError(kind, msg string) *Error {
	return &Error{
		Kind:    kind,
		Message: msg,
	}
}

// Error returns the message, or the kind when the message is empty.
// It adds no wrapping context, which keeps the cross-process failure readable.
//
//	failure := workflow.NewError("billing.card-declined", "payment declined")
//	fmt.Println(failure.Error()) // payment declined
func (err *Error) Error() string {
	if err.Message == "" {
		return err.Kind
	}

	return err.Message
}

// Is reports whether target is an [*Error] with the same non-empty Kind.
// Use errors.Is to find matching failures through wrapping and wire decoding.
//
//	target := workflow.NewError("billing.card-declined", "payment declined")
//	received := workflow.DecodeError(workflow.EncodeError(target))
//	fmt.Println(errors.Is(received, target)) // true
func (err *Error) Is(target error) bool {
	other, ok := target.(*Error)

	return ok && err.Kind != "" && err.Kind == other.Kind
}

// ErrNotFound reports an unknown workflow or run, or a signal sent to a run
// that is not running.
//
//	if errors.Is(err, workflow.ErrNotFound) {
//		return nil // The run has already disappeared or ended.
//	}
var ErrNotFound = NewError("workflow.not_found", "workflow: not found")

// ErrAlreadyStarted reports a run that already exists under the same name and
// identifier.
// Client.Start also returns the existing run handle for idempotent callers.
//
//	run, err := client.Start(ctx, Onboard, "user-42", user)
//	if err != nil && !errors.Is(err, workflow.ErrAlreadyStarted) {
//		return err
//	}
//	_ = run.ID()
var ErrAlreadyStarted = NewError("workflow.already_started", "workflow: already started")

// ErrCanceled reports a run, step, or wait that was canceled.
// Its kind survives the driver wire representation.
//
//	if errors.Is(err, workflow.ErrCanceled) {
//		return nil // Cancellation was the requested outcome.
//	}
var ErrCanceled = NewError("workflow.canceled", "workflow: canceled")

// ErrTimeout reports an expired wait, step timeout, or workflow
// [Builder.MaxDuration].
// Match the error rather than its persisted human-readable text.
//
//	if errors.Is(err, workflow.ErrTimeout) {
//		return workflow.NewError("onboarding.expired", "confirmation expired")
//	}
var ErrTimeout = NewError("workflow.timeout", "workflow: timeout")

// ErrUnsupported reports an operation the driver cannot perform.
// Select a compatible engine rather than retrying the same unsupported call.
//
//	if errors.Is(err, workflow.ErrUnsupported) {
//		return fmt.Errorf("this workflow needs a different engine: %w", err)
//	}
var ErrUnsupported = NewError("workflow.unsupported", "workflow: unsupported by driver")

// ErrNilFunc reports a definition built with a nil function, or a nil
// definition passed to [Handlers].
// This is a declaration error, not construction-callback misuse.
//
//	definition := workflow.NewStep[string, string]("charge", nil)
//	if errors.Is(definition.Err(), workflow.ErrNilFunc) {
//		return definition.Err()
//	}
var ErrNilFunc = errors.New("nil function")

// ErrInvalidName reports a name that is empty or has characters outside
// [a-z0-9-], or is otherwise rejected by the shared identifier rule. The
// definition constructors keep it on the value; [Handlers] returns it.
//
//	definition := workflow.New("Invalid", onboard)
//	if errors.Is(definition.Err(), workflow.ErrInvalidName) {
//		return definition.Err()
//	}
var ErrInvalidName = errors.New("invalid name")

// ErrInvalidID reports an empty run identifier, one containing ASCII control
// characters, or a child that uses the
// identifier of its own run.
// It rejects unsafe identities before a client submits work.
//
//	if _, err := client.Start(ctx, Onboard, "", user); errors.Is(err, workflow.ErrInvalidID) {
//		return err
//	}
var ErrInvalidID = errors.New("invalid run id")

// ErrInvalidOptions reports an out-of-range duration in workflow or step
// settings.
// Later declaration edits cannot clear the first invalid setting.
//
//	definition := workflow.New("onboard", onboard, func(definition *workflow.Builder) {
//		definition.MaxDuration(-time.Second)
//	})
//	if errors.Is(definition.Err(), workflow.ErrInvalidOptions) {
//		return definition.Err()
//	}
var ErrInvalidOptions = errors.New("invalid options")

// ErrDuplicateName reports two definitions registered under one name.
// Workflows and steps share the registry's name namespace.
//
//	_, err := workflow.Handlers(Onboard, Onboard)
//	if errors.Is(err, workflow.ErrDuplicateName) {
//		return err
//	}
var ErrDuplicateName = errors.New("duplicate workflow name")

// ErrMissingMaxDuration reports a workflow registered without
// [Builder.MaxDuration].
// Err deliberately leaves this required worker registration check to Handlers.
//
//	_, err := workflow.Handlers(workflow.New("onboard", onboard))
//	if errors.Is(err, workflow.ErrMissingMaxDuration) {
//		return err
//	}
var ErrMissingMaxDuration = errors.New("workflow requires MaxDuration")

// ErrNoCases reports a [Context.Select] without cases.
// Supply at least one signal or timer alternative before waiting.
//
//	if err := workflowContext.Select(); errors.Is(err, workflow.ErrNoCases) {
//		return "", err
//	}
var ErrNoCases = errors.New("select without cases")

// ErrPermanent matches, through [errors.Is], an error marked with
// [Permanent]. A step that fails with such an error stops at once even when
// attempts remain.
//
//	if errors.Is(err, workflow.ErrPermanent) {
//		return err // Do not apply an application-level retry around the step.
//	}
var ErrPermanent = errors.New("permanent failure")

// permanentError marks a failure that must not be retried.
type permanentError struct {
	// err preserves the original failure for Error and Unwrap.
	err error
}

// Error returns the message of the marked error unchanged.
// Retry policy is carried by the wrapper identity, not by a prefix inserted
// into persisted or model-facing text.
//
//	failure := workflow.Permanent(errors.New("payment declined"))
//	fmt.Println(failure.Error()) // payment declined
func (err *permanentError) Error() string {
	return err.err.Error()
}

// Unwrap returns the marked error so errors.Is and errors.As still see it.
// The original cause remains available locally; EncodeError separately decides
// which stable kind and safe message can cross a process boundary.
//
//	cause := errors.New("payment declined")
//	fmt.Println(errors.Unwrap(workflow.Permanent(cause)) == cause) // true
func (err *permanentError) Unwrap() error {
	return err.err
}

// Is reports whether target is [ErrPermanent].
// Cause matching is left to Unwrap, preserving both retry policy and the
// original error's identity without comparing human-readable text.
//
//	failure := workflow.Permanent(errors.New("payment declined"))
//	fmt.Println(errors.Is(failure, workflow.ErrPermanent)) // true
func (err *permanentError) Is(target error) bool {
	return target == ErrPermanent
}

// Permanent marks err as non-retryable: a step that returns it fails at once
// even when attempts remain. Check for the mark with
// errors.Is(err, [ErrPermanent]). Permanent(nil) is nil.
//
// Example:
//
//	return Receipt{}, workflow.Permanent(errors.New("card declined"))
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{
		err: err,
	}
}
