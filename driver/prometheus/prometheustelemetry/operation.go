package prometheustelemetry

import (
	"net/http"

	"arkoren.dev/core/http/router"
)

// Operation describes [Driver.Handler] on the scrape route chosen by the
// caller. It documents the text exposition (and OpenMetrics, when the scraper
// asks for it) at 200 together with the failure statuses the handler can
// return: 500 (plain text from the Prometheus handler) when no metrics can be
// served, and the problem documents [ErrNoGatherer] (501) and
// [ErrInvalidRegistry] (503).
//
// The metrics expose operational data. The annotation neither configures nor
// infers authentication, changes no handler behavior and registers no route:
// protect the scrape route with the application's middleware.
//
// Example:
//
//	routes.Get("/metrics", router.HTTP(driver.Handler()), prometheustelemetry.Operation)
func Operation(operation *router.OperationBuilder) {
	text := router.RawSchema(`{"type":"string"}`)

	operation.Summary("Prometheus metrics")
	operation.Description("Serves the application's metrics in the Prometheus text exposition format, " +
		"or OpenMetrics when the scraper's Accept header asks for it. The response is gzip-compressed " +
		"when the scraper accepts it. Metrics that fail to gather are logged and left out of an otherwise " +
		"successful response. Expose this route only to the scraper.")
	operation.MediaResponse(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", text)
	operation.MediaResponse(http.StatusOK, "application/openmetrics-text; version=1.0.0; charset=utf-8", text)
	operation.ResponseHeader(http.StatusOK, "Content-Encoding", router.RawSchema(`{"type":"string","enum":["gzip"]}`))
	operation.Response(http.StatusInternalServerError, "No metrics could be served")
	operation.MediaResponse(http.StatusInternalServerError, "text/plain; charset=utf-8", text)
	operation.Problem(ErrNoGatherer)
	operation.Problem(ErrInvalidRegistry)
}
