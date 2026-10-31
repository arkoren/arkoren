// Package engine owns in-process workflow runs, worker lifetime, and retention.
// A newly inserted run alone acquires its parent's cancellation ownership.
package engine

import (
	"context"
	"log/slog"
	"sync"

	"arkoren.dev/core/service/workflow"
)

// Engine implements [workflow.Driver] and runs the definitions of a
// [workflow.Registry] in goroutines of the process.
//
// It is single-process only: runs are lost on restart, and finished runs stay
// in memory for [workflow.WorkflowOptions.Retention], one hour when zero, and
// are then forgotten. A run with a Delay is Running while it waits. Each
// signal name buffers at most 1024 values per run; Signal then returns
// [ErrQueueFull].
// Call Stop at the acquiring owner's cleanup boundary, even after canceled
// runs: handler goroutines may still be completing cooperative cleanup.
//
//	driver := memoryworkflow.New(registry)
//	client := workflow.NewClient(driver)
//	defer func() {
//		if err := driver.Stop(ctx); err != nil {
//			logger.ErrorContext(ctx, "workflow cleanup incomplete", "error", err)
//		}
//	}()
//	_ = client.Engine()
type Engine struct {
	// registry supplies the immutable workflow and step registrations.
	registry *workflow.Registry

	// logger is shared by run-specific loggers created at start.
	logger *slog.Logger

	// root ends all runs when the driver stops.
	root context.Context

	// cancel ends root when Stop begins.
	cancel context.CancelFunc

	// wg tracks workflow execution, step attempts, and retention waits. Stop
	// joins them unless its own context expires; callbacks remain cooperative.
	wg sync.WaitGroup

	// mu guards the fields below.
	mu sync.Mutex

	// stopped prevents new runs after Stop has begun.
	stopped bool

	// runs retains active and not-yet-expired completed runs by identity.
	runs map[runKey]*run
}

// NewEngine creates one live in-process run engine without invoking handlers.
func NewEngine(registry *workflow.Registry, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}

	if registry == nil {
		// An empty registration has no declarations that could fail validation.
		registry, _ = workflow.Handlers()
	}

	root, cancel := context.WithCancel(context.Background())

	return &Engine{
		registry: registry, logger: logger, root: root, cancel: cancel,
		runs: map[runKey]*run{},
	}
}
