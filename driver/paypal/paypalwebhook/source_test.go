package paypalwebhook_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/payment"
	"arkoren.dev/driver/paypal/paypalwebhook"
)

// transportFunc substitutes the external PayPal HTTP transport, not source
// verification or the actual webhook handler's authentication/routing policy.
type transportFunc func(*http.Request) (*http.Response, error)

// RoundTrip invokes the deterministic external protocol implementation.
func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// response supplies real response-body ownership to the production HTTP client.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// sourceWith constructs an independent credentialed source and supplies OAuth
// while allowing each test to inspect verification requests at the fixed host.
func sourceWith(t *testing.T, verify transportFunc) webhook.Source[paypalwebhook.Envelope] {
	t.Helper()

	transport := transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api-m.sandbox.paypal.com" {
			return nil, errors.New("unexpected network destination")
		}

		if request.URL.Path == "/v1/oauth2/token" {
			return response(200, `{"access_token":"token","token_type":"Bearer","expires_in":3600}`), nil
		}

		return verify(request)
	})

	source, err := paypalwebhook.New("client", "secret", "HOOK-ID", func(listener *paypalwebhook.Builder) {
		listener.HTTPClient(&http.Client{Transport: transport})
	})

	if err != nil {
		t.Fatal(err)
	}

	return source
}

// captureHandler binds one known event so unknown-key tests exercise actual
// unbound routing, rather than an invalid handler with no registered events.
func captureHandler(t *testing.T, source webhook.Source[paypalwebhook.Envelope]) router.Handler {
	t.Helper()

	endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
		listener.Handle(paypalwebhook.PaymentCaptureCompleted, func(
			_ context.Context, _ webhook.Delivery[payment.Observation],
		) error {
			return errors.New("unexpected capture acceptance")
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	return endpoint
}

// signedRequest creates required metadata. The certificate URL is deliberately
// foreign: authentication forwards it as data and must never fetch it locally.
func signedRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://shop.example/hooks/paypal", strings.NewReader(body))
	request.Header.Set("PAYPAL-AUTH-ALGO", "SHA256withRSA")
	request.Header.Set("PAYPAL-CERT-URL", "https://untrusted.example/cert")
	request.Header.Set("PAYPAL-TRANSMISSION-ID", "transmission-attempt-1")
	request.Header.Set("PAYPAL-TRANSMISSION-SIG", "signed-value")
	request.Header.Set("PAYPAL-TRANSMISSION-TIME", "2026-10-24T03:04:05Z")

	return request
}

// TestSourceConstructionDoesNoIO requires only local configuration. It does not
// construct a payment driver or authenticate an account until delivery receipt.
func TestSourceConstructionDoesNoIO(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := paypalwebhook.New("client", "secret", "HOOK-ID", func(listener *paypalwebhook.Builder) {
		listener.HTTPClient(&http.Client{Transport: transportFunc(func(_ *http.Request) (*http.Response, error) {
			calls++

			return nil, errors.New("constructor must not use transport")
		})})
	})

	if err != nil || calls != 0 {
		t.Fatal("source construction performed I/O")
	}
}

// TestVerificationPreservesExactEventBytes checks the actual verification wire
// document, metadata and byte-exact event body rather than a raw-JSON DTO.
func TestVerificationPreservesExactEventBytes(t *testing.T) {
	t.Parallel()

	body := ` {
  "id":"WH-STABLE", "event_type":"PAYMENT.CAPTURE.COMPLETED",
"create_time":"2026-10-24T03:04:05Z", "resource": {"id":"CAPTURE", "status":"COMPLETED",
"amount":{"currency_code":"USD","value":"12.34"}}
 } `
	var wire []byte
	verifications := 0
	source := sourceWith(t, func(request *http.Request) (*http.Response, error) {
		verifications++

		if request.URL.Path != "/v1/notifications/verify-webhook-signature" {
			return nil, errors.New("unexpected verification path")
		}

		data, err := io.ReadAll(request.Body)
		wire = data

		return response(200, `{"verification_status":"SUCCESS"}`), err
	})

	endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
		listener.Handle(paypalwebhook.PaymentCaptureCompleted, func(
			_ context.Context, _ webhook.Delivery[payment.Observation],
		) error {
			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(body))

	if recorder.Code != http.StatusOK || verifications != 1 {
		t.Fatal("verified delivery was not accepted exactly once")
	}

	marker := []byte(`"webhook_event":`)
	index := bytes.Index(wire, marker)

	if index < 0 || !bytes.Equal(wire[index+len(marker):len(wire)-1], []byte(body)) {
		t.Fatal("verification compacted or otherwise changed original body bytes")
	}
}

// TestVerificationIncludesSigningMetadata checks every required signature field
// and the registered webhook ID independently of event routing and conversion.
func TestVerificationIncludesSigningMetadata(t *testing.T) {
	t.Parallel()

	var wire []byte
	source := sourceWith(t, func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		wire = data

		return response(200, `{"verification_status":"SUCCESS"}`), err
	})

	endpoint := captureHandler(t, source)
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(`{"id":"WH-UNKNOWN","event_type":"UNKNOWN.EVENT"}`))

	if recorder.Code != http.StatusOK {
		t.Fatal("verification did not succeed")
	}

	// metadata is the native request's signing subset; webhook_event is
	// tested separately because its original bytes must not be reencoded.
	type metadata struct {
		WebhookID      string `json:"webhook_id"`
		TransmissionID string `json:"transmission_id"`
		Signature      string `json:"transmission_sig"`
		Time           string `json:"transmission_time"`
		Algorithm      string `json:"auth_algo"`
		CertificateURL string `json:"cert_url"`
	}

	var actual metadata

	if err := json.Unmarshal(wire, &actual); err != nil {
		t.Fatal(err)
	}

	expected := metadata{WebhookID: "HOOK-ID", TransmissionID: "transmission-attempt-1",
		Signature: "signed-value", Time: "2026-10-24T03:04:05Z", Algorithm: "SHA256withRSA",
		CertificateURL: "https://untrusted.example/cert"}

	if actual != expected {
		t.Fatal("verification signing metadata was omitted or changed")
	}
}

