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
// Zero definitions and nil loaders are invalid configuration; callbacks still
// run once in order. A later zero handle restores default lookup. Loaders are
// not invoked during construction. Invalid identities stop middleware fallback.
// Handles do not isolate cookies, storage or user contexts (keyed only by U).
// One physical session has one identity slot. Definitions with the same K can
// still mix realms, and compatible persisted JSON may decode across types.
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
// Where loadUser resolves the stored identifier:
//
//	func loadUser(ctx context.Context, id int) (User, error) {
//		user, err := db.Find[User](ctx, query.Table("users", func(statement *query.Builder) {
//			statement.Where("id", id)
//		}))
//		if errors.Is(err, database.ErrNoRows) {
//			return User{}, auth.ErrNoCredentials // deleted user: anonymous
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
//		if err := identity.Login(request.MustSession(r), user.ID); err != nil {
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
// # Cookie authentication needs CSRF protection
//
// Browsers send session cookies on cross-site requests, so install
// csrf.Middleware whenever [SessionIdentity] authenticates browser cookies.
//
// # Deleted users
//
// Sessions outlive accounts. Return [ErrNoCredentials] from the load
// function when the user no longer exists; the request becomes anonymous
// instead of failing with a 500.
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
//	r.Get("/oauth/callback", client.Callback(identity, mapIdentityToUserID))
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
