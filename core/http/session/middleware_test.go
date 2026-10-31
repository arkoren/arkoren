package session_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
)

// prefix is the default session key prefix.
const prefix = "arkoren:session:"

// hostCookie is the default cookie name for a secure, root-path configuration.
const hostCookie = session.HostPrefix + session.DefaultCookie

// driver is a cache.Driver that records the session IDs it saves and deletes.
type driver struct {
	// saved and deleted record the most recent keys passed to cache writes and
	// removals.
	saved, deleted string
	// saves counts writes so tests can detect duplicate session persistence.
	saves int
	// values holds serialized session records and tombstones by cache key.
	values map[string][]byte
	// locks maps lease names to tokens for the cache locking contract.
	locks map[string]string
	// err injects a backend failure into the operations exercised by middleware.
	err error
}

// newDriver returns a driver holding sessions.
func newDriver(t *testing.T, sessions ...*session.Session) *driver {
	t.Helper()

	driver := &driver{
		values: map[string][]byte{},
	}

	for _, value := range sessions {
		raw, err := json.Marshal(map[string]any{
			"id":         value.ID(),
			"created_at": value.CreatedAt(),
			"expires_at": value.ExpiresAt(),
			"storage":    value.All(),
		})

		if err != nil {
			t.Fatal(err)
		}

		driver.values[prefix+value.ID()] = raw
	}

	return driver
}

// Get returns the stored record for key.
func (driver *driver) Get(_ context.Context, key string) ([]byte, error) {
	if driver.err != nil {
		return nil, driver.err
	}

	value, found := driver.values[key]

	if !found {
		return nil, cache.ErrNotFound
	}

	return value, nil
}

// Put records the saved session ID.
func (driver *driver) Put(_ context.Context, key string, value []byte, _ time.Duration) error {
	driver.saves++
	driver.saved = strings.TrimPrefix(key, prefix)
	driver.values[key] = value

	return driver.err
}

// CompareAndPut models the atomic guarded write for this single-threaded
// recording fixture. A mismatch preserves both entries and the write count.
func (driver *driver) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	if driver.err != nil {
		return false, driver.err
	}

	raw, found := driver.values[match.Key]

	if !found || !bytes.Equal(raw, match.Value) {
		return false, nil
	}

	return true, driver.Put(ctx, entry.Key, entry.Value, entry.TTL)
}

// Delete records the deleted session ID.
func (driver *driver) Delete(_ context.Context, key string) error {
	driver.deleted = strings.TrimPrefix(key, prefix)
	delete(driver.values, key)

	return driver.err
}

// Has is unused by sessions.
func (driver *driver) Has(context.Context, string) (bool, error) {
	return false, nil
}

// Add is unused by sessions.
func (driver *driver) Add(context.Context, string, []byte, time.Duration) (bool, error) {
	return false, nil
}

// Increment is unused by sessions.
func (driver *driver) Increment(context.Context, string, int64) (int64, error) {
	return 0, nil
}

// Decrement is unused by sessions.
func (driver *driver) Decrement(context.Context, string, int64) (int64, error) {
	return 0, nil
}

// Pull is unused by sessions.
func (driver *driver) Pull(context.Context, string) ([]byte, error) {
	return nil, cache.ErrNotFound
}

// TryLock takes the lock name for token when no one holds it. Expiry is
// not simulated: a lock stays held until released.
func (driver *driver) TryLock(_ context.Context, name, token string, _ time.Duration) (bool, error) {
	if driver.locks[name] != "" {
		return false, nil
	}

	if driver.locks == nil {
		driver.locks = map[string]string{}
	}

	driver.locks[name] = token

	return true, nil
}

// Unlock frees the lock name when token holds it.
func (driver *driver) Unlock(_ context.Context, name, token string) (bool, error) {
	if driver.locks[name] != token {
		return false, nil
	}

	delete(driver.locks, name)

	return true, nil
}

// ExtendLock reports whether token holds the lock name.
func (driver *driver) ExtendLock(_ context.Context, name, token string, _ time.Duration) (bool, error) {
	return driver.locks[name] == token, nil
}

