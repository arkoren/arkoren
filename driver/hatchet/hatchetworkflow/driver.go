package hatchetworkflow

import (
	"cmp"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"sync"
	"time"

	"arkoren.dev/core/service/workflow"
	"arkoren.dev/driver/hatchet"
	"arkoren.dev/driver/hatchet/hatchetworkflow/internal/protocol"
)

// cacheLimit bounds the run ids a driver remembers.
const cacheLimit = 4096

// pollStart is the first delay between status polls.
const pollStart = 25 * time.Millisecond

// resolveStart is the first delay between lookups of a run that is not listed
// yet. It doubles up to resolveCap.
const (
	resolveStart = 10 * time.Millisecond
	resolveCap   = 250 * time.Millisecond
)

// pollJitterDivisor bounds random variation to one fifth of the nominal delay.
// Integer arithmetic avoids float-to-duration overflow near MaxInt64.
const pollJitterDivisor = 5

// signalVersion retains the physical-execution protocol identity.
const signalVersion = protocol.SignalVersion

// cacheEntry retains a verified physical ID and the time this process learned
// it. Identity is immutable in Hatchet; only verified nonempty IDs are stored.
type cacheEntry struct {
	// key identifies the resource within its owning workflow or signal scope.
	key string
	// runID is the physical engine execution associated with the logical cache
	// key.
	runID string
	// at records when this cache entry was refreshed for expiry checks.
	at time.Time
}

// Driver implements [workflow.Driver] on Hatchet durable tasks. Create it with
// [NewDriver], or take the one of a [Runner]. It is safe for concurrent use.
// The shared host owns native acquisition and release, not this value.
//
// Example:
//
//	driver, err := NewDriver(hatchet.New())
//	if err != nil {
//		return err
//	}
//	client := workflow.NewClient(driver)
//	_ = client
type Driver struct {
	// config retains the completed construction settings used by this instance.
	config settings
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger

	// backend returns the Hatchet access; tests replace it.
	backend func() (backend, error)

	// namespace is the effective SDK prefix of workflow names and event
	// keys, including its trailing underscore; empty means no namespace.
	namespace string

	// known holds the workflows that Hatchet has.
	known sync.Map

	// mutex serializes access to the mutable state below it.
	mutex sync.Mutex
	// order tracks cache entries from most to least recently used.
	order *list.List
	// runs indexes verified executions only for eventual-consistency retry windows.
	runs map[string]*list.Element
}

var _ workflow.Driver = (*Driver)(nil)

// NewDriver returns a driver that starts and queries runs through host. The
// host needs no worker for that; use [hatchet.Builder.NoWorker] in a process
// that only starts runs. Call it before or after host.Start. It performs no
// I/O.
// Callbacks run once in order on one fresh [DriverBuilder]; nil entries panic.
// Published settings are independent of a retained draft.
//
// Example:
//
//	driver, err := NewDriver(hatchet.New(), func(driver *DriverBuilder) {
//		driver.Prefix("orders")
//	})
//	if err != nil {
//		panic(err)
//	}
//	_ = driver
func NewDriver(host *hatchet.Host, configure ...DriverBuilderFunc) (*Driver, error) {
	if host == nil {
		return nil, errors.New("new hatchet workflow driver: nil host")
	}

	return newHostDriver(host, driverSettings(configure...))
}

// newHostDriver attaches the validated host to completed settings. Runners use
// this publication seam without replaying their construction callbacks.
func newHostDriver(host *hatchet.Host, config settings) (*Driver, error) {
	driver := newDriver(config)

	if err := driver.config.validate(); err != nil {
		return nil, err
	}

	driver.namespace = host.Namespace()
	driver.backend = func() (backend, error) {
		client := host.Client()

		if client == nil {
			return nil, fmt.Errorf("hatchet workflow: %w", hatchet.ErrNotStarted)
		}

		return hatchetBackend{
			client: client,
		}, nil
	}

	return driver, nil
}

