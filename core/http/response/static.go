package response

import (
	"bytes"
	"encoding/xml"
	htmltemplate "html/template"
	"net/http"
	"net/url"
	"strings"
	"text/template"

	"arkoren.dev/foundation/problem"
)

// ErrUnsafeRedirect is returned by [SafeRedirect] when the target URL is not
// a safe relative path.
//
// Example:
//
//	failure := response.ErrUnsafeRedirect.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrUnsafeRedirect = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/response:unsafe-redirect")
	details.Title("Invalid redirect target")
	details.Detail("The redirect target must be a safe relative path.")
	details.Status(http.StatusBadRequest)
})

// Raw writes raw byte data to the response writer with the specified status
// code. If no Content-Type header has been explicitly set, it defaults to
// application/octet-stream to ensure a safe content type is always present.
// A failed write is returned after commitment; already accepted bytes are not
// undone. The input is used during the call and is not retained.
//
// Example:
//
//	w.Header().Set("Content-Type", "image/png")
//	return response.Raw(w, http.StatusOK, png)
func Raw(writer http.ResponseWriter, status int, data []byte) error {
	if writer.Header().Get("Content-Type") == "" {
		writer.Header().Set("Content-Type", "application/octet-stream")
	}

	writer.WriteHeader(status)

	_, err := writer.Write(data)

	return err
}

// Status sets the HTTP status code for the response without writing any body
// content. This is useful for responses that communicate only a status.
// The response is committed even though no body bytes are written.
//
// Example:
//
//	return response.Status(w, http.StatusNoContent)
func Status(writer http.ResponseWriter, status int) error {
	writer.WriteHeader(status)

	return nil
}

// Bytes writes binary content as application/octet-stream, replacing any
// existing Content-Type. It follows [Raw]'s commitment and write-error rules.
//
// Example:
//
//	return response.Bytes(w, http.StatusOK, []byte("download"))
func Bytes(writer http.ResponseWriter, status int, data []byte) error {
	writer.Header().Set("Content-Type", "application/octet-stream")

	return Raw(writer, status, data)
}

// String writes text with Content-Type text/plain; charset=utf-8, replacing any
// existing Content-Type. It does not validate or transform the supplied bytes
// and follows [Raw]'s write-error behavior.
//
// Example:
//
//	return response.String(w, http.StatusOK, "pong")
func String(writer http.ResponseWriter, status int, data string) error {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")

	return Raw(writer, status, []byte(data))
}

// StringTemplate executes a text/template template into a buffer before writing
// it. Execution failures leave the response uncommitted. Successful execution
// sets Content-Type to text/plain; charset=utf-8, replacing any existing value,
// and follows [Raw]'s commitment and write-error behavior.
//
// Example:
//
//	page, err := template.New("greeting").Parse("Hello, {{.}}")
//	if err != nil {
//		return err
//	}
//	return response.StringTemplate(w, http.StatusOK, page, "reader")
func StringTemplate(writer http.ResponseWriter, status int, responseTemplate *template.Template, data any) error {
	var buffer bytes.Buffer

	if err := responseTemplate.Execute(&buffer, data); err != nil {
		return err
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")

	return Raw(writer, status, buffer.Bytes())
}

// HTML writes trusted HTML with Content-Type text/html; charset=utf-8. It does
// not escape input; use [HTMLTemplate] for user-controlled template data.
// It follows [Raw]'s commitment and write-error behavior.
//
// Example:
//
//	return response.HTML(w, http.StatusOK, "<p>Ready</p>")
func HTML(writer http.ResponseWriter, status int, data string) error {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")

	return Raw(writer, status, []byte(data))
}

// HTMLTemplate executes an escaping html/template template into a buffer before
// writing it. Execution failures leave the response uncommitted. Successful
// execution sets Content-Type to text/html; charset=utf-8, replacing any existing
// value, and follows [Raw]'s commitment and write-error behavior.
//
// Example:
//
//	page, err := htmltemplate.New("greeting").Parse("<p>Hello, {{.}}</p>")
//	if err != nil {
//		return err
//	}
//	return response.HTMLTemplate(w, http.StatusOK, page, "reader")
func HTMLTemplate(writer http.ResponseWriter, status int, responseTemplate *htmltemplate.Template, data any) error {
	var buffer bytes.Buffer

	if err := responseTemplate.Execute(&buffer, data); err != nil {
		return err
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")

	return Raw(writer, status, buffer.Bytes())
}

// XML serializes and writes XML followed by a newline.
// Encoding finishes before commitment; an encoding failure leaves the response
// uncommitted. Successful encoding sets Content-Type to application/xml and
// follows [Raw]'s write-error behavior.
//
// Example:
//
//	type result struct {
//		Status string `xml:"status"`
//	}
//	return response.XML(w, http.StatusOK, result{Status: "ready"})
func XML(writer http.ResponseWriter, status int, data any) error {
	encoded, err := xml.Marshal(data)

	if err != nil {
		return err
	}

	writer.Header().Set("Content-Type", "application/xml")

	return Raw(writer, status, append(encoded, '\n'))
}

// Redirect writes a redirect status and Location header.
// It does not validate target. Use [SafeRedirect] for user-controlled values;
// callers choose the redirect status and this helper writes no response body.
//
// Example:
//
//	return response.Redirect(w, http.StatusSeeOther, "/dashboard")
func Redirect(writer http.ResponseWriter, status int, target string) error {
	writer.Header().Set("Location", target)

	return Status(writer, status)
}

// ValidateRedirectTarget returns [ErrUnsafeRedirect] unless target is a safe
// relative path. It prevents open redirects when target originates from user
// input, such as a query parameter or form field.
// Only a path starting with exactly one slash is accepted. Backslashes, schemes,
// hosts, and URL parse failures are rejected without writing a response.
//
// Example:
//
//	if err := response.ValidateRedirectTarget(next); err != nil {
//		return err
//	}
//	return response.Redirect(w, http.StatusSeeOther, next)
func ValidateRedirectTarget(target string) error {
	if !isRelativePath(target) {
		return ErrUnsafeRedirect
	}

	return nil
}

// SafeRedirect sends an HTTP redirect response only if the target is a safe
// relative path.
// Rejection returns [ErrUnsafeRedirect] before commitment; success follows
// [Redirect]. The path must start with one slash and contain no backslashes.
//
// Example:
//
//	return response.SafeRedirect(w, http.StatusSeeOther, request.QueryOr(r, "next", "/"))
func SafeRedirect(writer http.ResponseWriter, status int, target string) error {
	if err := ValidateRedirectTarget(target); err != nil {
		return err
	}

	return Redirect(writer, status, target)
}

// isRelativePath validates that a URL is a safe relative path
// and not an absolute URL, protocol-relative URL, javascript:
// URI, or data: URI that could be used in an open redirect attack.
func isRelativePath(target string) bool {
	// Browsers treat a backslash as a slash, so `/\evil.com` means `//evil.com`.
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, "\\") {
		return false
	}

	parsed, err := url.Parse(target)

	if err != nil {
		return false
	}

	return isRelativeURL(parsed)
}

// isRelativeURL reports whether a parsed URL lacks a scheme and host.
func isRelativeURL(parsed *url.URL) bool {
	return parsed.Scheme == "" && parsed.Host == ""
}
