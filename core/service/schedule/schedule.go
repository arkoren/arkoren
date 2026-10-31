package schedule

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"arkoren.dev/core/event"
	"arkoren.dev/core/internal/clientop"
	"arkoren.dev/core/internal/name"
	"arkoren.dev/core/internal/semconv"
	"arkoren.dev/core/internal/unwrap"
	"arkoren.dev/core/service/telemetry"
	"arkoren.dev/foundation/collection"
)

// settings holds completed scheduler construction state. Its pointers refer
// to shared services; copying it detaches the draft, not those services.
type settings struct {
	// Logger receives scheduler logs. A nil value uses [slog.Default].
	Logger *slog.Logger

	// Telemetry receives the span and metrics of every scheduled run. Nil uses
	// [telemetry.Default], resolved each time a run starts, so calling
	// [telemetry.SetDefault] after [New] still takes effect.
	Telemetry *telemetry.Telemetry

	// Events receives [RunFailed], [RunSkipped], [TaskDeferred], and
	// [TaskCanceled]. Nil, the default, publishes nothing.
	Events *event.Bus
}

// Builder edits scheduler construction settings. New copies the final draft;
// retaining it cannot change the published scheduler or its registrations.
//
//	scheduler := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Logger(app.Logger())
//	})
type Builder struct {
	// config contains settings, not scheduled entries or runtime state.
	config settings
}

// Logger replaces the shared logger. A final nil uses slog.Default at
// construction. It returns the same draft and performs no logging.
//
//	scheduler := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Logger(app.Logger())
//	})
func (scheduler *Builder) Logger(value *slog.Logger) *Builder {
	scheduler.config.Logger = value

	return scheduler
}

// Telemetry selects the shared instrumentation service. Nil follows
// telemetry.Default each time a run starts, rather than at construction.
//
//	scheduler := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Telemetry(app.Telemetry())
//	})
func (scheduler *Builder) Telemetry(value *telemetry.Telemetry) *Builder {
	scheduler.config.Telemetry = value

	return scheduler
}

// Events selects the shared bus for run and deferred-task outcomes. Nil
// publishes nothing. It replaces the previous bus and returns the same draft.
//
//	scheduler := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Events(app.Events())
//	})
func (scheduler *Builder) Events(value *event.Bus) *Builder {
	scheduler.config.Events = value

	return scheduler
}

// taskRunner decodes a payload and calls the task handler.
type taskRunner = func(ctx context.Context, payload []byte) error

// Scheduler registers entries and task handlers and hands them to a [Driver],
// which decides when they run. Create one with [New].
// Register before Run; manual RunNow execution is available independently.
//
//	scheduler := schedule.New(driver)
//	fmt.Println(len(scheduler.Entries()))
type Scheduler struct {
	// driver decides when work runs.
	driver Driver

	// logger receives scheduler logs.
	logger *slog.Logger

	// telemetry records scheduled runs. Nil follows [telemetry.Default].
	telemetry *telemetry.Telemetry

	// events receives [RunFailed]. Nil publishes nothing.
	events *event.Bus

	// instruments caches the telemetry instruments.
	instruments *telemetry.Instruments[clientop.Instruments]

	// mu guards the fields below.
	mu sync.Mutex

	// entries holds the registered entries in registration order.
	entries []Entry

	// tasks maps task names to their handlers.
	tasks map[string]taskRunner

	// running is set when [Scheduler.Run] starts and never cleared.
	running bool
}

// New creates a Scheduler that delegates timing to driver. When telemetry is
// enabled, every run, scheduled or started with [Scheduler.RunNow], emits one
// "schedule run" span, labeled with the entry or task name, and the
// arkoren.schedule.run.count and arkoren.schedule.run.duration metrics, and
// costs nothing otherwise. Cache, database and storage spans started with the
// run's context become children of the run span. Task payloads, error text
// and job identifiers are never recorded.
//
// Callbacks run directly once in order on one fresh draft. Nil entries panic;
// expanded nil/empty slices omit edits. The final settings are independent of
// retained drafts; loggers, telemetry and event services remain shared.
//
//	sched := schedule.New(driver, func(scheduler *schedule.Builder) {
//		scheduler.Telemetry(app.Telemetry())
//	})
func New(driver Driver, configure ...BuilderFunc) *Scheduler {
	draft := &Builder{}

	for _, configure := range configure {
		configure(draft)
	}

	config := draft.config
	config.Logger = cmp.Or(config.Logger, slog.Default())

	return &Scheduler{
		driver:      driver,
		logger:      config.Logger,
		telemetry:   config.Telemetry,
		events:      config.Events,
		instruments: telemetry.NewInstruments(clientop.Build(runNames)),
		tasks:       map[string]taskRunner{},
	}
}

