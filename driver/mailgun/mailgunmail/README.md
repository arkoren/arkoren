# Mailgun Mail Driver

`arkoren.dev/driver/mailgun/mailgunmail` implements `mail.Driver` with the Mailgun Messages
API. `New(domain, apiKey, configure ...func(*Builder))` requires a verified sending domain and private
API key; `NewFrom(client, domain)` wraps an existing client. An
empty `Endpoint` uses the US API endpoint; set the EU endpoint explicitly for
EU-region Mailgun accounts.

Use a trusted absolute API base URL without a version path. The SDK rejects
segments such as `/v3` during construction; it does not fully validate URLs.
Other malformed endpoint errors can be deferred until `Send`, which returns
an error and an empty receipt rather than submitting the message.

Select the EU endpoint at construction:

```go
import (
    "os"

    "arkoren.dev/driver/mailgun/mailgunmail"
)

driver, err := mailgunmail.New("mg.example.com", os.Getenv("MAILGUN_API_KEY"), func(transport *mailgunmail.Builder) {
	transport.Endpoint("https://api.eu.mailgun.net")
})
```

Pass `driver` to `mail.New`. Callbacks run once, synchronously in order on one
fresh draft before validation. Nil entries panic; expanded nil or empty slices
omit edits. Retaining the draft cannot change the published endpoint. `NewFrom`
still has no settings callbacks: configure its borrowed SDK client yourself.

The driver maps text, HTML, To, CC, BCC, Reply-To, regular attachments, and
inline attachments to Mailgun SDK multipart form fields. Mailgun derives each
inline attachment's content identity from its filename, so inline `ContentID`
must equal `Filename`. The SDK selects uploaded attachment content types rather
than preserving portable `Attachment.ContentType`; it also cannot accept an
arbitrary inline content ID. A successful submission returns the Mailgun
response ID as `mail.Receipt.ID`.

Install with `go get arkoren.dev/driver/mailgun`. Check the constructor error
before wrapping the driver in `mail.New`; bound sending with a caller context
deadline. Success means acceptance, not delivery. Optional event notifications
use a separate HTTP webhook signing key; inbound Routes forms are unsupported.
See [Mailgun setup and tasks](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md#mailgun)
and the [webhook adapter](../mailgunwebhook/README.md).
