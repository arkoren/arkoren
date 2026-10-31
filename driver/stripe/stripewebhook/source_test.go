package stripewebhook_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"
	stripehook "github.com/stripe/stripe-go/v87/webhook"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/stripe/stripewebhook"
)

// eventBody wraps a native resource in a current-version Stripe snapshot event.
// Fixed provider time permits exact observation assertions without sleeping.
func eventBody(kind, resource string) string {
	return fmt.Sprintf(`{"id":"evt_native","object":"event","type":%q,"api_version":%q,"created":1700000000,"data":{"object":%s}}`, kind, stripe.APIVersion, resource)
}

// signedRequest uses the SDK's signing fixture, not a real endpoint credential.
// It signs the exact test bytes with a fresh timestamp and performs no I/O.
func signedRequest(body string) *http.Request {
	signed := stripehook.GenerateTestSignedPayload(&stripehook.UnsignedPayload{
		Payload: []byte(body), Secret: "whsec_fixture", Timestamp: time.Now(),
	})
	request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(body))
	request.Header.Set("Stripe-Signature", signed.Header)

	return request
}

// newHandler binds exactly one observation event through the real core handler.
// Unknown events must therefore be authenticated and acknowledged as unbound.
func newHandler(
	t *testing.T,
	event webhook.Event[stripewebhook.Envelope, payment.Observation],
	accept webhook.AcceptFunc[payment.Observation],
) router.Handler {
	t.Helper()

	source, err := stripewebhook.New("whsec_fixture")

	if err != nil {
		t.Fatal(err)
	}

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
		receiver.Handle(event, accept)
	})

	if err != nil {
		t.Fatal(err)
	}

	return handler
}

func TestValidSignatureProducesNativePaymentObservation(t *testing.T) {
	t.Parallel()

	var delivery webhook.Delivery[payment.Observation]
	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(_ context.Context, received webhook.Delivery[payment.Observation]) error {
		delivery = received

		return nil
	})
	body := eventBody("payment_intent.succeeded", `{"id":"pi_native","object":"payment_intent","status":"succeeded","amount":2500,"currency":"usd"}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d; %s", recorder.Code, recorder.Body.String())
	}

	if delivery.ID != "evt_native" || delivery.Type != "payment_intent.succeeded" {
		t.Fatalf("native delivery: %+v", delivery)
	}

	if delivery.Payload.Kind != payment.PaymentSucceeded || delivery.Payload.PaymentID != "pi_native" {
		t.Fatalf("payment observation: %+v", delivery.Payload)
	}

	if delivery.Payload.Amount == nil || *delivery.Payload.Amount != (payment.Money{Amount: 2500, Currency: "USD"}) {
		t.Fatalf("observed money: %+v", delivery.Payload)
	}

	if delivery.Payload.OccurredAt.Unix() != 1700000000 {
		t.Fatalf("provider time: %v", delivery.Payload.OccurredAt)
	}
}

// TestAuthenticatedEnvelopePreservesCreatedWireTimestamp exposes the renamed
// Go field only after authentication while preserving Stripe's created member.
func TestAuthenticatedEnvelopePreservesCreatedWireTimestamp(t *testing.T) {
	t.Parallel()

	source, err := stripewebhook.New("whsec_fixture")

	if err != nil {
		t.Fatal(err)
	}

	timestamp := webhook.NewEvent("payment_intent.succeeded", func(envelope stripewebhook.Envelope) (int64, error) {
		return envelope.CreatedAt, nil
	})
	var occurredAt int64
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
		receiver.Handle(timestamp, func(_ context.Context, delivery webhook.Delivery[int64]) error {
			occurredAt = delivery.Payload

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	body := eventBody("payment_intent.succeeded", `{"id":"pi_native","object":"payment_intent","status":"succeeded"}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusOK || occurredAt != 1700000000 {
		t.Fatalf("authenticated timestamp = %d; receipt = %d", occurredAt, recorder.Code)
	}
}

func TestTamperedBodyRejectedBeforeResourceDecoding(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("tampered event accepted")

		return nil
	})
	request := signedRequest(`{"not":"a snapshot"}`)
	request.Body = http.NoBody
	recorder := httptest.NewRecorder()

	err := handler(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("tampered receipt: %d", recorder.Code)
	}

	if !errors.Is(err, webhook.ErrRejected) || !errors.Is(err, stripewebhook.ErrInvalidSignature) {
		t.Fatalf("signature error identities: %v", err)
	}

	if !errors.Is(err, stripehook.ErrNoValidSignature) {
		t.Fatalf("SDK signature cause was lost: %v", err)
	}

	if strings.Contains(err.Error(), "whsec_fixture") || strings.Contains(err.Error(), request.Header.Get("Stripe-Signature")) {
		t.Fatal("signature error disclosed credential or signature values")
	}
}

