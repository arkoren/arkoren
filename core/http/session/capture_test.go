package session_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"arkoren.dev/core/http/session"
)

// ExampleNew demonstrates that later edits to the input map do not change the
// session. Initial values remain available through typed field definitions.
func ExampleNew() {
	theme := session.NewField[string]("theme")
	initial := map[string]any{"theme": "light"}
	current := session.New(time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC), initial)
	initial["theme"] = "dark"

	value, err := current.Get(theme)

	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output: light
}

// TestNewStorageCaptureIgnoresCallerReplacement checks that replacing an input
// member after construction cannot bypass session mutation tracking.
func TestNewStorageCaptureIgnoresCallerReplacement(t *testing.T) {
	t.Parallel()

	storage := map[string]any{"theme": "light"}
	current := session.New(time.Now().Add(time.Hour), storage)
	current.MarkAsUnchanged()

	storage["theme"] = "dark"

	value, err := current.Get(session.NewField[string]("theme"))

	if err != nil || value != "light" {
		t.Fatalf("captured theme = %q, %v; want light", value, err)
	}

	if current.HasChanged() {
		t.Fatal("caller replacement marked the session changed")
	}
}

// TestNewStorageCaptureIgnoresCallerDeletion checks that the session keeps its
// initial members even when the caller removes them from the input map.
func TestNewStorageCaptureIgnoresCallerDeletion(t *testing.T) {
	t.Parallel()

	storage := map[string]any{"theme": "light"}
	current := session.New(time.Now().Add(time.Hour), storage)
	delete(storage, "theme")

	value, err := current.Get(session.NewField[string]("theme"))

	if err != nil || value != "light" {
		t.Fatalf("captured theme = %q, %v; want light", value, err)
	}
}

// TestNewStorageCaptureIgnoresCallerAddition checks that input-map additions
// cannot inject new session fields after the constructor returns.
func TestNewStorageCaptureIgnoresCallerAddition(t *testing.T) {
	t.Parallel()

	storage := map[string]any{}
	current := session.New(time.Now().Add(time.Hour), storage)
	storage["theme"] = "dark"

	_, err := current.Get(session.NewField[string]("theme"))

	if !errors.Is(err, session.ErrKeyNotFound) {
		t.Fatalf("caller-added field error = %v; want ErrKeyNotFound", err)
	}
}

// TestNewStorageCapturePutDoesNotChangeCallerMap checks that an empty input map
// remains caller-owned while Put writes and tracks changes in the session.
func TestNewStorageCapturePutDoesNotChangeCallerMap(t *testing.T) {
	t.Parallel()

	storage := map[string]any{}
	current := session.New(time.Now().Add(time.Hour), storage)
	current.MarkAsUnchanged()

	field := session.NewField[string]("theme")

	if err := current.Put(field, "dark"); err != nil {
		t.Fatal(err)
	}

	if len(storage) != 0 {
		t.Fatalf("caller map after Put = %v; want empty", storage)
	}

	value, err := current.Get(field)

	if err != nil || value != "dark" {
		t.Fatalf("stored theme = %q, %v; want dark", value, err)
	}

	if !current.HasChanged() {
		t.Fatal("Put did not mark the session changed")
	}
}

// TestNewStorageCaptureNilSupportsPut checks that nil means writable empty
// storage, without changing the constructor's initial persistence state.
func TestNewStorageCaptureNilSupportsPut(t *testing.T) {
	t.Parallel()

	current := session.New(time.Now().Add(time.Hour), nil)

	if !current.HasChanged() {
		t.Fatal("new session was not marked changed")
	}

	field := session.NewField[string]("theme")

	if err := current.Put(field, "dark"); err != nil {
		t.Fatal(err)
	}

	value, err := current.Get(field)

	if err != nil || value != "dark" {
		t.Fatalf("stored theme = %q, %v; want dark", value, err)
	}
}

// TestNewStorageCaptureKeepsNestedValuesShared checks that capturing the outer
// map neither clones arbitrary values nor tracks mutations through their aliases.
func TestNewStorageCaptureKeepsNestedValuesShared(t *testing.T) {
	t.Parallel()

	preferences := map[string]string{"theme": "light"}
	current := session.New(time.Now().Add(time.Hour), map[string]any{
		"preferences": preferences,
	})
	current.MarkAsUnchanged()

	preferences["theme"] = "dark"

	value, err := current.Get(session.NewField[map[string]string]("preferences"))

	if err != nil || value["theme"] != "dark" {
		t.Fatalf("shared preferences = %v, %v; want dark theme", value, err)
	}

	if current.HasChanged() {
		t.Fatal("nested alias mutation marked the session changed")
	}
}
