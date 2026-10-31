package mail

import (
	"context"
	"errors"

	"arkoren.dev/core/event"
)

// SentEvent observes driver acceptance, not recipient delivery. It contains
// submission metadata and counts, never message content or destination addresses.
//
// Example:
//
//	stop, err := bus.Listen(mail.Sent, func(ctx context.Context, observed mail.SentEvent) error {
//		fmt.Println(observed.Recipients)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
type SentEvent struct {
	// Receipt identifies the accepted submission, not a delivered message.
	// The opaque value is supplied by the driver; it can be empty.
	//
	// Example:
	//
	//	observed := mail.SentEvent{Receipt: mail.Receipt{ID: "submission-123"}}
	//	_ = observed.Receipt.ID
	Receipt Receipt

	// Recipients counts To, CC, and BCC entries, including repeated addresses.
	// It does not disclose which addresses received the submission.
	//
	// Example:
	//
	//	observed := mail.SentEvent{Recipients: 2}
	//	_ = observed.Recipients
	Recipients int

	// Attachments counts regular and inline attachments together. Zero means
	// the submitted message contained no attachments.
	//
	// Example:
	//
	//	observed := mail.SentEvent{Attachments: 1}
	//	_ = observed.Attachments
	Attachments int
}

// FailedEvent observes a returned validation or driver failure, not proof that
// delivery is impossible. Only a bounded classification and counts are retained;
// Mailer.Send separately returns the original error with its causes intact.
//
// Example:
//
//	stop, err := bus.Listen(mail.Failed, func(ctx context.Context, observed mail.FailedEvent) error {
//		fmt.Println(observed.ErrorClass)
//		return nil
//	})
//	if err != nil {
//		return err
//	}
//	defer stop()
type FailedEvent struct {
	// ErrorClass is context.Canceled, context.DeadlineExceeded,
	// mail.ErrInvalidMessage, or "error" for any other failure, in that priority
	// order. It never includes diagnostic text, arbitrary type names, or causes.
	// These classes do not establish whether a provider accepted a submission.
	//
	// Example:
	//
	//	observed := mail.FailedEvent{ErrorClass: "mail.ErrInvalidMessage"}
	//	_ = observed.ErrorClass
	ErrorClass string

	// Recipients counts the attempted To, CC, and BCC entries. It can be zero
	// for an invalid declaration and never contains an address itself.
	//
	// Example:
	//
	//	observed := mail.FailedEvent{Recipients: 1}
	//	_ = observed.Recipients
	Recipients int

	// Attachments counts attempted regular and inline attachments together.
	// The records and payload bytes are excluded from this observation.
	//
	// Example:
	//
	//	observed := mail.FailedEvent{Attachments: 1}
	//	_ = observed.Attachments
	Attachments int
}

var (
	// Sent reports a returned driver acceptance when Builder.Events is set.
	// It is synchronous and best effort, never confirmation of delivery.
	// Driver panics publish no outcome; listener failures cannot replace it.
	//
	// Example:
	//
	//	stop, err := bus.Listen(mail.Sent, func(ctx context.Context, observed mail.SentEvent) error {
	//		fmt.Println(observed.Recipients)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer stop()
	Sent = event.NewTopic[SentEvent]("mail.sent")

	// Failed reports a returned validation or driver error, excluding a nil
	// context rejected before instrumentation. Its bounded payload cannot leak
	// the diagnostic error returned by Send. It does not prove non-delivery.
	//
	// Example:
	//
	//	stop, err := bus.Listen(mail.Failed, func(ctx context.Context, observed mail.FailedEvent) error {
	//		fmt.Println(observed.ErrorClass)
	//		return nil
	//	})
	//	if err != nil {
	//		return err
	//	}
	//	defer stop()
	Failed = event.NewTopic[FailedEvent]("mail.failed")
)

// emit publishes a returned outcome with counts from the immutable message.
// Disabled events skip classification; native errors remain solely with Send's
// caller. A listener cannot change the returned submission outcome.
func (mailer *Mailer) emit(ctx context.Context, message Message, receipt Receipt, err error) {
	if mailer.defaults.Events == nil {
		return
	}

	recipients := len(message.to) + len(message.cc) + len(message.bcc)

	if err != nil {
		mailer.defaults.Events.Emit(ctx, Failed, FailedEvent{
			ErrorClass:  failureClass(err),
			Recipients:  recipients,
			Attachments: len(message.attachments),
		})

		return
	}

	mailer.defaults.Events.Emit(ctx, Sent, SentEvent{
		Receipt:     receipt,
		Recipients:  recipients,
		Attachments: len(message.attachments),
	})
}

// failureClass limits event metadata to known identities, never native text or
// concrete provider types. Context causes take precedence over invalid input.
func failureClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context.Canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context.DeadlineExceeded"
	case errors.Is(err, ErrInvalidMessage):
		return "mail.ErrInvalidMessage"
	default:
		return "error"
	}
}
