// Package postmarkmail provides a Postmark [mail.Driver].
//
// New requires a server token and accepts Builder callbacks for the send-email
// endpoint and borrowed HTTP client. Callbacks run directly once in order before
// validation; nil entries panic. Publication detaches the draft's settings, not
// the live HTTP client or transport. No request is made until Send, and there
// are no lifecycle hooks or native SDK accessors.
//
// Send never follows redirects, including same-origin redirects, and returns a
// submission error with an empty receipt for responses with status 300 or higher.
// A per-send shallow client copy enforces that policy without modifying or closing
// a supplied client. Its transport and cookie jar remain shared. Trust custom
// endpoints and transports with both the server token and message contents.
// Inline attachments keep bare ContentID values in the portable message; Send
// adds Postmark's required cid: prefix on the wire. Driver value formatting
// suppresses credentials, but does not sanitize provider errors or message data.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token", func(transport *postmarkmail.Builder) {
//		transport.HTTPClient(&http.Client{Timeout: 10 * time.Second})
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver // Pass to mail.New to submit portable messages.
package postmarkmail
