---
title: CAPTCHA
description: Verify browser tokens with Turnstile or Google reCAPTCHA using trusted hostname, action, and score policies before protected effects.
order: 30
summary:
  - Choose a provider and configure reusable verifiers with server-owned credentials and hostnames.
  - Obtain single-use browser tokens for the action your server expects.
  - Verify typed JSON submissions once and apply an explicit Google risk-score policy.
  - Use header middleware as an alternative and distinguish rejection from provider failure.
---

# CAPTCHA

A feedback form can receive automated submissions even when every field passes
validation. CAPTCHA adds a provider-issued browser token that your server verifies before
accepting the feedback. An accepted token is an abuse signal, not proof of identity
or permission; it does not replace [CSRF protection](../browser-security/csrf-protection.md).

Arkoren ships vendor-local verifiers and optional route middleware. There is no
core CAPTCHA contract, and the two providers' tokens are not interchangeable.

## Provider Choice

Choose Cloudflare Turnstile for an accepted-or-rejected token check. Choose Google
reCAPTCHA when your application needs a risk assessment and owns the decision to
accept, deny, or require more verification. A valid Google token can have a low
score: direct `Verify` returns that assessment successfully. Only the optional
middleware applies a configured minimum score automatically.

### Compatibility Matrix

This comparison covers Arkoren's server-side adapters, not every vendor feature
or a guarantee that a visitor is human.

