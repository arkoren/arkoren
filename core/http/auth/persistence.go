package auth

import "arkoren.dev/core/http/session"

// Login stores user.AuthID() in current and regenerates the session ID to
// prevent session fixation. Call it after verifying the credentials. A failed
// field write is returned before rotating the ID; callers must stop login.
// Invalid definitions fail before mutation; nil sessions return ErrNoSession.
// The explicit session is not checked against the definition's handle. Other
// data, creation time and expiry remain unchanged. The user is not reloaded or
// verified; only its identifier is stored, and a nil pointer U panics in AuthID.
//
// Example:
//
//	sess := request.MustSession(r)
//	if err := identity.Login(sess, user); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Login(current *session.Session, user U) error {
	if err := identity.Err(); err != nil {
		return err
	}

	if current == nil {
		return session.ErrNoSession
	}

	if err := current.Put(session.NewField[K](sessionKey), user.AuthID()); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}

// Logout removes even a stale, wrongly typed identifier and rotates the session
// ID. Other data and lifetime remain unchanged; use current.Destroy for full
// removal. Invalid definitions fail before mutation, nil returns ErrNoSession,
// and deletion errors precede rotation. Explicit sessions are not handle-checked.
//
// Example:
//
//	if err := identity.Logout(request.MustSession(r)); err != nil {
//		return err
//	}
func (identity SessionIdentity[K, U]) Logout(current *session.Session) error {
	if err := identity.Err(); err != nil {
		return err
	}

	if current == nil {
		return session.ErrNoSession
	}

	if err := current.Delete(session.NewField[any](sessionKey)); err != nil {
		return err
	}

	current.Regenerate()

	return nil
}
