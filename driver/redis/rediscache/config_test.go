package rediscache

import (
	"crypto/tls"
	"reflect"
	"testing"
)

// TestNewPublishesDetachedConnectionSettings checks the constructor, including
// its native conversion path, without opening a Redis connection.
func TestNewPublishesDetachedConnectionSettings(t *testing.T) {
	t.Parallel()

	var retained *Builder
	calls := 0
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	driver := New(func(store *Builder) {
		retained = store
		calls++
		store.Addr("localhost:6380")
		store.TLS(tlsConfig)
	})
	t.Cleanup(func() {
		_ = driver.Client().Close()
	})

	retained.Addr("localhost:6381")

	if calls != 1 || driver.Client().Options().Addr != "localhost:6380" {
		t.Fatal("construction replayed callbacks or retained its draft")
	}

	if driver.Client().Options().TLSConfig != tlsConfig {
		t.Fatal("native TLS ownership changed")
	}

	if NewFrom(driver.Client()) != driver {
		t.Fatal("NewFrom is no longer a native conversion")
	}
}

// TestBuilderTLSReachesClientOptions verifies the TLS setting is passed to the
// go-redis client without connecting.
func TestBuilderTLSReachesClientOptions(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	driver := New(func(store *Builder) {
		store.TLS(tlsConfig)
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

	cache := &Builder{}
	receiver := reflect.ValueOf(cache)

	for index := range receiver.NumMethod() {
		method := receiver.Method(index)
		argument := reflect.Zero(method.Type().In(0))
		var result []reflect.Value

		if method.Type().IsVariadic() {
			result = method.CallSlice([]reflect.Value{argument})
		} else {
			result = method.Call([]reflect.Value{argument})
		}

		if result[0].Interface() != cache {
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

	configureDriver(func(store *Builder) {
		retained = store
		calls++
	}, func(store *Builder) {
		if store != retained || calls != 1 {
			t.Fatal("callbacks did not share an ordered draft")
		}

		calls++
	})

	if calls != 2 {
		t.Fatalf("callbacks ran %d times", calls)
	}

	configureDriver(func(store *Builder) {
		if store == retained {
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

	_ = New(func(store *Builder) {
		panic(marker)
	})
}
