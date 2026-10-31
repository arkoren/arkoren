# SMTP Mail Driver

`arkoren.dev/driver/smtp/smtpmail` implements `mail.Driver` using SMTP. It
requires STARTTLS by default, supports implicit TLS, and sends plaintext only
when you opt in with `transport.TLSMode(smtpmail.TLSModeNone)` for trusted local
development such as Mailpit. It serializes plain-text and HTML bodies, regular attachments, inline CID
attachments, and hides BCC addresses from message headers.

`New(host, port, configure ...func(*Builder))` requires the server host and port.
Set both `Username` and `Password` on the draft to use
SMTP PLAIN authentication with `TLSModeStartTLS` or `TLSModeImplicit`; the
constructor rejects credentials with `TLSModeNone`. `TLSModeNone` permits only
unauthenticated trusted-local plaintext delivery. `TLSModeStartTLS` (the
default) requires the server to advertise STARTTLS; `TLSModeImplicit` uses TLS
before SMTP negotiation. `Builder.Timeout` (default 30 seconds, negative disables)
bounds dialing and the exchange when the send context has no earlier deadline.
Context cancellation interrupts every network phase, including TLS negotiation
and waiting for the DATA acknowledgement, even with a negative timeout. Errors
then match `ctx.Err()` through `errors.Is`, and the receipt is empty. A server
may already have accepted DATA before cancellation; do not infer non-delivery
or retry automatically. A QUIT failure after acknowledged DATA does not revoke
acceptance.

Configure authenticated STARTTLS without making a connection:

```go
import (
    "os"

    "arkoren.dev/driver/smtp/smtpmail"
)

driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
	transport.Username(os.Getenv("SMTP_USER"))
	transport.Password(os.Getenv("SMTP_PASSWORD"))
})
```

Pass `driver` to `mail.New`. Callbacks run once, synchronously in order on one
fresh draft before validation. Nil entries panic; expanded nil or empty slices
omit edits. Settings return the same draft, and retaining it cannot change the
published driver. `Config` remains a redacted reporting record, not a constructor
input.

Value formatting (`%v`, `%+v`, `%#v`) prints only `smtpmail.Driver`, not its
credentials or connection settings. Formatting `Config` redacts its password,
including `%#v` and structured logging. Native errors and message metadata can still contain
personal information; redact them before logging.

Install with `go get arkoren.dev/driver/smtp` and check the constructor error
before wrapping it in `mail.New`. Success confirms server acceptance, not inbox
delivery. A timeout can leave acceptance unknown; blindly retrying can send twice.
See [SMTP setup, sending, and a local inbox](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md#smtp).
