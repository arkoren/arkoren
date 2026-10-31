package webhook

import (
	"context"
	"fmt"
	"net/http"
)

// Delivery is one authenticated inbound notification.
//
// A [Source] returns a Delivery whose Payload is the provider envelope; a
// handler bound with [Builder.Handle] receives a Delivery whose Payload is the
// event's decoded type. The JSON tags are stable so a Delivery can be the
// payload of a queue job (see [Builder.Dispatch]).
//
// Example:
//
//	func(ctx context.Context, delivery webhook.Delivery[Invoice]) error {
//		return invoices.MarkPaid(ctx, delivery.Payload.ID)
//	}
type Delivery[T any] struct {
	// ID is the provider's event ID. It must be stable across the provider's
	// retries of the same event, never an ID of one delivery attempt. Empty
	// disables deduplication for the delivery.
	//
	// Example:
	//	delivery := webhook.Delivery[Invoice]{ID: "evt-42", Type: "invoice.paid", Payload: invoice}
	//	logger.DebugContext(ctx, "invoice delivery received", "has_id", delivery.ID != "")
	ID string `json:"id"`

	// Type is the opaque provider event key, such as "invoice.paid". It is
	// matched exactly to [Event.Key], not normalized into an owned category.
	//
	// Example:
	//	delivery := webhook.Delivery[Envelope]{ID: envelope.ID, Type: envelope.Kind, Payload: envelope}
	//	return delivery, nil // from the authenticated verifier
	Type string `json:"type"`

	// Payload is the envelope from a [Source] or the decoded event in a handler.
	// Reference-bearing values are not deep-copied; handlers own any mutations
	// and must not race with other users of shared data.
	//
	// Example:
	//	return invoices.MarkPaid(ctx, delivery.Payload.ID)
	Payload T `json:"payload"`
}

// VerifyFunc authenticates one request and returns the delivery it carries.
//
// r supplies the method, URL and headers (some providers sign the URL); its
// context is the request context. body is the raw body already read within the
// size limit, exactly as sent, and r.Body is spent. Nothing may be selected or
// trusted before authentication succeeds, although a provider may parse what
// the signature scheme needs.
//
// An error wrapping [ErrSkip], returned only after authentication succeeded,
// acknowledges the delivery without handling it. An error matching a
// [Builder.Reject] mapping or wrapping [ErrRejected] rejects it with a 4xx
// status. Any other error answers 503; whether it is retried is the provider's
// policy, not a guarantee of the receiver.
//
// Example:
//
//	verify := func(r *http.Request, body []byte) (webhook.Delivery[Envelope], error) {
//		envelope, err := parse(body, r.Header.Get("X-Signature"), secret)
//		if err != nil {
//			return webhook.Delivery[Envelope]{}, fmt.Errorf("%w: %w", webhook.ErrRejected, err)
//		}
//
//		return webhook.Delivery[Envelope]{ID: envelope.ID, Type: envelope.Kind, Payload: envelope}, nil
//	}
type VerifyFunc[E any] = func(r *http.Request, body []byte) (Delivery[E], error)

// Source authenticates the requests of one provider account and parses them
// into envelopes of type E. It holds the account's credentials and nothing of
// the application's policy: cache, events and handlers belong to the
// [Builder]. A vendor package usually returns a Source from its own
// constructor, which validates the credentials; [NewSource] is for a verifier
// that is already complete. The zero Source is invalid. The verifier and its
// captured credentials or clients remain shared, not cloned or lifecycle-managed
// by the Source; they must support concurrent requests.
//
// Example:
//
//	source := webhook.NewSource(verify)
//	if err := source.Err(); err != nil {
//		return err
//	}
type Source[E any] struct {
	// verify authenticates and parses one request.
	verify VerifyFunc[E]
}

// NewSource returns a Source that authenticates with verify. A nil verify is
// reported by [Source.Err] and by [Handler]. Construction neither calls verify
// nor validates its credentials; the provider constructor owns that validation.
// The function and its captures are retained, so they must support concurrent use.
//
// Example:
//
//	source := webhook.NewSource(verify)
//	if err := source.Err(); err != nil {
//		return err
//	}
func NewSource[E any](verify VerifyFunc[E]) Source[E] {
	return Source[E]{verify: verify}
}

// Err reports why source cannot be used, wrapping [ErrInvalid]: a nil or
// missing verify function. It returns nil for a usable Source.
//
// Example:
//
//	if err := source.Err(); err != nil {
//		return fmt.Errorf("acme source: %w", err)
//	}
func (source Source[E]) Err() error {
	if source.verify == nil {
		return fmt.Errorf("%w: source has no verify function; build it with NewSource", ErrInvalid)
	}

	return nil
}

// AcceptFunc handles one decoded delivery. The receiver answers 200 only after
// it returns nil. Every error, including [ErrSkip] and a queue permanent error,
// answers 503 so the provider may retry; acceptance is not transactional, so a
// callback that failed may already have committed part of its work. Do the
// minimum here, because the provider is waiting: hand slow work to a queue
// (see [Builder.Dispatch]). Return nil for an intentional no-op: it acknowledges
// the delivery and records its ID when deduplication is enabled. Returning
// [ErrSkip] is not a no-op here; it answers 503 like every other accept error.
//
// Example:
//
//	accept := func(ctx context.Context, delivery webhook.Delivery[Invoice]) error {
//		return invoices.MarkPaid(ctx, delivery.Payload.ID)
//	}
type AcceptFunc[P any] = func(ctx context.Context, delivery Delivery[P]) error
