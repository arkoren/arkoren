package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"time"

	"arkoren.dev/core/http/session"
	"arkoren.dev/core/service/cache"
)

// Guard turns "this person proved X" into a logged-in browser session. Every
// login method (password, OAuth, SAML, passkey) calls [Guard.Begin] after it
// verified its credential. Begin either logs the user in or, when a registered
// second-factor check says the user needs one, parks the login in a pending
// state in which the request stays anonymous until [Guard.Complete]. The
// factors recorded along the way give [Identity.Level], and [RequireLevel] and
// [RequireFresh] protect routes with them.
//
// A Guard stores one session record (subject, factors, credential version and
// a login-record ID) under one session key and a pending login under a second,
// so a login can never be half written. Every transition (Begin, Complete,
// Fail, Confirm, Logout) regenerates the session ID, which prevents session
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
// Releases before v0.7.0 named this type Login (and [NewGuard] NewLogin). The
// methods, session keys, events and stored record formats did not change.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.SecondFactor(factors.Enrolled)
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

	// settings are the completed construction settings.
	settings guardSettings
}

// NewGuard returns a [Guard] over provider. store is the cache the application
// also gives to [session.Middleware]; it keeps each user's login records (see
// [Guard.Logins]). NewGuard registers [Guard.RevokeAll] with
// [UserProvider.OnForget], so forgetting a user ends their sessions. It returns
// an error when provider or store is nil, when [GuardBuilder.ChallengeTTL],
// [GuardBuilder.MaxAttempts], [GuardBuilder.MaxLogins] or
// [GuardBuilder.SessionLifetime] is not positive, or when a second-factor check
// or [GuardBuilder.ClientIP] is nil. Construction does no I/O and the callbacks
// run once, in order.
//
// Example:
//
//	guard, err := auth.NewGuard(users, store, func(policy *auth.GuardBuilder) {
//		policy.ChallengeTTL(10 * time.Minute)
//		policy.SecondFactor(factors.Enrolled)
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

	if slices.ContainsFunc(settings.SecondFactors, func(check func(context.Context, string) (bool, error)) bool { return check == nil }) {
		return nil, errors.New("auth: nil second-factor check")
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

	guard := &Guard{provider: provider, store: store, settings: settings, records: cache.NewNamespace[[]LoginRecord]("arkoren:auth:logins", func(space *cache.NamespaceBuilder) {
		space.TTL(settings.SessionLifetime)
	}), attempts: cache.NewNamespace[int64]("arkoren:auth:attempts")}

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
// provider's [UserRecord], so [UserFromContext] returns its Value.
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
		stored, user, err := guard.valid(r)

		if errors.Is(err, session.ErrNoSession) {
			return Identity{}, ErrNoCredentials
		}

		if err != nil {
			return Identity{}, err
		}

		active, err := guard.seen(r.Context(), stored.Subject, stored.LoginID)

		if err != nil {
			return Identity{}, err
		}

		if !active {
			return Identity{}, ErrNoCredentials
		}

		return NewIdentity(stored.Subject, func(principal *IdentityBuilder) {
			principal.Factors(stored.Factors...)
			principal.Record(user)
		}), nil
	}
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

// Begin is the single entry for every login method. Call it after the method
// verified its credential, passing the verified factor; a zero factor.At means
// now. It first looks subject up with the provider: a deleted or disabled user
// is [ErrUnauthenticated], and other provider errors, including
// [ErrInvalidSubject], are returned unchanged.
//
// When the session is already logged in as subject, Begin behaves like
// [Guard.Confirm]: it adds factor to the logged-in session (a factor of the same
// [Method] replaces the older one), keeps the login record, skips the
// second-factor checks, regenerates the session ID and returns true. The user
// already passed every check, so a logged-in user who connects a provider or
// re-authenticates keeps the factors they have and is never challenged again.
// Before v0.6.0 Begin replaced the factors in this case, and parked the login
// when a second factor was enrolled. A different subject, an anonymous session
// and a pending login keep the behavior below.
//
// Otherwise it asks each [GuardBuilder.SecondFactor] check. When none says yes it
// logs the user in with factor, regenerates the session ID and returns true.
// Otherwise it replaces any logged-in user with a pending login that expires
// after [GuardBuilder.ChallengeTTL], regenerates the session ID and returns
// false: the request stays anonymous until [Guard.Complete]. The caller then
// asks for the second factor. A check error fails the login with that error and
// changes nothing.
//
// Logging in creates a [LoginRecord] for the device (see [Guard.Logins]) and
// removes the record of the login the session replaces, so Begin can also return
// the cache or lock errors of the record store; the session is then unchanged.
// A login that is parked creates its record at [Guard.Complete].
//
// Begin does not verify the credential: call it only after it checked out.
//
// Example:
//
//	complete, err := guard.Begin(r, subject, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	if err != nil {
//		return err
//	}
//	if !complete {
//		return response.Status(w, http.StatusAccepted) // Ask for the second factor.
//	}
func (guard *Guard) Begin(r *http.Request, subject string, factor Factor) (complete bool, err error) {
	current, err := guard.session(r)

	if err != nil {
		return false, err
	}

	user, err := guard.provider.Find(r.Context(), subject)

	if gone(err) {
		return false, ErrUnauthenticated
	}

	if err != nil {
		return false, err
	}

	// A session already logged in as this subject passed every check for it, so
	// the new factor joins the existing login like Confirm instead of replacing it.
	identity, err := guard.Authenticator()(r)

	if err == nil && identity.Subject() == user.Subject {
		return true, guard.Confirm(r, factor)
	}

	if err != nil && !errors.Is(err, ErrNoCredentials) {
		return false, err
	}

	needed := false

	for _, check := range guard.settings.SecondFactors {
		needed, err = check(r.Context(), user.Subject)

		if err != nil {
			return false, err
		}

		if needed {
			break
		}
	}

	now := time.Now()
	factors := withFactor(nil, factor, now)

	if !needed {
		return true, guard.start(r, current, user, factors)
	}

	waiting := pendingLogin{Subject: user.Subject, Factors: factors, Version: hashVersion(user.Version), Expires: now.Add(guard.settings.ChallengeTTL).UTC()}

	if err := guard.release(r.Context(), current); err != nil {
		return false, err
	}

	if err := current.Delete(sessionField); err != nil {
		return false, err
	}

	if err := current.Put(pendingField, waiting); err != nil {
		return false, err
	}

	current.Regenerate()

	return false, nil
}

