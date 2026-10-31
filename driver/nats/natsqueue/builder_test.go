package natsqueue

import (
	"errors"
	"testing"

	"github.com/nats-io/nats.go"
)

// TestNewDetachesOwnedRoutingState checks input copies and publication copies
// while leaving SDK option functions unexecuted until connection startup.
func TestNewDetachesOwnedRoutingState(t *testing.T) {
	t.Parallel()

	queues := map[string]Queue{
		"work": {
			Subject: "jobs.work",
			Durable: "work",
		},
	}
	urls := []string{"nats://localhost:4222"}
	options := []nats.Option{nats.Name("worker")}
	var retained *Builder
	calls := 0

	driver, err := New("JOBS", queues, func(consumer *Builder) {
		retained = consumer
		calls++
		consumer.URLs(urls...)
		consumer.Options(options...)

		delete(queues, "work")
		urls[0] = "nats://localhost:4223"
		options[0] = nil
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.config.URLs[0] = "nats://localhost:4224"
	retained.config.Options[0] = nil

	if calls != 1 || len(driver.queues) != 1 {
		t.Fatal("callbacks replayed or queue map was retained")
	}

	if driver.config.URLs[0] != "nats://localhost:4222" || driver.config.Options[0] == nil {
		t.Fatal("owned connection slices were retained")
	}
}

// TestNewFromRunsCallbacksBeforeInvalidConnection checks error timing without
// requiring a NATS server or changing borrowed connection ownership.
func TestNewFromRunsCallbacksBeforeInvalidConnection(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := NewFrom(nil, "JOBS", nil, func(consumer *Builder) {
		calls++
	})

	if calls != 1 || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("callbacks = %d, error = %v", calls, err)
	}
}
