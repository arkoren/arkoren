# Postmark Mail Driver

`arkoren.dev/driver/postmark/postmarkmail` implements `mail.Driver` with Postmark's HTTP API;
Postmark has no maintained official Go SDK. `New(token, configure ...func(*Builder))` requires a server token
and can override the endpoint for tests. The driver maps text and HTML bodies,
To/CC/BCC, Reply-To, regular base64 attachments, and inline Content-ID
attachments. A successful submission returns Postmark's `MessageID` as
`mail.Receipt.ID`.

For inline images, set the portable attachment's `ContentID` to a bare ID such
as `logo` and reference it as `<img src="cid:logo">` in HTML. The driver adds
`cid:` to Postmark's JSON `ContentID` without changing your message, matching
the [Postmark inline-image format](https://postmarkapp.com/developer/user-guide/send-email-with-api#inline-image-attachments).

Borrow an HTTP client with an explicit timeout:

```go
import (
    "net/http"
    "os"
    "time"

    "arkoren.dev/driver/postmark/postmarkmail"
)

driver, err := postmarkmail.New(os.Getenv("POSTMARK_TOKEN"), func(transport *postmarkmail.Builder) {
	transport.HTTPClient(&http.Client{Timeout: 10 * time.Second})
})
```

Pass `driver` to `mail.New`. Callbacks run once, synchronously in order on one
fresh draft before validation. Nil entries panic; expanded nil or empty slices
omit edits. Publication copies settings and borrows the HTTP client. Each send
uses a shallow client copy to disable redirects without modifying or closing your
client. Its transport and cookie jar remain shared and under your ownership;
configure them before concurrent use. Empty Endpoint restores the production URL;
nil HTTPClient restores a fresh client with a 30-second timeout. No `NewFrom` or
SDK accessor exists.

The driver never follows redirects, including same-origin redirects, even when
your HTTP client's `CheckRedirect` permits them. That callback is not invoked.
A redirect returns the original status and up to 4096 response-body bytes as a
submission error, with an empty receipt. Set `Builder.Endpoint` explicitly to a
trusted final URL rather than relying on a redirect to move credentials or replay
the message. Custom transports are trusted code: they receive the token and mail
contents and must not follow redirects internally.

Value formatting (`%v`, `%+v`, `%#v`) prints only `postmarkmail.Driver`; it does
not expose the server token, endpoint, or HTTP client internals. This does not
make provider errors or message contents safe to log.

Install with `go get arkoren.dev/driver/postmark` and check the constructor error
before creating the mailer. Keep custom endpoints trusted because the server
token is sent there. Provider errors can contain personal data; redact them before
logging. Success is acceptance, not delivery. See
[Postmark setup and tasks](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md#postmark)
for outbound notifications and parsed inbound email using separate Basic Auth
credentials with [postmarkwebhook](../postmarkwebhook/README.md).
