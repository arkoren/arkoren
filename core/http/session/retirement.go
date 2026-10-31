package session

import (
	"context"
	"net/http"
)

// persistSession creates the callback that persists session changes.
func persistSession(
	w http.ResponseWriter,
	r *http.Request,
	session *Session,
	driver sessionStore,
	config settings,
	runtime middlewareRuntime,
	sameSite http.SameSite,
) func(http.ResponseWriter, int) {
	return func(_ http.ResponseWriter, status int) {
		if status >= http.StatusInternalServerError {
			return
		}

		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), runtime.PersistenceTimeout)
		defer cancel()

		if session.Destroyed() {
			err := driver.Tombstone(ctx, session.OriginalID(), tombstoneTTL(config))
			reportPersistenceError(ctx, r, config, runtime, PersistRetire, err)

			if err == nil {
				config.Events.Emit(ctx, Destroyed, DestroyedEvent{
					Method: r.Method,
					Path:   sessionEventPath(r),
				})
			}

			expireSessionCookie(w, config, sameSite)

			return
		}

		renewSession(config, session)

		if !saveChangedSession(ctx, w, r, driver, config, runtime, sameSite, session) {
			return
		}

		// The old ID is retired only after the new record is saved, so a
		// failed save leaves the user's existing session intact. The
		// tombstone keeps stale requests from re-creating the old record.
		if session.HasRegenerated() {
			reportPersistenceError(ctx, r, config, runtime, PersistRetire, driver.Tombstone(ctx, session.OriginalID(), tombstoneTTL(config)))
		}
	}
}
