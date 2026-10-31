package resendwebhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/driver/resend/resendwebhook"
)

// TestSourceRejectsLineBreaksInSigningSecret prevents the base64 decoder's
// permissive CR/LF handling from silently repairing endpoint credentials. The
// public constructor must retain webhook.ErrInvalid for malformed secrets.
func TestSourceRejectsLineBreaksInSigningSecret(t *testing.T) {
	t.Parallel()

	for _, secret := range []string{
		"whsec_c2ln\nbmluZy1zZWNyZXQ=",
		"whsec_c2ln\rbmluZy1zZWNyZXQ=",
	} {
		_, err := resendwebhook.New(secret)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Errorf("signing secret containing line breaks: %v, want webhook.ErrInvalid", err)
		}
	}
}

// secret is a deterministic local fixture, not an endpoint credential.
const secret = "whsec_c2lnbmluZy1zZWNyZXQ="

// delivered is intentionally not timestamped at receipt time: event creation
// does not participate in the signature freshness policy.
const delivered = `{"type":"email.delivered","created_at":"2026-02-22T23:41:12.126Z","data":{"email_id":"email_123","from":"sender@example.com","to":["customer@example.com"],"subject":"Receipt","tags":{"category":"receipt"}}}`

// signedRequest independently implements the provider's documented fixture
// algorithm. Explicit attempt times test replay policy without real sleeps.
func signedRequest(body, id string, timestamp int64) *http.Request {
	seconds := strconv.FormatInt(timestamp, 10)
	digest := hmac.New(sha256.New, []byte("signing-secret"))
	digest.Write([]byte(id + "." + seconds + "." + body))

	request := httptest.NewRequest(http.MethodPost, "/resend", strings.NewReader(body))
	request.Header.Set("svix-id", id)
	request.Header.Set("svix-timestamp", seconds)
	request.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(digest.Sum(nil)))

	return request
}

// newHandler binds one email definition through core, preserving its actual
// safe-response and error classification behavior instead of testing internals.
func newHandler(t *testing.T, event webhook.Event[resendwebhook.Envelope, resendwebhook.Email], accept webhook.AcceptFunc[resendwebhook.Email]) router.Handler {
	t.Helper()

	source, err := resendwebhook.New(secret)

	if err != nil {
		t.Fatal(err)
	}

	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
		receiver.Handle(event, accept)
	})

	if err != nil {
		t.Fatal(err)
	}

	return handler
}

// rejectAccept ensures failed or unknown notifications never reach user code.
func rejectAccept(t *testing.T) webhook.AcceptFunc[resendwebhook.Email] {
	t.Helper()

	return func(context.Context, webhook.Delivery[resendwebhook.Email]) error {
		t.Error("unexpected application handler call")

		return nil
	}
}

func TestDeliveredUsesSignedMessageIdentity(t *testing.T) {
	t.Parallel()

	var received webhook.Delivery[resendwebhook.Email]
	handler := newHandler(t, resendwebhook.EmailDelivered, func(_ context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
		received = delivery

		return nil
	})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(delivered, "msg_stable", time.Now().Unix()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d; %s", recorder.Code, recorder.Body.String())
	}

	if received.ID != "msg_stable" || received.Type != "email.delivered" {
		t.Fatalf("delivery identity: %+v", received)
	}

	if received.Payload.EmailID != "email_123" || received.Payload.Tags["category"] != "receipt" {
		t.Fatalf("email metadata: %+v", received.Payload)
	}
}

func TestTamperedPayloadRejectsBeforeJSONParsing(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	request := signedRequest(delivered, "msg_stable", time.Now().Unix())
	request.Body = http.NoBody
	recorder := httptest.NewRecorder()

	err := handler(recorder, request)

	if recorder.Code != http.StatusBadRequest || !errors.Is(err, resendwebhook.ErrInvalidSignature) {
		t.Fatalf("signature failure: %d; %v", recorder.Code, err)
	}

	if !errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("missing rejection identity: %v", err)
	}

	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), request.Header.Get("svix-signature")) {
		t.Fatalf("authentication error leaks credentials: %v", err)
	}
}

