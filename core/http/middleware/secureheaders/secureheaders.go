package secureheaders

import (
	"net/http"

	"arkoren.dev/core/http/router"
)

// settings configures which security headers are
// set by the [Middleware] middleware. Each field maps to a standard HTTP
// security header; an empty field omits its header. [Middleware] starts from
// recommended defaults, with HSTS omitted until explicitly configured.
type settings struct {
	// ContentTypeOptions controls the X-Content-Type-Options
	// header, which prevents MIME-type sniffing.
	ContentTypeOptions string

	// FrameOptions controls the X-Frame-Options header, which
	// protects against clickjacking by restricting iframe embedding.
	FrameOptions string

	// ReferrerPolicy controls the Referrer-Policy header, which
	// limits referrer information sent with requests.
	ReferrerPolicy string

	// XSSProtection controls the X-XSS-Protection header.
	// The default value "0" disables the legacy XSS auditor,
	// which is the current OWASP recommendation. The auditor
	// has been removed from modern browsers and can introduce
	// XSS vulnerabilities when enabled.
	XSSProtection string

	// StrictTransportSecurity controls the Strict-Transport-Security
	// header (HSTS), which forces HTTPS for subsequent requests. It is sent
	// only on HTTPS requests, including those a proxy marks with
	// "X-Forwarded-Proto: https". The default is empty, omitting HSTS until
	// the application deliberately selects its deployment policy.
	StrictTransportSecurity string

	// ContentSecurityPolicy controls the Content-Security-Policy
	// header, which mitigates XSS and data-injection attacks. The default only
	// forbids framing, matching FrameOptions; extend it for your application
	// or set it to an empty string to omit it.
	ContentSecurityPolicy string

	// PermissionsPolicy controls the Permissions-Policy header,
	// which restricts browser feature access (camera, mic, etc.). Set it to an
	// empty string to omit it.
	PermissionsPolicy string
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
// Construction starts with recommended security headers, except HSTS, which is
// opt-in through StrictTransportSecurity. Explicit empty values omit their
// headers and are never replaced by later default normalization.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.FrameOptions("")
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// ContentTypeOptions controls the X-Content-Type-Options
// header, which prevents MIME-type sniffing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.ContentTypeOptions("")
//	}))
func (headers *Builder) ContentTypeOptions(value string) *Builder {
	headers.settings.ContentTypeOptions = value

	return headers
}

// FrameOptions controls the X-Frame-Options header, which
// protects against clickjacking by restricting iframe embedding.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.FrameOptions("")
//	}))
func (headers *Builder) FrameOptions(value string) *Builder {
	headers.settings.FrameOptions = value

	return headers
}

// ReferrerPolicy controls the Referrer-Policy header, which
// limits referrer information sent with requests.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.ReferrerPolicy("")
//	}))
func (headers *Builder) ReferrerPolicy(value string) *Builder {
	headers.settings.ReferrerPolicy = value

	return headers
}

// XSSProtection controls the X-XSS-Protection header.
// The default value "0" disables the legacy XSS auditor,
// which is the current OWASP recommendation. The auditor
// has been removed from modern browsers and can introduce
// XSS vulnerabilities when enabled.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.XSSProtection("")
//	}))
func (headers *Builder) XSSProtection(value string) *Builder {
	headers.settings.XSSProtection = value

	return headers
}

// StrictTransportSecurity controls the Strict-Transport-Security
// header (HSTS), which forces HTTPS for subsequent requests. It is sent
// only on HTTPS requests, including those a proxy marks with
// "X-Forwarded-Proto: https". HSTS is omitted by default; applications must
// deliberately enable it after establishing their HTTPS deployment policy.
// An empty value omits the header but does not clear a browser's cached policy.
// Add includeSubDomains only when every subdomain serves HTTPS, and preload
// only after reviewing browser preload requirements and removal constraints.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.StrictTransportSecurity("max-age=86400")
//	}))
func (headers *Builder) StrictTransportSecurity(value string) *Builder {
	headers.settings.StrictTransportSecurity = value

	return headers
}

// ContentSecurityPolicy controls the Content-Security-Policy
// header, which mitigates XSS and data-injection attacks. The default only
// forbids framing, matching FrameOptions; extend it for your application
// or set it to an empty string to omit it.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.ContentSecurityPolicy("")
//	}))
func (headers *Builder) ContentSecurityPolicy(value string) *Builder {
	headers.settings.ContentSecurityPolicy = value

	return headers
}

// PermissionsPolicy controls the Permissions-Policy header,
// which restricts browser feature access (camera, mic, etc.). Set it to an
// empty string to omit it.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware(func(headers *secureheaders.Builder) {
//		headers.PermissionsPolicy("")
//	}))
func (headers *Builder) PermissionsPolicy(value string) *Builder {
	headers.settings.PermissionsPolicy = value

	return headers
}

// defaultConfig returns recommended header values without a persistent HTTPS
// policy. HSTS stays empty so deployments must opt in explicitly.
func defaultConfig() settings {
	return settings{
		ContentTypeOptions:    "nosniff",
		FrameOptions:          "DENY",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		XSSProtection:         "0",
		ContentSecurityPolicy: "frame-ancestors 'none'",
	}
}

// Middleware returns middleware that sets HTTP security response
// headers from the configuration. Headers with empty values are skipped.
// Recommended framing, content-type, referrer, and legacy XSS headers are
// enabled by default; HSTS requires explicit StrictTransportSecurity policy.
// Skipping a header does not remove a value set by another handler or proxy.
//
// Example:
//
//	r := router.New()
//	r.Use(secureheaders.Middleware())
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			setConfiguredHeader(writer, "X-Content-Type-Options", config.ContentTypeOptions)
			setConfiguredHeader(writer, "X-Frame-Options", config.FrameOptions)
			setConfiguredHeader(writer, "Referrer-Policy", config.ReferrerPolicy)
			setConfiguredHeader(writer, "X-XSS-Protection", config.XSSProtection)

			if secure(request) {
				setConfiguredHeader(writer, "Strict-Transport-Security", config.StrictTransportSecurity)
			}

			setConfiguredHeader(writer, "Content-Security-Policy", config.ContentSecurityPolicy)
			setConfiguredHeader(writer, "Permissions-Policy", config.PermissionsPolicy)

			return next(writer, request)
		}
	}
}

// secure reports whether request arrived over HTTPS. Browsers ignore HSTS on
// plain HTTP, so trusting a spoofed X-Forwarded-Proto has no effect.
func secure(request *http.Request) bool {
	return request.TLS != nil || request.Header.Get("X-Forwarded-Proto") == "https"
}

// setConfiguredHeader writes value when it is configured and nonempty.
func setConfiguredHeader(writer http.ResponseWriter, name string, value string) {
	if value != "" {
		writer.Header().Set(name, value)
	}
}
