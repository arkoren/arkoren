package webhook

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/queue"
)

// BuilderFunc configures the policy and bindings of [Handler]. It is an alias,
// so ordinary functions and slices of the identical function type remain usable.
// Handler calls callbacks once, synchronously in order on one fresh draft, even
// when a setting is invalid. Nil entries panic; expanded nil or empty slices
// supply no callbacks. Publication copies owned containers, not bound functions,
// services or their captures.
//
// Example:
//
//	configure := []webhook.BuilderFunc[Envelope]{
//		func(receiver *webhook.Builder[Envelope]) {
//			receiver.Handle(InvoicePaid, acceptInvoice)
//		},
//		func(receiver *webhook.Builder[Envelope]) {
//			receiver.MaxBodySize(256 << 10)
//		},
//	}
//	handler, err := webhook.Handler(source, configure...)
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/billing", handler)
type BuilderFunc[E any] = func(*Builder[E])

// binding is one bound event with its type erased: it decodes the envelope and
// returns the accept step, so the receiver can tell a decode failure from an
// accept failure without knowing the payload type.
type binding[E any] func(Delivery[E]) (accept func(context.Context) error, err error)

// newBinding erases the payload type of definition and accept: the returned
// binding decodes the envelope payload with definition and defers accept to the
// returned step, so a decode failure stays distinguishable from an accept
// failure.
func newBinding[E, P any](definition Event[E, P], accept AcceptFunc[P]) binding[E] {
	return func(delivery Delivery[E]) (func(context.Context) error, error) {
		payload, err := definition.decode(delivery.Payload)

		if err != nil {
			return nil, err
		}

		return func(ctx context.Context) error {
			return accept(ctx, Delivery[P]{ID: delivery.ID, Type: delivery.Type, Payload: payload})
		}, nil
	}
}

// validateReject reports why status and errs cannot form a [Builder.Reject]
// mapping, or nil when they can. Statuses 406 (Mailgun stops retrying), 410
// (Standard Webhooks disables the endpoint), 408 and 429 (both mean "retry
// later") are refused because providers react to them beyond a plain failure.
func validateReject(status int, errs []error) error {
	switch {
	case status < 400 || status > 499:
		return fmt.Errorf("Reject status %d is not 4xx", status)
	case status == http.StatusNotAcceptable, status == http.StatusGone,
		status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return fmt.Errorf("Reject status %d changes provider retry behavior", status)
	case len(errs) == 0 || slices.Contains(errs, nil):
		return fmt.Errorf("Reject status %d needs at least one non-nil error", status)
	}

	return nil
}

// mapping is one [Builder.Reject] call.
type mapping struct {
	// status is the 4xx status answered.
	status int

	// errs are the verify errors that select status.
	errs []error
}

// settings is the completed configuration of a handler.
type settings[E any] struct {
	// maxBodySize is the body limit in bytes.
	maxBodySize int64

	// rejects are the Reject mappings in call order.
	rejects []mapping

	// seen is the deduplication cache. Nil disables deduplication.
	seen *cache.Cache

	// seenSpace supplies the exact namespace and retention policy for IDs.
	seenSpace cache.Namespace[bool]

	// events receives [Rejected], [Failed] and [Ignored]. Nil publishes nothing.
	events *event.Bus

	// label is the optional diagnostics label.
	label string

	// bindings maps a provider event key to its handler.
	bindings map[string]binding[E]

	// err is the first invalid setting.
	err error
}

// Builder configures [Handler] before publication. Callbacks run directly once,
// synchronously in order on one fresh draft; nil entries panic, and an expanded
// nil or empty slice supplies none. The first invalid setting is kept and
// returned by [Handler] wrapping [ErrInvalid]. Retaining the draft cannot
// change a published handler: owned binding and rejection containers are copied,
// not services, functions, error values, or their captures. Shared dependencies
// must support concurrent requests. Methods return the same draft.
//
// Example:
//
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[Envelope]) {
//		receiver.Handle(InvoicePaid, acceptInvoice)
//		receiver.MaxBodySize(256 << 10)
//	})
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/billing", handler)
type Builder[E any] struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the handler.
	settings settings[E]
}

