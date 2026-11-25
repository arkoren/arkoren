package asyncapi

import (
	"io"
	"net/http"

	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/problem"
)

// Handler serves the same cached JSON as JSON, without registering a route.
// Validation errors return before headers or body are committed; write failures
// propagate to the router. HEAD sends headers only, following net/http behavior.
// Applications own authentication, routing, and broker lifecycle separately.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/asyncapi.json", asyncapi.New("Events", "1").Handler(), asyncapi.DocumentOperation)
func (document *Document) Handler() router.Handler {
	return func(writer http.ResponseWriter, request *http.Request) error {
		data, err := document.JSON()

		if err != nil {
			return err
		}

		writer.Header().Set("Content-Type", "application/json")

		if request.Method == http.MethodHead {
			return nil
		}

		written, err := writer.Write(data)

		if err == nil && written != len(data) {
			return io.ErrShortWrite
		}

		return err
	}
}

// DocumentOperation describes [Document.Handler] on the route chosen by the
// caller. It documents the 200 JSON AsyncAPI document (kept opaque here) and the
// 500 problem returned when the document is invalid, since errors are reported
// before any header or body is written. The annotation changes no handler
// behavior and registers no route; the application owns the path and access
// control.
//
// Example:
//
//	routes.Get("/asyncapi.json", document.Handler(), asyncapi.DocumentOperation)
func DocumentOperation(operation *router.OperationBuilder) {
	operation.Summary("AsyncAPI document")
	operation.Description("Returns the AsyncAPI 3.1 description of this application's messaging contracts. " +
		"The document is built once and cached, errors included. HEAD answers headers only.")
	operation.JSONResponse(http.StatusOK, router.RawSchema(`{"type":"object","readOnly":true,"required":["asyncapi","info"]}`))
	operation.Problem(problem.InternalServerError())
}
