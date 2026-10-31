package paypalpayment_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"arkoren.dev/core/service/payment"
)

// TestCancelDispatchedFailureDoesNotReconcileOrReplay permits the lifecycle
// preflight but no subsequent GET or second POST after an ambiguous dispatch.
func TestCancelDispatchedFailureDoesNotReconcileOrReplay(t *testing.T) {
	t.Parallel()

	cause := errors.New("caller stopped waiting for cancellation")
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() {
		cancel(nil)
	})

	reads, mutations := 0, 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			reads++

			return response(200, `{"id":"I-SUB","status":"ACTIVE"}`), nil
		}

		mutations++
		cancel(cause)

		return nil, request.Context().Err()
	}))

	err := backend.CancelSubscription(ctx, payment.NewCancelSubscriptionRequest("I-SUB"))

	for _, target := range []error{payment.ErrOutcomeUnknown, context.Canceled, cause} {
		if !errors.Is(err, target) {
			t.Fatalf("cancellation lost error identity %v: %v", target, err)
		}
	}

	if reads != 1 || mutations != 1 {
		t.Fatalf("ambiguous cancellation performed reads=%d mutations=%d", reads, mutations)
	}
}

// TestSubscriptionReadsObservedSchedule keeps next billing time optional and
// distinct from paid-through or any successful capture observation.
func TestSubscriptionReadsObservedSchedule(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	if err := provider.activate(t.Context(), "I-SUB"); err != nil {
		t.Fatal(err)
	}

	subscription, err := backend.GetSubscription(t.Context(), "I-SUB")

	if err != nil {
		t.Fatal(err)
	}

	if subscription.NextBillingAt == nil || subscription.Status != payment.SubscriptionStatusActive {
		t.Fatal("missing observed lifecycle/schedule")
	}
}

// TestCancel204DoesNotHydrate verifies immediate no-content cancellation with
// one pre-read and no unsupported request-id header or post-mutation read.
func TestCancel204DoesNotHydrate(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)
	provider.subscriptionStatus = "ACTIVE"

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("I-SUB"))

	if err != nil {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/billing/subscriptions/I-SUB")) != 1 {
		t.Fatal("cancel performed a post-204 GET")
	}

	canceled := provider.calls("/v1/billing/subscriptions/I-SUB/cancel")

	if len(canceled) != 1 || canceled[0].header.Get("PayPal-Request-Id") != "" {
		t.Fatal("cancel repeated or advertised unsupported idempotency")
	}

	if string(canceled[0].body) != `{"reason":"Customer request"}` {
		t.Fatal("missing default cancellation reason")
	}
}

// TestCancelConflictAllowsOneCanceledObservation handles a concurrent cancel
// without replaying the unsupported-idempotency cancel mutation.
func TestCancelConflictAllowsOneCanceledObservation(t *testing.T) {
	t.Parallel()

	reads, cancels := 0, 0
	backend := backendWith(t, authenticated(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/cancel") {
			cancels++

			return response(422, `{"name":"UNPROCESSABLE_ENTITY","details":[{"issue":"SUBSCRIPTION_STATUS_INVALID"}]}`), nil
		}

		reads++

		if reads == 1 {
			return response(200, `{"id":"I-SUB","status":"ACTIVE"}`), nil
		}

		return response(200, `{"id":"I-SUB","status":"CANCELLED"}`), nil
	}))

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("I-SUB"))

	if err != nil {
		t.Fatal(err)
	}

	if reads != 2 || cancels != 1 {
		t.Fatal("cancel conflict caused retry or unbounded reconciliation")
	}
}

// TestPendingSubscriptionCannotCancel makes pending lifecycle an explicit state
// failure rather than issuing a cancel that the provider will reject.
func TestPendingSubscriptionCannotCancel(t *testing.T) {
	t.Parallel()

	provider, backend := newMerchant(t)

	err := backend.CancelSubscription(t.Context(), payment.NewCancelSubscriptionRequest("I-SUB"))

	if !errors.Is(err, payment.ErrInvalidState) {
		t.Fatal(err)
	}

	if len(provider.calls("/v1/billing/subscriptions/I-SUB/cancel")) != 0 {
		t.Fatal("pending subscription was canceled")
	}
}
