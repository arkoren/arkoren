---
title: Signed Links
description: Issue expiring application URLs and enforce verification, single use, and state bindings without confusing signatures with authorization.
order: 30
summary:
  - Issue expiring signed URLs and separate tamper resistance from resource authorization.
  - Enforce single use through cache-backed consumption and invalidate links with stable server-side bindings.
  - Inspect and rotate links while preserving backend, clock, recovery, and privacy boundaries.
---

# Signed Links

A password-reset email needs a link that works without a login but rejects an
altered account ID. A **signed link** authenticates its path and query using a
secret key. An expiry limits how long the link is accepted; a cache can record
single use, and a server-side binding can invalidate links when account state
changes.

A valid signature proves that the URL matches what a key holder signed. It does
not identify the person opening it or independently authorize access to the
named resource. Decide what possession of the link permits, authorize issuance,
and recheck application constraints at the action boundary. Anyone who obtains
the URL can present it: keep signed queries out of logs, analytics, and referrers,
and deliver links over HTTPS.

## Create Signers

Provision a separate random 32-byte shared key using
[application key tooling](../keys/overview.md). For example, generate the
application file `link-key.key` with `arkoren key generate shared --out
link-key.key`; keep it outside source control and restrict access. Import
`arkoren.dev/foundation/key` and `arkoren.dev/core/http/link`, then load the key
at startup:

```go
secret, err := key.LoadShared("link-key.key")
```

Handle loading failure before constructing the signer. For email links, give it
your configured public origin rather than deriving that origin from an untrusted
request host:

```go
signer, err := link.New(secret, func(links *link.Builder) {
	links.Origin("https://app.example.com")
})
```

Handle construction errors before issuing links or installing middleware. A zero
key returns `ErrInvalidSecret`. Origins must be HTTP(S) scheme/host origins,
without credentials, query, fragment, or an application path; invalid values
return `ErrInvalidOrigin`. Use HTTPS in production.

Without `Origin`, a signer accepts root-relative paths and returns relative URLs.
With an origin, it completes root-relative targets and accepts absolute URLs only
for that origin. Fragments, credentials, opaque URLs, and unsuitable targets
return `ErrInvalidURL`. A configured origin scopes link reconstruction; it is
not a substitute for your server's host-routing policy.

Construct once and share the signer. It detaches owned key material and
query-name containers, so retained drafts cannot reconfigure it. Cache/event
services remain shared and application-owned; the signer does not start or stop
them. Request-binding functions and captures must be safe for concurrent use.

## Issue Links

After the application authorizes a reset request, build the target from a typed
account reference. With `net/url` imported, avoid concatenating raw query input:

```go
type ResetTarget struct {
	UserID string
}

target := ResetTarget{UserID: "user-42"}
query := url.Values{}
query.Set("user", target.UserID)
path := "/reset?" + query.Encode()
```

The target identifies the account selected by your application, not arbitrary
request fields that the signing service trusts automatically. Use `Temporary`
with `time` imported to issue a short-lived link:

```go
signed, err := signer.Temporary(path, 15*time.Minute)
```

The result is an absolute URL under `https://app.example.com` with `expires` and
`signature` query parameters. The signature covers the path, ordinary query
values including `user`, and expiry; changing protected values invalidates it.
Signing replaces pre-existing reserved parameters. It does not encrypt query
values, so never put passwords or other secrets in them.

Temporary lifetimes must be positive; zero/negative returns `ErrInvalidExpiry`,
not a default. Expiry uses Unix-second precision and the verifier's local clock;
equality counts as expired. Synchronize verifier clocks rather than treating the
TTL as a cross-server timing guarantee. `Sign` creates permanent links, which
cannot be consumed and have no automatic expiry.

If an email system appends tracking parameters, an alternative signer can exclude
specific names:

```go
signer, err := link.New(secret, func(links *link.Builder) {
	links.Origin("https://app.example.com")
	links.IgnoreQuery("utm_source", "utm_medium")
})
```

Anyone can add, change, or remove ignored values without invalidating the link.
Never derive account selection, redirect destinations, authorization, or bindings
from them. Empty names and reserved `signature`/`expires` names return
`ErrInvalidIgnoreQuery`. Signing an arbitrary redirect destination does not make
that destination safe; validate allowed destinations before issuance and use.

## Protect Routes

