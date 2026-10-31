package request

import "net/http"

// Cookie retrieves the named cookie from the HTTP request using
// [http.Request.Cookie]. It returns nil if the cookie does not exist.
// The first parsed cookie with that name wins; malformed cookie entries are
// handled by net/http rather than surfaced as request input problems here.
//
// Example:
//
//	if cookie := request.Cookie(r, "theme"); cookie != nil {
//		return response.String(w, http.StatusOK, cookie.Value)
//	}
//	return response.Status(w, http.StatusNoContent)
func Cookie(r *http.Request, name string) *http.Cookie {
	if cookie, err := r.Cookie(name); err == nil {
		return cookie
	}

	return nil
}

// CookieValue retrieves the value of the named cookie from the HTTP request.
// It returns an empty string if the cookie does not exist.
// An empty cookie value and a missing cookie therefore have the same result;
// use [Cookie] when the distinction matters.
//
// Example:
//
//	return response.String(w, http.StatusOK, request.CookieValue(r, "theme"))
func CookieValue(r *http.Request, name string) string {
	if cookie := Cookie(r, name); cookie != nil {
		return cookie.Value
	}

	return ""
}

// CookieValueOr retrieves the value of the named cookie, falling back to the
// provided default value if the cookie is missing or empty.
// It follows [Cookie]'s first-match rule and does not validate application values.
//
// Example:
//
//	theme := request.CookieValueOr(r, "theme", "light")
//	return response.String(w, http.StatusOK, theme)
func CookieValueOr(r *http.Request, name string, fallback string) string {
	if value := CookieValue(r, name); value != "" {
		return value
	}

	return fallback
}
