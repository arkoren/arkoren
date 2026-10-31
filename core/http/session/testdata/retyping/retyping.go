// Package retyping contains invalid callers compiled by the session tests.
// Ordinary package discovery excludes testdata; this package must not build.
package retyping

import "arkoren.dev/core/http/session"

// reject demonstrates distinct ways a caller must not change a field's type.
func reject(current *session.Session) {
	var _ session.Field[int] = session.NewField[string]("user")
	_ = session.Field[int](session.NewField[string]("user"))
	_, _ = current.Get[int](session.NewField[string]("user"))
	_ = current.Put[int](session.NewField[string]("user"), 42)
	_ = current.Put[string](session.NewField[string]("user"), 42)
}
