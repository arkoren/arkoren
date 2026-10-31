// Package smtpmail provides an SMTP [mail.Driver].
//
// Connections use STARTTLS by default ([TLSModeStartTLS]); [TLSModeImplicit]
// uses TLS from the first byte. Plaintext needs an explicit
// [Builder.TLSMode] with [TLSModeNone], limited to unauthenticated trusted-local
// delivery such as a Mailpit development server. Credentials require an
// encrypted mode. Config remains a redacted reporting record, not an input.
//
// [Builder.Timeout] (30 seconds by default) bounds dialing and the SMTP
// exchange when the context passed to Send has no earlier deadline.
// Cancellation also interrupts established-connection I/O, even with a disabled
// timeout. A canceled DATA acknowledgement leaves acceptance unknown; retrying
// can duplicate mail. QUIT failures after acknowledged DATA do not revoke success.
// New runs construction callbacks once in order before validating the combined
// settings, and copies the draft before publication. Nil callback entries panic.
// Formatting Driver never discloses credentials; Config formatting redacts its
// password. Neither guarantee sanitizes provider errors or message contents.
//
// Example:
//
//	driver, err := smtpmail.New("smtp.example.com", 587, func(transport *smtpmail.Builder) {
//		transport.Username("user")
//		transport.Password("secret")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver // Pass to mail.New; Send opens each connection.
package smtpmail