// IncrementWithTTL is unused by sessions.
func (driver *driver) IncrementWithTTL(context.Context, string, int64, time.Duration) (int64, time.Duration, error) {
	return 0, 0, nil
}

// TTL is unused by sessions.
func (driver *driver) TTL(context.Context, string) (time.Duration, error) {
	return 0, nil
}

// TestMiddlewareCreatesAndExposesSession verifies session initialization.
func TestMiddlewareCreatesAndExposesSession(t *testing.T) {
	t.Parallel()

	driver := newDriver(t)
	handler := session.Middleware(cache.New(driver), func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		value, ok := session.FromContext(request.Context())

		if !ok {
			t.Fatal("session missing")
		}

		if err := value.Put(session.NewField[int]("user_id"), 42); err != nil {
			return err
		}

		return nil
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if len(response.Cookies()) != 1 || driver.saved == "" {
		t.Fatal("session was not persisted")
	}

	if response.Cookies()[0].Secure {
		t.Fatal("insecure development cookie is secure")
	}
}

// TestMiddlewareRegenerationTombstonesOldRecord verifies the old ID is retired with a tombstone.
func TestMiddlewareRegenerationTombstonesOldRecord(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), nil)
	value.MarkAsUnchanged()

	driver := newDriver(t, value)
	old := value.ID()
	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		mustSession(request).Regenerate()

		return nil
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: old,
	})

	_ = handler.Record(request)

	if !isTombstone(driver.values[prefix+old]) || len(driver.values) != 2 {
		t.Fatal("regenerated session was not retired and saved")
	}
}

// TestMiddlewareUsesCustomHandle verifies selected session storage.
func TestMiddlewareUsesCustomHandle(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	driver := newDriver(t)
	handler := session.Middleware(cache.New(driver), func(sessionPolicy *session.Builder) {
		sessionPolicy.Handle(handle)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		_, defaultFound := session.FromContext(request.Context())
		value, keyedFound := session.FromContextFor(request.Context(), handle)

		if defaultFound || !keyedFound || value == nil {
			t.Fatal("custom session context key was not used")
		}

		return nil
	}))

	_ = handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
}

// TestMiddlewareDistinctHandlesShareRequest verifies three middleware layers
// populate independent bridges when their cookie names are configured apart.
// Handles alone do not provide cookie or storage isolation.
func TestMiddlewareDistinctHandlesShareRequest(t *testing.T) {
	t.Parallel()

	store := cache.New(newDriver(t))
	first, second := session.NewHandle(), session.NewHandle()
	outer := session.Middleware(store)
	admin := session.Middleware(store, func(cookies *session.Builder) {
		cookies.Name("admin")
		cookies.Handle(first)
	})

	staff := session.Middleware(store, func(cookies *session.Builder) {
		cookies.Name("staff")
		cookies.Handle(second)
	})

	handler := outer(admin(staff(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		defaultSession, defaultFound := session.FromContext(request.Context())
		firstSession, firstFound := session.FromContextFor(request.Context(), first)
		secondSession, secondFound := session.FromContextFor(request.Context(), second)

		if !defaultFound || !firstFound || !secondFound {
			t.Fatal("a middleware bridge was missing")
		}

		if defaultSession == firstSession || defaultSession == secondSession || firstSession == secondSession {
			t.Fatal("independent bridges shared a session")
		}

		return nil
	}))))

	if response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil)); response.StatusCode != http.StatusNoContent {
		t.Fatal(response.StatusCode)
	}
}

// TestMiddlewareUnusedNewSessionIsNotSaved checks that an untouched fresh
// session causes neither a storage write nor a response cookie.
func TestMiddlewareUnusedNewSessionIsNotSaved(t *testing.T) {
	t.Parallel()

	driver := newDriver(t)
	handler := session.Middleware(cache.New(driver))(router.Handler(
		func(http.ResponseWriter, *http.Request) error {
			return nil
		},
	))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if driver.saved != "" || len(response.Cookies()) != 0 {
		t.Fatalf("saved = %q, cookies = %d, want nothing", driver.saved, len(response.Cookies()))
	}
}

