package memoryschedule

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"arkoren.dev/core/service/schedule"
)

// ErrAlreadyRunning is returned when Run is called while another Run is active.
// The original Run retains ownership while draining executions as well as while
// scheduling timers. This driver-specific identity remains stable; the facade
// separately uses schedule.ErrRunning to reject repeated Scheduler.Run calls.
//
//	if err := driver.Run(ctx, entries, dispatch); errors.Is(err, memoryschedule.ErrAlreadyRunning) {
//		return err
//	}
var ErrAlreadyRunning = errors.New("memory schedule driver is already running")

// Driver implements [schedule.Driver] with timers in memoryschedule.
//
// It is single-process only: every replica that runs it runs every entry.
// Queued timers are discarded when Run stops; all work is lost on process exit.
// Submissions accepted while stopped or draining wait pending for a later Run.
// Construction starts no timers; one Run owns timing and drains admitted work.
//
//	driver := memoryschedule.New()
//	scheduler := schedule.New(driver)
//	compose.Run(scheduler.Run)
type Driver struct {
	// logger receives dispatch failures without changing scheduling outcomes.
	logger *slog.Logger

	// mu guards lifecycle state and the owned timer collections.
	mu sync.Mutex

	// running permits timer admission until shutdown begins. runCtx remains
	// non-nil through the subsequent drain, preventing a concurrent Run.
	running bool

	// dispatch is the scheduler callback supplied to the active Run.
	dispatch schedule.Dispatch

	// runCtx stays live and non-nil until Run has joined admitted dispatches.
	// Nil means no Run owns the driver. Executions do not inherit runner
	// cancellation; entry timeouts or application work must end them.
	runCtx context.Context

	// wg tracks invocations that Run must await during shutdown.
	wg sync.WaitGroup

	// entries owns timers for recurring calendar entries.
	entries []*time.Timer

	// timers owns deferred-job timers indexed by their returned IDs.
	timers map[string]*time.Timer

	// pending owns jobs accepted while idle or draining, before a later Run
	// installs its dispatch callback. Defer captures independent payload bytes.
	pending map[string]schedule.Job
}

// New creates an in-process driver without starting timers. Callbacks run
// directly once in order on one fresh draft; nil entries panic and expanded
// nil/empty slices omit edits. Publication detaches retained drafts.
//
//	driver := memoryschedule.New(func(scheduler *memoryschedule.Builder) {
//		scheduler.Logger(slog.Default())
//	})
func New(configure ...BuilderFunc) *Driver {
	draft := &Builder{}

	for _, configure := range configure {
		configure(draft)
	}

	logger := draft.logger

	if logger == nil {
		logger = slog.Default()
	}

	return &Driver{
		logger:  logger,
		timers:  map[string]*time.Timer{},
		pending: map[string]schedule.Job{},
	}
}

// Run schedules entries and deferred jobs, and blocks until ctx is done. It
// then stops all timers, waits for runs in flight, and returns nil. A second
// concurrent call returns [ErrAlreadyRunning].
// This guard remains active through draining. After Run returns, the driver
// can be run again; a Scheduler facade itself remains single-use.
//
// Runner cancellation stops timer admission, not executions. Their contexts
// retain values but no runner deadline or cancellation. Draining has no driver
// timeout: default entry timeouts are unlimited. Handlers must finish through
// application work or a positive [schedule.EntryBuilder.Timeout]. A handler
// waiting only for its detached context can prevent Run from returning; timeouts
// require the execution to cooperate with its context.
// Application shutdown bounds its own runner wait, not this execution lifetime,
// and can proceed to cleanup while work remains. No forced termination occurs.
//
//	if err := driver.Run(ctx, scheduler.Entries(), dispatch); err != nil {
//		return err
//	}
func (driver *Driver) Run(ctx context.Context, entries []schedule.Entry, dispatch schedule.Dispatch) error {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	driver.mu.Lock()

	if driver.runCtx != nil {
		driver.mu.Unlock()

		return ErrAlreadyRunning
	}

	driver.running = true
	driver.dispatch = dispatch
	driver.runCtx = runCtx

	for _, entry := range entries {
		driver.armEntry(entry, &atomic.Bool{})
	}

	for id, job := range driver.pending {
		delete(driver.pending, id)
		driver.armJob(id, job)
	}

	driver.mu.Unlock()

	<-ctx.Done()

	driver.mu.Lock()

	driver.running = false

	for _, timer := range driver.entries {
		timer.Stop()
	}

	for id, timer := range driver.timers {
		timer.Stop()
		delete(driver.timers, id)
	}

	clear(driver.pending)

	driver.entries = nil
	driver.mu.Unlock()

	driver.wg.Wait()
	driver.mu.Lock()

	driver.runCtx = nil
	driver.dispatch = nil
	driver.mu.Unlock()

	return nil
}

