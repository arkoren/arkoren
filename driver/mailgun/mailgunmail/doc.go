// Package mailgunmail provides a Mailgun SDK-backed [mail.Driver].
//
// New requires the sending domain and private API key, with optional endpoint
// settings supplied through Builder callbacks. Callbacks run directly once in
// order before validation; nil entries panic. NewFrom has no optional settings:
// its caller owns the SDK client's endpoint and credentials. Neither constructor
// sends network requests; Send maps portable messages to the SDK.
// Endpoint validation delegates to the SDK's version-path check; other URL
// errors can remain deferred until Send. Borrowed native clients are not closed
// or sanitized for logging, and must be configured before concurrent use.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key", func(transport *mailgunmail.Builder) {
//		transport.Endpoint("https://api.eu.mailgun.net")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver // Pass to mail.New to submit portable messages.
package mailgunmail