Use `link.Middleware` on a display-only GET. With an existing router `routes`
and your application-owned `showResetForm` handler:

```go
routes.With(link.Middleware(signer)).Get("/reset", showResetForm)
```

The handler runs only after signature and expiry checks. Middleware returns
`403 Invalid Link` for invalid authentication and `403 Link Expired` for
authentic expired links. With a configured cache, it also returns
`403 Link Already Used` for a consumed temporary link. Cache-read failures pass
to application error handling; they are not proof that the link was forged.

The signature covers the URL, not the HTTP method. Register explicit methods,
not `Any`. If GET and POST intentionally share a path, GET must only display the
form; POST must independently verify and consume before changing state. This
also keeps email scanners and link previews from spending a reset link.

Verification uses the path as the router sees it; stripping a prefix can break
the signed path. Request verification reconstructs absolute links using the
configured origin, not forwarded host headers. Preserve the signed path and
query through proxy routing and form submission.

Outside middleware, `signer.Verify(signed)` checks authenticity and expiry only.
Use `errors.Is` to classify `ErrInvalidURL`, `ErrInvalidSignature`, or
`ErrExpired`. Expiry is reported only after authentication. `Verify` neither
checks consumption nor reserves a use, even when a cache is configured.

## Consume Links

Single use requires a cache shared by every server accepting the same links.
Choose [cache storage](../cache/drivers.md) that retains markers for their entire
lifetime; early eviction, data loss, or an in-memory restart lets spent links
work again. Construct this alternative signer with the existing `*cache.Cache`
named `store`:

```go
signer, err := link.New(secret, func(links *link.Builder) {
	links.Origin("https://app.example.com")
	links.Cache(store)
})
```

Middleware only observes consumption. Spend the link in the POST handler after
decoding and validating the proposed password and applying your application's
reset policy, but before committing the password change. Register that
application-owned handler for POST explicitly:

```go
routes.Post("/reset", resetPassword)
```

Inside `resetPassword`, with incoming request `r`, keep all consumption failures
off the action path:

```go
err := signer.ConsumeRequest(r)
if errors.Is(err, link.ErrConsumed) {
	return link.ErrLinkUsed
}
if err != nil {
	return err
}
```

Import `errors` for classification. Only after this succeeds may your
application-owned password update run. The form must post to the URL carrying
the original signed query; `ConsumeRequest` reconstructs it without manual URL
assembly. Use `Consume(ctx, signed)` outside HTTP, or `ConsumeURL` with a parsed
URL.

Consumption verifies the signature, binding, and expiry, then atomically adds a
cache marker. Confirmed concurrent calls sharing that backend have one winner;
the others return `ErrConsumed`. A marker lives until signed expiry plus one
minute for clock differences. That margin retains markers longer; it does not
extend the link's validity or guarantee arbitrary clock-skew tolerance.

> [!WARNING]
> A cache error can occur after a marker was committed but its confirmation was
> lost. Do not perform the action after any consumption error, and do not assume
> a retry can succeed. Confirm application state through your recovery flow or
> issue a new link.

Consumption is not a transaction with the password update. If the update fails
after successful consumption, the link stays spent. If you need an atomic
consume-and-update guarantee, enforce that boundary in application-owned durable
storage; the cache marker does not provide it.

Only temporary links can be spent; authenticated permanent links return
`ErrNotTemporary`. Missing cache configuration returns `ErrNoCache` before
verification of a parsed target. `Consume` first parses its raw URL, so an
unparsable value returns `ErrInvalidURL` even without a cache; `ConsumeURL` and
`ConsumeRequest` check cache configuration before validating their target.

## Bind State

A still-unexpired link might need to stop working after a password change.
**Binding** adds current server-side bytes to the authenticated input without
putting them in the URL. Load the account identified by `target.UserID` as `user`;
its `PasswordHash` is a persisted `[]byte`. Issue a bound variant:

```go
signed, err := signer.Temporary(path, 15*time.Minute, link.Bind(user.PasswordHash))
```

Use the same binding policy for every verification, consumption, and inspection.
Read the current persisted bytes each time, not a stale issuance snapshot:

```go
err := signer.Verify(signed, link.Bind(user.PasswordHash))
```

This succeeds while the stored bytes match; after they change, it returns
`ErrInvalidSignature`. A wrong, missing, or unexpected binding has the same
error. An empty binding is distinct from no binding. `Bind` copies its input;
only a digest participates in the signature.

