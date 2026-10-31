package push_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/push"
	"arkoren.dev/core/service/push/pushtest"
)

// TestSubmissionEventsSnapshotConstruction proves once-only ordered settings
// and retained-draft isolation while preserving the accepted driver submission.
func TestSubmissionEventsSnapshotConstruction(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []push.SubmissionEvent

	_, err := events.Listen(push.Accepted, func(_ context.Context, payload push.SubmissionEvent) error {
		observed = append(observed, payload)

		return errors.New("listener cannot replace acceptance")
	})

	if err != nil {
		t.Fatal(err)
	}

	fake := pushtest.New()
	var retained *push.Builder
	calls := 0
	sender := push.New(fake, func(delivery *push.Builder) {
		calls++
		retained = delivery
		delivery.Events(nil)
	}, func(delivery *push.Builder) {
		calls++
		delivery.Events(events)
	})
	retained.Events(nil)

	_, err = sender.Send(t.Context(), push.NewMessage(push.Token("private-token"), "private-title"))

	if err != nil {
		t.Fatal(err)
	}

	fake.AssertSentCount(t, 1)

	if calls != 2 || len(observed) != 1 {
		t.Fatalf("callbacks = %d, events = %v", calls, observed)
	}

	if observed[0].ErrorClass != "" || observed[0].Duration < 0 {
		t.Fatalf("acceptance = %+v", observed[0])
	}
}

// TestSubmissionUnknownRedactsNativeData keeps uncertainty ahead of cancellation
// while preserving all returned causes, and excludes recipient/content/receipt.
func TestSubmissionUnknownRedactsNativeData(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed []push.SubmissionEvent

	_, err := events.Listen(push.Failed, func(_ context.Context, payload push.SubmissionEvent) error {
		observed = append(observed, payload)
		panic("listener cannot replace error")
	})

	if err != nil {
		t.Fatal(err)
	}

	cause := errors.New("private-endpoint private-key")
	fake := pushtest.New()
	fake.FailNext(errors.Join(push.ErrOutcomeUnknown, cause, context.Canceled))

	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Events(events)
	})

	message := push.NewMessage(push.Token("private-token"), "private-title", func(notification *push.MessageBuilder) {
		notification.Body("private-body").Data(map[string]string{"secret": "private-data"})
	})

	receipt, err := sender.Send(t.Context(), message)

	if !errors.Is(err, cause) || !errors.Is(err, push.ErrOutcomeUnknown) {
		t.Fatalf("returned error = %v", err)
	}

	if receipt != (push.Receipt{}) || len(observed) != 1 {
		t.Fatalf("receipt = %v, events = %v", receipt, observed)
	}

	if observed[0].ErrorClass != "outcome_unknown" || strings.Contains(fmt.Sprint(observed), "private-") {
		t.Fatalf("unsafe event = %+v", observed)
	}

	fake.AssertNothingSent(t)
}

// TestSubmissionNativeFailureUsesClosedClass verifies arbitrary native error
// text cannot become event metadata and the original cause remains available.
func TestSubmissionNativeFailureUsesClosedClass(t *testing.T) {
	t.Parallel()

	events := event.New()
	var observed push.SubmissionEvent

	_, err := events.Listen(push.Failed, func(_ context.Context, payload push.SubmissionEvent) error {
		observed = payload

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	fake := pushtest.New()
	cause := errors.New("private provider response")
	fake.FailNext(cause)

	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Events(events)
	})

	_, err = sender.Send(t.Context(), push.NewMessage(push.Token("token"), "Ready"))

	if !errors.Is(err, cause) || observed.ErrorClass != "error" {
		t.Fatalf("error = %v, event = %+v", err, observed)
	}
}

// TestSubmissionNilDisablesAndInvalidInputEmitsNothing covers explicit disabling
// and portable preflight without confusing rejected input with driver activity.
func TestSubmissionNilDisablesAndInvalidInputEmitsNothing(t *testing.T) {
	t.Parallel()

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^push\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	fake := pushtest.New()
	sender := push.New(fake, func(delivery *push.Builder) {
		delivery.Events(events).Events(nil)
	})

	if _, err := sender.Send(t.Context(), push.NewMessage(push.Token("token"), "Ready")); err != nil {
		t.Fatal(err)
	}

	sender = push.New(fake, func(delivery *push.Builder) {
		delivery.Events(events)
	})

	if _, err := sender.Send(t.Context(), push.Message{}); !errors.Is(err, push.ErrInvalidMessage) {
		t.Fatal(err)
	}

	if count != 0 {
		t.Fatalf("unexpected events = %d", count)
	}

	fake.AssertSentCount(t, 1)
}

// panickingSender models a provider panic without turning it into a returned
// failure; the facade must propagate the panic without publishing any outcome.
type panickingSender struct{}

// Send panics before returning any receipt or error to the facade.
func (panickingSender) Send(context.Context, push.Message, push.SendConfig) (push.Receipt, error) {
	panic("push driver panic")
}

// TestSubmissionDriverPanicEmitsNothing guards against interpreting the named
// return error's initial nil value as a successful provider submission.
func TestSubmissionDriverPanicEmitsNothing(t *testing.T) {
	t.Parallel()

	events := event.New()
	count := 0

	_, err := events.ListenRegex(regexp.MustCompile(`^push\.`), func(context.Context, string, any) error {
		count++

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	sender := push.New(panickingSender{}, func(delivery *push.Builder) {
		delivery.Events(events)
	})
	defer func() {
		if recovered := recover(); recovered != "push driver panic" {
			t.Fatalf("panic = %v", recovered)
		}

		if count != 0 {
			t.Fatalf("events after driver panic = %d", count)
		}
	}()
	_, _ = sender.Send(t.Context(), push.NewMessage(push.Token("token"), "Ready"))
}
