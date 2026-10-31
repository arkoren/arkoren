---
title: Secure Links
nav: Signed Links
description: Create links that cannot be forged and can expire, be used once, or be revoked, for email verification, password reset, and downloads.
order: 30
---

# Secure Links

## Introduction

Some links must work without a login but must not be forgeable: the
verification link in a welcome email, a download link for a private file, an
unsubscribe link. A **secure link** solves this. It is an ordinary URL with a
signature added as a query parameter, computed from the rest of the URL and a
secret only your service knows. If anyone changes a signed part of the URL,
the signature no longer matches and the link is rejected.

The `link` package creates secure links and controls how long they stay valid:
forever, until an expiry, until first use, or until the state they were issued
for changes. Its middleware verifies them, so the handler behind a protected
route only runs for authentic links.

Treat signed URLs as bearer credentials: anyone who obtains one can use it.
Keep query strings out of logs and avoid exposing links through referrers.

Focused construction and signing snippets keep error results visible and assume
success. Handle those errors in your application; verification and consumption
checks remain explicit before protected actions.

## Creating a signer

A **signer** holds signing keys and settings. Create one at startup with a
cryptographically random secret of exactly 32 bytes, read from your configuration. Import
`arkoren.dev/core/http/link` and `arkoren.dev/foundation/key`. Provision a separate
secret with `arkoren key generate shared --out link-key.key`, then load it as a
`key.Shared`. Handle loading and construction errors
before issuing links or installing middleware:

```go
secret, err := key.LoadShared("link-key.key")
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.Origin("https://app.example.com")
})
```

With an origin, the signer produces absolute URLs that you can put in emails.
Without one, it signs relative paths only, which is enough for links used
inside your own pages. Construction detaches owned key material and query-name
containers, so retaining a builder does not let you change a published signer.
Supplied cache and event services remain shared, not cloned or lifecycle-managed
by the signer. Middleware binding functions and their captures are shared too;
use dependencies that are safe for concurrent requests.

