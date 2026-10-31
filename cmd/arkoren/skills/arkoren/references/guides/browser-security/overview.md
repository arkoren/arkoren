---
title: Browser Security Overview
nav: Overview
description: Choose browser protections for framing, cross-origin response access, and forged state-changing requests.
order: 10
---

# Browser Security Overview

## Introduction

A signed-in visitor can open an untrusted page while your application is still
open. Browser security controls limit how that page interacts with your service
and how the browser handles your responses. Response headers, CORS, and CSRF
protection address different risks; enabling one does not replace the others.

These controls do not establish identity or permissions. Use
[authentication](../authentication/overview.md) to identify the caller and
[authorization](../authorization/overview.md) to decide what they can do.
Non-browser clients are not constrained by browser response policies.

## Choosing protections

Start with [secure headers](secure-headers.md) on your router. Their defaults
prevent framing and content-type guessing and restrict referrer disclosure.
HSTS is opt-in after you establish HTTPS deployment. The default Content Security Policy only
prevents framing; applications serving HTML need a policy that also matches
their scripts, styles, and other resources.

If your service accepts browser session cookies, add
[CSRF protection](csrf-protection.md) before authentication and state-changing
handlers. Cross-site request forgery means an untrusted page causes a browser
to submit an action with the visitor's cookies. Arkoren checks browser origin
headers without requiring form tokens. Keep `GET` and `HEAD` free of side
effects: the CSRF middleware allows those methods through.

Configure [CORS](cors.md) if a browser application on another origin needs to
read your API responses. An origin consists of a scheme, host, and optional
port. List the origins, methods, and headers that application needs; allow
credentials only for explicitly trusted origins. Same-origin browser requests
do not need CORS permission.

## Cross-origin access is not permission to act

CORS governs what browser code can read. A request from a disallowed origin
can still reach your handler without CORS response headers, and a cross-site
form can cause damage without reading the response. Use CSRF protection for
cookie-authenticated actions even when your CORS policy is restrictive.

Conversely, trusting an origin in the CSRF policy does not let its JavaScript
read responses. A separate browser front end may need both an explicit CORS
policy and a trusted CSRF origin. Neither policy grants access to protected
application data; authentication and authorization still apply.

## Applying and checking the controls

Follow [middleware ordering](../http-fundamentals/middleware.md#ordering) when
combining these controls. CORS preflights must run before authentication so
the browser can ask permission without credentials. Keep any CSRF bypass
limited to routes with their own verification, such as signed webhook receivers.

Check response headers in a browser, test allowed and disallowed origins, and
assert that blocked state-changing requests leave application data untouched.
An ordinary test request without browser headers does not exercise the CSRF
boundary; use the browser headers shown in the [CSRF tests](csrf-protection.md#testing).

For excessive traffic and automated submissions, combine these controls with
[abuse prevention](../abuse-prevention/overview.md).
