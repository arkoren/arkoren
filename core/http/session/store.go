package session

import (
	"context"
	"encoding/json/jsontext"
	"time"

	"arkoren.dev/core/http/session/internal/persistence"
	"arkoren.dev/core/service/cache"
)

// sessionStore bridges runtime sessions to the private record engine. Only this
// boundary marks decoded values as persisted; runtime JSON keeps its Go type.
type sessionStore struct {
	// Store owns record I/O, exact-byte guards, and retirement coordination.
	persistence.Store
}

// get reconstructs a live record without exposing persisted provenance publicly.
func (store sessionStore) get(ctx context.Context, id string) (*Session, bool, error) {
	stored, found, err := store.Get(ctx, id)

	if err != nil || !found {
		return nil, found, err
	}

	for name, value := range stored.Storage {
		stored.Storage[name] = persistedValue(value.(jsontext.Value))
	}

	return newFrom(stored.ID, stored.CreatedAt, stored.ExpiresAt, stored.Storage), true, nil
}

// save captures runtime fields in the existing order before encoding. A loaded
// session retains its original-record authorization across regeneration.
func (store sessionStore) save(ctx context.Context, value *Session, ttl time.Duration, match cache.Match) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}

	stored := persistence.Record{
		ID: value.ID(), CreatedAt: value.CreatedAt(), ExpiresAt: value.ExpiresAt(), Storage: value.All(),
	}

	return store.Save(ctx, stored, value.loaded, ttl, match)
}
