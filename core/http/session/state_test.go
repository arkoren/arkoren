package session_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// readPersistedFields reconstructs a session through public middleware from a
// literal cache record. Unlike New with jsontext.Value input, this setup crosses
// the actual persistence codec and therefore establishes persisted provenance.
func readPersistedFields(t *testing.T, storage string) *session.Session {
	t.Helper()

	id := strings.Repeat("a", 43)
	raw := fmt.Sprintf(`{"id":%q,"created_at":%q,"expires_at":%q,"storage":%s}`,
		id, time.Now().UTC().Format(time.RFC3339Nano), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), storage)
	driver := memorycache.New()

	if err := driver.Put(t.Context(), prefix+id, []byte(raw), time.Hour); err != nil {
		t.Fatal(err)
	}

	var current *session.Session
	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current = mustSession(request)

		return nil
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: hostCookie, Value: id})
	handler.Record(request)

	if current == nil || current.ID() != id {
		t.Fatal("fixture did not load the persisted session")
	}

	return current
}

// TestStateRuntimeJSONPutCannotImpersonatePersistedField checks the public Put
// boundary preserves Go type identity even for the JSON codec's own byte type.
func TestStateRuntimeJSONPutCannotImpersonatePersistedField(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), nil)

	if err := current.Put(session.NewField[jsontext.Value]("theme"), jsontext.Value(`"dark"`)); err != nil {
		t.Fatal(err)
	}

	_, err := current.Get(session.NewField[string]("theme"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("runtime JSON read as string: %v, want ErrInvalidValueType", err)
	}

	value, err := current.Get(session.NewField[jsontext.Value]("theme"))

	if err != nil || string(value) != `"dark"` {
		t.Fatalf("runtime JSON lost its exact type: %s, %v", value, err)
	}
}

// TestStateRuntimeJSONConstructorPreservesDynamicType checks New's shallow
// snapshot does not reinterpret a caller's jsontext.Value as persisted data.
func TestStateRuntimeJSONConstructorPreservesDynamicType(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{"theme": jsontext.Value(`"dark"`)})

	value, err := current.Get(session.NewField[any]("theme"))

	if err != nil {
		t.Fatal(err)
	}

	raw, ok := value.(jsontext.Value)

	if !ok || string(raw) != `"dark"` {
		t.Fatalf("runtime value = %T %v, want jsontext.Value", value, value)
	}
}

// TestStateNilInterfacePutRoundTrips distinguishes an existing nil interface
// value from a missing field and from a mismatched concrete field declaration.
func TestStateNilInterfacePutRoundTrips(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), nil)
	field := session.NewField[any]("optional")

	if err := current.Put(field, nil); err != nil {
		t.Fatal(err)
	}

	value, err := current.Get(field)

	if err != nil || value != nil {
		t.Fatalf("nil interface round trip = %v, %v, want nil, nil", value, err)
	}

	_, err = current.Get(session.NewField[string]("optional"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("nil interface converted to a concrete field: %v", err)
	}
}

// TestStateNilInterfaceConstructorRoundTrips checks New's shallow map capture
// retains an existing nil interface, including a nonempty interface definition.
func TestStateNilInterfaceConstructorRoundTrips(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{"optional": nil})

	value, err := current.Get(session.NewField[error]("optional"))

	if err != nil || value != nil {
		t.Fatalf("nil constructor value = %v, %v, want nil, nil", value, err)
	}

	_, err = current.Get(session.NewField[any]("absent"))

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("missing interface field = %v, want ErrKeyNotFound", err)
	}
}

// TestStateNegativeIdleTTLRejectedAtConstruction checks invalid lifetime
// configuration fails before middleware publication instead of silently dropping
// every session write or handing an invalid lifetime to a cache driver.
func TestStateNegativeIdleTTLRejectedAtConstruction(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("negative idle TTL was accepted")
		}
	}()
	session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
		cookies.TTL(-time.Second)
	})
}

// TestStateNegativePersistenceTimeoutRejectedAtConstruction checks a negative
// timeout cannot silently cancel all persistence while returning normal HTTP
// responses. Zero remains the documented default, not an invalid setting.
func TestStateNegativePersistenceTimeoutRejectedAtConstruction(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("negative persistence timeout was accepted")
		}
	}()
	session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
		cookies.PersistenceTimeout(-time.Second)
	})
}

