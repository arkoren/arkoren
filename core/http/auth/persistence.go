package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"arkoren.dev/core/http/session"
)

// version returns the hex SHA-256 of user's AuthVersion, or empty when the user
// type is not [Versioned]. Hashing keeps the raw value out of session storage.
func version[U any](user U) string {
	versioned, ok := any(user).(Versioned)

	if !ok {
		return ""
	}

	sum := sha256.Sum256([]byte(versioned.AuthVersion()))

	return hex.EncodeToString(sum[:])
}

// checkSession reports the configuration or nil-session error shared by every
// operation that mutates or reads an explicit session.
func (identity SessionIdentity[K, U]) checkSession(current *session.Session) error {
	if err := identity.Err(); err != nil {
		return err
	}

	if current == nil {
		return session.ErrNoSession
	}

	return nil
}

// start writes the logged-in fields for user with factors, drops any pending
// login and rotates the session ID. Writes precede rotation so a failed write
// leaves the ID unchanged.
func (identity SessionIdentity[K, U]) start(current *session.Session, user U, factors []Factor) error {
	if err := current.Put(field[K](identity.realm, userKey), user.AuthID()); err != nil {
		return err
	}

	if err := current.Put(field[storedState](identity.realm, stateKey), storedState{Factors: factors, Version: version(user)}); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, pendingKey)); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// Login stores user.AuthID() in current, records that the user proved methods
// just now, and regenerates the session ID to prevent session fixation. Call it
// after verifying the credentials, and pass every method actually verified; with
// none the session has no factors and fails every [SessionIdentity.RequireMethods]
// and [SessionIdentity.RequireFresh] check. It also drops any pending login and,
// for a [Versioned] user, stores the credential version. A failed field write is
// returned before rotating the ID; callers must stop login. Invalid definitions
// fail before mutation; nil sessions return ErrNoSession. The explicit session
// is not checked against the definition's handle. Other data, creation time and
// expiry remain unchanged. The user is not reloaded or verified; only its
// identifier is stored, and a nil pointer U panics in AuthID.
//
// Use [SessionIdentity.Challenge] instead when a second factor is still required.
//
// Example:
//
//	sess := request.MustSession(r)
//	if err := identity.Login(sess, user, auth.MethodPassword); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Login(current *session.Session, user U, methods ...Method) error {
	if err := identity.checkSession(current); err != nil {
		return err
	}

	return identity.start(current, user, record(nil, time.Now().UTC(), methods...))
}

// Logout removes the identifier, factors and pending login of this identity's
// realm, even stale or wrongly typed ones, and rotates the session ID. Other
// data, other realms and lifetime remain unchanged; use current.Destroy for full
// removal. Invalid definitions fail before mutation, nil returns ErrNoSession,
// and deletion errors precede rotation. Explicit sessions are not handle-checked.
//
// Example:
//
//	if err := identity.Logout(request.MustSession(r)); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Logout(current *session.Session) error {
	if err := identity.checkSession(current); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, userKey)); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, stateKey)); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, pendingKey)); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// Challenge records that user passed methods but a further factor is still
// required, without logging them in. It removes any logged-in user and factors
// from the session, stores the user's identifier and the proved methods with a
// deadline of [SessionBuilder.ChallengeTTL], and regenerates the session ID. Until
// [SessionIdentity.Complete] succeeds the session is anonymous to
// [SessionIdentity.Authenticator] and to [Middleware]; only [SessionIdentity.Pending]
// reads the waiting user. Call it after the first factor verifies, then verify
// the second factor with a handler that uses Pending and Complete.
//
// Calling it again replaces the pending login. Errors and nil sessions behave as
// for [SessionIdentity.Login].
//
// Example:
//
//	if err := identity.Challenge(request.MustSession(r), user, auth.MethodPassword); err != nil {
//		return err
//	}
//	return response.Status(w, http.StatusAccepted)
func (identity SessionIdentity[K, U]) Challenge(current *session.Session, user U, methods ...Method) error {
	if err := identity.checkSession(current); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, userKey)); err != nil {
		return err
	}

	if err := current.Delete(field[any](identity.realm, stateKey)); err != nil {
		return err
	}

	now := time.Now().UTC()
	waiting := pendingLogin[K]{
		ID:      user.AuthID(),
		Factors: record(nil, now, methods...),
		Expires: now.Add(identity.challengeTTL),
	}

	if err := current.Put(field[pendingLogin[K]](identity.realm, pendingKey), waiting); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// pending returns the unexpired pending login of current. An absent, wrongly
// typed or expired one is ErrUnauthenticated; expiry also clears the slot so a
// stale challenge cannot linger.
func (identity SessionIdentity[K, U]) pending(current *session.Session) (pendingLogin[K], error) {
	waiting, err := current.Get(field[pendingLogin[K]](identity.realm, pendingKey))

	if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
		return waiting, ErrUnauthenticated
	}

	if err != nil {
		return waiting, err
	}

	if !time.Now().Before(waiting.Expires) {
		if err := current.Delete(field[any](identity.realm, pendingKey)); err != nil {
			return waiting, err
		}

		return waiting, ErrUnauthenticated
	}

	return waiting, nil
}

