package paypal

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/service/payment"
)

const (
	// Sandbox is the fixed testing origin; arbitrary endpoint overrides are absent.
	// Example: _ = paypal.Sandbox
	Sandbox = "https://api-m.sandbox.paypal.com"
	// Live is the fixed production origin, selected only explicitly.
	// Example: _ = paypal.Live
	Live = "https://api-m.paypal.com"
	// successLimit caps successful business responses at one MiB.
	successLimit = 1 << 20
	// smallLimit caps OAuth and error responses at 64 KiB.
	smallLimit = 64 << 10
	// defaultTimeout bounds a call even when the caller supplies no deadline.
	defaultTimeout = 30 * time.Second
)

// Settings selects the origin and borrowed HTTP transport. The client value is
// copied so redirect and timeout policy cannot mutate the caller's client.
// Example: _ = paypal.Settings{HTTPClient: http.DefaultClient}
type Settings struct {
	// Live opts into production instead of sandbox.
	// Example: _ = paypal.Settings{Live: true}
	Live bool
	// HTTPClient supplies transport, jar and timeout; nil uses standard defaults.
	// A nonpositive timeout becomes thirty seconds. Redirects are always disabled.
	// Example: _ = paypal.Settings{HTTPClient: &http.Client{Timeout: time.Second}}
	HTTPClient *http.Client
}

// Client owns credentials and a cancelable, serialized access-token cache.
// It is concurrency-safe; its borrowed transport is never closed.
// Example: client, err := paypal.New("id", "secret", paypal.Settings{}); _ = client; _ = err
type Client struct {
	// clientID identifies the merchant application; never include it in errors.
	clientID string
	// secret authenticates token requests and is never exposed by an accessor.
	secret string
	// origin is one of the two fixed PayPal API origins.
	origin string
	// http performs bounded requests without following redirects.
	http *http.Client
	// gate serializes token access without goroutines or uncancelable mutex waits.
	gate chan struct{}
	// token is protected by gate and reused until expiresAt.
	token string
	// expiresAt includes an early-refresh skew to avoid using expiring tokens.
	expiresAt time.Time
}

// Request describes one account API operation. Body accepts JSON values or
// preencoded []byte, needed to preserve webhook_event bytes during verification.
// Example: _ = paypal.Request{Method: http.MethodGet, Path: "/v2/checkout/orders/ABC"}
type Request struct {
	// Method is PayPal's HTTP verb. Call treats POST as a mutation; Verify
	// separately preserves the read-only semantics of signature verification.
	// Example: _ = paypal.Request{Method: http.MethodGet}
	Method string
	// Path is an internally constructed absolute API path, never a provider URL.
	// Example: _ = paypal.Request{Path: "/v2/checkout/orders/ABC"}
	Path string
	// Key sets PayPal-Request-Id only when this endpoint supports it.
	// Example: _ = paypal.Request{Key: "capture-123"}
	Key string
	// DeviceReference is optional SDK-collected risk data for order creation.
	// Only POST /v2/checkout/orders accepts it; it must be visible ASCII and
	// at most 255 bytes. It is never included in returned diagnostics.
	// Example: _ = paypal.Request{DeviceReference: "sdk-metadata-123"}
	DeviceReference string
	// Body is JSON data; nil sends no body. []byte is sent without reencoding.
	// Example: _ = paypal.Request{Body: map[string]string{"reason": "Customer request"}}
	Body any
}

// New validates credentials and snapshots settings without I/O. Nil transport
// settings use a thirty-second client; callbacks belong to the public packages.
// Example: client, err := paypal.New("id", "secret", paypal.Settings{}); _ = client; _ = err
func New(clientID, secret string, settings Settings) (*Client, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("paypal client ID and client secret are required")
	}

	client := &http.Client{Timeout: defaultTimeout}

	if settings.HTTPClient != nil {
		*client = *settings.HTTPClient
	}

	if client.Timeout <= 0 {
		client.Timeout = defaultTimeout
	}

	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	origin := Sandbox

	if settings.Live {
		origin = Live
	}

	return &Client{clientID: clientID, secret: secret, origin: origin, http: client, gate: make(chan struct{}, 1)}, nil
}

