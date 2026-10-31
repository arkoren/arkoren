---
title: Abuse Prevention Overview
nav: Overview
description: Combine request limits and CAPTCHA verification to protect sensitive operations without treating either as identity or permission.
order: 10
---

# Abuse Prevention Overview

## Introduction

Public forms and login endpoints receive automated submissions as well as
legitimate traffic. Abuse prevention limits how much work a caller can trigger
and adds checks before sensitive operations. Use rate limiting to control
request volume and CAPTCHA to evaluate a provider-issued browser token.

Neither control replaces [authentication](../authentication/overview.md),
[authorization](../authorization/overview.md), or
[CSRF protection](../browser-security/csrf-protection.md). A caller within a
rate limit or holding an accepted CAPTCHA token still needs permission for
the operation.

## Start with request limits

Apply [body limits](../http-fundamentals/body-limit.md) before reading request
bodies and [content-type checks](../http-fundamentals/content-type.md) before
decoding them. These checks bound input size and reject representations your
endpoint does not accept. They do not determine whether a submission is genuine.

Add [rate limiting](rate-limiting.md) before expensive work such as password
hashing or external token verification. Choose limits for the operation:
login attempts and a public read-only endpoint need different budgets.
Use distinct key prefixes when policies should count separately.

A client key defines who shares a budget. IP-based limits can group unrelated
visitors behind one network; user-based limits need authentication first.
Behind a proxy, configure trusted proxy networks instead of trusting arbitrary
forwarded headers. Use a shared cache across replicas so each process does not
grant a separate budget. Cache failures fail the request rather than allowing
it through.

## Add browser verification where needed

Use [CAPTCHA](captcha.md) when a public browser operation needs an additional
abuse signal, such as a contact submission or account registration. Obtain a
token for that operation and verify it on the server before performing the
side effect. Cloudflare Turnstile provides an accepted-or-rejected token check;
Google reCAPTCHA returns a risk assessment that needs an application-owned
score policy.

For JSON submissions, decode and validate in the handler, then verify the token
once. Header-based tokens can instead use the optional route middleware.
Choose one path: tokens are single-use, so middleware and the handler must not
both verify the same token. Apply size, content-type, and rate checks before
contacting the provider to avoid spending external calls on requests you can
already reject.

If verification fails or the provider is unavailable, do not perform the
protected operation. Ask for a fresh token on another attempt rather than
retrying the same verification. CAPTCHA does not make an operation idempotent;
prevent duplicate business effects separately.

## Check the protected operation

Test each rejection path and assert that it leaves the protected side effect
untouched. Exercise rate-limit keys and shared counters, and test rejected,
expired, and unavailable CAPTCHA responses. For Google, also test a valid
assessment below your score threshold.

Use [request logging](../observability/request-logging.md) and
[built-in events](../events/built-in-events.md#http) to investigate denials
without logging browser tokens or credentials. Begin with the
[rate limiting guide](rate-limiting.md), then add [CAPTCHA verification](captcha.md)
where the operation needs it.
