package memoryqueue

import (
	"testing"
	"time"
)

// TestBuilderPreservesNegativeRetryDelay keeps the prior runtime retry policy;
// construction must not introduce new normalization or scheduling behavior.
func TestBuilderPreservesNegativeRetryDelay(t *testing.T) {
	t.Parallel()

	driver := New(func(consumer *Builder) {
		consumer.RetryDelay(-time.Second)
	})

	if driver.broker.RetryDelay != -time.Second {
		t.Fatalf("retry delay = %s", driver.broker.RetryDelay)
	}
}

// TestBuilderPoliciesPublishDetachedSettings verifies ordered policies and
// publication without starting a consumer or changing retry scheduling.
func TestBuilderPoliciesPublishDetachedSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	driver := New(func(consumer *Builder) {
		retained = consumer
		calls++

		if consumer.RetryDelay(time.Second) != consumer {
			t.Fatal("setting returned another draft")
		}
	}, func(consumer *Builder) {
		calls++

		if consumer != retained {
			t.Fatal("policies received different drafts")
		}

		consumer.RetryDelay(time.Minute)
	})
	retained.RetryDelay(time.Hour)

	if calls != 2 || driver.broker.RetryDelay != time.Minute {
		t.Fatalf("calls = %d, retry delay = %s", calls, driver.broker.RetryDelay)
	}
}

// TestMemoryQueuePoliciesRunOnceInOrder verifies synchronous callback identity and
// ordering without running a driver, publishing a message, or doing lifecycle I/O.
func TestMemoryQueuePoliciesRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	component := New(func(consumer *Builder) {
		calls++
		retained = consumer
	}, func(consumer *Builder) {
		if calls != 1 || retained != consumer {
			t.Fatal("construction policies did not share the ordered draft")
		}

		calls++
	})

	if calls != 2 || component == nil {
		t.Fatalf("callback count = %d, component = %v", calls, component)
	}

	New(func(consumer *Builder) {
		if retained == consumer {
			t.Fatal("independent constructors reused a draft")
		}
	})
}

// TestMemoryQueueNilExpansionOmitsPolicies preserves ordinary defaults for both nil
// and allocated empty callback slices. Neither supplies a nil callback entry.
func TestMemoryQueueNilExpansionOmitsPolicies(t *testing.T) {
	t.Parallel()

	var absent []func(*Builder)
	empty := []func(*Builder){}

	if New(absent...) == nil || New(empty...) == nil {
		t.Fatal("empty construction did not publish a component")
	}
}

// TestMemoryQueueNilPolicyPanics verifies that programmer misuse is not filtered and
// that later policies do not run after a supplied nil construction callback.
func TestMemoryQueueNilPolicyPanics(t *testing.T) {
	t.Parallel()

	called := false
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(nil, func(consumer *Builder) {
			called = true
		})
	}()

	if recovered == nil || called {
		t.Fatalf("panic = %v, later callback ran = %v", recovered, called)
	}
}

// TestMemoryQueuePolicyPanicPropagates preserves application panic identity and stops
// construction rather than recovering or converting the panic into a domain error.
func TestMemoryQueuePolicyPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := &struct{}{}
	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		New(func(consumer *Builder) {
			panic(marker)
		})
	}()

	if recovered != marker {
		t.Fatalf("panic = %v, want application marker", recovered)
	}
}

// TestMemoryQueueRetryDelayReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryQueueRetryDelayReturnsDraft(t *testing.T) {
	t.Parallel()

	queue := &Builder{}

	if queue.RetryDelay(-1) != queue {
		t.Fatal("setting returned a different draft")
	}
}

// TestMemoryQueueLoggerReturnsDraft verifies fluent settings preserve draft identity;
// settings are construction-only and never modify a published component.
func TestMemoryQueueLoggerReturnsDraft(t *testing.T) {
	t.Parallel()

	queue := &Builder{}

	if queue.Logger(nil) != queue {
		t.Fatal("setting returned a different draft")
	}
}