// TestMiddlewareSkipsSaveOnServerError checks that a failing handler does not
// persist its session mutations when the response becomes HTTP 500.
func TestMiddlewareSkipsSaveOnServerError(t *testing.T) {
	t.Parallel()

	driver := newDriver(t)
	handler := session.Middleware(cache.New(driver))(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			if err := mustSession(request).Put(session.NewField[int]("cart"), 1); err != nil {
				return err
			}

			return errors.New("database down")
		},
	))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusInternalServerError || driver.saved != "" {
		t.Fatalf("status = %d, saved = %q, want 500 and no save", response.StatusCode, driver.saved)
	}
}

// TestMiddlewareSavesOnceWithEarlyHints checks that informational response
// headers do not cause duplicate persistence before the final response.
func TestMiddlewareSavesOnceWithEarlyHints(t *testing.T) {
	t.Parallel()

	counting := newDriver(t)
	handler := session.Middleware(cache.New(counting))(router.Handler(
		func(writer http.ResponseWriter, request *http.Request) error {
			if err := mustSession(request).Put(session.NewField[int]("user_id"), 1); err != nil {
				return err
			}

			writer.WriteHeader(http.StatusEarlyHints)
			writer.WriteHeader(http.StatusOK)

			return nil
		},
	))

	_ = handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if counting.saves != 1 {
		t.Fatalf("saves = %d, want 1", counting.saves)
	}
}

// TestMiddlewareDestroyTombstonesAndExpiresCookie checks that destroying a
// loaded session leaves only a retired-ID record and expires the browser
// cookie.
func TestMiddlewareDestroyTombstonesAndExpiresCookie(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	driver := newDriver(t, value)
	handler := session.Middleware(cache.New(driver))(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			mustSession(request).Destroy()

			return nil
		},
	))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: value.ID(),
	})

	response := handler.Record(request)

	if !isTombstone(driver.values[prefix+value.ID()]) || len(driver.values) != 1 {
		t.Fatalf("values = %q, want only a tombstone", driver.values)
	}

	if cookies := response.Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("cookies = %+v, want one expired cookie", cookies)
	}
}

// TestMiddlewareReturnsStorageFailure checks that a session-loading backend
// failure becomes HTTP 500 rather than silently creating a fresh session.
func TestMiddlewareReturnsStorageFailure(t *testing.T) {
	t.Parallel()

	handler := session.Middleware(cache.New(&driver{
		err: errors.New("backend down"),
	}))(router.Handler(
		func(http.ResponseWriter, *http.Request) error {
			return nil
		},
	))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: strings.Repeat("a", 43),
	})

	if response := handler.Record(request); response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
}

// mustSession returns the default session stored on request or fails the test run.
func mustSession(request *http.Request) *session.Session {
	value, ok := session.FromContext(request.Context())

	if !ok {
		panic(session.ErrNoSession)
	}

	return value
}

// point is a struct stored in a session by the precision test.
type point struct {
	// X and Y exercise exact signed integer preservation when a struct is stored
	// in a session.
	X, Y int64
}

// TestMiddlewareRoundTripsValuesExactly verifies large integers, strings, and
// structs survive save and load unchanged.
func TestMiddlewareRoundTripsValuesExactly(t *testing.T) {
	t.Parallel()

	const (
		big  = int64(9007199254740993)
		uuid = "0190a3b4-7c5e-7d2a-8f11-123456789abc"
	)
	want := point{
		X: big,
		Y: big + 2,
	}
	driver := newDriver(t)
	handler := session.Middleware(cache.New(driver), func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current := mustSession(request)

		if _, err := current.Get(session.NewField[int64]("big")); err != nil {
			if err := current.Put(session.NewField[int64]("big"), big); err != nil {
				return err
			}

			if err := current.Put(session.NewField[string]("uuid"), uuid); err != nil {
				return err
			}

			if err := current.Put(session.NewField[point]("point"), want); err != nil {
				return err
			}

			return nil
		}

		if got := requireGet[int64](t, current, "big"); got != big {
			t.Errorf("big = %d, want %d", got, big)
		}

		if got := requireGet[string](t, current, "uuid"); got != uuid {
			t.Errorf("uuid = %q, want %q", got, uuid)
		}

		if got := requireGet[point](t, current, "point"); got != want {
			t.Errorf("point = %+v, want %+v", got, want)
		}

		return nil
	}))

	_ = handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: driver.saved,
	})

	_ = handler.Record(request)
}

