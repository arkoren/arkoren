package webhook

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/foundation/problem"
)

const (
	// defaultMaxBodySize is the body limit when [Builder.MaxBodySize] is unset.
	defaultMaxBodySize int64 = 1 << 20
)

// ErrSkip is returned (or wrapped) by a [VerifyFunc] or an [Event] decoder for
// an authentic delivery that needs no processing, such as an event the
// provider sends for every account. The receiver acknowledges with 200 without
// calling the handler or recording the ID for deduplication. It must only be
// returned after authentication succeeded. From an [AcceptFunc] it has no
// special meaning: the delivery fails with 503 like any other accept error.
//
// Example:
//
//	return webhook.Delivery[Envelope]{}, fmt.Errorf("ping event: %w", webhook.ErrSkip)
var ErrSkip = errors.New("webhook: delivery skipped")

// ErrInvalid is wrapped by errors from [Handler], [Event.Err] and [Source.Err]
// for invalid definitions or settings, such as a nil function, a
// duplicate event binding, or a [Builder.Reject] status that is not allowed.
//
// Example:
//
//	_, err := webhook.Handler(source)
//	if errors.Is(err, webhook.ErrInvalid) {
//		return fmt.Errorf("configure at least one webhook event: %w", err)
//	}
var ErrInvalid = errors.New("webhook: invalid configuration")

// ErrRejected is the class of a delivery the receiver refuses with a 4xx
// status. A verifier may return it (or wrap it) for 400; [Builder.Reject]
// selects other 4xx statuses. A returned receipt error always matches it with
// errors.Is, whatever the status, and still matches the original cause.
//
// Example:
//
//	failure := webhook.ErrRejected.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrRejected = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/webhook:rejected")
	details.Title("Webhook Rejected")
	details.Detail("The webhook delivery is invalid and was rejected.")
	details.Status(http.StatusBadRequest)
})

// ErrUnavailable is the class of a delivery that could not be processed now
// and answers 503. A returned receipt error always matches it with errors.Is
// and still matches the original cause.
//
// Example:
//
//	failure := webhook.ErrUnavailable.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrUnavailable = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/webhook:unavailable")
	details.Title("Webhook Unavailable")
	details.Detail("The webhook delivery could not be processed now. Retry later.")
	details.Status(http.StatusServiceUnavailable)
})

