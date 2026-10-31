package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"arkoren.dev/core/http/client/internal/retry"
)

// RoundTrip sends one logical request under Transport's replay and retry policy.
// Each attempt clones req; retry callbacks run once per eligible phase, and a
// previous response is logged, drained, and closed before waiting and rewinding.
// A terminal response retains caller-owned body cleanup. Cancellation during a
// wait instead returns nil and the caller context's error. Native errors remain
// unchanged; request-rewind errors wrap their cause with operation context.
//
// Example:
//
//	// base, request, and destination belong to the caller.
//	transport := client.Transport(base)
//	response, err := transport.RoundTrip(request)
//	if err != nil {
//		return err
//	}
//	_, err = io.Copy(destination, response.Body)
//	return errors.Join(err, response.Body.Close())
func (retrytransport *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return retry.RoundTrip(req, retrytransport.next, retry.Policy{
		Attempts:       retrytransport.config.Attempts,
		Backoff:        retrytransport.config.Backoff,
		AttemptTimeout: retrytransport.config.AttemptTimeout,
		RetryIf:        retrytransport.config.RetryIf,
		Failed:         retrytransport.emitFailed,
		Retry:          retrytransport.logRetry,
	})
}

// emitFailed observes terminal results without consuming response bodies or
// exposing request paths, credentials, or bodies to lifecycle events.
func (retrytransport *retryTransport) emitFailed(ctx context.Context, req *http.Request, result retry.Result) {
	if retrytransport.config.Events == nil {
		return
	}

	payload := RequestFailedEvent{
		Method: req.Method, Host: req.URL.Hostname(), Attempts: result.Number, Err: result.Err,
	}

	if result.Response != nil {
		payload.StatusCode = result.Response.StatusCode
	}

	retrytransport.config.Events.Emit(ctx, RequestFailed, payload)
}

// logRetry observes a planned resend before response cleanup. Native errors
// contribute their type only; request values and error text remain private.
func (retrytransport *retryTransport) logRetry(ctx context.Context, req *http.Request, result retry.Result, delay time.Duration) {
	attrs := []slog.Attr{
		slog.String("http.request.method", req.Method),
		slog.String("server.address", req.URL.Hostname()),
		slog.Int("attempt", result.Number),
		slog.Duration("delay", delay),
	}

	if result.Err != nil {
		attrs = append(attrs, slog.String("error.type", fmt.Sprintf("%T", result.Err)))
	} else {
		attrs = append(attrs, slog.Int("http.response.status_code", result.Response.StatusCode))
	}

	retrytransport.config.Logger.LogAttrs(ctx, slog.LevelDebug, "client: retrying request", attrs...)
}
