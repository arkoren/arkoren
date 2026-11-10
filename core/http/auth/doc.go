// Package auth authenticates requests: it resolves the user of a request and
// stores it in the context. Authorization lives in the gate subpackage, and
// authtest checks Authenticator implementations.
//
// An [Authenticator] turns a request into a user of your own type U. It
// returns [ErrNoCredentials] when the request has nothing it understands, so
// [Middleware] tries the next one; any other error stops the request.
// Anonymous requests continue without a user; [Require] rejects them.
// Require uses [Builder] construction policies; session authentication uses
// immutable [SessionIdentity] to bind identifier type, loader and session.Handle.
// The user type implements [Identifiable] so the definition can map in both
// directions: the loader turns the stored identifier into the user and AuthID
// turns the user back into the identifier that [SessionIdentity.Login] stores.
// Zero definitions and nil loaders are invalid configuration; callbacks still
// run once in order. A later zero handle restores default lookup. Loaders are
// not invoked during construction. Invalid identities stop middleware fallback.
// Handles do not isolate cookies, storage or user contexts (keyed only by U).
// One physical session has one identity slot per [SessionBuilder.Realm].
// Definitions with the same realm and K can still mix users, and compatible
// persisted JSON may decode across types.
//
// # Wiring
//
// Order matters: session, then authentication, then everything that needs
// the user (rate limits keyed by user, routes).
//
//	r := router.New()
//	identity := auth.NewSessionIdentity(loadUser)
//	if err := identity.Err(); err != nil {
//		return err
//	}
//	r.Use(session.Middleware(store))
//	r.Use(csrf.Middleware())
//	r.Use(auth.Middleware(identity.Authenticator(), auth.Bearer(verifyKey)))
//	r.Use(ratelimit.Middleware(store, func(limiter *ratelimit.Builder) {
//		limiter.KeyFunc(func(r *http.Request) string {
//			if user, err := request.User[User](r); err == nil {
//				return "user:" + strconv.Itoa(user.ID)
//			}
//
//			return "ip:" + r.RemoteAddr
//		})
//	}))
//
//	r.Post("/login", login)
//	r.Post("/logout", logout)
//
//	private := r.With(auth.Require[User]())
//	private.Get("/me", me)
//	r.With(gate.Require(isAdmin)).Delete("/users/{id}", deleteUser)
//
// Where User reports its identifier and loadUser resolves it:
//
//	func (user User) AuthID() int { return user.ID }
//
//	func loadUser(ctx context.Context, id int) (User, error) {
//		user, err := db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
//			statement.Where("id", id)
//		}))
//		if errors.Is(err, database.ErrNoRows) {
//			return User{}, auth.ErrNotFound // deleted user: anonymous
//		}
//		return user, err
//	}
//
// # Login and logout
//
// Hash passwords with the hash package. Precompute dummyHash at startup with
// the same hasher and cost as real passwords. Checking it for unknown emails
// reduces timing differences; it does not guarantee identical request times.
// A missing account or password mismatch is a credential rejection; return
// database and hash failures without replacing them with [ErrUnauthenticated].
// JSON v2 matches field names case-sensitively, so tag the lower-case request
// members:
//
//	func login(w http.ResponseWriter, r *http.Request) error {
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
//		if err := identity.Login(request.MustSession(r), user, auth.MethodPassword); err != nil {
//			return err
//		}
//
//		return response.Status(w, http.StatusNoContent)
//	}
//
//	func logout(w http.ResponseWriter, r *http.Request) error {
//		if err := identity.Logout(request.MustSession(r)); err != nil {
//			return err
//		} // or Destroy() to drop all data
//		return response.Status(w, http.StatusNoContent)
//	}
//
// # Two-factor login and step-up
//
// Login records the [Method]s you verified and when. For a second factor, call
// [SessionIdentity.Challenge] after the first instead of Login: the session
// stays anonymous (the authenticator and [Middleware] see no user) until the
// further factor verifies and [SessionIdentity.Complete] logs the user in,
// once, with both methods. [SessionIdentity.Pending] returns the waiting user to
// the route verifying the factor; a challenge lasts [SessionBuilder.ChallengeTTL]
// (five minutes by default). Every transition rotates the session ID.
//
//	if !enrolled {
//		return identity.Login(current, user, auth.MethodPassword)
//	}
//	return identity.Challenge(current, user, auth.MethodPassword)
//
//	// in the verification route, after checking the code for pending:
//	pending, err := identity.Pending(r.Context(), current)
//	return identity.Complete(r.Context(), current, auth.MethodOTP)
//
// [SessionIdentity.RequireMethods] and [SessionIdentity.RequireFresh] guard
// sensitive routes and return [ErrStepUpRequired] (403) when the session lacks
// the methods or has none proved recently; [SessionIdentity.Confirm] records a
// fresh proof and [SessionIdentity.State] reads the record. Bearer-authenticated
// requests and sessions created before methods existed fail these checks.
// Arkoren verifies no factor itself and the pending state is not rate limited:
// limit attempts on the verification route.
//
// A user type that implements [Versioned] also ends its other sessions when
// AuthVersion changes: Login stores a digest, and the authenticator compares it
// with the loaded user's on every request.
//
// # Cookie authentication needs CSRF protection
//
// Browsers send session cookies on cross-site requests, so install
// csrf.Middleware whenever [SessionIdentity] authenticates browser cookies.
//
// # Deleted users
//
// Sessions outlive accounts. Return [ErrNotFound] from the load function when
// the user no longer exists; the request becomes anonymous instead of failing
// with a 500. [ErrNotFound] wraps [ErrNoCredentials], so session authentication
// treats both the same. Return plain [ErrNoCredentials] for a user that exists
// but is disabled: code that cleans up after deleted users acts only on
// [ErrNotFound].
//
// # Subjects
//
// Packages that attach data to users without knowing your user type refer to
// them by subject, a text form of the identifier: a string as is, an integer as
// decimal digits, or the text form of a [encoding.TextMarshaler] such as a UUID.
// A subject is 1 to 255 printable ASCII bytes without spaces. [SessionIdentity.Subject]
// and [SessionIdentity.Load] convert in both directions, and
// [SessionIdentity.Directory] returns a non-generic [Directory] to hand to such
// packages. Identifier types that are none of these (structs, for example) still
// work for sessions and fail only when a subject is requested with
// [ErrInvalidSubject].
//
//	subject, err := identity.Subject(user)
//	directory, err := identity.Directory()
//	err = directory.Exists(ctx, subject) // nil, auth.ErrNotFound, or another error
//
// # Scopes
//
// A user type that carries granted scopes implements [Scoped], which lets
// gate.HasScopes check them on any user type.
//
// # API keys
//
// Look the key up by a non-secret id or hash, and compare secrets in
// constant time. Bearer validates the header, not your application's key
// format: even a one-byte token reaches the verifier. This application uses
// an eight-byte lookup prefix and stores the full token's SHA-256 hash in
// key.Hash. Reject short tokens before slicing them:
//
//	auth.Bearer(func(ctx context.Context, token string) (Key, error) {
//		const prefixLength = 8
//		if len(token) < prefixLength {
//			return Key{}, auth.ErrUnauthenticated
//		}
//
//		key, err := keys.ByPrefix(ctx, token[:prefixLength])
//		if err != nil {
//			return Key{}, err
//		}
//
//		sum := sha256.Sum256([]byte(token))
//		if subtle.ConstantTimeCompare(sum[:], key.Hash) != 1 {
//			return Key{}, auth.ErrUnauthenticated
//		}
//
//		return key, nil
//	})
//
// keys.ByPrefix is application code. It must reject unknown or revoked keys
// with an error other than [ErrNoCredentials], which would allow anonymous
// fallback. Bearer wraps lookup failures as 401 responses while retaining
// their causes; do not include tokens in errors that can reach logs.
//
// # JWT bearer tokens
//
// Verify the signature and claims in the verify function and build the user
// from the claims without a database call. The trade-off is revocation lag:
// a token stays valid until it expires, so keep lifetimes short.
//
// # OAuth2 and OIDC
//
// The driver/oauth/oauthauth package plugs into auth without auth importing it. For
// API calls, verify bearer access tokens; for browser logins, finish the flow
// with Callback and read it back using the same [SessionIdentity]:
//
//	r.Use(auth.Middleware(
//		auth.Bearer(client.Verify),
//		identity.Authenticator(),
//	))
//	r.Get("/oauth/callback", client.Callback(identity, mapIdentityToUser))
//
// See the package documentation of arkoren.dev/driver/oauth/oauthauth for the
// full setup.
//
// # Types
//
// The user is stored under its type. If [Middleware] uses User but [Require]
// asks for *User, the lookup finds nothing and the request fails closed with
// 401. Define one helper in your application and use it everywhere:
//
//	func CurrentUser(r *http.Request) (User, error) {
//		return request.User[User](r)
//	}
//
// # Events
//
// With [Builder.Events], [Require] publishes [Rejected] ("http.auth.rejected")
// with the method of every anonymous request it rejects. The response defaults
// to 401; ErrorResponse can replace it. Paths are not published because they can
// contain identifiers or credentials. Clients control the event rate, so
// listeners must be cheap and a rate limiter should sit
// in front of [Require]. Payloads carry no user, credentials, query, or cause.
// Event contexts still retain request context values; listeners must select safe
// attributes. These payload limits do not change HTTP problem instance rendering.
package auth
