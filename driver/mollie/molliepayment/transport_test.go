package molliepayment_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/mollie/molliepayment"
)

// TestMalformedReadJSONIsUnavailable preserves a successful GET's decoder
// failure under the portable availability class without implying money moved.
func TestMalformedReadJSONIsUnavailable(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatal("observation dispatched a mutation")
		}

		return response(http.StatusOK, `{`), nil
	})

	_, err := driver.GetPayment(t.Context(), "tr_example")

	if !errors.Is(err, payment.ErrUnavailable) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("malformed read classification = %v", err)
	}

	var cause *jsontext.SyntacticError

	if !errors.As(err, &cause) {
		t.Fatalf("decoder cause lost: %v", err)
	}

	var native *molliepayment.Error

	if !errors.As(err, &native) || native.Status != 0 {
		t.Fatal("decoder failure must retain its documented zero status")
	}
}

// TestMalformedMutationJSONRetainsOutcomeClassification protects the existing
// mutation category while GET decoding acquires portable availability handling.
func TestMalformedMutationJSONRetainsOutcomeClassification(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusCreated, `{`), nil
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("existing mutation classification changed: %v", err)
	}

	var cause *jsontext.SyntacticError

	if !errors.As(err, &cause) {
		t.Fatalf("mutation decoder cause lost: %v", err)
	}
}

// TestPreCanceledMutationDoesNotDispatch distinguishes known no-dispatch
// cancellation from an uncertain accepted payment, including error identity.
func TestPreCanceledMutationDoesNotDispatch(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		t.Fatal("canceled request dispatched HTTP")

		return nil, io.EOF
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := driver.CreateCheckout(ctx, checkoutRequest())

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("pre-dispatch cancellation error = %v", err)
	}
}

// TestPostDispatchCancellationIsUnknown preserves cancellation identity but
// warns the caller that cancellation cannot undo a possibly accepted payment.
func TestPostDispatchCancellationIsUnknown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
		cancel()

		return nil, request.Context().Err()
	})

	_, err := driver.CreateCheckout(ctx, checkoutRequest())

	if !errors.Is(err, context.Canceled) || !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("post-dispatch cancellation error = %v", err)
	}
}

// TestCanceledObservationNeverImpliesMoneyMoved checks that the transport's
// shared cancellation behavior does not mark read-only requests as mutations.
func TestCanceledObservationNeverImpliesMoneyMoved(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})

	_, err := driver.GetPayment(t.Context(), "tr_example")

	if !errors.Is(err, context.Canceled) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("read cancellation error = %v", err)
	}
}

// TestRedirectDoesNotForwardAuthorization prevents even subdomain redirects
// from receiving the merchant key or replaying a money-moving request body.
func TestRedirectDoesNotForwardAuthorization(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		calls := 0
		driver := newDriver(t, func(request *http.Request) (*http.Response, error) {
			calls++

			if request.URL.Host != "api.mollie.com" || calls > 1 {
				t.Fatal("redirect forwarded credentials")
			}

			reply := response(status, "")
			reply.Header.Set("Location", "https://evil.api.mollie.com/steal")

			return reply, nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) || calls != 1 {
			t.Fatal("redirect followed or mutation ambiguity erased")
		}
	}
}

// TestProviderErrorBodyNeverLeaks tests free-form detail, header and malformed
// response text without storing or exposing any of it in a returned error.
func TestProviderErrorBodyNeverLeaks(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(401, `{"detail":"Bearer test_example secret@example.com"}`), nil
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrUnauthorized) || errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatalf("unauthorized response classification = %v", err)
	}

	if strings.Contains(err.Error(), "test_example") || strings.Contains(err.Error(), "secret@example.com") {
		t.Fatal("error body leaked")
	}

	var failure *molliepayment.Error

	if !errors.As(err, &failure) || failure.Status != 401 {
		t.Fatal("safe HTTP status unavailable")
	}
}

// TestTransportErrorTextIsRedacted proves even an injected transport that
// echoes a key cannot leak that text through ordinary driver error formatting.
func TestTransportErrorTextIsRedacted(t *testing.T) {
	t.Parallel()

	cause := errors.New("test_example https://secret.example/private")
	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return nil, cause
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if strings.Contains(fmt.Sprint(err), "test_example") || strings.Contains(fmt.Sprint(err), "secret.example") {
		t.Fatal("transport credentials leaked")
	}

	if !errors.Is(err, cause) || !errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal("redaction lost cause or outcome identity")
	}
}

// TestAmbiguousHTTPMutationStatusesRequireReconciliation includes conflicts
// because Mollie can return 409 while an idempotent first attempt is in flight.
func TestAmbiguousHTTPMutationStatusesRequireReconciliation(t *testing.T) {
	t.Parallel()

	for _, status := range []int{408, 409, 500, 502, 503} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			return response(status, `{"detail":"private"}`), nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("ambiguous status %d error = %v", status, err)
		}
	}
}

// TestKnownHTTPRejectionsAreNotUnknown ensures confirmed client rejections
// retain ordinary classifications rather than suggesting money moved.
func TestKnownHTTPRejectionsAreNotUnknown(t *testing.T) {
	t.Parallel()

	for _, status := range []int{400, 401, 403, 404, 405, 410, 415, 422, 429} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			return response(status, `{"detail":"private"}`), nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if err == nil || errors.Is(err, payment.ErrOutcomeUnknown) {
			t.Fatalf("known rejection %d error = %v", status, err)
		}
	}
}

// TestMalformedSuccessIsUnknown covers decoding failures after a mutation
// dispatch; arbitrary body text must remain absent from the safe error string.
func TestMalformedSuccessIsUnknown(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "test_example", `{"status":"test_example",`} {
		driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
			return response(201, body), nil
		})

		_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

		if !errors.Is(err, payment.ErrOutcomeUnknown) || strings.Contains(err.Error(), "test_example") {
			t.Fatalf("malformed response outcome = %v", err)
		}
	}
}

// TestOversizedSuccessIsBoundedAndUnknown verifies successful provider bodies
// cannot consume unbounded memory or establish a result after truncation.
func TestOversizedSuccessIsBoundedAndUnknown(t *testing.T) {
	t.Parallel()

	driver := newDriver(t, func(_ *http.Request) (*http.Response, error) {
		return response(201, strings.Repeat(" ", (1<<20)+1)), nil
	})

	_, err := driver.CreateCheckout(t.Context(), checkoutRequest())

	if !errors.Is(err, payment.ErrOutcomeUnknown) || !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("oversized success error = %v", err)
	}
}
