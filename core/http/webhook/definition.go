package webhook

import (
	"fmt"
	"strings"
	"unicode"
)

// Event names one provider event type and decodes the envelope of type E into
// the payload type P. It is an immutable, credential-free definition, usually
// a package-level variable published by a vendor package, a library or an ark,
// so the same Event works with any number of configured [Source] values. The
// compiler ties a handler to P; it does not check that an Event suits a
// particular account or secret.
//
// The key is opaque to Arkoren: it is the provider's own string, not a job or
// topic name. The zero Event is invalid.
//
// Example:
//
//	var InvoicePaid = webhook.NewEvent("invoice.paid", func(envelope Envelope) (Invoice, error) {
//		return decode[Invoice](envelope.Data)
//	})
type Event[E, P any] struct {
	// key is the provider event key.
	key string

	// decode converts the envelope into the payload.
	decode func(E) (P, error)

	// err is the first invalid input.
	err error
}

// NewEvent returns an Event for the provider event key. The key must not be
// empty or contain control characters, and decode must not be nil; the first
// problem is kept and reported by [Event.Err] and [Builder.Handle]. decode is
// called after authentication, only for a bound event, and must not perform
// network I/O. An error from decode answers 503 unless it wraps [ErrSkip],
// which acknowledges the authenticated delivery without handling it.
//
// Example:
//
//	var PullRequest = webhook.NewEvent("pull_request", func(envelope Envelope) (PullRequestEvent, error) {
//		return decode[PullRequestEvent](envelope.Body)
//	})
func NewEvent[E, P any](key string, decode func(E) (P, error)) Event[E, P] {
	event := Event[E, P]{key: key, decode: decode}

	switch {
	case key == "":
		event.err = fmt.Errorf("%w: event key is empty", ErrInvalid)
	case strings.ContainsFunc(key, unicode.IsControl):
		event.err = fmt.Errorf("%w: event key %q contains a control character", ErrInvalid, key)
	case decode == nil:
		event.err = fmt.Errorf("%w: event %q has no decode function", ErrInvalid, key)
	}

	return event
}

// Key returns the provider event key.
//
// Example:
//
//	key := acmewebhook.InvoicePaid.Key() // "invoice.paid"
func (event Event[E, P]) Key() string {
	return event.key
}

// Err reports why the Event is invalid, wrapping [ErrInvalid], or nil. A zero
// Event, which was not built by [NewEvent], is invalid.
//
// Example:
//
//	if err := InvoicePaid.Err(); err != nil {
//		return err
//	}
func (event Event[E, P]) Err() error {
	if event.err == nil && event.decode == nil {
		return fmt.Errorf("%w: event was not built with NewEvent", ErrInvalid)
	}

	return event.err
}

// Map returns a new Event with the same provider key, decoding with event before
// converting its payload from P to Q. Use it to project a provider's payload
// into an application-owned DTO without repeating envelope decoding or losing
// the delivery ID and type in [Builder.Dispatch]. Neither function runs during
// construction; an original decoder error prevents convert from running.
//
// The original Event is unchanged. Both Events remain credential-free and
// reusable with any compatible [Source]. Functions and their captures are
// retained, not copied; convert must be safe for concurrent calls and must not
// perform I/O or mutate shared payload data. Conversion errors are decode-stage
// failures: 503 unless they wrap [ErrSkip], which acknowledges without calling
// acceptance or recording the ID for deduplication.
//
// An invalid or zero base Event keeps its error ahead of a nil convert. A nil
// convert on a valid Event is invalid. [Event.Err] and registration report the
// first error wrapping [ErrInvalid], without invoking either function. The
// example uses a hypothetical provider package, acmewebhook.
//
// Example:
//
//	invoicePaid := acmewebhook.InvoicePaid.Map(func(invoice acmewebhook.Invoice) (InvoiceDTO, error) {
//		return InvoiceDTO{ID: invoice.ID}, nil
//	})
//	job := queue.NewJob[webhook.Delivery[InvoiceDTO]]("invoice-paid")
//	handler, err := webhook.Handler(source, func(receiver *webhook.Builder[acmewebhook.Envelope]) {
//		receiver.Dispatch(invoicePaid, dispatcher, job)
//	})
//	if err != nil {
//		return err
//	}
//	r.Post("/webhooks/billing", handler)
func (event Event[E, P]) Map[Q any](convert func(P) (Q, error)) Event[E, Q] {
	mapped := Event[E, Q]{key: event.key, err: event.Err()}

	if mapped.err != nil {
		return mapped
	}

	if convert == nil {
		mapped.err = fmt.Errorf("%w: event %q has no convert function", ErrInvalid, event.key)

		return mapped
	}

	mapped.decode = func(envelope E) (Q, error) {
		payload, err := event.decode(envelope)

		if err != nil {
			var zero Q

			return zero, err
		}

		return convert(payload)
	}

	return mapped
}
