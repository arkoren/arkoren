// Package mailtest provides test support for [mail.Driver] implementations
// and for code that sends mail.
//
// [Fake] records messages instead of sending them and offers assertions.
// [Run] is a conformance suite that every driver must pass.
//
// # Example
//
//	import "arkoren.dev/core/service/mail"
//
//	fake := mailtest.New()
//	mailer := mail.New(fake, func(mailer *mail.Builder) {
//		mailer.From(mail.Address{Email: "noreply@example.com"})
//	})
//
//	// ... exercise code that sends mail through mailer ...
//
//	fake.AssertSentCount(t, 1)
//	fake.AssertSent(t, func(message mail.Message) bool {
//		return message.Subject() == "Welcome"
//	})
package mailtest
