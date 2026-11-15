package apitoken

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"arkoren.dev/core/auth"
	"arkoren.dev/core/service/database"
	"arkoren.dev/foundation/query"

	"arkoren.dev/ark/access/internal/storage"
)

// parse splits a token text into its id and secret. ok is false for text that
// does not have the shape of a token of this issuer; recognized is false when
// the prefix is not ours, so another authenticator may try the text.
func (issuer *Issuer) parse(text string) (id, secret string, recognized, ok bool) {
	rest, found := strings.CutPrefix(text, issuer.prefix+"_")
	if !found {
		return "", "", false, false
	}
	id, secret, found = strings.Cut(rest, "_")

	return id, secret, true, found && len(id) == idLength && len(secret) == secretLength
}

// Authenticator returns the authenticator for bearer tokens: install it with
// [auth.Middleware], next to or instead of the guard's. A request without a
// bearer token, or with one that does not start with the prefix, passes through
// anonymously (so another authenticator, such as a JWT one, can try); a
// malformed, unknown, expired, revoked or outdated token is
// [auth.ErrUnauthenticated] with a Bearer challenge. The identity carries the
// token's scopes and the user's record, and no factors: it is never a browser
// session, [auth.RequireLevel] rejects it, and the credential handlers of this
// module refuse it.
//
// Example:
//
//	api := routes.With(auth.Middleware(issuer.Authenticator()), auth.Require())
//	api.With(gate.Require(gate.HasScopes("posts:write"))).Post("/api/posts", createPost)
func (issuer *Issuer) Authenticator() auth.Authenticator {
	return auth.Bearer(issuer.authenticate)
}

// authenticate maps the text of a bearer token to the identity of its user.
func (issuer *Issuer) authenticate(ctx context.Context, text string) (auth.Identity, error) {
	stored, secret, err := issuer.find(ctx, text)
	if err != nil {
		return auth.Identity{}, err
	}
	now := time.Now().Unix()
	if !stored.valid(secret, now, issuer.deviceIdle) {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	record, err := issuer.user(ctx, stored)
	if err != nil {
		return auth.Identity{}, err
	}
	issuer.touch(ctx, stored, now)

	return auth.NewIdentity(stored.Subject, func(principal *auth.IdentityBuilder) {
		principal.Scopes(stored.token().Scopes...).Record(record)
	}), nil
}

// find loads the row of the token text and returns it with the presented
// secret. Text with another prefix is [auth.ErrNoCredentials], so another
// authenticator may try it; a malformed or unknown token is
// [auth.ErrUnauthenticated].
func (issuer *Issuer) find(ctx context.Context, text string) (row, string, error) {
	id, secret, recognized, ok := issuer.parse(text)
	if !recognized {
		return row{}, "", auth.ErrNoCredentials
	}
	if !ok {
		return row{}, "", auth.ErrUnauthenticated
	}
	stored, err := issuer.db.Find[row](ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("id", id).Limit(1)
	}))
	if errors.Is(err, database.ErrNoRows) {
		return row{}, "", auth.ErrUnauthenticated
	}
	if err != nil {
		return row{}, "", fmt.Errorf("apitoken: load token: %w", err)
	}

	return stored, secret, nil
}

// valid reports whether secret is the token's, compared in constant time, the
// token has not expired at now (Unix seconds) and, for a device token, has not
// been idle longer than idle (zero means no limit). A device token that was
// never used counts its idle time from its creation.
func (row row) valid(secret string, now int64, idle time.Duration) bool {
	if subtle.ConstantTimeCompare([]byte(storage.Hash(secret)), []byte(row.SecretHash)) != 1 {
		return false
	}
	if row.ExpiresAt != 0 && row.ExpiresAt <= now {
		return false
	}
	if row.Kind == string(KindDevice) && idle > 0 {
		lastUse := cmp.Or(row.LastUsedAt, row.CreatedAt)

		return now-lastUse <= int64(idle/time.Second)
	}

	return true
}

// user returns the record of the token's user. A deleted or disabled user, or
// one whose version changed since the token was issued, is
// [auth.ErrUnauthenticated].
func (issuer *Issuer) user(ctx context.Context, stored row) (auth.UserRecord, error) {
	record, err := issuer.provider.Find(ctx, stored.Subject)
	if errors.Is(err, auth.ErrUserNotFound) || errors.Is(err, auth.ErrUserDisabled) {
		return auth.UserRecord{}, auth.ErrUnauthenticated
	}
	if err != nil {
		return auth.UserRecord{}, err
	}
	if subtle.ConstantTimeCompare([]byte(storage.Hash(record.Version)), []byte(stored.UserVersion)) != 1 {
		return auth.UserRecord{}, auth.ErrUnauthenticated
	}

	return record, nil
}

// touch records that the token was used at now, at most once per
// touchInterval. The timestamp is informational: a failed write must not fail
// the request, and the next use retries it.
func (issuer *Issuer) touch(ctx context.Context, stored row, now int64) {
	if stored.LastUsedAt >= now-int64(touchInterval/time.Second) {
		return
	}
	_, _ = issuer.db.Exec(ctx, query.Table(issuer.table, func(statement *query.Builder) {
		statement.Where("id", stored.ID).Update(map[string]any{"last_used_at": now})
	}))
}

// presentedID returns the id of the bearer token r presented, or false when the
// request carries none of this issuer's tokens.
func (issuer *Issuer) presentedID(r *http.Request) (string, bool) {
	scheme, text, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	id, _, recognized, ok := issuer.parse(text)

	return id, recognized && ok
}
