---
title: Drivers
description: Choose a push backend and configure sender retention, VAPID credentials, and secure endpoint submission.
order: 20
summary:
  - Compare offline fake recordings with encrypted Web Push submission.
  - Configure persistent credentials, request timeouts, and a reviewed endpoint policy.
  - Interpret payload limits and provider responses while preserving registration ownership.
  - Select sender retention and optional privacy-safe submission observations.
---
# Drivers

Use the core test fake to check what your application submits without making
network requests. Choose Web Push to send encrypted notifications to browser
subscriptions, including supported Safari web apps. Neither backend tracks
device delivery or stores your application's registrations.

## Compatibility Matrix

These capabilities describe Arkoren's exposed behavior. The fake records local
acceptance only; its browser-shaped input does not emulate a browser or provider.

| Driver | Browser Subscriptions | Native Tokens | External Submission | No-Store TTL | Delivery Tracking | Sender Events |
| --- | --- | --- | --- | --- | --- | --- |
| [Test Fake](#test-fake) | Yes — structural validation only | Yes — synthetic input | No | Yes — records resolved zero | No | Yes — opt-in |
| [Web Push](#web-push) | Yes | No | Yes | Yes — zero TTL header | No | Yes — opt-in |

Events belong to the sender, not the driver. Direct driver calls emit none.
`push.Token` does not select a provider automatically, and Web Push is not a
native mobile push integration.

## Test Fake

`pushtest.Fake` is bundled with `arkoren.dev/core`. It needs no credentials,
construction options, external process, or lifecycle hooks. In a test, import
`arkoren.dev/core/service/push` and its `pushtest` subpackage:

```go
fake := pushtest.New()
sender := push.New(fake)
```

Pass `sender` to the application code under test. The fake records immutable
messages and resolved TTL values; [submission tests](testing.md#asserting-submissions)
show precise assertions and failure injection. `Sent()` returns a detached slice
and message metadata accessors return copies.

Portable validation happens before recording or consuming an injected failure.
The fake does not enforce Web Push origin or payload limits, encrypt messages,
persist data, or simulate delivery. No cleanup is required; `Reset` clears
recordings and failure settings when a test deliberately reuses the instance.

See the [pushtest API reference](https://pkg.go.dev/arkoren.dev/core/service/push/pushtest).

## Web Push

`webpushpush` submits standards-based encrypted Web Push. You need a browser-issued
subscription, a [persistent VAPID identity](vapid-keys.md), and an operator contact
URI. Browser Web Push does not require a native APNs certificate or Apple
Developer Program membership; [browser integration](browser-integration.md)
still supplies registration and display code.

### Installing Web Push

From your application's module directory, install the driver:

```sh
go get arkoren.dev/driver/webpush
```

With `identity` as a loaded `key.Signing` value, construct the driver using
`arkoren.dev/driver/webpush/webpushpush` and wrap it with
`arkoren.dev/core/service/push`:

```go
driver, err := webpushpush.New(identity, "mailto:push@example.com")
```

Handle a construction error before wiring the driver. `New` validates the key,
contact, and settings without contacting a provider or registering a browser.
The contact is an operator's `mailto:` address or HTTPS URI, not the customer's
notification address.

```go
sender := push.New(driver)
```

Reuse the constructor's pointer for concurrent sends; do not copy its
synchronized authorization cache. The driver owns its signing-scalar copy and
hardened HTTP client, not your original identity. It has no `Start`, `Stop`,
`Ping`, native-client accessor, or credential-zeroization operation.

### Bounding Requests

A complete send has a ten-second timeout by default; a shorter caller context
deadline wins. If your request budget is three seconds, import `time` and use
this alternative construction:

```go
driver, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
	connection.Timeout(3 * time.Second)
})
```

Zero restores ten seconds; a negative timeout fails construction. This bound
controls request work, not the notification's offline retention.

### Endpoint Policy

Subscription endpoints are untrusted capability URLs. By default the driver
permits `https://fcm.googleapis.com`,
`https://updates.push.services.mozilla.com`, and DNS hosts below
`.push.apple.com` at a label boundary. The bare `push.apple.com` host and
lookalike suffixes do not match. It requires canonical lowercase DNS names and
HTTPS on port 443; explicit `:443` is accepted. IP literals, URL credentials,
fragments, and noncanonical authorities are rejected.

Keep this curated policy unless you need a reviewed alternative. To restrict
submission to Google's origin, replace the defaults at construction:

```go
driver, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
	connection.AllowedOrigins("https://fcm.googleapis.com")
})
```

`AllowedOrigins` replaces rather than extends the defaults. Entries are exact
HTTPS origins without paths, queries, trailing slashes, or wildcards; an empty
list denies all origins. This is trusted deployment configuration: never build
the allowlist from a browser's registration payload.

> [!WARNING]
> Approving an arbitrary subscription origin can expose your server to SSRF.
> Keep a narrow reviewed allowlist. Even allowed origins must resolve entirely
> to public addresses; the driver validates DNS at dialing and connects to a
> validated address while TLS verifies the original hostname.

The owned transport does not follow redirects or use environment proxies, and
there is no custom HTTP-client bypass. Request duration and response reads are
bounded. Private-network emulators and localhost providers are not supported
targets; use the fake for offline application tests.

### Sending Notifications

Send the [typed order notice](overview.md#preparing-messages) through the configured
sender using a stored `push.Subscription` value. The browser receives flat JSON
with `title`, optional `body`, and optional string-valued `data`, not a native
provider envelope. Recipient endpoints and encryption keys are absent from that
payload; the [service worker](browser-integration.md#displaying-notifications)
must read this shape.

The encoded JSON must fit within 3,993 plaintext bytes, including field names,
punctuation, and escaping. Encryption overhead and the final-record delimiter
bring the supported envelope to 4,096 bytes. Oversized input fails with
`push.ErrInvalidMessage` before transport; measuring body characters alone is
not sufficient.

TTL is rounded down to whole seconds. The representable limit is 2,147,483,647
whole seconds; a value whose whole-second count exceeds it fails rather than
being clamped. Providers can impose smaller bounds. Raw `SendConfig.TTL == 0`
requests no-store, unlike zero in the sender's per-call option.

### Interpreting Responses

HTTP 201 establishes acceptance and returns an empty `Receipt.ID`, even if
optional headers are missing or response cleanup fails. It does not establish
browser display. Other response classes are:

| Response | Classification |
| --- | --- |
| 404 or 410 | `push.ErrNotFound` |
| 429 | `push.ErrRateLimited` |
| 400 or 413 | `push.ErrInvalidMessage` |
| 500–599 | `push.ErrUnavailable` |
| Other 2xx | `push.ErrOutcomeUnknown` |

Authentication failures, redirects, and other unclassified responses remain
typed provider errors, not expired-registration or unsupported-operation signals.
Use `errors.As` with `*webpushpush.ProviderError` to inspect numeric `StatusCode`
and nonnegative `RetryAfter`; these fields do not schedule retries. Provider
bodies and capability URLs are not retained in that error.

Transport failures after dispatch conservatively preserve unknown acceptance
and native/context causes. Do not replay them automatically or delete a
registration after an authentication failure. On confirmed not-found responses,
invalidate only the submitted registration version; see
[outcome handling](overview.md#handling-outcomes).

### Credential Ownership

Concurrent sends share an origin-scoped VAPID authorization until five minutes
before its twelve-hour expiry. This cache belongs to one driver instance, not
other replicas. It holds at most 256 unexpired origins; adding another beyond
that limit fails closed. Keep the identity stable and the origin policy narrow.

Formatting the constructor pointer with `%v`, `%+v`, or `%#v` writes
`[REDACTED Web Push driver]`. This does not make arbitrary serialization,
reflection, driver copies, or native objects safe to log. Explicitly unwrapped
transport causes can contain sensitive URLs even when top-level error text is
bounded.

See the [Web Push API reference](https://pkg.go.dev/arkoren.dev/driver/webpush/webpushpush).

## Sender Settings

Sender settings apply to either backend. With an existing `driver`, import
`time` and `arkoren.dev/core/service/push` to request a one-hour default:

```go
sender := push.New(driver, func(delivery *push.Builder) {
	delivery.TTL(time.Hour)
})
```

A zero builder TTL restores 24 hours; a negative value disables offline storage.
The construction draft cannot reconfigure a published sender. The sender shares
the driver and does not take over its lifecycle.

If you need submission observations, supply a shared `*event.Bus` named `bus`
and a shared `*telemetry.Telemetry` named `instrumentation`:

```go
sender := push.New(driver, func(delivery *push.Builder) {
	delivery.Events(bus)
	delivery.Telemetry(instrumentation)
})
```

Nil events disable notifications; nil telemetry follows the process default on
each call. The [submission observation contract](overview.md#observing-submissions)
explains privacy and outcome limits. These settings add neither delivery
tracking nor background work.

See the [sender API reference](https://pkg.go.dev/arkoren.dev/core/service/push).
