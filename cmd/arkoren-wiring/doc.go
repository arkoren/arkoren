// Package wiring is the development-only module that proves the public modules
// work together. It is never released and nothing imports it: arkoren.dev/ark/iam
// must not import arkoren.dev/ark/oauth (a module boundary is a dependency
// boundary), so the only place that can show they fit is a module that requires
// both.
//
// The compile-time assertions in this file's package say that *oauthauth.Client
// satisfies iam.Provider and iam.NativeProvider. The end-to-end tests run
// the facade over HTTP against in-memory SQLite and a fake OpenID Connect
// provider: password with TOTP, the application's own users, social login then
// connect, native login then a device token then GET /me, a silent
// re-login that is not fresh for a TOTP user, a provider login refused while
// sign-up is off, and a password login whose session passes a core gate
// permission rule once ark/iam's permission store grants the user a role.
package wiring

import (
	"arkoren.dev/ark/iam"
	"arkoren.dev/ark/oauth/oauthauth"
)

var (
	_ iam.Provider       = (*oauthauth.Client)(nil)
	_ iam.NativeProvider = (*oauthauth.Client)(nil)
)
