package session_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// retirementSavePauseKey selects the request whose storage write must remain
// pending while another middleware instance retires its original ID.
type retirementSavePauseKey struct{}

// retirementPendingDriver pauses selected writes at the exported cache.Driver
// seam, before delegating to real memorycache atomic operations. Both old Put
// and guarded writes are intercepted so the regression also exposes the bug
// when session persistence is reverted to its unfenced implementation.
type retirementPendingDriver struct {
	// Driver supplies real, concurrency-safe storage and expiring leases.
	cache.Driver

	// pending closes when the selected write reaches the adapter.
	pending chan struct{}

	// resume releases that write after another request finishes retirement.
	resume chan struct{}

	// once limits the pause to the selected save. A subsequent retirement
	// by that same regenerating request must not reuse the barrier.
	once sync.Once
}

// pauseWrite holds the first marked write until the retirement request is
// complete. Unmarked requests never consume the once-only barrier.
func (driver *retirementPendingDriver) pauseWrite(ctx context.Context) {
	if ctx.Value(retirementSavePauseKey{}) != nil {
		driver.once.Do(func() {
			close(driver.pending)
			<-driver.resume
		})
	}
}

// Put exposes the unfenced implementation's pending write without altering
// ordinary seed or retirement writes. The context remains valid on release.
func (driver *retirementPendingDriver) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	driver.pauseWrite(ctx)

	return driver.Driver.Put(ctx, key, value, ttl)
}

// CompareAndPut pauses before the guard is evaluated, modeling a storage call
// delayed beyond its caller's lease but still within its persistence deadline.
func (driver *retirementPendingDriver) CompareAndPut(ctx context.Context, match cache.Match, entry cache.Entry) (bool, error) {
	driver.pauseWrite(ctx)

	return driver.Driver.CompareAndPut(ctx, match, entry)
}

// TestMiddlewareRetirementFencesSaveAfterLeaseExpiry exercises public
// middleware, cookie replay, and session lookup, not private store internals.
// Destroy must remain effective after a same-ID save outlives its lease.
func TestMiddlewareRetirementFencesSaveAfterLeaseExpiry(t *testing.T) {
	t.Parallel()

	checkRetirementFence(t, func(current *session.Session) error {
		return current.Put(session.NewField[int]("cart"), 1)
	})
}

// TestMiddlewareRetirementFencesRegenerationAfterLeaseExpiry checks the
// different-key case: a stale request cannot publish a new authenticated ID
// after another request successfully destroys its original session.
func TestMiddlewareRetirementFencesRegenerationAfterLeaseExpiry(t *testing.T) {
	t.Parallel()

	checkRetirementFence(t, func(current *session.Session) error {
		current.Regenerate()

		return nil
	})
}

// checkRetirementFence runs one deterministic late-write scenario through the
// public HTTP/session/cache seams. Fake time expires only the ten-second lease,
// not either request's permitted one-minute persistence context. Both the old
// cookie and a proposed regenerated cookie must remain unusable after retirement.
func checkRetirementFence(t *testing.T, edit func(*session.Session) error) {
	t.Helper()

	synctest.Test(t, func(t *testing.T) {
		driver := &retirementPendingDriver{
			Driver:  memorycache.New(),
			pending: make(chan struct{}),
			resume:  make(chan struct{}),
		}
		t.Cleanup(func() {
			select {
			case <-driver.resume:
			default:
				close(driver.resume)
			}
		})

		store := cache.New(driver)
		seed := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			return mustSession(request).Put(session.NewField[int]("user_id"), 42)
		}))

		created := seed.Record(httptest.NewRequest(http.MethodPost, "/login", nil))

		if len(created.Cookies()) != 1 {
			t.Fatalf("seed cookies = %v, want one", created.Cookies())
		}

		cookie := created.Cookies()[0]
		failures := make(chan error, 4)
		configure := func(cookies *session.Builder) {
			cookies.PersistenceTimeout(time.Minute)
			cookies.ErrorHandler(func(err error) {
				failures <- err
			})
		}

		destination := ""
		stale := session.Middleware(store, configure)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			current := mustSession(request)

			err := edit(current)
			destination = current.ID()

			return err
		}))

		logout := session.Middleware(store, configure)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
			mustSession(request).Destroy()

			return nil
		}))

		request := httptest.NewRequest(http.MethodPost, "/cart", nil)
		request.AddCookie(cookie)

		request = request.WithContext(context.WithValue(request.Context(), retirementSavePauseKey{}, true))
		completed := make(chan *http.Response, 1)

		go func() {
			completed <- stale.Record(request)
		}()

		<-driver.pending

		time.Sleep(11 * time.Second)

		retire := httptest.NewRequest(http.MethodPost, "/logout", nil)
		retire.AddCookie(cookie)
		logout.Record(retire)

		afterRetirement := replayRetirementCookie(t, store, cookie)
		close(driver.resume)

		response := <-completed

		if afterRetirement == cookie.Value {
			t.Fatal("logout did not retire the original ID before the pending save resumed")
		}

		if len(failures) != 0 {
			t.Fatalf("persistence failed instead of exercising retirement fencing: %v", <-failures)
		}

		if replayRetirementCookie(t, store, cookie) == cookie.Value {
			t.Fatal("pending save revived the retired ID")
		}

		proposed := &http.Cookie{Name: cookie.Name, Value: destination}

		if replayRetirementCookie(t, store, proposed) == destination {
			t.Fatal("pending save published a usable ID after its original was retired")
		}

		if len(response.Cookies()) != 0 {
			t.Fatalf("stale save issued a cookie after retirement: %v", response.Cookies())
		}
	})
}

