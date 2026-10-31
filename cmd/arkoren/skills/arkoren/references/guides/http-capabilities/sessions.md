---
title: Sessions
description: Keep per-visitor state across requests in a cache-backed session, regenerate the ID on login, and understand when changes are saved.
order: 10
---

# Sessions

## Introduction

HTTP is stateless: each request arrives on its own. A **session** lets a
service remember something about a visitor between requests, such as who they
are after they log in or what is in their cart. The browser holds only a random
session ID in a cookie, and the data lives on the server.

Arkoren stores session data in the [cache](../cache/overview.md), so sessions
need no driver of their own. In development the memory cache is enough; in
production, use a shared cache such as Redis so every replica sees the same
sessions.

## Enabling sessions

Sessions are middleware. With an existing `store` (`*cache.Cache`) and router
`r`, add them before the routes that use session state. Follow the
[cache setup](../cache/overview.md) for driver construction and lifecycle:

Import `arkoren.dev/core/http/session`. Handler examples also use
`arkoren.dev/core/http/request` and `errors`; duration settings use `time`.

```go
r.Use(session.Middleware(store))
```

This uses HTTPS-only cookies. For local plain HTTP, set
`AllowInsecure(true)` on the session builder as described under
[the session cookie](#the-session-cookie),
never in production. Register [CSRF protection](../browser-security/csrf-protection.md)
before authentication or handlers that act on the signed-in user.

The middleware loads the session at the start of each request, creating a new
one for a first-time visitor. It saves changed state before committing response
headers. A fresh session starts unchanged: reading it alone creates neither a
cache record nor a cookie unless middleware renewal changes its expiry or ID.

## Reading and writing values

When separate handlers write and read the same value, share a **field**: an
immutable declaration of its literal storage name and Go type. Declare it next
to the feature that owns the value. A session can hold fields of different types;
declaring a field does not allocate storage or register a schema:

```go
var CartID = session.NewField[string]("cart_id")
```

In a handler whose incoming request is `r`, get the session with
`request.MustSession`. With your existing string cart ID `cart`, store it and
let the middleware save it before the response is committed:

```go
s := request.MustSession(r)
if err := s.Put(CartID, cart); err != nil {
	return err
}
```

In a later handler, read the cart with that same definition:

```go
s := request.MustSession(r)
savedCart, err := s.Get(CartID)
if errors.Is(err, session.ErrKeyNotFound) {
	// nothing stored yet
}
```

On success, `savedCart` is a string; `Get` infers the type from `CartID`. A
missing field returns `session.ErrKeyNotFound`. To remove the cart from an
existing session `s`, check `Delete` before continuing:

```go
if err := s.Delete(CartID); err != nil {
	return err
}
```

Deleting an absent field succeeds and still marks the session changed.

A successful `Put` or `Delete` changes this request's in-memory session. It does
not confirm a cache write, a response cookie, or durable storage. The middleware
owns [saving those changes](#when-changes-are-saved).

Standalone `NewField` names are literal: no prefix, separator, trimming, or normalization is added.
Independently declared fields with the same name address the same entry even
when their types differ; typed declarations do not enforce a storage schema.
`Name()` reveals the name, while ordinary formatting and structured logging
redact it. `Err()` reports `session.ErrInvalidField` for an empty name or a
zero-value field. `Get`, `Put`, and `Delete` reject invalid fields before reading
or changing state. Fields have no TTL or configuration callback; expiry belongs
to the whole session.

`MustSession` panics when the session middleware is missing, which is a wiring
mistake you see on the first request. Use `request.Session`, which returns a
second `ok` result, in code that also runs without sessions.

### Runtime values and persisted JSON

Before persistence, values must already match the field's Go type. `Get` does
not convert an in-memory map or a differently typed number through JSON; it
returns `session.ErrInvalidValueType` instead. This also applies to
`jsontext.Value` (`encoding/json/jsontext`) supplied through `New` or `Put`:
those bytes remain runtime Go data, not evidence that a field was persisted.
For example, a runtime `jsontext.Value` containing `"dark"` cannot be read as a
`Field[string]`; an `any` field returns the original `jsontext.Value`.

Only fields actually loaded from a stored session record permit typed JSON
decoding. `Get` decodes those fields into the requested type, preserving integer
precision; incompatible JSON returns `ErrInvalidValueType`. Replacing a loaded
field with `Put` restores runtime type checking until the replacement is saved
and loaded again. Use values that round-trip through JSON, and share field
definitions between producers and consumers.

A present nil interface is different from a missing field. For an in-memory
entry containing a nil interface, `Get` with an interface field such as
`Field[any]` returns `nil, nil`. Reading that same entry with a concrete field
returns `ErrInvalidValueType`; an absent entry returns `ErrKeyNotFound`.
A typed nil pointer retains its concrete Go pointer type rather than becoming
a nil interface.

### Mutable values and direct construction

`Put` retains the supplied value rather than copying it. The session mutex
protects its fields and outer storage map, not arbitrary nested maps, slices, or
pointed-to objects. Do not mutate shared values concurrently with session access
or persistence. After changing a shared value, call `Put` again to mark the
session changed; mutation through an alias is not tracked automatically.

For a test or other code that needs standalone in-memory state, use `session.New`
rather than a zero `Session`. With the `CartID` field declared above:

```go
initial := map[string]any{"cart_id": "cart-42"}
current := session.New(time.Now().Add(time.Hour), initial)
initial["cart_id"] = "cart-99"
savedCart, err := current.Get(CartID) // "cart-42" on success
```

`New` snapshots the outer map, so later caller-map edits and session writes are
independent. Do not mutate the input map while the constructor copies it. A nil
input map creates empty, writable storage. Nested values remain shared under
the same synchronization and change-tracking rules as `Put`.

`New` marks the new session changed, but neither attaches it to a request nor
saves it. Keep the middleware path for normal HTTP use. `All` also copies only
the outer map; unmodified loaded fields appear there as borrowed
`jsontext.Value` slices. Passing an `All` snapshot to `New` makes those values
ordinary runtime input again; it does not preserve their loaded provenance.

## Sharing a field prefix

Keep standalone fields for fixed state. If a feature owns several fields of one
Go type, use a namespace to avoid repeating its prefix:

```go
cartFields := session.NewNamespace[string]("cart")
coupon := cartFields.Field("coupon") // coupon.Name() == "cart:coupon"
if err := s.Put(coupon, "WELCOME"); err != nil {
	return err
}
```

Here `s` is the request's session from above. The result is an ordinary
`session.Field[string]`, shared by `Get`, `Put`, and `Delete`. This changes no
session expiry policy and adds no field TTL or bulk operations.

`session.Namespace[T]` names fields **inside** one session record. In contrast,
`session.Builder.Namespace(cache.Namespace[jsontext.Value])` names whole cache
records for the middleware; it does not prefix field names. Neither mechanism
changes context handles or the fixed `arkoren:auth:user` field used by
authentication. Do not reuse that reserved name for application data.

The default separator is `":"`. Use a nongeneric callback when existing field
names require another convention:

```go
legacy := cartFields.Derive(func(namespace *session.NamespaceBuilder) {
	namespace.Separator(".")
})
shipping := legacy.Subnamespace("shipping")
city := shipping.Field("city") // city.Name() == "cart.shipping.city"
```

`Derive` preserves the original. `Subnamespace` joins with the parent's separator
before its callbacks change future joins. `Name`, `Separator`, and `Err` inspect
the namespace; ordinary formatting and structured logging redact its name.
Empty separators concatenate names. Empty namespace or child
names and zero namespaces report `session.ErrInvalidField`; the first error
survives derivation and reaches fields. Callbacks run once in order even for
invalid definitions; nil entries panic, and retained drafts cannot change
published definitions.

Names have no escaping or normalization: `a:b` plus `c` and `a` plus `b:c` both
produce `a:b:c`. Validate or encode untrusted components before joining them;
convert numeric IDs explicitly with standard-library `strconv`. Different
namespaces and types do not isolate entries or grant authorization. Renaming a
field does not move existing session data; retain its exact physical name when
adopting namespaces unless you intentionally migrate that data.

## Logging in and out

For user authentication, share one identity definition between middleware and
handlers. Import `arkoren.dev/core/http/auth`; `users.Load` is your application's
typed user loader from [Session Login](../authentication/session-login.md).
After the session and CSRF middleware shown above, register authentication:

```go
identity := auth.NewSessionIdentity(users.Load)
r.Use(auth.Middleware(identity.Authenticator()))
```

After checking credentials, use that same identity in the login handler before
writing response headers:

```go
if err := identity.Login(request.MustSession(r), user.ID); err != nil {
	return err
}
```

Login writes the identifier and rotates the session ID only if the write succeeds.
Rotation prevents **session fixation**, where an attacker plants a known session
ID in the browser before login. For privilege changes outside login, call
`Session.Regenerate` explicitly. To log out while retaining unrelated fields:

```go
if err := identity.Logout(request.MustSession(r)); err != nil {
	return err
}
```

Logout deletes even a stale identifier before rotating. To remove all data
instead, destroy the session before writing response headers:

```go
request.MustSession(r).Destroy()
```

These methods perform no storage I/O themselves. Before a non-5xx response, the
middleware saves a regenerated session and retires its old ID, or retires a
destroyed session and expires its cookie. Retirement replaces the old record
with a marker, rather than physically deleting it. If storage fails, the old ID
can remain valid; monitor [storage errors](#handling-storage-errors).

[Authentication](../authentication/overview.md) builds on sessions for
login and covers this in detail.

## When changes are saved

The middleware saves the session just before the response status is written,
so only changes made before that first final status are included. Modify session
state before writing a response or starting a stream. Two exceptions keep
sessions consistent:

- Changes are not saved when that status is `5xx`. This does not roll back
  database writes or other application effects, and a later failure cannot undo
  session state already saved for a successful response.
- A destroyed or regenerated session cannot be brought back by a slower
  request that still holds the old one. The old record is replaced by a marker
  that makes later saves to it fail quietly.

For a loaded session, the middleware re-reads the original record, then uses an
[atomic guarded write](../cache/overview.md#writing-only-while-an-observed-value-still-matches)
to compare those exact bytes and save the current ID together. A missing or
retired original, or one changed after that re-read, rejects the save without a
cookie. This also protects a proposed regenerated ID, not only writes back to
the original key. Rejection is not a storage error and does not retry the stale
edit.

The check-and-save operation and marker write still take a per-ID
[cache lease](../cache/locks.md) for ten seconds to reduce conflicts. Expiring
that lease does not bypass the atomic guard, even when `PersistenceTimeout`
permits storage work longer than ten seconds. Lease-acquisition and storage
failures are reported through the error handler and persistence events; a
failed retirement can leave the old ID valid.

Primary persistence ignores the captured request context's cancellation and
deadline, retains its values, and applies `Builder.PersistenceTimeout`, which
defaults to five seconds. Each internal lease-release attempt gets a separate
ten-second cleanup budget, independent of request or persistence cancellation,
while retaining those context values. Release is best effort: if it fails, the
lease's own expiry is the fallback. Cleanup can therefore delay response headers
beyond `PersistenceTimeout`.

These deadlines bound backend work only when the cache driver honors its
context. They do not bound your error handler or event listeners, which run
synchronously; keep those callbacks fast. A backend that ignores its context or
an application callback that blocks can still delay the response indefinitely.

Concurrent requests from the same visitor otherwise follow
last-write-wins while lease coordination serializes them: the last save decides
its content. If writes overlap after a lease expires, a changed guard drops
the conflicting save instead. Retirement does not revoke a different ID that
was committed before retirement, or cancel effects already performed by a
handler.

> [!WARNING]
> Upgrade every session writer and drain requests from older versions before
> relying on guarded retirement. An older writer's unconditional write can
> bypass the guard. Guarded retirement itself changes no stored JSON, key names,
> cookie names, or configured TTL settings, but restoring an earlier live record
> can authorize stale writes again.
> Custom cache drivers must implement atomic `CompareAndPut`; do not emulate it
> with separate reads and writes or fall back to `Put`.

To change storage names, supply a [cache namespace](../cache/overview.md#sharing-a-prefix-and-ttl-with-namespaces).
Pass `Builder.Namespace` a
`cache.Namespace[jsontext.Value]` (`jsontext` is `encoding/json/jsontext`). The
default name is `arkoren:session` with separator `":"`, preserving existing
session keys. Names and separators are literal; choose names that cannot collide
with other entries. Session idle and maximum-lifetime policy determine each
write's TTL, overriding the namespace TTL. A zero or invalid namespace panics
after middleware configuration callbacks run.

## Expiry

A session's idle lifetime defaults to two hours from creation or renewal.
Before committing a response, the middleware renews a live session when strictly
less than 15 minutes remain, not on every request. `Builder.ExpirationDelta`
changes that threshold. Independently of activity, the default absolute maximum
lifetime is 24 hours. Change the lifetime settings on the middleware:

```go
r.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.TTL(time.Hour)
	cookies.MaxLifetime(7 * 24 * time.Hour)
}))
```

A positive `MaxLifetime` caps initial expiry, renewal, and application-selected
expiry from `Session.Extend` at the original `CreatedAt` plus that maximum.
Regeneration does not reset the creation time. The response cookie and cache
record TTL use the capped expiry. Capping an otherwise unused new session does
not itself mark it changed or cause a record or cookie to be written.

The exact expiry instant counts as expired. When saving changed state, if the
handler has reached the absolute deadline, the middleware discards the record
write and any new or refreshed session cookie rather than granting the same
state a fresh lifetime. This rejection is not a storage error. An expired
loaded session starts a fresh session on the next request; that replacement
does not retain the old state. A nonpositive remaining write TTL is discarded,
never passed through as the cache's zero-TTL/no-expiry policy.

Zero restores the defaults: two hours for `TTL`, 24 hours for `MaxLifetime`,
15 minutes for `ExpirationDelta`, and five seconds for `PersistenceTimeout`.
Session TTL zero does not mean forever. A negative `MaxLifetime` disables the
absolute cap; a negative `ExpirationDelta` disables near-expiry renewal. An idle
deadline reached during a handler can still trigger ID regeneration and renewal,
subject to the absolute cap.

`Middleware` validates the completed settings after all construction callbacks
return. A final negative `TTL` or `PersistenceTimeout` panics during construction;
a later callback can replace an earlier invalid value before validation. These
settings apply to the whole session, not individual fields, and session lifetime
overrides the cache namespace's TTL when saving a session record.

## The session cookie

The cookie is always `HttpOnly`, so scripts in the page cannot read it, and
`Secure` by default, so it is only sent over HTTPS. Its default name starts
with `__Host-`, which browsers only accept for secure cookies scoped to the
whole site. `SameSite` is `Lax`, which keeps the cookie off most cross-site
requests.

For local development over plain HTTP, allow an insecure cookie explicitly:

```go
r.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.AllowInsecure(true)
}))
```

Never enable this in production; without HTTPS, a session cookie can be
intercepted. Browsers do not handle `Secure` cookies on `http://localhost`
consistently, so do not rely on a localhost exception.

Sessions that a browser sends with every request make cross-site request
forgery possible. Protect forms and other state-changing requests with the
[CSRF middleware](../browser-security/csrf-protection.md).

## Handling storage errors

An error while saving does not itself change the HTTP response to an error.
By default the error is discarded. Pass an error handler to log it. The callback
intentionally receives the unredacted diagnostic error, including its backend
cause; error text can contain backend names or session keys. Apply your logging
and redaction policy rather than treating it as a privacy-safe event payload.
Here `logger` is your existing `*slog.Logger` writing
to access-controlled diagnostic logs:

```go
session.Middleware(store, func(cookies *session.Builder) {
	cookies.ErrorHandler(func(err error) {
		logger.Error("session save failed", "error", err)
	})
})
```

Loading a session is different: if the cache fails while loading, the request
fails, because the handler cannot know who the visitor is.

A failed retirement write can leave an old ID valid even after logout or
regeneration. Monitor persistence errors and treat the cache as part of your
authentication boundary; successful HTTP completion alone does not prove the
session change persisted.

## Session events

To observe expiry, logout, and persistence failures without exposing session
data in the event payload, connect the middleware to the application's event bus.
Here `events` is the bus obtained from `app.Events()`:

```go
r.Use(session.Middleware(store, func(cookies *session.Builder) {
	cookies.Events(events)
}))
```

It publishes `session.expired` when it replaces an expired session,
`session.destroyed` once a session the handler destroyed has been retired, and
`session.persist.failed` when verification, saving, or retirement fails,
alongside the error handler above. `session.destroyed` fires only after the
marker that retires the old ID is written; if that write fails, the old ID may
still work, and the middleware publishes `session.persist.failed` with the `retire`
operation instead. No payload carries the session ID, its data, or the storage
error.

`DestroyedEvent.Path` and `PersistFailedEvent.Path` contain the registered route
pattern with its method prefix removed, such as `/accounts/{id}/cart`, not an
expanded URL path or query. A host prefix on a host-specific pattern is retained.
Without a matched request pattern, `Path` is empty; it never falls back to
`URL.Path`. Do not put secrets in registered route literals.

These payload limits do not scrub the listener's context: request context values
remain available. They also do not redact the diagnostic errors sent to
`ErrorHandler`. Apply your own privacy controls to context values and callback
logging. [Built-in Events](../events/built-in-events.md#http) describes each
payload.

For every option, see the [`session` API reference](https://pkg.go.dev/arkoren.dev/core/http/session).
