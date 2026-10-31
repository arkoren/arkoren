// Package core is the root of the core module of Arkoren, a modular Go
// toolkit for building services. It contains no code;
// the core is split into focused packages:
//
//   - arkoren.dev/core/application coordinates lifecycle hooks, runners, events, and probes.
//   - arkoren.dev/core/http/router routes requests to error-returning handlers.
//   - arkoren.dev/core/http/server serves an http.Handler with secure defaults.
//   - arkoren.dev/core/service holds the services: contracts such as
//     arkoren.dev/core/service/cache and arkoren.dev/core/service/database,
//     which pair with independently versioned modules under
//     arkoren.dev/driver.
//
// Optional arks live under arkoren.dev/ark, and standard-library-only
// building blocks under arkoren.dev/foundation.
package core
