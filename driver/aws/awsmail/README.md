# Amazon SES Mail Driver

`arkoren.dev/driver/aws/awsmail` implements `mail.Driver` with the Amazon SES v2 Go
SDK. It uses the AWS default credential chain and supports optional region
selection. Every valid portable message is serialized as raw RFC 5322 MIME, so
the driver supports text and HTML bodies, To/CC/BCC, Reply-To, regular
attachments, and inline CID attachments. A successful submission returns the
SES `MessageId` as `mail.Receipt.ID`.

```go
import "arkoren.dev/driver/aws/awsmail"

driver, err := awsmail.New(ctx, func(mailer *awsmail.Builder) {
	mailer.Region("eu-west-1")
})
```

`New` runs callbacks directly once, synchronously in order on one fresh draft
before loading AWS configuration. Nil entries panic; expanded nil/empty slices
omit edits. `Region` returns the draft; retaining it cannot change the completed
client. `NewFrom(client)` has no optional settings or callback; the client stays
caller-owned.

Install with `go get arkoren.dev/driver/aws`. Supply a caller context `ctx`, check
the constructor error, and wrap the driver with `mail.New`. Your identity must
be verified and IAM credentials must allow `ses:SendEmail`; sandbox accounts
also restrict recipients. Success means acceptance, not delivery. AWS SDK retry
policies remain in effect. See
[SES setup and sending](../../../cmd/arkoren/skills/arkoren/references/guides/mail/drivers.md#amazon-ses)
and the [API reference](https://pkg.go.dev/arkoren.dev/driver/aws/awsmail).