// Handler returns the receiver for one provider account, ready for router.Post.
// source authenticates requests; the callbacks bind handlers to events and set
// the application's policy. There is no endpoint name: a route is just a route.
//
// Per request it reads the body within the limit, authenticates it with the
// source, finds the handler bound to the delivery's event type, optionally
// skips an already handled event ID, decodes the event, calls the handler, and
// answers 200 with an empty body only if the handler returned nil. See the
// package documentation for the status of every outcome.
//
// It returns an error wrapping [ErrInvalid] for an invalid source, an invalid
// setting, a binding error, or when no event is bound. Nothing else is
// validated lazily at request time. A nil entry in configure panics.
// Callbacks run once synchronously in order, including on invalid input; an
// expanded nil or empty slice supplies none. Publication detaches owned binding
// and rejection containers, not services, error values, functions, or captures.
// The handler owns no dependency lifecycle; shared dependencies and captures
// must be safe for concurrent requests.
//
// Example:
//
// With cache imported from arkoren.dev/core/service/cache, store is a
// *cache.Cache. source verifies Envelope values, InvoicePaid decodes the
// invoice event, acceptInvoice handles it, and r is the application's router.
// The namespace's default zero TTL retains deduplication markers without expiry.
//
//	space := cache.NewNamespace[bool]("billing")
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[Envelope]) {
//		receiver.Handle(InvoicePaid, acceptInvoice)
//		receiver.Deduplicate(store, space)
//	})
//	if err != nil {
//		return fmt.Errorf("billing webhook: %w", err)
//	}
//	r.Post("/webhooks/billing", handler)
func Handler[E any](source Source[E], configure ...BuilderFunc[E]) (router.Handler, error) {
	receiver := &Builder[E]{}

	for _, configure := range configure {
		configure(receiver)
	}

	config := receiver.settings

	if err := cmp.Or(config.err, source.Err()); err != nil {
		return nil, err
	}

	if len(config.bindings) == 0 {
		return nil, fmt.Errorf("%w: no event is bound", ErrInvalid)
	}

	config.maxBodySize = cmp.Or(config.maxBodySize, defaultMaxBodySize)
	config.rejects = slices.Clone(config.rejects)
	config.bindings = maps.Clone(config.bindings)

	published := &endpoint[E]{settings: config, verify: source.verify}

	return published.serve, nil
}

// endpoint is a published receiver: completed settings and the source's
// verifier.
type endpoint[E any] struct {
	// settings is the completed configuration.
	settings settings[E]

	// verify authenticates requests.
	verify VerifyFunc[E]
}

// errAcknowledged is the internal signal that a delivery is answered with an
// empty 200 without calling its handler: an authentic skip, an event nobody
// bound, or an ID already handled. It never leaves the package.
var errAcknowledged = errors.New("webhook: delivery acknowledged")

// serve handles one request. [endpoint.receive] and its helpers render every
// failure response and return the receipt error for logging; serve only
// answers 200 with an empty body for a handled delivery or an acknowledgement
// without handling.
func (endpoint *endpoint[E]) serve(writer http.ResponseWriter, r *http.Request) error {
	err := endpoint.receive(writer, r)

	if err != nil && !errors.Is(err, errAcknowledged) {
		return err
	}

	writer.WriteHeader(http.StatusOK)

	return nil
}

// receive runs the delivery pipeline: authenticate, find the bound event, then
// accept. It returns nil after a handled delivery, [errAcknowledged] when the
// delivery needs no handling, and a receipt error once a failure response was
// rendered.
func (endpoint *endpoint[E]) receive(writer http.ResponseWriter, r *http.Request) error {
	delivery, err := endpoint.authenticate(writer, r)

	if err != nil {
		return err
	}

	bound, ok := endpoint.settings.bindings[delivery.Type]

	if !ok {
		endpoint.settings.events.Emit(r.Context(), Ignored, IgnoredEvent{
			Label:  endpoint.settings.label,
			Reason: IgnoreReasonUnbound,
		})

		return errAcknowledged
	}

	return endpoint.accept(writer, r, bound, delivery)
}

// authenticate reads the body within the limit and verifies it with the
// source. A failure is answered by [endpoint.bodyFailure] or
// [endpoint.verifyFailure] and returned.
func (endpoint *endpoint[E]) authenticate(writer http.ResponseWriter, r *http.Request) (Delivery[E], error) {
	body, err := request.Bytes(r, request.MaxSize(endpoint.bodyLimit(r.Context())))

	if err != nil {
		return Delivery[E]{}, endpoint.bodyFailure(writer, r, err)
	}

	delivery, err := endpoint.verify(r, body)

	if err != nil {
		return Delivery[E]{}, endpoint.verifyFailure(writer, r, err)
	}

	return delivery, nil
}

// accept skips a duplicate ID, decodes the delivery with its bound event, calls
// the handler and remembers the ID only after the handler succeeded. A decode
// skip is acknowledged; every other decode or accept error answers 503.
func (endpoint *endpoint[E]) accept(
	writer http.ResponseWriter,
	r *http.Request,
	bound binding[E],
	delivery Delivery[E],
) error {
	ctx := r.Context()
	key := endpoint.seenKey(delivery.ID)

	if endpoint.seenBefore(ctx, key) {
		return errAcknowledged
	}

	accept, err := bound(delivery)

	if errors.Is(err, ErrSkip) {
		return errAcknowledged
	}

	if err != nil {
		return endpoint.unavailable(writer, r, FailureStageDecode, err)
	}

	if err := accept(ctx); err != nil {
		return endpoint.unavailable(writer, r, FailureStageAccept, err)
	}

	endpoint.remember(ctx, key)

	return nil
}

// bodyLimit returns the effective body limit: the configured one, lowered to an
// outer bodylimit middleware's ceiling.
func (endpoint *endpoint[E]) bodyLimit(ctx context.Context) int64 {
	limit := endpoint.settings.maxBodySize

	if ceiling, ok := request.MaxBodySizeFromContext(ctx); ok && ceiling > 0 {
		return min(limit, ceiling)
	}

	return limit
}

// seenKey returns the deduplication key for id, or a zero key when disabled.
// Only IDs are encoded; the explicitly supplied namespace remains literal.
func (endpoint *endpoint[E]) seenKey(id string) cache.Key[bool] {
	if endpoint.settings.seen == nil || id == "" {
		return cache.Key[bool]{}
	}

	encoding := base64.RawURLEncoding

	return endpoint.settings.seenSpace.Key(encoding.EncodeToString([]byte(id)))
}

// seenBefore reports whether key was remembered. A cache error fails open:
// handling a duplicate is better than losing an event.
func (endpoint *endpoint[E]) seenBefore(ctx context.Context, key cache.Key[bool]) bool {
	if key.Err() != nil {
		return false
	}

	seen, err := endpoint.settings.seen.Has(ctx, key)

	return err == nil && seen
}

// remember records key after a successful handler. A failure only risks a
// duplicate, which handlers must tolerate, so it is ignored.
func (endpoint *endpoint[E]) remember(ctx context.Context, key cache.Key[bool]) {
	if key.Err() != nil {
		return
	}

	_, _ = endpoint.settings.seen.Add(ctx, key, true)
}