// newDriver returns a driver with defaults applied and no backend.
func newDriver(config settings) *Driver {
	config.Prefix = cmp.Or(config.Prefix, DefaultPrefix)
	config.Lookback = cmp.Or(config.Lookback, DefaultLookback)
	config.TimeoutMargin = cmp.Or(config.TimeoutMargin, DefaultTimeoutMargin)
	config.ScheduleTimeout = cmp.Or(config.ScheduleTimeout, DefaultScheduleTimeout)
	config.EvictionTTL = cmp.Or(config.EvictionTTL, DefaultEvictionTTL)
	config.PollInterval = cmp.Or(config.PollInterval, DefaultPollInterval)
	config.LookupGrace = cmp.Or(config.LookupGrace, DefaultLookupGrace)

	return &Driver{
		config: config,
		logger: cmp.Or(config.Logger, slog.Default()),
		order:  list.New(),
		runs:   map[string]*list.Element{},
	}
}

// workflowName returns the Hatchet name of workflow name.
func (driver *Driver) workflowName(name string) string {
	return driver.config.Prefix + "-wf-" + name
}

// runKey binds the public driver settings to the private identity protocol.
func (driver *Driver) runKey(name, id string) string {
	return protocol.RunKey(driver.namespace, driver.config.Prefix, name, id)
}

// signalWorkflow returns the Hatchet name of the marker workflow of signals.
func (driver *Driver) signalWorkflow() string {
	return driver.config.Prefix + "-signal"
}

// signalKey returns the event key of a signal.
func (driver *Driver) signalKey(signal string) string {
	return driver.config.Prefix + ".sig." + signal
}

// signalScope binds a verified logical identity to one physical execution.
func signalScope(key, executionID string) string { return protocol.SignalScope(key, executionID) }

// signalMarkerKey binds one signal name to the verified physical scope.
func signalMarkerKey(scope, signal string) string { return protocol.SignalMarkerKey(scope, signal) }

// remember keeps the Hatchet id of a run, evicting the least recently used
// entry when the cache is full.
func (driver *Driver) remember(key, runID string) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	entry := &cacheEntry{
		key:   key,
		runID: runID,
		at:    time.Now(),
	}

	if element, ok := driver.runs[key]; ok {
		element.Value = entry
		driver.order.MoveToFront(element)

		return
	}

	driver.runs[key] = driver.order.PushFront(entry)

	if driver.order.Len() > cacheLimit {
		oldest := driver.order.Back()

		driver.order.Remove(oldest)
		delete(driver.runs, oldest.Value.(*cacheEntry).key) //nolint:forcetypeassert // only entries are stored
	}
}

// cached returns the remembered entry of a run.
func (driver *Driver) cached(key string) (cacheEntry, bool) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	element, ok := driver.runs[key]

	if !ok {
		return cacheEntry{}, false
	}

	driver.order.MoveToFront(element)

	return *element.Value.(*cacheEntry), true //nolint:forcetypeassert // only entries are stored
}

// forget drops the entry of a run.
func (driver *Driver) forget(key string) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	if element, ok := driver.runs[key]; ok {
		driver.order.Remove(element)
		delete(driver.runs, key)
	}
}

// recent reports whether this process learned of the run within the lookup
// grace, so that Hatchet may not list it yet.
func (driver *Driver) recent(entry cacheEntry) bool {
	return time.Since(entry.at) < driver.config.LookupGrace
}

