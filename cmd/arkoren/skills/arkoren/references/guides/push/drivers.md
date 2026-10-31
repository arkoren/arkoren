---
title: Push Drivers
nav: Drivers
description: Choose the in-process test fake or standards-based Web Push, and configure secure provider submission.
order: 50
---
# Push Drivers

## Introduction

Use the in-process fake when testing application behavior without a provider.
Use Web Push when sending visible notifications to browser subscriptions,
including supported Safari web apps. Native mobile push delivery is not supplied
by the Web Push driver; `push.Token` reserves an opaque recipient form for drivers
that understand it, not an automatic provider router.

## Compatibility matrix

This comparison covers behavior exposed by Arkoren, not every vendor feature.
The fake's acceptance is a recording in memory, not provider acceptance. The
implementation and deterministic tests establish the listed contract and wire
behavior; real-browser display still needs the [manual verification](testing.md#verifying-a-browser).

| Driver | Browser subscription input | Device token input | Requires `key.Signing` | External submission | Explicit no-store TTL | Delivery tracking | Submission events through sender |
| --- | --- | --- | --- | --- | --- | --- | --- |
| [Test fake](#test-fake) | Yes | Yes — synthetic input only | No | No | Yes — records resolved zero | No | Yes — opt-in in-process observations |
| [Web Push](#web-push) | Yes | No | Yes | Yes | Yes — TTL header zero | No | Yes — opt-in in-process observations |

The sender, not the driver, publishes optional [submission observations](overview.md#handling-the-result).
They contain duration and bounded error classes, not recipient or message data.
Acceptance is not delivery or display; an unknown outcome is not proof of failed
delivery. Direct driver calls publish nothing.

## Test fake

`pushtest.Fake` is included in the core module and records accepted submissions
without credentials or external resources. Choose it for unit tests, not for
delivering browser notifications.

### Connecting

Import `arkoren.dev/core/service/push` and
`arkoren.dev/core/service/push/pushtest`, then pass the fake to the ordinary sender:

```go
fake := pushtest.New()
sender := push.New(fake)
```

Construction performs no I/O and requires no start or stop hooks.

### Configuration

The fake has no construction settings. Use its failure injection methods to
exercise application policy; sender retention settings still apply normally.

### Asserting notifications

Follow the complete [submission assertion](testing.md#asserting-a-submission).
It checks both message content and the resolved raw TTL. `Sent` returns a detached
snapshot; inspecting or modifying returned maps cannot change the recording.

### Behavior and limits

Portable validation runs before failure injection and recording. The fake does
not encrypt payloads, apply Web Push's size or origin policy, emulate a browser,
persist registrations, or simulate delivery. It has no emulator or external
process to clean up.

For its methods, see [testing in the push reference](testing.md).

## Web Push

`webpushpush` sends encrypted standards-based Web Push using a persistent P-256
VAPID key and an operator contact URI. Choose it for browser-issued subscriptions;
you do not need a provider SDK account, native APNs certificate, or Apple Developer
Program membership. You do need [browser registration and display code](browser-integration.md).

### Installation

From your application's module directory, install the driver:

```sh
go get arkoren.dev/driver/webpush
```

### Connecting

Load your [persisted key](vapid-keys.md#loading-and-publishing-the-public-key)
before constructing the driver. With `identity` as the resulting `key.Signing`,
import these packages and check construction before using the sender:

```go
import (
	"arkoren.dev/core/service/push"
	"arkoren.dev/driver/webpush/webpushpush"
)

driver, err := webpushpush.New(identity, "mailto:push@example.com")
sender := push.New(driver)
```

Use a reachable operator contact as a `mailto:` address or HTTPS URI, not the
customer's notification address. `New` validates settings and snapshots the key;
it does not contact a provider or register a browser. The driver is safe for
concurrent sends and has no `Start`, `Stop`, or `Ping` lifecycle hooks. Reuse one
long-lived driver rather than constructing a new driver for each notification.
Keep the constructor's pointer; do not copy its synchronized authorization cache.
It owns its signing-scalar copy and hardened HTTP client, not the application's
original signing identity. With no lifecycle hook, it provides no explicit
credential-zeroization operation.

### Configuration

A send has a ten-second timeout by default. A shorter caller context deadline
wins. To use a three-second bound, import `time` and add this construction callback:

```go
driver, err := webpushpush.New(identity, "mailto:push@example.com", func(connection *webpushpush.Builder) {
	connection.Timeout(3 * time.Second)
})
```

Zero timeout restores ten seconds; a negative timeout fails construction. This is
a request bound, separate from the notification's offline retention TTL.

Subscription endpoints are untrusted network destinations. By default, the driver
permits `https://fcm.googleapis.com`,
`https://updates.push.services.mozilla.com`, and DNS hosts below `.push.apple.com`
at a label boundary. Lookalike suffixes do not match. HTTPS on port 443 is required;
explicit `:443` is accepted. The bare `push.apple.com` host is not the wildcard
namespace. IP literals, credentials, fragments, and noncanonical hostnames are
rejected.

If your deployment must send to another reviewed service, replace the defaults
with exact trusted origins using `AllowedOrigins`. For example, to restrict sends
to Google's origin only, add this line to the existing construction callback:

```go
connection.AllowedOrigins("https://fcm.googleapis.com")
```

The list replaces rather than extends defaults. An empty list denies all origins;
paths, queries, and wildcards are not accepted. Do not derive it from a browser's
registration payload. This setting is trusted deployment policy, not a way to
approve arbitrary customer-supplied endpoints.

> [!WARNING]
> A push endpoint is a capability URL and an SSRF input. Keep the curated policy
> unless you have reviewed a replacement. An allowed origin still must resolve
> entirely to public addresses; the driver validates DNS at dialing and connects
> to the validated address. It does not use environment proxies, follow redirects,
> or expose a custom HTTP-client bypass.

TLS still verifies the original hostname. Responses and request duration are
bounded. An internal proxy, localhost provider, or private-network emulator is
not a supported target for this driver; use the fake for offline tests.

The driver reuses a VAPID authorization for each provider origin until five
minutes before its twelve-hour expiry. This state is shared by concurrent sends
on that driver, not across replicas or separate driver instances. The cache holds
at most 256 unexpired origins; adding another origin beyond that bound fails
closed. This is one reason to keep a stable driver with a narrow origin policy,
not create a fresh signing identity or driver for each send.

### Sending browser notifications

Use the shared [order notification example](overview.md#sending-a-notification)
with the stored `push.Subscription` value. The [service worker](browser-integration.md#displaying-the-notification)
receives a flat JSON object containing `title`, optional `body`, and optional
`data`; recipient endpoints and encryption keys never enter that payload.

### Behavior and limits

The driver's actual encoded JSON must fit within 3,993 plaintext bytes. This
includes title, body, application data, JSON member names, punctuation, and
escaping; character count or body length alone is not sufficient. Encryption
metadata and the final-record delimiter bring the supported envelope to 4,096
bytes. Oversized messages fail with `push.ErrInvalidMessage` before transport.

TTL is rounded down to whole seconds. The driver's representable limit is
2,147,483,647 whole seconds; values exceeding it fail as invalid messages rather
than being silently increased. A provider can impose a smaller retention bound.
Raw `SendConfig{TTL: 0}` means no offline storage, whereas zero in a sender's
per-call `push.TTL(0)` inherits the sender default.

HTTP 201 is authoritative acceptance and returns an empty `Receipt.ID`; provider
URLs are not returned as identifiers. HTTP 404/410 map to `push.ErrNotFound`, 429
to `push.ErrRateLimited`, 400/413 to `push.ErrInvalidMessage`, and 5xx to
`push.ErrUnavailable`. Authentication and other unclassified rejections remain
typed provider errors, not unsupported-operation or expired-subscription signals.
Unrecognized successful status codes and ambiguous
transport failures are `push.ErrOutcomeUnknown`. No status triggers an automatic
retry.

Use `errors.As` with `*webpushpush.ProviderError` for a rejected response's numeric
`StatusCode` and parsed nonnegative `RetryAfter`. That metadata does not schedule
work. Provider response bodies and capability URLs are not retained in that
error. Native transport causes remain inspectable and can be sensitive; use
portable classification in general logs rather than dumping an error chain.

The driver's `fmt.Formatter` method writes `[REDACTED Web Push driver]` for `%v`,
`%+v`, and `%#v` on its constructor pointer. It does not traverse signing material,
cached authorizations, or transports. This is not a guarantee for arbitrary
serialization, reflection, invalid `fmt` verbs, copying the driver, or formatting
native/caller-owned objects. Bounded `Error()` text also does not make explicitly
unwrapped transport causes safe to format or log.

Invalidate only the registration version used for a definitive not-found send.
Do not remove registrations after authentication failures, or automatically
replay unknown outcomes. See [handling the result](overview.md#handling-the-result)
for application policy and [browser verification](testing.md#verifying-a-browser)
for the distinction between acceptance and display.

For every constructor setting and response field, see the
[Web Push API reference](https://pkg.go.dev/arkoren.dev/driver/webpush/webpushpush).
