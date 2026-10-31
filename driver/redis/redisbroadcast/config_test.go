package redisbroadcast

import (
	"crypto/tls"
	"log/slog"
	"reflect"
	"testing"

	"github.com/redis/go-redis/v9"
)

// TestNewFromUsesLogger checks the borrowed-client role accepts Logger without
// connecting or changing the native client.
func TestNewFromUsesLogger(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)
	driver := NewFrom(nil, func(broker *Builder) {
		broker.Logger(logger)
	})

	if driver.logger != logger {
		t.Fatal("borrowed-client construction ignored Logger")
	}
}

// TestNewPublishesDetachedPrefix proves retained draft edits cannot redirect
// published traffic, even though native objects remain shared.
func TestNewPublishesDetachedPrefix(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	driver := New(func(broker *Builder) {
		retained = broker
		calls++
		broker.Prefix("billing:")
	})
	t.Cleanup(func() {
		_ = driver.Client().Close()
	})

	retained.Prefix("shipping:")

	if calls != 1 || driver.prefix != "billing:" {
		t.Fatal("construction replayed callbacks or retained its draft")
	}
}

// TestNewFromAppliesOnlyBorrowedClientSettings checks connection policy is
// ignored and the caller remains the client's owner without network I/O.
func TestNewFromAppliesOnlyBorrowedClientSettings(t *testing.T) {
	t.Parallel()

	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6380",
	})
	t.Cleanup(func() {
		_ = client.Close()
	})

	calls := 0
	driver := NewFrom(client, func(broker *Builder) {
		calls++
		broker.Addr("localhost:6381")
		broker.DB(9)
		broker.Prefix("billing:")
	})

	if calls != 1 || driver.owned || driver.Client() != client {
		t.Fatal("borrowed client ownership or callback execution changed")
	}

	if driver.prefix != "billing:" || client.Options().Addr != "localhost:6380" {
		t.Fatal("borrowed-client setting applicability changed")
	}

	if client.Options().DB != 0 {
		t.Fatal("construction mutated the native client")
	}
}

// TestBuilderTLSReachesClientOptions verifies the TLS setting is passed to the
// go-redis client without connecting.
func TestBuilderTLSReachesClientOptions(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	driver := New(func(broker *Builder) {
		broker.TLS(tlsConfig)
	})
	t.Cleanup(func() {
		_ = driver.Client().Close()
	})

	if got := driver.Client().Options().TLSConfig; got != tlsConfig {
		t.Fatalf("TLSConfig = %v, want the configured value", got)
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

	_ = New(nil)
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

	_ = New(func(broker *Builder) {
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

	_ = NewFrom(nil, nil)
}
