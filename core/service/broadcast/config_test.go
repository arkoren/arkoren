package broadcast_test

import (
	"testing"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/core/service/telemetry/telemetrytest"
)

// TestRetainedPublisherPolicyCannotReconfigurePublishedFacade verifies detached
// telemetry settings while retaining the live publisher driver.
func TestRetainedPublisherPolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := broadcasttest.New()
	var retained *broadcast.PublisherBuilder
	publisher := broadcast.NewPublisher(driver, func(publisher *broadcast.PublisherBuilder) {
		retained = publisher
		publisher.Telemetry(fake.Telemetry(t))
		publisher.System("test-system")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.System("mutated")

	if err := publisher.Publish(t.Context(), topicString, "test"); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "send users.created", telemetry.String("messaging.system", "test-system"))
}

// TestRetainedConsumerPolicyCannotReconfigurePublishedFacade verifies consumer
// publication independently of publisher settings and runner lifecycle behavior.
func TestRetainedConsumerPolicyCannotReconfigurePublishedFacade(t *testing.T) {
	t.Parallel()

	fake := telemetrytest.New()
	driver := broadcasttest.New()
	var retained *broadcast.ConsumerBuilder
	consumer := broadcast.NewConsumer(driver, func(consumer *broadcast.ConsumerBuilder) {
		retained = consumer
		consumer.Telemetry(fake.Telemetry(t))
		consumer.System("test-system")
	})
	retained.Telemetry(telemetry.New(nil))
	retained.System("mutated")

	if err := broadcast.NewPublisher(driver).Publish(t.Context(), topicString, "test"); err != nil {
		t.Fatal(err)
	}

	if err := driver.Drain(t.Context(), consumer); err != nil {
		t.Fatal(err)
	}

	fake.AssertSpan(t, "process users.created", telemetry.String("messaging.system", "test-system"))
}

// TestPublisherPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestPublisherPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *broadcast.PublisherBuilder
	calls := 0
	component := broadcast.NewPublisher(nil, func(publisher *broadcast.PublisherBuilder) {
		calls++
		retained = publisher
	}, func(publisher *broadcast.PublisherBuilder) {
		if calls != 1 || retained != publisher {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	broadcast.NewPublisher(nil, func(publisher *broadcast.PublisherBuilder) {
		if retained == publisher {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestPublisherNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestPublisherNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*broadcast.PublisherBuilder)
	empty := []func(*broadcast.PublisherBuilder){}

	if broadcast.NewPublisher(nil, absent...) == nil || broadcast.NewPublisher(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestPublisherNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestPublisherNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		broadcast.NewPublisher(nil, nil, func(publisher *broadcast.PublisherBuilder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestPublisherPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestPublisherPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		broadcast.NewPublisher(nil, func(publisher *broadcast.PublisherBuilder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestPublisherTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestPublisherTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	publisher := &broadcast.PublisherBuilder{}

	if publisher.Telemetry(nil) != publisher {
		t.Fatal("setting returned a different draft")
	}
}

// TestPublisherSystemReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestPublisherSystemReturnsDraft(t *testing.T) {
	t.Parallel()

	publisher := &broadcast.PublisherBuilder{}

	if publisher.System("custom") != publisher {
		t.Fatal("setting returned a different draft")
	}
}

// TestConsumerPoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestConsumerPoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *broadcast.ConsumerBuilder
	calls := 0
	component := broadcast.NewConsumer(nil, func(consumer *broadcast.ConsumerBuilder) {
		calls++
		retained = consumer
	}, func(consumer *broadcast.ConsumerBuilder) {
		if calls != 1 || retained != consumer {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	broadcast.NewConsumer(nil, func(consumer *broadcast.ConsumerBuilder) {
		if retained == consumer {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestConsumerNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestConsumerNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*broadcast.ConsumerBuilder)
	empty := []func(*broadcast.ConsumerBuilder){}

	if broadcast.NewConsumer(nil, absent...) == nil || broadcast.NewConsumer(nil, empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestConsumerNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestConsumerNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		broadcast.NewConsumer(nil, nil, func(consumer *broadcast.ConsumerBuilder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestConsumerPolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestConsumerPolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		broadcast.NewConsumer(nil, func(consumer *broadcast.ConsumerBuilder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestConsumerTelemetryReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestConsumerTelemetryReturnsDraft(t *testing.T) {
	t.Parallel()

	consumer := &broadcast.ConsumerBuilder{}

	if consumer.Telemetry(nil) != consumer {
		t.Fatal("setting returned a different draft")
	}
}

// TestConsumerSystemReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestConsumerSystemReturnsDraft(t *testing.T) {
	t.Parallel()

	consumer := &broadcast.ConsumerBuilder{}

	if consumer.System("custom") != consumer {
		t.Fatal("setting returned a different draft")
	}
}

// TestConsumerEventsReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestConsumerEventsReturnsDraft(t *testing.T) {
	t.Parallel()

	consumer := &broadcast.ConsumerBuilder{}

	if consumer.Events(nil) != consumer {
		t.Fatal("setting returned a different draft")
	}
}
