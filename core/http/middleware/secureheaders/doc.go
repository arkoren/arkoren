// Package secureheaders provides HTTP security response-header middleware.
// Defaults prevent framing and content sniffing, limit referrer disclosure,
// and disable the legacy XSS auditor. The default Content Security Policy
// only forbids framing; applications must tailor resource-loading restrictions.
// HSTS is omitted by default because persistent HTTPS policy belongs to the
// deployment. Enable it deliberately with Builder.StrictTransportSecurity.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware())
package secureheaders
