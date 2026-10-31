// Package restate holds the code shared by the Restate drivers: the [Host],
// which serves one Restate service endpoint and talks to the Restate server.
//
// Construction callbacks edit one fresh [Builder], once in order; a supplied
// nil callback panics. Expanded nil or empty callback slices leave defaults
// unchanged. Publication copies owned settings and identity-key slices, not
// HTTP clients, loggers, or callback captures.
//
//	host := restate.New(func(host *restate.Builder) {
//		host.ClientOnly(true)
//	})
//	_ = host.Addr() // Client-only hosts never open a listener.
//
// A module root never holds a driver. Each driver, such as
// arkoren.dev/driver/restate/restateschedule or arkoren.dev/driver/restate/restateworkflow,
// binds its services with [Host.Bind] before [Host.Start] runs.
//
// The lifecycle has three steps. [Host.Start] builds the endpoint and opens
// the listener without serving. [Host.Serve] serves requests, registers the
// deployment when [Builder.Register] is set, and blocks until its context is
// done. [Host.Stop] shuts the endpoint down.
// Stop also releases a listener opened by Start when Serve never ran. It waits
// for active HTTP requests until its context expires, then closes remaining
// connections. Request cancellation is cooperative, not forced termination of
// application code. The HTTP client supplied at construction remains shared
// and is not closed by endpoint cleanup.
//
// A host with [Builder.ClientOnly], or with nothing bound, opens no listener and
// registers nothing; it only reaches the ingress and the admin API. Use it in
// processes that start work but do not run it.
//
// [Host.Query] runs SQL against the introspection tables of the admin service.
// [Builder.AdminAuthKey] authenticates the admin calls, and
// [Builder.RegisterForce] and [Builder.RegisterBreaking] set the registration
// policy.
//
// The endpoint verifies that each request is signed by the Restate server.
// Set [Builder.IdentityKeys] to the request-identity public keys
// ("publickeyv1_...") of the Restate server. A host that would listen without
// keys fails [Host.Start] with [ErrNoIdentityKeys]; [Builder.AllowInsecure]
// opts out for local development and tests. The server also sets
// ReadHeaderTimeout and IdleTimeout, but no ReadTimeout or WriteTimeout,
// because Restate keeps long-lived HTTP/2 streams open.
//
// The endpoint speaks HTTP/2 without TLS (h2c) and HTTP/1.1. The Restate
// server must be able to reach [Builder.AdvertisedURL], and the deployment must
// be registered, either by [Builder.Register] or by the operator.
package restate