// Driver returns the underlying [Driver].
// Raw operations bypass the facade's execution registration and observations.
//
//	scheduler := schedule.New(driver)
//	fmt.Println(scheduler.Driver() == driver)
func (scheduler *Scheduler) Driver() Driver {
	return scheduler.driver
}

// DriverAs returns the first driver of type T in the chain of the
// underlying driver. A driver that wraps another exposes it with an
// Unwrap() Driver method, so wrappers do not hide the concrete driver.
//
//	if driver, ok := scheduler.DriverAs[*memoryschedule.Driver](); ok {
//		fmt.Printf("memory scheduling: %T\n", driver)
//	}
func (scheduler *Scheduler) DriverAs[T any]() (T, bool) {
	return unwrap.As[T](scheduler.Driver())
}

// taken reports whether name is used by an entry or a task. The caller holds
// the lock.
func (scheduler *Scheduler) taken(entryName string) bool {
	_, deferred := scheduler.tasks[entryName]

	return deferred || slices.ContainsFunc(scheduler.entries, func(entry Entry) bool {
		return entry.Name() == entryName
	})
}

// Add registers entries. It adds all of them or none. It returns
// [ErrInvalidName] for a bad name, [ErrInvalidEntry] for any other invalid
// entry, [ErrDuplicateName] for a name that is already used, and [ErrRunning]
// after [Scheduler.Run] started. Entries in a non-UTC zone are checked for
// daylight saving transitions; see the package documentation.
//
//	if err := scheduler.Add(schedule.NewEntry("report", report, func(entry *schedule.EntryBuilder) {
//		entry.When(schedule.UTC().Hour(3))
//	})); err != nil {
//		return err
//	}
func (scheduler *Scheduler) Add(entries ...Entry) error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	if scheduler.running {
		return fmt.Errorf("add entries: %w", ErrRunning)
	}

	seen := map[string]bool{}

	for _, entry := range entries {
		if err := entry.Err(); err != nil {
			return fmt.Errorf("add entry: %w", err)
		}

		if seen[entry.Name()] || scheduler.taken(entry.Name()) {
			return fmt.Errorf("add entry %s: %w", entry.Name(), ErrDuplicateName)
		}

		seen[entry.Name()] = true
	}

	scheduler.entries = append(scheduler.entries, entries...)

	for _, entry := range entries {
		warnDST(scheduler.logger, entry)
	}

	return nil
}

// Entries returns a copy of the registered entries in registration order.
// Replacing elements in the returned slice cannot edit registrations; each
// entry and its Spec are immutable values with shared execution captures.
// The eager collection is a snapshot, not a live query; the wrapper itself
// adds no ownership or copying beyond the detached slice returned here.
//
//	limited := scheduler.Entries().Filter(func(_ int, entry schedule.Entry) bool {
//		return entry.TimeoutValue() > 0
//	})
//	for _, entry := range limited {
//		fmt.Println(entry.Name(), entry.Spec().Expression())
//	}
func (scheduler *Scheduler) Entries() collection.Slice[Entry] {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	return collection.NewSlice(slices.Clone(scheduler.entries))
}

// Handle registers fn as the handler of task. It returns [ErrInvalidName] for a
// bad task name, [ErrInvalidEntry] for a nil fn, [ErrDuplicateName]
// when the name is already used and [ErrRunning] after [Scheduler.Run]
// started.
//
//	task := schedule.NewTask[string]("remind")
//	if err := scheduler.Handle(task, func(ctx context.Context, text string) error {
//		return sendReminder(ctx, text)
//	}); err != nil {
//		return err
//	}
func (scheduler *Scheduler) Handle[T any](task Task[T], fn func(context.Context, T) error) error {
	if err := name.Identifier(task.name); err != nil {
		return fmt.Errorf("%w: task name: %w", ErrInvalidName, err)
	}

	if fn == nil {
		return fmt.Errorf("%w: handle %s: nil handler", ErrInvalidEntry, task.name)
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()

	if scheduler.running {
		return fmt.Errorf("handle %s: %w", task.name, ErrRunning)
	}

	if scheduler.taken(task.name) {
		return fmt.Errorf("handle %s: %w", task.name, ErrDuplicateName)
	}

	scheduler.tasks[task.name] = func(ctx context.Context, payload []byte) error {
		var value T

		if err := json.Unmarshal(payload, &value); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}

		return fn(ctx, value)
	}

	return nil
}

