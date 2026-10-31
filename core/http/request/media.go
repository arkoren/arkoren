package request

import (
	"net/http"

	"arkoren.dev/foundation/problem"
)

// ErrUnsupportedMediaType is returned by [Multipart] when the request is not
// multipart/form-data. It has the same type, status and title as
// contenttype.ErrUnsupportedMediaType, which this package cannot import, so
// errors.Is matches either one against the other.
//
// Example:
//
//	failure := request.ErrUnsupportedMediaType.Derive(func(details *problem.Builder) {
//		details.Detail("The request cannot be completed.")
//	})
//	_ = failure.Status()
var ErrUnsupportedMediaType = problem.New(func(details *problem.Builder) {
	details.Type("tag:arkoren.dev,2026-09:core:http/middleware/contenttype:unsupported-media-type")
	details.Title("Unsupported Media Type")
	details.Detail("The request Content-Type is not supported by this endpoint.")
	details.Status(http.StatusUnsupportedMediaType)
})
