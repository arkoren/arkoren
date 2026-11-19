package session

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/session/internal/persistence"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// TestStoreNamespacePreservesCodecAndOverridesTTL checks namespace composition
// does not replace the session codec or let inherited TTL govern session age.
func TestStoreNamespacePreservesCodecAndOverridesTTL(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	store := sessionStore{
		Store: persistence.Store{Driver: driver, Locks: cache.New(driver),
			Space: cache.NewNamespace[jsontext.Value]("tenant.sessions/", func(space *cache.NamespaceBuilder) {
				space.Separator("")
				space.TTL(time.Second)
			})},
	}

	value := New(time.Now().Add(time.Hour), map[string]any{"large": int64(9007199254740993)})
	ctx := context.Background()

	if _, err := store.save(ctx, value, time.Hour, cache.Match{}); err != nil {
		t.Fatal(err)
	}

	ttl, err := driver.TTL(ctx, "tenant.sessions/"+value.ID())

	if err != nil || ttl < 59*time.Minute {
		t.Fatalf("record TTL = %v, %v; want runtime lifetime", ttl, err)
	}

	loaded, found, err := store.get(ctx, value.ID())

	if err != nil || !found {
		t.Fatalf("load = %v, %v", found, err)
	}

	large, err := loaded.Get(NewField[int64]("large"))

	if err != nil || large != 9007199254740993 {
		t.Fatalf("large = %d, %v", large, err)
	}
}

// TestStoreNamespaceTombstoneOverridesTTL checks retirement retention uses
// maximum session age rather than the inherited cache retention policy.
func TestStoreNamespaceTombstoneOverridesTTL(t *testing.T) {
	t.Parallel()

	driver := memorycache.New()
	store := sessionStore{
		Store: persistence.Store{Driver: driver, Locks: cache.New(driver),
			Space: cache.NewNamespace[jsontext.Value]("tenant.sessions/", func(space *cache.NamespaceBuilder) {
				space.Separator("")
				space.TTL(time.Second)
			})},
	}

	value := New(time.Now().Add(time.Hour), nil)
	ctx := context.Background()

	if err := store.Tombstone(ctx, value.ID(), 2*time.Hour); err != nil {
		t.Fatal(err)
	}

	_, found, err := store.get(ctx, value.ID())

	if err != nil || found {
		t.Fatalf("tombstoned load = %v, %v", found, err)
	}

	ttl, err := driver.TTL(ctx, "tenant.sessions/"+value.ID())

	if err != nil || ttl < 119*time.Minute {
		t.Fatalf("tombstone TTL = %v, %v; want runtime lifetime", ttl, err)
	}
}

// TestMiddlewareRejectsZeroNamespaceAfterCallbacks checks invalid definitions
// cannot silently restore the default namespace or skip later callbacks.
func TestMiddlewareRejectsZeroNamespaceAfterCallbacks(t *testing.T) {
	t.Parallel()

	called := false
	defer func() {
		if recover() == nil || !called {
			t.Fatal("expected namespace panic after every callback")
		}
	}()
	Middleware(cache.New(memorycache.New()), func(cookies *Builder) {
		cookies.Namespace(cache.Namespace[jsontext.Value]{})
	}, func(cookies *Builder) {
		called = true
	})
}

// TestRecordTombstoneIsWrittenOnlyWhenSet verifies a live record omits the
// tombstone member, which older records also lack, and a retired one writes it.
func TestRecordTombstoneIsWrittenOnlyWhenSet(t *testing.T) {
	t.Parallel()

	live, err := json.Marshal(persistence.Record{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(live), "tombstone") {
		t.Fatalf("live record %s has a tombstone member", live)
	}
	retired, err := json.Marshal(persistence.Record{ID: "a", Tombstone: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(retired), `"tombstone":true`) {
		t.Fatalf("retired record %s lacks the tombstone", retired)
	}
}
