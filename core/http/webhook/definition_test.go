package webhook_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/webhook"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// invoiceDTO is an application-owned queue payload, independent of the local
// provider's invoice type. Its ID is distinct from the delivery's event ID.
type invoiceDTO struct {
	// ID identifies the invoice to update when the queued job is consumed.
	ID string `json:"id"`
}

// TestEventMapDecodesBeforeConversion checks output inference and sequencing:
// the converter receives the decoded payload, not the original envelope.
func TestEventMapDecodesBeforeConversion(t *testing.T) {
	t.Parallel()

	var order []string
	base := webhook.NewEvent("echo", func(envelope string) (invoice, error) {
		order = append(order, "decode")

		return invoice{Number: strings.ToUpper(envelope)}, nil
	})

	mapped := base.Map(func(payload invoice) (invoiceDTO, error) {
		order = append(order, "convert")

		return invoiceDTO{ID: payload.Number}, nil
	})

	var got webhook.Delivery[invoiceDTO]
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, func(_ context.Context, delivery webhook.Delivery[invoiceDTO]) error {
			got = delivery

			return nil
		})
	})

	if status := post(handler, "inv-1", "evt-1").StatusCode; status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if !slices.Equal(order, []string{"decode", "convert"}) {
		t.Fatalf("order = %v, want decode then convert", order)
	}

	if got != (webhook.Delivery[invoiceDTO]{ID: "evt-1", Type: "echo", Payload: invoiceDTO{ID: "INV-1"}}) {
		t.Fatalf("delivery = %+v, want inferred DTO and original metadata", got)
	}
}

// TestEventMapLeavesOriginalReusable checks mapping does not change the base
// decoder and that the mapped definition can be registered with two sources.
func TestEventMapLeavesOriginalReusable(t *testing.T) {
	t.Parallel()

	mapped := invoiceEvent.Map(func(payload invoice) (string, error) {
		return "mapped-" + payload.Number, nil
	})

	accepted := &recorder{}
	bindMapped := func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, accepted.accept)
	}

	first := newHandler(t, headerSource(), bindMapped)
	secondSource := webhook.NewSource(func(*http.Request, []byte) (webhook.Delivery[string], error) {
		return webhook.Delivery[string]{Type: "echo", Payload: "second"}, nil
	})

	second := newHandler(t, secondSource, bindMapped)
	post(first, "first", "")
	post(second, "unused", "")

	if !slices.Equal(accepted.events, []string{"mapped-first", "mapped-second"}) {
		t.Fatalf("payloads = %v, want one mapped event from each source", accepted.events)
	}

	var original invoice
	baseHandler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(invoiceEvent, func(_ context.Context, delivery webhook.Delivery[invoice]) error {
			original = delivery.Payload

			return nil
		})
	})
	post(baseHandler, "original", "")

	if original.Number != "original" || mapped.Key() != invoiceEvent.Key() {
		t.Fatalf("original = %+v mapped key = %q, want unchanged base", original, mapped.Key())
	}
}

// TestEventMapDecoderErrorStopsConversion checks the original decoding error
// keeps its identity and neither conversion nor acceptance runs afterward.
func TestEventMapDecoderErrorStopsConversion(t *testing.T) {
	t.Parallel()

	cause := errors.New("provider schema mismatch")
	base := webhook.NewEvent("echo", func(string) (invoice, error) {
		return invoice{}, cause
	})

	converted := false
	mapped := base.Map(func(invoice) (string, error) {
		converted = true

		return "unexpected", nil
	})

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, accepted.accept)
	})

	writer := httptest.NewRecorder()

	err := handler(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if !errors.Is(err, cause) || writer.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d error = %v, want 503 preserving decoder cause", writer.Code, err)
	}

	if converted || accepted.count() != 0 {
		t.Fatal("decoder error reached conversion or acceptance")
	}
}

// TestEventMapDecoderSkipStopsConversion checks the original decoder's skip
// still acknowledges before conversion and never reaches acceptance.
func TestEventMapDecoderSkipStopsConversion(t *testing.T) {
	t.Parallel()

	base := webhook.NewEvent("echo", func(string) (invoice, error) {
		return invoice{}, webhook.ErrSkip
	})

	converted := false
	mapped := base.Map(func(invoice) (string, error) {
		converted = true

		return "unexpected", nil
	})

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, accepted.accept)
	})

	if status := post(handler, "x", "").StatusCode; status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if converted || accepted.count() != 0 {
		t.Fatal("decoder skip reached conversion or acceptance")
	}
}

// TestEventMapConversionFailureIsDecodeFailure checks conversion errors are
// reported at the decode stage, preserve their cause, and never call accept.
func TestEventMapConversionFailureIsDecodeFailure(t *testing.T) {
	t.Parallel()

	cause := errors.New("unsupported invoice version")
	mapped := invoiceEvent.Map(func(invoice) (string, error) {
		return "", cause
	})

	bus := event.New()
	var failed webhook.FailedEvent

	if _, err := bus.Listen(webhook.Failed, func(_ context.Context, payload webhook.FailedEvent) error {
		failed = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, accepted.accept)
		receiver.Events(bus)
	})

	writer := httptest.NewRecorder()

	err := handler(writer, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))

	if !errors.Is(err, cause) || !errors.Is(err, webhook.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable preserving conversion cause", err)
	}

	if writer.Code != http.StatusServiceUnavailable || failed.Stage != webhook.FailureStageDecode {
		t.Fatalf("status = %d stage = %q, want 503 decode failure", writer.Code, failed.Stage)
	}

	if accepted.count() != 0 {
		t.Fatal("conversion error reached acceptance")
	}
}

