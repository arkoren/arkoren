// Package bodylimit caps request body reads for a router or group. Declared
// oversized bodies receive 413 before the next handler runs; bodies that exceed
// the ceiling while streaming fail the handler's read. The default ceiling is
// request.DefaultMaxBodySize, also selected by a zero or negative configured limit.
//
// Nil bodies are treated as empty input, so request.JSON returns
// request.ErrInvalidJSON (400). The caller's request fields stay unchanged;
// non-nil body streams remain shared with the downstream handler.
//
// Configure a smaller ceiling before registering upload routes:
//
//	routes := router.New()
//	routes.Use(bodylimit.Middleware(func(limiter *bodylimit.Builder) {
//		limiter.Limit(1 << 20)
//	}))
//
// Request helper options and nested middleware cannot raise an existing ceiling.
// Mount routes requiring larger bodies outside that middleware.
package bodylimit
