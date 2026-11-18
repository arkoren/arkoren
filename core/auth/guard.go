package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
)

const (
	// maxLockout caps one escalating second-factor lockout (see
	// [GuardBuilder.MaxAttempts]), unless the challenge window is longer.
	maxLockout = 24 * time.Hour

	// budgetTTL is the lifetime of a subject's failure budget, counted from its
	// first failure. The budget sets how long the next lockout lasts, so the
	// escalation resets once a day passes since the first counted failure, or
	// earlier on a successful challenge or [Guard.Unlock].
	budgetTTL = 24 * time.Hour
)

// Guard turns "this person proved X" into a logged-in browser session. Every
// login method (password, OAuth, SAML, passkey) calls [Guard.Begin] after it
// verified its credential. Begin either logs the user in or, when the user is
// enrolled in a registered second-factor [Credential], parks the login in a
// pending state in which the request stays anonymous until
// [Guard.ChallengeHandler] verifies one of the user's methods. A logged-in user
// re-proves themselves with [Guard.ConfirmHandler] against the registered
// step-up credentials. The factors recorded along the way give
// [Identity.Level], and [RequireLevel] and [Guard.RequireFresh] protect routes
// with them.
//
// A Guard stores one session record (subject, factors, credential version and
// a login-record ID) under one session key and a pending login under a second,
// so a login can never be half written. Every transition (Begin, a challenge, a
// confirmation, Logout) regenerates the session ID, which prevents session
// fixation.
//
// All methods read the session from the request context with the handle chosen
// by [GuardBuilder.Handle], so [session.Middleware] must run first; without a
// session they return [session.ErrNoSession] (the authenticator returns
// [ErrNoCredentials]). Methods change the session in memory; it is saved by the
// session middleware, so change it before the response headers are written.
// Concurrent use is safe: a Guard is immutable after construction.
//
// Create one with [NewGuard]. In an application the build order is user
// provider, arks, [NewGuard], handlers.
//
// Releases before v0.7.0 named this type Login (and [NewGuard] NewLogin). Before
// v0.8.0 modules also drove a pending login with Pending, Complete and Fail and
// a logged-in session with Confirm; those are now internal to the two handlers.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.SecondFactor(totp.Credential()).StepUp(passwords.Credential(), totp.Credential())
//	})
//	if err != nil {
//		return err
//	}
//	r.Use(session.Middleware(store), auth.Middleware(guard.Authenticator()))
type Guard struct {
	// provider finds users at begin, completion and on every request.
	provider *UserProvider

	// store is the shared cache that holds the login records.
	store *cache.Cache

	// records names the entry of each subject's login records, with the session
	// lifetime as its TTL.
	records cache.Namespace[[]LoginRecord]

	// attempts names the counter of each subject's wrong second factors. The
	// counter lives in the shared store, not the session, so neither a new
	// session nor a forked one resets it.
	attempts cache.Namespace[int64]

	// locks names the entry that holds when a subject's escalating lockout ends.
	// Its lifetime is the lockout, so the entry vanishes when the lockout does.
	locks cache.Namespace[time.Time]

	// budgets names the counter of a subject's wrong second factors over
	// [budgetTTL], across windows. It decides how long the next lockout lasts.
	budgets cache.Namespace[int64]

	// settings are the completed construction settings.
	settings guardSettings
}

