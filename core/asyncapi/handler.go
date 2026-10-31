package asyncapi

import (
	"io"
	"net/http"

	"arkoren.dev/core/http/router"
)

// Handler serves the same cached JSON as JSON, without registering a route.
// Validation errors return before headers or body are committed; write failures
// propagate to the router. HEAD sends headers only, following net/http behavior.
// Applications own authentication, routing, and broker lifecycle separately.
//
// Example:
//
//	routes := router.New()
//	routes.Get("/asyncapi.json", asyncapi.New("Events", "1").Handler())
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
