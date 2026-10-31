// Package slogmail provides a development [mail.Driver] that logs each message to
// a [log/slog.Logger] instead of sending it.
//
// The driver logs sender, recipients, subject, and body sizes at the
// configured level. Attachment bytes are never logged. Use [DetailBody] to
// include message bodies.
//
// # Example
//
//	import "arkoren.dev/core/service/mail"
//
//	driver := slogmail.New(func(mailer *slogmail.Builder) {
//		mailer.Logger(logger)
//		mailer.Detail(slogmail.DetailBody)
//	})
//	mailer := mail.New(driver, func(mailer *mail.Builder) {
//		mailer.From(mail.Address{Email: "noreply@example.com"})
//	})
//	_, err := mailer.Send(ctx, message)
//	if err != nil {
//		return err
//	}
//
// New takes Builder policies for Logger, Level and Detail. They run directly
// once in order on a fresh draft; nil entries panic and expanded nil/empty slices
// supply no edits. Publication detaches settings, not the logger. Nil Logger
// resolves slog.Default when sending. Construction logs and sends nothing.
package slogmail
