# Resend Mail Driver

`arkoren.dev/driver/resend/resendmail` implements `mail.Driver` with the Resend v3 Go SDK.
`New(apiKey)` requires an API key. The driver maps text and HTML bodies, To/CC/BCC,
Reply-To, regular attachments, and inline CID attachments to the Resend email
API. A successful submission returns the Resend response ID as
`mail.Receipt.ID`.

Install with `go get arkoren.dev/driver/resend`. Verify your sender's domain,
check the constructor error before wrapping the driver with `mail.New`, and use
a context deadline for sends. Acceptance does not prove inbox delivery; an
interrupted send may already have been accepted, so blindly retrying can send
twice. See [Resend setup and tasks](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md#resend)
for client configuration and optional delivery/inbound metadata notifications
using [resendwebhook](../resendwebhook/README.md) with a separate signing secret.
