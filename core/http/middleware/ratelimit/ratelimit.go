package ratelimit

import (
	"net/http"
	"time"

	"arkoren.dev/core/http/router"
	"arkoren.dev/core/service/cache"
)

// middleware builds the limiter reading the current time from clock, so tests
// can move time.
func middleware(c *cache.Cache, config settings, clock func() time.Time) router.Middleware {
	return func(next router.Handler) router.Handler {
		return func(writer http.ResponseWriter, request *http.Request) error {
			result, err := consume(request, c, config, clock())

			if err != nil {
				return err
			}

			setHeaders(writer, config.Limit, result)

			if result.denied {
				writer.Header().Set("Retry-After", result.reset)

				if result.first {
					config.Events.Emit(request.Context(), Exceeded, ExceededEvent{
						Key:    result.key,
						Method: request.Method,
						Path:   request.URL.Path,
						Limit:  config.Limit,
						Window: config.Window,
					})
				}

				return config.ErrorResponse
			}

			return next(writer, request)
		}
	}
}
