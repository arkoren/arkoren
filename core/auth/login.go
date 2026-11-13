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

// Login turns "this person proved X" into a logged-in browser session. Every
// login method (password, OAuth, SAML, passkey) calls [Login.Begin] after it
// verified its credential. Begin either logs the user in or, when a registered
// second-factor check says the user needs one, parks the login in a pending
// state in which the request stays anonymous until [Login.Complete]. The
// factors recorded along the way give [Identity.Level], and [RequireLevel] and
// [RequireFresh] guard routes with them.
//
// A Login stores one session record (subject, factors, credential version and
// a login-record ID) under one session key and a pending login under a second,
// so a login can never be half written. Every transition (Begin, Complete,
// Fail, Confirm, Logout) regenerates the session ID, which prevents session
// fixation.
//
// All methods read the session from the request context with the handle chosen
// by [LoginBuilder.Handle], so [session.Middleware] must run first; without a
// session they return [session.ErrNoSession] (the authenticator returns
// [ErrNoCredentials]). Methods change the session in memory; it is saved by the
// session middleware, so change it before the response headers are written.
// Concurrent use is safe: a Login is immutable after construction.
//
// Create one with [NewLogin]. In an application the build order is user
// provider, arks, [NewLogin], handlers.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.SecondFactor(factors.Enrolled)
//	})
//	if err != nil {
//		return err
//	}
//	r.Use(session.Middleware(store), auth.Middleware(login.Authenticator()))
type Login struct {
	// provider finds users at begin, completion and on every request.
	provider *UserProvider

	// store is the shared cache, kept for login records. It is not used until
	// login records exist.
	store *cache.Cache

	// settings are the completed construction settings.
	settings loginSettings
}

// NewLogin returns a [Login] over provider. store is the cache the application
// also gives to [session.Middleware]; it is validated and kept for login
// records (listing and revoking devices) and is not used yet. It returns an
// error when provider or store is nil, when [LoginBuilder.ChallengeTTL] or
// [LoginBuilder.MaxAttempts] is not positive, or when a second-factor check is
// nil. Construction does no I/O and the callbacks run once, in order.
//
// Example:
//
//	login, err := auth.NewLogin(users, store, func(policy *auth.LoginBuilder) {
//		policy.ChallengeTTL(10 * time.Minute)
//		policy.SecondFactor(factors.Enrolled)
//	})
func NewLogin(provider *UserProvider, store *cache.Cache, configure ...LoginBuilderFunc) (*Login, error) {
	if provider == nil {
		return nil, errors.New("auth: nil user provider")
	}

	if store == nil {
		return nil, errors.New("auth: nil login store")
	}

	settings := newLoginSettings(configure...)

	if settings.ChallengeTTL <= 0 {
		return nil, errors.New("auth: login challenge TTL must be positive")
	}

	if settings.MaxAttempts <= 0 {
		return nil, errors.New("auth: login max attempts must be positive")
	}

	if slices.ContainsFunc(settings.SecondFactors, func(check func(context.Context, string) (bool, error)) bool { return check == nil }) {
		return nil, errors.New("auth: nil second-factor check")
	}

	settings.SecondFactors = slices.Clone(settings.SecondFactors)

	return &Login{provider: provider, store: store, settings: settings}, nil
}