// TestStateLoadedSessionRuntimeReplacementIsNotPersisted checks provenance is
// per value, not merely the fact that middleware loaded the surrounding session.
func TestStateLoadedSessionRuntimeReplacementIsNotPersisted(t *testing.T) {
	t.Parallel()

	current := readPersistedFields(t, `{"theme":"light"}`)

	if err := current.Put(session.NewField[jsontext.Value]("theme"), jsontext.Value(`"dark"`)); err != nil {
		t.Fatal(err)
	}

	_, err := current.Get(session.NewField[string]("theme"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("replacement decoded as persisted JSON: %v", err)
	}
}

// TestStateAllExposesJSONWithoutPersistedProvenance checks the public shallow
// snapshot keeps its raw-JSON shape but cannot transport private codec authority
// into a new in-memory session through the constructor.
func TestStateAllExposesJSONWithoutPersistedProvenance(t *testing.T) {
	t.Parallel()

	loaded := readPersistedFields(t, `{"theme":"dark"}`)

	snapshot := loaded.All()
	raw, ok := snapshot["theme"].(jsontext.Value)

	if !ok || string(raw) != `"dark"` {
		t.Fatalf("snapshot value = %T %v, want raw JSON", snapshot["theme"], snapshot["theme"])
	}

	current := session.New(time.Now().Add(time.Hour), snapshot)

	_, err := current.Get(session.NewField[string]("theme"))

	if !errors.Is(err, session.ErrInvalidValueType) {
		t.Fatalf("snapshot impersonated persisted data: %v", err)
	}

	value, err := loaded.Get(session.NewField[string]("theme"))

	if err != nil || value != "dark" {
		t.Fatalf("persisted lookup = %q, %v, want dark, nil", value, err)
	}
}

// TestStateRuntimeJSONDecodesOnlyAfterPersistence crosses the real HTTP/cache
// codec rather than pretending a constructor argument has already been stored.
func TestStateRuntimeJSONDecodesOnlyAfterPersistence(t *testing.T) {
	t.Parallel()

	store := cache.New(memorycache.New())
	seed := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		return mustSession(request).Put(session.NewField[jsontext.Value]("theme"), jsontext.Value(`"dark"`))
	}))

	created := seed.Record(httptest.NewRequest(http.MethodPost, "/theme", nil))

	if len(created.Cookies()) != 1 {
		t.Fatalf("seed cookies = %v, want one", created.Cookies())
	}

	got := ""
	reader := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		value, err := mustSession(request).Get(session.NewField[string]("theme"))
		got = value

		return err
	}))

	request := httptest.NewRequest(http.MethodGet, "/theme", nil)
	request.AddCookie(created.Cookies()[0])

	response := reader.Record(request)

	if response.StatusCode != http.StatusNoContent || got != "dark" {
		t.Fatalf("reloaded value = %q, status %d", got, response.StatusCode)
	}
}

// TestStateExpiryBoundaryIsExpired checks the exact expiry instant, where a
// zero remaining TTL must not accidentally create a persistent cache record.
func TestStateExpiryBoundaryIsExpired(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		current := session.New(time.Now(), nil)

		if !current.HasExpired() {
			t.Fatal("session is live at its exact expiry")
		}
	})
}

// TestStateExpiresSoonMinimumDurationDoesNotOverflow verifies a negative
// renewal window cannot wrap into a positive window through unary negation.
func TestStateExpiresSoonMinimumDurationDoesNotOverflow(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		current := session.New(time.Now().Add(time.Hour), nil)

		if current.ExpiresSoon(time.Duration(math.MinInt64)) {
			t.Fatal("minimum negative duration enabled renewal")
		}
	})
}

