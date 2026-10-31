package webhook

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"

	"arkoren.dev/core/http/request"
	"arkoren.dev/foundation/problem"
)

// bodyFailure answers a failed body read. A body over the limit keeps its own
// 413 problem representation, rendered locally before returning its receipt
// error so error mappers cannot replace it. A read cut short by cancellation or
// a timeout answers 503, not a malformed-request rejection; any other read error
// is a malformed body.
func (endpoint *endpoint[E]) bodyFailure(writer http.ResponseWriter, r *http.Request, err error) error {
	if errors.Is(err, request.ErrBodyTooLarge) {
		endpoint.emitRejected(r.Context(), RejectionReasonBodyTooLarge, http.StatusRequestEntityTooLarge)
		request.ErrBodyTooLarge.ServeHTTP(writer, r)

		return receiptError{class: ErrRejected, cause: err}
	}

	if readInterrupted(r.Context(), err) {
		return endpoint.unavailable(writer, r, FailureStageRead, err)
	}

	endpoint.emitRejected(r.Context(), RejectionReasonUnreadableBody, http.StatusBadRequest)

	return endpoint.reject(writer, r, http.StatusBadRequest, err)
}

// readInterrupted reports whether a body read failed because the request was
// canceled or timed out, rather than because the body was malformed.
func readInterrupted(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}

	timeout, ok := errors.AsType[net.Error](err)

	return ok && timeout.Timeout()
}

// verifyFailure answers a verifier error: a skip is acknowledged with
// [errAcknowledged], a mapped or [ErrRejected] error is a 4xx, anything else
// is retryable.
func (endpoint *endpoint[E]) verifyFailure(writer http.ResponseWriter, r *http.Request, err error) error {
	if errors.Is(err, ErrSkip) {
		return errAcknowledged
	}

	status := endpoint.settings.rejectStatus(err)

	if status == 0 {
		return endpoint.unavailable(writer, r, FailureStageVerify, err)
	}

	endpoint.emitRejected(r.Context(), RejectionReasonVerify, status)

	return endpoint.reject(writer, r, status, err)
}

// rejectStatus returns the 4xx status for a verify error: the first matching
// Reject mapping, then 400 for [ErrRejected]. Zero means the error is
// unclassified.
func (config settings[E]) rejectStatus(err error) int {
	for _, reject := range config.rejects {
		if slices.ContainsFunc(reject.errs, func(target error) bool { return errors.Is(err, target) }) {
			return reject.status
		}
	}

	if errors.Is(err, ErrRejected) {
		return http.StatusBadRequest
	}

	return 0
}

// emitRejected publishes the selected rejection category and response status,
// excluding request data and the cause from the event payload.
func (endpoint *endpoint[E]) emitRejected(ctx context.Context, reason RejectionReason, status int) {
	endpoint.settings.events.Emit(ctx, Rejected, RejectedEvent{
		Label:  endpoint.settings.label,
		Reason: reason,
		Status: status,
	})
}

// reject renders a safe 4xx problem and returns the receipt error.
func (endpoint *endpoint[E]) reject(writer http.ResponseWriter, r *http.Request, status int, cause error) error {
	ErrRejected.Derive(func(details *problem.Builder) {
		details.Status(status)
		details.Title(http.StatusText(status))
	}).ServeHTTP(writer, r)

	return receiptError{class: ErrRejected, cause: cause}
}

// unavailable publishes [Failed], renders the safe 503 problem and returns the
// receipt error. The stage and configured label reach the payload, never cause;
// the returned error retains cause for errors.Is and application error handling.
func (endpoint *endpoint[E]) unavailable(
	writer http.ResponseWriter,
	r *http.Request,
	stage FailureStage,
	cause error,
) error {
	endpoint.settings.events.Emit(r.Context(), Failed, FailedEvent{Label: endpoint.settings.label, Stage: stage})
	ErrUnavailable.ServeHTTP(writer, r)

	return receiptError{class: ErrUnavailable, cause: cause}
}

// receiptError is the error returned after the receiver rendered its own
// failure response. It matches both the stable class and the original cause,
// so logging and errors.Is keep working. The response is already committed, so
// the router and error mappers cannot change what the provider received.
type receiptError struct {
	// class is [ErrRejected] or [ErrUnavailable].
	class problem.Details

	// cause is the verifier, decoder, handler or read error.
	cause error
}

// Error includes the cause for logs; it is never written to a response.
func (receipt receiptError) Error() string {
	return fmt.Sprintf("%s: %v", receipt.class.Title(), receipt.cause)
}

// Unwrap returns the class followed by the cause.
func (receipt receiptError) Unwrap() []error {
	return []error{receipt.class, receipt.cause}
}
