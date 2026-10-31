---
title: Mail Drivers
nav: Drivers
description: Choose between the log, SMTP, Resend, Mailgun, Postmark, and Amazon SES mail drivers, and install, connect, and configure each.
order: 40
---

# Mail Drivers

## Introduction

Every driver accepts the same `mail.Message` and returns an acceptance receipt.
Choose **Log** for development without delivery, **SMTP** for a relay or local
inbox, or **Resend**, **Mailgun**, **Postmark**, or **Amazon SES** for your provider's
HTTP API. SES fits applications already using AWS credentials and permissions.

Use immutable `mail.NewMessage` values with required recipients and body. Drivers
validate before I/O. Only the mailer applies defaults:
direct `driver.Send` needs `MessageBuilder.From` and a valid `message.Validate()`.
See [Mail Overview](overview.md) for sending and [Choosing a
Driver](../drivers/choosing-a-driver.md) for other services.

Run installation commands from your application's module directory. Each
Connecting example creates `driver`; check its error before wrapping it with
`mail.New` and a verified sender as shown in [Creating a mailer](overview.md#creating-a-mailer).
Use imports `arkoren.dev/core/service/mail`, `os` for environment reads,
`net/http` and `time` for HTTP timeout settings, and `log/slog` for log settings.
No driver has application lifecycle hooks. Construction does not verify remote
credentials or sending permissions.

Formatting an SMTP or Postmark driver with `fmt` (`%v`, `%+v`, or `%#v`) produces
a fixed type label, for both pointers and copied values. This protects those
driver values, not borrowed clients, returned errors, messages, or arbitrary
serialization. Select safe diagnostic fields rather than dumping native objects.

## Compatibility matrix

Compare Arkoren's mappings and receivers, not vendor products or verified live
delivery. All drivers accept text-only, HTML-only, alternatives, and To/CC/BCC
groups including BCC-only mail. Log neither submits mail nor renders MIME and
discloses BCC in logs. SMTP and SES use the shared MIME encoder, preserving
attachment media-type parameters with `Filename` supplying `name`. Resend and
Postmark forward the declared media type to their APIs; Mailgun lets its SDK
select media types. Mailgun also requires inline IDs to equal filenames.
Postmark adds `cid:` to bare inline IDs only in its API payload. Resend inbound is
metadata-only; Postmark inbound attachments need a custom decoder.
SMTP and SES apply the shared [MIME encoding rules](overview.md#mime-encoding),
including physical-line and Message-ID checks, before submission.

| Driver | Outbound submission | Regular attachments | Inline attachments | Notification webhooks | Inbound email |
| --- | --- | --- | --- | --- | --- |
| [Log](#log) | No | Partial — Logs metadata only, never attachment bytes. | No | No | No |
| [SMTP](#smtp) | Yes | Yes — MIME preserves media-type parameters. | Yes | No | No |
| [Resend](#resend) | Yes | Yes | Yes | Yes | Partial — Metadata only; fetch content separately. |
| [Mailgun](#mailgun) | Yes | Partial — SDK selects media types. | Partial — Content ID must equal the filename; SDK selects media types. | Yes | No |
| [Postmark](#postmark) | Yes | Yes | Yes — Bare ContentID is mapped to a cid: reference in the API payload. | Yes | Partial — Typed bodies; attachments require a custom event decoder. |
| [SES](#amazon-ses) | Yes | Yes — MIME preserves media-type parameters. | Yes | No | No |

The facade and drivers add no retry loop; native SDK/transport policies can
still retry, including AWS SDK defaults. Timeouts can leave acceptance unknown
and retries can deliver duplicates. Set a send-context deadline and deliberate
retry policies rather than repeating every failure.

## Receiving provider notifications

For delivery, bounce, and other provider notifications, Resend, Mailgun, and
Postmark also ship independent [webhook adapters](../http-capabilities/webhooks.md).
Sending mail does not register a receiver or require a webhook endpoint.

If you need those notifications, prepare an application `*router.Router` named
`r`, receiver credentials loaded from configuration, and a typed
application-owned `accept` function that durably stores or queues each
notification and returns its error. The receiver examples below are fragments
in a function returning `error`. Import `arkoren.dev/core/http/webhook` alongside
the named vendor package and check both construction errors before mounting the
handler. Then configure the route's public HTTPS URL in the provider dashboard
and enable the events you bind.

A receiver's 200 acknowledges your application's acceptance of a notification,
not delivery of the original email. Core returns 200 after acceptance succeeds,
503 on acceptance/decode failure, and 400 on rejected authentication by default.
Unknown authenticated event types receive 200 without a callback.
Deliveries can repeat or arrive out of order; make effects idempotent. The
[webhook guide](../http-capabilities/webhooks.md) explains queueing, body limits,
and deduplication.

## Log

Log is bundled in `arkoren.dev/core`. It writes one record per message to a
`*slog.Logger` without sending mail. Use it for development or log inspection;
for assertions, use [`mailtest.Fake`](overview.md#testing).

### Connecting

With a `*slog.Logger` named `logger` and a `mail.Address` named `from`,
construct the driver and mailer. `New` never fails:

```go
import "arkoren.dev/core/service/mail/slogmail"

driver := slogmail.New(func(mailer *slogmail.Builder) {
	mailer.Logger(logger)
})
mailer := mail.New(driver, func(mailer *mail.Builder) {
	mailer.From(from)
})
```

### Configuration

Configure the logger, level, and detail. Summary logs disclose addresses and
subjects; use fixture mail. `DetailBody` also discloses reset-link credentials,
so enable it only for safe local data:

```go
driver := slogmail.New(func(mailer *slogmail.Builder) {
	mailer.Logger(logger)
	mailer.Level(slog.LevelDebug)
	mailer.Detail(slogmail.DetailBody)
})
```

Without a logger the driver uses `slog.Default()`, and the level defaults to
info. The default detail, `DetailSummary`, logs the sender, the recipients
including cc, bcc, and reply-to, the subject, the sizes of the text and HTML
bodies, and an attachment summary with each name, type, and size.
`DetailBody` adds the bodies themselves.

### Sending mail

Use the [shared sending example](overview.md#sending-a-message) with this mailer.
A successful send produces a log record if the logger enables the configured
level; it never sends email to a recipient.

### Behavior and limits

The driver validates like the real ones and returns a generated message ID.
Attachment bytes are never logged. Keep personal data and reset links out of
shared logs; this driver is for development, not production delivery.

### API references

See the [`slogmail` API](https://pkg.go.dev/arkoren.dev/core/service/mail/slogmail).

## SMTP

Choose SMTP for a relay or provider SMTP access without its API. The driver uses
the standard library and MIME: text/HTML alternatives, regular/inline attachments,
and BCC only in the envelope. Obtain relay access, host, port, security mode, and
credentials from the server operator.

### Installation

```sh
go get arkoren.dev/driver/smtp
```

### Connecting

`New` takes the host and port, validates the settings, and connects to nothing;
a connection opens for each `Send`:

```go
import "arkoren.dev/driver/smtp/smtpmail"

driver, err := smtpmail.New("smtp.example.com", 587,
	func(transport *smtpmail.Builder) {
		transport.Username(os.Getenv("SMTP_USER"))
		transport.Password(os.Getenv("SMTP_PASSWORD"))
	},
)
```

`New` fails when the host is empty, the port is zero, only one of the username
and password is set, or credentials are combined with plaintext.

### Configuration

The default requires STARTTLS and fails if the server offers no upgrade; use it
for port 587. For TLS from the first byte, usually port 465, choose implicit TLS:

```go
driver, err := smtpmail.New("smtp.example.com", 465,
	func(transport *smtpmail.Builder) {
		transport.TLSMode(smtpmail.TLSModeImplicit)
		transport.Username(user)
		transport.Password(pass)
	},
)
```

Credentials use PLAIN authentication over encryption. `smtpmail.TLSModeNone`
sends plaintext for trusted local servers; construction rejects it with
credentials to prevent password disclosure. Certificates are verified against
the supplied host, with TLS 1.2 minimum.

Construction callbacks run once in order before validation; retaining the draft
cannot change the driver.

`Builder.Timeout` bounds dialing and the whole SMTP exchange; the earlier of
this timeout and the context deadline applies. The default is 30 seconds, and
a negative value disables the driver's timeout, not context cancellation.
Cancellation and deadlines interrupt greeting, TLS negotiation, authentication,
envelope commands, and DATA reads and writes.

To bound a relay exchange to ten seconds, add this setting to the construction
callback (with import `time`):

```go
transport.Timeout(10 * time.Second)
```

`smtpmail.Config` prints a nonempty password as `[REDACTED]` through `fmt` and
`slog`; an empty password stays empty. Other reporting fields, including the
username, remain visible and can contain personal data.

### Sending mail

Wrap the checked driver in `mail.New` and use the
[shared sending example](overview.md#sending-a-message). The server's acceptance
returns the generated message ID, not an inbox-delivery confirmation.

### Behavior and limits

SMTP encodes through `mail.EncodeMIME` before opening the connection. Attachment
media-type parameters are preserved, with `Filename` overriding `name`.
Unformattable metadata or an encoded physical line over 998 bytes excluding CRLF
fails with `mail.ErrInvalidMessage` and no encoded output, before submission.
Headers and metadata are not truncated to fit; see
[MIME encoding](overview.md#mime-encoding) for line limits, related-part root types,
and Message-ID syntax.

The generated `Message-ID` needs server logs for correlation; it is not a
server-assigned ID or delivery confirmation. If the server rejects a recipient, the error names
that address and the driver submits no message to the others.

Each `Send` connects, authenticates, submits one message, and attempts QUIT.
Cancellation while awaiting the final DATA acknowledgement returns an empty
receipt and an error matching the context error, but the server might already
have accepted the message. Retrying can duplicate mail. Once DATA is acknowledged,
a failed or canceled QUIT does not revoke acceptance: `Send` still returns the
receipt with no error.

Concurrent sends are independent but cost a handshake each; respect your relay's
rate limits.

### Running locally

With Docker installed, run a disposable Mailpit inbox from any directory. Bind
both ports to loopback so other machines cannot use your unauthenticated relay:

```sh
docker run --detach --rm --name acme-mailpit --publish 127.0.0.1:1025:1025 --publish 127.0.0.1:8025:8025 axllent/mailpit:latest
```

Mailpit uses no encryption, so connect with plaintext and no credentials:

```go
driver, err := smtpmail.New("localhost", 1025,
	func(transport *smtpmail.Builder) {
		transport.TLSMode(smtpmail.TLSModeNone)
	},
)
```

Check the constructor error, wrap the driver with `mail.New`, and send the
[welcome message](overview.md#sending-a-message). Open `http://localhost:8025`
and confirm its subject and recipient. Stop the container to remove the inbox
and its messages:

```sh
docker stop acme-mailpit
```

### API references

See the [`smtpmail` API](https://pkg.go.dev/arkoren.dev/driver/smtp/smtpmail).

## Resend

Choose Resend with an account and verified sending domain. Its SDK driver maps
text/HTML, to, cc, bcc, reply-to, and regular/inline attachments to the email API.

### Installation

```sh
go get arkoren.dev/driver/resend
```

### Connecting

`New` takes the API key and fails when it is empty:

```go
import "arkoren.dev/driver/resend/resendmail"

driver, err := resendmail.New(os.Getenv("RESEND_API_KEY"))
```

### Configuration

The key is the only setting. For custom HTTP configuration, pass a
`*resend.Client` to `resendmail.NewFrom`. Set its timeout before wrapping it:

```go
import "github.com/resend/resend-go/v3"

client := resend.NewCustomClient(&http.Client{Timeout: 10 * time.Second}, apiKey)
driver := resendmail.NewFrom(client)
```

`NewFrom` does not validate the client; supply a nonnil initialized client.
The driver neither copies nor closes it. `driver.Client()` exposes the live SDK
client for native operations; do not mutate it during concurrent use.

### Sending mail

Wrap the checked driver with `mail.New` and use the
[shared sending example](overview.md#sending-a-message).

### Behavior and limits

The receipt is Resend's email identifier. The driver sets no timeout of its own, so put
a deadline on the context you pass to `Send`.

### Receiving Resend notifications

To track delivery or receive inbound email metadata, use `resendwebhook` from
the same module. Copy the endpoint's `whsec_` signing secret from Resend, not its
sending API key. The part after `whsec_` must be nonempty standard base64 without
CR or LF; the constructor rejects line-broken or malformed secrets with
`webhook.ErrInvalid`, without exposing their bytes or repairing them.
With import `arkoren.dev/driver/resend/resendwebhook` and
`accept` of type `webhook.AcceptFunc[resendwebhook.Email]`, bind delivery events:

```go
source, err := resendwebhook.New(signingSecret)
if err != nil {
	return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[resendwebhook.Envelope]) {
	receiver.Handle(resendwebhook.EmailDelivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/resend", handler)
```

The adapter also defines sent, bounced, complained, and received events. Received
events contain metadata, not fetched email bodies or attachment contents. Svix
signatures authenticate the raw body and require an attempt timestamp within
five minutes; keep clocks synchronized. `Delivery.ID` is `svix-id`, not the email
ID. Enable deduplication to reduce replays within that window. For payloads and
failure behavior, see the [Resend webhook reference](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook).

For inbound metadata, configure a receiving domain and enable `email.received`
in Resend, then replace the binding with
`receiver.Handle(resendwebhook.EmailReceived, accept)`. This records the inbound
reference only; fetch content separately through Resend's API. Sender claims
and fetched HTML/attachments remain untrusted after webhook authentication.

Invalid, missing, stale, or future signatures match both
`resendwebhook.ErrInvalidSignature` and `webhook.ErrRejected` (400 by default).
Duplicate authentication header lines fail closed. Authenticated malformed JSON
or invalid bound email data returns 503; unknown authenticated types receive
200 without email decoding. Bounce/SMTP diagnostics can contain personal data;
keep them out of routine logs and metrics.

### API references

See the [Resend mail API](https://pkg.go.dev/arkoren.dev/driver/resend/resendmail) and
[Resend webhook API](https://pkg.go.dev/arkoren.dev/driver/resend/resendwebhook).

## Mailgun

Choose Mailgun with an account, verified sending domain, and private API key.
The driver maps portable fields through the Messages API's Go SDK.

### Installation

```sh
go get arkoren.dev/driver/mailgun
```

### Connecting

`New` takes the Mailgun domain, the private API key, and optional Builder callbacks, and fails if
either value is empty:

```go
import "arkoren.dev/driver/mailgun/mailgunmail"

driver, err := mailgunmail.New("mg.acme.test", os.Getenv("MAILGUN_API_KEY"))
```

### Configuration

Mailgun runs separate US and EU regions, and the driver uses the US endpoint
unless you say otherwise. An account in the EU region must set the endpoint:

```go
driver, err := mailgunmail.New("mg.acme.test", os.Getenv("MAILGUN_API_KEY"),
	func(transport *mailgunmail.Builder) {
		transport.Endpoint("https://api.eu.mailgun.net")
	},
)
```

The SDK rejects API-version path segments such as `/v3` during construction;
it does not fully validate URL syntax. Other malformed endpoint errors can be
deferred until `Send`. A client you build yourself goes to
`mailgunmail.NewFrom(client, domain)`; the client carries the
key and endpoint but not the domain, so you pass that separately. NewFrom is
settings-free: configure the caller-owned SDK client before wrapping it.
`driver.Client()` returns the SDK client.

Use an explicit send-context deadline to bound submission; the driver adds no
retry loop. `NewFrom` borrows the client without copying or closing it;
configure timeout/transport policies before concurrent use.

### Sending mail

Use the [shared sending example](overview.md#sending-a-message). For an inline
logo, keep `ContentID: "logo.png"` and `cid:logo.png` in HTML; an ID
different from the filename fails before submission.

### Behavior and limits

Mailgun uses filenames as inline IDs; the driver rejects a mismatched
`ContentID`. For `logo.png`, use `<img src="cid:logo.png">`. The SDK chooses
attachment media types instead of preserving `ContentType`. The receipt is
Mailgun's response ID.

### Receiving Mailgun notifications

To track delivery, failures, or engagement, use the account's HTTP webhook signing
key, not its API key. The constructor rejects empty keys, whitespace, and control
characters with `webhook.ErrInvalid`; it does not trim or repair the key, and
the error does not contain its bytes.

Mailgun signs only `timestamp + token`, **not event data**.
Serve the endpoint over HTTPS and apply provider IP restrictions at a trusted
edge when possible. A captured valid token can accompany altered data within
the freshness window; do not treat the HMAC as proof of payload integrity.

Import `arkoren.dev/driver/mailgun/mailgunwebhook` and supply `accept` of type
`webhook.AcceptFunc[events.Delivered]`, where `events` is
`github.com/mailgun/mailgun-go/v5/events`:

```go
source, err := mailgunwebhook.New(signingKey)
if err != nil {
	return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[mailgunwebhook.Envelope]) {
	receiver.Handle(mailgunwebhook.Delivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/mailgun", handler)
```

Configure this route as an event webhook, not an inbound Routes form or Alert.
The adapter covers accepted, delivered, failed, opened, clicked, unsubscribed,
and complained events. The default timestamp window is thirty minutes in either
direction; adjust `Builder.Tolerance` if your retry policy needs a longer window.
Delivery IDs combine the event's UTC date and ID because event IDs are unique
only within a day. See the [Mailgun webhook reference](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook)
for payload types and failure behavior.

`Failed` uses native key `failed` for both temporary and permanent severities;
inspect `Severity`, not dashboard names such as `permanent_fail`. The signature
timestamp is separate from the event time. Zero tolerance restores thirty
minutes; negative fails construction. Subaccounts require their own signing
key; `parent-signature` is not used. Invalid authentication returns 400 by
default; authenticated malformed envelopes or incompatible bound data return
503. Unknown authenticated events receive 200 without invoking acceptance.

### API references

See the [Mailgun mail API](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunmail) and
[Mailgun webhook API](https://pkg.go.dev/arkoren.dev/driver/mailgun/mailgunwebhook).

## Postmark

Choose Postmark with a server token and confirmed sender signature or domain.
The driver calls its HTTP API directly, mapping bodies, recipients, and regular
and inline attachments; no official Go SDK is used.

### Installation

```sh
go get arkoren.dev/driver/postmark
```

### Connecting

`New` takes the server token and optional Builder callbacks, and fails when the token is empty:

```go
import "arkoren.dev/driver/postmark/postmarkmail"

driver, err := postmarkmail.New(os.Getenv("POSTMARK_TOKEN"))
```

### Configuration

`Builder.HTTPClient` supplies the borrowed HTTP client; nil restores a fresh
client with a 30-second timeout. Use it to set your own timeout, proxy, or a
transport that [traces the requests](events-and-telemetry.md#http-detail).
The driver rejects every HTTP redirect, including a redirect to another path
on the same origin. A supplied `CheckRedirect` callback is ignored, not invoked.

`Builder.Endpoint` replaces the send-email URL; empty restores
`https://api.postmarkapp.com/email`. URL errors remain deferred until `Send`.
Supply only a trusted **final endpoint**: the driver sends its server token and
message directly to that URL, and redirect rejection does not make an arbitrary
endpoint safe. Use only synthetic tokens and messages with a local test server.

To bound a request to ten seconds, pass an HTTP client in the construction
callback. Here `token` is your Postmark server token:

```go
client := &http.Client{Timeout: 10 * time.Second}
driver, err := postmarkmail.New(token,
	func(transport *postmarkmail.Builder) {
		transport.HTTPClient(client)
	},
)
```

Each send makes a shallow copy of the client's current settings and replaces
only the copy's redirect policy. Your client's settings stay unchanged and the
driver does not close it. The transport and cookie jar remain shared, not cloned
or closed; trust custom transports with the token and message.
Configure the client, transport, and jar
before concurrent use. The driver adds no retry loop.

### Sending mail

Wrap the checked driver with `mail.New` and use the
[shared sending example](overview.md#sending-a-message). The receipt contains
Postmark's `MessageID` after acceptance.

For an inline image, follow the [shared attachment example](overview.md#attachments)
with a bare `ContentID`, such as `"logo"`, and HTML `src="cid:logo"`. The driver
sends `"ContentID": "cid:logo"` in the API payload, matching
[Postmark's inline-image format](https://postmarkapp.com/developer/user-guide/send-email-with-api#inline-image-attachments).
The immutable message retains `ContentID: "logo"`; regular attachments omit the
native `ContentID` field. This mapping does not prove inbox delivery or rendering.

### Behavior and limits

A response with status 300 or higher fails the send; its error includes status
and up to 4096 response-body bytes. Redirects return that original response as
an error with an empty receipt, without forwarding the token or replaying the
message through the HTTP client's redirect mechanism. Redact sensitive
diagnostics before logging.
The driver has no `NewFrom` or client accessor; use `DriverAs` to reach the driver
itself. The receipt is Postmark's `MessageID`.

### Receiving Postmark notifications

To receive delivery and tracking notifications, configure separate strong Basic
Auth credentials on the Postmark webhook URL, not a sending API token.
**Use HTTPS:** Postmark does not sign webhook payloads, and anyone with these
credentials can forge or replay notifications. Protect the internal hop if TLS
terminates at a proxy; apply provider IP restrictions at a trusted edge where
possible.

`New` and `NewInbound` reject empty credentials, control characters, or a colon
in the username with `webhook.ErrInvalid`. Valid password bytes are retained
without trimming; the construction error does not expose credentials.

Import `arkoren.dev/driver/postmark/postmarkwebhook` and supply `accept` of type
`webhook.AcceptFunc[postmarkwebhook.Delivery]`:

```go
source, err := postmarkwebhook.New(username, password)
if err != nil {
	return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
	receiver.Handle(postmarkwebhook.Delivered, accept)
})
if err != nil {
	return err
}
r.Post("/webhooks/postmark", handler)
```

The adapter also covers bounces, complaints, opens, clicks, and subscription
changes. Outbound deduplication uses `X-PM-Webhook-Trace-Id`; if absent, it is
disabled rather than collapsing distinct events with the same message ID.

### Receiving inbound email

Configure Postmark's inbound stream/domain to POST to a separate HTTPS URL with
separate Basic Auth credentials. With `acceptEmail` of type
`webhook.AcceptFunc[postmarkwebhook.Email]`, bind the inbound payload:

```go
source, err := postmarkwebhook.NewInbound(inboundUser, inboundPassword)
if err != nil {
	return err
}
handler, err := webhook.Handler(source, func(receiver *webhook.Builder[postmarkwebhook.Envelope]) {
	receiver.Handle(postmarkwebhook.Inbound, acceptEmail)
	receiver.MaxBodySize(10 << 20)
})
if err != nil {
	return err
}
r.Post("/webhooks/postmark/inbound", handler)
```

This accepts bodies up to 10 MiB instead of core's 1 MiB default; size it for
your own resource budget. Delivery identity is the received message ID.
Sender claims are not application-user authentication. Sanitize HTML before
rendering and scan attachments before use. The typed payload includes text/HTML
and reply text; raw attachments require a custom event over `Envelope.Body`.

Inbound JSON has no `RecordType`; the adapter assigns local key `Inbound` and
retains the sender's variable-format `Date` string. Missing or ambiguous Basic
Auth is rejected (400 by default); invalid authenticated JSON or incompatible
bound fields returns 503. Omitted and nullable fields retain zero values, so
validate business-required fields yourself. Unknown outbound types receive 200
without event-specific decoding. Scope deduplication separately per endpoint.

### API references

See the [Postmark mail API](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkmail) and
[Postmark webhook API](https://pkg.go.dev/arkoren.dev/driver/postmark/postmarkwebhook).

## Amazon SES

The SES driver submits mail through Amazon SES v2. It serializes each message to
raw MIME and sends it as one raw email, which is how attachments and inline
images work. Choose it with an AWS account, region, verified SES identity, and
IAM credentials allowed to call `ses:SendEmail` for that identity.

### Installation

```sh
go get arkoren.dev/driver/aws
```

### Connecting

`New` loads the AWS configuration with the standard credential chain, so
environment variables, shared profiles, and instance or workload roles all
work. It takes a context and does not call SES:

```go
import "arkoren.dev/driver/aws/awsmail"

driver, err := awsmail.New(ctx, func(mailer *awsmail.Builder) {
	mailer.Region("eu-west-1")
})
```

### Configuration

`Builder.Region` is the only setting; without it the region comes from the default
chain. For a custom endpoint, credentials, or an HTTP client, build a
`*sesv2.Client` and pass it to `awsmail.NewFrom`. `driver.Client()` returns the
client, and is nil if you built the driver around a test double.

For a configured `aws.Config` named `config`, customize the client before
wrapping it (import `github.com/aws/aws-sdk-go-v2/service/sesv2`):

```go
client := sesv2.NewFromConfig(config, func(options *sesv2.Options) {
	options.HTTPClient = &http.Client{Timeout: 10 * time.Second}
})
driver := awsmail.NewFrom(client)
```

The driver neither copies nor closes that client. AWS SDK retry policies remain
in effect; configure them deliberately if duplicate submissions matter.

### Sending mail

Use the [shared sending example](overview.md#sending-a-message) with a verified
sender. `receipt.ID` is the SES acceptance identifier, not delivery evidence.

### Behavior and limits

SES uses `mail.EncodeMIME` before calling its sending API. Attachment media-type
parameters are preserved, with `Filename` overriding `name`. Unformattable
metadata or an encoded physical line over 998 bytes excluding CRLF fails with
`mail.ErrInvalidMessage` and no encoded output, before submission. Headers and
metadata are not truncated to fit; see [MIME encoding](overview.md#mime-encoding)
for line limits, related-part root types, and Message-ID syntax.

The sender must be a verified SES identity, and an account in the SES sandbox
can only send to verified recipients, so a send that works for your own address
and fails for a customer's is usually the sandbox. Request production access in
AWS to lift it. The receipt is the SES message ID.

### Running locally

With Docker installed, start a disposable SES emulator on loopback:

```sh
docker run --detach --rm --name acme-ses --publish 127.0.0.1:14569:4566 --env AWS_ACCESS_KEY_ID=000000000000 --env AWS_SECRET_ACCESS_KEY=test --env AWS_DEFAULT_REGION=us-east-1 ministackorg/ministack:1.5.8
```

Wait for `http://127.0.0.1:14569/_ministack/health` to respond. With imports
`github.com/aws/aws-sdk-go-v2/aws`, `github.com/aws/aws-sdk-go-v2/credentials`,
and `github.com/aws/aws-sdk-go-v2/service/sesv2`, use fake local-only credentials:

```go
client := sesv2.NewFromConfig(aws.Config{
	Region: "us-east-1",
	Credentials: credentials.NewStaticCredentialsProvider("000000000000", "test", ""),
}, func(options *sesv2.Options) {
	options.BaseEndpoint = aws.String("http://127.0.0.1:14569")
})
driver := awsmail.NewFrom(client)
```

Wrap the driver and send the [welcome message](overview.md#sending-a-message).
A nonempty receipt ID confirms emulator acceptance, not real delivery. Stop and
remove the temporary state with `docker stop acme-ses`.

### API references

For every function and builder method, see [the `awsmail` API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awsmail).
