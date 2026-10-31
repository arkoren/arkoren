// Package resendmail provides a Resend Go SDK [mail.Driver]. New requires an
// explicit API key; NewFrom borrows an existing native client with its endpoint
// and transport settings. Neither constructor sends a request or adds lifecycle
// hooks. Configure borrowed objects before concurrent use; the driver does not
// close them. A nil native client can be retained but cannot send valid messages.
//
// Send maps immutable portable messages to the SDK without changing the message.
// Its receipt confirms API acceptance, not delivery. Interrupted submissions can
// have an unknown outcome, so do not retry blindly. Native clients, provider
// diagnostics and message contents can contain secrets or personal information.
//
// Example:
//
//	driver, err := resendmail.New("api-key")
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
package resendmail
