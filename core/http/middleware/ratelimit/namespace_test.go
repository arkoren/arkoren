package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestCounterNamespaceUsesSeparatorAndRuntimeTTL checks custom namespaces join
// both client and window, while counters and bool markers stay separate types.
func TestCounterNamespaceUsesSeparatorAndRuntimeTTL(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	config := newSettings(func(limiter *Builder) {
		limiter.Namespace(cache.NewNamespace[int64]("tenant.rate", func(space *cache.NamespaceBuilder) {
			space.Separator("/")
			space.TTL(time.Second)
		}))
		limiter.KeyFunc(func(*http.Request) string {
			return "client:part"
		})
		limiter.Limit(1)
		limiter.Events(event.New())
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	if _, err := consume(request, store, config, epoch); err != nil {
		t.Fatal(err)
	}

	result, err := consume(request, store, config, epoch)

	if err != nil || !result.denied {
		t.Fatalf("denied = %#v, %v", result, err)
	}

	name := "tenant.rate/client:part/" + strconv.FormatInt(epoch.UnixNano()/time.Minute.Nanoseconds(), 10)
	ctx := context.Background()

	count, err := store.Get(ctx, cache.NewKey[int64](name))

	if err != nil || count != 1 {
		t.Fatalf("counter = %d, %v", count, err)
	}

	marked, err := store.Get(ctx, cache.NewKey[bool](name+"/denied"))

	if err != nil || !marked {
		t.Fatalf("denied marker = %v, %v", marked, err)
	}

	ttl, err := store.TTL(ctx, cache.NewKey[int64](name))

	if err != nil || ttl < time.Minute {
		t.Fatalf("counter TTL = %v, %v", ttl, err)
	}
}

// TestDeniedMarkersHonorDotAndEmptySeparators checks marker suffixes follow
// the same literal joining policy as counters, with runtime retention intact.
func TestDeniedMarkersHonorDotAndEmptySeparators(t *testing.T) {
	t.Parallel()

	for _, separator := range []string{".", ""} {
		store := cache.New(memorycache.New())
		config := newSettings(func(limiter *Builder) {
			limiter.Namespace(cache.NewNamespace[int64]("tenant", func(space *cache.NamespaceBuilder) {
				space.Separator(separator)
				space.TTL(time.Second)
			}))
			limiter.KeyFunc(func(*http.Request) string {
				return "client"
			})
			limiter.Limit(1)
			limiter.Events(event.New())
		})

		request := httptest.NewRequest(http.MethodGet, "/", nil)

		if _, err := consume(request, store, config, epoch); err != nil {
			t.Fatal(err)
		}

		if _, err := consume(request, store, config, epoch); err != nil {
			t.Fatal(err)
		}

		name := "tenant" + separator + "client" + separator + "30000000" + separator + "denied"
		marker := cache.NewKey[bool](name)

		marked, err := store.Get(context.Background(), marker)

		if err != nil || !marked {
			t.Fatalf("separator %q: denied marker = %v, %v", separator, marked, err)
		}

		ttl, err := store.TTL(context.Background(), marker)

		if err != nil || ttl < time.Minute {
			t.Fatalf("separator %q: marker TTL = %v, %v", separator, ttl, err)
		}
	}
}

// TestDefaultDeniedMarkerPreservesExactIdentity checks the default colon
// policy still writes the legacy event marker without changing stored names.
func TestDefaultDeniedMarkerPreservesExactIdentity(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	config := newSettings(func(limiter *Builder) {
		limiter.KeyFunc(func(*http.Request) string {
			return "client"
		})
		limiter.Limit(1)
		limiter.Events(event.New())
	})

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	for range 2 {
		if _, err := consume(request, store, config, epoch); err != nil {
			t.Fatal(err)
		}
	}

	marked, err := store.Get(context.Background(), cache.NewKey[bool]("arkoren:ratelimit:client:30000000:denied"))

	if err != nil || !marked {
		t.Fatalf("legacy denied marker = %v, %v", marked, err)
	}
}

// TestMiddlewareRejectsZeroNamespaceAfterCallbacks checks invalid definitions
// are not normalized back to defaults and later callbacks still execute.
func TestMiddlewareRejectsZeroNamespaceAfterCallbacks(t *testing.T) {
	t.Parallel()

	called := false
	defer func() {
		if recover() == nil || !called {
			t.Fatal("expected namespace panic after every callback")
		}
	}()
	Middleware(cache.New(memorycache.New()), func(limiter *Builder) {
		limiter.Namespace(cache.Namespace[int64]{})
	}, func(limiter *Builder) {
		called = true
	})
}
