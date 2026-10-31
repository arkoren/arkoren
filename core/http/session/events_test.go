package session_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
)

// TestMiddlewarePublishesExpired verifies loading an expired session
// publishes Expired with its timestamps.
func TestMiddlewarePublishesExpired(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got []session.ExpiredEvent

	if _, err := bus.Listen(session.Expired, func(_ context.Context, payload session.ExpiredEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	value := session.New(time.Now().Add(-time.Minute), map[string]any{})
	driver := newDriver(t, value)
	handler := session.Middleware(cache.New(driver), func(sessionPolicy *session.Builder) {
		sessionPolicy.Events(bus)
	})(router.Handler(
		func(http.ResponseWriter, *http.Request) error {
			return nil
		},
	))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: value.ID(),
	})
	handler.Record(request)

	if len(got) != 1 || !got[0].ExpiresAt.Equal(value.ExpiresAt()) || !got[0].CreatedAt.Equal(value.CreatedAt()) {
		t.Fatalf("Expired = %+v, want one event for the stored session", got)
	}
}

// TestMiddlewarePublishesNothingForValidSession verifies a usable session is
// not published.
func TestMiddlewarePublishesNothingForValidSession(t *testing.T) {
	t.Parallel()

	bus := event.New()
	published := false

	if _, err := bus.Listen(session.Expired, func(context.Context, session.ExpiredEvent) error {
		published = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	handler := session.Middleware(cache.New(newDriver(t, value)), func(sessionPolicy *session.Builder) {
		sessionPolicy.Events(bus)
	})(router.Handler(
		func(http.ResponseWriter, *http.Request) error {
			return nil
		},
	))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: value.ID(),
	})
	handler.Record(request)

	if published {
		t.Fatal("Expired published for a valid session")
	}
}

// persistFailures returns a bus and the slice its PersistFailed payloads are
// appended to.
func persistFailures(t *testing.T) (*event.Bus, *[]session.PersistFailedEvent) {
	t.Helper()

	bus := event.New()
	var failures []session.PersistFailedEvent

	if _, err := bus.Listen(session.PersistFailed, func(_ context.Context, payload session.PersistFailedEvent) error {
		failures = append(failures, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &failures
}

// TestMiddlewarePublishesPersistFailed verifies a failed save publishes the
// operation, method, and path, and still calls the error handler.
func TestMiddlewarePublishesPersistFailed(t *testing.T) {
	t.Parallel()

	bus, failures := persistFailures(t)
	var handled error
	handler := session.Middleware(
		cache.New(&driver{
			err:    errors.New("backend down"),
			values: map[string][]byte{},
		}),
		func(sessionPolicy *session.Builder) {
			sessionPolicy.Events(bus)
		},
		func(cookies *session.Builder) {
			cookies.ErrorHandler(func(err error) {
				handled = err
			})
		},
	)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		if err := mustSession(request).Put(session.NewField[int]("user_id"), 42); err != nil {
			return err
		}

		return nil
	}))

	handler.Record(httptest.NewRequest(http.MethodPost, "/login?token=secret", nil))

	want := session.PersistFailedEvent{
		Operation: session.PersistSave,
		Method:    http.MethodPost,
		Path:      "", // Direct Handler use has no registered route pattern.
	}

	if len(*failures) != 1 || (*failures)[0] != want {
		t.Fatalf("PersistFailed = %+v, want one %+v", *failures, want)
	}

	if handled == nil {
		t.Fatal("ErrorHandler was not called")
	}
}

// TestMiddlewarePersistFailedSurvivesCanceledRequest verifies the event is
// published when the request context is already canceled.
func TestMiddlewarePersistFailedSurvivesCanceledRequest(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var ctxErr error
	published := false

	if _, err := bus.Listen(session.PersistFailed, func(ctx context.Context, _ session.PersistFailedEvent) error {
		published, ctxErr = true, ctx.Err()

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := session.Middleware(
		cache.New(&driver{
			err:    errors.New("backend down"),
			values: map[string][]byte{},
		}),
		func(sessionPolicy *session.Builder) {
			sessionPolicy.Events(bus)
		},
	)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		if err := mustSession(request).Put(session.NewField[int]("user_id"), 42); err != nil {
			return err
		}

		return nil
	}))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler.Record(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	if !published || ctxErr != nil {
		t.Fatalf("published = %v, ctx error = %v, want published with a live context", published, ctxErr)
	}
}

