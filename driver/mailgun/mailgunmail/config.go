package mailgunmail

// Builder edits an owned Mailgun construction draft. The zero value keeps the
// SDK's US endpoint. New consumes the completed settings without retaining the
// draft; NewFrom instead uses the caller's already configured SDK client.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key", func(transport *mailgunmail.Builder) {
//		transport.Endpoint("https://api.eu.mailgun.net")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
type Builder struct {
	// config holds the scalar endpoint override until publication.
	config settings
}

// BuilderFunc edits one Mailgun construction draft. New runs callbacks once in
// order before validation and publishes their completed settings; nil entries
// panic. The alias accepts ordinary functions and function slices directly.
//
// Example:
//
//	var europe mailgunmail.BuilderFunc = func(transport *mailgunmail.Builder) {
//		transport.Endpoint("https://api.eu.mailgun.net")
//	}
//	driver, err := mailgunmail.New("mg.example.com", "private-key", europe)
//	if err != nil {
//		panic(err)
//	}
//	_ = mail.New(driver)
type BuilderFunc = func(*Builder)

// Endpoint replaces the API base URL and returns the same draft. Empty keeps
// the SDK's US default. New delegates to SetAPIBase, which rejects API-version
// path segments such as /v3 but does not fully validate URL syntax. Other URL
// errors can remain deferred until Send. Supply a trusted absolute API base URL;
// changing this draft later cannot reconfigure the published client.
//
// Example:
//
//	driver, err := mailgunmail.New("mg.example.com", "private-key", func(transport *mailgunmail.Builder) {
//		transport.Endpoint("https://api.eu.mailgun.net")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func (transport *Builder) Endpoint(endpoint string) *Builder {
	transport.config.endpoint = endpoint

	return transport
}