func TestMissingSignatureRejectsUnknownEvent(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	request := signedRequest(`{"type":"future.event"}`, "msg_stable", time.Now().Unix())
	request.Header.Del("svix-signature")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsigned unknown receipt: %d", recorder.Code)
	}
}

func TestStaleSignatureRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(delivered, "msg_stable", time.Now().Add(-6*time.Minute).Unix()))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("stale receipt: %d", recorder.Code)
	}
}

func TestFutureSignatureRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(delivered, "msg_stable", time.Now().Add(6*time.Minute).Unix()))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("future receipt: %d", recorder.Code)
	}
}

func TestUnsupportedSignatureVersionRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	request := signedRequest(delivered, "msg_stable", time.Now().Unix())
	request.Header.Set("svix-signature", strings.Replace(request.Header.Get("svix-signature"), "v1,", "v2,", 1))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsupported version receipt: %d", recorder.Code)
	}
}

func TestRotatingSignaturesAcceptMatchingV1(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))

	// Use an unbound event so no application callback is needed.
	request := signedRequest(`{"type":"future.event"}`, "msg_stable", time.Now().Unix())
	request.Header.Set("svix-signature", "v1,invalid v2,ignored "+request.Header.Get("svix-signature"))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("rotating signature receipt: %d", recorder.Code)
	}
}

func TestAuthenticatedUnknownEventAcknowledged(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(`{"type":"future.event","data":[1,2]}`, "msg_stable", time.Now().Unix()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("unknown receipt: %d", recorder.Code)
	}
}

func TestAuthenticatedMalformedEnvelopeUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	recorder := httptest.NewRecorder()

	err := handler(recorder, signedRequest(`{"type":`, "msg_stable", time.Now().Unix()))

	if recorder.Code != http.StatusServiceUnavailable || errors.Is(err, webhook.ErrRejected) {
		t.Fatalf("malformed receipt: %d; %v", recorder.Code, err)
	}
}

func TestBoundMalformedEmailUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	body := strings.Replace(delivered, `"email_id":"email_123"`, `"email_id":123`, 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_stable", time.Now().Unix()))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid email receipt: %d", recorder.Code)
	}
}

func TestBoundMissingEmailIdentityUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	body := strings.Replace(delivered, `"email_id":"email_123",`, "", 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_stable", time.Now().Unix()))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing email identity receipt: %d", recorder.Code)
	}
}

func TestBouncedPreservesClassificationAndDiagnostics(t *testing.T) {
	t.Parallel()

	var received resendwebhook.Email
	handler := newHandler(t, resendwebhook.EmailBounced, func(_ context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
		received = delivery.Payload

		return nil
	})
	body := `{"type":"email.bounced","created_at":"2026-02-22T23:41:12.126Z","data":{"email_id":"email_123","bounce":{"type":"Permanent","subType":"Suppressed","message":"Mailbox unavailable","diagnosticCode":["smtp; 550"]}}}`
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_bounce", time.Now().Unix()))

	if recorder.Code != http.StatusOK || received.Bounce == nil {
		t.Fatalf("bounce receipt: %d; %+v", recorder.Code, received)
	}

	if received.Bounce.SubType != "Suppressed" || received.Bounce.DiagnosticCode[0] != "smtp; 550" {
		t.Fatalf("bounce details: %+v", received.Bounce)
	}
}

func TestReceivedPreservesForwardingMetadata(t *testing.T) {
	t.Parallel()

	var received resendwebhook.Email
	handler := newHandler(t, resendwebhook.EmailReceived, func(_ context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
		received = delivery.Payload

		return nil
	})
	body := `{"type":"email.received","created_at":"2026-02-22T23:41:12.126Z","data":{"email_id":"email_123","message_id":"<123@example.com>","cc":["copy@example.com"],"received_for":["forwarded@example.com"]}}`
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_inbound", time.Now().Unix()))

	if recorder.Code != http.StatusOK || received.MessageID != "<123@example.com>" {
		t.Fatalf("received email receipt: %d; %+v", recorder.Code, received)
	}

	if len(received.ReceivedFor) != 1 || received.ReceivedFor[0] != "forwarded@example.com" {
		t.Fatalf("forwarding metadata: %+v", received)
	}
}

