package queue_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestComponentPublicationDetachesTelemetryAndSystem checks root replacement
// and setter mutation cannot redirect either published component's telemetry.
func TestComponentPublicationDetachesTelemetryAndSystem(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	service := fake.Telemetry(t)
	driver := queuetest.New()
	var dispatcherDraft *queue.DispatcherBuilder
	dispatcher := queue.NewDispatcher(driver, func(dispatcher *queue.DispatcherBuilder) {
		dispatcherDraft = dispatcher
		dispatcher.Telemetry(service).System("custom")
	})

	var consumerDraft *queue.ConsumerBuilder
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumerDraft = consumer
		consumer.Telemetry(service).System("custom")
	})

	*dispatcherDraft = queue.DispatcherBuilder{}
	consumerDraft.Telemetry(nil).System("changed")

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.Dispatch(t.Context(), emailJob, email{}); err != nil {
		t.Fatal(err)
	}

	if err := driver.Drain(t.Context(), consumer); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send emails", telemetry.String("messaging.system", "custom"))
	fake.AssertSpan(t, "process emails", telemetry.String("messaging.system", "custom"))
}

// TestConsumerQueuesSnapshotsInput checks capture when the setting is called,
// including mutation of the caller's slice before constructor publication.
func TestConsumerQueuesSnapshotsInput(t *testing.T) {
	t.Parallel()

	queues := []string{"emails"}
	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues(queues...)

		queues[0] = "changed"
	})

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumerQueuesPublicationDetachesDraft checks both root replacement and
// queue replacement on a retained draft after the consumer is published.
func TestConsumerQueuesPublicationDetachesDraft(t *testing.T) {
	t.Parallel()

	var draft *queue.ConsumerBuilder
	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		draft = consumer
		consumer.Queues("emails")
	})
	draft.Queues("absent")

	*draft = queue.ConsumerBuilder{}

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumersReusePolicyIndependently replaces retired option-reuse coverage
// with the equivalent reusable callback and a local override on only one draft.
func TestConsumersReusePolicyIndependently(t *testing.T) {
	t.Parallel()

	policy := func(consumer *queue.ConsumerBuilder) {
		consumer.Queues("emails")
	}
	queue.NewConsumer(&recordingDriver{}, policy, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues("changed")
	})

	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, policy)

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumersDetachInputAndRetainedDrafts checks publication across repeated
// applications of a policy that captures one caller-owned slice.
func TestConsumersDetachInputAndRetainedDrafts(t *testing.T) {
	t.Parallel()

	var drafts []*queue.ConsumerBuilder
	queues := []string{"emails"}
	policy := func(consumer *queue.ConsumerBuilder) {
		consumer.Queues(queues...)

		drafts = append(drafts, consumer)
	}

	firstDriver := &recordingDriver{
		failQueue: "emails",
	}
	first := queue.NewConsumer(firstDriver, policy)
	secondDriver := &recordingDriver{
		failQueue: "emails",
	}
	second := queue.NewConsumer(secondDriver, policy)
	queues[0] = "changed"
	drafts[0].Queues("changed")

	*drafts[1] = queue.ConsumerBuilder{}

	if drafts[0] == drafts[1] {
		t.Fatal("constructors shared a draft")
	}

	checkConsumerQueue(t, first, firstDriver)
	checkConsumerQueue(t, second, secondDriver)
}

// TestConsumerCallbacksShareDraftInOrder checks direct, once-only invocation
// and replacement by the final callback rather than an accumulated restriction.
func TestConsumerCallbacksShareDraftInOrder(t *testing.T) {
	t.Parallel()

	var draft *queue.ConsumerBuilder
	var calls []int
	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		draft = consumer
		calls = append(calls, 1)
		consumer.Queues("discarded")
	}, func(consumer *queue.ConsumerBuilder) {
		if consumer != draft {
			t.Fatal("callbacks received different drafts")
		}

		calls = append(calls, 2)
		consumer.Queues("emails")
	})

	if !slices.Equal(calls, []int{1, 2}) {
		t.Fatalf("callback order = %v", calls)
	}

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumerEmptyQueuesRemovesRestriction checks that an empty setting
// replaces earlier restrictions, preserving the consume-all default.
func TestConsumerEmptyQueuesRemovesRestriction(t *testing.T) {
	t.Parallel()

	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues("absent").Queues()
	})

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumerEmptyPolicyExpansions checks nil and empty callback slices are
// omission, not a supplied nil callback. Neither changes default consumption.
func TestConsumerEmptyPolicyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*queue.ConsumerBuilder)
	driver := &recordingDriver{
		failQueue: "emails",
	}
	consumer := queue.NewConsumer(driver, omitted...)

	checkConsumerQueue(t, consumer, driver)

	driver = &recordingDriver{
		failQueue: "emails",
	}
	consumer = queue.NewConsumer(driver, []func(*queue.ConsumerBuilder){}...)

	checkConsumerQueue(t, consumer, driver)
}

