package temporalschedule

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"arkoren.dev/core/service/schedule"
	"arkoren.dev/driver/temporal"
)

// ErrNotStarted reports an operation without a connected host.
// Connect the host before issuing commands;
// deferred jobs may be submitted before Run starts in the worker process.
//
// Example:
//
//	if errors.Is(err, ErrNotStarted) {
//		return fmt.Errorf("connect the Temporal host first: %w", err)
//	}
var ErrNotStarted = errors.New("schedule driver is not running")

// executionGrace is added to the run bound to form the workflow timeout.
const executionGrace = time.Minute

// input is the workflow input.
type input struct {
	// Name is the entry or task name to dispatch.
	Name string

	// Payload is the task payload; entries have none.
	Payload []byte

	// Timeout bounds the run. Zero uses settings.MaxRunTime.
	Timeout time.Duration
}

// Driver runs schedules on Temporal.
// Construct it before the shared host starts. It owns each local Run's
// dispatcher lifetime, not the host or caller-owned native client.
//
// Example:
//
//	driver, err := New(temporal.New())
//	if err != nil {
//		return err
//	}
//	_ = schedule.New(driver)
type Driver struct {
	// config is the normalized configuration.
	config settings

	// host supplies the client and the task queue.
	host *temporal.Host

	// api overrides the Temporal client in tests.
	api backend

	// mu guards the local Run lifetime and its dispatch function.
	mu sync.RWMutex

	// dispatch is the function stored by Run.
	dispatch schedule.Dispatch

	// ready wakes waiting activities when a local Run installs dispatch. It is
	// replaced when that Run exits, so later activities wait for the next Run.
	ready chan struct{}

	// runDone cancels active dispatches when their owning Run exits, including
	// reconciliation failures. It is nil while dispatch is absent.
	runDone <-chan struct{}
}

// New returns a Driver and registers its workflow and activity with host. It
// performs no I/O. Call it before [temporal.Host.Start].
// Callbacks run once in order on one fresh [Builder]; nil entries panic.
// Published settings are independent of a retained draft; a nil host is an error.
//
// Example:
//
//	driver, err := New(temporal.New(), func(scheduler *Builder) {
//		scheduler.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func New(host *temporal.Host, configure ...BuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new temporal schedule driver: nil host")
	}

	driver := &Driver{
		config: normalize(buildSettings(configure...)),
		host:   host,
		ready:  make(chan struct{}),
	}

	if err := host.Register(driver.register); err != nil {
		return nil, fmt.Errorf("register temporal schedule driver: %w", err)
	}

	return driver, nil
}

// normalize replaces zero values with defaults.
func normalize(config settings) settings {
	defaults := buildSettings()
	config.Prefix = cmp.Or(config.Prefix, defaults.Prefix)

	if config.MaxRunTime <= 0 {
		config.MaxRunTime = defaults.MaxRunTime
	}

	if config.CatchupWindow <= 0 {
		config.CatchupWindow = defaults.CatchupWindow
	}

	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	return config
}

// workflowName returns the name of the workflow.
func (driver *Driver) workflowName() string {
	return driver.config.Prefix + "-schedule"
}

// activityName returns the name of the activity.
func (driver *Driver) activityName() string {
	return driver.config.Prefix + "-schedule-run"
}

// register adds the workflow and activity to the worker.
func (driver *Driver) register(registry worker.Registry) {
	registry.RegisterWorkflowWithOptions(driver.workflow, workflow.RegisterOptions{
		Name: driver.workflowName(),
	})
	registry.RegisterActivityWithOptions(driver.activity, activity.RegisterOptions{
		Name: driver.activityName(),
	})
}

// workflow runs the activity once for one firing.
func (driver *Driver) workflow(ctx workflow.Context, in input) error {
	timeout := cmp.Or(max(in.Timeout, 0), driver.config.MaxRunTime)

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    timeout,
		ScheduleToCloseTimeout: timeout,
		RetryPolicy: &sdktemporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})

	return workflow.ExecuteActivity(ctx, driver.activityName(), in).Get(ctx, nil)
}

// activity waits for a local dispatcher, then calls it with cancellation from
// its native activity context and the owning Run. The worker's early stop
// notification is not used for active callbacks: the SDK cancels their context
// after its grace wait. The watcher is joined on return; a dispatch function
// that ignores cancellation can still block.
func (driver *Driver) activity(ctx context.Context, in input) error {
	dispatch, runDone, err := driver.waitDispatch(ctx)

	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})

	go func() {
		defer close(finished)

		select {
		case <-runDone:
		case <-ctx.Done():
		}

		cancel()
	}()
	defer func() {
		cancel()
		<-finished
	}()

	return dispatch(ctx, in.Name, in.Payload)
}

// waitDispatch keeps early native deliveries pending rather than failing a
// single-attempt job before Run is installed. Waiting ends on native activity
// cancellation or worker stop; a Run that exits before observation is skipped.
func (driver *Driver) waitDispatch(ctx context.Context) (schedule.Dispatch, <-chan struct{}, error) {
	for {
		driver.mu.RLock()
		dispatch, ready, runDone := driver.dispatch, driver.ready, driver.runDone
		driver.mu.RUnlock()

		if dispatch != nil {
			return dispatch, runDone, nil
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-activity.GetWorkerStopChannel(ctx):
			return nil, nil, context.Canceled
		case <-ready:
		}
	}
}