func TestDeduplicationUsesMessageIDNotEmailID(t *testing.T) {
	t.Parallel()

	source, err := resendwebhook.New(secret)

	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
		receiver.Deduplicate(cache.New(memorycache.New()), cache.NewNamespace[bool]("resend-fixture", func(space *cache.NamespaceBuilder) {
			space.TTL(time.Hour)
		}))
		receiver.Handle(resendwebhook.EmailDelivered, func(context.Context, webhook.Delivery[resendwebhook.Email]) error {
			calls++

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"msg_one", "msg_one", "msg_two"} {
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, signedRequest(delivered, id, time.Now().Unix()))

		if recorder.Code != http.StatusOK {
			t.Fatalf("deduplicated receipt: %d", recorder.Code)
		}
	}

	if calls != 2 {
		t.Fatalf("same email has two events, one retried: got %d calls", calls)
	}
}

func TestNewRejectsInvalidSigningSecrets(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", "re_api_key", "whsec_", "whsec_!invalid", "c2lnbmluZy1zZWNyZXQ="} {
		_, err := resendwebhook.New(input)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Fatalf("invalid secret accepted: %q; %v", input, err)
		}
	}
}

func TestTamperedMessageIDRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	request := signedRequest(delivered, "msg_original", time.Now().Unix())
	request.Header.Set("svix-id", "msg_tampered")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("tampered ID receipt: %d", recorder.Code)
	}
}

func TestDuplicateAuthenticationHeaderRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	request := signedRequest(delivered, "msg_original", time.Now().Unix())
	request.Header.Add("svix-id", "msg_second")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate ID receipt: %d", recorder.Code)
	}
}

func TestExtremeTimestampRejected(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))

	for _, timestamp := range []int64{-9223372036854775808, 9223372036854775807} {
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, signedRequest(delivered, "msg_original", timestamp))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("extreme timestamp receipt: %d", recorder.Code)
		}
	}
}

func TestMissingEventKeyUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(`{"data":{}}`, "msg_original", time.Now().Unix()))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key receipt: %d", recorder.Code)
	}
}

func TestInvalidEventTimeUnavailable(t *testing.T) {
	t.Parallel()

	handler := newHandler(t, resendwebhook.EmailDelivered, rejectAccept(t))
	body := strings.Replace(delivered, "2026-02-22T23:41:12.126Z", "not-a-timestamp", 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_original", time.Now().Unix()))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid event time receipt: %d", recorder.Code)
	}
}

func TestComplainedUsesTypedEmailMetadata(t *testing.T) {
	t.Parallel()

	var received string
	handler := newHandler(t, resendwebhook.EmailComplained, func(_ context.Context, delivery webhook.Delivery[resendwebhook.Email]) error {
		received = delivery.Payload.EmailID

		return nil
	})
	body := strings.Replace(delivered, "email.delivered", "email.complained", 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_complaint", time.Now().Unix()))

	if recorder.Code != http.StatusOK || received != "email_123" {
		t.Fatalf("complaint receipt: %d; %q", recorder.Code, received)
	}
}

func TestSentMapsToApplicationPayload(t *testing.T) {
	t.Parallel()

	source, err := resendwebhook.New(secret)

	if err != nil {
		t.Fatal(err)
	}

	mapped := resendwebhook.EmailSent.Map(func(email resendwebhook.Email) (string, error) {
		return email.EmailID, nil
	})
	var received string
	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
		receiver.Handle(mapped, func(_ context.Context, delivery webhook.Delivery[string]) error {
			received = delivery.Payload

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	body := strings.Replace(delivered, "email.delivered", "email.sent", 1)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, signedRequest(body, "msg_sent", time.Now().Unix()))

	if recorder.Code != http.StatusOK || received != "email_123" {
		t.Fatalf("mapped sent receipt: %d; %q", recorder.Code, received)
	}
}