// replayRetirementCookie observes the session ID selected by public middleware
// for an incoming cookie. The handler makes no changes, so rejected cookies
// produce an unused fresh session without writing another record or cookie.
func replayRetirementCookie(t *testing.T, store *cache.Cache, cookie *http.Cookie) string {
	t.Helper()

	selected := ""
	reader := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		selected = mustSession(request).ID()

		return nil
	}))

	request := httptest.NewRequest(http.MethodGet, "/account", nil)
	request.AddCookie(cookie)

	response := reader.Record(request)

	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("replay status = %d, want 204", response.StatusCode)
	}

	return selected
}

// retirementUnsupportedDriver represents a custom adapter that cannot provide
// the required atomic primitive. Ordinary storage remains real memorycache;
// loaded-session persistence must fail without an unconditional-write fallback.
type retirementUnsupportedDriver struct {
	// Driver supplies the unchanged seed, read, and lease behavior.
	cache.Driver
}

// CompareAndPut rejects guarded writes explicitly so the middleware can report
// the storage failure rather than quietly relying on lease ownership alone.
func (driver retirementUnsupportedDriver) CompareAndPut(context.Context, cache.Match, cache.Entry) (bool, error) {
	return false, cache.ErrUnsupported
}

// TestMiddlewareRetirementGuardFailureDoesNotFallBack proves a rejected atomic
// write neither issues the regenerated cookie nor retires the still-live source.
// All observations go through public middleware and cookie/session selection.
func TestMiddlewareRetirementGuardFailureDoesNotFallBack(t *testing.T) {
	t.Parallel()

	store := cache.New(retirementUnsupportedDriver{Driver: memorycache.New()})
	seed := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		return mustSession(request).Put(session.NewField[int]("user_id"), 42)
	}))

	created := seed.Record(httptest.NewRequest(http.MethodPost, "/login", nil))

	if len(created.Cookies()) != 1 {
		t.Fatalf("seed cookies = %v, want one", created.Cookies())
	}

	cookie := created.Cookies()[0]
	var handled error
	proposed := ""
	handler := session.Middleware(store, func(cookies *session.Builder) {
		cookies.ErrorHandler(func(err error) {
			handled = err
		})
	})(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		current := mustSession(request)
		current.Regenerate()

		proposed = current.ID()

		return nil
	}))

	request := httptest.NewRequest(http.MethodPost, "/rotate", nil)
	request.AddCookie(cookie)

	response := handler.Record(request)

	if !errors.Is(handled, cache.ErrUnsupported) || len(response.Cookies()) != 0 {
		t.Fatalf("guard failure = %v, cookies = %v; want unsupported and no cookie", handled, response.Cookies())
	}

	if replayRetirementCookie(t, store, cookie) != cookie.Value {
		t.Fatal("guard failure retired the original session")
	}

	if replayRetirementCookie(t, store, &http.Cookie{Name: cookie.Name, Value: proposed}) == proposed {
		t.Fatal("unsupported guard fell back to an unconditional regenerated write")
	}
}