// session resolves the request's session through the configured handle.
func (login *Login) session(r *http.Request) (*session.Session, error) {
	current, ok := session.FromContextFor(r.Context(), login.settings.Handle)

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

// start logs the user in with factors: it stores the session record, drops any
// pending login and regenerates the session ID. Writes precede regeneration so a
// failed write leaves the ID unchanged.
func (login *Login) start(current *session.Session, user UserRecord, factors []Factor) error {
	record := sessionRecord{Subject: user.Subject, Factors: factors, Version: hashVersion(user.Version)}

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
// ([ErrUserNotFound], [ErrUserDisabled]) and a credential version that no longer
// matches [UserRecord.Version] all return [ErrNoCredentials], which makes the
// request anonymous. The version comparison is constant time. Other provider
// errors pass through and stop the chain.
//
// Example:
//
//	r.Use(session.Middleware(store), auth.Middleware(login.Authenticator()))
func (login *Login) Authenticator() Authenticator {
	return func(r *http.Request) (Identity, error) {
		current, err := login.session(r)

		if err != nil {
			return Identity{}, ErrNoCredentials
		}

		stored, err := current.Get(sessionField)

		if missing(err) {
			return Identity{}, ErrNoCredentials
		}

		if err != nil {
			return Identity{}, err
		}

		user, err := login.provider.Find(r.Context(), stored.Subject)

		if gone(err) {
			return Identity{}, ErrNoCredentials
		}

		if err != nil {
			return Identity{}, err
		}

		if subtle.ConstantTimeCompare([]byte(stored.Version), []byte(hashVersion(user.Version))) != 1 {
			return Identity{}, ErrNoCredentials
		}

		return NewIdentity(stored.Subject, func(principal *IdentityBuilder) {
			principal.Factors(stored.Factors...)
			principal.Record(user)
		}), nil
	}
}

// Begin is the single entry for every login method. Call it after the method
// verified its credential, passing the verified factor; a zero factor.At means
// now. It first looks subject up with the provider: a deleted or disabled user
// is [ErrUnauthenticated], and other provider errors, including
// [ErrInvalidSubject], are returned unchanged.
//
// It then asks each [LoginBuilder.SecondFactor] check. When none says yes it
// logs the user in with factor, regenerates the session ID and returns true.
// Otherwise it replaces any logged-in user with a pending login that expires
// after [LoginBuilder.ChallengeTTL], regenerates the session ID and returns
// false: the request stays anonymous until [Login.Complete]. The caller then
// asks for the second factor. A check error fails the login with that error and
// changes nothing.
//
// Begin does not verify the credential: call it only after it checked out.
//
// Example:
//
//	complete, err := login.Begin(r, subject, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge})
//	if err != nil {
//		return err
//	}
//	if !complete {
//		return response.Status(w, http.StatusAccepted) // Ask for the second factor.
//	}
func (login *Login) Begin(r *http.Request, subject string, factor Factor) (complete bool, err error) {
	current, err := login.session(r)

	if err != nil {
		return false, err
	}

	user, err := login.provider.Find(r.Context(), subject)

	if gone(err) {
		return false, ErrUnauthenticated
	}

	if err != nil {
		return false, err
	}

	needed := false

	for _, check := range login.settings.SecondFactors {
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
		return true, login.start(current, user, factors)
	}

	waiting := pendingLogin{Subject: user.Subject, Factors: factors, Expires: now.Add(login.settings.ChallengeTTL).UTC()}

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
func (login *Login) pending(current *session.Session) (pendingLogin, error) {
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
// provider. A user deleted or disabled during the challenge discards the pending
// login and is [ErrUnauthenticated].
func (login *Login) pendingUser(r *http.Request, current *session.Session) (pendingLogin, UserRecord, error) {
	waiting, err := login.pending(current)

	if err != nil {
		return waiting, UserRecord{}, err
	}

	user, err := login.provider.Find(r.Context(), waiting.Subject)

	if gone(err) {
		if err := current.Delete(pendingField); err != nil {
			return waiting, UserRecord{}, err
		}

		return waiting, UserRecord{}, ErrUnauthenticated
	}

	return waiting, user, err
}

// Pending returns the user waiting for a second factor, as parked by
// [Login.Begin]. It returns [ErrUnauthenticated] when there is no pending login,
// it expired (which also clears it) or the user was deleted or disabled in the
// meantime (which discards it). Other provider errors pass through. Pending does
// not consume the login, so a handler can read the user, check a code and report
// a wrong one with [Login.Fail].
//
// Example:
//
//	user, err := login.Pending(r)
//	if err != nil {
//		return err
//	}
//	ok := verifyCode(user.Subject, code)
func (login *Login) Pending(r *http.Request) (UserRecord, error) {
	current, err := login.session(r)

	if err != nil {
		return UserRecord{}, err
	}

	_, user, err := login.pendingUser(r, current)

	return user, err
}

// Complete finishes a pending login after the second factor verified: it logs
// the user in with the factors verified at [Login.Begin] plus factor (a zero
// factor.At means now), regenerates the session ID and consumes the pending
// login, so a second call returns [ErrUnauthenticated]. The errors are those of
// [Login.Pending]; on any error nothing is logged in.
//
// Complete does not verify the factor: call it only after it checked out.
//
// Example:
//
//	if _, err := login.Pending(r); err != nil {
//		return err
//	}
//	if !verifyCode(code) {
//		return login.Fail(r)
//	}
//	return login.Complete(r, auth.Factor{Method: auth.MethodOTP, Kind: auth.KindPossession})
func (login *Login) Complete(r *http.Request, factor Factor) error {
	current, err := login.session(r)

	if err != nil {
		return err
	}

	waiting, user, err := login.pendingUser(r, current)

	if err != nil {
		return err
	}

	return login.start(current, user, withFactor(waiting.Factors, factor, time.Now()))
}

// Fail counts one wrong second-factor attempt on the pending login and
// regenerates the session ID. When the count reaches [LoginBuilder.MaxAttempts]
// the pending login is discarded and the next [Login.Pending] returns
// [ErrUnauthenticated]. It returns [ErrUnauthenticated] when there is no pending
// login. The count lives in the session, so it limits one browser session only;
// rate-limit by user or address as well.
//
// Example:
//
//	if !verifyCode(code) {
//		if err := login.Fail(r); err != nil {
//			return err
//		}
//		return problem.Unauthorized()
//	}
func (login *Login) Fail(r *http.Request) error {
	current, err := login.session(r)

	if err != nil {
		return err
	}

	waiting, err := login.pending(current)

	if err != nil {
		return err
	}

	waiting.Attempts++

	if waiting.Attempts >= login.settings.MaxAttempts {
		err = current.Delete(pendingField)
	} else {
		err = current.Put(pendingField, waiting)
	}

	if err != nil {
		return err
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
//	if err := login.Confirm(r, auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}); err != nil {
//		return err
//	}
func (login *Login) Confirm(r *http.Request, factor Factor) error {
	current, err := login.session(r)

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

// Logout removes the logged-in session record and any pending login, even stale
// or unreadable ones, and regenerates the session ID. Other session data and its
// lifetime stay; use [session.Session.Destroy] to remove the whole session.
//
// Example:
//
//	if err := login.Logout(r); err != nil {
//		return err
//	}
func (login *Login) Logout(r *http.Request) error {
	current, err := login.session(r)

	if err != nil {
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
