package webpushpush

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arkoren.dev/core/service/push"
)

// ProviderError records a rejected provider response without retaining its
// body, endpoint, headers or capability URL. Errors.As exposes safe metadata;
// errors.Is follows its portable push classification through Unwrap.
//
// Example:
//
//	var rejected *webpushpush.ProviderError
//	if errors.As(err, &rejected) {
//		log.Printf("push rejected: status=%d retry_after=%s", rejected.StatusCode, rejected.RetryAfter)
//	}
type ProviderError struct {
	// StatusCode is the authoritative HTTP response code, not a delivery result.
	// Example: if rejected.StatusCode == 410 { invalidateSubmittedVersion() }
	StatusCode int
	// RetryAfter is the nonnegative provider delay, or zero when absent,
	// invalid, in the past or too large for time.Duration. No retry is made.
	// Example: nextAttemptAt := time.Now().Add(rejected.RetryAfter)
	RetryAfter time.Duration
	// class is the portable classification, independent of provider text.
	class error
}

// Error produces safe diagnostic text containing only the numeric status.
// It never includes provider-controlled body text or endpoint capabilities.
//
// Example:
//
//	var rejected *webpushpush.ProviderError
//	if errors.As(err, &rejected) { log.Print(rejected.Error()) }
func (rejected *ProviderError) Error() string {
	return fmt.Sprintf("web push provider rejected request (status %d)", rejected.StatusCode)
}

// Unwrap returns the portable push classification for errors.Is checks, or nil
// when no contract sentinel describes the rejection. Authentication failures,
// redirects and other unclassified rejections remain typed provider errors;
// they do not mean the recipient is unsupported or its subscription expired.
//
// Example:
//
//	if errors.Is(err, push.ErrNotFound) { invalidateSubmittedVersion() }
func (rejected *ProviderError) Unwrap() error {
	return rejected.class
}

// safeError preserves native failures for explicit errors.As/Is inspection,
// but its text never formats potentially secret-bearing url.Error values.
type safeError struct {
	// operation describes a fixed, nonsecret failure phase.
	operation string
	// cause preserves native and portable error identity without formatting it.
	cause error
}

// Error returns only the static phase. Explicitly unwrapped native causes
// may contain confidential data and must not be logged indiscriminately.
// This method does not call the cause's Error method or copy the cause.
//
// Example:
//
//	err := &safeError{operation: "web push send failed", cause: context.Canceled}
//	_ = err.Error() // Only "web push send failed".
func (failure *safeError) Error() string {
	return failure.operation
}

// Unwrap permits callers to inspect cancellation and native transport errors.
// It returns the original shared cause, which is not necessarily safe to format.
// No cause means nil; callers should prefer errors.Is/As for classification.
//
// Example:
//
//	err := &safeError{operation: "web push send failed", cause: context.Canceled}
//	_ = errors.Is(err, context.Canceled)
func (failure *safeError) Unwrap() error {
	return failure.cause
}

// responseClass maps authoritative rejections. Unknown successes are outcome
// unknown rather than definitive failures that would encourage unsafe replay.
func responseClass(status int) error {
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return push.ErrNotFound
	case http.StatusTooManyRequests:
		return push.ErrRateLimited
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return push.ErrInvalidMessage
	}

	if status >= 500 && status <= 599 {
		return push.ErrUnavailable
	}

	if status >= 200 && status <= 299 {
		return push.ErrOutcomeUnknown
	}

	return nil
}

// retryAfter parses HTTP delay-seconds or an HTTP date, without overflow or
// negative delays. Invalid provider metadata does not change rejection class.
func retryAfter(value string, now time.Time) time.Duration {
	seconds, err := strconv.ParseUint(value, 10, 63)

	if err == nil {
		if seconds > uint64((1<<63-1)/int64(time.Second)) {
			return 0
		}

		return time.Duration(seconds) * time.Second
	}

	at, err := http.ParseTime(value)

	if err != nil || !at.After(now) {
		return 0
	}

	delay := at.Sub(now)

	if delay == time.Duration(1<<63-1) {
		return 0
	}

	return delay
}