// TestMiddlewarePublishesNothingWhenPersistSucceeds verifies a successful
// save publishes no failure.
func TestMiddlewarePublishesNothingWhenPersistSucceeds(t *testing.T) {
	t.Parallel()

	bus, failures := persistFailures(t)
	handler := session.Middleware(cache.New(newDriver(t)), func(sessionPolicy *session.Builder) {
		sessionPolicy.Events(bus)
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			if err := mustSession(request).Put(session.NewField[int]("user_id"), 42); err != nil {
				return err
			}

			return nil
		},
	))

	handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if len(*failures) != 0 {
		t.Fatalf("PersistFailed = %+v, want none", *failures)
	}
}

// listenDestroyed returns a bus and the slice its Destroyed payloads are
// appended to.
func listenDestroyed(t *testing.T) (*event.Bus, *[]session.DestroyedEvent) {
	t.Helper()

	bus := event.New()
	var got []session.DestroyedEvent

	if _, err := bus.Listen(session.Destroyed, func(_ context.Context, payload session.DestroyedEvent) error {
		got = append(got, payload)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return bus, &got
}

// TestMiddlewarePublishesDestroyed verifies a destroyed session publishes the
// method and path once its record is retired.
func TestMiddlewarePublishesDestroyed(t *testing.T) {
	t.Parallel()

	bus, got := listenDestroyed(t)
	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	driver := newDriver(t, value)
	handler := session.Middleware(cache.New(driver), func(sessionPolicy *session.Builder) {
		sessionPolicy.Events(bus)
	})(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			mustSession(request).Destroy()

			return nil
		},
	))

	request := httptest.NewRequest(http.MethodPost, "/logout?token=secret", nil)
	request.AddCookie(&http.Cookie{
		Name:  hostCookie,
		Value: value.ID(),
	})
	handler.Record(request)

	want := session.DestroyedEvent{
		Method: http.MethodPost,
		Path:   "", // Direct Handler use has no registered route pattern.
	}

	if len(*got) != 1 || (*got)[0] != want || !isTombstone(driver.values[prefix+value.ID()]) {
		t.Fatalf("Destroyed = %+v, want one %+v after the tombstone", *got, want)
	}
}

// TestMiddlewareDestroyedNotPublishedWhenRetireFails verifies a failed
// tombstone publishes PersistFailed and not Destroyed.
func TestMiddlewareDestroyedNotPublishedWhenRetireFails(t *testing.T) {
	t.Parallel()

	bus, destroyed := listenDestroyed(t)
	handler := session.Middleware(
		cache.New(&driver{
			err:    errors.New("backend down"),
			values: map[string][]byte{},
		}),
		func(sessionPolicy *session.Builder) {
			sessionPolicy.Events(bus)
		},
	)(router.Handler(func(_ http.ResponseWriter, request *http.Request) error {
		mustSession(request).Destroy()

		return nil
	}))

	handler.Record(httptest.NewRequest(http.MethodPost, "/logout", nil))

	if len(*destroyed) != 0 {
		t.Fatalf("Destroyed = %+v, want none", *destroyed)
	}
}

// TestMiddlewareDestroyedNotPublishedWithoutDestroy verifies an ordinary
// request publishes no Destroyed event.
func TestMiddlewareDestroyedNotPublishedWithoutDestroy(t *testing.T) {
	t.Parallel()

	bus, destroyed := listenDestroyed(t)
	handler := session.Middleware(cache.New(newDriver(t)), func(sessionPolicy *session.Builder) {
		sessionPolicy.Events(bus)
	}, func(sessionPolicy *session.Builder) {
		sessionPolicy.AllowInsecure(true)
	})(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			if err := mustSession(request).Put(session.NewField[int]("user_id"), 42); err != nil {
				return err
			}

			return nil
		},
	))

	handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if len(*destroyed) != 0 {
		t.Fatalf("Destroyed = %+v, want none", *destroyed)
	}
}

// TestMiddlewareDestroyWithoutBusSucceeds verifies destroying works without a bus.
func TestMiddlewareDestroyWithoutBusSucceeds(t *testing.T) {
	t.Parallel()

	handler := session.Middleware(cache.New(newDriver(t)))(router.Handler(
		func(_ http.ResponseWriter, request *http.Request) error {
			mustSession(request).Destroy()

			return nil
		},
	))

	handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
}
