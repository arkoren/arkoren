// Package retry owns replay safety, attempt lifetime, and retry sequencing.
// Callers supply policy and observations without transferring request ownership.
package retry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/internal/resend"
)

const (
	// MaxRetryAfter is the fixed server-directed wait cap.
	MaxRetryAfter = 10 * time.Second
	// drainLimit bounds best-effort response draining before retry waits.
	drainLimit = 4 << 10
)

// Result retains one attempt's native outcome and identity for observations.
type Result struct {
	// Number starts at one and includes rewind failures before native I/O.
	Number int
	// Response is returned unchanged on a terminal response; its body is owned
	// by the engine until it is returned or discarded before retrying.
	Response *http.Response
	// Err is the native transport error or contextual request-rewind error.
	Err error
}

// Policy contains completed callbacks and attempt limits, not construction code.
type Policy struct {
	// Attempts includes the initial attempt.
	Attempts int
	// Backoff runs before Retry-After selection, even when the header stops retry.
	Backoff func(int) time.Duration
	// AttemptTimeout remains active until the returned body is closed.
	AttemptTimeout time.Duration
	// RetryIf classifies outcomes only after the caller-context guard.
	RetryIf func(*http.Response, error) bool
	// Failed observes terminal failures without consuming their response body.
	Failed func(context.Context, *http.Request, Result)
	// Retry observes a selected retry before response cleanup and waiting.
	Retry func(context.Context, *http.Request, Result, time.Duration)
}

// engine carries completed request-local policy across retry phases.
type engine struct {
	// next performs one instrumented native request.
	next http.RoundTripper
	// policy contains completed settings and outer observations.
	policy Policy
}

// RoundTrip sends one logical request. Cancellation checks, policy callbacks,
// observations, cleanup, waits, and rewinds retain their distinct ordered phases.
func RoundTrip(req *http.Request, next http.RoundTripper, policy Policy) (*http.Response, error) {
	sender := engine{next: next, policy: policy}

	return sender.roundTrip(req)
}

// roundTrip enforces replay safety before entering the attempt loop.
func (sender engine) roundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	attempts := sender.policy.Attempts

	if !canRepeat(req) {
		attempts = 1
	}

	for attempt := 1; ; attempt++ {
		resp, err := sender.attempt(req, attempt)
		result := Result{Number: attempt, Response: resp, Err: err}

		if sender.finishAttempt(ctx, req, result, attempts) {
			return resp, err
		}

		delay, allowed := sender.retryDelay(ctx, result)

		if !allowed {
			sender.policy.Failed(ctx, req, result)

			return resp, nil
		}

		sender.policy.Retry(ctx, req, result, delay)

		if err := waitForRetry(ctx, result.Response, delay); err != nil {
			return nil, err
		}
	}
}

// finishAttempt checks cancellation before invoking RetryIf exactly once, even
// on the final attempt. Callback cancellation belongs to subsequent phases.
func (sender engine) finishAttempt(ctx context.Context, req *http.Request, result Result, attempts int) bool {
	if ctx.Err() != nil {
		return true
	}

	retry := sender.policy.RetryIf(result.Response, result.Err)

	if retry && result.Number < attempts {
		return false
	}

	if retry || result.Err != nil {
		sender.policy.Failed(ctx, req, result)
	}

	return true
}

// retryDelay invokes Backoff before inspecting server-directed waits. A refused
// wait leaves the response unconsumed; callback cancellation does not skip cleanup.
func (sender engine) retryDelay(ctx context.Context, result Result) (time.Duration, bool) {
	delay := max(sender.policy.Backoff(result.Number+1), 0)

	if result.Response == nil {
		return delay, true
	}

	status := result.Response.StatusCode

	if status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		return delay, true
	}

	wait, ok := retryAfter(result.Response)

	if !ok {
		return delay, true
	}

	deadline, deadlineSet := ctx.Deadline()

	if wait > MaxRetryAfter || deadlineSet && time.Now().Add(wait).After(deadline) {
		return 0, false
	}

	return wait, true
}

// waitForRetry drains and closes the discarded response before starting its
// timer, including when an earlier callback has already canceled the context.
func waitForRetry(ctx context.Context, response *http.Response, delay time.Duration) error {
	if response != nil {
		// Cleanup is best effort; native read/close failures do not block retries.
		_, _ = io.CopyN(io.Discard, response.Body, drainLimit)
		_ = response.Body.Close()
	}

	timer := time.NewTimer(delay)

	select {
	case <-ctx.Done():
		timer.Stop()

		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// attempt clones the request and rewinds only subsequent bodies. Resend context
// identity is shared with telemetry. Returned bodies retain the attempt context
// and their native write capability until once-only closure releases it.
func (sender engine) attempt(req *http.Request, attempt int) (*http.Response, error) {
	ctx := req.Context()

	if attempt > 1 {
		ctx = resend.NewContext(ctx, attempt-1)
	}

	cancel := context.CancelFunc(func() {
	})

	if sender.policy.AttemptTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, sender.policy.AttemptTimeout)
	}

	clone := req.Clone(ctx)

	if attempt > 1 && req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()

		if err != nil {
			cancel()

			return nil, fmt.Errorf("client: rewinding request body: %w", err)
		}

		clone.Body = body
	}

	resp, err := sender.next.RoundTrip(clone)

	if err != nil {
		cancel()

		return nil, err
	}

	body := &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	resp.Body = body

	if writer, ok := body.ReadCloser.(io.Writer); ok {
		resp.Body = &writableCancelBody{cancelBody: body, Writer: writer}
	}

	return resp, nil
}

// canRepeat requires an unchanged idempotent method or explicit key and a
// rewindable body. Outcome policy cannot override either safety boundary.
func canRepeat(req *http.Request) bool {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete}

	if !slices.Contains(methods, req.Method) && req.Header.Get("Idempotency-Key") == "" {
		return false
	}

	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}
