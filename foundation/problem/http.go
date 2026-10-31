package problem

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/textproto"
	"strings"

	"arkoren.dev/foundation/problem/internal"
)

// maxHTTPStatus is net/http's largest accepted three-digit status. Rendering
// guards this transport limit without normalizing stored RFC 9457 identities.
const maxHTTPStatus = 999

// textContentType is both the negotiated text representation and its emitted
// Content-Type. Keeping its charset here prevents negotiation/encoding drift.
const textContentType = "text/plain; charset=utf-8"

// writeHeaders replaces the response header fields supplied by headers, except
// Content-Type, which is always selected by the response renderer.
func writeHeaders(destination, headers http.Header) {
	for name, values := range headers {
		name = textproto.CanonicalMIMEHeaderKey(name)

		if name == "Content-Type" {
			continue
		}

		destination.Del(name)

		if len(values) > 0 {
			destination[name] = append([]string(nil), values...)
		}
	}
}

// renderedProblem stores one complete response prepared before commitment.
// Keeping status with its representation makes ordinary and 406 responses use
// the same commit boundary without consulting or mutating the stored problem.
type renderedProblem struct {
	// status is the final transport code, including negotiated fallback codes.
	status int

	// contentType is the renderer-owned response media type.
	contentType string

	// body is the complete response body.
	body []byte
}

// jsonEncoding caches the JSON representation for equivalent JSON renderers.
type jsonEncoding struct {
	// body is the successfully marshaled JSON response.
	body []byte

	// err records a JSON marshaling failure.
	err error

	// prepared reports whether marshaling has already been attempted.
	prepared bool
}

// marshal prepares the JSON response once for every JSON representation.
func (encoding *jsonEncoding) marshal(problem Details) ([]byte, error) {
	if encoding.prepared {
		return encoding.body, encoding.err
	}

	encoding.body, encoding.err = json.Marshal(problem)
	encoding.prepared = true

	if encoding.err != nil {
		return nil, encoding.err
	}

	encoding.body = append(encoding.body, '\n')

	return encoding.body, nil
}

// representation prepares media's response without committing response headers.
func (problem Details) representation(
	media string,
	encoding *jsonEncoding,
) (renderedProblem, bool) {
	switch media {
	case "application/problem+json", "application/json":
		body, err := encoding.marshal(problem)

		if err != nil {
			return renderedProblem{}, false
		}

		return renderedProblem{
			status:      problem.data.Status,
			contentType: media,
			body:        body,
		}, true
	case textContentType:
		body := fmt.Appendf(nil, "%d %s\n\n%s\n", problem.data.Status, problem.data.Title, problem.data.Detail)

		return renderedProblem{
			status:      problem.data.Status,
			contentType: textContentType,
			body:        []byte(body),
		}, true
	default:
		return renderedProblem{}, false
	}
}

// writeResponse applies response headers and returns a post-commit write error.
func writeResponse(
	writer http.ResponseWriter,
	contextHeaders http.Header,
	problemHeaders http.Header,
	response renderedProblem,
) error {
	writeHeaders(writer.Header(), contextHeaders)
	writeHeaders(writer.Header(), problemHeaders)
	varyAccept(writer.Header())
	writer.Header().Set("Content-Type", response.contentType)
	writer.WriteHeader(response.status)

	_, err := writer.Write(response.body)

	return err
}

// varyAccept preserves existing Vary tokens and adds Accept only when neither
// it nor the wildcard is present. It runs after user headers so negotiation
// remains cache-safe even when those headers replace a contextual Vary value.
func varyAccept(headers http.Header) {
	for token := range strings.SplitSeq(strings.Join(headers.Values("Vary"), ","), ",") {
		token = strings.TrimSpace(token)

		if token == "*" || strings.EqualFold(token, "Accept") {
			return
		}
	}

	headers.Add("Vary", "Accept")
}

// notAcceptable writes a response without the original problem representation.
func (problem Details) notAcceptable(
	writer http.ResponseWriter,
	contextHeaders http.Header,
) {
	response := renderedProblem{
		status:      http.StatusNotAcceptable,
		contentType: textContentType,
		body:        []byte(http.StatusText(http.StatusNotAcceptable) + "\n"),
	}

	if err := writeResponse(writer, contextHeaders, problem.headers, response); err != nil {
		return
	}
}

// ServeHTTP writes the problem response negotiated from request's Accept
// header. It supports application/problem+json, application/json, and
// text/plain; a valid header excluding every representation receives 406.
// It completes missing members and snapshots ResponseContext before encoding.
// A failed JSON encoding falls back only to an acceptable text representation.
// A stored status outside net/http's three-digit range (100–999) renders as
// 500 without changing the stored problem or exposing its wrapped cause.
// Media parameters must match the emitted representation; text uses UTF-8.
// Vary includes Accept unless an existing token or wildcard already covers it.
// Encoding finishes before commitment. A body-write failure stops rendering;
// this http.Handler interface cannot return that post-commit error to callers.
//
// Example:
//
//	recorder := httptest.NewRecorder()
//	problem.NotFound().ServeHTTP(recorder, httptest.NewRequest("GET", "/orders/42", nil))
//	_ = recorder.Code
func (problem Details) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	problem, contextHeaders := problem.withResponseContext(request.Context())

	if problem.data.Status < http.StatusContinue || problem.data.Status > maxHTTPStatus {
		problem.data.Status = http.StatusInternalServerError
	}

	problem = problem.Defaulted(request)
	encoding := jsonEncoding{}
	supported := []string{"application/problem+json", "application/json", textContentType}

	for _, media := range internal.ParseAccept(request).Order(supported) {
		response, rendered := problem.representation(media, &encoding)

		if !rendered {
			continue
		}

		if err := writeResponse(writer, contextHeaders, problem.headers, response); err != nil {
			return
		}

		return
	}

	problem.notAcceptable(writer, contextHeaders)
}
