---
title: Abuse Prevention Overview
nav: Overview
description: Choose request budgets and browser verification for sensitive endpoints without confusing abuse checks with permission.
order: 10
summary:
  - Distinguish request budgets, browser verification, authentication, and permission checks.
  - Place inexpensive rejections before external verification and protected effects.
  - Test denial and outage paths without exposing tokens or client keys.
---

# Abuse Prevention Overview

A public contact form can receive valid input faster than your application can
process it. Abuse prevention controls how much work a caller can trigger and
whether a browser submission needs additional verification. Choose these controls
for the operation you are protecting, rather than applying one policy to every request.

## Choose Controls

Use [rate limiting](rate-limiting.md) to give callers a request budget over a time
window. The cache-backed middleware rejects requests over that budget before
calling the protected handler. It counts admitted attempts, not successful
business outcomes: an attempt can consume budget even if the handler later fails.

Add [CAPTCHA verification](captcha.md) when a browser submission needs another
abuse signal. Cloudflare Turnstile verifies a token against your hostname and
action policy. Google reCAPTCHA returns a risk score as well; your application
chooses whether to accept that score, deny the submission, or require another factor.

Neither control establishes [identity](../authentication/overview.md) or grants
[permission](../authorization/overview.md). An authenticated user can exceed an
abuse budget, and an accepted CAPTCHA token does not authorize access to another
user's data. Browser session endpoints still need the appropriate
[CSRF protection](../browser-security/csrf-protection.md).

## Place Checks

For a JSON contact submission, reject an unsupported
[content type](../http-fundamentals/content-type.md) and bound the
[body size](../http-fundamentals/body-limit.md) before decoding. Apply a rate
budget before expensive work, including external CAPTCHA calls. Decode and
validate the contact input, verify its token once, then perform the contact effect.
Follow [middleware ordering](../http-fundamentals/middleware.md#order-policies)
and [scope selection](../http-fundamentals/middleware.md#choose-scope) when placing
those checks on your routes.

The budget's key determines who shares it. An IP key can group unrelated people
behind one network; an account key requires authentication before the limiter.
Behind a reverse proxy, trust only the proxy networks you control, not arbitrary
forwarded headers. Separate unrelated budgets with distinct cache names, and use
a shared backend when replicas must consult the same counters. Backend failures
stop the protected request; they do not disable the limiter.

For CAPTCHA, choose handler verification when the token is part of JSON. Use the
optional header middleware instead when the token arrives separately. Do not use
both for one token: verification consumes a single-use credential. If verification
fails or its outcome is unknown, stop the effect and obtain a fresh token for a
later attempt, rather than retrying the same verification.

A fresh token does not make a repeated submission safe. Keep duplicate-effect
handling in the business operation; neither rate limiting nor CAPTCHA promises
exactly-once execution. Likewise, [signed links](../http-capabilities/secure-links.md)
protect URL integrity and expiry, not request volume or general permission.

## Check Rejections

Test that an over-budget request never reaches the handler, and that CAPTCHA
rejection or provider unavailability leaves the protected effect untouched. For
Google, include a valid token whose score fails your policy. Exercise different
client keys and the backend failure path, not only successful requests.

Use [request logging](../observability/request-logging.md) for request outcomes
and [cache monitoring](../cache/monitoring.md) for backend failures. Keep browser
tokens and credentials out of logs. Client keys and concrete URL paths can also
identify people or resources; restrict and redact them before export.

Start with a [route budget](rate-limiting.md), then add
[browser verification](captcha.md) where that operation needs it.