// TestStateAbsoluteLifetimeCapsCookieAndCache observes initial expiry through
// public cookie metadata and Cache.TTL, independently of namespace write policy.
func TestStateAbsoluteLifetimeCapsCookieAndCache(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		createdAt := time.Now()
		handler := session.Middleware(store, func(cookies *session.Builder) {
			cookies.TTL(time.Hour)
			cookies.MaxLifetime(time.Minute)
		})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			return mustSession(request).Put(session.NewField[int]("cart"), 1)
		}))

		response := handler.Record(httptest.NewRequest(http.MethodPost, "/cart", nil))

		if len(response.Cookies()) != 1 {
			t.Fatalf("cookies = %v, want one", response.Cookies())
		}

		cookie := response.Cookies()[0]

		if cookie.Expires.After(createdAt.Add(time.Minute)) || cookie.MaxAge > 60 {
			t.Fatalf("cookie exceeds absolute lifetime: %v", cookie)
		}

		remaining, err := store.TTL(t.Context(), cache.NewKey[jsontext.Value](prefix+cookie.Value))

		if err != nil || remaining <= 0 || remaining > time.Minute {
			t.Fatalf("record lifetime = %s, %v; want (0, 1m]", remaining, err)
		}
	})
}

// TestStateAbsoluteLifetimeKeepsUnusedSessionUnpersisted checks capping an
// initial expiry does not turn a no-op renewal back into a dirty new session.
func TestStateAbsoluteLifetimeKeepsUnusedSessionUnpersisted(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		handler := session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
			cookies.MaxLifetime(time.Minute)
		})(router.Handler(func(http.ResponseWriter, *http.Request) error {
			return nil
		}))

		response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

		if len(response.Cookies()) != 0 {
			t.Fatalf("unused capped session was persisted: %v", response.Cookies())
		}
	})
}

// TestStateAbsoluteLifetimeCapsRenewal checks automatic idle renewal keeps
// the original creation anchor rather than granting a fresh absolute lifetime.
func TestStateAbsoluteLifetimeCapsRenewal(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		createdAt := time.Now()
		handler := session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
			cookies.TTL(time.Minute)
			cookies.MaxLifetime(90 * time.Second)
			cookies.ExpirationDelta(30 * time.Second)
		})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			return mustSession(request).Put(session.NewField[int]("cart"), 1)
		}))

		first := handler.Record(httptest.NewRequest(http.MethodPost, "/cart", nil))

		if len(first.Cookies()) != 1 {
			t.Fatalf("seed cookies = %v, want one", first.Cookies())
		}

		time.Sleep(45 * time.Second)

		request := httptest.NewRequest(http.MethodPost, "/cart", nil)
		request.AddCookie(first.Cookies()[0])

		response := handler.Record(request)

		if len(response.Cookies()) != 1 {
			t.Fatalf("renewal cookies = %v, want one", response.Cookies())
		}

		if response.Cookies()[0].Expires.After(createdAt.Add(90 * time.Second)) {
			t.Fatalf("renewal exceeded original absolute deadline: %v", response.Cookies()[0])
		}
	})
}

// TestStateAbsoluteLifetimeRejectsLateFirstSave checks a handler that outlives
// the absolute lifetime cannot publish its first session record or cookie.
func TestStateAbsoluteLifetimeRejectsLateFirstSave(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := cache.New(memorycache.New())
		id := ""
		handler := session.Middleware(store, func(cookies *session.Builder) {
			cookies.MaxLifetime(time.Second)
		})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			id = mustSession(request).ID()
			time.Sleep(time.Second)

			return mustSession(request).Put(session.NewField[int]("cart"), 1)
		}))

		response := handler.Record(httptest.NewRequest(http.MethodPost, "/cart", nil))

		if len(response.Cookies()) != 0 {
			t.Fatalf("late first save issued a cookie: %v", response.Cookies())
		}

		found, err := store.Has(t.Context(), cache.NewKey[jsontext.Value](prefix+id))

		if err != nil || found {
			t.Fatalf("late first save created a record: %t, %v", found, err)
		}
	})
}

// stateCleanupDriver observes the public cache cleanup context and simulates
// network I/O that waits for its deadline. An unbounded context returns promptly
// with an error, so the failing regression never deadlocks its test bubble.
type stateCleanupDriver struct {
	// Driver supplies real session records, guarded writes, and leases.
	cache.Driver

	// budget records the release operation's independent deadline budget.
	budget time.Duration

	// trace records a request context value observed at the public driver seam.
	trace any
}

// stateTraceKey models an application trace value without using another
// module's private trace representation or changing the request's other values.
type stateTraceKey struct{}

// Unlock models a cooperative slow backend. The deadline, not the original
// request cancellation, must end this detached best-effort release operation.
func (driver *stateCleanupDriver) Unlock(ctx context.Context, _, _ string) (bool, error) {
	driver.trace = ctx.Value(stateTraceKey{})
	deadline, ok := ctx.Deadline()

	if !ok {
		return false, errors.New("release has no deadline")
	}

	driver.budget = time.Until(deadline)
	<-ctx.Done()

	return false, ctx.Err()
}

