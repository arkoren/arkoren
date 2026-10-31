package router_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
)

// TestRecoverPublishesPanicRecovered verifies a recovered panic is published
// with the request method and the stack.
func TestRecoverPublishesPanicRecovered(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var got router.PanicRecoveredEvent

	if _, err := bus.Listen(router.PanicRecovered, func(_ context.Context, payload router.PanicRecoveredEvent) error {
		got = payload

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Events(bus)
	})(func(http.ResponseWriter, *http.Request) error {
		panic("boom")
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	if got.Method != http.MethodPost || !errors.Is(got.Err, router.ErrRecoverUnexpected) || len(got.Stack) == 0 || !errors.Is(err, router.ErrRecoverUnexpected) {
		t.Fatalf("event = %+v, err = %v", got, err)
	}
}

// TestRecoverEventStackDoesNotAliasReturnedError verifies listener-owned stack
// bytes can be redacted without corrupting the returned diagnostic error.
func TestRecoverEventStackDoesNotAliasReturnedError(t *testing.T) {
	t.Parallel()

	bus := event.New()
	var want []byte

	if _, err := bus.Listen(router.PanicRecovered, func(_ context.Context, payload router.PanicRecoveredEvent) error {
		want = bytes.Clone(payload.Stack)

		if len(payload.Stack) != 0 {
			payload.Stack[0] ^= 0xff
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Events(bus)
	})(func(http.ResponseWriter, *http.Request) error {
		panic("boom")
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var diagnostic interface{ Stack() []byte }

	if !errors.As(err, &diagnostic) {
		t.Fatalf("error has no diagnostic stack: %v", err)
	}

	if len(want) == 0 || !bytes.Equal(diagnostic.Stack(), want) {
		t.Fatal("event mutation corrupted the returned diagnostic stack")
	}
}

// TestRecoverAbortHandlerDoesNotPublish verifies the server abort sentinel
// remains a control-flow panic, never a recovered diagnostic notification.
func TestRecoverAbortHandlerDoesNotPublish(t *testing.T) {
	t.Parallel()

	bus := event.New()
	called := false

	if _, err := bus.Listen(router.PanicRecovered, func(context.Context, router.PanicRecoveredEvent) error {
		called = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Events(bus)
	})(func(http.ResponseWriter, *http.Request) error {
		panic(http.ErrAbortHandler)
	})
	defer func() {
		if got, ok := recover().(error); !ok || !errors.Is(got, http.ErrAbortHandler) {
			t.Fatalf("panic = %v, want ErrAbortHandler", got)
		}

		if called {
			t.Fatal("abort handler published a recovered panic event")
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
