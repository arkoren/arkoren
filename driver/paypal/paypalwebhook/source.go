package paypalwebhook

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/http/webhook"
	"arkoren.dev/driver/paypal/internal/paypal"
)

// Envelope retains the native authenticated event and resource. Resource owns
// its decoded bytes; callers must not mutate it while sharing an envelope.
// Native Type remains the webhook definition's routing key.
// Example: _ = paypalwebhook.Envelope{ID: "WH-ABC", Type: "PAYMENT.CAPTURE.COMPLETED"}
type Envelope struct {
	// ID is the stable event ID, not the retry-specific transmission ID.
	// Example: _ = paypalwebhook.Envelope{ID: "WH-ABC"}
	ID string `json:"id"`
	// Type is the original provider event_type, including unknown future keys.
	// Example: _ = paypalwebhook.Envelope{Type: "PAYMENT.CAPTURE.COMPLETED"}
	Type string `json:"event_type"`
	// CreatedAt is the provider event instant; zero means not supplied.
	// Example: _ = paypalwebhook.Envelope{CreatedAt: time.Unix(1700000000, 0)}
	CreatedAt time.Time `json:"create_time"`
	// Resource is the authenticated native JSON, converted only for bound events.
	// Example: _ = paypalwebhook.Envelope{Resource: jsontext.Value(`{"id":"ABC"}`)}
	Resource jsontext.Value `json:"resource"`
}

// Error exposes safe native diagnostics while preserving underlying causes.
// Error text does not contain credentials, response bodies or certificate URLs.
//
// Example:
//
//	var native *paypalwebhook.Error
//	if errors.As(err, &native) {
//		_ = native.DebugID
//	}
type Error = paypal.Error

// New creates a source without network I/O. All three credentials are required;
// webhookID is the configured PayPal webhook registration identity. Callbacks
// run directly, once, synchronously in order before validation. Nil entries
// panic; empty/nil expanded slices omit edits. Sandbox is the default. Each
// source has an independent token cache; no background goroutine refreshes it.
// Example:
//
//	source, err := paypalwebhook.New("client-id", "secret", "webhook-id")
//	if err != nil {
//		return err
//	}
//	_ = source
func New(clientID, clientSecret, webhookID string, configure ...BuilderFunc) (webhook.Source[Envelope], error) {
	listener := &Builder{}

	for _, configure := range configure {
		configure(listener)
	}

	if strings.TrimSpace(webhookID) == "" {
		return webhook.Source[Envelope]{}, errors.New("paypal webhook ID is required")
	}

	client, err := paypal.New(clientID, clientSecret, listener.settings)

	if err != nil {
		return webhook.Source[Envelope]{}, err
	}

	return webhook.NewSource(func(request *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		return verify(request, body, client, webhookID)
	}), nil
}

// verify sends signature metadata and the byte-exact original event only to
// PayPal. No event routing or resource interpretation precedes authentication.
func verify(
	request *http.Request, body []byte, client *paypal.Client, webhookID string,
) (webhook.Delivery[Envelope], error) {
	payload, err := verificationBody(request.Header, body, webhookID)

	if err != nil {
		return webhook.Delivery[Envelope]{}, err
	}

	// result distinguishes a bad signature from an unavailable verifier.
	var result struct {
		// Status must be SUCCESS; FAILURE is a confirmed rejected signature.
		Status string `json:"verification_status"`
	}
	err = client.Verify(request.Context(), payload, &result)

	if err != nil {
		return webhook.Delivery[Envelope]{}, err
	}

	if result.Status == "FAILURE" {
		return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: paypal signature verification failed", webhook.ErrRejected)
	}

	if result.Status != "SUCCESS" {
		return webhook.Delivery[Envelope]{}, errors.New("paypal verification returned an unknown status")
	}

	var envelope Envelope

	if err := json.Unmarshal(body, &envelope); err != nil {
		return webhook.Delivery[Envelope]{}, paypal.NewError(err)
	}

	if envelope.ID == "" || envelope.Type == "" {
		return webhook.Delivery[Envelope]{}, errors.New("paypal authenticated event lacks identity")
	}

	return webhook.Delivery[Envelope]{ID: envelope.ID, Type: envelope.Type, Payload: envelope}, nil
}

// verificationBody JSON-encodes untrusted headers, then appends the original
// body without jsontext.Value reencoding/compaction. All five signing headers
// are required. CERT-URL is data, not an HTTP destination. Invalid JSON or absent
// metadata is rejected before OAuth or verification I/O.
func verificationBody(header http.Header, body []byte, webhookID string) ([]byte, error) {
	if !jsontext.Value(body).IsValid() {
		return nil, fmt.Errorf("%w: invalid paypal event JSON", webhook.ErrRejected)
	}

	// fields uses provider field names rather than Go HTTP header casing.
	fields := map[string]string{
		"auth_algo": header.Get("PAYPAL-AUTH-ALGO"), "cert_url": header.Get("PAYPAL-CERT-URL"),
		"transmission_id": header.Get("PAYPAL-TRANSMISSION-ID"), "transmission_sig": header.Get("PAYPAL-TRANSMISSION-SIG"),
		"transmission_time": header.Get("PAYPAL-TRANSMISSION-TIME"),
	}

	for _, value := range fields {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%w: missing paypal signature header", webhook.ErrRejected)
		}
	}

	fields["webhook_id"] = webhookID

	prefix, err := json.Marshal(fields)

	if err != nil {
		return nil, paypal.NewError(err)
	}

	payload := append(prefix[:len(prefix)-1], []byte(`,"webhook_event":`)...)
	payload = append(payload, body...)

	return append(payload, '}'), nil
}
