package request

import (
	"net/http/httptest"
	"testing"
	"time"

	"arkoren.dev/core/http/session"
)

// TestMustSessionDelegatesOptionsOnce counts option execution within the private
// context-key-only draft. It catches replay across the MustSession delegation.
func TestMustSessionDelegatesOptionsOnce(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), map[string]any{})
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(session.NewContext(r.Context(), current))
	calls := 0
	got := MustSession(r, func(config *sessionConfig) {
		calls++
		config.handle = session.Handle{}
	})

	if calls != 1 || got != current {
		t.Fatalf("calls = %d, session = %p, want %p", calls, got, current)
	}
}
