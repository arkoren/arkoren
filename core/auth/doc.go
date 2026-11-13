// Package auth answers two questions for any part of an application: who is
// acting, and does this user exist. Authentication of HTTP requests, the
// application user lookup and the cleanup of user data share one vocabulary, the
// subject, and none of them needs to know the application's user type.
// Authorization lives in the gate subpackage, and authtest checks drivers and
// authenticators.
//
// # Identity
//
// An [Identity] says who is acting: a [Identity.Subject], the [Factor] values
// that were verified, the granted [Identity.Scopes], an optional
// [Identity.Actor] and the [UserRecord] the authenticator resolved. It is an
// immutable value built with [NewIdentity] and read from a context with
// [FromContext], so jobs, MCP tools and arks can use it without importing the
// application's user type. [Identity.Level] is derived from the factors:
// [LevelNone] without factors, [LevelSingle] for factors of one kind and
// [LevelMulti] for two distinct kinds among [KindKnowledge], [KindPossession]
// and [KindInherence]. [KindFederated] never counts toward [LevelMulti].
//
// A request authenticator may attach the application's own user to the record.
// [UserFromContext] returns it by type, and a value of another type is reported
// as absent, so a mismatch fails closed.
//
// # Authenticating requests
//
// An [Authenticator] turns a request into an [Identity]. It returns
// [ErrNoCredentials] when the request has nothing it understands, so
// [Middleware] tries the next one; any other error stops the request.
// Anonymous requests continue without an identity; [Require] rejects them.
// [Bearer] adapts a token verifier. [Require] uses [MiddlewareBuilder]
// construction policies.
//
// Browser sessions use [Guard], described below. Its [Guard.Authenticator]
// reads the session record, asks the [UserProvider] on every request and builds
// an identity with the stored factors and the provider's [UserRecord].
//
// # Wiring
//
// Order matters: session, then authentication, then everything that needs
// the identity (rate limits keyed by user, routes).
//
//	r := router.New()
//	driver, err := auth.NewUserDriver(loadUser, userID)
//	if err != nil {
//		return err
//	}
//	users, err := auth.NewUserProvider(driver)
//	if err != nil {
//		return err
//	}
//	guard, err := auth.NewGuard(users, store)
//	if err != nil {
//		return err
//	}
//	r.Use(session.Middleware(store))
//	r.Use(csrf.Middleware())
//	r.Use(auth.Middleware(guard.Authenticator(), auth.Bearer(verifyKey)))
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.KeyFunc(func(r *http.Request) string {
//			if current, err := request.Identity(r); err == nil {
//				return "user:" + current.Subject()
//			}
//
//			return "ip:" + r.RemoteAddr
//		})
//	}))
//
//	r.Post("/login", signIn)
//	r.Post("/logout", guard.LogoutHandler())
//
//	private := r.With(auth.Require())
//	private.Get("/me", me)
//	r.With(gate.Require(isAdmin)).Delete("/users/{id}", deleteUser)
//
// # Guard, levels and step-up
//
// [Guard] turns "this person proved X" into a logged-in session. Every login
// method (password, OAuth, SAML, passkey) verifies its own credential and then
// calls [Guard.Begin] with the subject and the verified [Factor]. Begin looks
// the user up with the provider (a deleted or disabled user is
// [ErrUnauthenticated]) and asks each check registered with
// [GuardBuilder.SecondFactor], so core never imports the package that knows
// whether a user is enrolled. If none says yes the user is logged in and Begin
// returns true. Otherwise the login is parked as pending and the request stays
// anonymous: the handler asks for the second factor, reads the user with
// [Guard.Pending], reports a wrong code with [Guard.Fail] (the pending login is
// discarded after [GuardBuilder.MaxAttempts]) and finishes with [Guard.Complete],
// which is single use. [GuardBuilder.ChallengeTTL] bounds the wait.
// [Guard.Confirm] records a fresh factor on a logged-in session, such as a
// re-entered password. Every transition regenerates the session ID.
//
// A session that is already logged in as the subject keeps its login: Begin
// then adds the factor like Confirm and skips the second-factor checks, so a
// logged-in user who connects a provider or re-authenticates through one is not
// challenged again and keeps the factors they have. Releases before v0.6.0
// replaced the factors and parked the login instead. [Guard.LogoutHandler]
// returns the handler of the logout route: it ends the login with
// [Guard.Logout], publishes [LoggedOut] and answers 204, or 303 with
// [LogoutBuilder.Redirect].
//
// The session holds one record (subject, factors, a hash of the user's
// [UserRecord.Version] and a login-record ID) under one key, and a pending login
// under another. The authenticator compares the version in constant time, so
// changing the version ends every other session of the user. Sessions written
// before this layout, under "arkoren:auth:user", are anonymous.
//
// # Devices
//
// Every login (Begin without a second factor, and Complete) creates a
// [LoginRecord] with a random ID that survives session ID rotation. All records
// of a user live in one entry of the cache given to [NewGuard], under
// "arkoren:auth:logins", whose lifetime is [GuardBuilder.SessionLifetime] (set it
// to the session middleware's maximum lifetime). [Guard.Logins] lists them and
// flags the current one, [Guard.Revoke], [Guard.RevokeOthers] and
// [Guard.RevokeAll] log devices out, and [Guard.LoginsOf] serves administrators.
// The authenticator reads the entry on every request and treats a missing record
// as [ErrNoCredentials], so a revoked device cannot list or revoke devices
// either; it refreshes [LoginRecord.SeenAt] at most every five minutes, ignoring
// a failed write. Records older than the session lifetime are never listed and
// the next write removes them. At most [GuardBuilder.MaxLogins] records are kept
// and the oldest is dropped, which logs that device out. [Guard.Logout] removes its record and
// [NewGuard] registers [Guard.RevokeAll] with [UserProvider.OnForget].
//
// Records hold the user agent (cut to 255 bytes) and client address, which are
// personal data: [GuardBuilder.Devices] turns them off, and they never enter
// telemetry or events. The address is the host of r.RemoteAddr unless
// [GuardBuilder.ClientIP] says otherwise; behind a reverse proxy pass
// ratelimit.TrustedProxyKey from core/http/middleware/ratelimit with your proxy
// prefixes (it reports IPv6 addresses as their /64 prefix). Writes take a per-user cache lock and compare-and-put with a bounded
// retry. A flushed cache logs everyone out, and each authenticated session
// request costs one extra cache read.
//
// [RequireLevel] and [RequireFresh] protect routes by the factors of the identity
// in the context, so they also work for tokens that carry factors. Too weak or
// too old is [ErrStepUpRequired] (403); no identity is rejected like [Require].
// A factor dated in the future is not fresh.
//
// Hash passwords with the hash package. Precompute dummyHash at startup with
// the same hasher and cost as real passwords. Checking it for unknown emails
// reduces timing differences; it does not guarantee identical request times.
// A missing account or password mismatch is a credential rejection; return
// database and hash failures without replacing them with [ErrUnauthenticated].
// JSON v2 matches field names case-sensitively, so tag the lower-case request
// members:
//
//	func signIn(w http.ResponseWriter, r *http.Request) error {
//		in, err := request.JSON[struct {
//			Email    string `json:"email"`
//			Password string `json:"password"`
//		}](r)
//		if err != nil {
//			return err
//		}
//
//		user, err := db.Find[User](r.Context(), query.Table("users", func(statement *query.Builder) {
//			statement.Where("email", in.Email)
//		}))
//		if err != nil && !errors.Is(err, database.ErrNoRows) {
//			return err
//		}
//
//		found := err == nil
//		hashed := dummyHash
//		if found {
//			hashed = user.PasswordHash
//		}
//		ok, err := hasher.CheckRaw([]byte(in.Password), hashed)
//		if err != nil {
//			return err
//		}
//		if !found || !ok {
//			return auth.ErrUnauthenticated
//		}
//
//		subject, err := driver.Subject(user) // the user driver of the wiring above
//		if err != nil {
//			return err
//		}
//
//		factor := auth.Factor{Method: auth.MethodPassword, Kind: auth.KindKnowledge}
//		complete, err := guard.Begin(r, subject, factor)
//		if err != nil {
//			return err
//		}
//		if !complete {
//			return response.Status(w, http.StatusAccepted) // ask for the second factor
//		}
//
//		return response.Status(w, http.StatusNoContent)
//	}
//
//	r.With(auth.RequireLevel(auth.LevelMulti)).Post("/payouts", payout)
//	r.With(auth.RequireFresh(10 * time.Minute)).Delete("/account", destroy)
//
// # Cookie authentication needs CSRF protection
//
// Browsers send session cookies on cross-site requests, so install
// csrf.Middleware whenever [Guard] authenticates browser cookies.
//
// # Deleted users
//
// Sessions outlive accounts. [Guard.Authenticator] asks the provider on every
// request, and a user that is deleted ([ErrUserNotFound]) or disabled
// ([ErrUserDisabled]) becomes anonymous instead of failing with a 500.
//
// # API keys
//
// Look the key up by a non-secret id or hash, and compare secrets in
// constant time. Bearer validates the header, not your application's key
// format: even a one-byte token reaches the verifier. This application uses
// an eight-byte lookup prefix and stores the full token's SHA-256 hash in
// key.Hash. Reject short tokens before slicing them:
//
//	auth.Bearer(func(ctx context.Context, token string) (auth.Identity, error) {
//		const prefixLength = 8
//		if len(token) < prefixLength {
//			return auth.Identity{}, auth.ErrUnauthenticated
//		}
//
//		key, err := keys.ByPrefix(ctx, token[:prefixLength])
//		if err != nil {
//			return auth.Identity{}, err
//		}
//
//		sum := sha256.Sum256([]byte(token))
//		if subtle.ConstantTimeCompare(sum[:], key.Hash) != 1 {
//			return auth.Identity{}, auth.ErrUnauthenticated
//		}
//
//		return auth.NewIdentity(key.OwnerSubject, func(principal *auth.IdentityBuilder) {
//			principal.Scopes(key.Scopes...)
//		}), nil
//	})
//
// keys.ByPrefix is application code. It must reject unknown or revoked keys
// with an error other than [ErrNoCredentials], which would allow anonymous
// fallback. Bearer wraps lookup failures as 401 responses while retaining
// their causes; do not include tokens in errors that can reach logs.
//
// # JWT bearer tokens
//
// Verify the signature and claims in the verify function and build the identity
// from the claims without a database call. The trade-off is revocation lag:
// a token stays valid until it expires, so keep lifetimes short.
//
// # OAuth2 and OIDC
//
// The arkoren.dev/ark/oauth/oauthauth package logs users in through
// [Guard.Begin] with a federated factor and verifies OIDC bearer tokens; auth
// does not import it. arkoren.dev/ark/federation maps its external identities
// to local subjects.
//
// # Subjects
//
// A subject is the stable text reference of a user: 1 to 255 bytes of
// printable ASCII without spaces. It is case sensitive and compared bytewise.
// [ValidateSubject] checks the rule; [ErrInvalidSubject] reports a violation.
// [NewUserDriver] converts between a typed identifier and its subject: strings
// are used as is, integers become decimal digits, and types implementing
// [encoding.TextMarshaler] and (through a pointer) [encoding.TextUnmarshaler]
// use their text form. Parsing is canonical: a subject that does not render
// back to itself, such as "007" or "+1" for an integer, is an invalid subject.
//
// # Looking up users
//
// A [UserDriver] finds one [UserRecord] by subject. Wrap it with
// [NewUserProvider] and call [UserProvider.Find]. A missing user is
// [ErrUserNotFound]; a user that exists but may not act is [ErrUserDisabled].
// Only [ErrUserNotFound] proves a user is deleted, so cleanup code must never
// treat any other error as deletion.
//
// # Forgetting users
//
// Packages that store data about users register a hook with
// [UserProvider.OnForget]. [UserProvider.Forget] runs the hooks in
// registration order in the caller's context, so hooks join the caller's
// database transaction. The first error stops the run and is returned. When
// every hook succeeded, Forget emits [UserForgotten] on the bus given to
// [UserProviderBuilder.Events]; without a bus nothing is emitted.
//
// # Events
//
// With [MiddlewareBuilder.Events], [Require] publishes [Rejected]
// ("http.auth.rejected") with the method of every anonymous request it
// rejects. The response defaults to 401; ErrorResponse can replace it. Paths
// are not published because they can contain identifiers or credentials.
// Clients control the event rate, so listeners must be cheap and a rate limiter
// should sit in front of [Require]. Payloads carry no identity, credentials,
// query, or cause. Event contexts still retain request context values;
// listeners must select safe attributes. These payload limits do not change
// HTTP problem instance rendering.
//
// [Guard.LogoutHandler], built with [LogoutBuilder.Events], publishes [LoggedOut]
// ("auth.user.logged-out") after a logged-in user logged out. It carries only
// the subject.
//
// [UserForgotten] ("auth.user.forgotten") carries only the subject. It means
// the forget hooks completed, not that a surrounding transaction committed.
//
// # Drivers and tests
//
// [arkoren.dev/core/auth/memoryauth] is an in-memory user driver and
// [arkoren.dev/core/auth/authtest] holds the conformance suites: one for every
// user driver and one for every [Authenticator].
//
// Example:
//
//	driver, err := auth.NewUserDriver(loadUser, func(user User) int64 { return user.ID })
//	if err != nil {
//		return err
//	}
//	users, err := auth.NewUserProvider(driver, func(users *auth.UserProviderBuilder) {
//		users.Events(app.Events())
//	})
//	if err != nil {
//		return err
//	}
//	record, err := users.Find(ctx, "42")
package auth
