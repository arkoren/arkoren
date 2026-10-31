package request

import "net/http"

// HasHeader reports whether the first value for key is nonempty. A present
// header with an empty first value reports false, matching [HeaderOr]'s fallback
// rule rather than testing map-key presence.
//
// Example:
//
//	if request.HasHeader(r, "Accept-Language") {
//		language := request.Header(r, "Accept-Language")
//		_ = language
//	}
func HasHeader(r *http.Request, key string) bool {
	return Header(r, key) != ""
}

// Header returns the first value associated with the given header key from the
// request. It returns an empty string if the header is not present.
// Header names use the standard http.Header lookup rules; values are not split
// into comma-separated tokens or otherwise normalized.
//
// Example:
//
//	return response.String(w, http.StatusOK, request.Header(r, "Accept-Language"))
func Header(r *http.Request, key string) string {
	return r.Header.Get(key)
}

// HeaderOr returns the first value for the given header key, falling back to
// the provided default value if the header is missing or empty.
// Only the first value participates; later header entries do not replace an
// empty first value.
//
// Example:
//
//	language := request.HeaderOr(r, "Accept-Language", "en")
//	return response.String(w, http.StatusOK, language)
func HeaderOr(r *http.Request, key string, fallback string) string {
	if value := Header(r, key); value != "" {
		return value
	}

	return fallback
}

// HeaderValues returns all values associated with the given header key from the
// request. It returns nil if the header is not present.
// Like http.Header.Values, the returned slice aliases the request's values;
// copy it before mutation if the original header must remain unchanged.
//
// Example:
//
//	values := request.HeaderValues(r, "Accept-Language")
//	return response.JSON(w, http.StatusOK, values)
func HeaderValues(r *http.Request, key string) []string {
	return r.Header.Values(key)
}