// Pending loads the user waiting for a further factor, as stored by
// [SessionIdentity.Challenge]. An absent, wrongly typed or expired pending
// login returns [ErrUnauthenticated]; expiry also clears it. Loader errors,
// including [ErrNotFound] for a user deleted in the meantime, pass through. It
// does not consume the challenge, so a handler may call it and reject a wrong
// code; rate-limit such attempts, since this state is a login in progress.
// Invalid definitions fail first and nil sessions return ErrNoSession.
//
// Example:
//
//	user, err := identity.Pending(r.Context(), request.MustSession(r))
//	if err != nil {
//		return err
//	}
//	_ = user
func (identity SessionIdentity[K, U]) Pending(ctx context.Context, current *session.Session) (user U, err error) {
	if err := identity.checkSession(current); err != nil {
		return user, err
	}

	waiting, err := identity.pending(current)

	if err != nil {
		return user, err
	}

	return identity.load(ctx, waiting.ID)
}

// Complete finishes a challenged login after the further factor verified. It
// loads the pending user, then behaves like [SessionIdentity.Login] with the
// methods proved at the challenge plus methods, so the pending login is
// consumed and cannot be completed twice. Errors are those of
// [SessionIdentity.Pending] and Login; on any error nothing is logged in.
//
// Example:
//
//	if err := identity.Complete(r.Context(), request.MustSession(r), auth.MethodOTP); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Complete(ctx context.Context, current *session.Session, methods ...Method) error {
	if err := identity.checkSession(current); err != nil {
		return err
	}

	waiting, err := identity.pending(current)

	if err != nil {
		return err
	}

	user, err := identity.load(ctx, waiting.ID)

	if err != nil {
		return err
	}

	return identity.start(current, user, record(waiting.Factors, time.Now().UTC(), methods...))
}

// Confirm records that the logged-in user proved methods just now and
// regenerates the session ID. Use it to re-check a password before a sensitive
// change, which satisfies [SessionIdentity.RequireFresh], or to add a factor to
// a single-factor session so [SessionIdentity.RequireMethods] passes. A method
// already recorded is refreshed in place; others are appended. The session must
// hold a logged-in user of this realm, otherwise it returns [ErrUnauthenticated].
// A session without state (created before factors existed) gains one; for a
// [Versioned] user it cannot be repaired this way and is ended by the
// authenticator. Errors and nil sessions behave as for [SessionIdentity.Login].
//
// Confirm does not verify anything: call it only after the factor checked out.
//
// Example:
//
//	if err := identity.Confirm(request.MustSession(r), auth.MethodPassword); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Confirm(current *session.Session, methods ...Method) error {
	if err := identity.checkSession(current); err != nil {
		return err
	}

	_, err := current.Get(field[K](identity.realm, userKey))

	if errors.Is(err, session.ErrKeyNotFound) || errors.Is(err, session.ErrInvalidValueType) {
		return ErrUnauthenticated
	}

	if err != nil {
		return err
	}

	saved, err := current.Get(field[storedState](identity.realm, stateKey))

	if err != nil && !errors.Is(err, session.ErrKeyNotFound) && !errors.Is(err, session.ErrInvalidValueType) {
		return err
	}

	// A missing or wrongly typed state restarts from the zero value, keeping the
	// stored version empty so Versioned users still fail the authenticator.
	if err != nil {
		saved = storedState{}
	}

	saved.Factors = record(saved.Factors, time.Now().UTC(), methods...)

	if err := current.Put(field[storedState](identity.realm, stateKey), saved); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// State returns how the session's user authenticated: each proved method once
// with the time it was last proved. A session without stored state, such as one
// created before factors existed, returns an empty State and no error. Invalid
// definitions fail first and nil sessions return ErrNoSession. A wrongly typed
// stored state returns session.ErrInvalidValueType. The result is a copy.
//
// State does not check that a user is logged in; use [SessionIdentity.Authenticator]
// for that.
//
// Example:
//
//	state, err := identity.State(request.MustSession(r))
//	if err != nil {
//		return err
//	}
//	for _, factor := range state.Factors {
//		_ = factor.Method
//	}
func (identity SessionIdentity[K, U]) State(current *session.Session) (State, error) {
	if err := identity.checkSession(current); err != nil {
		return State{}, err
	}

	saved, err := current.Get(field[storedState](identity.realm, stateKey))

	if errors.Is(err, session.ErrKeyNotFound) {
		return State{}, nil
	}

	if err != nil {
		return State{}, err
	}

	return State{Factors: slices.Clone(saved.Factors)}, nil
}
