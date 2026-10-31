package request_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// ExampleHooks edits a header before final response commitment.
func ExampleHooks() {
	handler := router.Handler(func(writer http.ResponseWriter, incoming *http.Request) error {
		request.Hooks(incoming).BeforeWriteHeader(func(writer http.ResponseWriter, _ int) {
			writer.Header().Set("Cache-Control", "no-store")
		})

		return response.String(writer, http.StatusOK, "ready")
	})

	writer := httptest.NewRecorder()

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/", nil))
	fmt.Println(writer.Code, writer.Header().Get("Cache-Control"), writer.Body.String())
	// Output: 200 no-store ready
}

// TestHooksRegistersCallbacksForHandlerLifecycle verifies lifecycle callback order.
func TestHooksRegistersCallbacksForHandlerLifecycle(t *testing.T) {
	t.Parallel()

	var order []int
	handler := hooksHandler(&order)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	assertHookOrder(t, order)
}

// hooksHandler returns a handler that registers callbacks for each hook phase.
func hooksHandler(order *[]int) router.Handler {
	return router.Handler(func(writer http.ResponseWriter, r *http.Request) error {
		hooks := request.Hooks(r)
		hooks.BeforeWriteHeader(func(http.ResponseWriter, int) {
			*order = append(*order, 1)
		})
		hooks.BeforeWriteHeader(func(http.ResponseWriter, int) {
			*order = append(*order, 2)
		})
		hooks.BeforeWrite(func(http.ResponseWriter, []byte) {
			*order = append(*order, 3)
		})
		hooks.BeforeWrite(func(http.ResponseWriter, []byte) {
			*order = append(*order, 4)
		})
		hooks.AfterResponse(func(error) {
			*order = append(*order, 5)
		})
		hooks.AfterResponse(func(error) {
			*order = append(*order, 6)
		})

		_, err := writer.Write([]byte("response"))

		return err
	})
}

// assertHookOrder compares the observed callbacks with their required order.
func assertHookOrder(t *testing.T, order []int) {
	t.Helper()

	want := []int{2, 1, 4, 3, 6, 5}

	if len(order) != len(want) {
		t.Fatalf("got %v, want %v", order, want)
	}

	for index, value := range want {
		if order[index] != value {
			t.Fatalf("got %v, want %v", order, want)
		}
	}
}

// TestHooksPanicsWithErrNoHooksMiddleware verifies missing hook context handling.
func TestHooksPanicsWithErrNoHooksMiddleware(t *testing.T) {
	t.Parallel()

	defer func() {
		recovered, ok := recover().(problem.Details)

		if !ok || recovered.Title() != request.ErrNoHooksMiddleware.Title() || recovered.Status() != request.ErrNoHooksMiddleware.Status() {
			t.Fatalf("got %v", recovered)
		}
	}()
	request.Hooks(httptest.NewRequest(http.MethodGet, "/", nil))
}
