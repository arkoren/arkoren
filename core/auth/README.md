# Authenticate requests before authorizing actions

Use [auth](https://pkg.go.dev/arkoren.dev/core/auth) to resolve who is acting
from verified credentials. The result is an immutable `auth.Identity` (subject,
factors, scopes, optional actor and user record) that any package can read
without knowing your application's user type. Parsing an identity header is not
verification. Your verifier must check authenticity, expiry, and revocation
before returning an identity.

For an existing router `routes`, handler `me`, and verifier
`verifyToken(context.Context, string) (auth.Identity, error)`:

```go
routes.Use(auth.Middleware(auth.Bearer(verifyToken)), auth.Require())
routes.Get("/me", me)
```

Requests without credentials remain anonymous until `Require` rejects them.
Invalid credentials stop the chain. Return `ErrNoCredentials` only when your
authenticator has nothing to handle, not for a rejected token. `Require` defaults
to 401 with a Bearer challenge; `MiddlewareBuilder.ErrorResponse` can replace the
response.

Read the identity with `request.Identity(r)` or `auth.FromContext`. When the
authenticator attached your application user to the identity's record,
`request.User[User](r)` and `auth.UserFromContext[User]` return it; a record
holding another type is reported as absent.

## Browser sessions and resource access

Install session middleware and CSRF protection before cookie authentication.
Create one `auth.NewLogin(provider, store)` and share it across authentication,
login, and logout. Every login method calls `Begin` with the subject and the
factor it verified; `Begin` logs the user in, or parks a pending login when a
registered `SecondFactor` check says a second factor is needed (finish with
`Pending`, `Fail` and `Complete`). `Confirm` records a fresh factor on a
logged-in session. `RequireLevel` and `RequireFresh` guard routes by the factors
of the identity, so they also work for tokens. The provider is asked on every
request, so a deleted or disabled user is anonymous. Each login also creates a
login record, so a user can list devices with `Logins` and log them out with
`Revoke`, `RevokeOthers` and `RevokeAll`; a revoked device is anonymous on its
next request. Handle mutation errors.
Rotation does not prove persistence succeeded; session middleware owns saving,
renewal, and retirement. Handles select context slots, not separate authorization
realms or physical identity fields.

Use [gate](https://pkg.go.dev/arkoren.dev/core/auth/gate) for permission
checks after authentication. Route admission does not authorize every resource:
check resource policy in the application operation and scope database queries.
Conditional writes or database locking must enforce concurrent invariants.

## Look up users without HTTP

`auth.NewUserProvider` wraps a `UserDriver` so jobs, MCP tools, and feature
packages can find a user by subject and clean up user data through
`Forget`. See the package documentation for the subject rules.

## Observe rejection without publishing identity data

Configure `auth.MiddlewareBuilder.Events` or `gate.Builder.Events` to publish
observations. Authentication rejection carries `Method`; gate denial carries
`Method` and the zero-based denying `Rule`. Neither payload carries paths,
queries, identities, or raw causes. Paths were removed because they can contain
identifiers and credentials. Listeners that previously read `Path` must select
application-owned safe metadata instead; no request-path replacement is populated
automatically.

The event context still has request context values. Custom HTTP methods are
client-controlled. Select safe attributes rather than dumping contexts or
credential errors. Normal HTTP problem `instance` rendering is unchanged and can
include the request path. Every rejection publishes, so keep listeners cheap and
place rate limiting before these checks when needed.