| Provider | Hostname Allowlist | Expected Action | Risk Score | Route Middleware | Middleware Score Policy |
| --- | --- | --- | --- | --- | --- |
| [Cloudflare Turnstile](#cloudflare-turnstile) | Yes | Yes | No | Yes | No |
| [Google reCAPTCHA](#google-recaptcha) | Yes | Yes | Yes | Yes | Yes — Requires an explicit finite minimum in [0, 1]. |

Both adapters require an allowlist from trusted deployment configuration. Supply
bare hostnames, not URLs, wildcards, or a list derived from the incoming `Host`
header. Neither implicitly trusts subdomains. Turnstile compares case-sensitively;
Google compares case-insensitively.

Choose an action literal on the server, such as `feedback`, and use it in the
browser integration too. Empty `ExpectedAction` or middleware `Action` skips the
local action comparison. Do not copy the expected value from the submitted body.

Create one verifier during application setup, handle construction errors before
mounting routes, and reuse it concurrently. Neither verifier has lifecycle or
readiness hooks. Keep secrets and API credentials on the server; browser site
keys are public.

## Cloudflare Turnstile

[Create a Turnstile widget](https://developers.cloudflare.com/turnstile/get-started/)
for your deployment's hostnames. Store its secret in server configuration and
use its public site key in the browser. From your application module, install:

```sh
go get arkoren.dev/driver/cloudflare
```

### Server Setup

Import `arkoren.dev/driver/cloudflare/cloudflarecaptcha`. With `secret` loaded
from server configuration, construct the verifier:

```go
verifier, err := cloudflarecaptcha.New(secret, []string{"app.example.com"})
```

Construction validates local settings without network I/O. A blank secret, empty
allowlist, or invalid hostname returns `ErrConfiguration`; handle `err` before
using `verifier`. Verification uses Turnstile's HTTPS Siteverify endpoint.

The default verification deadline is ten seconds, including response reading.
If feedback needs a shorter budget, import `time` and use this alternative
construction:

```go
verifier, err := cloudflarecaptcha.New(secret, []string{"app.example.com"},
	func(connection *cloudflarecaptcha.Builder) {
		connection.Timeout(2 * time.Second)
	})
```

An earlier caller deadline still wins. Zero restores ten seconds; negative
timeouts fail construction. `Builder.HTTPClient` can borrow an existing
`*http.Client`: construction snapshots its fields, disables redirects in the
snapshot, and retains its transport and cookie jar without modifying your client.
A shorter native timeout still applies. Configure shared objects before
concurrent use; the verifier closes neither client nor transport.

Verification makes one attempt and does not send Siteverify's optional
idempotency key. Do not add a retrying transport: a timeout can mean the token
was consumed. Empty tokens or tokens over 2,048 bytes reject before I/O. Nil
error means provider success, a nonzero challenge timestamp, an allowed hostname,
and a matching action when requested. Returned `CData` remains untrusted,
potentially sensitive client-originated metadata, not a permission claim or risk score.

Formatting a verifier value or pointer with `%v`, `%+v`, or `%#v` emits
`[REDACTED Turnstile verifier]` without traversing its borrowed objects. This is
not a general sanitization boundary for reflection, serialization, invalid format
verbs, or native objects, and does not apply to the Google verifier. There is no
explicit secret-zeroization operation.

### Browser Token

Define the token callback before loading the widget script. Replace
`TURNSTILE_SITE_KEY` with your public key:

```html
<script>
  function feedbackToken(token) {
    // Attach token as captcha_token to the pending feedback submission.
  }
</script>
<script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
<div class="cf-turnstile" data-sitekey="TURNSTILE_SITE_KEY"
     data-action="feedback" data-callback="feedbackToken"></div>
```

The callback receives a credential, not verification success. Attach it to your
submission and wait for the server's result. Clear stored tokens on widget error
or expiry and reset the widget before another attempt. Traditional forms instead
receive a `cf-turnstile-response` field; decode that representation explicitly
rather than treating it as JSON.

[Turnstile tokens](https://developers.cloudflare.com/turnstile/get-started/server-side-validation/)
expire after five minutes and are single-use. Continue with
[JSON verification](#json-verification), or choose [header middleware](#header-middleware)
instead; do not verify the same token in both places.

For constructor and evidence details, see the
[Cloudflare CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha).

## Google reCAPTCHA

Create a Google Cloud project, enable the reCAPTCHA Enterprise API, and
[create a score-based website key](https://docs.cloud.google.com/recaptcha/docs/create-key-website)
for your hostnames. Use that project and site key on the server. Install:

```sh
go get arkoren.dev/driver/gcp
```

### Server Setup

Configure Application Default Credentials (ADC) for assessment creation, with
`roles/recaptchaenterprise.agent` on the project. Import
`arkoren.dev/driver/gcp/gcpcaptcha`. With the setup context `ctx`, trusted
`projectID`, and public `siteKey`, construct the verifier:

```go
verifier, err := gcpcaptcha.New(ctx, projectID, siteKey, []string{"app.example.com"})
```

Construction validates settings and loads credentials; it does not create an
assessment or perform readiness checks. ADC discovery can do credential-loading
I/O, and its synchronous discovery call cannot be interrupted by `ctx`. The
constructor checks cancellation around discovery and uses `ctx` for quota-project
lookup, but does not retain it. Later native token refresh uses each verification
request's bounded context; asynchronous background refresh is disabled.

If you use a restricted server API key instead of ADC, pass it in this alternative
construction. `apiKey` is not the public browser site key:

```go
verifier, err := gcpcaptcha.New(ctx, projectID, siteKey, []string{"app.example.com"},
	func(access *gcpcaptcha.Builder) {
		access.APIKey(apiKey)
	})
```

This bypasses ADC and sends the key as `X-Goog-Api-Key`, never in the URL. Keep
the key and credential-bearing construction draft out of logs.

### Borrowed Clients

If your application already owns an authenticated `*http.Client` named `client`,
select it instead of ADC:

```go
verifier, err := gcpcaptcha.New(ctx, projectID, siteKey, []string{"app.example.com"},
	func(access *gcpcaptcha.Builder) {
		access.HTTPClient(client)
	})
```

A nonnil client bypasses ADC completely. Without `APIKey`, its transport must
authenticate requests. If you intentionally configure both, the adapter adds
the API-key header without removing transport authentication. A nil client
restores normal ADC/API-key selection.

Construction snapshots the client, discards its jar, and disables redirects.
Its transport remains borrowed and is never closed. Positive client timeouts
shorter than ten seconds are retained; zero, negative, or longer timeouts become
ten seconds. Each verification, including ADC token refresh, also has a ten-second
context limit and honors an earlier caller deadline. Custom transports must be
concurrency-safe, honor cancellation, and never retry tokens. The builder has no
timeout or score-threshold setting; use a shorter caller context and choose score
policy separately.

This adapter uses `projects.assessments.create` (`CreateAssessment`), not legacy
Siteverify or a shared-secret flow. It makes one attempt. Empty tokens and tokens
over 64 KiB reject locally; that size is an application guard, not a documented
Google maximum. Successful assessments expose `Score`, `Name`, and reason/evidence
fields. They do not authenticate a visitor or expose other Fraud Defense APIs.

### Browser Token

Load the Enterprise script with your public score-based site key:

```html
<script src="https://www.google.com/recaptcha/enterprise.js?render=RECAPTCHA_SITE_KEY"></script>
```

In your existing feedback-submit callback, execute the matching action. Replace
the key here with the same value:

```js
grecaptcha.enterprise.ready(async () => {
  const token = await grecaptcha.enterprise.execute("RECAPTCHA_SITE_KEY", {
    action: "feedback",
  });
  // Attach token as captcha_token to this feedback submission.
});
```

Handle script-load and execution errors in your UI; do not submit as though
verification succeeded. Generate the token at the interaction, not when the page
first opens. [Google tokens](https://docs.cloud.google.com/recaptcha/docs/create-assessment-website)
expire after two minutes and can be assessed only once. Obtain a fresh token after
expiry or a failed submission. Continue with [JSON verification](#json-verification)
or the alternative [header middleware](#header-middleware).

For authentication and assessment fields, see the
[Google CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha).

## JSON Verification

For a token inside JSON, let the handler decode the body once and verify the token
before its effect. Apply [body limits](../http-fundamentals/body-limit.md),
[content-type checks](../http-fundamentals/content-type.md), and
[rate limiting](rate-limiting.md) before external verification;
`request.JSON` does not enforce `Content-Type` itself. See
[middleware ordering](../http-fundamentals/middleware.md#order-policies) when
placing those checks.

Use one provider branch below, not both. Neither adapter retries automatically,
and custom HTTP transports must not replay a verification. If a response is lost,
the token might already be spent: stop the effect and ask for a fresh token on a
later attempt. A fresh token does not deduplicate feedback or any other business
operation; application-owned idempotency remains separate.

### Map Failures

Direct `Verify` returns plain classified errors, not HTTP problems. Before
registering a Turnstile handler, map them with
`arkoren.dev/core/http/middleware/errormap`, `arkoren.dev/foundation/problem`, and
`net/http`. Here `routes` is your existing router:

```go
routes.Use(errormap.Middleware(
	errormap.Map(cloudflarecaptcha.ErrRejected, problem.Forbidden()),
	errormap.Map(cloudflarecaptcha.ErrUnavailable, problem.New(func(details *problem.Builder) {
		details.Status(http.StatusServiceUnavailable)
	})),
	errormap.Map(cloudflarecaptcha.ErrConfiguration, problem.InternalServerError()),
))
```

For Google, replace the three sentinel qualifiers with `gcpcaptcha`. Rejection
then returns `403`, provider unavailability `503`, and configuration failure
`500`, without publishing provider diagnostics. Without mapping, a plain rejection
gets the router's generic `500`, not `403`. Cancellation and deadline causes remain
identifiable with `errors.Is`; they are operational failures, not bad tokens.

Both adapters keep native diagnostics out of their `Error()` text. Original
causes remain inspectable with `errors.Is` and `errors.As`, however, and can contain
confidential transport or provider data. Do not dump unwrapped causes or assume
arbitrary formatting and serialization of wrappers sanitizes them.

### Decode Input

Define the feedback request separately from provider input. JSON tags describe
the browser's representation, not verification policy:

```go
type FeedbackInput struct {
	Message      string `json:"message"`
	CAPTCHAToken string `json:"captcha_token"`
}
```

With `request` imported from `arkoren.dev/core/http/request` and `incoming` the
handler's `*http.Request`, decode once:

```go
input, err := request.JSON[FeedbackInput](incoming)
if err != nil {
	return err
}
```

Validate the message using your application's input rules before verification.
Keep `CAPTCHAToken` out of request-body logging, traces, and validation diagnostics.

### Verify Turnstile

Using the Turnstile `verifier` from setup, require the server-owned feedback
action and stop on every verification error:

```go
_, err = verifier.Verify(incoming.Context(), cloudflarecaptcha.Input{
	Token:          input.CAPTCHAToken,
	ExpectedAction: "feedback",
})
if err != nil {
	return err
}
// Accept the validated feedback here.
```

Only nil error permits the feedback operation to proceed. The hostname check
always applies; the action check applies because you supplied a nonempty action.

### Evaluate Scores

For Google, choose a finite `feedbackMinScore` in `[0, 1]` from your site's
observations and validate it when loading server configuration. There is no
universal safe threshold. Higher scores indicate lower assessed risk. Replace
the Turnstile block with:

```go
assessment, err := verifier.Verify(incoming.Context(), gcpcaptcha.Input{
	Token:          input.CAPTCHAToken,
	ExpectedAction: "feedback",
})
if err != nil {
	return err
}
if assessment.Score < feedbackMinScore {
	return problem.Forbidden()
}
// Accept the validated feedback here.
```

The operation runs only after token verification and score policy succeed.
Explicit zero accepts every valid score, but still requires valid token evidence,
an allowed hostname, the requested action, and a present risk-analysis message.
An omitted or null score inside a present message means zero under ProtoJSON;
a positive threshold denies it as policy, not a verification outage. A missing
risk-analysis message instead fails verification.

For login, direct verification can support a step-up decision instead of a flat
denial. After checking credentials and verifying once with `ExpectedAction: "login"`,
a low score can start an application-owned pending factor challenge.
That branch must not create an authenticated session. Verify the extra factor
on a separate endpoint before signing in. A high score never replaces factors
your authentication policy always requires.

Optional `Input.RemoteIP` is a `netip.Addr`; supply it only from your trusted
client-address resolver, never directly from arbitrary forwarded headers. Google
also accepts optional `Input.UserAgent`. Sending these fields discloses visitor
data to the provider; omit them unless your privacy policy permits and needs
that disclosure. The examples omit both.

## Header Middleware

If the token travels separately in a header, use middleware instead of handler
verification. It extracts and verifies once, then calls the handler only on
success. Neither adapter automatically parses JSON or forms, stores evidence in
request context, or infers client IP; Google middleware also infers no user agent.
Use direct verification when you need an assessment for a step-up decision.

Keep body limits, content-type checks, and rate budgets outside CAPTCHA middleware.
For cross-origin browser requests, [CORS](../browser-security/cors.md) must permit
the token header. With `net/http` imported, define a body-preserving extractor:

```go
extract := func(incoming *http.Request) (string, error) {
	return incoming.Header.Get("X-CAPTCHA-Token"), nil
}
```

For an existing Turnstile verifier and application handler `feedback`, mount:

```go
routes.With(cloudflarecaptcha.Middleware(verifier, extract,
	func(policy *cloudflarecaptcha.MiddlewareBuilder) {
		policy.Action("feedback")
	})).Post("/feedback", feedback)
```

For Google, use this alternative and explicitly configure the minimum score:

```go
routes.With(gcpcaptcha.Middleware(verifier, extract,
	func(policy *gcpcaptcha.MiddlewareBuilder) {
		policy.Action("feedback")
		policy.MinScore(feedbackMinScore)
	})).Post("/feedback", feedback)
```

The handler must not verify that token again. Google requires a finite minimum
in `[0, 1]`; omission is a configuration error, not a default threshold.

Both middleware adapters return safe HTTP problems: rejection is `403`, including
a Google score below the minimum; provider unavailability is `503`; configuration
failure is `500`. Ordinary extractor errors reject with `403`. Extractor errors
wrapping cancellation, deadline, or unavailability return `503` without adding a
token-rejection identity. Configuration takes precedence over other classes.
Original causes remain inspectable, but their text is not rendered to the client.

Turnstile panics during wiring for a nil verifier, extractor, next handler, or
construction callback. A zero verifier instead fails requests with configuration
errors. Google reports a nil verifier/extractor or missing/invalid score policy
as `500` on requests without extracting or verifying. Nil construction callbacks
panic in both adapters. These are configuration faults, not permission to bypass
the check.

## Rejection Tests

Inject a controlled `*http.Client` through each provider's `Builder.HTTPClient`
for deterministic responses. Google tests with a supplied client bypass ADC;
they do not verify live credentials or IAM permissions. Neither adapter has a
memory driver or local emulator. A stub proves application decisions, not a
genuine browser challenge.

Test accepted and missing tokens, rejection, hostname/action mismatches,
unavailability, and cancellation. For Google, include the exact score boundary,
a valid score below it, ProtoJSON's zero score, and missing middleware `MinScore`.
Assert that every failure leaves the feedback effect untouched. If login uses a
step-up branch, assert that it creates only a pending challenge, never a session.
Keep real-browser and live-provider checks separate from deterministic tests.

For complete contracts, see the
[Cloudflare CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/cloudflare/cloudflarecaptcha)
and [Google CAPTCHA API reference](https://pkg.go.dev/arkoren.dev/driver/gcp/gcpcaptcha).
