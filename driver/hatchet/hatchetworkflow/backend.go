package hatchetworkflow

import (
	"context"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"

	"arkoren.dev/core/service/workflow"
)

// Run statuses as the driver sees them.
const (
	// statusQueued maps engine queueing to portable Running, not completion.
	statusQueued = string(rest.V1TaskStatusQUEUED)
	// statusRunning denotes an active native task, including durable eviction.
	statusRunning = string(rest.V1TaskStatusRUNNING)
	// statusCompleted permits decoding the task's output envelope.
	statusCompleted = string(rest.V1TaskStatusCOMPLETED)
	// statusFailed permits decoding the task's failure envelope or native error.
	statusFailed = string(rest.V1TaskStatusFAILED)
	// statusCanceled has a portable cancellation outcome, independent of output.
	statusCanceled = string(rest.V1TaskStatusCANCELLED)
)

// listedRun is one row of a run listing.
type listedRun struct {
	// ID is the native identifier used to request details for this record.
	ID string
	// Created is the engine creation time used to order matching executions.
	Created time.Time

	// Workflow is the actual full workflow name returned by Hatchet, not
	// the requested list filter. An absent name cannot prove ownership.
	Workflow string
}

// runDetail is the state of one run.
type runDetail struct {
	// Status is the engine status before translation to the workflow contract.
	Status string

	// Output is the JSON output of the single task of the run.
	Output []byte

	// Input is the JSON input of the run.
	Input []byte

	// Error is the error text of the single task of the run.
	Error string
}

// backend is the part of Hatchet the driver uses. Tests replace it.
type backend interface {
	// trigger starts the workflow called name and returns the run id. A
	// collision on the idempotency key returns an error that
	// [collisionRun] recognizes.
	trigger(ctx context.Context, name string, in wire, metadata map[string]string) (string, error)

	// exists reports whether Hatchet has a workflow of this full name.
	exists(ctx context.Context, name string) (bool, error)

	// list restricts runs to the exact namespaced workflow name and the
	// idempotency key since since. Callers must still verify returned rows
	// and their input: a filter is not proof of execution identity.
	list(ctx context.Context, name, key string, since time.Time) ([]listedRun, error)

	// details returns the state of run id.
	details(ctx context.Context, id string) (runDetail, error)

	// mark triggers the marker workflow name, which holds the first value of
	// a signal, and returns the id of the run that owns the idempotency key:
	// the new run, or the existing one.
	mark(ctx context.Context, name string, in wire) (string, error)

	// cancel cancels run id.
	cancel(ctx context.Context, id string) error

	// push sends event key with payload in the filter scope.
	push(ctx context.Context, key string, payload wire, scope string) error
}

// errNoRun reports a run id the engine does not know.
var errNoRun = workflow.ErrNotFound