// fail records the first invalid setting with [ErrInvalid] and operation
// context. Later failures leave it unchanged without formatting another error;
// construction callbacks still execute normally on the same draft.
func (receiver *Builder[E]) fail(format string, args ...any) *Builder[E] {
	if receiver.settings.err != nil {
		return receiver
	}

	receiver.settings.err = fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)

	return receiver
}

// Handle binds accept to definition: a delivery whose type is definition's key is
// decoded and passed to accept after authentication. One event has one owner,
// so binding a key twice is invalid, and there is no fan-out. Events that are
// not bound are acknowledged and ignored. An invalid event or a nil accept is
// invalid. Decoder and accept functions and their captures remain shared, not
// cloned; they must support concurrent requests.
//
// Example:
//
//	receiver.Handle(acmewebhook.InvoicePaid, func(
//		ctx context.Context,
//		delivery webhook.Delivery[acmewebhook.Invoice],
//	) error {
//		return invoices.MarkPaid(ctx, delivery.Payload.ID)
//	})
func (receiver *Builder[E]) Handle[P any](definition Event[E, P], accept AcceptFunc[P]) *Builder[E] {
	if err := definition.Err(); err != nil {
		return receiver.fail("Handle: %w", err)
	}

	if accept == nil {
		return receiver.fail("Handle %q: accept must not be nil", definition.key)
	}

	if _, bound := receiver.settings.bindings[definition.key]; bound {
		return receiver.fail("Handle %q: the event is already bound", definition.key)
	}

	if receiver.settings.bindings == nil {
		receiver.settings.bindings = map[string]binding[E]{}
	}

	receiver.settings.bindings[definition.key] = newBinding(definition, accept)

	return receiver
}

// Dispatch binds definition to a queue job: the delivery becomes the payload of job
// through dispatcher, and the receiver answers 200 once the queue driver
// accepted the message. Acknowledgement is as durable as that driver.
//
// The job payload is the [Delivery] with its stable JSON tags, so the job keeps
// the provider event ID and type; the queue still generates its own message
// ID. Queue limits on message size may be below [Builder.MaxBodySize]. A
// payload type that must survive across releases while messages are queued
// should be an application-owned DTO rather than a vendor SDK type; [Event.Map]
// reuses the provider decoder for that conversion. A nil dispatcher, an invalid
// job or an invalid event is invalid. The dispatcher remains shared; its owner
// manages any required lifecycle independently of this receiver.
//
// Example:
//
//	invoicePaid := acmewebhook.InvoicePaid.Map(func(invoice acmewebhook.Invoice) (InvoiceDTO, error) {
//		return InvoiceDTO{ID: invoice.ID}, nil
//	})
//	job := queue.NewJob[webhook.Delivery[InvoiceDTO]]("invoice-paid")
//	receiver.Dispatch(invoicePaid, dispatcher, job)
func (receiver *Builder[E]) Dispatch[P any](
	definition Event[E, P],
	dispatcher *queue.Dispatcher,
	job queue.Job[Delivery[P]],
) *Builder[E] {
	if dispatcher == nil {
		return receiver.fail("Dispatch %q: dispatcher must not be nil", definition.key)
	}

	if err := job.Err(); err != nil {
		return receiver.fail("Dispatch %q: %w", definition.key, err)
	}

	return receiver.Handle(definition, func(ctx context.Context, delivery Delivery[P]) error {
		return dispatcher.Dispatch(ctx, job, delivery)
	})
}

// MaxBodySize limits the body to size bytes; a larger body answers 413
// [request.ErrBodyTooLarge]. Zero, the default, means 1 MiB. An outer bodylimit
// middleware remains a ceiling: the smaller limit applies. A negative size is
// invalid.
//
// Example:
//
//	receiver.MaxBodySize(256 << 10)
func (receiver *Builder[E]) MaxBodySize(size int64) *Builder[E] {
	if size < 0 {
		return receiver.fail("MaxBodySize %d is negative", size)
	}

	receiver.settings.maxBodySize = size

	return receiver
}

