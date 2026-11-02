---
title: Mail Drivers
nav: Drivers
description: Configure outbound mail transports and optional provider notification or inbound receivers without confusing submission with delivery.
order: 20
summary:
  - Compare all six mail drivers and configure their credentials, transport policies, and sender wiring.
  - Understand attachment mappings, borrowed native clients, submission identifiers, and timeout limits.
  - Bind optional Resend, Mailgun, and Postmark notification or inbound receivers with separate verification credentials.
  - Capture development messages with Log, a local SMTP inbox, or an isolated SES emulator.
---

# Mail Drivers

Choose Log when development mail must not leave the process, SMTP for a relay or
local inbox, or an API driver for an existing Resend, Mailgun, Postmark, or
Amazon SES account. Provider credentials, verified senders, and account sending
limits remain provider-specific even though your application sends the same
immutable `mail.Message`.

## Compatibility Matrix

This compares Arkoren's shipped mappings and companion adapters, not the whole
vendor product or verified inbox delivery. Every driver accepts text-only,
HTML-only, alternative bodies, and To/CC/BCC groups, including BCC-only mail.
Log records those fields without rendering or sending email.

| Driver | Outbound Submission | Regular Attachments | Inline Attachments | Notification Webhooks | Inbound Email |
| --- | --- | --- | --- | --- | --- |
| [Log](#log) | No | Partial — Logs metadata, never attachment bytes. | No | No | No |
| [SMTP](#smtp) | Yes | Yes — MIME preserves media-type parameters. | Yes | No | No |
| [Resend](#resend) | Yes | Yes | Yes | Yes | Partial — Metadata only; fetch content separately. |
| [Mailgun](#mailgun) | Yes | Partial — SDK selects media types. | Partial — Content ID must equal filename; SDK selects media types. | Yes | No |
| [Postmark](#postmark) | Yes | Yes | Yes — Adds cid: only in the native API payload. | Yes | Partial — Typed bodies; attachments need a custom decoder. |
| [SES](#amazon-ses) | Yes | Yes — MIME preserves media-type parameters. | Yes | No | No |

SMTP and SES use the shared [MIME encoder](composing-messages.md#mime-encoding).
Resend and Postmark forward attachment media types to their APIs; Mailgun's SDK
selects them instead. Provider size limits still apply to encoded submissions.

## Connecting Mailers

Run each `go get` command from your application's module directory. Load secrets
at startup from your environment or [secret store](../secrets/overview.md), not
committed source. Examples use `os.Getenv` from the standard-library `os` package.
Check a driver's construction error before wrapping it; construction does not
verify remote sending permissions.

With import `arkoren.dev/core/service/mail`, connect your successfully constructed
driver to a sender verified with the chosen provider:

```go
mailer := mail.New(driver, func(sender *mail.Builder) {
	sender.From(mail.Address{Name: "Acme", Email: "notifications@acme.test"})
})
```

Use the [welcome message](overview.md#sending-mail) with this mailer and a nonnil
send context. `mail.New` returns no error; invalid sender defaults are detected
when sending. The facade and these drivers have no application lifecycle hooks.
SMTP connects per send; API requests happen at submission. SES construction loads
AWS configuration but does not call SES. Configure shared clients before concurrent
use and leave native resource cleanup with their owner.

Set a send-context deadline and a deliberate retry policy. Arkoren adds no retry
loop, but native SDKs or transports can retry, including AWS SDK defaults.
Interrupted submissions can have unknown acceptance; retrying can duplicate mail.
Direct `driver.Send` bypasses facade defaults, events, and telemetry, so the
message must already contain a valid sender.

## Provider Notifications

Outbound drivers do not register webhook receivers. The optional companion
examples below assume import `arkoren.dev/core/http/webhook`, an existing
`*router.Router` named `r` from `arkoren.dev/core/http/router`, separately loaded
receiver credentials, and typed application-owned acceptance functions. The
fragments run in a function returning `error`; both construction errors must be
checked before mounting a handler.

An acceptance function receives `context.Context` and `webhook.Delivery[T]` and
returns `error`; store or queue the verified notification according to your
application's durability requirements. Configure the mounted route's public
HTTPS URL in the provider dashboard and enable the events you bind. Receiver
construction alone does not configure the provider.

Use the canonical [provider adapter guide](../webhooks/provider-adapters.md)
for verification boundaries and [Delivery Policies](../webhooks/delivery-policies.md)
for receipt identity, retention, queued acceptance, body limits, and responses.
By default, successful acceptance answers 200, rejected authentication 400, and
ordinary authenticated envelope/decode/acceptance failures 503. Unknown
authenticated event types answer 200 without event-specific decoding or a
callback. These acknowledgements concern your receiver, not delivery of the
original email; they neither guarantee provider retries nor exactly-once effects.

## Log

Log is bundled in `arkoren.dev/core/service/mail/slogmail`. It simulates
acceptance with a generated message ID and a log record, not external delivery.
For assertions rather than inspection, use the [mail fake](../testing/fakes.md#assert-mail).

Even default summary logs disclose sender, To/CC/BCC, reply-to, subject, body
sizes, and attachment names, media types, and sizes. Use synthetic local data;
BCC privacy in delivery headers does not hide BCC from these logs.

```go
import "arkoren.dev/core/service/mail/slogmail"

driver := slogmail.New()
```

Wrap it as shown in [Connecting Mailers](#connecting-mailers). It uses
`slog.Default()` at info level. A successful send can produce no visible record
if the logger filters that level. Attachment bytes are never logged.

### Log Detail

If local debugging requires body inspection, pass an existing `*slog.Logger`
named `logger` and import `log/slog`. Body logging can expose reset links and
other credentials; enable it only for safe fixture messages:

```go
driver := slogmail.New(func(output *slogmail.Builder) {
	output.Logger(logger)
	output.Level(slog.LevelDebug)
	output.Detail(slogmail.DetailBody)
})
```

This alternative construction adds text and HTML bodies to the summary.
Omitting `Detail`, or selecting `DetailSummary`, leaves bodies out.

See the [`slogmail` API](https://pkg.go.dev/arkoren.dev/core/service/mail/slogmail).

## SMTP

Choose SMTP when you have relay access and know the server's host, port,
encryption mode, and any credentials. The standard-library transport renders
MIME and keeps BCC destinations in the envelope, not visible headers.

```sh
go get arkoren.dev/driver/smtp
```

### Relay Connection

Port 587 commonly uses STARTTLS, the required default. Construction validates
settings without connecting:

```go
import "arkoren.dev/driver/smtp/smtpmail"

driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
	transport.Username(os.Getenv("SMTP_USER"))
	transport.Password(os.Getenv("SMTP_PASSWORD"))
})
```

An empty host, zero port, unknown TLS mode, incomplete credential pair, or
credentials with plaintext transport fails construction. Empty username and
password together mean no authentication. Wrap the checked driver and send the
[welcome message](overview.md#sending-mail).

### Transport Policy

If the relay requires TLS from the first byte, commonly on port 465, use a
separate implicit-TLS construction. Import `time` for a shorter exchange bound:

```go
driver, err := smtpmail.New("smtp.example.com", 465, func(transport *smtpmail.Builder) {
	transport.TLSMode(smtpmail.TLSModeImplicit)
	transport.Username(os.Getenv("SMTP_USER"))
	transport.Password(os.Getenv("SMTP_PASSWORD"))
	transport.Timeout(10 * time.Second)
})
```

Certificates are verified against the host with TLS 1.2 minimum. Credentials use
PLAIN authentication over encryption. Required STARTTLS fails if no upgrade is
offered. `TLSModeNone` is an explicit plaintext choice only for trusted local,
unauthenticated servers; the constructor rejects credentials in that mode.

The default timeout is 30 seconds for dialing and the entire SMTP exchange.
Zero restores it; negative disables that bound, not context cancellation or an
earlier context deadline. Cancellation interrupts greeting, TLS negotiation,
authentication, envelope commands, and DATA reads or writes.

### Submission Limits

MIME encoding and its line/metadata checks happen before connection. If a relay
rejects a recipient, the error can name that address; the driver submits no DATA
for the other recipients. A successful receipt is the generated Message-ID,
not a relay-assigned delivery ID; correlate it with server logs.

Each concurrent send owns a separate connection and handshake. Cancellation
while waiting for the final DATA acknowledgement returns an empty receipt and
an error matching the context error, but acceptance can already have happened.
After DATA acknowledgement, a failed or canceled QUIT does not revoke success.
Respect relay rate limits rather than retrying every failure.

SMTP driver values print as fixed type labels through `fmt`, including copied
values. Its `Config` formatting redacts a nonempty password, but leaves username
and other fields visible. Neither protection sanitizes returned errors, messages,
or arbitrary serialization.

### Local Inbox

With Docker installed, run a disposable Mailpit inbox bound only to loopback.
It is an unauthenticated relay; do not expose these ports publicly:

```sh
docker run --detach --rm --name acme-mailpit --publish 127.0.0.1:1025:1025 --publish 127.0.0.1:8025:8025 axllent/mailpit:latest
```

Connect without credentials or TLS:

```go
driver, err := smtpmail.New("localhost", 1025, func(transport *smtpmail.Builder) {
	transport.TLSMode(smtpmail.TLSModeNone)
})
```

Check the error, wrap it, and send the shared welcome message. Open
`http://localhost:8025` to check its subject and recipient. This proves local
capture, not internet delivery. `docker stop acme-mailpit` stops the disposable
container and removes its captured messages.

See the [`smtpmail` API](https://pkg.go.dev/arkoren.dev/driver/smtp/smtpmail).

## Resend

Choose Resend with an account, sending API key, and verified sending domain.
Its SDK maps bodies, recipient groups, reply-to, and regular or inline attachments.

```sh
go get arkoren.dev/driver/resend
```

### API Connection

The key is the only driver setting; an empty key fails construction:

```go
import "arkoren.dev/driver/resend/resendmail"

driver, err := resendmail.New(os.Getenv("RESEND_API_KEY"))
```

Wrap the checked driver and send the [welcome message](overview.md#sending-mail).
The receipt ID is Resend's email identifier. The driver adds no timeout of its
own; bound sends with your context.

### Native Client

If you need HTTP transport or timeout control, configure the SDK first. With
imports `net/http`, `time`, and `github.com/resend/resend-go/v3`, and your loaded
`apiKey`:

```go
client := resend.NewCustomClient(&http.Client{Timeout: 10 * time.Second}, apiKey)
driver := resendmail.NewFrom(client)
```

`NewFrom` performs no validation; supply a nonnil initialized client. It borrows
the client and transport without copying, closing, or reconfiguring them.
`driver.Client()` returns that live SDK client. Configure it before concurrent
use; native sends bypass facade observations and validation.

If you hold only the facade, `mailer.DriverAs[*resendmail.Driver]()` returns
the first matching driver through wrappers, plus a boolean indicating a match.
Check that boolean before using `Client()`; inspection neither removes wrappers
nor transfers ownership.

### Receiving Resend Notifications

Use the optional `resendwebhook` companion from the same module for delivery,
bounce, complaint, sent, or received-email events. Follow the shared
[receiver setup](#provider-notifications). The endpoint's `whsec_` signing secret
is not the sending API key. Its suffix must be nonempty unbroken standard base64;
malformed or line-broken input fails construction with `webhook.ErrInvalid`
without exposing or repairing its bytes.

Import `arkoren.dev/driver/resend/resendwebhook`. With application-owned
`accept` of type `webhook.AcceptFunc[resendwebhook.Email]`, create the source:

```go
source, err := resendwebhook.New(signingSecret)
if err != nil {
	return err
}
```

Bind delivery events, check the receiver, then mount it on the existing router `r`:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/resend", handler)
```

Svix v1 signatures cover the exact raw body and require an attempt timestamp
within five minutes in either direction. Synchronize clocks. Missing, invalid,
stale, future, or duplicate authentication header lines fail closed; signature
errors match both `resendwebhook.ErrInvalidSignature` and `webhook.ErrRejected`.
The delivery identity is `svix-id`, not the email ID. Replays within the window
still require [receipt policy](../webhooks/delivery-policies.md#duplicate-receipts)
and idempotent effects.

For inbound metadata, configure a receiving domain and enable `email.received`
in Resend, then bind `resendwebhook.EmailReceived` to a compatible acceptance
function instead. It provides metadata, not body or attachment content; fetch
those separately through the provider API. Sender claims and fetched content
remain untrusted. Bounce and SMTP diagnostics can contain personal data.

See the [Resend mail API](https://pkg.go.dev/arkoren.dev/driver/resend/resendmail)
and [Resend webhook API](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook).

## Mailgun

Choose Mailgun with a verified sending domain and private API key. Its SDK uses
the Messages API; account region determines the endpoint.

```sh
go get arkoren.dev/driver/mailgun
```

### API Connection

US-region accounts use the default endpoint:

```go
import "arkoren.dev/driver/mailgun/mailgunmail"

driver, err := mailgunmail.New("mg.acme.test", os.Getenv("MAILGUN_API_KEY"))
```

Both domain and key are required. For an EU account, use this alternative
construction before wrapping the driver:

```go
driver, err := mailgunmail.New("mg.acme.test", os.Getenv("MAILGUN_API_KEY"),
	func(transport *mailgunmail.Builder) {
		transport.Endpoint("https://api.eu.mailgun.net")
	})
```

Supply a trusted absolute API base URL without a version suffix such as `/v3`.
The SDK rejects version-path segments at construction but does not fully validate
URL syntax; other malformed URLs can fail at send time.

For a preconfigured `*mailgun.Client` from `github.com/mailgun/mailgun-go/v5`,
use `driver, err := mailgunmail.NewFrom(client, domain)`. The client carries key
and endpoint but not the sending domain. Nil client or empty domain fails
construction. The driver borrows the client without copying or closing it;
`driver.Client()` exposes it. Configure HTTP and retry policies before sharing
it, and bound sends with a context deadline.

### Attachment Mapping

Use the [welcome send](overview.md#sending-mail) or the
[inline image example](composing-messages.md#inline-images). Mailgun requires
`ContentID` to equal `Filename`, such as `logo.png`, with HTML `cid:logo.png`.
A mismatch fails before submission. The SDK chooses attachment media types;
it does not preserve `Attachment.ContentType`. The receipt is Mailgun's response ID.

### Receiving Mailgun Notifications

Use the same module's `mailgunwebhook` for event webhooks, not inbound Routes
forms or Alerts. Prepare the account's HTTP webhook signing key, not the sending
API key. Empty keys, whitespace, or control characters fail with
`webhook.ErrInvalid`; keys are not trimmed, repaired, or quoted in errors.

Mailgun signs only `timestamp + token`, **not event data**. Serve HTTPS and apply
provider IP restrictions at a trusted edge where possible. A captured valid token
can accompany altered event data within the freshness window; the HMAC is not
proof of payload integrity.

Import `arkoren.dev/driver/mailgun/mailgunwebhook` and
`github.com/mailgun/mailgun-go/v5/events`. With `accept` of type
`webhook.AcceptFunc[events.Delivered]`, create the source:

```go
source, err := mailgunwebhook.New(signingKey)
if err != nil {
	return err
}
```

Bind it using the [shared receiver prerequisites](#provider-notifications):

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
	receiver.Handle(mailgunwebhook.Delivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/mailgun", handler)
```

The companion covers accepted, delivered, failed, opened, clicked, unsubscribed,
and complained events. For `Failed`, inspect native `Severity`; temporary and
permanent failures both use key `failed`, not dashboard name `permanent_fail`.
Delivery IDs combine the event's UTC date and ID because IDs are unique only
within a day.

Signature freshness defaults to thirty minutes in either direction, separately
from event age. If delayed attempts need a wider window, construct the source
with a callback calling `signature.Tolerance(time.Hour)` on
`*mailgunwebhook.Builder` (`time` is the standard-library package). Zero restores
thirty minutes; negative fails construction. Subaccounts need their own signing
key; `parent-signature` is not substituted. There is no token cache.

See the [Mailgun mail API](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunmail)
and [Mailgun webhook API](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook).

## Postmark

Choose Postmark with a server token and confirmed sender signature or domain.
The driver calls its HTTP API directly, without an official Go SDK.

```sh
go get arkoren.dev/driver/postmark
```

### API Connection

An empty server token fails construction; remote permissions are checked only
when submitting:

```go
import "arkoren.dev/driver/postmark/postmarkmail"

driver, err := postmarkmail.New(os.Getenv("POSTMARK_TOKEN"))
```

Wrap it and use the [welcome send](overview.md#sending-mail). The receipt contains
Postmark's `MessageID`. For [inline images](composing-messages.md#inline-images),
keep the portable `ContentID` bare. The driver adds `cid:` only in its API
payload; it does not change the immutable message or prove rendering.

### HTTP Policy

The default HTTP client has a 30-second timeout. To supply a shorter bound or
an instrumented transport, import `net/http` and `time` and use the loaded `token`:

```go
client := &http.Client{Timeout: 10 * time.Second}
driver, err := postmarkmail.New(token, func(transport *postmarkmail.Builder) {
	transport.HTTPClient(client)
})
```

The supplied client is borrowed. Each send shallow-copies its current settings
and replaces only the copy's redirect policy: every redirect is rejected,
including same-origin redirects, without invoking your `CheckRedirect` callback.
The original client is not changed or closed; its transport and cookie jar remain
shared and caller-owned. Configure them before concurrent use and trust the
transport with the token and message. Nil `HTTPClient` restores a fresh default
client. The driver has no `NewFrom` or client accessor.

`Builder.Endpoint` can replace `https://api.postmarkapp.com/email`; empty restores
it. Only use a trusted final URL: the token and message go directly there, and
redirect rejection does not make arbitrary endpoints safe. Malformed URL errors
are deferred until sending. Use synthetic credentials and messages for local
HTTP fixtures.

Responses with status 300 or higher return an empty receipt and an error with
the status and up to 4096 response-body bytes. Redirects fail without forwarding
the token or replaying the message through HTTP redirection. Redact returned
diagnostics before logging. Driver values format as fixed type labels through
`fmt`; that does not sanitize clients, errors, messages, or serialization.

### Receiving Postmark Notifications

Configure separate strong Basic Auth credentials on the Postmark webhook URL,
not the sending token. Use HTTPS, protect any proxy-to-app hop, and apply provider
IP restrictions at a trusted edge where possible. Postmark does not sign these
payloads; anyone holding the credentials can forge or replay notifications.

Import `arkoren.dev/driver/postmark/postmarkwebhook`. With `accept` of type
`webhook.AcceptFunc[postmarkwebhook.Delivery]`, create the source:

```go
source, err := postmarkwebhook.New(username, password)
if err != nil {
	return err
}
```

Bind delivery notifications and mount the receiver:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
	receiver.Handle(postmarkwebhook.Delivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/postmark", handler)
```

The companion also covers bounce, spam complaint, open, click, and subscription
changes. Delivery reports receiving-server acceptance, not inbox placement;
opens and clicks can come from automated clients. Inspect `Bounce.Inactive`
before treating a recipient as deactivated and `Subscription.SuppressSending`
for the resulting suppression state.

Outbound identity uses `X-PM-Webhook-Trace-Id`. Without it the ID is empty and
deduplication is disabled, rather than merging distinct events sharing a message
ID. Both source constructors reject empty credentials, control characters, or
a colon in the username with `webhook.ErrInvalid`. Valid credential bytes are
retained without trimming and excluded from construction errors.

### Receiving Inbound Email

Configure Postmark's inbound stream/domain to POST to a separate HTTPS endpoint
with separate Basic Auth credentials. With `acceptEmail` of type
`webhook.AcceptFunc[postmarkwebhook.Email]`, create its source:

```go
source, err := postmarkwebhook.NewInbound(inboundUser, inboundPassword)
if err != nil {
	return err
}
```

Bind the parsed inbound message. This example raises the body allowance to
10 MiB; choose a limit within your own resource budget:

```go
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
	receiver.Handle(postmarkwebhook.Inbound, acceptEmail)
	receiver.MaxBodySize(10 << 20)
})
if err != nil {
	return err
}
r.Post("/webhooks/postmark/inbound", handler)
```

Inbound JSON has no `RecordType`; the adapter assigns local key `Inbound` and
uses the received `MessageID` as delivery identity. Scope receipt storage
separately per endpoint. The typed payload includes text, HTML, extracted reply
text, and the sender's variable-format `Date` string. Omitted or nullable fields
retain zero values; validate business-required fields yourself. For attachments
or full address structures, define a custom event decoder over `Envelope.Body`.

Endpoint authentication does not authenticate the claimed sender as an
application user. Sanitize HTML before rendering and scan attachments before
use. Align the [body limit](../webhooks/delivery-policies.md#body-limits) with
ingress and queue limits; raising this setting cannot override a smaller outer
HTTP limit.

See the [Postmark mail API](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkmail)
and [Postmark webhook API](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook).

## Amazon SES

Choose SES with an AWS account, region, verified SES identity, and IAM permission
`ses:SendEmail` for that identity. The driver sends raw MIME through SES v2.
Sandbox accounts can send only to verified recipients; request production access
before relying on customer addresses.

```sh
go get arkoren.dev/driver/aws
```

### AWS Connection

Use an existing startup context `ctx` while loading the standard AWS
configuration and credential chain:

```go
import "arkoren.dev/driver/aws/awsmail"

driver, err := awsmail.New(ctx, func(transport *awsmail.Builder) {
	transport.Region("eu-west-1")
})
```

Check configuration-loading errors before creating the facade. Environment,
shared profiles, and instance/workload roles follow AWS resolution. `Region` is
the only driver setting; omitting it leaves region resolution to the chain.
Construction does not call SES or confirm permission to send.

### Native Client

For custom credentials, endpoint, HTTP policy, or retries, configure an SDK client
first. With `config` of type `aws.Config`, imports `net/http`, `time`, and
`github.com/aws/aws-sdk-go-v2/service/sesv2`:

```go
client := sesv2.NewFromConfig(config, func(options *sesv2.Options) {
	options.HTTPClient = &http.Client{Timeout: 10 * time.Second}
})
driver := awsmail.NewFrom(client)
```

`NewFrom` has no settings callbacks and neither copies nor closes the client.
`driver.Client()` returns the live SES client, or nil when wrapped around a test
double. Configure native policies before sharing; AWS SDK retry policies remain
in effect. Use the [welcome send](overview.md#sending-mail) with a verified
sender. The returned ID is SES acceptance, not delivery evidence. MIME checks
happen before the SES sending API is called.

### Local Emulator

With Docker installed, run an isolated SES emulator on loopback using fake
local-only credentials:

```sh
docker run --detach --rm --name acme-ses --publish 127.0.0.1:14569:4566 --env AWS_ACCESS_KEY_ID=000000000000 --env AWS_SECRET_ACCESS_KEY=test --env AWS_DEFAULT_REGION=us-east-1 ministackorg/ministack:1.5.8
```

Wait for `http://127.0.0.1:14569/_ministack/health` to respond. Import
`github.com/aws/aws-sdk-go-v2/aws`, `github.com/aws/aws-sdk-go-v2/credentials`,
and `github.com/aws/aws-sdk-go-v2/service/sesv2`:

```go
client := sesv2.NewFromConfig(aws.Config{
	Region: "us-east-1",
	Credentials: credentials.NewStaticCredentialsProvider("000000000000", "test", ""),
}, func(options *sesv2.Options) {
	options.BaseEndpoint = aws.String("http://127.0.0.1:14569")
})
driver := awsmail.NewFrom(client)
```

Wrap it and send the welcome message with synthetic addresses. A nonempty
receipt ID confirms emulator acceptance only. `docker stop acme-ses` removes
the disposable container and its temporary state.

See the [`awsmail` API](https://pkg.go.dev/arkoren.dev/driver/aws/awsmail).