// Start begins run id of workflow name with a namespace-qualified idempotency
// key. A duplicate returns [workflow.ErrAlreadyStarted] only after the listed
// workflow and stored input prove ownership of the existing physical run.
// Verification failures return an error without retaining the physical ID;
// a successful trigger may already have started work, so callers must not
// infer from that error that no execution exists. A delay longer than the
// timeout margin is not supported. Legacy keys are never discovered.
//
// Example:
//
//	err := driver.Start(ctx, "order", "order-42", payload, workflow.StartOptions{})
//	if errors.Is(err, workflow.ErrAlreadyStarted) {
//		// The existing execution has the same namespace and logical identity.
//	}
func (driver *Driver) Start(ctx context.Context, name, id string, in []byte, o workflow.StartOptions) error {
	if o.Delay > driver.config.TimeoutMargin {
		return fmt.Errorf("start %s: delay above the timeout margin %s: %w", name, driver.config.TimeoutMargin, workflow.ErrUnsupported)
	}

	be, err := driver.backend()

	if err != nil {
		return fmt.Errorf("start %s %s: %w", name, id, err)
	}

	if err := driver.checkWorkflow(ctx, be, driver.workflowName(name)); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	key := driver.runKey(name, id)
	input := wire{
		Trace:         maps.Clone(o.Trace),
		Data:          string(in),
		ID:            id,
		Key:           key,
		Delay:         int64(max(o.Delay, 0)),
		SignalVersion: signalVersion,
	}

	metadata := maps.Clone(o.Metadata)

	if metadata == nil {
		metadata = map[string]string{}
	}

	metadata["workflow_id"] = id
	metadata["workflow_name"] = name

	runID, err := be.trigger(ctx, driver.workflowName(name), input, metadata)

	if err != nil {
		if existing, ok := collisionRun(err); ok {
			driver.forget(key)

			verified, _, err := driver.find(ctx, be, runIdentity{
				workflow: driver.namespace + driver.workflowName(name), key: key, logical: id, physical: existing,
			}, time.Now().Add(max(driver.config.LookupGrace, 0)))

			if err != nil {
				return fmt.Errorf("verify existing run %s %s: %w", name, id, err)
			}

			driver.remember(key, verified)

			return fmt.Errorf("start %s %s: %w", name, id, workflow.ErrAlreadyStarted)
		}

		if isNotFound(err) {
			return fmt.Errorf("start %s %s: %w: %w", name, id, workflow.ErrNotFound, err)
		}

		return fmt.Errorf("start %s %s: %w", name, id, err)
	}

	driver.forget(key)

	verified, _, err := driver.find(ctx, be, runIdentity{
		workflow: driver.namespace + driver.workflowName(name), key: key, logical: id, physical: runID,
	}, time.Now().Add(max(driver.config.LookupGrace, 0)))

	if err != nil {
		return fmt.Errorf("verify started run %s %s: %w", name, id, err)
	}

	driver.remember(key, verified)

	return nil
}

// checkWorkflow returns [workflow.ErrNotFound] unless Hatchet knows the
// workflow called full: triggering an unknown workflow does not fail by
// itself. It remembers the workflows it found.
func (driver *Driver) checkWorkflow(ctx context.Context, be backend, full string) error {
	if _, ok := driver.known.Load(full); ok {
		return nil
	}

	found, err := be.exists(ctx, driver.namespace+full)

	if err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("%w: %s is not registered in Hatchet", workflow.ErrNotFound, full)
	}

	driver.known.Store(full, true)

	return nil
}

// resolve discovers the current execution under the exact namespaced workflow
// and versioned key. Cached IDs cannot prove current ownership after logical-ID
// reuse; the cache only extends consistency retries for recently verified runs.
// No legacy key fallback is permitted, even for an empty namespace.
func (driver *Driver) resolve(ctx context.Context, be backend, name, id string) (string, error) {
	key := driver.runKey(name, id)

	if id == "" {
		return "", workflow.ErrNotFound
	}

	deadline := time.Now()

	if entry, ok := driver.cached(key); ok {
		deadline = entry.at.Add(max(driver.config.LookupGrace, 0))
	}

	runID, _, err := driver.find(ctx, be, runIdentity{
		workflow: driver.namespace + driver.workflowName(name), key: key, logical: id,
	}, deadline)

	if err != nil {
		driver.forget(key)

		return "", fmt.Errorf("find run %s %s: %w", name, id, err)
	}

	driver.remember(key, runID)

	return runID, nil
}

// details returns the state of a run. A run that this process just started may
// not be visible yet; it is retried during [DriverBuilder.LookupGrace]. A run that
// Hatchet no longer knows is dropped from the cache.
func (driver *Driver) details(ctx context.Context, be backend, key, runID string) (runDetail, error) {
	delay := resolveStart

	for {
		detail, err := be.details(ctx, runID)

		if err == nil {
			return detail, nil
		}

		if !errors.Is(err, workflow.ErrNotFound) {
			return runDetail{}, err
		}

		entry, known := driver.cached(key)

		if !known || entry.runID != runID || !driver.recent(entry) {
			driver.forget(key)

			return runDetail{}, err
		}

		if err := sleep(ctx, delay); err != nil {
			return runDetail{}, err
		}

		delay = min(delay*2, resolveCap)
	}
}