// checkMiddlewareAccepts fails the test if building the session middleware with
// opts panics.
func checkMiddlewareAccepts(t *testing.T, opts ...func(*session.Builder)) {
	t.Helper()

	session.Middleware(cache.New(newDriver(t)), opts...)
}

// TestMiddlewareAcceptsValidPrefixedConfigHost verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigHost(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(cookies *session.Builder) {
		cookies.Name(session.HostPrefix + "sid")
	})
}

// TestMiddlewareAcceptsValidPrefixedConfigHostExplicit verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigHostExplicit(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(cookies *session.Builder) {
		cookies.Name(session.HostPrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.Path("/")
	})
}

// TestMiddlewareAcceptsValidPrefixedConfigSecure verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigSecure(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(cookies *session.Builder) {
		cookies.Name(session.SecurePrefix + "sid")
	})
}

// TestMiddlewareAcceptsValidPrefixedConfigSecureDomain verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigSecureDomain(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(cookies *session.Builder) {
		cookies.Name(session.SecurePrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.Domain("example.com")
	})
}

// TestMiddlewareAcceptsValidPrefixedConfigSecureForPath verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigSecureForPath(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(cookies *session.Builder) {
		cookies.Name(session.SecurePrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.Path("/app")
	})
}

// TestMiddlewareAcceptsValidPrefixedConfigPlainInsecure verifies a valid prefix combination.
func TestMiddlewareAcceptsValidPrefixedConfigPlainInsecure(t *testing.T) {
	t.Parallel()

	checkMiddlewareAccepts(t, func(sessionPolicy *session.Builder) {
		sessionPolicy.Name("sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})
}

// checkMiddlewarePanics fails the test unless building the session middleware
// with opts panics.
func checkMiddlewarePanics(t *testing.T, opts ...func(*session.Builder)) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("Middleware did not panic")
		}
	}()
	session.Middleware(cache.New(newDriver(t)), opts...)
}