// TestStateSessionCleanupHasFiniteBudget checks internal cleanup does not
// remove the only deadline from I/O performed before response headers commit.
func TestStateSessionCleanupHasFiniteBudget(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := &stateCleanupDriver{Driver: memorycache.New()}
		handler := session.Middleware(cache.New(driver), func(cookies *session.Builder) {
			cookies.PersistenceTimeout(time.Second)
		})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			return mustSession(request).Put(session.NewField[int]("cart"), 1)
		}))

		first := handler.Record(httptest.NewRequest(http.MethodPost, "/cart", nil))

		if len(first.Cookies()) != 1 {
			t.Fatalf("seed cookies = %v, want one", first.Cookies())
		}

		request := httptest.NewRequest(http.MethodPost, "/cart", nil)
		request.AddCookie(first.Cookies()[0])

		request = request.WithContext(context.WithValue(request.Context(), stateTraceKey{}, "retained-trace"))

		response := handler.Record(request)

		if driver.budget <= 0 || driver.budget > 10*time.Second {
			t.Fatalf("release budget = %s, want (0, 10s]", driver.budget)
		}

		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", response.StatusCode)
		}

		if driver.trace != "retained-trace" {
			t.Fatalf("cleanup lost request context values: %v", driver.trace)
		}
	})
}

// TestStateDestroyedEventUsesRoutePattern keeps path parameters and query
// secrets out of notifications while retaining useful, bounded route identity.
func TestStateDestroyedEventUsesRoutePattern(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got session.DestroyedEvent

	if _, err := bus.Listen(session.Destroyed, func(_ context.Context, payload session.DestroyedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	routes := router.New()
	routes.Use(session.Middleware(cache.New(memorycache.New()), func(cookies *session.Builder) {
		cookies.Events(bus)
	}))
	routes.Post("/sessions/{id}/logout", func(_ http.ResponseWriter, request *http.Request) error {
		mustSession(request).Destroy()

		return nil
	})
	routes.Record(httptest.NewRequest(http.MethodPost, "/sessions/private-session-id/logout?token=private-token", nil))

	if got.Path != "/sessions/{id}/logout" || strings.Contains(fmt.Sprint(got), "private") {
		t.Fatalf("destroyed event exposed expanded path: %+v", got)
	}
}

// stateFailureDriver injects a backend error whose text includes private data.
// The error remains available to the configured error callback, not event data.
type stateFailureDriver struct {
	// Driver supplies normal reads and leases for middleware setup.
	cache.Driver

	// err is returned unchanged by every storage write.
	err error
}

// Put fails persistence at the public cache driver seam without mutating data.
func (driver stateFailureDriver) Put(context.Context, string, []byte, time.Duration) error {
	return driver.err
}

// TestStatePersistenceEventRedactsPathButPreservesCallbackError verifies the
// event and error callback have distinct privacy contracts at the HTTP seam.
func TestStatePersistenceEventRedactsPathButPreservesCallbackError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got session.PersistFailedEvent

	if _, err := bus.Listen(session.PersistFailed, func(_ context.Context, payload session.PersistFailedEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cause := errors.New("private-backend-key")
	var handled error
	store := cache.New(stateFailureDriver{Driver: memorycache.New(), err: cause})
	routes := router.New()
	routes.Use(session.Middleware(store, func(cookies *session.Builder) {
		cookies.Events(bus)
		cookies.ErrorHandler(func(err error) {
			handled = err
		})
	}))
	routes.Post("/accounts/{id}/cart", func(_ http.ResponseWriter, request *http.Request) error {
		return mustSession(request).Put(session.NewField[int]("cart"), 1)
	})
	routes.Record(httptest.NewRequest(http.MethodPost, "/accounts/private-account/cart?token=private-token", nil))

	if got.Path != "/accounts/{id}/cart" || strings.Contains(fmt.Sprint(got), "private") {
		t.Fatalf("persistence event exposed private data: %+v", got)
	}

	if !errors.Is(handled, cause) {
		t.Fatalf("callback lost the backend cause: %v", handled)
	}
}
