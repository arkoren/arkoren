package mailgunwebhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mailgun/mailgun-go/v5/events"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/driver/mailgun/mailgunwebhook"
)

// TestSourceRejectsControlCharactersInSigningKey checks configuration through
// the public source constructor. Embedded controls must not become invisible
// HMAC key bytes, and rejection retains the portable webhook.ErrInvalid identity.
func TestSourceRejectsControlCharactersInSigningKey(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"fixture\x00key", "fixture\x7fkey", "fixture\u0080key"} {
		_, err := mailgunwebhook.New(key)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Errorf("signing key containing controls: %v, want webhook.ErrInvalid", err)
		}
	}
}

// signedBody implements the published timestamp + token algorithm independently
// of the adapter. Event data is deliberately not part of the digest.
func signedBody(data string, issuedAt time.Time) string {
	timestamp := strconv.FormatInt(issuedAt.Unix(), 10)
	token := strings.Repeat("a", 50)
	mac := hmac.New(sha256.New, []byte("fixture-signing-key"))
	mac.Write([]byte(timestamp + token))

	return fmt.Sprintf(`{"signature":{"timestamp":%q,"token":%q,"signature":%q},"event-data":%s}`,
		timestamp, token, hex.EncodeToString(mac.Sum(nil)), data)
}

// eventData provides the mandatory event identity and native message metadata.
// Fixed event time makes it independent of receipt or signature time.
func eventData(kind string) string {
	return fmt.Sprintf(`{"id":"native-id","event":%q,"timestamp":1700000000.25,"recipient":"reader@example.com","message":{"headers":{"message-id":"message@example.com"}},"user-variables":[]}`, kind)
}

// handlerFor binds one native event using the real core request path.
// All receipt and error classification tests exercise the public adapter API.
func handlerFor[T any](t *testing.T, definition webhook.Event[mailgunwebhook.Envelope, T], accept webhook.AcceptFunc[T]) router.Handler {
	t.Helper()

	source, err := mailgunwebhook.New("fixture-signing-key")

	if err != nil {
		t.Fatal(err)
	}

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
		receiver.Handle(definition, accept)
	})

	if err != nil {
		t.Fatal(err)
	}

	return handler
}

// receipt sends a JSON POST directly through the endpoint and retains its error.
// No network or lifecycle service is required for protocol verification.
func receipt(handler router.Handler, body string) (*httptest.ResponseRecorder, error) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mailgun", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	err := handler(recorder, request)

	return recorder, err
}

func TestDeliveredUsesDayQualifiedEventIdentityAndNativeFields(t *testing.T) {
	t.Parallel()

	var received webhook.Delivery[events.Delivered]

	handler := handlerFor(t, mailgunwebhook.Delivered, func(_ context.Context, delivery webhook.Delivery[events.Delivered]) error {
		received = delivery

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now()))

	if err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d, %v", recorder.Code, err)
	}

	if received.ID != "2023-11-14:native-id" || received.Type != "delivered" {
		t.Fatalf("delivery identity: %+v", received)
	}

	if received.Payload.Recipient != "reader@example.com" || received.Payload.Message.Headers.MessageID != "message@example.com" {
		t.Fatalf("native fields: %+v", received.Payload)
	}

	if received.Payload.Timestamp != 1700000000.25 {
		t.Fatalf("event time: %v", received.Payload.Timestamp)
	}
}

func TestWrongSignatureRejectedAndRedacted(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("invalid signature accepted")

		return nil
	})

	body := strings.Replace(signedBody(eventData("delivered"), time.Now()), `"token":"a`, `"token":"b`, 1)

	recorder, err := receipt(handler, body)

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("signature receipt: %d, %v", recorder.Code, err)
	}

	if !errors.Is(err, webhook.ErrRejected) || strings.Contains(err.Error(), "fixture-signing-key") {
		t.Fatalf("error classification/redaction: %v", err)
	}
}

func TestOldSignatureRejected(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("old signature accepted")

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now().Add(-time.Hour)))

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("old signature receipt: %d, %v", recorder.Code, err)
	}
}

func TestFutureSignatureRejected(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("future signature accepted")

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now().Add(time.Hour)))

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("future signature receipt: %d, %v", recorder.Code, err)
	}
}

func TestMissingSignatureRejectedEvenForUnknownEvent(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("unauthenticated event accepted")

		return nil
	})

	recorder, err := receipt(handler, `{"event-data":{"id":"other","event":"future","timestamp":1700000000}}`)

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("missing signature: %d, %v", recorder.Code, err)
	}
}

func TestAuthenticatedUnknownEventAcknowledged(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("unbound event accepted")

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("future-event"), time.Now()))

	if err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("unknown receipt: %d, %v", recorder.Code, err)
	}
}

func TestVerifiedMalformedEventIsUnavailable(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("invalid envelope accepted")

		return nil
	})

	recorder, err := receipt(handler, signedBody(`{"event":"delivered"}`, time.Now()))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("malformed event: %d, %v", recorder.Code, err)
	}
}