// At defers task with payload until when and returns the job identifier from
// the driver. The task must be registered with [Scheduler.Handle]
// ([ErrUnknownName] otherwise) and when must not be zero.
// Payload encoding and driver errors retain their causes. A successful result
// means accepted for later execution, not that the task ran. Driver-specific
// cancellation and durability rules apply; no retry is automatic here.
//
//	id, err := scheduler.At(ctx, Remind, reminder, due)
//	if err != nil {
//		return err
//	}
//	fmt.Println(id)
func (scheduler *Scheduler) At[T any](ctx context.Context, task Task[T], payload T, when time.Time) (string, error) {
	if when.IsZero() {
		return "", fmt.Errorf("%w: at %s: zero time", ErrInvalidEntry, task.name)
	}

	scheduler.mu.Lock()

	_, registered := scheduler.tasks[task.name]
	scheduler.mu.Unlock()

	if !registered {
		return "", fmt.Errorf("at %s: %w", task.name, ErrUnknownName)
	}

	encoded, err := json.Marshal(payload)

	if err != nil {
		return "", fmt.Errorf("at %s: marshal payload: %w", task.name, err)
	}

	id, err := scheduler.driver.Defer(ctx, Job{
		Name:    task.name,
		Payload: encoded,
		At:      when,
	})

	if err != nil {
		return "", fmt.Errorf("at %s: defer: %w", task.name, err)
	}

	scheduler.events.Emit(ctx, TaskDeferred, TaskDeferredEvent{
		Name: task.name,
		ID:   id,
		At:   when,
	})

	return id, nil
}

// After defers task with payload until delay from now. See [Scheduler.At].
// Zero or negative delay requests an execution due now or in the past; the
// driver's next opportunity determines when it actually runs.
//
//	id, err := scheduler.After(ctx, Remind, reminder, 10*time.Minute)
//	if err != nil {
//		return err
//	}
//	fmt.Println(id)
func (scheduler *Scheduler) After[T any](ctx context.Context, task Task[T], payload T, delay time.Duration) (string, error) {
	return scheduler.At(ctx, task, payload, time.Now().Add(delay))
}

// Cancel removes the deferred job with the given identifier. An unknown or
// finished identifier is not an error, and publishes [TaskCanceled] like any
// successful call, because the driver cannot say whether a job was removed.
// Driver errors are wrapped without publishing a success notification.
//
//	if err := scheduler.Cancel(ctx, id); err != nil {
//		return err
//	}
func (scheduler *Scheduler) Cancel(ctx context.Context, id string) error {
	if err := scheduler.driver.Cancel(ctx, id); err != nil {
		return fmt.Errorf("cancel %s: %w", id, err)
	}

	scheduler.events.Emit(ctx, TaskCanceled, TaskCanceledEvent{
		ID: id,
	})

	return nil
}

// Run hands the registered entries to the driver and blocks until ctx is
// canceled or the driver fails. Registration is closed once Run starts, and a
// Scheduler runs only once: a second call returns [ErrRunning].
// In-flight execution and drain semantics belong to the driver. In particular,
// memoryschedule stops admission on cancellation but waits without a drain
// timeout for detached executions to finish. No runner can forcibly stop Go code.
//
//	compose.Run(scheduler.Run)
func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler.driver == nil {
		return errors.New("run: nil driver")
	}

	scheduler.mu.Lock()

	if scheduler.running {
		scheduler.mu.Unlock()

		return fmt.Errorf("run: %w", ErrRunning)
	}

	scheduler.running = true
	entries := slices.Clone(scheduler.entries)
	scheduler.mu.Unlock()

	return scheduler.driver.Run(ctx, entries, scheduler.dispatch)
}