> [!WARNING]
> The secret is what makes links unforgeable. Keep it out of your source code
> and rotate it if it leaks, as described [below](#rotating-the-secret).

When logging configuration, `link.Config` is a diagnostic record, not an input
to `link.New`. Its `fmt` and `slog` views replace previous keys with a redaction
marker and omit cache/event service pointers. They still display `Origin` and
ignored query names; keep credentials out of those values. This is not a
general-purpose sanitizer for signers, URLs, or returned errors.

## Signing a link

A verification email needs a link that works when the recipient opens it, but
not one that stays valid forever. With `time` imported, use `Temporary` to give
that link an expiry:

```go
url, err := signer.Temporary("/verify?user=42", 24*time.Hour)
```

The result is the URL with two query parameters added: `expires`, the
expiry time, and `signature`. The signature authenticates the expiry, path,
and other query parameters; the `signature` field itself is excluded from the
signed text. Altering either field invalidates the link. This includes `user=42`, so
a recipient cannot change the link to verify another user.

For a link that should never expire, use `Sign` instead.

## Protecting a route

Wrap the routes that accept secure links with `link.Middleware`. It verifies
the signature and the expiry before your handler runs. Here `r` is your existing
router, `request` is `arkoren.dev/core/http/request`, and `verifyEmail` is your
application's verification function. Use `net/http` for the handler types:

```go
r.With(link.Middleware(signer)).Get("/verify", func(w http.ResponseWriter, r *http.Request) error {
	id := request.QueryOr(r, "user", "") // trusted: it was part of the signature
	return verifyEmail(r.Context(), id)
})
```

An expired link is rejected with `403 Link Expired`, and any other invalid
link with `403 Invalid Link`. The messages are deliberately generic, so they
reveal nothing about why verification failed.
With a configured cache, an already-consumed link gets `403 Link Already Used`.
If the cache check fails, middleware returns the cache error for application
error handling rather than classifying the link as forged.

> [!NOTE]
> The signature covers the URL, not the HTTP method. Register protected routes for
> a single method with `Get` or `Post`, never with `Any`, so a link sent for a
> `GET` cannot be replayed as a `POST` unless you also register that method. If
> GET and POST intentionally share a signed path, GET must only display the form;
> POST must verify and consume the link before changing state.

## Verifying in code

To verify a link outside a route, for example in a background job, call
`Verify`. It returns an error that tells an expired link apart from an invalid
one. Import `errors` for classification:

```go
err := signer.Verify(url)
switch {
case errors.Is(err, link.ErrInvalidURL):
	// the URL cannot be parsed or does not fit this signer's mode
case errors.Is(err, link.ErrExpired):
	// authentic, but too old
case err != nil:
	// authentication or binding failed
}
```

Parsing and signer-mode failures return `ErrInvalidURL`. Malformed signature
data, tampering, and missing or wrong bindings return `ErrInvalidSignature`.
Expiry is reported only after authentication succeeds. `Verify` does not check
whether a link has already been consumed; use `Consume` before a one-time action.

## Single-use links

A password reset link should work once. Give the signer a cache, then spend
the link in the handler that applies the change, not in the one that shows
the form. Use your existing shared `*cache.Cache` named `store` from the
[cache guide](../cache/overview.md), plus `arkoren.dev/foundation/problem` for
the failure callbacks. `showResetForm` and `resetPassword` are application-owned
handlers; neither is supplied by `link`:

> [!WARNING]
> Do not apply the action after any consumption error. A cache error can occur
> after the consumption marker was committed but its confirmation was lost. The
> link may already be spent; do not assume an automatic retry can succeed.
> Confirm application state or issue a new link through your recovery flow.

```go
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.Cache(store)
})

r.With(link.Middleware(signer)).Get("/reset", showResetForm)
r.Post("/reset", func(w http.ResponseWriter, r *http.Request) error {
	err := signer.ConsumeRequest(r)
	if errors.Is(err, link.ErrConsumed) {
		return link.ErrLinkUsed.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}
	if err != nil {
		return link.ErrLinkRejected.Derive(func(details *problem.Builder) {
			details.Error(err)
		})
	}
	return resetPassword(r)
})
```

The form posts back to a URL that carries the same signed query, so
`ConsumeRequest` needs no URL rebuilding. The example keeps every failure off
the action path and attaches its cause to the application-selected response.
Consumption verifies the link as `Verify` does, then records it with an atomic
cache add. If concurrent calls are confirmed by the cache, one succeeds and the
others get `ErrConsumed`. With a cache on the signer,
`Middleware` also answers `403 Link Already Used` for a spent link, so the
form page does not render for a dead link.

Only temporary links can be spent. After authentication, `ErrNotTemporary`
rejects permanent links, whose markers would have to be kept forever. The record
stays until the link would have expired, plus one minute for clock differences
between servers.

Error precedence differs by input form: `Consume(ctx, rawURL)` parses the URL
first, so an unparsable value returns `ErrInvalidURL` even without a cache.
For a parsed value, missing cache configuration returns `ErrNoCache` before
verification. `ConsumeURL` and `ConsumeRequest` check cache configuration before
validating the target. Cache failures preserve their wrapped causes, but do not
prove that no marker was written.

Spend the link before you change anything. If `resetPassword` fails after
`ConsumeRequest` succeeded, the link stays spent and the user must request a
new one. Consumption is not a transaction with the password change: it prevents
another consumption of an otherwise valid link only while the cache retains
the record.

> [!WARNING]
> The cache decides how reliable single use is. If it evicts or loses entries
> early, for example an in-memory cache after a restart, a spent link works
> again. Use a cache driver that keeps entries for their full lifetime, shared
> by every server.

With `Events(app.Events())` in the signer's construction callback, every successful
`Consume`, `ConsumeURL`, or `ConsumeRequest` publishes `link.consumed` with the
request `Method` after the use is confirmed. `Method` is empty for `Consume` and
`ConsumeURL`, which have no request. Failed calls, including uncertain cache
writes and `ErrConsumed`, publish nothing; neither do `Verify` and `Inspect`.
The same option makes middleware publish `link.rejected` for invalid links;
authentic expired or already-consumed links publish no rejection event.

Both payloads omit paths, queries, and bindings. `link.RejectedEvent.Path`,
`link.RejectedEvent.Err`, and `link.ConsumedEvent.Path` have been removed. Remove
those field reads from existing listeners; do not replace them with unfiltered
request URLs or causes. An event counts an observed outcome, not which account
or reset link was involved. See [signed-link events](../events/http-events.md#signed-links).

These limits apply to payload fields, not every value available to a listener.
Event contexts retain caller values, and custom HTTP methods are client-controlled.
Select safe attributes explicitly. HTTP problems can still include a request
path in `instance`; event privacy does not redact that member.

## Revoking links when state changes

A link stays valid until it expires, even if the account changed after you
sent it. To end a link early without keeping a list of revoked links, **bind**
it to server-side state. The signature then also covers that value, and the
link stops verifying as soon as the value changes:

```go
signed, err := signer.Temporary("/reset?user=42", time.Hour, link.Bind(user.PasswordHash))
err = signer.Verify(signed, link.Bind(user.PasswordHash)) // nil
// After the password changes, the same call returns link.ErrInvalidSignature.
```

Pass the same `Bind` to every call for that link: `Verify`, `Consume`, and
`Inspect`. A wrong value, no value, or a value on a link that was signed
without one all return `ErrInvalidSignature`, so the error does not reveal
which part failed. The value itself never appears in the link. Links signed
without `Bind` keep verifying as before.

For a protected route, derive the value from the request. Here `users.Find` is
your application's lookup and `PasswordHash` is the current `[]byte` hash.
The query is still untrusted during the lookup: perform no state change before
verification succeeds:

```go
bind := func(policy *link.MiddlewareBuilder) {
	policy.BindRequest(func(r *http.Request) ([]byte, error) {
		user, err := users.Find(r.Context(), r.URL.Query().Get("user"))
		if err != nil {
			return nil, err
		}
		return user.PasswordHash, nil
	})
}
r.With(link.Middleware(signer, bind)).Get("/reset", showResetForm)
```

> [!WARNING]
> The function runs before the signature is checked, so the request is still
> untrusted. Use `user` only to find the record, and do not act on it. If the
> function returns an error, for example because the user does not exist, the
> link is rejected with `403 Invalid Link`.

The returned rejection problem retains the binding failure for `errors.Is` and
`errors.As`; the `Rejected` payload and HTTP error detail do not contain it.
Inspect the returned chain in application error handling, not through the removed
event `Err` field. Redact before logging: a lookup error can contain credentials
or a full URL. The HTTP problem's normal `instance` member may still contain the
request path.

This GET-only variant does not change the earlier POST handler automatically.
For a bound reset flow, load the current hash in the POST handler, fail closed
if the lookup fails, and pass `link.Bind(user.PasswordHash)` to `ConsumeRequest`
there too. Preserve the signed query when submitting the form.

## Inspecting a link

`Inspect` tells you about a link without acting on it. It checks the
signature (and binding) like `Verify`, but an expired link is not an error:

```go
details, err := signer.Inspect(ctx, url)
switch {
case errors.Is(err, link.ErrInvalidURL):
	// the URL cannot be parsed or does not fit this signer's mode
case errors.Is(err, link.ErrInvalidSignature):
	// unauthenticated: no link details are returned
case err != nil:
	// a cache observation failed: no details, not proof of forgery
case details.Expired:
	// offer to send a new link for details.URL
case details.Previous:
	// signed with a previous secret: re-issue before you drop it
}
```

`details.URL` is the link without its `signature` and `expires` parameters, so
you can pass it to `Temporary` to issue a new one. It still contains application
paths and query values, including ignored parameters; it is not a generally
sanitized URL. Do not log the whole inspection as safe diagnostics.

Every inspection error returns zero details. `details.Consumed` is true for a
known spent link and is always false without a cache or for permanent links.
False does not guarantee usability. Another request can consume the link after
inspection; call `Consume` at the action boundary rather than treating this
observation as a reservation.

## Ignoring tracking parameters

Email tools often append parameters such as `utm_source` to links, which would
break the signature. List them with `link.Builder.IgnoreQuery` so they are left out of
the signature:

```go
signer, err := link.New(secret, func(signer *link.Builder) {
	signer.IgnoreQuery("utm_source", "utm_medium")
})
```

Ignored parameters are not protected: anyone can add or change them. Never read
anything security-relevant from them.

## Rotating the secret

To change the secret without breaking links that are already in people's
inboxes, load `current` and `previous` as separate `key.Shared` values with
`key.LoadShared` or `key.ParseShared`. Keep the old value for verification only:

```go
signer, err := link.New(current, func(signer *link.Builder) {
	signer.Previous(previous)
})
```

New links are signed with the new secret, and links signed with either secret
are accepted. Once every link signed with the old secret has expired, remove
it.

Permanent links never expire; removing their signing secret invalidates them.

For every option and error, see the [`link` API reference](https://pkg.go.dev/arkoren.dev/core/http/link).
