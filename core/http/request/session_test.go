package request_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"arkoren.dev/core/http/request"
	"arkoren.dev/core/http/session"
)

// TestSessionReturnsDefaultSession verifies default-key session retrieval.
func TestSessionReturnsDefaultSession(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/", nil).
		WithContext(session.NewContext(context.Background(), value))
	result, ok := request.Session(req)

	if !ok || result != value {
		t.Fatal("request did not return its session")
	}
}

// TestSessionReturnsKeyedSession verifies WithSessionHandle selects the bridge.
func TestSessionReturnsKeyedSession(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContextFor(req.Context(), handle, value))

	result, ok := request.Session(req, request.WithSessionHandle(handle))
	_, defaultFound := request.Session(req)

	if !ok || result != value || defaultFound {
		t.Fatal("keyed session lookup was wrong")
	}
}

// TestSessionMissingReturnsFalse verifies absent sessions are reported.
func TestSessionMissingReturnsFalse(t *testing.T) {
	t.Parallel()

	_, ok := request.Session(httptest.NewRequest(http.MethodGet, "/", nil))

	if ok {
		t.Fatal("session found on bare request")
	}
}

// TestMustSessionPanicsWithErrNoSession verifies missing default sessions panic.
func TestMustSessionPanicsWithErrNoSession(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	defer func() {
		if !reflect.DeepEqual(recover(), session.ErrNoSession) {
			t.Fatal("did not panic with ErrNoSession")
		}
	}()

	request.MustSession(req)
}

// TestMustSessionKeyedPanicsWithErrNoSession verifies missing keyed sessions panic.
func TestMustSessionKeyedPanicsWithErrNoSession(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	defer func() {
		if !reflect.DeepEqual(recover(), session.ErrNoSession) {
			t.Fatal("did not panic with ErrNoSession")
		}
	}()

	request.MustSession(req, request.WithSessionHandle(handle))
}

// TestSessionLaterZeroRestoresDefault verifies that a later zero handle resets
// the selection without falling back from an absent selected session.
func TestSessionLaterZeroRestoresDefault(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(req.Context(), value))
	got := request.MustSession(req, request.WithSessionHandle(session.NewHandle()), request.WithSessionHandle(session.Handle{}))

	if got != value {
		t.Fatal("zero did not restore default lookup")
	}
}

// TestSessionLaterHandleReplacesEarlierHandle returns only the last bridge,
// even when the earlier selection is absent.
func TestSessionLaterHandleReplacesEarlierHandle(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContextFor(req.Context(), handle, value))
	got, ok := request.Session(req, request.WithSessionHandle(session.NewHandle()), request.WithSessionHandle(handle))

	if !ok || got != value {
		t.Fatal("later typed key did not select the store")
	}
}

// TestSessionEmptyOptionsPreserveDefault ensures nil and empty expanded lists
// supply zero policies and preserve default selection through MustSession.
func TestSessionEmptyOptionsPreserveDefault(t *testing.T) {
	t.Parallel()

	value := session.New(time.Now().Add(time.Hour), map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(session.NewContext(req.Context(), value))
	var omitted []request.SessionOption

	if request.MustSession(req, omitted...) != value || request.MustSession(req, []request.SessionOption{}...) != value {
		t.Fatal("empty options changed selection")
	}
}

// TestSessionNilOptionPanics keeps a supplied nil entry distinct from omission.
func TestSessionNilOptionPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("nil option was filtered")
		}
	}()
	_, _ = request.Session(httptest.NewRequest(http.MethodGet, "/", nil), nil)
}

// TestSessionAbsentHandleDoesNotFallback verifies a populated default cannot
// satisfy another selected bridge, including an explicitly stored nil pointer.
func TestSessionAbsentHandleDoesNotFallback(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := session.NewContext(r.Context(), session.New(time.Now().Add(time.Hour), map[string]any{}))
	r = r.WithContext(ctx)

	if current, ok := request.Session(r, request.WithSessionHandle(handle)); ok || current != nil {
		t.Fatal("absent handle fell back to default")
	}

	r = r.WithContext(session.NewContextFor(ctx, handle, nil))

	if current, ok := request.Session(r, request.WithSessionHandle(handle)); ok || current != nil {
		t.Fatal("nil selected session fell back to default")
	}
}
