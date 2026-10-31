package session_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/http/session"
)

// TestHandleCopiesAndDistinctCalls verifies opaque selectors are comparable,
// copied by value, and unique across independent constructor calls.
func TestHandleCopiesAndDistinctCalls(t *testing.T) {
	t.Parallel()

	first := session.NewHandle()
	copy := first

	if copy != first || first == session.NewHandle() || first == (session.Handle{}) {
		t.Fatal("handle identity or zero semantics changed")
	}
}

// TestHandleConcurrentUniqueness exercises independent allocations in parallel,
// retaining every selector so no live address can be reused.
func TestHandleConcurrentUniqueness(t *testing.T) {
	t.Parallel()

	handles := make(chan session.Handle, 1000)
	var workers sync.WaitGroup

	for range 1000 {
		workers.Go(func() {
			handles <- session.NewHandle()
		})
	}

	workers.Wait()
	close(handles)

	seen := make(map[session.Handle]bool)

	for handle := range handles {
		if seen[handle] || handle == (session.Handle{}) {
			t.Fatal("constructor reused a live handle")
		}

		seen[handle] = true
	}
}

// TestHandleContextIsolation verifies selected, default and absent bridges stay
// distinct and a copied handle retrieves the same pointer.
func TestHandleContextIsolation(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	copy := handle
	defaultSession := session.New(time.Now().Add(time.Hour), map[string]any{})
	selected := session.New(time.Now().Add(time.Hour), map[string]any{})
	ctx := session.NewContext(context.Background(), defaultSession)
	ctx = session.NewContextFor(ctx, handle, selected)

	if value, ok := session.FromContextFor(ctx, copy); !ok || value != selected {
		t.Fatal("copy did not select its session")
	}

	if value, ok := session.FromContextFor(ctx, session.Handle{}); !ok || value != defaultSession {
		t.Fatal("zero did not select default")
	}

	if value, ok := session.FromContextFor(ctx, session.NewHandle()); ok || value != nil {
		t.Fatal("absent handle fell back")
	}
}

// TestHandleRawContextKeyDoesNotPopulateBridge verifies casual context keys
// cannot accidentally satisfy the official private context-key wrapper.
func TestHandleRawContextKeyDoesNotPopulateBridge(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	current := session.New(time.Now().Add(time.Hour), map[string]any{})
	ctx := context.WithValue(context.Background(), handle, current)

	if value, ok := session.FromContextFor(ctx, handle); ok || value != nil {
		t.Fatal("raw context key populated official bridge")
	}
}

// TestHandleNilSessionIsAbsent verifies typed nil values hide earlier values
// without becoming present sessions in default or selected lookup.
func TestHandleNilSessionIsAbsent(t *testing.T) {
	t.Parallel()

	handle := session.NewHandle()
	ctx := session.NewContext(context.Background(), nil)
	ctx = session.NewContextFor(ctx, handle, nil)

	if value, ok := session.FromContext(ctx); ok || value != nil {
		t.Fatal("nil default session was present")
	}

	if value, ok := session.FromContextFor(ctx, handle); ok || value != nil {
		t.Fatal("nil selected session was present")
	}
}