// RunNow runs the entry called name immediately, with the entry timeout, panic
// recovery, and logging of a scheduled run. It returns [ErrUnknownName] when
// no entry has that name.
// An application error can also wrap ErrUnknownName; the error alone does not
// distinguish failed lookup from a nested scheduling operation's failure.
// RunNow preserves caller cancellation and bypasses driver-owned overlap guards.
//
//	if err := scheduler.RunNow(ctx, "report"); err != nil {
//		return err
//	}
func (scheduler *Scheduler) RunNow(ctx context.Context, entryName string) error {
	_, err := scheduler.runNow(ctx, entryName)

	return err
}

// runNow reports whether entry lookup succeeded separately from execution.
// Diagnostic adapters can classify a missing entry without misclassifying an
// application error that happens to wrap ErrUnknownName. The error retains
// RunNow's public wrapping and identity in both cases.
func (scheduler *Scheduler) runNow(ctx context.Context, entryName string) (bool, error) {
	scheduler.mu.Lock()

	found := slices.ContainsFunc(scheduler.entries, func(entry Entry) bool {
		return entry.Name() == entryName
	})
	scheduler.mu.Unlock()

	if !found {
		return false, fmt.Errorf("run now %s: %w", entryName, ErrUnknownName)
	}

	return true, scheduler.dispatch(ctx, entryName, nil)
}

// dispatch runs the entry or task called name. It is the [Dispatch] function
// given to the driver.
func (scheduler *Scheduler) dispatch(ctx context.Context, entryName string, payload []byte) error {
	scheduler.mu.Lock()

	index := slices.IndexFunc(scheduler.entries, func(entry Entry) bool {
		return entry.Name() == entryName
	})

	runner, deferred := scheduler.tasks[entryName]

	var entry Entry

	if index >= 0 {
		entry = scheduler.entries[index]
	}

	scheduler.mu.Unlock()

	var run func(context.Context) error

	kind := semconv.ScheduleKindEntry

	switch {
	case index >= 0:
		run = entry.fn
	case deferred:
		kind = semconv.ScheduleKindTask
		run = func(ctx context.Context) error {
			return runner(ctx, payload)
		}
	default:
		return fmt.Errorf("dispatch %s: %w", entryName, ErrUnknownName)
	}

	if entry.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, entry.timeout)
		defer cancel()
	}

	ctx, op := clientop.Begin(ctx, scheduler.telemetry, scheduler.instruments, telemetry.Internal, "schedule", "run",
		telemetry.String(semconv.ScheduleEntry, entryName), telemetry.String(semconv.ScheduleKind, kind))

	logger := scheduler.logger.With(slog.String("name", entryName))
	logger.DebugContext(ctx, "schedule run started")

	started := time.Now()

	err := protect(func() error {
		return run(ctx)
	})

	duration := time.Since(started)

	switch {
	case err == nil:
		op.Label(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeOK))
		op.End(ctx, nil)
		logger.InfoContext(ctx, "schedule run finished", slog.Duration("duration", duration))
	case errors.Is(err, ErrSkipped):
		op.Label(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeSkipped))
		op.End(ctx, nil)
		logger.InfoContext(ctx, "schedule run skipped", slog.Duration("duration", duration))
		scheduler.events.Emit(ctx, RunSkipped, RunSkippedEvent{
			Name:     entryName,
			Task:     kind == semconv.ScheduleKindTask,
			Duration: duration,
		})

		return nil
	default:
		op.Label(telemetry.String(semconv.ScheduleOutcome, semconv.ScheduleOutcomeFailed))
		op.End(ctx, err, errorSentinels...)
		logger.ErrorContext(ctx, "schedule run failed", slog.Duration("duration", duration), slog.Any("error", err))
		scheduler.events.Emit(ctx, RunFailed, RunFailedEvent{
			Name:     entryName,
			Task:     kind == semconv.ScheduleKindTask,
			Duration: duration,
			Err:      err,
		})

		return fmt.Errorf("run %s: %w", entryName, err)
	}

	return nil
}

// protect runs fn and converts a panic into an error carrying the stack.
func protect(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v\n%s", errPanic, recovered, debug.Stack())
		}
	}()

	return fn()
}

// BuilderFunc edits the fresh construction draft used by [New]. Callbacks run
// synchronously once in order; retaining the draft cannot reconfigure the
// published scheduler. A supplied nil callback panics.
//
// Example:
//
//	policy := schedule.BuilderFunc(func(scheduler *schedule.Builder) {
//		scheduler.Logger(slog.Default())
//	})
//	scheduler := schedule.New(driver, policy)
type BuilderFunc = func(*Builder)
