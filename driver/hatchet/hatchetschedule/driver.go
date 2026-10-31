package hatchetschedule

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	"github.com/hatchet-dev/hatchet/sdks/go/features"

	"arkoren.dev/core/service/schedule"
	hatchethost "arkoren.dev/driver/hatchet"
)

// cronAPI is the part of the Hatchet crons client the driver uses.
type cronAPI interface {
	// Create registers a cron trigger for the named workflow.
	Create(ctx context.Context, workflowName string, cron features.CreateCronTrigger) (*rest.CronWorkflows, error)
	// List returns cron triggers matching the native filter options.
	List(ctx context.Context, opts rest.CronWorkflowListParams) (*rest.CronWorkflowsList, error)
	// Delete removes the cron trigger identified by cronID.
	Delete(ctx context.Context, cronID string) error
}

// scheduleAPI is the part of the Hatchet schedules client the driver uses.
type scheduleAPI interface {
	// Create schedules a run of the named workflow.
	Create(ctx context.Context, workflowName string, trigger features.CreateScheduledRunTrigger) (*rest.ScheduledWorkflows, error)
	// Delete removes the scheduled run identified by scheduledRunID.
	Delete(ctx context.Context, scheduledRunID string) error
}

// Driver implements [schedule.Driver] on Hatchet cron triggers and scheduled runs.
// Its Run owns one dispatch lifetime; the shared Host owns the native worker.
// Construct before host startup and use schedule.New to obtain typed dispatch.
//
// Example:
//
//	driver, err := New(hatchethost.New())
//	if err != nil {
//		return err
//	}
//	scheduler := schedule.New(driver)
//	_ = scheduler
type Driver struct {
	// config retains the completed construction settings used by this instance.
	config settings
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger
	// host provides the native clients and lifecycle shared with the other
	// drivers.
	host *hatchethost.Host

	// clients returns the Hatchet clients; tests replace it.
	clients func() (cronAPI, scheduleAPI, error)

	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex
	// dispatch holds the application callback captured when Run begins; nil means
	// no callback is installed.
	dispatch schedule.Dispatch
	// ready closes when Run installs its callback. Native assignments wait here
	// instead of failing during the gap between worker startup and Run.
	ready chan struct{}
	// calls joins callbacks admitted during Run. Admission and callback removal
	// share mutex so teardown cannot race a later WaitGroup.Add.
	calls sync.WaitGroup
}

var _ schedule.Driver = (*Driver)(nil)

// New returns a driver and registers its three tasks on host. Call it before
// host.Start. It performs no I/O.
// Callbacks run once in order on one fresh [Builder]; nil entries panic.
// Published settings are independent of a retained draft; a nil host is an error.
//
// Example:
//
//	driver, err := New(hatchethost.New(), func(scheduler *Builder) {
//		scheduler.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(host *hatchethost.Host, configure ...BuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new hatchet schedule driver: nil host")
	}

	config := buildSettings(configure...)
	config.Prefix = cmp.Or(config.Prefix, DefaultPrefix)
	config.MaxRunTime = cmp.Or(config.MaxRunTime, DefaultMaxRunTime)

	if config.MaxRunTime < 0 {
		return nil, fmt.Errorf("hatchet schedule MaxRunTime must not be negative: %w", hatchethost.ErrInvalidConfig)
	}

	driver := &Driver{
		config: config,
		logger: cmp.Or(config.Logger, slog.Default()),
		host:   host,
		ready:  make(chan struct{}),
	}
	driver.clients = driver.hostClients

	registrations := []hatchethost.Registration{
		driver.register(driver.runWorkflow(), false),
		driver.register(driver.exclusiveWorkflow(), true),
		driver.register(driver.deferredWorkflow(), false),
	}

	for _, registration := range registrations {
		if err := host.Register(registration); err != nil {
			return nil, fmt.Errorf("new hatchet schedule driver: %w", err)
		}
	}

	return driver, nil
}

// runWorkflow returns the namespaced task name used for ordinary recurring
// runs.
func (driver *Driver) runWorkflow() string {
	return driver.config.Prefix + "-run"
}

// exclusiveWorkflow returns the task name whose native concurrency policy
// rejects overlapping runs.
func (driver *Driver) exclusiveWorkflow() string {
	return driver.config.Prefix + "-run-exclusive"
}

// deferredWorkflow returns the task name used for one-shot deferred jobs.
func (driver *Driver) deferredWorkflow() string {
	return driver.config.Prefix + "-deferred"
}

// Cancel deletes the scheduled run id. An unknown identifier is not an error.
// It does not cancel a job already executing; retain the identifier returned by
// Defer rather than passing an entry name or workflow execution ID.
//
// Example:
//
//	if err := driver.Cancel(ctx, scheduledID); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, id string) error {
	_, schedules, err := driver.clients()

	if err != nil {
		return fmt.Errorf("cancel %q: %w", id, err)
	}

	if err := schedules.Delete(ctx, id); err != nil && !isNotFound(err) && !isNoContent(err) {
		return fmt.Errorf("cancel %q: %w", id, err)
	}

	return nil
}

// isNotFound reports whether err says the target does not exist. The SDK
// returns plain errors that carry the HTTP status in their text.
func isNotFound(err error) bool {
	return errorMentions(err, "status 404", "not found", "parse scheduled run id")
}

// isNoContent reports whether err is the SDK rejecting the 204 No Content
// with which the engine answers a successful delete; the SDK accepts only 200.
func isNoContent(err error) bool {
	return errorMentions(err, "got status 204")
}

// isConflict reports whether err says the target already exists.
func isConflict(err error) bool {
	return errorMentions(err, "status 409", "already exists", "duplicate key", "unique constraint")
}

// errorMentions matches case-insensitive SDK error text against known response
// fragments. The caller supplies a non-nil error.
func errorMentions(err error, fragments ...string) bool {
	text := strings.ToLower(err.Error())

	for _, fragment := range fragments {
		if strings.Contains(text, fragment) {
			return true
		}
	}

	return false
}