// TestBadSignatureRejectsBeforeRouting proves a bound resource is not decoded
// or accepted before remote authentication returns SUCCESS.
func TestBadSignatureRejectsBeforeRouting(t *testing.T) {
	t.Parallel()

	source := sourceWith(t, func(_ *http.Request) (*http.Response, error) {
		return response(200, `{"verification_status":"FAILURE"}`), nil
	})

	accepted := false
	endpoint, err := webhook.Handler(source, func(listener *webhook.Builder[paypalwebhook.Envelope]) {
		listener.Handle(paypalwebhook.PaymentCaptureCompleted, func(
			_ context.Context, _ webhook.Delivery[payment.Observation],
		) error {
			accepted = true

			return nil
		})
	})

	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()

	err = endpoint(recorder, signedRequest(`{"id":"WH-1","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":null}`))

	if !errors.Is(err, webhook.ErrRejected) || recorder.Code != 400 || accepted {
		t.Fatal("bad signature was not rejected before resource routing")
	}
}

// TestVerifierOutageIsUnavailableNotBadSignature distinguishes external API
// failure from authenticated rejection so the handler can answer 503.
func TestVerifierOutageIsUnavailableNotBadSignature(t *testing.T) {
	t.Parallel()

	source := sourceWith(t, func(_ *http.Request) (*http.Response, error) {
		return response(503, `{"name":"SERVICE_UNAVAILABLE"}`), nil
	})

	endpoint := captureHandler(t, source)
	recorder := httptest.NewRecorder()

	err := endpoint(recorder, signedRequest(`{"id":"WH-1","event_type":"UNKNOWN.EVENT"}`))

	if !errors.Is(err, webhook.ErrUnavailable) || errors.Is(err, webhook.ErrRejected) || recorder.Code != 503 {
		t.Fatal("verification outage was mislabeled invalid signature")
	}

	if errors.Is(err, payment.ErrOutcomeUnknown) {
		t.Fatal("read-only signature verification was treated as money movement")
	}
}

// TestUnknownUnboundEventAuthenticatesThenAcknowledges verifies the actual
// handler's unbound routing path without making up resource semantics.
func TestUnknownUnboundEventAuthenticatesThenAcknowledges(t *testing.T) {
	t.Parallel()

	verifications := 0
	source := sourceWith(t, func(_ *http.Request) (*http.Response, error) {
		verifications++

		return response(200, `{"verification_status":"SUCCESS"}`), nil
	})

	endpoint := captureHandler(t, source)
	recorder := httptest.NewRecorder()

	endpoint.ServeHTTP(recorder, signedRequest(`{"id":"WH-UNKNOWN","event_type":"UNKNOWN.EVENT","resource":"opaque"}`))

	if recorder.Code != 200 || verifications != 1 {
		t.Fatal("unknown unbound event was not authenticated and acknowledged")
	}
}

// TestMissingSignatureMetadataRejectsWithoutIO prevents incomplete metadata
// from triggering OAuth, verification or any certificate URL fetch.
func TestMissingSignatureMetadataRejectsWithoutIO(t *testing.T) {
	t.Parallel()

	calls := 0
	source, err := paypalwebhook.New("client", "secret", "HOOK-ID", func(listener *paypalwebhook.Builder) {
		listener.HTTPClient(&http.Client{Transport: transportFunc(func(_ *http.Request) (*http.Response, error) {
			calls++

			return nil, errors.New("missing metadata must not use transport")
		})})
	})

	if err != nil {
		t.Fatal(err)
	}

	endpoint := captureHandler(t, source)
	request := signedRequest(`{"id":"WH-1","event_type":"UNKNOWN.EVENT"}`)
	request.Header.Del("PAYPAL-TRANSMISSION-SIG")
	recorder := httptest.NewRecorder()

	err = endpoint(recorder, request)

	if !errors.Is(err, webhook.ErrRejected) || calls != 0 {
		t.Fatal("missing signature metadata performed I/O or was accepted")
	}
}