// pending returns the unexpired pending login of current. An absent, unreadable
// or expired one is [ErrUnauthenticated]; expiry also clears it so a stale
// challenge cannot linger.
func (guard *Guard) pending(current *session.Session) (pendingLogin, error) {
	waiting, err := current.Get(pendingField)

	if missing(err) {
		return waiting, ErrUnauthenticated
	}

	if err != nil {
		return waiting, err
	}

	if !time.Now().Before(waiting.Expires) {
		if err := current.Delete(pendingField); err != nil {
			return waiting, err
		}

		return waiting, ErrUnauthenticated
	}

	return waiting, nil
}

// pendingUser returns the pending login of current and its user, loaded from the
// provider. A user deleted or disabled during the challenge, or whose
// [UserRecord.Version] differs from the one seen at [Guard.Begin] (compared in
// constant time), discards the pending login and is [ErrUnauthenticated]. So
// does a subject whose attempts within the window exceed
// [GuardBuilder.MaxAttempts], whatever session holds the login.
//
// With reserve set, the call first takes one attempt with an atomic increment of
// the subject's counter, so concurrent callers each get a distinct count and at
// most MaxAttempts of them pass. That is what bounds the code checks: a read
// followed by a later increment would let every request that read the counter
// before the limit through. Without reserve the counter is only read, for
// [Guard.Complete], which follows a check that [Guard.Pending] already counted.
func (guard *Guard) pendingUser(r *http.Request, current *session.Session, reserve bool) (pendingLogin, UserRecord, error) {
	waiting, err := guard.pending(current)

	if err != nil {
		return waiting, UserRecord{}, err
	}

	user, err := guard.provider.Find(r.Context(), waiting.Subject)

	if err != nil && !gone(err) {
		return waiting, UserRecord{}, err
	}

	if gone(err) || subtle.ConstantTimeCompare([]byte(waiting.Version), []byte(hashVersion(user.Version))) != 1 {
		if err := current.Delete(pendingField); err != nil {
			return waiting, UserRecord{}, err
		}

		return waiting, UserRecord{}, ErrUnauthenticated
	}

	var failures int64

	if reserve {
		failures, _, err = guard.store.IncrementWithTTL(r.Context(), guard.attempts.Key(waiting.Subject), 1, guard.settings.ChallengeTTL)
	} else {
		failures, err = guard.store.Get(r.Context(), guard.attempts.Key(waiting.Subject))
	}

	if err != nil && !errors.Is(err, cache.ErrNotFound) {
		return waiting, UserRecord{}, err
	}

	if failures > int64(guard.settings.MaxAttempts) {
		if err := current.Delete(pendingField); err != nil {
			return waiting, UserRecord{}, err
		}

		return waiting, UserRecord{}, ErrUnauthenticated
	}

	return waiting, user, nil
}

