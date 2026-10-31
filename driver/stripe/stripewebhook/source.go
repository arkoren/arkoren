package stripewebhook

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	stripehook "github.com/stripe/stripe-go/v87/webhook"

	"arkoren.dev/core/http/webhook"
)

// Envelope is an authenticated Stripe snapshot event. Data.Object is retained as
// raw JSON until a bound event validates its API version and resource schema.
// No resource is fetched from Stripe. Treat the snapshot as sensitive data.
// CreatedAt retains Stripe's Unix-seconds created field; zero means absent.
//
// Example:
//
//	accept := func(ctx context.Context, delivery webhook.Delivery[stripewebhook.Envelope]) error {
//		_ = delivery.Payload.APIVersion
//		return nil
//	}
//	_ = accept
type Envelope struct {
	// ID is the stable Stripe event identity, not a delivery attempt identity.
	// Example: _ = stripewebhook.Envelope{ID: "evt_example"}
	ID string `json:"id"`
	// Type is the native event key used to select the application's binding.
	// Example: _ = stripewebhook.Envelope{Type: "payment_intent.succeeded"}
	Type string `json:"type"`
	// APIVersion identifies the snapshot schema; bound decoders require an exact match.
	// Example: _ = stripewebhook.Envelope{APIVersion: stripe.APIVersion}
	APIVersion string `json:"api_version"`
	// CreatedAt is provider event time in Unix seconds, not local receipt time.
	// Zero means absent. Its wire member remains Stripe's native created name.
	// Example: _ = stripewebhook.Envelope{CreatedAt: 1700000000}
	CreatedAt int64 `json:"created"`
	// Data contains the raw resource snapshot, owned by this envelope.
	// Example: _ = stripewebhook.Envelope{}.Data.Object
	Data struct {
		// Object is the JSON resource, decoded only for explicitly bound events.
		// Example: _ = stripewebhook.Envelope{}.Data.Object
		Object jsontext.Value `json:"object"`
	} `json:"data"`
	// Object must be "event"; thin notification envelopes are not supported.
	// Example: _ = stripewebhook.Envelope{Object: "event"}
	Object string `json:"object"`
}

// Builder configures signature freshness without credentials or application
// delivery policy. A copy is captured when New returns.
//
// Example:
//
//	_, err := stripewebhook.New("whsec_example", func(signature *stripewebhook.Builder) {
//		signature.Tolerance(time.Minute)
//	})
//	if err != nil { return err }
type Builder struct {
	// tolerance bounds signature age; zero restores Stripe's five-minute default.
	tolerance time.Duration
}

// BuilderFunc edits one draft synchronously in argument order after New validates
// the required signing secret. Invalid secrets skip every callback. With a valid
// secret, nil entries and application panics propagate; omitting callbacks keeps
// the five-minute signature tolerance.
//
// Example:
//
//	var policy stripewebhook.BuilderFunc = func(signature *stripewebhook.Builder) {
//		signature.Tolerance(time.Minute)
//	}
//	_, err := stripewebhook.New("whsec_example", policy)
//	if err != nil { return err }
type BuilderFunc = func(*Builder)

// ErrInvalidSignature marks a missing, invalid, or stale Stripe signature.
// Source errors also wrap webhook.ErrRejected; no signature or secret is echoed.
// The SDK's constant signature-error sentinel is retained for errors.Is.
//
// Example:
//
//	if errors.Is(err, stripewebhook.ErrInvalidSignature) {
//		// Reject the unauthenticated request; do not retry local processing.
//	}
var ErrInvalidSignature = errors.New("stripe webhook: invalid signature")

// errEnvelope marks malformed authenticated envelopes without exposing JSON.
var errEnvelope = errors.New("stripe webhook: invalid snapshot envelope")

// defaultTolerance matches Stripe's default five-minute freshness window.
const defaultTolerance = 300 * time.Second

// Tolerance sets the maximum signature age. Zero restores five minutes and a
// negative value makes New fail with webhook.ErrInvalid. It does not alter time.
//
// Example:
//
//	_, err := stripewebhook.New("whsec_example", func(signature *stripewebhook.Builder) {
//		signature.Tolerance(2 * time.Minute)
//	})
//	if err != nil { return err }
func (signature *Builder) Tolerance(tolerance time.Duration) *Builder {
	signature.tolerance = tolerance

	return signature
}

// New validates the endpoint signing secret and returns an independent source.
// It performs no network I/O and requires no payment API key. Verification uses
// the exact received body before json/v2 snapshot parsing. Application bindings,
// deduplication, body limits, queues, and routing belong to core webhook.Handler.
// Invalid secrets return before callbacks. With a valid secret, callbacks run
// directly once, synchronously in order on one draft; nil entries and panics
// propagate. Tolerance is validated afterwards and captured as an independent
// scalar value, so retaining the draft cannot change a published source.
//
// Example:
//
//	source, err := stripewebhook.New("whsec_example")
//	if err != nil { return err }
//	_ = source.Err()
func New(secret string, configure ...BuilderFunc) (webhook.Source[Envelope], error) {
	if !strings.HasPrefix(secret, "whsec_") || len(secret) <= len("whsec_") {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: stripe signing secret", webhook.ErrInvalid)
	}

	if strings.ContainsFunc(secret, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: stripe signing secret", webhook.ErrInvalid)
	}

	signature := &Builder{}

	for _, apply := range configure {
		apply(signature)
	}

	if signature.tolerance < 0 {
		return webhook.Source[Envelope]{}, fmt.Errorf("%w: stripe signature tolerance", webhook.ErrInvalid)
	}

	if signature.tolerance == 0 {
		signature.tolerance = defaultTolerance
	}

	tolerance := signature.tolerance
	verify := func(request *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
		err := stripehook.ValidatePayloadWithTolerance(body, request.Header.Get("Stripe-Signature"), secret, tolerance)

		if err != nil {
			return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w: %w", webhook.ErrRejected, ErrInvalidSignature, err)
		}

		return parseEnvelope(body)
	}

	return webhook.NewSource(verify), nil
}

// parseEnvelope runs only after authentication and intentionally does not check
// APIVersion. Unbound native events must remain acknowledgeable across versions.
func parseEnvelope(body []byte) (webhook.Delivery[Envelope], error) {
	var envelope Envelope

	if err := json.Unmarshal(body, &envelope); err != nil {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	if envelope.ID == "" || envelope.Type == "" || envelope.Object != "event" {
		return webhook.Delivery[Envelope]{}, errEnvelope
	}

	return webhook.Delivery[Envelope]{ID: envelope.ID, Type: envelope.Type, Payload: envelope}, nil
}
