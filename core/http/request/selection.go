package request

import "arkoren.dev/core/http/session"

// sessionConfig holds only the per-call context selection. Cookie and lifetime
// policy belongs to session.Middleware, not request lookup.
type sessionConfig struct {
	// handle selects the bridge; zero reads the default session context.
	handle session.Handle
}
