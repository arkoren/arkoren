// Package wiring is the development-only module that proves the public modules
// work together. It is never released and nothing imports it: arkoren.dev/ark/access
// must not import arkoren.dev/ark/oauth (a module boundary is a dependency
// boundary), so the only place that can show they fit is a module that requires
// both.
//
// The compile-time assertions in this file's package say that *oauthauth.Client
// satisfies access.Provider and access.NativeProvider. The end-to-end tests run
// the facade over HTTP against in-memory SQLite and a fake OpenID Connect
// provider: password with TOTP, the application's own users, social login then
// connect, native login then a device token then GET /me, and a silent
// re-login that is not fresh for a TOTP user.
package wiring

import (
	"arkoren.dev/ark/access"
	"arkoren.dev/ark/oauth/oauthauth"
)

var (
	_ access.Provider       = (*oauthauth.Client)(nil)
	_ access.NativeProvider = (*oauthauth.Client)(nil)
)
