package webpushpush

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"arkoren.dev/core/service/push"
)

const (
	// recordSize is the RFC 8030 minimum supported encrypted envelope size.
	recordSize = 4096
	// plaintextLimit leaves room for salt, key, GCM tag and final delimiter
	// within the sending library's single-record 4096-byte representation.
	plaintextLimit = 3993
	// maximumTTLSeconds avoids overflow in providers using signed 32-bit TTLs.
	maximumTTLSeconds = 1<<31 - 1
	// responseBodyLimit bounds discarded provider data regardless of status.
	responseBodyLimit = 4096
	// authorizationLifetime stays below RFC 8292's 24-hour expiration ceiling.
	authorizationLifetime = 12 * time.Hour
	// authorizationRefreshMargin leaves time for transit and clock skew while
	// keeping refresh far less frequent than Apple's once-per-hour limit.
	authorizationRefreshMargin = 5 * time.Minute
	// maximumAuthorizationOrigins caps memory without evicting live tokens,
	// which would allow hostile origins to force premature token refresh.
	maximumAuthorizationOrigins = 256
)

// Driver sends one encrypted request per Send and never retries a request.
// Its private credentials and settings are detached from construction inputs;
// concurrent calls share a synchronized per-origin authorization cache.
// Keep New's pointer: the mutex must not be copied. Pointer formatting emits a
// fixed redaction marker without traversing owned credentials or caller objects.
//
// Example:
//
//	driver, err := webpushpush.New(identity, "mailto:push@example.com")
//	if err != nil {
//		return err
//	}
//	_, err = driver.Send(ctx, push.NewMessage(subscription, "Ready"), push.SendConfig{})
//	return err
type Driver struct {
	// private is an independent base64url private scalar for library signing.
	private string
	// public is the matching uncompressed base64url application server key.
	public string
	// contact is normalized for the library, which adds mailto itself.
	contact string
	// origins is nil for curated defaults or a detached exact allowlist.
	origins []string
	// timeout bounds all work involving transport and response handling.
	timeout time.Duration
	// client is always an owned hardened client outside package tests.
	client httpDoer
	// mutex serializes authorization publication, not network operations.
	mutex sync.Mutex
	// authorizations stores only origin-scoped tokens, never endpoint paths.
	authorizations map[string]authorization
}

// authorization retains a library-signed RFC 8292 header until refresh time.
// No caller can observe or mutate the cached credential.
type authorization struct {
	// header is the complete vapid authorization, including public key.
	header string
	// expiresAt is the signed expiration, not a sliding cache TTL.
	expiresAt time.Time
}

// payload is the browser service worker wire contract. Optional empty fields
// are omitted; there is deliberately no provider-specific notification wrapper.
type payload struct {
	// Title is always present because core requires a notification title.
	Title string `json:"title"`
	// Body is omitted when empty, independent of data presence.
	Body string `json:"body,omitempty"`
	// Data contains application-owned string values, omitted when empty.
	Data map[string]string `json:"data,omitempty"`
}

// Send validates the message, endpoint policy, actual encoded size and TTL
// before dispatch. Only value push.Subscription recipients are supported.
// TTL is truncated to seconds and must not exceed 2147483647 seconds. Failures
// return a zero receipt. A 201 response returns an empty receipt ID even if
// optional headers are absent or cleanup fails. Transport errors conservatively
// wrap push.ErrOutcomeUnknown and preserve native/context error identity.
// Top-level diagnostics never contain endpoint URLs, keys or payload text.
//
// Example:
//
//	message := push.NewMessage(subscription, "Your export is ready")
//	_, err := driver.Send(ctx, message, push.SendConfig{TTL: time.Hour})
//	if errors.Is(err, push.ErrNotFound) {
//		invalidateSubmittedVersion()
//	}
//	return err
func (driver *Driver) Send(ctx context.Context, message push.Message, config push.SendConfig) (push.Receipt, error) {
	if err := push.CheckSend(ctx, message, config); err != nil {
		return push.Receipt{}, err
	}

	subscription, ok := message.To().(push.Subscription)

	if !ok {
		return push.Receipt{}, fmt.Errorf("web push requires a browser subscription: %w", push.ErrUnsupported)
	}

	endpoint, err := driver.checkEndpoint(subscription.Endpoint)

	if err != nil {
		return push.Receipt{}, err
	}

	// RFC 8292 audiences serialize origins without the default port. The
	// subscription's opaque path and query remain byte-for-byte intact.
	endpoint.Host = endpoint.Hostname()
	subscription.Endpoint = endpoint.String()

	encoded, err := encodeMessage(message, config)

	if err != nil {
		return push.Receipt{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, driver.timeout)
	defer cancel()

	expiresAt := time.Now().Add(authorizationLifetime)
	submission := &delivery{driver: driver, expiresAt: expiresAt}
	native := &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys:     webpush.Keys{P256dh: subscription.Keys.P256DH, Auth: subscription.Keys.Auth},
	}

	response, err := webpush.SendNotificationWithContext(ctx, encoded, native, &webpush.Options{
		AuthScheme: webpush.Vapid, HTTPClient: submission,
		Subscriber: driver.contact, VAPIDPrivateKey: driver.private, VAPIDPublicKey: driver.public,
		TTL: int(config.TTL / time.Second), RecordSize: recordSize, VapidExpiration: expiresAt,
	})

	if err != nil {
		class := push.ErrUnavailable

		if submission.dispatched {
			class = push.ErrOutcomeUnknown
		}

		return push.Receipt{}, &safeError{
			operation: "web push send failed",
			cause:     errors.Join(class, err),
		}
	}

	defer func() {
		// End request work before releasing the body; no more data is useful.
		cancel()
		// The authoritative response cannot be undone by cleanup errors.
		_ = response.Body.Close()
	}()

	return finishResponse(response)
}

