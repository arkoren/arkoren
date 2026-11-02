---
title: Overview
description: Choose browser defenses for response handling, cross-origin access, and forged cookie-backed actions.
order: 10
summary:
  - Distinguish response policies, cross-origin response access, and forged cookie-backed actions.
  - Place browser defenses at router or group scope without replacing identity and permission checks.
  - Test browser headers and rejected actions rather than relying on ordinary server requests.
---

# Overview

A visitor can stay signed in to your application while browsing an untrusted
site. That site might frame your account page, try to read your API, or submit
a request with cookies the browser attaches automatically. Browser security
policies address these different interactions; no single policy covers them all.

These defenses do not identify the caller or grant access to a resource. Keep
[authentication](../authentication/overview.md) and
[authorization](../authorization/overview.md) on protected operations, including
requests from trusted browser origins and non-browser clients.

## Choose Defenses

An **origin** is a scheme, host, and optional port. A page at
`https://portal.example.com` has a different origin from an API at
`https://api.example.com`, even though both belong to your organization.
Choose each defense for the interaction it controls:

| Interaction | Defense | Boundary |
| --- | --- | --- |
| Another site embeds your page, or a browser guesses its content type | [Secure headers](secure-headers.md) | Response headers instruct the browser how to handle your content. |
| Your separate browser front end needs to read API responses | [CORS](cors.md) | Permission headers tell browser code which cross-origin responses it may access. |
| Another site submits an action using the visitor's cookies | [CSRF protection](csrf-protection.md) | Request-header checks reject unsafe cross-origin browser requests before application work. |

Start with secure headers. Their defaults forbid framing, disable content
sniffing, and restrict referrer disclosure. The default Content Security Policy
only forbids framing: HTML applications need a resource-loading policy suited to
their pages. Enable HSTS only after establishing a lasting HTTPS deployment.

Add CSRF protection wherever browser cookies authorize actions. Keep `GET`,
`HEAD`, and `OPTIONS` free of state changes because the checker always allows
them. Configure CORS only when a different browser origin needs response access;
same-origin requests do not need CORS permission.

CORS is not a write barrier. A disallowed origin can still reach a handler
without permission headers, and a forged form submission can cause damage
without reading its response. Conversely, a trusted CSRF origin does not gain
permission to read responses. A separate cookie-backed front end can need both
policies, plus authentication and resource authorization.

## Place Policies

Apply shared response headers at router scope so they also cover unmatched
requests. With a fresh `routes` router and
`arkoren.dev/core/http/middleware/secureheaders` imported:

```go
routes.Use(secureheaders.Middleware())
```

For account settings authorized by cookies, apply CSRF protection before the
group's authentication and handlers. Import `arkoren.dev/core/http/router` and
`arkoren.dev/core/http/middleware/csrf`; `requireAccount` is your authentication
middleware and `changeEmail` is your existing authorized handler:

```go
routes.Group("/account", func(account *router.Router) {
	account.Use(csrf.Middleware(), requireAccount)
	account.Post("/email", changeEmail)
})
```

The group inherits the router headers; the CSRF policy does not affect unrelated
routes. If `requireAccount` uses sessions, install the
[session middleware](../http-capabilities/sessions.md#enable-sessions) before
it. Register middleware before routes and choose
[scope](../http-fundamentals/middleware.md#choose-scope) deliberately: signed
webhook receivers outside this group need their own request verification.

If you add CORS, place it before authentication and body checks so allowed
preflights can finish without credentials. Place secure headers outside CORS
if preflight responses also need those headers. See
[policy ordering](../http-fundamentals/middleware.md#order-policies) for the
execution model.

## Verify Boundaries

Check headers on successful responses and errors, then test CORS with allowed
and disallowed origins. A successful server-side test alone does not prove that
a browser can read a response; browser permission headers must also match.

For CSRF tests, send cross-site browser headers and assert that rejection leaves
application state unchanged. Requests without `Sec-Fetch-Site` or `Origin` pass
the CSRF checker; that is compatibility behavior, not evidence of identity.
Use the focused [rejection test](csrf-protection.md#test-rejection) to exercise
the actual boundary.

Begin with [Secure Headers](secure-headers.md) to establish the shared response
policy. For traffic volume and automated submissions, use
[abuse prevention](../abuse-prevention/overview.md) as a separate defense.
