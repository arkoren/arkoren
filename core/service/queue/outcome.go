package queue

import (
	"time"
)

// RetryAfterError requests another attempt after a delay. The delay is a
// requested wait, not a guarantee: drivers may be unable to schedule delayed
// retries exactly or at all. [errors.Is] and [errors.AsType] traverse its
// cause. Create it with [RetryAfter].
// A nil cause is allowed; the result still requests retry rather than success.
//
//	err := queue.RetryAfter(errors.New("busy"), time.Second)
//	fmt.Println(err.RetryAfter()) // 1s
type RetryAfterError struct {
	// cause is the error that prompted the retry outcome.
	cause error

	// delay is the requested wait before the next attempt.
	delay time.Duration
}

// RejectedError requests terminal rejection. Broker dead-lettering, if any,
// is determined by the driver's backend configuration. Create it with
// [Reject].
// A nil cause is allowed; rejection remains an explicit settlement outcome.
//
//	err := queue.Reject(errors.New("invalid recipient"))
//	fmt.Println(err.Error())
type RejectedError struct {
	// cause is the error that prompted the rejection outcome.
	cause error
}

// permanentError requests terminal failure handling and matches
// [ErrPermanent] under [errors.Is]. Create it with [Permanent].
type permanentError struct {
	// cause is the error that prompted the failure outcome.
	cause error
}

// Error returns the underlying cause message, or a generic outcome message
// when no cause was provided.
// It describes the cause, not the requested delay or broker acceptance.
//
//	err := queue.RetryAfter(errors.New("busy"), time.Second)
//	fmt.Println(err.Error()) // busy
func (retryAfterError *RetryAfterError) Error() string {
	return jobOutcomeMessage(retryAfterError.cause)
}

// Unwrap returns the error that caused this outcome.
// A nil cause stays nil even though RetryAfter itself is a non-nil outcome.
//
//	cause := errors.New("busy")
//	err := queue.RetryAfter(cause, time.Second)
//	fmt.Println(errors.Is(err.Unwrap(), cause)) // true
func (retryAfterError *RetryAfterError) Unwrap() error {
	return retryAfterError.cause
}

// RetryAfter returns the requested delay before retrying.
// It retains the original value; the consumer normalizes a negative delay to
// zero when selecting settlement. Native transport timing remains driver-owned.
//
//	err := queue.RetryAfter(errors.New("busy"), time.Second)
//	fmt.Println(err.RetryAfter()) // 1s
func (retryAfterError *RetryAfterError) RetryAfter() time.Duration {
	return retryAfterError.delay
}

// Error returns the underlying cause message, or a generic outcome message
// when no cause was provided.
// It does not confirm that a broker accepted rejection.
//
//	err := queue.Reject(errors.New("invalid recipient"))
//	fmt.Println(err.Error())
func (rejectedError *RejectedError) Error() string {
	return jobOutcomeMessage(rejectedError.cause)
}

// Unwrap returns the error that caused this outcome.
// A nil rejection cause remains nil; the explicit rejection is still non-nil.
//
//	cause := errors.New("invalid recipient")
//	err := queue.Reject(cause)
//	fmt.Println(errors.Is(err.Unwrap(), cause)) // true
func (rejectedError *RejectedError) Unwrap() error {
	return rejectedError.cause
}

// Error returns the underlying cause message, or a generic outcome message
// when no cause was provided.
// Classification does not depend on this text: Is supplies ErrPermanent even
// when the explicit terminal outcome has no cause.
//
//	fmt.Println(queue.Permanent(nil).Error()) // job outcome
func (permanentError permanentError) Error() string {
	return jobOutcomeMessage(permanentError.cause)
}

// Unwrap returns the error that caused this outcome.
// A nil cause stays nil without clearing the independent permanent marker.
//
//	cause := errors.New("unsupported template")
//	fmt.Println(errors.Is(errors.Unwrap(queue.Permanent(cause)), cause))
func (permanentError permanentError) Unwrap() error {
	return permanentError.cause
}

// Is reports whether target is [ErrPermanent].
// Matching the original cause is handled separately through Unwrap by errors.Is.
//
//	fmt.Println(errors.Is(queue.Permanent(nil), queue.ErrPermanent)) // true
func (permanentError permanentError) Is(target error) bool {
	return target == ErrPermanent
}

// jobOutcomeMessage returns the cause message or a generic outcome message.
func jobOutcomeMessage(cause error) string {
	if cause == nil {
		return "job outcome"
	}

	return cause.Error()
}

// RetryAfter requests another attempt after delay, subject to the job's
// attempt limit. The delay is a request, not a guarantee; see
// [RetryAfterError]. A negative delay counts as zero. Ordinary handler errors
// are retried already; use RetryAfter only to choose the delay.
//
// Example:
//
//	return queue.RetryAfter(err, time.Minute)
func RetryAfter(err error, delay time.Duration) *RetryAfterError {
	return &RetryAfterError{
		cause: err,
		delay: delay,
	}
}

// Reject requests terminal rejection. Broker dead-lettering, if any, is
// determined by the driver's backend configuration. See [RejectedError].
//
// Example:
//
//	return queue.Reject(errors.New("missing user"))
func Reject(err error) *RejectedError {
	return &RejectedError{
		cause: err,
	}
}

// Permanent marks err as a permanent failure: the delivery is settled as
// failed and never retried, whatever attempts remain. A driver may archive or
// dead-letter it when its backend supports and is configured for that. The
// result matches [ErrPermanent] under [errors.Is] and still unwraps to err.
// Unlike broadcast.Permanent, Permanent(nil) is a non-nil terminal outcome.
// Check a fallible operation's error before wrapping it; return nil on success.
//
// Example:
//
//	return queue.Permanent(fmt.Errorf("unsupported plan: %w", err))
func Permanent(err error) error {
	return permanentError{
		cause: err,
	}
}
