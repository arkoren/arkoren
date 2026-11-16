package contenttype

import (
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// suffixPrefix marks an allowed-list entry that matches a structured syntax
// suffix, such as "+json" for application/problem+json.
const suffixPrefix = "+"

// ErrUnsupportedMediaType is the default error returned for a request whose
// Content-Type is missing or not allowed.
//
// Example:
//
//	failure := contenttype.ErrUnsupportedMediaType.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrUnsupportedMediaType = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/middleware/contenttype:unsupported-media-type")
	details.Title("Unsupported Media Type")
	details.Detail("The request Content-Type is not supported by this endpoint.")
	details.Status(http.StatusUnsupportedMediaType)
})

// unsafeMethods are the methods that change state and are checked even when
// the request has no body.
var unsafeMethods = []string{
	http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "QUERY",
}

// typedWriteMethods are the unsafe methods that must declare an allowed
// Content-Type even when the request has no body, because they normally carry
// a representation. DELETE and QUERY are exempt: a bodyless request is their
// ordinary form.
var typedWriteMethods = []string{
	http.MethodPost, http.MethodPut, http.MethodPatch,
}

// settings configures Content-Type enforcement.
type settings struct {
	// Allowed lists the accepted media types. An entry such as
	// "application/json" matches that exact type; an entry starting with "+",
	// such as "+json", matches every type with that structured syntax suffix.
	// Matching ignores case and media type parameters such as charset. Empty
	// uses application/json and "+json".
	Allowed []string

	// ErrorResponse is the problem returned for a rejected request. Only the
	// complete zero value uses [ErrUnsupportedMediaType]; an absent status in a
	// nonzero problem is preserved for the problem renderer to complete.
	ErrorResponse problem.Details

	// Events receives [Rejected] for every disallowed Content-Type, regardless
	// of ErrorResponse's status. The default, nil, publishes nothing.
	Events *event.Bus
}

// Builder configures Middleware before publication. Callbacks run directly
// once, synchronously in order on one fresh draft; nil entries panic. Retaining
// the draft cannot change published settings. Methods return the same draft.
//
// Example:
//
//	r := router.New()
//	r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
//		content.Allowed("application/json")
//	}))
type Builder struct {
	// settings accumulates construction edits. Publication copies its completed
	// value so retaining the draft cannot change the component.
	settings settings
}

// Allowed lists the accepted media types. An entry such as
// "application/json" matches that exact type; an entry starting with "+",
// such as "+json", matches every type with that structured syntax suffix.
// Matching ignores case and media type parameters such as charset. Empty
// uses application/json and "+json".
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
//		content.Allowed("application/json")
//	}))
func (content *Builder) Allowed(types ...string) *Builder {
	content.settings.Allowed = slices.Clone(types)

	return content
}

// ErrorResponse selects the problem returned for a rejected request. Only the
// complete zero value uses [ErrUnsupportedMediaType]. A nonzero problem with no
// status keeps its members and receives the problem renderer's default 500;
// supply an explicit status when a different HTTP outcome is intended.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
//		content.ErrorResponse(problem.Forbidden())
//	}))
func (content *Builder) ErrorResponse(response problem.Details) *Builder {
	content.settings.ErrorResponse = response

	return content
}

// Events receives [Rejected] for every disallowed Content-Type, including when
// [Builder.ErrorResponse] changes the response status. Nil publishes nothing.
//
// The setting replaces the previous value on this construction draft.
// Publication keeps independent settings; functions and services stay shared.
//
// Example:
//
//	r := router.New()
//	r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
//		content.Events(event.New())
//	}))
func (content *Builder) Events(bus *event.Bus) *Builder {
	content.settings.Events = bus

	return content
}

// withDefaults replaces absent settings, normalizes allowed types into an owned
// list, and defaults only a completely zero problem. Missing problem members in
// a nonzero value remain the HTTP renderer's responsibility.
func (settings settings) withDefaults() settings {
	allowed := settings.Allowed

	if len(allowed) == 0 {
		allowed = []string{"application/json", suffixPrefix + "json"}
	}

	settings.Allowed = make([]string, len(allowed))

	for i, entry := range allowed {
		settings.Allowed[i] = strings.ToLower(strings.TrimSpace(entry))
	}

	if reflect.ValueOf(settings.ErrorResponse).IsZero() {
		settings.ErrorResponse = ErrUnsupportedMediaType
	}

	return settings
}

// Middleware rejects requests whose Content-Type is not allowed. The default
// response is 415 Unsupported Media Type as RFC 9457 problem details;
// [Builder.ErrorResponse] can replace it. With [Builder.Events], every rejection
// publishes [Rejected], independently of the configured response status.
//
// A request carrying a body must have an allowed media type; a missing or
// malformed Content-Type is rejected. A request without a body is judged by its
// method:
//
//   - Safe methods (GET, HEAD, OPTIONS and the like) always pass.
//   - POST, PUT and PATCH must send an allowed Content-Type even without a
//     body. A missing one is rejected, so an empty cross-site form post cannot
//     slip through by omitting or faking the header.
//   - DELETE and QUERY pass without a Content-Type, but a Content-Type they do
//     send must be allowed.
//
// Clients that send a bodyless POST, PUT or PATCH must now set a Content-Type
// such as application/json; earlier versions let that request through.
//
//	r.Use(contenttype.Middleware())
//	r.Use(contenttype.Middleware(func(content *contenttype.Builder) {
//		content.Allowed("application/json", "application/x-www-form-urlencoded")
//	}))
func Middleware(configure ...BuilderFunc) router.Middleware {
	config := newSettings(configure...)

	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			if !acceptable(request, config.Allowed) {
				// Report any parsed base type, even when media parameters are invalid.
				mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))

				config.Events.Emit(request.Context(), Rejected, RejectedEvent{
					Method:    request.Method,
					Path:      request.URL.Path,
					MediaType: mediaType,
				})

				return config.ErrorResponse
			}

			return next(writer, request)
		}
	}
}

// acceptable reports whether request satisfies the allowed media types. A
// bodyless request is exempt only when its method is safe, or when it is a
// DELETE or QUERY with no Content-Type; see [Middleware].
func acceptable(request *http.Request, allowed []string) bool {
	contentType := request.Header.Get("Content-Type")

	if request.ContentLength == 0 {
		if !slices.Contains(unsafeMethods, request.Method) {
			return true
		}

		if contentType == "" && !slices.Contains(typedWriteMethods, request.Method) {
			return true
		}
	}

	return allows(allowed, contentType)
}

// allows reports whether the Content-Type header value is one of allowed.
func allows(allowed []string, contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	if err != nil {
		return false
	}

	return slices.ContainsFunc(allowed, func(entry string) bool {
		suffix, found := strings.CutPrefix(entry, suffixPrefix)

		if found {
			return strings.HasSuffix(mediaType, suffixPrefix+suffix)
		}

		return mediaType == entry
	})
}