// encodeMessage applies limits to the actual JSON bytes, not rune counts or
// individual fields. The 3993-byte budget leaves the RFC 8291 overhead and
// final-record delimiter within the 4096-byte minimum provider envelope.
func encodeMessage(message push.Message, config push.SendConfig) ([]byte, error) {
	if config.TTL/time.Second > maximumTTLSeconds {
		return nil, fmt.Errorf("web push TTL exceeds supported range: %w", push.ErrInvalidMessage)
	}

	encoded, err := json.Marshal(payload{Title: message.Title(), Body: message.Body(), Data: message.Data()})

	if err != nil {
		return nil, fmt.Errorf("web push JSON encoding failed: %w", push.ErrInvalidMessage)
	}

	if len(encoded) > plaintextLimit {
		return nil, fmt.Errorf("web push encoded payload exceeds %d bytes: %w", plaintextLimit, push.ErrInvalidMessage)
	}

	return encoded, nil
}

// finishResponse treats acceptance as authoritative before discarding body
// and cleanup failures. Response bodies are never interpreted or formatted;
// at most 4096 bytes are drained. Upgraded connections are never read because
// net/http transfers ownership and stops enforcing request cancellation. The
// caller owns canceling the request and closing the body after classification.
func finishResponse(response *http.Response) (push.Receipt, error) {
	// Body text is untrusted and unnecessary for portable classification.
	if response.StatusCode != http.StatusSwitchingProtocols {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, responseBodyLimit))
	}

	if response.StatusCode == http.StatusCreated {
		return push.Receipt{}, nil
	}

	return push.Receipt{}, &ProviderError{
		StatusCode: response.StatusCode,
		RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now()),
		class:      responseClass(response.StatusCode),
	}
}

// delivery intercepts the library-signed header immediately before dispatch,
// replacing it with the shared origin authorization to satisfy Apple's reuse
// restriction. Signing and payload encryption remain library responsibilities.
type delivery struct {
	// driver owns the shared cache and hardened transport boundary.
	driver *Driver
	// expiresAt bounds the library's default twelve-hour signed expiration.
	expiresAt time.Time
	// dispatched marks entry into HTTP I/O; failures before that point cannot
	// have been accepted. Each delivery is used by one synchronous Send only.
	dispatched bool
}

// Do installs the reusable credential, disables body replay and checks for
// cancellation before allowing the single request to leave the process.
// The signing library calls it synchronously once per Send. It mutates the
// prepared request's Authorization/GetBody fields, retains no response, and
// passes transport errors back intact. Its caller owns response-body cleanup.
//
// Example:
//
//	pending := &delivery{driver: driver, expiresAt: expiresAt}
//	options := &webpush.Options{HTTPClient: pending}
//	_ = options // The library invokes pending.Do after signing/encryption.
func (delivery *delivery) Do(message *http.Request) (*http.Response, error) {
	if err := message.Context().Err(); err != nil {
		return nil, err
	}

	header, err := delivery.driver.authorization(message, delivery.expiresAt)

	if err != nil {
		return nil, err
	}

	message.Header.Set("Authorization", header)
	message.GetBody = nil
	delivery.dispatched = true

	return delivery.driver.client.Do(message)
}

// authorization publishes at most one token per origin under concurrent
// sends. Expired entries are removed when a new origin needs capacity;
// unexpired credentials are never evicted and prematurely regenerated.
func (driver *Driver) authorization(request *http.Request, expiresAt time.Time) (string, error) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	now := time.Now()
	origin := "https://" + request.URL.Hostname()
	cached, ok := driver.authorizations[origin]

	if ok && now.Before(cached.expiresAt.Add(-authorizationRefreshMargin)) {
		return cached.header, nil
	}

	for origin, cached := range driver.authorizations {
		if !now.Before(cached.expiresAt) {
			delete(driver.authorizations, origin)
		}
	}

	if !ok && len(driver.authorizations) >= maximumAuthorizationOrigins {
		return "", fmt.Errorf("web push authorization origin capacity reached: %w", push.ErrUnavailable)
	}

	header := request.Header.Get("Authorization")
	driver.authorizations[origin] = authorization{header: header, expiresAt: expiresAt}

	return header, nil
}

// The compile-time check keeps the adapter aligned with the portable contract.
var _ push.Driver = (*Driver)(nil)
