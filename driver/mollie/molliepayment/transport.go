package molliepayment

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"

	"arkoren.dev/core/service/payment"
)

// responseLimit bounds a success body before decoding. Error bodies are never
// read or included in diagnostics; closing them does not drain untrusted data.
const responseLimit = 1 << 20

// operation is an internal fixed-origin request. A nonempty key identifies a
// POST mutation; reads carry no key and cannot create money-moving outcomes.
type operation struct {
	// path contains only validated native identifiers and fixed API segments.
	path string
	// key is the caller's stable Idempotency-Key, unmodified by the driver.
	key string
	// body is JSON request data, or nil for an observation.
	body any
}

// call prepares and dispatches exactly one operation. Pre-dispatch failures
// retain their ordinary category; only ambiguous dispatched mutations acquire
// ErrOutcomeUnknown. No read-after-write lookup is used to synthesize a result.
func (driver *Driver) call(ctx context.Context, action operation, output any) error {
	request, err := driver.request(ctx, action)

	if err != nil {
		return err
	}

	err = driver.exchange(request, output)

	if action.key != "" {
		return mutationError(err)
	}

	return err
}

// request uses deterministic JSON so explicit retries preserve request bytes,
// and checks cancellation immediately before handing off to the transport.
// GetBody is deliberately nil: net/http must not replay a POST on a reused
// connection merely because Idempotency-Key is set.
func (driver *Driver) request(ctx context.Context, action operation) (*http.Request, error) {
	if err := payment.CheckContext(ctx); err != nil {
		return nil, err
	}

	method := http.MethodGet
	var body io.Reader

	if action.body != nil {
		data, err := json.Marshal(action.body, json.Deterministic(true))

		if err != nil {
			return nil, &Error{cause: err}
		}

		body = bytes.NewReader(data)
		method = http.MethodPost
	}

	request, err := http.NewRequestWithContext(ctx, method, "https://api.mollie.com/v2"+action.path, body)

	if err != nil {
		return nil, &Error{cause: err}
	}

	request.GetBody = nil
	request.Header.Set("Authorization", "Bearer "+driver.apiKey)
	request.Header.Set("Accept", "application/hal+json")
	request.Header.Set("Content-Type", "application/json")

	if action.key != "" {
		request.Header.Set("Idempotency-Key", action.key)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return request, nil
}

// exchange is the dispatch boundary. All external error strings remain behind
// Error's redaction boundary; cancellation identity survives wrapping. Close
// cannot undo an observed result and is intentionally not a retry trigger.
// Malformed successful GET JSON wraps ErrUnavailable; mutation decoding errors
// retain their existing outcome classification at call's outer boundary.
func (driver *Driver) exchange(request *http.Request, output any) error {
	response, err := driver.http.Do(request)

	if err != nil {
		return fmt.Errorf("%w: %w", payment.ErrUnavailable, &Error{cause: err})
	}

	defer func() {
		// A bounded response is authoritative; cleanup cannot reverse its outcome.
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))

	if err != nil {
		return fmt.Errorf("%w: %w", payment.ErrUnavailable, &Error{cause: err})
	}

	if len(data) > responseLimit {
		return fmt.Errorf("%w: mollie response exceeds limit", payment.ErrUnavailable)
	}

	if err := json.Unmarshal(data, output); err != nil {
		failure := &Error{cause: err}

		if request.Method == http.MethodGet {
			return fmt.Errorf("%w: %w", payment.ErrUnavailable, failure)
		}

		return failure
	}

	return nil
}