// Pending returns the user waiting for a second factor, as parked by
// [Guard.Begin]. It returns [ErrUnauthenticated] when there is no pending login,
// it expired (which also clears it) or the user was deleted, disabled or had its
// [UserRecord.Version] changed, for example by a password change, in the
// meantime (which discards it). Other provider and store errors pass through.
// Pending does not consume the login, so a handler can read the user, check a
// code and report a wrong one with [Guard.Fail].
//
// Every successful Pending reserves one second-factor attempt for the user with
// an atomic increment, before the code is checked, and the attempt stays counted
// whether the code is right or wrong. Once [GuardBuilder.MaxAttempts] attempts
// fall in a [GuardBuilder.ChallengeTTL] window, further calls discard the login
// and return [ErrUnauthenticated], in every session, until the window ends or a
// [Guard.Complete] clears the count. This bounds the code checks even for
// requests arriving in parallel, on one forked pending cookie or on many.
// Call Pending once per code check, in the route that verifies the code, not to
// render a form: each call spends an attempt.
//
// Example:
//
//	user, err := guard.Pending(r)
//	if err != nil {
//		return err
//	}
//	ok := verifyCode(user.Subject, code)
func (guard *Guard) Pending(r *http.Request) (UserRecord, error) {
	current, err := guard.session(r)

	if err != nil {
		return UserRecord{}, err
	}

	_, user, err := guard.pendingUser(r, current, true)

	return user, err
}

// Complete finishes a pending login after the second factor verified: it logs
// the user in with the factors verified at [Guard.Begin] plus factor (a zero
// factor.At means now), creates the [LoginRecord] of the device as [Guard.Begin]
// does, regenerates the session ID and consumes the pending login, so a second
// call returns [ErrUnauthenticated]. The errors are those of [Guard.Pending]
// plus the cache and lock errors of the record store; on any error nothing is
// logged in. Complete does not reserve an attempt itself: it relies on the one
// the preceding [Guard.Pending] took, and returns [ErrUnauthenticated] when the
// user's attempts already exceed [GuardBuilder.MaxAttempts]. A successful
// Complete clears the user's attempt count.
//
// Complete does not verify the factor: call it only after it checked out.
//
// Example:
//
//	if _, err := guard.Pending(r); err != nil {
//		return err
//	}
//	if !verifyCode(code) {
//		return guard.Fail(r)
//	}
//	return guard.Complete(r, auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})
func (guard *Guard) Complete(r *http.Request, factor Factor) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	waiting, user, err := guard.pendingUser(r, current, false)

	if err != nil {
		return err
	}

	if err := guard.start(r, current, user, withFactor(waiting.Factors, factor, time.Now())); err != nil {
		return err
	}

	return guard.store.Delete(r.Context(), guard.attempts.Key(waiting.Subject))
}

// Fail reports that the code checked after [Guard.Pending] was wrong: it
// regenerates the session ID and, when the attempt that Pending reserved was the
// last of [GuardBuilder.MaxAttempts], discards the pending login. It does not
// count the attempt itself: the count is per user, kept in the guard's cache
// store, and [Guard.Pending] increments it atomically before the code is
// checked, so it survives a new [Guard.Begin], a new session and requests that
// fork one pending-session cookie in parallel. At most MaxAttempts code checks
// pass Pending within a [GuardBuilder.ChallengeTTL] window, however many
// requests race; after that the user is locked out of the second factor until
// the window ends, and [Guard.Pending] and [Guard.Complete] return
// [ErrUnauthenticated] in every session. A successful [Guard.Complete] clears
// the count. Fail returns [ErrUnauthenticated] when there is no pending login,
// and store errors unchanged.
//
// A lockout lets anyone who knows the subject delay that user's second factor,
// so also rate-limit by address.
//
// Example:
//
//	if !verifyCode(code) {
//		if err := guard.Fail(r); err != nil {
//			return err
//		}
//		return problem.Unauthorized()
//	}
func (guard *Guard) Fail(r *http.Request) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	waiting, err := guard.pending(current)

	if err != nil {
		return err
	}

	failures, err := guard.store.Get(r.Context(), guard.attempts.Key(waiting.Subject))

	if err != nil && !errors.Is(err, cache.ErrNotFound) {
		return err
	}

	if failures >= int64(guard.settings.MaxAttempts) {
		err = current.Delete(pendingField)

		if err != nil {
			return err
		}
	}

	current.Regenerate()

	return nil
}

// Confirm records a freshly verified factor on the logged-in session and
// regenerates the session ID. Use it to re-check a password before a sensitive
// change, which satisfies [RequireFresh], or to add a second factor later so
// [RequireLevel] passes. A factor with the same [Method] as a recorded one
// replaces it, so each method appears once with its latest time; a zero
// factor.At means now. It returns [ErrUnauthenticated] when the session holds no
// logged-in user.
//
// Confirm does not verify the factor: call it only after it checked out.
//
// Example:
//
//	if err := guard.Confirm(r, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}); err != nil {
//		return err
//	}
func (guard *Guard) Confirm(r *http.Request, factor Factor) error {
	current, err := guard.session(r)

	if err != nil {
		return err
	}

	stored, err := current.Get(sessionField)

	if missing(err) {
		return ErrUnauthenticated
	}

	if err != nil {
		return err
	}

	stored.Factors = withFactor(stored.Factors, factor, time.Now())

	if err := current.Put(sessionField, stored); err != nil {
		return err
	}

	current.Regenerate()

	return nil
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