// TestMiddlewarePanicsOnInvalidPrefixedConfigHostInsecure verifies a contradictory prefix setting.
func TestMiddlewarePanicsOnInvalidPrefixedConfigHostInsecure(t *testing.T) {
	t.Parallel()

	checkMiddlewarePanics(t, func(cookies *session.Builder) {
		cookies.Name(session.HostPrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})
}

// TestMiddlewarePanicsOnInvalidPrefixedConfigHostDomain verifies a contradictory prefix setting.
func TestMiddlewarePanicsOnInvalidPrefixedConfigHostDomain(t *testing.T) {
	t.Parallel()

	checkMiddlewarePanics(t, func(cookies *session.Builder) {
		cookies.Name(session.HostPrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.Domain("example.com")
	})
}

// TestMiddlewarePanicsOnInvalidPrefixedConfigHostPath verifies a contradictory prefix setting.
func TestMiddlewarePanicsOnInvalidPrefixedConfigHostPath(t *testing.T) {
	t.Parallel()

	checkMiddlewarePanics(t, func(cookies *session.Builder) {
		cookies.Name(session.HostPrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.Path("/app")
	})
}

// TestMiddlewarePanicsOnInvalidPrefixedConfigSecureInsecure verifies a contradictory prefix setting.
func TestMiddlewarePanicsOnInvalidPrefixedConfigSecureInsecure(t *testing.T) {
	t.Parallel()

	checkMiddlewarePanics(t, func(cookies *session.Builder) {
		cookies.Name(session.SecurePrefix + "sid")
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})
}

// TestMiddlewarePrefixedCookieRoundTrips verifies a prefixed cookie loads its session.
func TestMiddlewarePrefixedCookieRoundTrips(t *testing.T) {
	t.Parallel()

	name := session.HostPrefix + "sid"
	handler := session.Middleware(cache.New(newDriver(t)), func(sessionPolicy *session.Builder) {
		sessionPolicy.Name(name)
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		value := mustSession(request)

		if request.Header.Get("Cookie") == "" {
			if err := value.Put(session.NewField[string]("user"), "a"); err != nil {
				return err
			}
		} else if got, err := value.Get(session.NewField[string]("user")); err != nil || got != "a" {
			t.Fatal("session not restored")
		}

		return nil
	}))

	first := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	cookies := first.Cookies()

	if len(cookies) != 1 || cookies[0].Name != name || !cookies[0].Secure || cookies[0].Path != "/" || cookies[0].Domain != "" {
		t.Fatalf("unexpected cookies %v", cookies)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookies[0])
	handler.Record(request)
}

// isTombstone reports whether raw is a retired-session record.
func isTombstone(raw []byte) bool {
	return strings.Contains(string(raw), `"tombstone":true`)
}

// staleSaveSetup stores a live session and returns the driver, its ID, and a
// request carrying its cookie.
func staleSaveSetup(t *testing.T) (*driver, string, func() *http.Request) {
	t.Helper()

	value := session.New(time.Now().Add(time.Hour), map[string]any{
		"user": 1,
	})
	driver := newDriver(t, value)

	return driver, value.ID(), func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{
			Name:  hostCookie,
			Value: value.ID(),
		})

		return request
	}
}

// retiringHandler returns a handler that, while the outer request is in
// flight, runs a second request that calls retire on the same session, then
// makes the outer request modify its stale copy.
func retiringHandler(
	store *cache.Cache,
	newRequest func() *http.Request,
	retire func(*session.Session),
	stale func(*session.Session),
) router.Handler {
	inner := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		retire(mustSession(request))

		return nil
	}))

	return session.Middleware(store)(router.Handler(func(w http.ResponseWriter, request *http.Request) error {
		stale(mustSession(request))

		_ = inner.Record(newRequest())

		return nil
	}))
}

// TestMiddlewareStaleSaveAfterLogoutKeepsOldIDDead verifies a request that
// loaded a session before a logout cannot re-create it.
func TestMiddlewareStaleSaveAfterLogoutKeepsOldIDDead(t *testing.T) {
	t.Parallel()

	driver, id, newRequest := staleSaveSetup(t)
	handler := retiringHandler(cache.New(driver), newRequest,
		func(value *session.Session) {
			if err := value.Delete(session.NewField[string]("user")); err != nil {
				t.Fatal(err)
			}

			value.Regenerate()
		},
		func(value *session.Session) {
			if err := value.Put(session.NewField[int]("cart"), 1); err != nil {
				t.Fatal(err)
			}
		})

	response := handler.Record(newRequest())

	if len(response.Cookies()) != 0 {
		t.Fatalf("stale save set a cookie: %v", response.Cookies())
	}

	if !isTombstone(driver.values[prefix+id]) {
		t.Fatalf("old record = %s, want tombstone", driver.values[prefix+id])
	}
}

// TestMiddlewareStaleSaveAfterDestroyKeepsOldIDDead verifies Destroy retires
// the ID against stale saves.
func TestMiddlewareStaleSaveAfterDestroyKeepsOldIDDead(t *testing.T) {
	t.Parallel()

	driver, id, newRequest := staleSaveSetup(t)
	handler := retiringHandler(cache.New(driver), newRequest,
		func(value *session.Session) {
			value.Destroy()
		},
		func(value *session.Session) {
			if err := value.Put(session.NewField[int]("cart"), 1); err != nil {
				t.Fatal(err)
			}
		})

	response := handler.Record(newRequest())

	if len(response.Cookies()) != 0 || !isTombstone(driver.values[prefix+id]) {
		t.Fatalf("cookies = %v, record = %s", response.Cookies(), driver.values[prefix+id])
	}
}

