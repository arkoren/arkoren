package requestlog

import (
	"log/slog"
	"net/http"

	"arkoren.dev/core/http/request"
)

// observe attaches failure logging to the response lifecycle. Unknown status
// stays zero, so taking ownership of a connection does not invent an HTTP 500.
func observe(r *http.Request, logger *slog.Logger) {
	// Zero means no final status passed through the HTTP writer yet.
	status := 0

	hooks := request.Hooks(r)
	hooks.BeforeWriteHeader(func(_ http.ResponseWriter, code int) {
		status = code
	})
	hooks.AfterResponse(func(err error) {
		if err == nil && status < http.StatusInternalServerError {
			return
		}

		orDefault(logger).ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "status", status, "error", err)
	})
}
