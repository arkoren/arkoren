---
title: Sessions
description: Store visitor state in a cache-backed session and understand identity rotation, expiry, and response-time persistence.
order: 10
summary:
  - Store typed visitor state while keeping cookie trust, CSRF protection, and value ownership explicit.
  - Rotate or destroy sessions before response commitment and understand guarded retirement and writer rollout.
  - Set idle and absolute lifetimes and observe backend failures without exposing session credentials.
---

# Sessions

A cart selection or signed-in identity must survive the next HTTP request. A
**session** keeps that state on the server, in your [cache](../cache/overview.md);
the browser carries only a random session ID in a cookie. Possession of that
cookie grants access to the session, so treat it as a bearer credential.

## Enable Sessions

Use an existing `*cache.Cache` named `store` and router named `routes`. Choose
the cache's [driver and lifecycle](../cache/drivers.md) first. An in-process cache
is sufficient for local development; replicas need shared storage to see the
same records. Sharing a cache does not make its data durable or prevent eviction.

Import `arkoren.dev/core/http/session` and install the middleware before routes
that use it:

```go
routes.Use(session.Middleware(store))
```

The middleware loads a session before the handler and saves changed state before
the first final response headers. A new, unused session normally creates neither
a cache record nor a cookie; automatic renewal can itself mark state changed.
Missing, malformed, unknown, or retired IDs start fresh sessions. A cache read
failure instead fails the request, rather than silently replacing known state.

The default cookie is `__Host-arkoren.session`: `Secure`, `HttpOnly`, `Path=/`,
no `Domain`, and `SameSite=Lax`. The host prefix prevents sibling subdomains from
setting that cookie in conforming browsers. Setting a domain or narrower path
changes that trust boundary and, with an automatically selected name, drops the
host prefix. Explicit prefix names must satisfy their browser rules or
construction panics.

Register [CSRF protection](../browser-security/csrf-protection.md) before
authentication and state-changing handlers. `HttpOnly` limits script access;
`SameSite=Lax` is not a replacement for CSRF protection. Do not log cookie values,
`Session.ID()`, or `OriginalID()`.

For local plain HTTP only, use this alternative registration:

```go
routes.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.AllowInsecure(true)
}))
```

This selects the unprefixed default cookie name. Never enable it in production:
HTTP exposes the bearer ID in transit. Do not rely on browser-specific localhost
exceptions for secure cookies.

## Store Values

