package router

import (
	"context"
	"errors"
	"net/http"

	"arkoren.dev/foundation/problem"
)

// representationHeaders describe a body the handler meant to send. They are
// removed before an error response replaces that body.
var representationHeaders = []string{
	"Content-Disposition",
	"Content-Encoding",
	"Content-Length",
	"Content-Range",
	"ETag",
	"Last-Modified",
}

// errorStatus selects the response status for err. Context errors mean the
// client went away only when the request context itself ended; otherwise a
// server-side deadline expired.
func errorStatus(request *http.Request, err error) int {
	if target, ok := errors.AsType[HTTPStatusError](err); ok {
		return target.HTTPStatus()
	}

	if request.Context().Err() != nil {
		return StatusClientClosedRequest
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}

	return http.StatusInternalServerError
}

// handleError renders an error using [HTTPHandlerError] or Problem Details.
// Errors that carry no HTTP meaning, such as a raw validate.Errors, render as
// 500; map them first with request.ValidationProblem or the errormap
// middleware.
func handleError(writer http.ResponseWriter, request *http.Request, err error) {
	for _, header := range representationHeaders {
		writer.Header().Del(header)
	}

	if target, ok := errors.AsType[HTTPHandlerError](err); ok {
		target.ServeHTTP(writer, request)

		return
	}

	response := problem.New(func(details *problem.Builder) {
		details.Error(err)
		details.Status(errorStatus(request, err))
	})

	response.ServeHTTP(writer, request)
}
