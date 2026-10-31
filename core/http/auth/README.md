# Authenticate requests before authorizing actions

Use [auth](https://pkg.go.dev/arkoren.dev/core/http/auth) to resolve your
application's user type from verified credentials. Parsing an identity header is
not verification. Your verifier must check authenticity, expiry, and revocation
before returning a user.

For an existing router `routes`, handler `me`, user type `User`, and verifier
`verifyToken(context.Context, string) (User, error)`:

```go
routes.Use(auth.Middleware(auth.Bearer(verifyToken)), auth.Require[User]())
routes.Get("/me", me)
```

Requests without credentials remain anonymous until `Require` rejects them.
Invalid credentials stop the chain. Return `ErrNoCredentials` only when your
authenticator has nothing to handle, not for a rejected token. `Require` defaults
to 401 with a Bearer challenge; `Builder.ErrorResponse` can replace the response.

## Browser sessions and resource access

Install session middleware and CSRF protection before cookie authentication.
Share one `auth.NewSessionIdentity(loadUser)` across authentication, login, and
logout. Check its `Err` during setup and handle login/logout mutation errors.
Rotation does not prove persistence succeeded; session middleware owns saving,
renewal, and retirement. Handles select context slots, not separate authorization
realms or physical identity fields.

Use [gate](https://pkg.go.dev/arkoren.dev/core/http/auth/gate) for permission
checks after authentication. Route admission does not authorize every resource:
check resource policy in the application operation and scope database queries.
Conditional writes or database locking must enforce concurrent invariants.

## Observe rejection without publishing identity data

Configure `auth.Builder.Events` or `gate.Builder.Events` to publish observations.
Authentication rejection carries `Method`; gate denial carries `Method` and the
zero-based denying `Rule`. Neither payload carries paths, queries, users, or raw
causes. Paths were removed because they can contain identifiers and credentials.
Listeners that previously read `Path` must select application-owned safe metadata
instead; no request-path replacement is populated automatically.

The event context still has request context values. Custom HTTP methods are
client-controlled. Select safe attributes rather than dumping contexts or
credential errors. Normal HTTP problem `instance` rendering is unchanged and can
include the request path. Every rejection publishes, so keep listeners cheap and
place rate limiting before these checks when needed.
