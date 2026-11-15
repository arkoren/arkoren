package federation

import (
	"context"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/http/session"
)

// ConnectIntentTTL is how long a connect intent stays valid: the time the user
// has to finish signing in at the provider after starting a connection (ten
// minutes).
//
// Example:
//
//	deadline := time.Now().Add(federation.ConnectIntentTTL)
const ConnectIntentTTL = 10 * time.Minute

// connectField is the session field that holds the connect intent.
var connectField = session.NewField[connectIntent]("arkoren:federation:connect")

// connectIntent records that a logged-in user asked to connect a provider: the
// next callback of that session may link its identity to this subject.
type connectIntent struct {
	// Subject is the user who started the connection.
	Subject string `json:"subject"`

	// ExpiresAt is when the intent stops being honored, in Unix seconds.
	ExpiresAt int64 `json:"expires_at"`
}

// validFor reports whether the intent belongs to subject and has not expired at
// now. An intent with another subject belongs to a user who has since logged
// out and been replaced in the session; it never links anything.
func (intent connectIntent) validFor(subject string, now time.Time) bool {
	return intent.Subject == subject && now.Unix() < intent.ExpiresAt
}

// takeIntent returns the subject of the connect intent in the session of ctx,
// provided it is unexpired and belongs to the logged-in user, and removes it:
// an intent serves one callback whatever its outcome. It returns "" when there
// is none.
func (links *Links) takeIntent(ctx context.Context) string {
	current, ok := session.FromContext(ctx)
	if !ok {
		return ""
	}
	intent, err := current.Get(connectField)
	if err != nil {
		return ""
	}
	// Deleting can only fail for an invalid field; the intent is unusable then
	// anyway, and a stale one still expires by its own time.
	_ = current.Delete(connectField)
	identity, ok := auth.FromContext(ctx)
	if !ok || !identity.Session() || !intent.validFor(identity.Subject(), time.Now()) {
		return ""
	}

	return intent.Subject
}