func TestMalformedJSONRejectedWithoutSignatureMaterial(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("malformed JSON accepted")

		return nil
	})

	recorder, err := receipt(handler, `{"signature":`)

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("malformed wrapper: %d, %v", recorder.Code, err)
	}
}

func TestBoundWrongNativeFieldTypeIsUnavailable(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("invalid resource accepted")

		return nil
	})

	data := strings.Replace(eventData("delivered"), `"reader@example.com"`, `42`, 1)

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("invalid native field: %d, %v", recorder.Code, err)
	}
}

func TestFailureCarriesTemporarySeverity(t *testing.T) {
	t.Parallel()

	var received events.Failed

	handler := handlerFor(t, mailgunwebhook.Failed, func(_ context.Context, delivery webhook.Delivery[events.Failed]) error {
		received = delivery.Payload

		return nil
	})

	data := strings.TrimSuffix(eventData("failed"), "}") + `,"severity":"temporary","reason":"generic","delivery-status":{"code":421,"retry-seconds":600}}`

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("failure receipt: %d, %v", recorder.Code, err)
	}

	if received.Severity != "temporary" || received.DeliveryStatus.Code != 421 {
		t.Fatalf("failure payload: %+v", received)
	}
}

func TestUnknownFailureSeverityIsUnavailable(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Failed, func(context.Context, webhook.Delivery[events.Failed]) error {
		t.Fatal("unknown severity accepted")

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("failed"), time.Now()))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("failure schema: %d, %v", recorder.Code, err)
	}
}

func TestHMACDoesNotAuthenticateEventData(t *testing.T) {
	t.Parallel()

	var received string

	handler := handlerFor(t, mailgunwebhook.Delivered, func(_ context.Context, delivery webhook.Delivery[events.Delivered]) error {
		received = delivery.Payload.Recipient

		return nil
	})

	body := signedBody(eventData("delivered"), time.Now())
	body = strings.Replace(body, "reader@example.com", "changed@example.com", 1)

	recorder, err := receipt(handler, body)

	if err != nil || recorder.Code != http.StatusOK || received != "changed@example.com" {
		t.Fatalf("protocol boundary: %d, %q, %v", recorder.Code, received, err)
	}
}

func TestRepeatedTokenRemainsVerifiableWithoutDeduplication(t *testing.T) {
	t.Parallel()

	calls := 0

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		calls++

		return nil
	})

	body := signedBody(eventData("delivered"), time.Now())

	for range 2 {
		recorder, err := receipt(handler, body)

		if err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("replayed token: %d, %v", recorder.Code, err)
		}
	}

	if calls != 2 {
		t.Fatalf("unexpected source replay cache: %d", calls)
	}
}

func TestEventDeduplicationUsesEventDayNotSignatureToken(t *testing.T) {
	t.Parallel()

	source, err := mailgunwebhook.New("fixture-signing-key")

	if err != nil {
		t.Fatal(err)
	}

	store := cache.New(memorycache.New())
	calls := 0

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
		receiver.Deduplicate(store, cache.NewNamespace[bool]("mailgun-account", func(space *cache.NamespaceBuilder) {
			space.TTL(time.Hour)
		}))
		receiver.Handle(mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
			calls++

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now()))

		if err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("duplicate receipt: %d, %v", recorder.Code, err)
		}
	}

	data := strings.Replace(eventData("delivered"), "1700000000.25", "1700086400.25", 1)

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK || calls != 2 {
		t.Fatalf("new day collision: %d, calls %d, %v", recorder.Code, calls, err)
	}
}

func TestConstructionRejectsEmptyKeyAndNegativeTolerance(t *testing.T) {
	t.Parallel()

	_, err := mailgunwebhook.New("")

	if !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("empty key: %v", err)
	}

	_, err = mailgunwebhook.New("fixture-signing-key", func(signature *mailgunwebhook.Builder) {
		signature.Tolerance(-time.Second)
	})

	if !errors.Is(err, webhook.ErrInvalid) {
		t.Fatalf("negative tolerance: %v", err)
	}
}

func TestAcceptedDecodesNativeRecipient(t *testing.T) {
	t.Parallel()

	var recipient string

	handler := handlerFor(t, mailgunwebhook.Accepted, func(_ context.Context, delivery webhook.Delivery[events.Accepted]) error {
		recipient = delivery.Payload.Recipient

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("accepted"), time.Now()))

	if err != nil || recorder.Code != http.StatusOK || recipient != "reader@example.com" {
		t.Fatalf("accepted: %d, %q, %v", recorder.Code, recipient, err)
	}
}

func TestOpenedDecodesTrackingMetadata(t *testing.T) {
	t.Parallel()

	var received events.Opened

	handler := handlerFor(t, mailgunwebhook.Opened, func(_ context.Context, delivery webhook.Delivery[events.Opened]) error {
		received = delivery.Payload

		return nil
	})

	data := strings.TrimSuffix(eventData("opened"), "}") + `,"ip":"192.0.2.1","client-info":{"client-name":"Chrome"}}`

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK || received.ClientInfo.ClientName != "Chrome" {
		t.Fatalf("opened: %d, %+v, %v", recorder.Code, received, err)
	}
}

