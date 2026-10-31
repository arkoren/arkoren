package postmarkmail

import "net/http"

// Builder edits a Postmark construction draft. The zero value selects the
// production endpoint and a client with a 30-second timeout. New copies settings
// after all callbacks; supplied HTTP clients remain borrowed live dependencies.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token", func(transport *postmarkmail.Builder) {
//		transport.HTTPClient(&http.Client{Timeout: 10 * time.Second})
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
type Builder struct {
	// config holds scalar settings and a borrowed HTTP client until publication.
	config settings
}

// BuilderFunc edits one Postmark construction draft. New runs callbacks once in
// order before validation and copies completed settings; nil entries panic. The
// alias accepts ordinary functions and function slices without conversion.
//
// Example:
//
//	var timeout postmarkmail.BuilderFunc = func(transport *postmarkmail.Builder) {
//		transport.HTTPClient(&http.Client{Timeout: 10 * time.Second})
//	}
//	driver, err := postmarkmail.New("server-token", timeout)
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type BuilderFunc = func(*Builder)

// Endpoint replaces the send-email URL and returns the same draft. Empty uses
// https://api.postmarkapp.com/email. Like the original constructor, New does not
// validate a nonempty URL; malformed URLs fail when Send constructs its request.
//
// Example:
//
//	driver, err := postmarkmail.New("server-token", func(transport *postmarkmail.Builder) {
//		transport.Endpoint("https://api.postmarkapp.com/email")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) Endpoint(endpoint string) *Builder {
	transport.config.endpoint = endpoint

	return transport
}

// HTTPClient replaces the borrowed request client and returns the same draft.
// Nil restores a fresh client with a 30-second timeout. Send reads the client's
// current settings into a shallow copy and replaces only CheckRedirect: no
// redirects are followed and the supplied callback is not invoked. The caller's
// client is not modified or closed. Its transport and cookie jar remain shared,
// not cloned or closed; their ownership remains with the caller. Configure the
// client before concurrent use and trust its transport with credentials and mail.
//
// Example:
//
//	client := &http.Client{Timeout: 10 * time.Second}
//	driver, err := postmarkmail.New("server-token", func(transport *postmarkmail.Builder) {
//		transport.HTTPClient(client)
//	})
//	if err != nil {
//		panic(err)
//	}
//	mailer := mail.New(driver)
//	_ = mailer // The client remains yours to manage.
func (transport *Builder) HTTPClient(client *http.Client) *Builder {
	transport.config.httpClient = client

	return transport
}