// TestMiddlewareStaleRenewalKeepsOldIDDead verifies the automatic renewal of
// a session near expiry cannot revive a retired record.
func TestMiddlewareStaleRenewalKeepsOldIDDead(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Minute), map[string]any{
		"user": 1,
	})
	driver := newDriver(t, value)
	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(&http.Cookie{
			Name:  hostCookie,
			Value: value.ID(),
		})

		return request
	}

	handler := retiringHandler(cache.New(driver), newRequest,
		func(value *session.Session) {
			value.Destroy()
		},
		func(*session.Session) {
		})

	response := handler.Record(newRequest())

	if len(response.Cookies()) != 0 || !isTombstone(driver.values[prefix+value.ID()]) {
		t.Fatalf("cookies = %v, record = %s", response.Cookies(), driver.values[prefix+value.ID()])
	}
}

// TestMiddlewareTombstonedIDStartsFreshSession verifies a retired ID loads as
// no session.
func TestMiddlewareTombstonedIDStartsFreshSession(t *testing.T) {
	t.Parallel()

	driver, id, newRequest := staleSaveSetup(t)
	driver.values[prefix+id] = []byte(`{"id":"` + id + `","tombstone":true}`)
	handler := session.Middleware(cache.New(driver))(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		if mustSession(request).ID() == id {
			t.Fatal("tombstoned session was loaded")
		}

		return nil
	}))

	_ = handler.Record(newRequest())
}

// defaultCookieName returns the name of the cookie set by a middleware built
// with opts and no explicit name.
func defaultCookieName(t *testing.T, opts ...func(*session.Builder)) string {
	t.Helper()

	value := session.New(time.Now().Add(time.Hour), nil)
	handler := session.Middleware(cache.New(newDriver(t, value)), opts...)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		if err := mustSession(request).Put(session.NewField[int]("k"), 1); err != nil {
			return err
		}

		return nil
	}))

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if len(response.Cookies()) != 1 {
		t.Fatalf("cookies = %v, want one", response.Cookies())
	}

	return response.Cookies()[0].Name
}

// TestMiddlewareDefaultCookieIsHostPrefixed verifies the secure default name.
func TestMiddlewareDefaultCookieIsHostPrefixed(t *testing.T) {
	t.Parallel()

	if name := defaultCookieName(t); name != "__Host-arkoren.session" {
		t.Fatalf("name = %q", name)
	}
}

// TestMiddlewareDefaultCookieAllowInsecureDropsPrefix verifies the plain name over HTTP.
func TestMiddlewareDefaultCookieAllowInsecureDropsPrefix(t *testing.T) {
	t.Parallel()

	if name := defaultCookieName(t, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	}); name != "arkoren.session" {
		t.Fatalf("name = %q", name)
	}
}

// TestMiddlewareDefaultCookieDomainDropsPrefix verifies the plain name with a domain.
func TestMiddlewareDefaultCookieDomainDropsPrefix(t *testing.T) {
	t.Parallel()

	if name := defaultCookieName(t, func(sessionPolicy *session.Builder) {
		sessionPolicy.Domain("example.com")
	}); name != "arkoren.session" {
		t.Fatalf("name = %q", name)
	}
}

// TestMiddlewareDefaultCookiePathDropsPrefix verifies the plain name with a custom path.
func TestMiddlewareDefaultCookiePathDropsPrefix(t *testing.T) {
	t.Parallel()

	if name := defaultCookieName(t, func(sessionPolicy *session.Builder) {
		sessionPolicy.Path("/app")
	}); name != "arkoren.session" {
		t.Fatalf("name = %q", name)
	}
}

// TestMiddlewareExplicitCookieNameIsKept verifies an explicit name is never prefixed.
func TestMiddlewareExplicitCookieNameIsKept(t *testing.T) {
	t.Parallel()

	if name := defaultCookieName(t, func(sessionPolicy *session.Builder) {
		sessionPolicy.Name("sid")
	}); name != "sid" {
		t.Fatalf("name = %q", name)
	}
}
