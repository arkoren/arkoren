// Package namespaceretyping intentionally fails compilation to exercise the
// public namespace's invariant value type. Only the negative test builds it.
package namespaceretyping

import "arkoren.dev/core/http/session"

// invalid demonstrates forbidden namespace conversions and typed leaf writes.
func invalid(current *session.Session) {
	_ = session.Namespace[int](session.NewNamespace[string]("app"))
	var integers session.Namespace[int] = session.NewNamespace[string]("app")
	_ = integers
	_ = current.Put[int](session.NewNamespace[string]("app").Field("value"), 1)
}