Share a **field**, an immutable declaration of a storage name and Go type,
between handlers that read and write the same value. For a cart selected through
[validated request input](../http-fundamentals/requests.md#validate-input), define
the application's writable shape and field:

```go
type CartSelection struct {
	CartID string `json:"cart_id"`
}

var CartID = session.NewField[string]("cart_id")
```

In the handler, `input` is an already decoded, validated `CartSelection`; verify
that the visitor may use that cart before storing its ID. With
`arkoren.dev/core/http/request` imported and incoming request `r`:

```go
current := request.MustSession(r)
if err := current.Put(CartID, input.CartID); err != nil {
	return err
}
```

`Put` changes this request's session, not the cache. It does not authorize the
cart or confirm persistence. `MustSession` panics if middleware is missing; use
`request.Session(r)` and its `ok` result for code that also runs without sessions.

In a later handler, read the same field. Import `errors` to distinguish absence
from an incompatible value:

```go
current := request.MustSession(r)
saved, err := current.Get(CartID)
switch {
case errors.Is(err, session.ErrKeyNotFound):
	// No cart has been selected.
case err != nil:
	return err
default:
	// saved is the stored string; authorize access before loading the cart.
}
```

Use `current.Delete(CartID)` to remove the selection and check its error before
continuing. Deleting an absent valid field succeeds and still marks state
changed. Empty or zero-value fields return `ErrInvalidField` without mutation.

Field names are literal: no trimming, normalization, or automatic prefixing.
Different declarations with the same name address the same entry, even with
different types; they do not create a storage schema. `Name()` reveals the name,
while ordinary formatting and structured logging redact it. Keep the reserved
`arkoren:auth:` prefix for authentication; it uses `arkoren:auth:user`, `:state`
and `:pending`, each with an optional realm suffix.

### Value Ownership

Runtime values must already have the field's Go type. A differently typed number
or map returns `ErrInvalidValueType`; supplying `encoding/json/jsontext.Value`
does not turn runtime input into persisted JSON. Only fields loaded from stored
records permit typed JSON decoding, which preserves integer precision. Replacing
a loaded field with `Put` restores runtime type checking until the next save and
load. Use JSON-compatible values and share field declarations across readers.

A present nil interface is not an absent field: interface fields can return
`nil, nil`, while concrete fields reject that value. Typed nil pointers retain
their pointer type.

`Put` retains values without copying them. The mutex protects session state, not
nested maps, slices, or pointed-to objects. Do not mutate borrowed values
concurrently with access or persistence; call `Put` again after a mutation to
mark it changed. Fields have no separate TTL.

For standalone tests, `session.New(expiresAt, initial)` creates changed state but
does not attach or save it. Do not use a zero `Session`. `New` snapshots only the
outer input map; do not mutate that input during construction. A nil map creates
empty writable state, and nested values remain shared. `All` returns only a shallow
copy and exposes loaded values as borrowed `jsontext.Value` slices. Passing that
copy to `New` makes them runtime input again, not persisted fields.

## Group Fields

When a feature owns several fields of one type, declare a namespace rather than
repeating its prefix:

```go
cart := session.NewNamespace[string]("cart")
coupon := cart.Field("coupon")
err := current.Put(coupon, "WELCOME")
```

The field's physical name is `cart:coupon`; it behaves like any standalone
`Field[string]`. This adds neither expiry nor authorization. To adopt a namespace
without migrating data, preserve each existing physical name.

`Derive` creates an independent definition. If existing names use dots, an
alternative definition can retain that convention:

```go
legacy := cart.Derive(func(space *session.NamespaceBuilder) {
	space.Separator(".")
})
city := legacy.Subnamespace("shipping").Field("city")
```

`city.Name()` is `cart.shipping.city`. A subnamespace joins with its parent's
separator before its own callbacks configure future joins. Empty separators
concatenate names. Names are not escaped: `a:b` joined with `c` collides with `a`
joined with `b:c`. Validate or encode untrusted components and convert numeric
IDs explicitly. Invalid ancestors retain their first `ErrInvalidField` through
derivation; fields inherit it. Construction callbacks run once in order, nil
callbacks panic, and retained drafts cannot change published definitions.
Namespace formatting also redacts names; `Name()` deliberately reveals them.

This field namespace is distinct from the middleware's cache namespace, which
names whole records. To customize record names, pass `Builder.Namespace` a
`cache.Namespace[jsontext.Value]`, as explained under
[cache key policy](../cache/keys.md). The default produces
`arkoren:session:ID`. Choose collision-free literal names; a zero or invalid
namespace panics after configuration. Session lifetime overrides its TTL.

## Change Identity

Rotate the ID after login or privilege changes to prevent **session fixation**:
an attacker arranging for a victim to authenticate into a known session. For
login, use the same `auth.SessionIdentity` as your authentication middleware;
[Session Login](../authentication/session-login.md) owns the user loader,
credential checks, and complete identity setup.

After credentials succeed, with the configured `identity` and the loaded `user` (which reports its identifier through `AuthID`):

```go
if err := identity.Login(request.MustSession(r), user, auth.MethodPassword); err != nil {
	return err
}
```

Login stores the identifier and the verified methods, then regenerates the ID only
if those writes succeed. See [Two-Factor Login](../authentication/two-factor.md)
for holding a login until a second factor passes.
For other privilege changes, call `current.Regenerate()` after successfully
changing the authentication-related session state. Regeneration keeps the fields
and original creation time; it does not log the visitor out.

To remove the identity but retain unrelated fields:

```go
if err := identity.Logout(request.MustSession(r)); err != nil {
	return err
}
```

Logout removes even a stale, wrongly typed identifier, the recorded methods and
any pending challenge, and rotates the ID. To end
the entire session instead, call `current.Destroy()` before response headers.
Destroy discards later changes. `Clear` only clears fields; it neither rotates
nor retires the ID.

None of these calls performs storage I/O. On a non-5xx response, middleware saves
a regenerated record and issues its cookie before retiring the original ID.
Destroy retires the original and expires the cookie, even if retirement fails.
A failed save leaves the previous session intact; a failed retirement can leave
the old ID usable. A successful login/logout HTTP response alone therefore does
not prove the storage transition succeeded.

## Save Changes

Finish session mutations before [response commitment](../http-fundamentals/responses.md#response-commitment),
including before [streaming](../http-fundamentals/streaming-responses.md).
Persistence runs before the first final status, not after every mutation. A
`5xx` status skips persistence; hijacked connections such as WebSocket upgrades
do not save changes. This does not roll back database effects or undo state
already saved before a later response failure.

Retirement replaces the old record with a **tombstone**, a marker that rejects
reuse. Loaded sessions re-read their original record, then atomically compare
those exact bytes and write the current ID using the cache driver's
`CompareAndPut`. A missing, retired, or subsequently changed original drops the
save without a cookie or retry. The same guard protects a proposed regenerated
ID. Guard rejection is not a persistence error.

Ten-second per-ID [cache leases](../cache/locks.md) reduce conflicts; the atomic
guard, not the lease, prevents stale revival after lease expiry. Ordinary
concurrent requests otherwise save whole snapshots in lease order:
last-write-wins can lose independent field edits. Retirement neither revokes a
different ID committed earlier nor cancels a handler's completed effects.

> [!WARNING]
> Upgrade every session writer and drain older requests before relying on guarded
> retirement. An old unconditional writer or restoration of an earlier live
> record can bypass it. Custom cache drivers must implement atomic
> `CompareAndPut`, never emulate it with separate reads and writes or fall back
> to `Put`. The rollout changes no JSON format, key names, cookie names, or TTL
> settings.

Tombstones live for `MaxLifetime`, or the idle TTL when the maximum is disabled.
Missing originals also reject guarded writes; marker retention is not a promise
to revoke every independently issued session.

Primary persistence keeps request context values but ignores its cancellation
and deadline, applying `PersistenceTimeout` instead: five seconds by default.
Each best-effort lease release has a separate ten-second cleanup budget and
falls back to lease expiry on failure. Cleanup can delay headers beyond the
primary timeout. These bounds require a driver that honors context; synchronous
error handlers and event listeners are unbounded by them, so keep callbacks fast.

## Set Lifetimes

The idle lifetime defaults to two hours from creation or renewal. Before response
commitment, a live session renews when strictly less than 15 minutes remain,
not on every request. The absolute maximum is 24 hours from original creation.
With `time` imported, choose a different policy at construction:

```go
routes.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.TTL(time.Hour)
	cookies.MaxLifetime(7 * 24 * time.Hour)
}))
```

A positive maximum caps initial expiry, renewal, and `Session.Extend` at the
original creation time plus that maximum. Regeneration does not reset it. The
cookie expiry and record TTL use the cap; capping unused state alone does not
create a record or cookie.

Equality counts as expired. If the absolute deadline is reached during a handler,
middleware discards its record write and new/refreshed cookie rather than
granting the same authenticated state a fresh lifetime. An expired loaded session
is replaced on the next request without its old fields. An idle deadline reached
during a handler can still trigger regeneration and renewal, subject to the
absolute maximum. No nonpositive remaining TTL is passed to the cache as its
zero-TTL/no-expiry policy.

Zero restores defaults for `TTL`, `MaxLifetime`, `ExpirationDelta`, and
`PersistenceTimeout`; session TTL zero never means forever. Negative
`MaxLifetime` disables the absolute cap; negative `ExpirationDelta` disables
near-expiry renewal. Final negative `TTL` or `PersistenceTimeout` panics after
all configuration callbacks. A later callback can replace an earlier invalid
setting before validation.

## Observe Failures

Save, verification, lease-acquisition, and retirement errors do not themselves
change the response status. They are discarded unless you configure observation.
Use your access-controlled `*slog.Logger` named `logger` and optional application
event bus `events` in this alternative registration:

```go
routes.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.ErrorHandler(func(err error) {
		logger.Error("session persistence failed", "error", err)
	})
	cookies.Events(events)
}))
```

The error callback receives unredacted diagnostics, including backend causes
that can contain session keys. Apply your own redaction and retention policy.
This is different from the field-name formatting protections above.

Events report observed outcomes: `session.expired` on replacement of expired
state, `session.destroyed` only after successful retirement, and
`session.persist.failed` for persistence failures. A failed destroy retirement
emits the failure with operation `retire`, not the destroyed event. Payloads
contain no session IDs, data, or storage error.

Destroyed/failure event paths use the registered route pattern without its
method prefix, never expanded path parameters or queries. Host prefixes remain;
unmatched patterns produce an empty path. Keep secrets out of route literals.
Contexts still retain caller values, and callback diagnostics remain unredacted;
event payload limits do not sanitize either. See
[HTTP events](../events/built-in-events.md#http) for payloads.

For all fields, lifecycle operations, and settings, see the
[`session` API reference](https://pkg.go.dev/arkoren.dev/core/http/session).