// TestConsumerExplicitDefaultsReplaceEarlierPolicy replaces retired whole-config
// reset coverage with explicit default settings on the role-specific draft.
func TestConsumerExplicitDefaultsReplaceEarlierPolicy(t *testing.T) {
	t.Parallel()

	delivery := &jobDelivery{
		message: queue.Message{
			Name:     "unknown",
			Attempts: 1,
		},
	}
	consumer := queue.NewConsumer(jobConsumerDriver{
		delivery: delivery,
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues("absent").UnknownNameAttempts(1)
	}, func(consumer *queue.ConsumerBuilder) {
		consumer.Queues().UnknownNameAttempts(0)
	})

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !delivery.retried || delivery.retryDelay != 30*time.Second {
		t.Fatalf("settlement = %+v", delivery)
	}
}

// TestConsumerBuilderMethodsReturnReceiver checks fluent settings are edits
// on the same draft, including explicit default restoration.
func TestConsumerBuilderMethodsReturnReceiver(t *testing.T) {
	t.Parallel()

	queue.NewConsumer(&recordingDriver{}, func(consumer *queue.ConsumerBuilder) {
		if consumer.Telemetry(nil).System("").Queues().UnknownNameAttempts(0).Events(nil) != consumer {
			t.Fatal("setting returned a different receiver")
		}
	})
}

// TestConsumerNilCallbackPanics checks that a supplied nil callback is called
// directly instead of being ignored or reported as a domain validation error.
func TestConsumerNilCallbackPanics(t *testing.T) {
	t.Parallel()

	checkConstructionPanic(t, func() {
		queue.NewConsumer(&recordingDriver{}, nil)
	})
}

// TestDispatcherNilCallbackPanics preserves the same callback contract for
// dispatch-only construction, which has no consumer-only settings.
func TestDispatcherNilCallbackPanics(t *testing.T) {
	t.Parallel()

	checkConstructionPanic(t, func() {
		queue.NewDispatcher(discardDriver{}, nil)
	})
}

// TestDispatcherCallbacksShareFreshDrafts checks order, exact invocation count,
// receiver identity, zero callbacks and retained root independence.
func TestDispatcherCallbacksShareFreshDrafts(t *testing.T) {
	t.Parallel()

	var draft *queue.DispatcherBuilder
	var calls []int
	queue.NewDispatcher(discardDriver{}, func(dispatcher *queue.DispatcherBuilder) {
		draft = dispatcher
		calls = append(calls, 1)

		if dispatcher.Telemetry(nil).System("custom") != dispatcher {
			t.Fatal("setting returned a different receiver")
		}
	}, func(dispatcher *queue.DispatcherBuilder) {
		if dispatcher != draft {
			t.Fatal("callbacks received different drafts")
		}

		calls = append(calls, 2)
	})
	queue.NewDispatcher(discardDriver{}, func(dispatcher *queue.DispatcherBuilder) {
		if dispatcher == draft {
			t.Fatal("constructors shared a draft")
		}
	})
	var omitted []func(*queue.DispatcherBuilder)
	queue.NewDispatcher(discardDriver{}, omitted...)
	queue.NewDispatcher(discardDriver{}, []func(*queue.DispatcherBuilder){}...)

	if !slices.Equal(calls, []int{1, 2}) {
		t.Fatalf("callback order = %v", calls)
	}
}

// checkConstructionPanic distinguishes callback misuse from ordinary domain
// errors. The callback is invoked synchronously and must panic in this goroutine.
func checkConstructionPanic(t *testing.T, construct func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Error("constructor did not propagate callback panic")
		}
	}()
	construct()
}

// checkConsumerQueue verifies the consumer reaches its failing driver without
// a clock wait; a changed restriction yields ErrNoQueues instead.
func checkConsumerQueue(t *testing.T, consumer *queue.Consumer, driver *recordingDriver) {
	t.Helper()

	if err := consumer.Handle(emailJob, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	if err := consumer.Run(context.Background()); errors.Is(err, queue.ErrNoQueues) || err == nil {
		t.Fatalf("Run error = %v, want driver failure", err)
	}

	if !slices.Equal(driver.queues, []string{"emails"}) {
		t.Fatalf("consumed queues = %v, want [emails]", driver.queues)
	}
}
