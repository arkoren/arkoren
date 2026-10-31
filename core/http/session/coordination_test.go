package session_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
	"arkoren.dev/core/service/cache/memorycache"
)

// lingerAfterLiveCheck is how long the racing driver gives a concurrent
// logout to finish inside the window between a save's liveness check and its
// write. A logout that has to wait for the session lock never finishes in
// time, which is the point.
const lingerAfterLiveCheck = 100 * time.Millisecond

// racingDriver runs hook right after the second read of a session record
// while armed, which is the liveness check of a save, so a test can place a
// logout exactly between the check and the write.
type racingDriver struct {
	// Driver supplies cache behavior except for the coordinated Get call that
	// exposes the session race.
	cache.Driver

	// armed enables the hook.
	armed atomic.Bool

	// reads counts session record reads since the driver was armed.
	reads atomic.Int32

	// hook runs once, after the liveness check returns.
	hook func()
}

// Get reads key and fires the hook after the liveness check of a save.
func (driver *racingDriver) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := driver.Driver.Get(ctx, key)

	if driver.armed.Load() && strings.HasPrefix(key, prefix) && driver.reads.Add(1) == 2 {
		driver.hook()
	}

	return raw, err
}

// TestMiddlewareLogoutBetweenLiveCheckAndSaveIsNotOverwritten verifies a
// logout that runs between a stale request's liveness check and its save
// still retires the session: the logout waits for the session lock and
// tombstones the record after the save instead of being overwritten by it.
func TestMiddlewareLogoutBetweenLiveCheckAndSaveIsNotOverwritten(t *testing.T) {
	t.Parallel()

	driver := &racingDriver{
		Driver: memorycache.New(),
	}
	store := cache.New(driver)
	logout := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		mustSession(request).Destroy()

		return nil
	}))

	handler := session.Middleware(store)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		if err := mustSession(request).Put(session.NewField[int]("cart"), 1); err != nil {
			return err
		}

		return nil
	}))

	first := httptest.NewRequest(http.MethodGet, "/", nil)

	created := handler.Record(first)

	if len(created.Cookies()) != 1 {
		t.Fatalf("cookies = %v, want one", created.Cookies())
	}

	cookie := created.Cookies()[0]
	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(cookie)

		return request
	}

	done := make(chan struct{})
	driver.hook = func() {
		go func() {
			defer close(done)

			_ = logout.Record(newRequest())
		}()

		select {
		case <-done:
		case <-time.After(lingerAfterLiveCheck):
		}
	}
	driver.armed.Store(true)

	_ = handler.Record(newRequest())
	<-done

	raw, err := driver.Driver.Get(t.Context(), prefix+cookie.Value)

	if err != nil || !isTombstone(raw) {
		t.Fatalf("record = %s, %v, want tombstone", raw, err)
	}
}