// Reject answers status when the verifier's error matches (errors.Is) any of
// errs, so a caller can map a signature failure to 401 and a malformed payload
// to 400. Calls accumulate; the first matching mapping in call order wins.
//
// status must be a 4xx. 406 (Mailgun stops retrying), 410 (Standard Webhooks
// disables the endpoint), 408 and 429 (both mean "retry later") are refused, as
// is a call without errs or with a nil error; [Handler] then returns an error
// wrapping [ErrInvalid].
//
// The error slice is copied, but the error values remain shared; do not mutate
// their matching behavior after publication. With no mappings, [ErrRejected]
// still answers 400 and unclassified verify errors answer 503.
//
// Example:
//
//	receiver.Reject(http.StatusUnauthorized, payment.ErrInvalidSignature)
func (receiver *Builder[E]) Reject(status int, errs ...error) *Builder[E] {
	if err := validateReject(status, errs); err != nil {
		return receiver.fail("%w", err)
	}

	receiver.settings.rejects = append(receiver.settings.rejects, mapping{status: status, errs: slices.Clone(errs)})

	return receiver
}

// Deduplicate skips deliveries whose provider event ID was already handled,
// remembering IDs in store using space's TTL; zero TTL retains them forever.
//
// space is required: a stable, explicit namespace that separates this receiver's
// IDs from others sharing the cache, such as "billing". It is never derived
// from the route, the provider or a secret, so renaming a route is safe and
// changing space deliberately forgets what was seen, allowing duplicates.
// Deliveries with an empty [Delivery.ID] are never suppressed.
// Names are used literally, not base64 encoded. IDs alone are base64url encoded.
// The namespace defaults to colon joins. Set Separator(".") explicitly to keep
// markers written with the former dot default; no fallback or migration occurs.
// Every separator is literal, including empty concatenation. Callers sharing
// a cache must choose names and separators whose resulting keys differ for
// unrelated deliveries; different namespaces do not guarantee isolation.
//
// The ID is checked before decoding and remembered after the handler succeeded
// and before the 200 is written, so a failed handler does not suppress a later
// attempt. This is best effort: a cache error fails open toward handling,
// concurrent deliveries of one ID can both pass the check, and a failure to
// remember is ignored. Handlers must be idempotent. A nil store or an invalid
// or zero namespace is invalid.
//
// Without this setting, deduplication is disabled. The cache remains shared;
// this receiver does not start or stop it. Repeated calls replace store and space.
//
// Example:
//
//	space := cache.NewNamespace[bool]("webhook.billing", func(space *cache.NamespaceBuilder) {
//		space.TTL(24 * time.Hour)
//	})
//	receiver.Deduplicate(store, space)
func (receiver *Builder[E]) Deduplicate(store *cache.Cache, space cache.Namespace[bool]) *Builder[E] {
	if store == nil {
		return receiver.fail("Deduplicate: cache must not be nil")
	}

	if err := space.Err(); err != nil {
		return receiver.fail("Deduplicate: invalid namespace: %v", err)
	}

	receiver.settings.seen = store
	receiver.settings.seenSpace = space

	return receiver
}

// Events publishes [Rejected], [Failed] and [Ignored] to bus. Nil, the
// default, publishes nothing. Calls replace the shared bus; the receiver neither
// clones it nor owns its listeners' lifecycle. Listeners cannot change receipts.
//
// Example:
//
//	receiver.Events(bus)
func (receiver *Builder[E]) Events(bus *event.Bus) *Builder[E] {
	receiver.settings.events = bus

	return receiver
}

// Label names the receiver in published events, to tell receivers apart. It is
// a short, low-cardinality diagnostic string chosen by the application, not an
// identity: it is never part of a deduplication key. The default is empty.
// Calls replace the label without validation or redaction. Do not include
// credentials, personal identifiers, or provider-controlled values.
//
// Example:
//
//	receiver.Label("billing")
func (receiver *Builder[E]) Label(label string) *Builder[E] {
	receiver.settings.label = label

	return receiver
}