// HTTPClient exposes the actual client used for requests, not a fictitious SDK.
// Do not mutate it concurrently with use or weaken its redirect policy.
// Example: _ = client.HTTPClient().Timeout
func (client *Client) HTTPClient() *http.Client {
	return client.http
}

// Call sends one request with no automatic retries or 401 replay. Successful
// mutations with unreadable, oversized or malformed bodies are outcome-unknown.
// Malformed successful GET bodies wrap payment.ErrUnavailable and retain the
// decoder cause; they never imply an uncertain money-moving outcome.
// A nil output accepts only an empty successful response. Response-body Close
// errors are ignored after the bounded read: they cannot change an observed
// provider result and retrying a mutation would risk a second effect.
// Example: err := client.Call(ctx, paypal.Request{Method: "GET", Path: "/v2/payments/captures/ABC"}, &result)
func (client *Client) Call(ctx context.Context, operation Request, output any) error {
	request, err := client.request(ctx, operation)

	if err != nil {
		return err
	}

	data, err := client.exchange(request, successLimit)

	if err != nil {
		return outcome(operation.Method, err)
	}

	err = decodeBody(data, output)

	if err != nil && operation.Method == http.MethodGet {
		return fmt.Errorf("%w: %w", payment.ErrUnavailable, err)
	}

	return outcome(operation.Method, err)
}

// Verify sends a remote signature-verification observation. Although PayPal
// requires POST, this operation cannot create a money-moving outcome, so its
// transport/server/decoding errors never gain payment.ErrOutcomeUnknown.
// body is already encoded to preserve the exact original webhook_event bytes.
// Example: err := client.Verify(ctx, verificationBody, &result); _ = err
func (client *Client) Verify(ctx context.Context, body []byte, output any) error {
	request, err := client.request(ctx, Request{Method: http.MethodPost,
		Path: "/v1/notifications/verify-webhook-signature", Body: body})

	if err != nil {
		return err
	}

	data, err := client.exchange(request, successLimit)

	if err != nil {
		return err
	}

	return decodeBody(data, output)
}

// request prepares a fixed-origin authenticated JSON request without dispatching
// the business operation. OAuth failures and pre-dispatch cancellation therefore
// stay distinct from an unknown mutation outcome at either calling boundary.
func (client *Client) request(ctx context.Context, operation Request) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := operation.checkDeviceReference(); err != nil {
		return nil, err
	}

	body, err := encodeBody(operation.Body)

	if err != nil {
		return nil, err
	}

	token, err := client.authorization(ctx)

	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, operation.Method, client.origin+operation.Path, bytes.NewReader(body))

	if err != nil {
		return nil, NewError(err)
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Prefer", "return=representation")

	if operation.Key != "" {
		request.Header.Set("PayPal-Request-Id", operation.Key)
	}

	if operation.DeviceReference != "" {
		request.Header.Set("PayPal-Client-Metadata-Id", operation.DeviceReference)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return request, nil
}

// checkDeviceReference prevents header injection and accidental forwarding to
// another endpoint before authentication. The validation error names only the
// field, never the SDK risk-data value or a constructed HTTP request.
func (request Request) checkDeviceReference() error {
	if request.DeviceReference == "" {
		return nil
	}

	if request.Method != http.MethodPost || request.Path != "/v2/checkout/orders" {
		return fmt.Errorf("%w: device_reference endpoint", payment.ErrInvalidRequest)
	}

	if len(request.DeviceReference) > 255 {
		return fmt.Errorf("%w: device_reference length", payment.ErrInvalidRequest)
	}

	if strings.ContainsFunc(request.DeviceReference, func(value rune) bool {
		return value < 0x21 || value > 0x7e
	}) {
		return fmt.Errorf("%w: device_reference syntax", payment.ErrInvalidRequest)
	}

	return nil
}

// encodeBody preserves preencoded webhook verification bytes. Ordinary request
// structs are encoded exactly once; encoder diagnostics remain private causes.
func encodeBody(body any) ([]byte, error) {
	if data, ok := body.([]byte); ok {
		return data, nil
	}

	if body == nil {
		return nil, nil
	}

	data, err := json.Marshal(body)

	if err != nil {
		return nil, NewError(err)
	}

	return data, nil
}

// decodeBody validates an entire bounded JSON document. A no-content endpoint
// must actually return an empty body; whitespace-only JSON is not a document.
func decodeBody(data []byte, output any) error {
	if output == nil {
		if len(bytes.TrimSpace(data)) == 0 {
			return nil
		}

		return NewError(errors.New("unexpected response body"))
	}

	if err := json.Unmarshal(data, output); err != nil {
		return NewError(err)
	}

	return nil
}

// exchange performs the only HTTP dispatch. The successful bound is supplied
// by the endpoint; every non-2xx response uses the smaller error bound.
// A canceled request retains both the transport error and context cause so
// callers can reconcile an ambiguous mutation without losing cancellation identity.
func (client *Client) exchange(request *http.Request, limit int64) ([]byte, error) {
	response, err := client.http.Do(request)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", payment.ErrUnavailable,
			NewError(errors.Join(err, context.Cause(request.Context()))))
	}

	defer func() {
		// A completed bounded read is authoritative; Close cannot undo it.
		_ = response.Body.Close()
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, responseError(response)
	}

	data, err := readBounded(response.Body, limit)

	if err != nil {
		return nil, fmt.Errorf("%w: %w", payment.ErrUnavailable,
			NewError(errors.Join(err, context.Cause(request.Context()))))
	}

	return data, nil
}

