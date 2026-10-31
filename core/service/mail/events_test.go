package mail_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/mail"
	"arkoren.dev/core/service/mail/mailtest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// eventMessage supplies a valid message with two recipients for event counts.
var eventMessage = mail.NewMessage(mail.Recipients{
	To: []mail.Address{{Email: "person@example.com"}},
	CC: []mail.Address{{Email: "copy@example.com"}},
}, mail.Body{Text: "Hello."}, func(letter *mail.MessageBuilder) {
	letter.From(mail.Address{Email: "noreply@example.com"})
})

// TestSendPublishesSent verifies an accepted message publishes Sent.
func TestSendPublishesSent(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got mail.SentEvent

	if _, err := bus.Listen(mail.Sent, func(_ context.Context, payload mail.SentEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	receipt, err := mailer.Send(context.Background(), eventMessage)

	if err != nil {
		t.Fatal(err)
	}

	if got.Receipt != receipt || got.Recipients != 2 || got.Attachments != 0 {
		t.Fatalf("sent = %+v, want receipt %+v and 2 recipients", got, receipt)
	}
}

// TestSendPublishesFailed verifies a rejected message publishes Failed.
func TestSendPublishesFailed(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got mail.FailedEvent

	if _, err := bus.Listen(mail.Failed, func(_ context.Context, payload mail.FailedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	driver := mailtest.New()
	rejected := errors.New("rejected")
	driver.FailNext(rejected)

	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	if _, err := mailer.Send(context.Background(), eventMessage); !errors.Is(err, rejected) {
		t.Fatalf("send error = %v, want rejected", err)
	}

	if got.ErrorClass != "error" || got.Recipients != 2 {
		t.Fatalf("failed = %+v, want a bounded rejection and 2 recipients", got)
	}
}

// TestSendListenerErrorKeepsOutcome verifies a failing listener never fails
// the send.
func TestSendListenerErrorKeepsOutcome(t *testing.T) {
	t.Parallel()

	bus := event.New()

	if _, err := bus.Listen(mail.Sent, func(context.Context, mail.SentEvent) error {
		return errors.New("listener broke")
	}); err != nil {
		t.Fatal(err)
	}

	mailer := mail.New(mailtest.New(), func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	if _, err := mailer.Send(context.Background(), eventMessage); err != nil {
		t.Fatalf("send error = %v, want nil", err)
	}
}

// TestSendFailureEventRedactsProviderDiagnostics keeps rejected BCC addresses
// and arbitrary provider text out of notifications without losing the API cause.
func TestSendFailureEventRedactsProviderDiagnostics(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed mail.FailedEvent

	stop, err := bus.Listen(mail.Failed, func(_ context.Context, payload mail.FailedEvent) error {
		observed = payload

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	driver := mailtest.New()
	cause := errors.New("private-provider-diagnostic")
	driver.FailNext(fmt.Errorf("set smtp recipient hidden-private@example.com: %w", cause))

	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	message := mail.NewMessage(mail.Recipients{
		BCC: []mail.Address{{Email: "hidden-private@example.com"}},
	}, mail.Body{Text: "private-body"}, func(letter *mail.MessageBuilder) {
		letter.From(mail.Address{Email: "sender@example.com"})
	})

	_, err = mailer.Send(t.Context(), message)

	if !errors.Is(err, cause) {
		t.Fatal("Send lost the original provider cause")
	}

	if observed.Recipients != 1 {
		t.Fatalf("failure recipient count = %d, want 1", observed.Recipients)
	}

	if strings.Contains(fmt.Sprint(observed), "private") {
		t.Fatal("failure event disclosed recipient or provider data")
	}
}

// TestSendFailureEventClassifiesCancellation preserves a joined native cause
// for the caller while publishing only the fixed context cancellation class.
func TestSendFailureEventClassifiesCancellation(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed mail.FailedEvent

	stop, err := bus.Listen(mail.Failed, func(_ context.Context, payload mail.FailedEvent) error {
		observed = payload

		return errors.New("listener cannot replace the provider result")
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	driver := mailtest.New()
	cause := errors.New("private-cancellation-diagnostic")
	driver.FailNext(errors.Join(context.Canceled, mail.ErrInvalidMessage, cause))

	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	_, err = mailer.Send(t.Context(), eventMessage)

	for _, target := range []error{context.Canceled, mail.ErrInvalidMessage, cause} {
		if !errors.Is(err, target) {
			t.Fatalf("Send lost cause %v", target)
		}
	}

	if observed.ErrorClass != "context.Canceled" {
		t.Fatalf("failure class = %q, want context.Canceled", observed.ErrorClass)
	}
}

// TestSendFailureEventClassifiesDeadline keeps expiration diagnostics private
// while retaining the provider's deadline identity in the returned error.
func TestSendFailureEventClassifiesDeadline(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var observed mail.FailedEvent

	stop, err := bus.Listen(mail.Failed, func(_ context.Context, payload mail.FailedEvent) error {
		observed = payload

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stop)

	driver := mailtest.New()
	driver.FailNext(fmt.Errorf("private-deadline-diagnostic: %w", context.DeadlineExceeded))

	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(bus)
	})

	_, err = mailer.Send(t.Context(), eventMessage)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Send lost the deadline cause")
	}

	if observed.ErrorClass != "context.DeadlineExceeded" {
		t.Fatalf("failure class = %q, want context.DeadlineExceeded", observed.ErrorClass)
	}
}

// panicOutcomeDriver records the completed message before panicking, so tests
// can distinguish a driver panic from validation failure without sending mail.
// Tests use each driver synchronously; its history is not concurrency-safe.
type panicOutcomeDriver struct {
	// messages retains the immutable messages passed to Send in call order.
	messages []mail.Message

	// value is propagated unchanged, including when it contains private data.
	value any
}

// Send validates and records message before raising the configured panic.
// It never returns an acceptance receipt for a valid message.
func (driver *panicOutcomeDriver) Send(ctx context.Context, message mail.Message) (mail.Receipt, error) {
	if err := mail.CheckSend(ctx, message); err != nil {
		return mail.Receipt{}, err
	}

	driver.messages = append(driver.messages, message)
	panic(driver.value)
}

// panicOutcomeEvents records both public mail outcome topics synchronously.
// Each test owns its bus and subscriptions, avoiding shared event state.
type panicOutcomeEvents struct {
	// bus is the shared service supplied to the test's mailer.
	bus *event.Bus

	// sent retains all acceptance notifications, including duplicate emissions.
	sent []mail.SentEvent

	// failed retains all returned-error notifications in emission order.
	failed []mail.FailedEvent
}

// newPanicOutcomeEvents subscribes to both outcomes using only the public bus
// API. Registration errors fail the test; cleanup removes both subscriptions.
func newPanicOutcomeEvents(t *testing.T) *panicOutcomeEvents {
	t.Helper()

	events := &panicOutcomeEvents{bus: event.New()}

	unsubscribe, err := events.bus.Listen(mail.Sent, func(_ context.Context, payload mail.SentEvent) error {
		events.sent = append(events.sent, payload)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(unsubscribe)

	unsubscribe, err = events.bus.Listen(mail.Failed, func(_ context.Context, payload mail.FailedEvent) error {
		events.failed = append(events.failed, payload)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(unsubscribe)

	return events
}

// captureSendPanic recovers only at the test's caller boundary and returns the
// original panic value. A normal Send return yields nil; returned errors are
// ignored because these callers require a panic and assert its identity.
func captureSendPanic(ctx context.Context, mailer *mail.Mailer, message mail.Message) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	_, _ = mailer.Send(ctx, message)

	return nil
}

// TestSendPanicDoesNotPublishOutcome proves that reaching a driver is not
// acceptance: a panic propagates unchanged without Sent or Failed.
func TestSendPanicDoesNotPublishOutcome(t *testing.T) {
	t.Parallel()

	events := newPanicOutcomeEvents(t)
	marker := "private mail panic value"
	driver := &panicOutcomeDriver{value: &marker}
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(events.bus)
		mailer.Telemetry(telemetry.New(nil))
		mailer.From(mail.Address{Email: "sender@example.com"})
	})

	message := mail.NewMessage(mail.Recipients{
		To: []mail.Address{{Email: "recipient@example.com"}},
	}, mail.Body{Text: "Hello."})

	if got := captureSendPanic(t.Context(), mailer, message); got != &marker {
		t.Fatalf("panic = %v, want original marker", got)
	}

	if len(driver.messages) != 1 {
		t.Fatalf("driver calls = %d, want 1", len(driver.messages))
	}

	if driver.messages[0].From().Email != "sender@example.com" {
		t.Fatal("driver did not receive the completed sender default")
	}

	if len(events.sent) != 0 || len(events.failed) != 0 {
		t.Fatalf("sent = %d, failed = %d, want no outcome", len(events.sent), len(events.failed))
	}
}

// TestSendPanicFinishesOwnedTelemetry keeps the attempt span and metrics
// complete without ending the caller's span or recording private panic data.
func TestSendPanicFinishesOwnedTelemetry(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	instrumentation := fake.Telemetry(t)
	ctx, parent := instrumentation.Tracer("mail-regression").Start(t.Context(), "caller")
	t.Cleanup(parent.End)

	marker := "private panic body recipient@example.com"
	driver := &panicOutcomeDriver{value: &marker}
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Telemetry(instrumentation)
	})

	if got := captureSendPanic(ctx, mailer, eventMessage); got != &marker {
		t.Fatalf("panic = %v, want original marker", got)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertCounter(t, "arkoren.mail.operations", 1, telemetry.String("arkoren.mail.operation", "send"))
	fake.AssertHistogram(t, "arkoren.mail.operation.duration", 1, telemetry.String("arkoren.mail.operation", "send"))

	span, found := fake.Span("mail send")

	if !found {
		t.Fatal("mail send span was not finished")
	}

	if span.Parent != parent.Context() || !parent.IsRecording() {
		t.Fatal("send did not preserve the active caller span")
	}

	if span.Status != telemetry.Unset || span.StatusDescription != "" {
		t.Fatalf("panic span status = %v %q, want unset", span.Status, span.StatusDescription)
	}

	if len(span.Events) != 0 || len(fake.Logs()) != 0 {
		t.Fatal("panic produced exception events or logs")
	}

	if strings.Contains(fmt.Sprint(fake.Spans(), fake.Metrics(), fake.Logs()), marker) {
		t.Fatal("telemetry contains private panic data")
	}
}

// TestSendValidationFailureStillPublishesOutcome covers the early-return path
// before driver invocation, including error identity and telemetry completion.
func TestSendValidationFailureStillPublishesOutcome(t *testing.T) {
	t.Parallel()

	events := newPanicOutcomeEvents(t)
	fake := telemetrytest.New()
	driver := mailtest.New()
	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(events.bus)
		mailer.Telemetry(fake.Telemetry(t))
	})

	_, err := mailer.Send(t.Context(), mail.Message{})

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("send error = %v, want invalid message", err)
	}

	driver.AssertNothingSent(t)

	if len(events.sent) != 0 || len(events.failed) != 1 {
		t.Fatalf("sent = %d, failed = %d, want one failure", len(events.sent), len(events.failed))
	}

	if events.failed[0].ErrorClass != "mail.ErrInvalidMessage" {
		t.Fatalf("event class = %q, want invalid message", events.failed[0].ErrorClass)
	}

	fake.AssertSpanCount(t, 1)
	fake.AssertCounter(t, "arkoren.mail.operations", 1, telemetry.String("error.type", "mail.ErrInvalidMessage"))
	fake.AssertHistogram(t, "arkoren.mail.operation.duration", 1, telemetry.String("error.type", "mail.ErrInvalidMessage"))
}

// TestSendMissingDriverStillPublishesOutcome covers the facade's other early
// rejection, which must remain a returned failure rather than a panic outcome.
func TestSendMissingDriverStillPublishesOutcome(t *testing.T) {
	t.Parallel()

	events := newPanicOutcomeEvents(t)
	mailer := mail.New(nil, func(mailer *mail.Builder) {
		mailer.Events(events.bus)
		mailer.Telemetry(telemetry.New(nil))
	})

	_, err := mailer.Send(t.Context(), eventMessage)

	if !errors.Is(err, mail.ErrInvalidMessage) {
		t.Fatalf("send error = %v, want invalid message", err)
	}

	if len(events.sent) != 0 || len(events.failed) != 1 {
		t.Fatalf("sent = %d, failed = %d, want one failure", len(events.sent), len(events.failed))
	}

	if events.failed[0].ErrorClass != "mail.ErrInvalidMessage" {
		t.Fatalf("event class = %q, want invalid message", events.failed[0].ErrorClass)
	}
}

// ExampleFailedEvent demonstrates privacy-safe observations while the caller
// retains the original failure for application-owned handling.
func ExampleFailedEvent() {
	bus := event.New()

	stop, err := bus.Listen(mail.Failed, func(_ context.Context, observed mail.FailedEvent) error {
		fmt.Println(observed.ErrorClass, observed.Recipients)

		return nil
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	defer stop()
	driver := mailtest.New()
	cause := errors.New("private SMTP recipient diagnostic")
	driver.FailNext(cause)

	mailer := mail.New(driver, func(mailer *mail.Builder) {
		mailer.Events(bus)
		mailer.From(mail.Address{Email: "sender@example.com"})
	})

	message := mail.NewMessage(mail.Recipients{
		BCC: []mail.Address{{Email: "hidden@example.com"}},
	}, mail.Body{Text: "Hello."})

	_, err = mailer.Send(context.Background(), message)
	fmt.Println(errors.Is(err, cause))
	// Output:
	// error 1
	// true
}
