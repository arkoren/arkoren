---
title: CAPTCHA
description: Verify Turnstile tokens or Google reCAPTCHA assessments before accepting a submission, with explicit action, hostname, and risk policies.
order: 30
---

# CAPTCHA

## Introduction

Public forms attract automated submissions even when their input is valid.
CAPTCHA adds a provider-issued browser token that your server verifies before
performing the protected operation. It is an abuse signal, not proof of identity
or permission, and does not replace authentication, authorization, or
[CSRF protection](../browser-security/csrf-protection.md).

Arkoren provides vendor-local verifiers and optional route middleware in the
Cloudflare and Google Cloud modules. There is no core CAPTCHA contract: the
providers have different browser integrations and different acceptance policies.

## Choosing a provider

Choose the provider before building the browser integration; its tokens are not
interchangeable.

| Provider | Choose when | Server decision |
| --- | --- | --- |
| Cloudflare Turnstile | You want a valid-or-rejected token check. | Verify token validity, allowed hostname, and expected action. |
| Google reCAPTCHA | You need a risk assessment and application-owned score policy. | Verify token validity, allowed hostname, and expected action, then evaluate the score. |

A valid Google token can have a low score. Direct `Verify` returns that
assessment successfully so you can reject it or require another factor. The
Google middleware instead applies your explicit minimum score and rejects scores
below it. Turnstile does not provide an equivalent score; there is no universal
threshold shared by these providers.

## Compatibility matrix

Compare the verification policy exposed by Arkoren's adapters. These are
server-side checks, not guarantees that a visitor is human or authorized.

