// Package webhook receives inbound webhook notifications: it reads the raw
// body within a size limit, authenticates the request with a provider
// [Source], hands the delivery to the handler the application bound to its
// event, and acknowledges acceptance with 200 after that handler succeeds.
// Authenticated unbound events, duplicates and deliberate preaccept skips are
// also acknowledged without running a handler.
//
// # Overview
//
// Three parts have separate owners:
//
//   - A [Source] authenticates the requests of one provider account and parses
//     them into an envelope of type E. It holds the credentials, so a vendor
//     package returns one from its own constructor.
//   - An [Event] names one provider event type and decodes the envelope into
//     the payload type P. It holds no credentials: a vendor package, library or
//     ark publishes it as an immutable package-level variable, and the same
//     Event works with any number of Sources.
//   - The application calls [Handler] and binds handlers to events with
//     [Builder.Handle] or [Builder.Dispatch]. Cache, event bus, body limit and
//     rejection statuses are the application's policy, not the Source's.
//
// There is no endpoint name and no receiver object: [Handler] returns a
// [router.Handler] for router.Post, and configuration errors are returned
// when it is built.
//
// # Receiving an event
//
// The acmewebhook package in these examples is hypothetical, not an existing
// integration. Its constructor validates the signing secret and its verifier
// wraps [ErrRejected] for known invalid signatures or envelopes; unexpected
// failures remain unclassified and answer 503.
//
// Example:
//
//	source, err := acmewebhook.New(signingSecret)
//	if err != nil {
//		return err
//	}
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[acmewebhook.Envelope]) {
//		receiver.Handle(acmewebhook.OrderPaid, acceptOrder)
//	})
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/orders", handler)
//
// Add another Handle call in the same callback to receive another event. The
// compiler infers Handler's envelope type from source and Handle's payload type
// from the event and callback; explicit generic arguments are unnecessary.
//
// # Request flow
//
//  1. Read the body, at most [Builder.MaxBodySize] bytes (and never more than
//     an outer bodylimit middleware allows).
//  2. Authenticate with the [VerifyFunc] of the Source. Nothing is selected or
//     handled before this succeeds.
//  3. Find the handler bound to the delivery's event type.
//  4. If [Builder.Deduplicate] is on and the delivery has an ID, skip a
//     delivery already handled.
//  5. Decode the envelope with the [Event].
//  6. Call the handler.
//  7. Remember the ID, then answer 200 with an empty body.
//
// # Statuses
//
//   - Success, an authenticated delivery skipped with [ErrSkip] by the Source or
//     the decoder, a delivery of an event type nobody bound, and a duplicate all
//     answer exactly 200 with an empty body. An unbound type publishes
//     [Ignored] with a reason and no type.
//   - A body over the limit answers 413. A body that cannot be read answers 400
//     when it is malformed, and 503 when the read was cut short by
//     cancellation or a timeout.
//   - A verify error matching a [Builder.Reject] mapping answers that 4xx; one
//     wrapping [ErrRejected] answers 400; any other verify error answers 503.
//   - A decode error answers 503. A schema or API-version mismatch can be fixed
//     by a deploy, so it is not treated as permanent.
//   - Every handler error answers 503, including [ErrSkip] and permanent queue
//     errors. The handler decides nothing about 4xx.
//
// Return nil for a deliberate no-op in acceptance: it acknowledges and records
// the ID when deduplication is enabled. An ErrSkip from the Source or decoder
// acknowledges before acceptance and does not record the ID.
//
// Retries are the provider's policy and differ: some providers retry every
// non-2xx answer, others never redeliver a failed delivery automatically. A
// 503 therefore means "may be retried", never "will be retried".
//
// # Failure responses and errors
//
// Failure responses are rendered by the receiver itself as safe problem
// details without any cause, then committed, so an app-wide error mapper
// cannot change what the provider received. The returned error matches both
// its class ([ErrRejected] or [ErrUnavailable]) and the original cause with
// errors.Is, so logging keeps the cause. Middleware hooks around the response
// writer still run as usual, and a mapper can still affect how the returned
// error is classified in logs. Events never carry a body, header, ID, event
// type or cause.
//
// # Observing receipt categories
//
// Configure [Builder.Events] to observe [Rejected], [Failed] and [Ignored].
// Their payload fields use [RejectionReason], [FailureStage] and [IgnoreReason],
// respectively. These are string-backed categories with stable explicit values,
// not provider event keys. Their empty zero values remain unspecified, with no
// normalization. [Delivery.Type], [Event.Key] and [Builder.Label] stay strings.
//
// Compare against constants; convert explicitly for APIs requiring a string:
//
//	unsubscribe, err := bus.Listen(webhook.Failed, func(ctx context.Context, failed webhook.FailedEvent) error {
//		logger.ErrorContext(ctx, "webhook failed", "stage", string(failed.Stage))
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer unsubscribe()
//
// Events omit request data and causes, but labels are application-controlled and
// unvalidated. Keep credentials and personal identifiers out of labels. Listener
// contexts retain request context values, and returned errors retain causes;
// neither is generally sanitized. HTTP problem instances can still include the
// request path. Skips and duplicates publish no Ignored event.
//
// # Configuration ownership and defaults
//
// [Handler] runs its construction callbacks once in order and then copies owned
// binding and rejection containers. Retaining a draft cannot change the published
// receiver. Services, functions, error values and closure captures remain shared;
// the receiver does not start or stop them. They must support concurrent requests.
// The omitted or zero body limit is 1 MiB; negative limits are invalid. Without
// explicit settings there is no deduplication, no event publication, no label and
// no custom rejection mapping. [ErrRejected] still selects 400 without a mapping.
//
// # Deduplication
//
// [Builder.Deduplicate] needs an explicit stable typed namespace. Return a stable
// provider event ID in [Delivery.ID]; an ID that changes per delivery attempt
// defeats suppression. The check is best effort: it fails open, concurrent
// duplicates can both be handled, and the ID is remembered only after the
// handler succeeded. Handlers must be idempotent. Changing the namespace forgets
// everything seen.
//
// # Not covered
//
// Delivery and retry guarantees belong to the provider; assume duplicates.
// Acceptance is not transactional: a handler that failed may have committed
// part of its work. Protocols whose response carries data (Slack challenges,
// interactive decision callbacks) are out of scope; wrap the handler and own
// the response. Decoding must not fetch data
// over the network, so providers whose notifications are thin must be handled
// by the application. Respect the provider's response deadline: hand slow work
// to a queue with [Builder.Dispatch] and return quickly. Acknowledgement is only
// as durable as the selected queue driver; no inbox or outbox is provided.
//
// # Mapping payloads for a queue
//
// [Event.Map] reuses a provider event's decoder to produce an application-owned
// DTO while keeping its key and Delivery metadata. It returns a new definition;
// the original stays reusable. Conversion must be pure and perform no I/O.
// Conversion errors follow the decoder's 503/ErrSkip policy, not acceptance's.
//
//	orderPaid := acmewebhook.OrderPaid.Map(func(order acmewebhook.Order) (OrderDTO, error) {
//		return OrderDTO{OrderID: order.OrderID}, nil
//	})
//	job := queue.NewJob[webhook.Delivery[OrderDTO]]("order-paid")
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[acmewebhook.Envelope]) {
//		receiver.Dispatch(orderPaid, dispatcher, job)
//	})
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/orders", handler)
//
// # Publishing events from a library or ark
//
// A library validates its credentials in its Source constructor and publishes
// credential-free Events separately. For example, its event declarations can
// decode raw JSON without implementing any application policy:
//
//	package acmewebhook
//
//	import (
//		"encoding/json/jsontext"
//		"encoding/json/v2"
//
//		"arkoren.dev/core/http/webhook"
//	)
//
//	type Envelope struct {
//		ID   string
//		Kind string
//		Data jsontext.Value
//	}
//
//	type Shipment struct {
//		ID string `json:"id"`
//	}
//
//	var ShipmentDelivered = webhook.NewEvent("shipment.delivered", func(envelope Envelope) (Shipment, error) {
//		var shipment Shipment
//		err := json.Unmarshal(envelope.Data, &shipment)
//
//		return shipment, err
//	})
//
// An ark can export a [BuilderFunc] that binds its handlers, leaving policy to
// the application. The shipping package below is also hypothetical:
//
//	func Webhooks(store *Store) webhook.BuilderFunc[acmewebhook.Envelope] {
//		return func(receiver *webhook.Builder[acmewebhook.Envelope]) {
//			receiver.Handle(acmewebhook.ShipmentDelivered, store.MarkDelivered)
//		}
//	}
//
// The application composes them with its own policy:
//
//	handler, err := webhook.Handler(source, shipping.Webhooks(store), func(receiver *webhook.Builder[acmewebhook.Envelope]) {
//		receiver.Deduplicate(store, cache.NewNamespace[bool]("shipping", func(space *cache.NamespaceBuilder) {
//			space.TTL(72 * time.Hour)
//		}))
//	})
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/shipping", handler)
package webhook
