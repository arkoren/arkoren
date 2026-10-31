package memoryworkflow

import (
	"log/slog"
)

// Builder edits an in-process driver's construction draft. New copies the
// completed logger selection, not the logger or the required registry.
//
//	driver := memoryworkflow.New(registry, func(driver *memoryworkflow.Builder) {
//		driver.Logger(slog.Default())
//	})
type Builder struct {
	// logger is shared; a final nil selects slog.Default in New.
	logger *slog.Logger
}

// Logger selects the shared run logger. A final nil selects slog.Default at
// construction. It returns the same draft and never invokes workflow handlers.
//
//	driver := memoryworkflow.New(registry, func(driver *memoryworkflow.Builder) {
//		driver.Logger(slog.Default())
//	})
func (driver *Builder) Logger(value *slog.Logger) *Builder {
	driver.logger = value

	return driver
}

// BuilderFunc edits the fresh draft used by [New] without executing handlers.
// Callbacks run synchronously once in order; supplied nil callbacks panic.
// Later edits through a retained draft do not replace the driver's logger.
//
// Example:
//
//	policy := memoryworkflow.BuilderFunc(func(driver *memoryworkflow.Builder) {
//		driver.Logger(slog.Default())
//	})
//	driver := memoryworkflow.New(registry, policy)
type BuilderFunc = func(*Builder)