// NewGuard returns a [Guard] over provider. store is the cache the application
// also gives to [session.Middleware]; it keeps each user's login records (see
// [Guard.Logins]). NewGuard registers [Guard.RevokeAll] with
// [UserProvider.OnForget], so forgetting a user ends their sessions. It returns
// an error when provider or store is nil, when [GuardBuilder.ChallengeTTL],
// [GuardBuilder.MaxAttempts], [GuardBuilder.MaxLogins],
// [GuardBuilder.SessionLifetime] or [GuardBuilder.FreshFor] is not positive,
// when [GuardBuilder.ClientIP] is nil, or when a second-factor or step-up
// [Credential] has an empty method or a nil Enrolled, or repeats a method of its
// registry. Construction does no I/O and the callbacks run once, in order.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.ChallengeTTL(10 * time.Minute)
//		policy.SecondFactor(totp.Credential())
//	})
func NewGuard(provider *UserProvider, store *cache.Cache, configure ...GuardBuilderFunc) (*Guard, error) {
	if provider == nil {
		return nil, errors.New("auth: nil user provider")
	}

	if store == nil {
		return nil, errors.New("auth: nil guard store")
	}

	settings := newGuardSettings(configure...)

	if settings.ChallengeTTL <= 0 {
		return nil, errors.New("auth: guard challenge TTL must be positive")
	}

	if settings.MaxAttempts <= 0 {
		return nil, errors.New("auth: guard max attempts must be positive")
	}

	if err := validateCredentials("second-factor", settings.SecondFactors); err != nil {
		return nil, err
	}

	if err := validateCredentials("step-up", settings.StepUps); err != nil {
		return nil, err
	}

	if settings.Fresh <= 0 {
		return nil, errors.New("auth: guard freshness window must be positive")
	}

	if settings.MaxLogins <= 0 {
		return nil, errors.New("auth: guard max logins must be positive")
	}

	if settings.SessionLifetime <= 0 {
		return nil, errors.New("auth: guard session lifetime must be positive")
	}

	if settings.ClientIP == nil {
		return nil, errors.New("auth: nil client IP function")
	}

	settings.SecondFactors = slices.Clone(settings.SecondFactors)
	settings.StepUps = slices.Clone(settings.StepUps)

	guard := &Guard{provider: provider, store: store, settings: settings, records: cache.NewNamespace[[]LoginRecord]("arkoren:auth:logins", func(space *cache.NamespaceBuilder) {
		space.TTL(settings.SessionLifetime)
	}), attempts: cache.NewNamespace[int64]("arkoren:auth:attempts"),
		locks:   cache.NewNamespace[time.Time]("arkoren:auth:lockouts"),
		budgets: cache.NewNamespace[int64]("arkoren:auth:failure-budgets")}

	provider.OnForget(guard.RevokeAll)

	return guard, nil
}

// session resolves the request's session through the configured handle.
func (guard *Guard) session(r *http.Request) (*session.Session, error) {
	current, ok := session.FromContextFor(r.Context(), guard.settings.Handle)

	if !ok {
		return nil, session.ErrNoSession
	}

	return current, nil
}

// gone reports whether err says the user cannot act: deleted or disabled.
func gone(err error) bool {
	return errors.Is(err, ErrUserNotFound) || errors.Is(err, ErrUserDisabled)
}

// missing reports whether err says a session field is absent or unreadable, so
// stale or foreign data is treated like no data.
func missing(err error) bool {
	return errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType)
}

