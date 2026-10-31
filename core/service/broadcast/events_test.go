package broadcast_test

import (
	"context"
	"errors"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/broadcast"
)

// newEventConsumer returns a consumer that delivers one message and the
// failures published for it.
func newEventConsumer(t *testing.T, bus *event.Bus, payload string) (*broadcast.Consumer, *[]broadcast.ListenerFailedEvent) {
	t.Helper()

	var got []broadcast.ListenerFailedEvent

	if _, err := bus.Listen(broadcast.ListenerFailed, func(_ context.Context, payload broadcast.ListenerFailedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				ID:      "m-1",
				Name:    "users.created",
				Payload: []byte(payload),
			})
		},
	}, func(consumer *broadcast.ConsumerBuilder) {
		consumer.Events(bus)
	})

	return consumer, &got
}

// TestConsumerPublishesListenerFailed checks failure notifications identify the
// delivery and preserve the listener's error.
func TestConsumerPublishesListenerFailed(t *testing.T) {
	t.Parallel()

	consumer, got := newEventConsumer(t, event.New(), `1`)

	if _, err := consumer.Listen(topicInt, func(context.Context, int) error {
		return errHandler
	}); err != nil {
		t.Fatal(err)
	}

	_ = consumer.Run(t.Context()) // The delivery error is not under test.

	if len(*got) != 1 || (*got)[0].ID != "m-1" || (*got)[0].Name != "users.created" || !errors.Is((*got)[0].Err, errHandler) {
		t.Fatalf("ListenerFailed = %+v", *got)
	}
}

// TestConsumerPublishesListenerFailedForDecodeError checks incompatible JSON
// emits a terminal failure without invoking a successful listener.
func TestConsumerPublishesListenerFailedForDecodeError(t *testing.T) {
	t.Parallel()

	consumer, got := newEventConsumer(t, event.New(), `"not a number"`)

	if _, err := consumer.Listen(topicInt, func(context.Context, int) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_ = consumer.Run(t.Context()) // The delivery error is not under test.

	if len(*got) != 1 || !errors.Is((*got)[0].Err, broadcast.ErrPermanent) {
		t.Fatalf("ListenerFailed = %+v; want one permanent decode failure", *got)
	}
}

// TestConsumerPublishesOneListenerFailedPerFailingListener verifies successful
// listeners do not add notifications to a delivery's individual failures.
func TestConsumerPublishesOneListenerFailedPerFailingListener(t *testing.T) {
	t.Parallel()

	consumer, got := newEventConsumer(t, event.New(), `1`)

	for _, fail := range []bool{true, false, true} {
		if _, err := consumer.Listen(topicInt, func(context.Context, int) error {
			if fail {
				return errHandler
			}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	_ = consumer.Run(t.Context()) // The delivery error is not under test.

	if len(*got) != 2 {
		t.Fatalf("ListenerFailed = %+v; want two", *got)
	}
}

// TestConsumerPublishesNothingOnSuccess verifies a successful listener
// result does not emit a failure notification.
func TestConsumerPublishesNothingOnSuccess(t *testing.T) {
	t.Parallel()

	consumer, got := newEventConsumer(t, event.New(), `1`)

	if _, err := consumer.Listen(topicInt, func(context.Context, int) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(*got) != 0 {
		t.Fatalf("ListenerFailed = %+v; want none", *got)
	}
}

// TestConsumerWithoutBusPublishesNothing checks disabling events preserves the
// listener error without sending it to an unrelated bus.
func TestConsumerWithoutBusPublishesNothing(t *testing.T) {
	t.Parallel()

	bus := event.New()
	_, got := newEventConsumer(t, bus, `1`)
	consumer := broadcast.NewConsumer(consumer{
		consume: func(ctx context.Context, handler broadcast.Handler) error {
			return handler(ctx, broadcast.Message{
				Name:    "users.created",
				Payload: []byte(`1`),
			})
		},
	})

	if _, err := consumer.Listen(topicInt, func(context.Context, int) error {
		return errHandler
	}); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); !errors.Is(err, errHandler) {
		t.Fatalf("Run = %v", err)
	}

	if len(*got) != 0 {
		t.Fatalf("ListenerFailed = %+v; want none", *got)
	}
}