// Result resolves the current physical execution, then waits for that execution
// to end. It returns facade-envelope bytes or a portable failure. Every poll delay
// (including jitter) is capped by PollInterval; transport work adds its own time.
// Caller cancellation retains context error identity and does not cancel the run.
//
// Example:
//
//	output, err := driver.Result(ctx, "order", "order-42")
//	if err != nil {
//		return err
//	}
//	_ = output // Decode through workflow.Client for a typed result.
func (driver *Driver) Result(ctx context.Context, name, id string) ([]byte, error) {
	be, err := driver.backend()

	if err != nil {
		return nil, fmt.Errorf("result %s %s: %w", name, id, err)
	}

	runID, err := driver.resolve(ctx, be, name, id)

	if err != nil {
		return nil, err
	}

	key := driver.runKey(name, id)
	delay := min(pollStart, driver.config.PollInterval)

	for {
		detail, err := driver.details(ctx, be, key, runID)

		if err != nil {
			return nil, fmt.Errorf("result %s %s: %w", name, id, err)
		}

		switch detail.Status {
		case statusCompleted:
			return outputOf(detail.Output)
		case statusCanceled:
			return nil, fmt.Errorf("result %s %s: %w", name, id, workflow.ErrCanceled)
		case statusFailed:
			return nil, failure(detail.Error)
		}

		if err := sleep(ctx, jitter(delay, driver.config.PollInterval)); err != nil {
			return nil, err
		}

		delay += min(delay, driver.config.PollInterval-delay)
	}
}

// jitter varies a positive nominal delay by at most one fifth, without ever
// exceeding limit. The caller maintains 0 < delay <= limit; bounded integer
// offsets remain representable even when limit is the largest duration.
func jitter(delay, limit time.Duration) time.Duration {
	spread := delay / pollJitterDivisor
	upper := min(spread, limit-delay)

	return delay - spread + time.Duration(rand.Int64N(int64(spread+upper)+1))
}

// Status rediscovers the current physical execution for a logical identity and
// returns its portable state. Stored wire identity is verified even if this
// process queried an earlier execution with that logical ID before TTL expiry.
//
// Example:
//
//	state, err := driver.Status(ctx, "order", "order-42")
//	if err != nil {
//		return err
//	}
//	_ = state == workflow.Running
func (driver *Driver) Status(ctx context.Context, name, id string) (workflow.Status, error) {
	be, err := driver.backend()

	if err != nil {
		return "", fmt.Errorf("status %s %s: %w", name, id, err)
	}

	runID, err := driver.resolve(ctx, be, name, id)

	if err != nil {
		return "", err
	}

	detail, err := driver.details(ctx, be, driver.runKey(name, id), runID)

	if err != nil {
		return "", fmt.Errorf("status %s %s: %w", name, id, err)
	}

	return mapStatus(detail.Status)
}

// mapStatus converts a Hatchet status into a facade status.
func mapStatus(got string) (workflow.Status, error) {
	switch got {
	case statusQueued, statusRunning:
		return workflow.Running, nil
	case statusCompleted:
		return workflow.Completed, nil
	case statusFailed:
		return workflow.Failed, nil
	case statusCanceled:
		return workflow.Canceled, nil
	default:
		return "", fmt.Errorf("hatchet workflow: unknown run status %q", got)
	}
}

// Cancel resolves the current physical execution and requests native cancellation.
// Canceling a finished run does nothing; running application code must honor its
// context. Missing identities return workflow.ErrNotFound without submission.
//
// Example:
//
//	if err := driver.Cancel(ctx, "order", "order-42"); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, name, id string) error {
	be, err := driver.backend()

	if err != nil {
		return fmt.Errorf("cancel %s %s: %w", name, id, err)
	}

	runID, err := driver.resolve(ctx, be, name, id)

	if err != nil {
		return err
	}

	if err := be.cancel(ctx, runID); err != nil {
		return fmt.Errorf("cancel %s %s: %w", name, id, err)
	}

	return nil
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for run: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
