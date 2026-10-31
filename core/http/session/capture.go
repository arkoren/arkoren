package session

import (
	"maps"
	"time"
)

// New creates a session expiring at expiresAt with the given initial data
// and a random ID. It is marked as changed so the first save persists it.
// Nil storage creates an empty, writable session.
//
// New copies the outer storage map. Do not mutate that map during construction;
// after New returns, caller map edits and session writes are independent.
// Values are retained without recursive copying, so nested maps, slices, and
// pointers may remain shared. Callers must synchronize access to shared mutable
// values: the session mutex protects its fields and outer map, not those values.
// Mutations through shared values do not mark the session as changed; use
// [Session.Put] to replace a value and record a change.
//
// Example:
//
//	theme := session.NewField[string]("theme")
//	initial := map[string]any{"theme": "light"}
//	current := session.New(time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC), initial)
//	initial["theme"] = "dark"
//	value, err := current.Get(theme)
//	if err != nil {
//		fmt.Println(err)
//		return
//	}
//	fmt.Println(value) // light
func New(expiresAt time.Time, storage map[string]any) *Session {
	id := generateSessionID()

	// snapshot owns a writable outer map even for nil input, not nested values.
	snapshot := make(map[string]any, len(storage))
	maps.Copy(snapshot, storage)

	return &Session{
		originalID: id,
		id:         id,
		createdAt:  time.Now(),
		expiresAt:  expiresAt,
		storage:    snapshot,
		changed:    true,
	}
}
