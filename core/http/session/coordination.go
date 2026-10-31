package session

import (
	"context"
	"net/http"
	"time"

	"arkoren.dev/core/service/cache"
)

// saveChangedSession persists session and writes its cookie when it changed.
// A session loaded from the store is saved only while its original record is
// still live; if logout, destroy, or regeneration retired it meanwhile, the
// save and the cookie are dropped. A conflicting update after verification
// also drops the save rather than retrying stale state. Storage failures are
// reported; a guard mismatch is a normal rejection, not a persistence error.
// It reports whether a save happened.
func saveChangedSession(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	driver sessionStore,
	config settings,
	runtime middlewareRuntime,
	sameSite http.SameSite,
	session *Session,
) bool {
	if !session.HasChanged() || session.HasExpired() {
		return false
	}

	// match is populated only for loaded sessions. Fresh sessions have no
	// existing record to authorize their first write.
	var match cache.Match

	if session.loaded {
		// The lease reduces concurrent update conflicts. The atomic match
		// below, not this lease, prevents a save from bypassing retirement.
		lease, err := driver.Lock(ctx, session.OriginalID())

		if err != nil {
			reportPersistenceError(ctx, r, config, runtime, PersistVerify, err)

			return false
		}

		defer driver.Unlock(ctx, lease)

		var live bool

		match, live, err = driver.Match(ctx, session.OriginalID())

		if err != nil {
			reportPersistenceError(ctx, r, config, runtime, PersistVerify, err)

			return false
		}

		if !live {
			return false
		}
	}

	ttl := time.Until(session.ExpiresAt())

	saved, err := driver.save(ctx, session, ttl, match)

	if err != nil {
		reportPersistenceError(ctx, r, config, runtime, PersistSave, err)

		return false
	}

	if !saved {
		return false
	}

	setSessionCookie(w, config, sameSite, session, ttl)

	return true
}