func TestClickedMapsNativeURLToApplicationPayload(t *testing.T) {
	t.Parallel()

	var received string

	mapped := mailgunwebhook.Clicked.Map(func(payload events.Clicked) (string, error) {
		return payload.Url, nil
	})

	handler := handlerFor(t, mapped, func(_ context.Context, delivery webhook.Delivery[string]) error {
		received = delivery.Payload

		return nil
	})

	data := strings.TrimSuffix(eventData("clicked"), "}") + `,"url":"https://example.com/news"}`

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK || received != "https://example.com/news" {
		t.Fatalf("clicked map: %d, %q, %v", recorder.Code, received, err)
	}
}

func TestUnsubscribedRetainsNativeTags(t *testing.T) {
	t.Parallel()

	var received events.Unsubscribed

	handler := handlerFor(t, mailgunwebhook.Unsubscribed, func(_ context.Context, delivery webhook.Delivery[events.Unsubscribed]) error {
		received = delivery.Payload

		return nil
	})

	data := strings.TrimSuffix(eventData("unsubscribed"), "}") + `,"tags":["newsletter"]}`

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("unsubscribe: %d, %v", recorder.Code, err)
	}

	if len(received.Tags) != 1 || received.Tags[0] != "newsletter" {
		t.Fatalf("tags: %+v", received)
	}
}

func TestComplainedRetainsRecipient(t *testing.T) {
	t.Parallel()

	var received events.Complained

	handler := handlerFor(t, mailgunwebhook.Complained, func(_ context.Context, delivery webhook.Delivery[events.Complained]) error {
		received = delivery.Payload

		return nil
	})

	recorder, err := receipt(handler, signedBody(eventData("complained"), time.Now()))

	if err != nil || recorder.Code != http.StatusOK || received.Recipient != "reader@example.com" {
		t.Fatalf("complaint: %d, %+v, %v", recorder.Code, received, err)
	}
}

func TestFailureCarriesPermanentSeverity(t *testing.T) {
	t.Parallel()

	var received events.Failed

	handler := handlerFor(t, mailgunwebhook.Failed, func(_ context.Context, delivery webhook.Delivery[events.Failed]) error {
		received = delivery.Payload

		return nil
	})

	data := strings.TrimSuffix(eventData("failed"), "}") + `,"severity":"permanent","reason":"bounce"}`

	recorder, err := receipt(handler, signedBody(data, time.Now()))

	if err != nil || recorder.Code != http.StatusOK || received.Severity != "permanent" {
		t.Fatalf("permanent failure: %d, %+v, %v", recorder.Code, received, err)
	}
}

func TestCustomToleranceIsSnapshotted(t *testing.T) {
	t.Parallel()

	var retained *mailgunwebhook.Builder

	source, err := mailgunwebhook.New("fixture-signing-key", func(signature *mailgunwebhook.Builder) {
		retained = signature
		signature.Tolerance(2 * time.Hour)
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Tolerance(time.Second)

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
		receiver.Handle(mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now().Add(-time.Hour)))

	if err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("snapshot: %d, %v", recorder.Code, err)
	}
}

func TestMalformedSignatureHexRejected(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("malformed digest accepted")

		return nil
	})

	body := strings.Replace(signedBody(eventData("delivered"), time.Now()), `"signature":"`, `"signature":"z`, 1)

	recorder, err := receipt(handler, body)

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, mailgunwebhook.ErrInvalidSignature) {
		t.Fatalf("hex receipt: %d, %v", recorder.Code, err)
	}
}

func TestInvalidMandatoryEventFieldsAreUnavailable(t *testing.T) {
	t.Parallel()

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		t.Fatal("invalid mandatory fields accepted")

		return nil
	})

	for _, data := range []string{
		`null`,
		`[]`,
		`{"id":"event","timestamp":1700000000}`,
		`{"id":"event","event":"delivered"}`,
		`{"id":"event","event":"delivered","timestamp":-1}`,
		`{"id":"event","event":"delivered","timestamp":253402300800}`,
		`{"id":"event","event":"delivered","timestamp":"1700000000"}`,
	} {
		recorder, err := receipt(handler, signedBody(data, time.Now()))

		if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
			t.Fatalf("invalid event %s: %d, %v", data, recorder.Code, err)
		}
	}
}

func TestHandlerFailureIsUnavailable(t *testing.T) {
	t.Parallel()

	failed := errors.New("persistence failed")

	handler := handlerFor(t, mailgunwebhook.Delivered, func(context.Context, webhook.Delivery[events.Delivered]) error {
		return failed
	})

	recorder, err := receipt(handler, signedBody(eventData("delivered"), time.Now()))

	if recorder.Code != http.StatusServiceUnavailable || !errors.Is(err, failed) {
		t.Fatalf("accept failure: %d, %v", recorder.Code, err)
	}
}