// release removes the login record of the logged-in user of current, if any, so
// replacing or ending a login leaves no phantom device behind.
func (guard *Guard) release(ctx context.Context, current *session.Session) error {
	stored, err := current.Get(sessionField)

	if missing(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if stored.LoginID == "" {
		return nil
	}

	return guard.Revoke(ctx, stored.Subject, stored.LoginID)
}

// start logs the user in with factors: it replaces the login record of any
// previous login with a new one, stores the session record, drops any pending
// login and regenerates the session ID. Writes precede regeneration so a failed
// write leaves the ID unchanged.
func (guard *Guard) start(r *http.Request, current *session.Session, user UserRecord, factors []Factor) error {
	if err := guard.release(r.Context(), current); err != nil {
		return err
	}

	id, err := guard.open(r, user.Subject)

	if err != nil {
		return err
	}

	record := sessionRecord{Subject: user.Subject, Factors: factors, Version: hashVersion(user.Version), LoginID: id}

	if err := current.Put(sessionField, record); err != nil {
		return err
	}

	if err := current.Delete(pendingField); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// Authenticator returns the [Authenticator] to pass to [Middleware]. It reads
// only the logged-in session record, never a pending login, so a half-logged-in
// request stays anonymous. It looks the subject up with the provider on every
// request and builds an [Identity] carrying the stored factors and the
// provider's [UserRecord], so [UserFromContext] returns its Value. It is the only
// code that sets [Identity.Session]. With [GuardBuilder.Grants] it also loads
// the user's roles and scopes once per request, after the login record check;
// an error from that function is returned wrapped as "auth: load grants", so the
// request fails instead of continuing without them. Without the option the identity carries
// no roles and no scopes.
//
// A missing session, an absent or unreadable record, a deleted or disabled user
// ([ErrUserNotFound], [ErrUserDisabled]), a credential version that no longer
// matches [UserRecord.Version] and a login record that was revoked, expired or
// dropped (see [Guard.Revoke]) all return [ErrNoCredentials], which makes the
// request anonymous. The version comparison is constant time. Other provider
// errors and errors reading the login records from the store pass through and
// stop the chain. The check costs one cache read per request; when the record's
// SeenAt is older than five minutes the authenticator also tries to refresh it,
// ignoring a failed write.
//
// Example:
//
//	r.Use(session.Middleware(store), auth.Middleware(guard.Authenticator()))
func (guard *Guard) Authenticator() Authenticator {
	return func(r *http.Request) (Identity, error) {
		stored, user, err := guard.active(r)

		if err != nil {
			return Identity{}, err
		}

		var roles, scopes []string

		if guard.settings.Grants != nil {
			roles, scopes, err = guard.settings.Grants(r.Context(), stored.Subject)

			if err != nil {
				return Identity{}, fmt.Errorf("auth: load grants: %w", err)
			}
		}

		identity := NewIdentity(stored.Subject, func(principal *IdentityBuilder) {
			principal.Factors(stored.Factors...)
			principal.Record(user)
			principal.Roles(roles...)
			principal.Scopes(scopes...)
		})
		identity.session = true

		return identity, nil
	}
}

// active returns the session record of r and its user when r is logged in:
// [Guard.valid] passes and the login record is still active (see [Guard.seen]).
// A missing session, a failed check and an inactive login record are
// [ErrNoCredentials]; other errors pass through. It never calls
// [GuardBuilder.Grants], so [Guard.Begin] can ask whether a user is logged in
// without depending on the permission store.
func (guard *Guard) active(r *http.Request) (sessionRecord, UserRecord, error) {
	stored, user, err := guard.valid(r)

	if errors.Is(err, session.ErrNoSession) {
		return stored, user, ErrNoCredentials
	}

	if err != nil {
		return stored, user, err
	}

	active, err := guard.seen(r.Context(), stored.Subject, stored.LoginID)

	if err != nil {
		return stored, user, err
	}

	if !active {
		return stored, user, ErrNoCredentials
	}

	return stored, user, nil
}

// valid returns the logged-in session record of r and its user, after the
// checks every credential-bearing operation must pass: the session holds a
// readable record, the provider still finds an enabled user and the stored
// credential version matches [UserRecord.Version] (constant time). A missing
// session is [session.ErrNoSession]; an absent record, a gone or disabled user
// and a version mismatch are [ErrNoCredentials]; other errors pass through. It
// does not check the login record, which [Guard.Authenticator] and the records
// methods own.
func (guard *Guard) valid(r *http.Request) (sessionRecord, UserRecord, error) {
	current, err := guard.session(r)

	if err != nil {
		return sessionRecord{}, UserRecord{}, err
	}

	stored, err := current.Get(sessionField)

	if missing(err) {
		return stored, UserRecord{}, ErrNoCredentials
	}

	if err != nil {
		return stored, UserRecord{}, err
	}

	user, err := guard.provider.Find(r.Context(), stored.Subject)

	if gone(err) {
		return stored, UserRecord{}, ErrNoCredentials
	}

	if err != nil {
		return stored, UserRecord{}, err
	}

	if subtle.ConstantTimeCompare([]byte(stored.Version), []byte(hashVersion(user.Version))) != 1 {
		return stored, UserRecord{}, ErrNoCredentials
	}

	return stored, user, nil
}

// Logout removes the logged-in session record, its login record and any pending
// login, even stale or unreadable ones, and regenerates the session ID. Other session data and its
// lifetime stay; use [session.Session.Destroy] to remove the whole session.
//
// Example:
//
//	if err := guard.Logout(r); err != nil {
//		return err
//	}
func (guard *Guard) Logout(r *http.Request) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	if err := guard.release(r.Context(), current); err != nil {
		return err
	}

	if err := current.Delete(sessionField); err != nil {
		return err
	}

	if err := current.Delete(pendingField); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}
