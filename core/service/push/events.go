package push

import (
	"context"
	"errors"
	"time"

	"arkoren.dev/core/event"
)

// SubmissionEvent describes one observed driver submission, without recipient,
// message, receipt, or native error data. Acceptance is not delivery or display.
//
// Example:
//
//	_, err := events.Listen(push.Accepted, func(ctx context.Context, observed push.SubmissionEvent) error {
//		log.Printf("submission took %s", observed.Duration)
//		return nil
//	})
//	if err != nil { return err }
type SubmissionEvent struct {
	// Duration measures the observed driver call, not device delivery latency.
	// For example: log.Print(observed.Duration).
	Duration time.Duration

	// ErrorClass is empty for acceptance, outcome_unknown for uncertain provider
	// acceptance, a push sentinel name, canceled, deadline_exceeded, or error.
	// No error text is retained. For example: log.Print(observed.ErrorClass).
	ErrorClass string
}

// Accepted reports a successful driver submission once, after Send returns from
// the driver. It contains no receipt ID and proves neither delivery nor display.
// Events are best effort and in process, never a durable audit trail.
//
// Example:
//
//	_, err := events.Listen(push.Accepted, func(ctx context.Context, observed push.SubmissionEvent) error {
//		return nil
//	})
//	if err != nil { return err }
var Accepted = event.NewTopic[SubmissionEvent]("push.submission.accepted")

// Failed reports a driver submission error, not proof of failed delivery.
// outcome_unknown explicitly means acceptance could have occurred; application
// reconciliation and retry policy remain necessary. Preflight failures emit none.
//
// Example:
//
//	_, err := events.Listen(push.Failed, func(ctx context.Context, observed push.SubmissionEvent) error {
//		log.Print(observed.ErrorClass)
//		return nil
//	})
//	if err != nil { return err }
var Failed = event.NewTopic[SubmissionEvent]("push.submission.failed")

// emit reports exactly the driver boundary and never exposes its returned data.
// Bus.Emit isolates listener errors and panics from the submission result.
func (sender *Sender) emit(ctx context.Context, duration time.Duration, err error) {
	if sender.events == nil {
		return
	}

	observed := SubmissionEvent{Duration: duration}

	if err != nil {
		observed.ErrorClass = submissionErrorClass(err)

		sender.events.Emit(ctx, Failed, observed)

		return
	}

	sender.events.Emit(ctx, Accepted, observed)
}

// submissionErrorClass uses a closed vocabulary, giving uncertain acceptance
// precedence over wrapped cancellation. Unknown provider errors become error.
func submissionErrorClass(err error) string {
	switch {
	case errors.Is(err, ErrOutcomeUnknown):
		return "outcome_unknown"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}

	for _, sentinel := range errorSentinels {
		if errors.Is(err, sentinel.Err) {
			return sentinel.Name
		}
	}

	return "error"
}
