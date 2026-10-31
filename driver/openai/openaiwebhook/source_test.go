package openaiwebhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/driver/openai/openaiwebhook"
)

// signingKey is a deterministic 32-byte test key, never an endpoint credential.
const signingKey = "0123456789abcdef0123456789abcdef"

// secret constructs the SDK's canonical prefixed secret from the fixture key.
func secret() string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(signingKey))
}

// eventBody creates a documented thin response envelope. Header and body IDs
// intentionally differ so identity assertions detect accidental substitution.
func eventBody(kind string) string {
	return fmt.Sprintf(`{"id":"evt_native","type":%q,"created_at":1719168000,"data":{"id":"resp_native"}}`, kind)
}

// signedRequest signs exact body bytes using Standard Webhooks' HMAC scheme.
// The timestamp is caller-supplied to exercise freshness without real sleeps.
func signedRequest(body string, timestamp time.Time) *http.Request {
	stamp := strconv.FormatInt(timestamp.Unix(), 10)
	digest := hmac.New(sha256.New, []byte(signingKey))
	digest.Write([]byte("wh_native." + stamp + "." + body))
	request := httptest.NewRequest(http.MethodPost, "/openai", strings.NewReader(body))
	request.Header.Set("webhook-id", "wh_native")
	request.Header.Set("webhook-timestamp", stamp)
	request.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(digest.Sum(nil)))

	return request
}

// newHandler binds one lifecycle event through the real HTTP receiver; all
// authentication, decoding and acknowledgement behavior runs through core.
func newHandler(t *testing.T, definition webhook.Event[openaiwebhook.Envelope, openaiwebhook.Response], accept webhook.AcceptFunc[openaiwebhook.Response]) router.Handler {
	t.Helper()

	source, err := openaiwebhook.New(secret())

	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := webhook.Handler(source, func(receiver *webhook.Builder[openaiwebhook.Envelope]) {
		receiver.Handle(definition, accept)
	})

	if err != nil {
		t.Fatal(err)
	}

	return endpoint
}

// acceptResponse acknowledges decoded notifications without side effects.
func acceptResponse(_ context.Context, _ webhook.Delivery[openaiwebhook.Response]) error {
	return nil
}

func TestCompletedCarriesResponseReferenceAndSignedIdentity(t *testing.T) {
	t.Parallel()

	var received webhook.Delivery[openaiwebhook.Response]
	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, func(_ context.Context, delivery webhook.Delivery[openaiwebhook.Response]) error {
		received = delivery

		return nil
	})
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(eventBody("response.completed"), time.Now()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d; %s", recorder.Code, recorder.Body.String())
	}

	if received.ID != "wh_native" || received.Type != "response.completed" {
		t.Fatalf("delivery identity: %+v", received)
	}

	if received.Payload != (openaiwebhook.Response{ID: "resp_native", CreatedAt: 1719168000}) {
		t.Fatalf("response notification: %+v", received.Payload)
	}
}

func TestRemainingResponseLifecycleDefinitions(t *testing.T) {
	t.Parallel()

	for _, definition := range []webhook.Event[openaiwebhook.Envelope, openaiwebhook.Response]{
		openaiwebhook.ResponseFailed, openaiwebhook.ResponseCanceled, openaiwebhook.ResponseIncomplete,
	} {
		called := false
		endpoint := newHandler(t, definition, func(_ context.Context, delivery webhook.Delivery[openaiwebhook.Response]) error {
			called = true

			if delivery.Payload.ID != "resp_native" {
				t.Fatalf("response: %+v", delivery)
			}

			return nil
		})
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, signedRequest(eventBody(definition.Key()), time.Now()))

		if recorder.Code != http.StatusOK || !called {
			t.Fatalf("%s receipt: %d; called: %t", definition.Key(), recorder.Code, called)
		}
	}

	if openaiwebhook.ResponseCanceled.Key() != "response.cancelled" {
		t.Fatal("canceled definition must preserve OpenAI's native spelling")
	}
}

func TestMissingSignatureRejectsBeforeParsing(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/openai", strings.NewReader("not json"))

	err := endpoint(recorder, request)

	if !errors.Is(err, webhook.ErrRejected) || !errors.Is(err, openaiwebhook.ErrInvalidSignature) {
		t.Fatalf("classification: %v", err)
	}

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	request := signedRequest(eventBody("response.completed"), time.Now())
	request.Body = http.NoBody
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestTamperedWebhookIDRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	request := signedRequest(eventBody("response.completed"), time.Now())
	request.Header.Set("webhook-id", "wh_forged")
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestMissingRequiredAuthenticationHeadersRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)

	for _, name := range []string{"webhook-id", "webhook-timestamp", "webhook-signature"} {
		request := signedRequest(eventBody("response.completed"), time.Now())
		request.Header.Del(name)
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("missing %s receipt: %d", name, recorder.Code)
		}
	}
}

func TestIncorrectSignatureRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	request := signedRequest(eventBody("response.completed"), time.Now())
	request.Header.Set("webhook-signature", "v1,"+base64.StdEncoding.EncodeToString(make([]byte, sha256.Size)))
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestExpiredSignatureRejected(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
		request := signedRequest(eventBody("response.completed"), time.Now())
		time.Sleep(301 * time.Second)
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("receipt: %d", recorder.Code)
		}
	})
}

func TestFutureSignatureRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(eventBody("response.completed"), time.Now().Add(301*time.Second)))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestMultipleSignaturesAllowRotation(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	request := signedRequest(eventBody("response.completed"), time.Now())
	request.Header.Set("webhook-signature", "v1,invalid "+request.Header.Get("webhook-signature"))
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestUnknownEventAcknowledgedWithoutDataDecoding(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, func(_ context.Context, _ webhook.Delivery[openaiwebhook.Response]) error {
		t.Fatal("unbound event reached response handler")

		return nil
	})
	body := `{"id":"evt_unknown","type":"future.event","data":"opaque"}`
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(body, time.Now()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestUnknownUnauthenticatedEventRejected(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	request := signedRequest(eventBody("future.event"), time.Now())
	request.Header.Del("webhook-signature")
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestMalformedAuthenticatedEnvelopesUnavailable(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)

	for _, body := range []string{
		`{`, `null`, `{"id":"evt_missing_type"}`, `{"type":"response.completed"}`,
		`{"id":"evt_native","type":"response.completed","object":"response"}`,
		`{"id":"evt_native","id":"evt_duplicate","type":"response.completed"}`,
	} {
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, signedRequest(body, time.Now()))

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("receipt for %s: %d", body, recorder.Code)
		}
	}
}

func TestMalformedBoundReferencesUnavailable(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)

	for _, data := range []string{`null`, `{}`, `{"id":12}`, `"opaque"`} {
		body := fmt.Sprintf(`{"id":"evt_native","type":"response.completed","created_at":1719168000,"data":%s}`, data)
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, signedRequest(body, time.Now()))

		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("receipt for %s: %d", data, recorder.Code)
		}
	}
}

func TestMissingBoundEventTimeUnavailable(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	body := `{"id":"evt_native","type":"response.completed","data":{"id":"resp_native"}}`
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(body, time.Now()))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}

func TestInvalidSecretsFailConstruction(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "plain-secret", "whsec_", "whsec_!", "whsec_YWJj", secret() + "\n"} {
		_, err := openaiwebhook.New(value)

		if !errors.Is(err, webhook.ErrInvalid) {
			t.Fatalf("configuration classification: %v", err)
		}
	}
}

func TestHandlerFailureUnavailable(t *testing.T) {
	t.Parallel()

	errAccept := errors.New("worker unavailable")
	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, func(_ context.Context, _ webhook.Delivery[openaiwebhook.Response]) error {
		return errAccept
	})
	recorder := httptest.NewRecorder()

	err := endpoint(recorder, signedRequest(eventBody("response.completed"), time.Now()))

	if !errors.Is(err, errAccept) || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("receipt: %d; error: %v", recorder.Code, err)
	}
}

func TestReplayWithinWindowUsesApplicationDeduplication(t *testing.T) {
	t.Parallel()

	source, err := openaiwebhook.New(secret())

	if err != nil {
		t.Fatal(err)
	}

	store := cache.New(memorycache.New())
	calls := 0
	accept := func(_ context.Context, _ webhook.Delivery[openaiwebhook.Response]) error {
		calls++

		return nil
	}

	endpoint, err := webhook.Handler(source, func(receiver *webhook.Builder[openaiwebhook.Envelope]) {
		receiver.Handle(openaiwebhook.ResponseCompleted, accept)
		receiver.Deduplicate(store, cache.NewNamespace[bool]("openai-project"))
	})

	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		recorder := httptest.NewRecorder()

		endpoint.ServeHTTP(recorder, signedRequest(eventBody("response.completed"), time.Now()))

		if recorder.Code != http.StatusOK {
			t.Fatalf("receipt: %d", recorder.Code)
		}
	}

	if calls != 1 {
		t.Fatalf("replayed webhook handled %d times", calls)
	}
}

func TestPresentObjectAccepted(t *testing.T) {
	t.Parallel()

	endpoint := newHandler(t, openaiwebhook.ResponseCompleted, acceptResponse)
	body := `{"object":"event","id":"evt_native","type":"response.completed","created_at":1719168000,"data":{"id":"resp_native"}}`
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(body, time.Now()))

	if recorder.Code != http.StatusOK {
		t.Fatalf("receipt: %d", recorder.Code)
	}
}