| Provider | Hostname allowlist | Expected-action check | Risk score | Route middleware | Middleware score threshold |
| --- | --- | --- | --- | --- | --- |
| [Cloudflare Turnstile](#cloudflare-turnstile) | Yes | Yes | No | Yes | No |
| [Google reCAPTCHA](#google-recaptcha) | Yes | Yes | Yes | Yes | Yes — Requires an explicitly configured finite threshold in [0, 1]. |

Action checks require a nonempty server-owned action. Turnstile hostname
matching is case-sensitive; Google's is case-insensitive. Direct Google
verification returns a valid low-score assessment without rejecting it: your
application owns that decision unless you use the score-checking middleware.
These cells are backed by adapter source and controlled HTTP tests, not a
live-cloud Google assessment or a real-browser challenge. Diagnostic formatting
does not add verification capabilities or change these cells.

## Setting up the provider

Create the verifier once during application composition and reuse it across
requests. Keep provider credentials in server-side configuration, never in HTML
or browser JavaScript. Site keys identify the browser integration and are public.

Allowed hostnames must come from trusted deployment configuration, such as
`[]string{"app.example.com"}`. Supply hostnames, not URLs. Never build this list
from an incoming request's `Host` header: that lets the caller choose what the
verifier trusts.

Each provider subsection is an alternative, not a cumulative setup. In the
route examples, `routes` is your existing `*router.Router` from
`arkoren.dev/core/http/router`; inside handler fragments, `r` is the incoming
`*http.Request`. See [routing](../http-fundamentals/routing.md) for construction
and registration. Select the verifier for the provider whose token you receive.

### Cloudflare Turnstile

[Create a Turnstile widget](https://developers.cloudflare.com/turnstile/get-started/)
for your site's hostnames in Cloudflare. Keep its
secret on the server and put its site key in the browser.

#### Installation

Install the module from your application's module directory:

```sh
go get arkoren.dev/driver/cloudflare
```

#### Connecting

With `secret` loaded from your server configuration, construct the verifier
using `arkoren.dev/driver/cloudflare/cloudflarecaptcha`:

```go
verifier, err := cloudflarecaptcha.New(secret, []string{"app.example.com"})
```

Handle construction errors before registering routes. This verifier calls
Turnstile's Siteverify API; its nil verification error means the provider accepted
the token and the configured hostname and requested action checks passed.
There are no lifecycle hooks to register.

#### Configuration

Verification has a ten-second total
timeout by default; `Builder.Timeout` changes that limit, and an earlier caller
deadline still wins.

To shorten the budget, import `time` and pass this policy as the trailing
argument to the `New` call above:

```go
budget := func(connection *cloudflarecaptcha.Builder) {
	connection.Timeout(3 * time.Second)
}
```

The resulting verifier uses at most three seconds per verification.
Zero restores the ten-second default; negative timeouts fail construction.

`Builder.HTTPClient` snapshots a supplied client value and replaces the snapshot's
redirect policy without changing your client. It retains the borrowed transport
and cookie jar. A shorter native client timeout still applies. Configure those
shared objects before concurrent use; neither the client nor its transport is
closed by this verifier.

#### Verifying tokens

Use the [Turnstile widget](#turnstile-widget), then follow
[Decoding and verifying](#decoding-and-verifying) to verify its token before
accepting a JSON submission. Use [header middleware](#protecting-a-header-based-route)
instead when your browser sends the token separately from the body.

#### Behavior and limits

Turnstile returns evidence, not a risk score. Hostname matching is exact and
case-sensitive. The adapter performs one verification attempt, never retries,
and treats provider failures as unavailable rather than permitting the operation.
There is no local emulator or memory driver; use the controlled HTTP transport
described under [Testing](#testing).

The verifier owns its secret and allowlist, but has no `Start`, `Stop`, or readiness
hook and no explicit secret-zeroization operation. Its `fmt.Formatter` method
writes `[REDACTED Turnstile verifier]` for `%v`, `%+v`, and `%#v` on a `Verifier`
value or pointer without traversing the borrowed client, transport, or jar.
This does not promise sanitization through arbitrary serialization, reflection,
invalid `fmt` verbs, native objects, or caller-owned data. This formatting hook
does not apply to the Google verifier or its credential-bearing construction draft.

#### API references

See the [Cloudflare CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha)
for borrowed clients, timeouts, error classes, and evidence fields.

### Google reCAPTCHA

Create a Google Cloud project, enable the reCAPTCHA Enterprise API, and
[create a score-based website key](https://docs.cloud.google.com/recaptcha/docs/create-key-website)
for your hostnames. Use the same project and site key on
the server.

#### Installation

Install the module:

```sh
go get arkoren.dev/driver/gcp
```

#### Connecting

Before constructing the verifier, configure Google Cloud authentication for
assessment creation. By default, use Application Default Credentials (ADC)
with `roles/recaptchaenterprise.agent` on the project. A restricted server API
key is an alternative; a browser site key is not that API key. The
[Google adapter reference](captcha.md#google-authentication-and-transport)
explains `APIKey` and borrowed preauthenticated HTTP clients, both of which
bypass ADC.

With authentication configured, `ctx` from application setup, and `projectID`
and `siteKey` from trusted configuration, construct the verifier using
`arkoren.dev/driver/gcp/gcpcaptcha`:

```go
verifier, err := gcpcaptcha.New(ctx, projectID, siteKey, []string{"app.example.com"})
```

Handle construction errors before registering routes.
ADC discovery can perform credential-loading I/O and cannot itself be interrupted
by the constructor context. That context is checked around discovery and is not
retained for later token refresh; see the reference for the exact boundary.
There are no lifecycle hooks to register. Each verification, including native
ADC token refresh, is bounded to ten seconds or the caller's earlier deadline.

#### Configuration

The builder selects `APIKey` or a borrowed `HTTPClient`; it has no timeout or
score-threshold setting. Bound direct calls with a shorter caller context when
needed. Set score policy in application code or explicitly configure the optional
middleware's `MinScore`.

For the API-key alternative, pass this policy to `New`; `apiKey` is your
restricted server key, not the public browser site key:

```go
credentials := func(access *gcpcaptcha.Builder) {
	access.APIKey(apiKey)
}
```

This selects API-key authentication and skips ADC discovery. Do not combine it
with a borrowed client unless you intentionally need both authentication paths.

#### Verifying tokens

Obtain a token with the [Google browser integration](#google-score-based-key),
then use the Google branch in [Decoding and verifying](#decoding-and-verifying).
That example checks authenticity and your score threshold before the effect;
[Requiring another login factor](#requiring-another-login-factor) shows a richer
application policy.

#### Behavior and limits

This adapter uses the current `projects.assessments.create` API
(`CreateAssessment`), not the legacy Google Siteverify endpoint or its shared
secret flow. A successful assessment exposes `Score`, `Name`, and `Reasons` for
your application policy; it does not sign the visitor in.

Hostname matching is exact and case-insensitive, without implicit subdomain
trust. Verification never retries. There is no local emulator or memory driver;
use a controlled HTTP transport for deterministic [tests](#testing).

#### API references

See the [Google CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha)
for authentication ownership, response defaults, evidence fields, and errors.

## Google authentication and transport

Use `Builder.APIKey` for an API key restricted to this backend and API. It is
sent as `X-Goog-Api-Key`, not in the URL; never expose it to the browser or log
the construction draft.

If your application already owns an authenticated HTTP client, pass it to the
verifier instead of discovering ADC. With your project, site key, and allowed
hostnames from the Google setup, the construction callback can select it:

```go
func(access *gcpcaptcha.Builder) {
	access.HTTPClient(authenticated)
}
```

Here `authenticated` is your existing `*http.Client`; use the callback in
`gcpcaptcha.New` above. A nonnil client bypasses ADC entirely. Without an API
key, its transport must add authentication. With an API key, the adapter adds
that header without removing authentication supplied by the transport. Passing
nil restores normal ADC/API-key selection.

The client value is snapshotted, but its transport remains borrowed and is
never closed by the verifier. Configure shared transport state before concurrent
use. Redirects are disabled and the cookie jar is discarded, even for a supplied
client. Positive timeouts below ten seconds are retained; zero, negative, or
larger values become ten seconds. Every verification also has a ten-second
context bound and honors an earlier caller deadline.

Custom transports and authentication must support concurrent requests, honor
cancellation, and never replay them. Tokens are single use and Google does not
document assessment idempotency; retrying middleware can consume a token without
giving your application usable evidence.

## Obtaining a browser token

Generate a token for the operation you are about to submit. The examples use
the action `contact`; configure that same literal as the server's expected
action. Treat an action name as server policy, not a field the caller chooses.
Both adapters skip local action comparison if `ExpectedAction` or middleware
`Action` is empty; set it explicitly for each protected operation in this guide.

### Turnstile widget

Replace `TURNSTILE_SITE_KEY` with your widget's public site key. Define
`receiveToken` before loading the provider script:

```html
<script>
  function receiveToken(token) {
    // Pass token as captcha_token in the next contact submission.
  }
</script>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
<div class="cf-turnstile" data-sitekey="TURNSTILE_SITE_KEY"
     data-action="contact" data-callback="receiveToken"></div>
```

The callback receives the token, not a verification result. Your application
must attach it to the submission and wait for server verification before showing
success. Clear stored tokens when the widget expires or errors, and reset the
widget to obtain a fresh token for another attempt. In a traditional form,
Turnstile also inserts a `cf-turnstile-response` field; choose and decode that
form representation explicitly instead of using the JSON example below.

### Google score-based key

Replace both occurrences of `RECAPTCHA_SITE_KEY` with your public score-based
site key. In your existing contact-submit callback, after loading this script,
execute the action and attach the resulting token:

```html
<script src="https://www.google.com/recaptcha/enterprise.js?render=RECAPTCHA_SITE_KEY"></script>
```

```js
grecaptcha.enterprise.ready(async () => {
  const token = await grecaptcha.enterprise.execute("RECAPTCHA_SITE_KEY", {
    action: "contact",
  });
  // Pass token as captcha_token in this contact submission.
});
```

Handle script-load and execution failures in your submit UI; do not send a
protected operation as though CAPTCHA succeeded. Execute on the interaction,
not long before it: the server must create the assessment within two minutes
of token generation. Turnstile tokens expire after five minutes. Both are
single-use, so obtain a fresh token after expiry or a failed submission.

## Verifying a JSON submission

For JSON requests, decode the body once in the handler, validate the application
fields, then call the verifier before the side effect. This keeps body ownership
with the handler and avoids middleware consuming a stream the handler still
needs. Apply [content-type checks](../http-fundamentals/content-type.md), [body limits](../http-fundamentals/body-limit.md),
and [rate limiting](rate-limiting.md) before any external verification call.
`request.JSON` does not enforce `Content-Type` itself.

Verification spends a single-use token. Verify once before effects; on another
attempt, obtain a fresh token instead of retrying verification. A lost response
may mean the provider already consumed the token. Keep verification fail-closed
and avoid retrying HTTP transports.

### Mapping verifier errors

Direct `Verify` returns plain errors, not HTTP problems. Before registering
the handler, map its selected provider's sentinels with
`arkoren.dev/core/http/middleware/errormap`, `arkoren.dev/foundation/problem`,
and `net/http`:

```go
routes.Use(errormap.Middleware(
	errormap.Map(cloudflarecaptcha.ErrRejected, problem.Forbidden()),
	errormap.Map(cloudflarecaptcha.ErrUnavailable, problem.New(func(details *problem.Builder) {
		details.Status(http.StatusServiceUnavailable)
	})),
	errormap.Map(cloudflarecaptcha.ErrConfiguration, problem.InternalServerError()),
))
```

Use `gcpcaptcha` in all three mappings for a Google route. These mappings keep
provider details out of the response while preserving the cause for internal
handling. Without mapping, returning a plain rejection error produces the
router's generic `500`, not a `403`. Context cancellation and deadline errors
remain identifiable with `errors.Is`; do not classify them as a bad token.

The adapters' `Error()` text uses bounded classifications, not native diagnostics.
Original causes remain available through `errors.Is`/`errors.As`, but those causes
can contain confidential transport or provider data. Inspect identities deliberately;
do not dump unwrapped objects or treat arbitrary formatting/serialization of error
wrappers as a sanitization boundary.

### Decoding and verifying

Using `request` from `arkoren.dev/core/http/request`, decode your application's
existing contact request type, which includes a `CAPTCHAToken` field tagged
`json:"captcha_token"`:

```go
input, err := request.JSON[ContactRequest](r)
if err != nil {
	return err
}
```

After validating the contact fields, verify the Turnstile token with the
constructed `verifier`. Only proceed to your contact operation after nil error:

```go
_, err = verifier.Verify(r.Context(), cloudflarecaptcha.Input{
	Token:          input.CAPTCHAToken,
	ExpectedAction: "contact",
})
if err != nil {
	return err
}
// Perform the validated contact operation here.
```

For Google, replace that verification step with an assessment and your own
server-configured `contactMinScore` policy. Higher scores indicate lower
assessed risk. Choose the threshold from your site's observations, not a
universal recommended value, and validate that it is finite and in `[0, 1]`
when loading configuration.

A zero threshold deliberately accepts every valid score but still checks the
token, hostname, action, and risk-analysis message. Inside a present Google
risk-analysis message, an omitted or null score means zero under ProtoJSON;
a positive threshold rejects it as a policy denial, not a verification outage.

This example sends the visitor's user agent to Google;
omit `UserAgent` if your privacy policy does not permit that disclosure:

```go
assessment, err := verifier.Verify(r.Context(), gcpcaptcha.Input{
	Token:          input.CAPTCHAToken,
	ExpectedAction: "contact",
	UserAgent:      r.UserAgent(),
})
if err != nil {
	return err
}
if assessment.Score < contactMinScore {
	return problem.Forbidden()
}
// Perform the validated contact operation here.
```

The protected contact operation runs only after verification and the score
policy succeed. A low valid score returns `403` without running that operation.

### Requiring another login factor

For login, a low valid Google score can start an additional verification step
instead of denying the visitor outright. In an application that has already
checked credentials and obtained a `candidate`, verify the login token once with
`ExpectedAction: "login"` and handle errors before evaluating its `assessment`.
Use a separately configured, validated `loginMinScore` threshold.

Here `factors` and `sessions` are application-owned collaborators, not CAPTCHA
APIs. `factors.Begin` must create only a short-lived pending challenge, not an
authenticated session. A separate endpoint verifies that factor and only then
signs in the candidate. Apply any factor your authentication policy always
requires on both branches; a high CAPTCHA score never substitutes for it:

```go
if assessment.Score < loginMinScore {
	return factors.Begin(r.Context(), candidate)
}
return sessions.SignIn(r.Context(), candidate)
```

The low-score path starts a pending challenge and returns without signing in.

## Protecting a header-based route

If the browser sends its token in a header, optional middleware can reject the
request without reading the body. The extractor owns token location; neither
adapter automatically parses JSON or forms. Use this instead of handler-level
verification; the `contact` handler must not verify the token again. Keep body
limits, content-type checks, and rate limiting outside CAPTCHA middleware.
For a cross-origin browser, configure [CORS](../browser-security/cors.md) to allow the token header.
With `net/http` imported, define the extractor:

```go
extract := func(r *http.Request) (string, error) {
	return r.Header.Get("X-CAPTCHA-Token"), nil
}
```

For a Turnstile route, set the server-owned action before registering your
existing `contact` handler:

```go
routes.With(cloudflarecaptcha.Middleware(verifier, extract,
	func(policy *cloudflarecaptcha.MiddlewareBuilder) {
		policy.Action("contact")
	},
)).Post("/contact", contact)
```

For Google, explicitly set both the action and a finite minimum score in
`[0, 1]`; omitting `MinScore` is a configuration error, not an implicit default:

```go
routes.With(gcpcaptcha.Middleware(verifier, extract,
	func(policy *gcpcaptcha.MiddlewareBuilder) {
		policy.Action("contact")
		policy.MinScore(contactMinScore)
	},
)).Post("/contact", contact)
```

The middleware returns safe HTTP problems: `403` for rejection (including a
Google score below the threshold), `503` for provider unavailability, and `500`
for configuration failures. It fails closed and never calls the handler on
verification failure. Use direct verification instead when the handler needs
the Google assessment for a step-up decision. Both adapters map ordinary
extraction errors to `403`, and cancellation, deadline, or unavailable extraction
errors to `503`. Configuration errors take precedence and return `500`.
The original causes remain identifiable with `errors.Is`; operational extraction
failures do not acquire a token-rejection classification.

Turnstile requires nonnil verifier, extractor, and next-handler values; nil is
programmer misuse and panics during middleware wiring. Google's middleware instead
reports a nil verifier or extractor, or missing/invalid score policy, as a safe
configuration `500` on requests without extracting or verifying a token.

## Production boundaries

Neither adapter retries verification automatically. A lost response can mean
the provider already consumed the token; do not add a retrying HTTP transport
or replay the same token on a timeout. Keep a bounded request deadline and
fail closed during an outage. Ask the browser for a fresh token on another
attempt. Business-operation idempotency is separate: a fresh CAPTCHA token
does not prevent a payment, message, or account operation from running twice.

`Input.RemoteIP` is an optional `netip.Addr`. Supply it only from a trusted
client-address resolver; arbitrary forwarded headers are attacker-controlled
unless your proxy trust boundary validates them. Sending IP addresses and
Google user agents discloses visitor data to the provider; apply your privacy
policy and omit optional data you do not need. Keep tokens and provider
credentials out of logs, traces, and error responses.

## Testing

Use a controlled HTTP client through `Builder.HTTPClient` to return provider
responses without network access. See the provider reference for authentication
behavior when supplying that client. There is no memory CAPTCHA driver: a
local stub proves your decision logic, not that a browser challenge is genuine.

Test an accepted token, a rejected or missing token, an action mismatch, a
hostname mismatch, upstream failure, and canceled requests. For Google, also
test a valid low score, the threshold boundary, missing `MinScore` in middleware,
and that a pending second factor never creates an authenticated session. Assert
that rejected requests leave the protected side effect untouched. Keep live
provider checks separate from deterministic unit tests.

Local Google tests can establish request/authentication wiring, ProtoJSON defaults,
context handling, and score-policy behavior. They do not establish live ADC/IAM
permissions, provider acceptance of production credentials, or real-browser
challenge behavior.

For configuration, error, and HTTP-client details, see the
[Cloudflare CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha)
and [Google CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha).
