---
title: CSRF Protection
description: Stop other sites from making requests as your signed-in users with header-based checks that need no tokens, and allow trusted origins.
order: 40
---

# CSRF Protection

## Introduction

Browsers attach cookies automatically when the cookie's scope and `SameSite`
rules allow it; the page sending the request need not know their values.
If a person signed in to your service visits a
malicious page, that page can submit a hidden form to your service, and the
browser attaches the session cookie. Your service sees a valid session and
transfers the money, changes the email address, or deletes the account. This
attack is called **cross-site request forgery** (CSRF).

Arkoren's `csrf` middleware checks browser headers to reject unsafe cross-origin
requests. It uses headers that browsers add on their own and that pages cannot
fake: `Sec-Fetch-Site` describes the request's relationship to the destination,
and `Origin` names the origin that made it. Go's `http.CrossOriginProtection`
performs the checks; the middleware adds an Arkoren error response and events.
Unlike token-based CSRF protection, this approach needs no tokens in forms or
JavaScript and stores nothing in the session.

## Enabling protection

Register the middleware before anything that acts on the user, and in
particular before [authentication](../authentication/overview.md). Import
`arkoren.dev/core/http/middleware/csrf`, `arkoren.dev/core/http/session`, and
`arkoren.dev/core/http/auth`. Here `r` is your existing router, `store` is your
configured `*cache.Cache`, and `users.Load` is your application's typed session
user loader from the [session login guide](../authentication/session-login.md):

```go
identity := auth.NewSessionIdentity(users.Load)
r.Use(session.Middleware(store))
r.Use(csrf.Middleware())
r.Use(auth.Middleware(identity.Authenticator()))
```

That is all a typical service needs. Every request that uses a session cookie
must pass through it, so you should register it whenever you sign people in
with sessions.

## How requests are checked

The middleware treats `GET`, `HEAD`, and `OPTIONS` as safe methods and always
lets them pass. Browsers let any page trigger requests such as an image `GET`,
so your handlers for these methods must not change state. A logout or a delete
behind a `GET` route is unprotected.

For every other method, the middleware looks at the headers in this order:

1. If `Sec-Fetch-Site` is present, the request passes when it is `same-origin`,
   meaning it came from your own pages, or `none`, meaning the person typed the
   address or used a bookmark. Any other value means the request came from
   another origin, even a subdomain of your own site, and it is blocked unless
   that origin is [trusted](#trusting-other-origins).
2. Otherwise, if `Origin` is present, the request passes when its host matches
   the request's `Host` header.
3. When both headers are missing, the checker assumes a same-origin or
   non-browser request and lets it pass. Programs such as `curl` and other
   servers commonly send requests without these headers; their absence is not
   proof of the caller's identity or permission.

A blocked request receives a `403` Problem Details response, and your handler
never runs:

```json
{
  "type": "tag:arkoren.dev,2026-09:core:http/middleware/csrf:blocked",
  "title": "Cross-Origin Request Blocked",
  "status": 403,
  "detail": "The request was rejected because its origin or fetch context did not meet security requirements."
}
```

> [!NOTE]
> Clients that send neither header pass this check. Browsers that honor the
> session cookie's `SameSite=Lax` setting, the
> [default](../http-capabilities/sessions.md#the-session-cookie), also withhold it on cross-site
> form posts. Do not assume that old browsers implement either protection.

## Trusting other origins

Sometimes a request from another origin is legitimate: your marketing site at
`www.example.com` posts a sign-up form to `app.example.com`, or a partner's
dashboard calls your API from the browser. List these origins as trusted:

```go
r.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.TrustedOrigins("https://www.example.com")
}))
```

Requests whose `Origin` matches a trusted origin pass. An origin is a scheme, a
host, and an optional port, with no path, and a malformed one makes the
middleware panic when it is created, so mistakes show up at startup.

Trusting an origin only lets its requests through the CSRF check. For a browser
to let pages on that origin read your responses, you also need
[CORS](cors.md). The two are independent: CORS controls who
can read responses, and CSRF protection controls who can trigger actions. CORS
alone does not stop CSRF, because a forged form post does not need to read the
response to do damage.

## Routes that must accept cross-site requests

A few routes receive cross-site requests on purpose. A payment provider posts
webhooks to your service, and Apple's sign-in sends people back with a
cross-site `POST`. Exempt such routes only when they verify requests another
way, such as a webhook signature or the login flow's own cookie and callback
checks. The exemption itself provides no verification.

The simplest approach is to register them outside the protected group. With
`arkoren.dev/core/http/router` imported, `stripeWebhook` is your configured
signature-verifying webhook handler and `saveSettings` is your application handler:

```go
r.Post("/webhooks/stripe", stripeWebhook) // verified by its signature

r.Grouped(func(web *router.Router) {
	web.Use(csrf.Middleware())
	web.Post("/settings", saveSettings)
})
```

When that is not practical, build Go's checker yourself, add bypass patterns
for those routes, and pass it to the middleware. Import `net/http`:

```go
engine := http.NewCrossOriginProtection()
engine.AddInsecureBypassPattern("POST /webhooks/stripe")

r.Use(csrf.Middleware(func(protection *csrf.Builder) {
	protection.Protection(engine)
}))
```

Patterns use the same syntax as `http.ServeMux`. As the name of the method
says, a bypassed route has no CSRF protection, so keep the patterns narrow and
make sure each route verifies its requests another way.

## APIs and tokens

Requests authenticated exclusively with an [API token](../authentication/api-tokens.md)
in the `Authorization` header do not have the automatic-cookie CSRF risk: the
browser does not attach that header on its own, so a malicious page without
the token cannot authenticate a request. An API used only
with tokens does not need CSRF protection. When an API accepts both session
cookies and tokens, keep the middleware: it lets token requests from programs
through, since they carry neither browser header.

If you leave out CSRF protection for JSON endpoints, still register the
[content type middleware](../http-fundamentals/content-type.md). A cross-site
form can only send form encodings and `text/plain`, never JSON, so rejecting
other content types closes the most common forgery route.

## Logging blocked requests

With `csrf.Builder.Events`, every blocked request publishes an
[event](../events/overview.md) with the method, the path, and the `Origin`
header. A steady trickle usually means a misconfigured trusted origin, and a
burst on one route may be an attack worth investigating.

## Testing

A request built with `httptest.NewRequest` carries neither browser header, so
it passes the check like any program would. To test that a route is
protected, send it as a browser would from another site. This test fragment uses
`net/http`, `net/http/httptest`, and `t *testing.T` against the configured router:

```go
req := httptest.NewRequest(http.MethodPost, "/settings", nil)
req.Header.Set("Sec-Fetch-Site", "cross-site")

if res := r.Record(req); res.StatusCode != http.StatusForbidden {
	t.Fatalf("status = %d, want 403", res.StatusCode)
}
```

For every option, see the [`csrf` API reference](https://pkg.go.dev/arkoren.dev/core/http/middleware/csrf).