// bindDispatch publishes one local Run and returns its release operation.
// The derived context is canceled on every exit, not only parent cancellation,
// and the dispatcher is removed so a stopped Run cannot receive later work.
func (driver *Driver) bindDispatch(ctx context.Context, dispatch schedule.Dispatch) (func(), error) {
	if dispatch == nil {
		return nil, errors.New("run temporal schedule: nil dispatch")
	}

	driver.mu.Lock()
	defer driver.mu.Unlock()

	if driver.dispatch != nil {
		return nil, schedule.ErrRunning
	}

	ctx, cancel := context.WithCancel(ctx)
	driver.dispatch = dispatch
	driver.runDone = ctx.Done()
	close(driver.ready)

	return func() {
		cancel()

		driver.mu.Lock()
		driver.dispatch = nil
		driver.runDone = nil
		driver.ready = make(chan struct{})
		driver.mu.Unlock()
	}, nil
}

// scheduleID returns the schedule ID of the entry called name.
func (driver *Driver) scheduleID(name string) string {
	return driver.config.Prefix + "-" + name
}

// reconcile makes Temporal match entries.
func (driver *Driver) reconcile(ctx context.Context, api backend, entries []schedule.Entry) error {
	declared := make(map[string]struct{}, len(entries))

	for _, entry := range entries {
		options, err := driver.options(entry)

		if err != nil {
			return err
		}

		declared[options.ID] = struct{}{}

		err = api.createSchedule(ctx, options)

		if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			err = api.updateSchedule(ctx, options.ID, func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				current := in.Description.Schedule
				current.Spec = &options.Spec
				current.Action = options.Action
				current.Policy = &client.SchedulePolicies{
					Overlap:       options.Overlap,
					CatchupWindow: options.CatchupWindow,
				}

				return &client.ScheduleUpdate{
					Schedule: &current,
				}, nil
			})
		}

		if err != nil {
			return fmt.Errorf("reconcile schedule %s: %w", entry.Name(), err)
		}
	}

	return driver.prune(ctx, api, declared)
}

// prune deletes managed schedules that are not declared.
func (driver *Driver) prune(ctx context.Context, api backend, declared map[string]struct{}) error {
	ids, err := api.listScheduleIDs(ctx)

	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}

	for _, id := range ids {
		if !strings.HasPrefix(id, driver.config.Prefix+"-") {
			continue
		}

		if _, ok := declared[id]; ok {
			continue
		}

		if err := api.deleteSchedule(ctx, id); err != nil {
			var notFound *serviceerror.NotFound

			if errors.As(err, &notFound) {
				continue
			}

			return fmt.Errorf("delete schedule %s: %w", id, err)
		}

		driver.config.Logger.InfoContext(ctx, "deleted stale schedule", "id", id)
	}

	return nil
}

// cron converts a spec expression to Temporal's cron syntax.
func cron(spec schedule.Spec) string {
	expression := spec.Expression()

	if spec.HasSeconds() {
		// Temporal's six-field form puts the year last, so add a year field.
		return expression + " *"
	}

	return expression
}

// overlap maps the overlap setting of an entry to a Temporal policy.
func overlap(entry schedule.Entry) enumspb.ScheduleOverlapPolicy {
	if !entry.Overlapping() {
		return enumspb.SCHEDULE_OVERLAP_POLICY_SKIP
	}

	return enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL
}

// options maps an entry to Temporal schedule options.
func (driver *Driver) options(entry schedule.Entry) (client.ScheduleOptions, error) {
	if err := entry.Err(); err != nil {
		return client.ScheduleOptions{}, err
	}

	zone := entry.Spec().Location().String()

	if zone == "Local" {
		return client.ScheduleOptions{}, fmt.Errorf("%w: %s: the Local time zone", schedule.ErrUnsupported, entry.Name())
	}

	id := driver.scheduleID(entry.Name())
	timeout := cmp.Or(entry.TimeoutValue(), driver.config.MaxRunTime)

	return client.ScheduleOptions{
		ID: id,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{cron(entry.Spec())},
			TimeZoneName:    zone,
		},
		Action: &client.ScheduleWorkflowAction{
			ID:       id,
			Workflow: driver.workflowName(),
			Args: []any{input{
				Name:    entry.Name(),
				Timeout: entry.TimeoutValue(),
			}},
			TaskQueue:                driver.host.TaskQueue(),
			WorkflowExecutionTimeout: timeout + executionGrace,
		},
		Overlap:       overlap(entry),
		CatchupWindow: driver.config.CatchupWindow,
	}, nil
}

// deferJob implements Defer over api.
func (driver *Driver) deferJob(ctx context.Context, api backend, job schedule.Job) (string, error) {
	options := client.StartWorkflowOptions{
		ID:         driver.config.Prefix + "-deferred-" + rand.Text(),
		TaskQueue:  driver.host.TaskQueue(),
		StartDelay: max(time.Until(job.At), 0),
	}

	id, err := api.startWorkflow(ctx, options, driver.workflowName(), input{
		Name:    job.Name,
		Payload: job.Payload,
	})

	if err != nil {
		return "", fmt.Errorf("defer %s: %w", job.Name, err)
	}

	return id, nil
}

// cancel implements Cancel over api.
func (driver *Driver) cancel(ctx context.Context, api backend, id string) error {
	err := api.terminateWorkflow(ctx, id, "canceled")

	var notFound *serviceerror.NotFound

	if err != nil && !errors.As(err, &notFound) {
		return fmt.Errorf("cancel %s: %w", id, err)
	}

	return nil
}