// armEntry schedules the next tick of entry while the caller holds driver.mu.
// The timer captures its Run generation's context and dispatch callback. A stale
// callback cannot rearm an old entry after a later Run starts. Admission and
// wait-group registration remain under the same lock as stopping timers.
func (driver *Driver) armEntry(entry schedule.Entry, busy *atomic.Bool) {
	next := entry.Next(time.Now())

	if next.IsZero() {
		return
	}

	ctx, dispatch := driver.runCtx, driver.dispatch
	var timer *time.Timer
	timer = time.AfterFunc(time.Until(next), func() {
		driver.mu.Lock()
		defer driver.mu.Unlock()

		if !driver.running || driver.runCtx != ctx {
			return
		}

		driver.forget(timer)
		driver.armEntry(entry, busy)

		if !entry.Overlapping() && !busy.CompareAndSwap(false, true) {
			driver.logger.DebugContext(driver.runCtx, "schedule tick skipped: previous run in flight",
				slog.String("name", entry.Name()))

			return
		}

		driver.wg.Add(1)

		go func() {
			defer driver.wg.Done()
			defer busy.Store(false)

			driver.call(ctx, dispatch, entry.Name(), nil)
		}()
	})

	driver.entries = append(driver.entries, timer)
}

// forget removes a fired entry timer while the caller holds driver.mu. Missing
// timers are harmless. Clearing the vacated slice slot releases the timer and
// its captured entry when no running callback still owns them.
func (driver *Driver) forget(timer *time.Timer) {
	for index, candidate := range driver.entries {
		if candidate == timer {
			driver.entries = slices.Delete(driver.entries, index, index+1)

			return
		}
	}
}

// call executes an admitted run with its captured execution context and
// callback. It never reads replaceable per-Run state after admission, and logs
// a dispatch failure without changing the driver's scheduling result.
func (driver *Driver) call(ctx context.Context, dispatch schedule.Dispatch, name string, payload []byte) {
	if err := dispatch(ctx, name, payload); err != nil {
		driver.logger.ErrorContext(ctx, "schedule dispatch failed",
			slog.String("name", name), slog.Any("error", err))
	}
}

// armJob starts a deferred timer while the caller holds driver.mu. Its callback
// captures this Run's context and dispatch function, then admits execution only
// while the ID remains registered and timer admission is open. Cancel and Run
// shutdown can remove the registration before dispatch without executing it.
func (driver *Driver) armJob(id string, job schedule.Job) {
	ctx, dispatch := driver.runCtx, driver.dispatch
	driver.timers[id] = time.AfterFunc(time.Until(job.At), func() {
		driver.mu.Lock()

		if _, ok := driver.timers[id]; !ok || !driver.running {
			driver.mu.Unlock()

			return
		}

		delete(driver.timers, id)
		driver.wg.Add(1)
		driver.mu.Unlock()

		defer driver.wg.Done()
		driver.call(ctx, dispatch, job.Name, job.Payload)
	})
}

// Defer stores job until its time. Before Run it is kept pending and armed
// when Run starts. A job whose time has passed fires as soon as possible.
// Payload bytes are copied before acceptance, including for pending jobs.
// A context canceled before acceptance returns its error and an empty ID without
// scheduling. Cancellation after acceptance does not remove the job; use Cancel.
// During draining, new submissions wait pending for a later Run.
//
//	id, err := driver.Defer(ctx, schedule.Job{
//		Name:    "remind",
//		Payload: []byte(`"hello"`),
//		At:      due,
//	})
//	if err != nil {
//		return err
//	}
//	fmt.Println(id)
func (driver *Driver) Defer(ctx context.Context, job schedule.Job) (string, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	job.Payload = slices.Clone(job.Payload)
	id := rand.Text()

	if driver.running {
		driver.armJob(id, job)
	} else {
		driver.pending[id] = job
	}

	return id, nil
}

// Cancel removes the deferred job with the given id. An unknown id is not an
// error. A context canceled before mutation returns its error without removing
// pending work. Cancel does not interrupt a dispatch that has already started.
//
//	if err := driver.Cancel(ctx, id); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, id string) error {
	driver.mu.Lock()
	defer driver.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	if timer, ok := driver.timers[id]; ok {
		timer.Stop()
		delete(driver.timers, id)
	}

	delete(driver.pending, id)

	return nil
}

// Builder edits an in-process driver's construction draft. New copies its
// logger selection; retaining the draft cannot reconfigure a published driver.
//
//	driver := memoryschedule.New(func(scheduler *memoryschedule.Builder) {
//		scheduler.Logger(slog.Default())
//	})
type Builder struct {
	// logger is shared, not cloned; a final nil selects slog.Default.
	logger *slog.Logger
}

// Logger selects the shared run logger without logging or starting timers.
// A final nil selects slog.Default in New. It returns the same draft.
//
//	driver := memoryschedule.New(func(scheduler *memoryschedule.Builder) {
//		scheduler.Logger(slog.Default())
//	})
func (scheduler *Builder) Logger(value *slog.Logger) *Builder {
	scheduler.logger = value

	return scheduler
}

// BuilderFunc edits the fresh draft used by [New] without starting timers.
// Callbacks run synchronously once in order; supplied nil callbacks panic.
// Later edits through a retained draft do not replace the driver's logger.
//
// Example:
//
//	policy := memoryschedule.BuilderFunc(func(scheduler *memoryschedule.Builder) {
//		scheduler.Logger(slog.Default())
//	})
//	driver := memoryschedule.New(policy)
type BuilderFunc = func(*Builder)
