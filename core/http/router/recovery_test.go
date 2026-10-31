package router_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/router"
)

// TestRecoverCallbacksRunOnceInOrder checks the public constructor, identical
// draft receivers, and replacement order without executing request-time hooks.
func TestRecoverCallbacksRunOnceInOrder(t *testing.T) {
	t.Parallel()

	var retained *router.RecoverBuilder
	order := 0
	router.Recover(func(recovery *router.RecoverBuilder) {
		retained = recovery
		order++

		if recovery.Handler(nil) != recovery {
			t.Fatal("setting returned another draft")
		}
	}, func(recovery *router.RecoverBuilder) {
		if order != 1 || recovery != retained {
			t.Fatal("callbacks were reordered or used different drafts")
		}

		order++
	})

	if order != 2 {
		t.Fatalf("callback count = %d, want 2", order)
	}
}

// TestRecoverOmissionAndEmptyExpansions supply zero callbacks. A supplied nil
// is instead misuse and must panic before a later callback can execute.
func TestRecoverOmissionAndEmptyExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*router.RecoverBuilder)
	router.Recover(omitted...)
	router.Recover([]func(*router.RecoverBuilder){}...)

	reached := false
	defer func() {
		if recover() == nil || reached {
			t.Fatal("nil entry was filtered or later callback ran")
		}
	}()
	router.Recover(nil, func(recovery *router.RecoverBuilder) {
		reached = true
	})
}

// TestRecoverApplicationPanicPropagates keeps callback misuse separate from
// ordinary constructor validation and prevents later policies from executing.
func TestRecoverApplicationPanicPropagates(t *testing.T) {
	t.Parallel()

	reached := false
	defer func() {
		if value := recover(); value != "construction sentinel" || reached {
			t.Fatalf("panic = %v, later callback = %v", value, reached)
		}
	}()
	router.Recover(func(recovery *router.RecoverBuilder) {
		panic("construction sentinel")
	}, func(recovery *router.RecoverBuilder) {
		reached = true
	})
}

// TestMiddlewareRecoversPanic converts a handler panic into a recoverable error.
func TestMiddlewareRecoversPanic(t *testing.T) {
	t.Parallel()

	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic("boom")
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, router.ErrRecoverUnexpected) {
		t.Fatalf("error = %v", err)
	}

	var stack interface{ Stack() []byte }

	if !errors.As(err, &stack) || len(stack.Stack()) == 0 {
		t.Fatal("recovery error has no stack")
	}
}

// TestRecoverUsesCustomHandler verifies RecoverBuilder.Handler converts panics.
func TestRecoverUsesCustomHandler(t *testing.T) {
	t.Parallel()

	errCustom := errors.New("custom")
	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Handler(func(any) error {
			return errCustom
		})
	})(
		func(http.ResponseWriter, *http.Request) error {
			panic("boom")
		},
	)

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, errCustom) {
		t.Fatalf("error = %v", err)
	}
}

// TestRecoverNilHandlerResultRemainsAnError verifies an accidental nil result
// cannot turn a panic into success or an error whose Error method panics.
func TestRecoverNilHandlerResultRemainsAnError(t *testing.T) {
	t.Parallel()

	handler := router.Recover(func(recovery *router.RecoverBuilder) {
		recovery.Handler(func(any) error {
			return nil
		})
	})(func(http.ResponseWriter, *http.Request) error {
		panic("private panic value")
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(err, router.ErrRecoverUnexpected) {
		t.Fatalf("error = %v, want ErrRecoverUnexpected", err)
	}

	if err.Error() != router.ErrRecoverUnexpected.Error() {
		t.Fatalf("error text = %q", err.Error())
	}

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
}

// TestRecoverStackReturnsOwnedCopies verifies callers can retain or redact a
// diagnostic stack without mutating subsequent reads of the recovered error.
func TestRecoverStackReturnsOwnedCopies(t *testing.T) {
	t.Parallel()

	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic("boom")
	})

	err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var diagnostic interface{ Stack() []byte }

	if !errors.As(err, &diagnostic) {
		t.Fatalf("error has no diagnostic stack: %v", err)
	}

	first := diagnostic.Stack()

	if len(first) == 0 {
		t.Fatal("empty diagnostic stack")
	}

	want := bytes.Clone(first)
	first[0] ^= 0xff

	if !bytes.Equal(diagnostic.Stack(), want) {
		t.Fatal("mutating a returned stack changed the recovered error")
	}
}

// TestRecoverKeepsDiagnosticOutOfHTTPBody verifies the ordinary panic cause
// remains available server-side without entering the generated problem body.
func TestRecoverKeepsDiagnosticOutOfHTTPBody(t *testing.T) {
	t.Parallel()

	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic("private diagnostic fixture")
	})

	response := handler.Record(httptest.NewRequest(http.MethodGet, "/", nil))
	t.Cleanup(func() {
		_ = response.Body.Close()
	})

	body, err := io.ReadAll(response.Body)

	if err != nil {
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusInternalServerError || bytes.Contains(body, []byte("private diagnostic fixture")) {
		t.Fatalf("status = %d, body = %q", response.StatusCode, body)
	}
}

// TestRecoverReraisesAbortHandler checks that recovery preserves the HTTP
// server abort sentinel instead of converting it into a normal problem
// response.
func TestRecoverReraisesAbortHandler(t *testing.T) {
	t.Parallel()

	handler := router.Recover()(func(http.ResponseWriter, *http.Request) error {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		if err, _ := recover().(error); !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered = %v, want http.ErrAbortHandler", err)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