// TestEventMapConversionSkipDoesNotRememberID checks conversion skips are
// preaccept acknowledgements, not successful accepts recorded by deduplication.
func TestEventMapConversionSkipDoesNotRememberID(t *testing.T) {
	t.Parallel()

	conversions := 0
	mapped := invoiceEvent.Map(func(invoice) (string, error) {
		conversions++

		return "", webhook.ErrSkip
	})

	accepted := &recorder{}
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, accepted.accept)
		receiver.Deduplicate(cache.New(memorycache.New()), cache.NewNamespace[bool]("billing"))
	})

	first := post(handler, "x", "evt-1").StatusCode
	second := post(handler, "x", "evt-1").StatusCode

	if first != http.StatusOK || second != http.StatusOK {
		t.Fatalf("statuses = %d %d, want two acknowledgements", first, second)
	}

	if conversions != 2 || accepted.count() != 0 {
		t.Fatalf("conversions = %d accepted = %d, want 2 and 0", conversions, accepted.count())
	}
}

// TestEventMapRejectsZeroBase checks mapping a zero definition is safe and
// registration reports the inherited invalidity rather than invoking code.
func TestEventMapRejectsZeroBase(t *testing.T) {
	t.Parallel()

	base := webhook.Event[string, string]{}
	mapped := base.Map(func(payload string) (int, error) {
		return len(payload), nil
	})

	if !errors.Is(mapped.Err(), webhook.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", mapped.Err())
	}

	_, err := webhook.Handler(headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, func(context.Context, webhook.Delivery[int]) error {
			return nil
		})
	})

	if !errors.Is(err, mapped.Err()) {
		t.Fatalf("registration error = %v, want inherited error %v", err, mapped.Err())
	}
}

// TestEventMapKeepsBaseErrorBeforeNilConverter checks a later invalid mapper
// cannot replace the first error already recorded by NewEvent.
func TestEventMapKeepsBaseErrorBeforeNilConverter(t *testing.T) {
	t.Parallel()

	base := webhook.NewEvent("", func(envelope string) (string, error) {
		return envelope, nil
	})

	mapped := base.Map[int](nil)

	if !errors.Is(mapped.Err(), base.Err()) {
		t.Fatalf("error = %v, want first base error %v", mapped.Err(), base.Err())
	}

	_, err := webhook.Handler(headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(mapped, func(context.Context, webhook.Delivery[int]) error {
			return nil
		})
	})

	if !errors.Is(err, base.Err()) {
		t.Fatalf("registration error = %v, want original base error", err)
	}
}

// TestEventMapRejectsNilConverterAndKeepsItsError checks mapping again cannot
// repair an invalid mapped definition or invalidate the original event.
func TestEventMapRejectsNilConverterAndKeepsItsError(t *testing.T) {
	t.Parallel()

	mapped := invoiceEvent.Map[string](nil)

	if !errors.Is(mapped.Err(), webhook.ErrInvalid) || !strings.Contains(mapped.Err().Error(), "convert") {
		t.Fatalf("error = %v, want nil converter diagnostic", mapped.Err())
	}

	again := mapped.Map(func(payload string) (int, error) {
		return len(payload), nil
	})

	if !errors.Is(again.Err(), mapped.Err()) || invoiceEvent.Err() != nil {
		t.Fatalf("mapped error = %v base error = %v, want sticky mapped error only", again.Err(), invoiceEvent.Err())
	}

	_, err := webhook.Handler(headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Handle(again, func(context.Context, webhook.Delivery[int]) error {
			return nil
		})
	})

	if !errors.Is(err, mapped.Err()) {
		t.Fatalf("registration error = %v, want nil converter error", err)
	}
}

// TestEventMapDispatchRoundTripKeepsMetadata checks a mapped application DTO
// travels through the fake queue's JSON encoding and consumption with ID/type.
func TestEventMapDispatchRoundTripKeepsMetadata(t *testing.T) {
	t.Parallel()

	mapped := invoiceEvent.Map(func(payload invoice) (invoiceDTO, error) {
		return invoiceDTO{ID: payload.Number}, nil
	})

	job := queue.NewJob[webhook.Delivery[invoiceDTO]]("invoice-paid")
	fake := queuetest.New()
	handler := newHandler(t, headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Dispatch(mapped, queue.NewDispatcher(fake), job)
	})

	if status := post(handler, "INV-1", "evt-1").StatusCode; status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	fake.AssertDispatchedCount(t, job, 1)

	consumer := queue.NewConsumer(fake)
	var got webhook.Delivery[invoiceDTO]

	if err := consumer.Handle(job, func(_ context.Context, delivery webhook.Delivery[invoiceDTO]) error {
		got = delivery

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := fake.Drain(t.Context(), consumer); err != nil {
		t.Fatal(err)
	}

	if got != (webhook.Delivery[invoiceDTO]{ID: "evt-1", Type: "echo", Payload: invoiceDTO{ID: "INV-1"}}) {
		t.Fatalf("consumed = %+v, want application DTO and original metadata", got)
	}
}

// TestEventMapDispatchRejectsInvalidJob checks mapping retains Dispatch's
// construction-time validation of the destination job before any request.
func TestEventMapDispatchRejectsInvalidJob(t *testing.T) {
	t.Parallel()

	mapped := invoiceEvent.Map(func(payload invoice) (invoiceDTO, error) {
		return invoiceDTO{ID: payload.Number}, nil
	})

	_, err := webhook.Handler(headerSource(), func(receiver *webhook.Builder[string]) {
		receiver.Dispatch(mapped, queue.NewDispatcher(queuetest.New()), queue.Job[webhook.Delivery[invoiceDTO]]{})
	})

	if !errors.Is(err, webhook.ErrInvalid) || !strings.Contains(err.Error(), "job name") {
		t.Fatalf("error = %v, want invalid webhook configuration identifying the job", err)
	}
}