Choose stable bytes: a stored hash or a dedicated reset-version value, not a
freshly salted recomputation or an inconsistently formatted timestamp. Every
verifier must read the current state from an appropriate consistency boundary.
Replica lag or cached old values can delay rejection; reverting to old bytes
can make old links verify again. Binding is not an atomic lock against a state
change that races with the subsequent action.

For bound GET verification, configure a lookup. Here `users.Find(ctx, id)` is
your application-owned loader returning that account and an error:

```go
binding := func(policy *link.MiddlewareBuilder) {
	policy.BindRequest(func(r *http.Request) ([]byte, error) {
		user, err := users.Find(r.Context(), r.URL.Query().Get("user"))
		if err != nil {
			return nil, err
		}
		return user.PasswordHash, nil
	})
}
```

Import `net/http` for the callback. It runs **before** authentication: use the
query only for a bounded lookup, never as proof of permission, and perform no
state change there. Lookup errors reject with `403 Invalid Link`. The returned
problem retains the cause for application error handling; response detail and
rejection events do not expose it. Redact causes before diagnostic logging.

Install the bound display route instead of the earlier unbound one:

```go
routes.With(link.Middleware(signer, binding)).Get("/reset", showResetForm)
```

This does not configure POST automatically. In the POST handler, load the current
account from the signed query, fail closed on lookup errors, and replace the
earlier unbound consumption call with:

```go
err := signer.ConsumeRequest(r, link.Bind(user.PasswordHash))
```

Keep the same explicit error checks before the password update. Enforce any
required state-version condition in the application's update as well; neither
binding nor single-use markers authorize a different resource or transaction.

## Inspect Links

For a recovery page, inspect an authentic link without spending it. With a
context `ctx` and current bound account:

```go
details, err := signer.Inspect(ctx, signed, link.Bind(user.PasswordHash))
```

On success, `details.Expired` can be true: expiry is descriptive, not an
inspection error. `Previous` identifies a previous signing key; `Consumed`
reports a known cache marker. False does not guarantee usability: it is always
false without a cache or for permanent links, and another request can consume
immediately after inspection. All inspection errors return zero details,
including authentication failures and cache observation errors.

`details.URL` removes `signature` and `expires` but retains application paths,
query values, and ignored tracking values. It is not safe diagnostic text. If
your recovery policy permits reissuance, pass it to `Temporary` with the current
binding; inspection alone is not permission to extend a reset indefinitely.

## Rotate Keys

To accept existing links during rotation, load `current` and `previous` as
separate `key.Shared` values, then use an alternative construction:

```go
signer, err := link.New(current, func(links *link.Builder) {
	links.Origin("https://app.example.com")
	links.Previous(previous)
})
```

Retain any required cache, binding, or ignored-query policy in the replacement
configuration. New links use the current key; old keys verify only. Coordinate
all issuers/verifiers before removing a previous key. Removing it invalidates
its remaining links, including permanent links, which never expire naturally.
If a key leaks, continuing to accept it also accepts forged links made with it;
choose invalidation over a graceful overlap when that risk requires it.

## Observe Outcomes

Configure `Events(events)` on the signer with your application's event bus if
you need outcome counts. Confirmed consumption publishes `link.consumed`;
failures, including uncertain writes and repeat use, publish nothing. Middleware
publishes `link.rejected` for invalid authentication, not authentic expiry or
already-used outcomes. `Verify` and `Inspect` publish neither event.

Payloads omit paths, queries, bindings, and diagnostic errors. They carry
`Method`, which is empty for non-request consumption. Do not replace removed
`Path`/`Err` field reads with full request URLs or causes. Context values remain
available and custom HTTP methods are client-controlled, so select safe
attributes explicitly. HTTP problem `instance` can still contain a request
path; event privacy does not scrub it. See
[signed-link events](../events/http-events.md#signed-links).

`link.Config` is a printable diagnostic record, not an input to `New`. Its
formatting redacts previous keys and omits service pointers, but displays origin
and ignored query names. Keep credentials out of those settings; this does not
sanitize arbitrary signers, URLs, returned errors, or inspection results.

For URL variants, inspection fields, and error contracts, see the
[`link` API reference](https://pkg.go.dev/arkoren.dev/core/http/link).
