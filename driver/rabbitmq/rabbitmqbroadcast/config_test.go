package rabbitmqbroadcast

import (
	"errors"
	"reflect"
	"testing"
)

// TestNewPublishesDetachedExchange checks callbacks are applied once and the
// construction root cannot later redirect publications to another exchange.
func TestNewPublishesDetachedExchange(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	driver, err := New("amqp://localhost:5672/", func(broker *Builder) {
		retained = broker
		calls++
		broker.Exchange("billing.events")
	})

	if err != nil {
		t.Fatal(err)
	}

	retained.Exchange("shipping.events")

	if calls != 1 || driver.exchange != "billing.events" {
		t.Fatal("construction replayed callbacks or retained its draft")
	}
}

// TestNewRunsCallbacksBeforeInvalidURL checks validation does not skip supplied
// construction policies even when the required URL is missing.
func TestNewRunsCallbacksBeforeInvalidURL(t *testing.T) {
	t.Parallel()

	calls := 0

	_, err := New(" ", func(broker *Builder) {
		calls++
	})

	if calls != 1 || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("callbacks = %d, error = %v", calls, err)
	}
}

// TestBuilderMethodsReturnReceiver checks every setting's mutable-draft contract.
// Zero arguments are used only to check method shape, not domain validity.
func TestBuilderMethodsReturnReceiver(t *testing.T) {
	t.Parallel()

	broadcast := &Builder{}
	receiver := reflect.ValueOf(broadcast)

	for index := range receiver.NumMethod() {
		method := receiver.Method(index)
		argument := reflect.Zero(method.Type().In(0))
		var result []reflect.Value

		if method.Type().IsVariadic() {
			result = method.CallSlice([]reflect.Value{argument})
		} else {
			result = method.Call([]reflect.Value{argument})
		}

		if result[0].Interface() != broadcast {
			t.Fatal("setting returned another draft")
		}
	}
}

// TestBuilderCallbacksShareOneFreshDraft verifies ordered, once-only execution
// and that a later construction starts from independent defaults.
func TestBuilderCallbacksShareOneFreshDraft(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0

	configureDriver(func(broker *Builder) {
		retained = broker
		calls++
	}, func(broker *Builder) {
		if broker != retained || calls != 1 {
			t.Fatal("callbacks did not share an ordered draft")
		}

		calls++
	})

	if calls != 2 {
		t.Fatalf("callbacks ran %d times", calls)
	}

	configureDriver(func(broker *Builder) {
		if broker == retained {
			t.Fatal("construction reused the previous draft")
		}
	})
}

// TestBuilderEmptyCallbacksPreserveDefaults distinguishes omitted callbacks
// from an explicitly supplied nil entry, which is programmer misuse.
func TestBuilderEmptyCallbacksPreserveDefaults(t *testing.T) {
	t.Parallel()

	var omitted []func(*Builder)
	expected := defaultConfig()

	if got := configureDriver(omitted...); !reflect.DeepEqual(got, expected) {
		t.Fatalf("nil slice changed defaults: %#v", got)
	}

	if got := configureDriver([]func(*Builder){}...); !reflect.DeepEqual(got, expected) {
		t.Fatalf("empty slice changed defaults: %#v", got)
	}
}

// TestBuilderNilCallbackPanics verifies misuse is not filtered or converted
// into a configuration error.
func TestBuilderNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = New("", nil)
}

// TestBuilderCallbackPanicPropagates preserves the exact application panic.
func TestBuilderCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	marker := new(int)
	defer func() {
		if got := recover(); got != marker {
			t.Fatalf("panic = %v, want application marker", got)
		}
	}()

	_, _ = New("", func(broker *Builder) {
		panic(marker)
	})
}

// TestNewFromNilCallbackPanics verifies borrowed-client construction does not
// silently discard supplied nil policies, even for a nil native client.
func TestNewFromNilCallbackPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil construction callback did not panic")
		}
	}()

	_, _ = NewFrom(nil, nil)
}