// readBounded uses limit+1 to distinguish a complete response from truncation.
// It does not drain an untrusted oversized response before closing it.
func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))

	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, errors.New("paypal response exceeds limit")
	}

	return data, nil
}

// authorization serializes refresh with a context-aware gate. Concurrent cold
// callers share a successful token; no refresh goroutine survives cancellation.
func (client *Client) authorization(ctx context.Context) (string, error) {
	select {
	case client.gate <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	defer func() { <-client.gate }()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	if time.Now().Before(client.expiresAt) {
		return client.token, nil
	}

	return client.refresh(ctx)
}

// refresh requests client-credentials access with no background work. Tokens
// lacking a positive lifetime or bearer type are not cached or dispatched.
// Malformed JSON and invalid token payloads are unavailable before any business
// mutation is sent; both retain safe wrappers around their diagnostic causes.
func (client *Client) refresh(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.origin+"/v1/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))

	if err != nil {
		return "", NewError(err)
	}

	request.SetBasicAuth(client.clientID, client.secret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	data, err := client.exchange(request, smallLimit)

	if err != nil {
		return "", err
	}

	// credentials is an OAuth response, never exposed by the driver.
	var credentials struct {
		// Token is retained only behind the serialized cache gate.
		Token string `json:"access_token"`
		// Type must be bearer before the token is used on a business request.
		Type string `json:"token_type"`
		// Expires is a positive provider lifetime in seconds, bounded on conversion.
		Expires int64 `json:"expires_in"`
	}

	if err := json.Unmarshal(data, &credentials); err != nil {
		return "", fmt.Errorf("%w: %w", payment.ErrUnavailable, NewError(err))
	}

	if credentials.Token == "" || !strings.EqualFold(credentials.Type, "Bearer") || credentials.Expires <= 0 {
		return "", fmt.Errorf("%w: %w", payment.ErrUnavailable, NewError(errors.New("invalid OAuth response")))
	}

	// lifetime is bounded before duration conversion to avoid integer overflow.
	lifetime := time.Duration(min(credentials.Expires, int64(24*60*60))) * time.Second
	client.token = credentials.Token
	client.expiresAt = time.Now().Add(lifetime - min(30*time.Second, lifetime/10))

	return client.token, nil
}

// outcome marks only ambiguous dispatched mutations. Confirmed 4xx responses
// remain their original category, and read failures never imply money moved.
func outcome(method string, err error) error {
	if err == nil || method == http.MethodGet {
		return err
	}

	var native *Error

	if errors.As(err, &native) &&
		native.Status >= http.StatusMultipleChoices && native.Status < http.StatusInternalServerError {
		return err
	}

	return fmt.Errorf("%w: %w", payment.ErrOutcomeUnknown, err)
}