func TestStaleSignatureRejectedWithoutWaiting(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("stale event accepted")

		return nil
	})
	body := eventBody("payment_intent.succeeded", `{"id":"pi_native","object":"payment_intent","status":"succeeded"}`)
	request := signedRequest(body)
	signed := stripehook.GenerateTestSignedPayload(&stripehook.UnsignedPayload{
		Payload: []byte(body), Secret: "whsec_fixture", Timestamp: time.Unix(1700000000, 0),
	})
	request.Header.Set("Stripe-Signature", signed.Header)
	recorder := httptest.NewRecorder()

	err := handler(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("stale receipt: %d", recorder.Code)
	}

	if !errors.Is(err, stripehook.ErrTooOld) {
		t.Fatalf("SDK stale-signature cause was lost: %v", err)
	}
}

func TestBoundWrongAPIVersionIsUnavailableNotRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("wrong schema version accepted")

		return nil
	})
	body := eventBody("payment_intent.succeeded", `{"id":"pi_native","object":"payment_intent","status":"succeeded"}`)
	body = strings.Replace(body, stripe.APIVersion, "2020-08-27", 1)
	recorder := httptest.NewRecorder()

	err := handler(recorder, signedRequest(body))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("schema version classified as signature failure: %d; %v", recorder.Code, err)
	}
}

func TestBoundWrongResourceSchemaIsUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("wrong resource accepted")

		return nil
	})
	body := eventBody("payment_intent.succeeded", `{"id":"sub_wrong","object":"subscription","status":"active"}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("schema receipt: %d", recorder.Code)
	}
}

func TestAuthenticatedMalformedEnvelopeIsUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("malformed envelope accepted")

		return nil
	})
	recorder := httptest.NewRecorder()

	err := handler(recorder, signedRequest(`{"id":`))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("envelope receipt: %d; %v", recorder.Code, err)
	}
}

func TestUnknownUnboundVersionIsAuthenticatedAndAcknowledged(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, stripewebhook.PaymentIntentSucceeded, func(context.Context, webhook.Delivery[payment.Observation]) error {
		t.Fatal("unknown event reached bound callback")

		return nil
	})
	body := eventBody("future.resource.changed", `{"object":"future_resource"}`)
	body = strings.Replace(body, stripe.APIVersion, "future-version", 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unknown unbound receipt: %d; %s", recorder.Code, recorder.Body.String())
	}
}

func TestEventMapConvertsAfterVerification(t *testing.T) {
	t.Parallel()

	source, err := stripewebhook.New("whsec_fixture")

	if err != nil {
		t.Fatal(err)
	}

	mapped := stripewebhook.PaymentIntentSucceeded.Map(func(observation payment.Observation) (string, error) {
		return string(observation.PaymentID), nil
	})
	var received string
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[stripewebhook.Envelope]) {
		receiver.Handle(mapped, func(_ context.Context, delivery webhook.Delivery[string]) error {
			received = delivery.Payload

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	body := eventBody("payment_intent.succeeded", `{"id":"pi_native","object":"payment_intent","status":"succeeded","amount":2500,"currency":"usd"}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusOK || received != "pi_native" {
		t.Fatalf("mapped receipt: %d; %q", recorder.Code, received)
	}
}

func TestConstructionRejectsBadSecretAndTolerance(t *testing.T) {
	t.Parallel()

	_, err := stripewebhook.New("sk_test_not_a_signing_secret")

	if !errors.Is(err, webhook.ErrInvalid) || strings.Contains(err.Error(), "sk_test") {
		t.Fatalf("secret error: %v", err)
	}

	_, err = stripewebhook.New("whsec_fixture", func(signature *stripewebhook.Builder) {
		signature.Tolerance(-time.Second)
	})

	if !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("negative tolerance: %v", err)
	}
}

// TestInvalidSecretSkipsConstructionCallbacks preserves credential validation
// before policy execution, including a supplied nil policy entry.
func TestInvalidSecretSkipsConstructionCallbacks(t *testing.T) {
	t.Parallel()

	_, err := stripewebhook.New("", nil)

	if !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("invalid signing secret classification = %v", err)
	}
}

// TestValidSecretPropagatesNilConstructionCallback verifies that valid signing
// credentials do not hide application misuse of the construction policy.
func TestValidSecretPropagatesNilConstructionCallback(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = stripewebhook.New("whsec_fixture", nil)
}

func TestHandlerRequiresApplicationBinding(t *testing.T) {
	t.Parallel()

	source, err := stripewebhook.New("whsec_fixture")

	if err != nil {
		t.Fatal(err)
	}

	_, err = webhook.Handler(source)

	if !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("unconfigured handler: %v", err)
	}
}
