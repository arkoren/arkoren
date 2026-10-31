package hatchet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/rs/zerolog"
)

// Start connects to Hatchet, runs every registration, and starts the worker.
// It validates explicit settings before client acquisition and releases owned
// resources on failure. With registrations it waits for the registration log;
// client-only startup performs no worker registration. Earlier SDK RPCs use
// background contexts, so neither ctx nor RegisterTimeout bounds all startup.
// SDK worker failures can panic in an internal goroutine; Start cannot recover
// them. Stop racing Start waits for acquisition and releases the resulting client.
//
// Example:
//
//	if err := host.Start(ctx); err != nil {
//		return err
//	}
//	return host.Ping(ctx)
func (host *Host) Start(ctx context.Context) error {
	host.mutex.Lock()

	if host.stopped {
		host.mutex.Unlock()

		return fmt.Errorf("start hatchet host: %w", ErrStopped)
	}

	if host.started || host.starting != nil {
		host.mutex.Unlock()

		return fmt.Errorf("start hatchet host: %w", ErrStarted)
	}

	if err := ctx.Err(); err != nil {
		host.mutex.Unlock()

		return fmt.Errorf("start hatchet host: %w", err)
	}

	host.starting = make(chan struct{})
	registrations := slices.Clone(host.registrations)
	host.mutex.Unlock()

	client, cleanup, err := host.connect(ctx, registrations)

	host.mutex.Lock()
	defer host.mutex.Unlock()

	close(host.starting)
	host.starting = nil

	if err != nil {
		return err
	}

	host.client = client
	host.cleanup = cleanup
	host.started = true

	return nil
}

// connect creates the client, runs the registrations, and starts the worker.
func (host *Host) connect(ctx context.Context, registrations []Registration) (_ *hatchet.Client, _ func() error, err error) {
	options, err := host.clientOptions()

	if err != nil {
		return nil, nil, err
	}

	log := newSDKLog(host.logger)
	options = append(options, hatchet.WithClientLogger(log.zerolog()))

	client, err := newClient(options)

	if err != nil {
		return nil, nil, fmt.Errorf("create hatchet client: %w", err)
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, client.Close(context.WithoutCancel(ctx)))
		}
	}()

	// The SDK creates these sub-clients lazily and without a lock. Creating
	// them here makes later concurrent calls only read.
	_ = client.Runs()
	_ = client.Crons()
	_ = client.Schedules()
	_ = client.Events()
	_ = client.Workflows()

	if host.config.NoWorker || len(registrations) == 0 {
		host.logger.InfoContext(ctx, "hatchet client started without worker")

		return client, nil, nil
	}

	cleanup, err := host.startRegisteredWorker(ctx, client, registrations, log)

	if err != nil {
		return nil, nil, err
	}

	return client, cleanup, nil
}

// startRegisteredWorker builds native declarations and checks capabilities before
// creating intake. It returns only after registration observation, or synchronous
// rollback; connect remains the owner of client acquisition and failure release.
func (host *Host) startRegisteredWorker(ctx context.Context, client *hatchet.Client, registrations []Registration, log *sdkLog) (func() error, error) {
	workflows := make([]hatchet.WorkflowBase, 0, len(registrations))

	for index, registration := range registrations {
		workflow, err := runRegistration(registration, client)

		if err != nil {
			return nil, fmt.Errorf("register hatchet workflow %d: %w", index, err)
		}

		workflows = append(workflows, workflow)
	}

	if err := host.checkEngine(ctx, client, workflows); err != nil {
		return nil, err
	}

	cleanup, err := host.startWorker(client, workflows)

	if err != nil {
		return nil, err
	}

	if err := host.awaitRegistration(ctx, log, cleanup); err != nil {
		return nil, err
	}

	host.logger.InfoContext(ctx, "hatchet worker started", "worker", host.config.WorkerName, "workflows", len(workflows))

	return cleanup, nil
}

// startWorker binds native callbacks to one owned lifetime before the SDK starts
// intake. Cleanup cancels and joins admitted actions; SDK registration and intake
// goroutines remain subject to the pinned SDK's separate lifecycle limitations.
func (host *Host) startWorker(client *hatchet.Client, workflows []hatchet.WorkflowBase) (func() error, error) {
	workerOptions := []hatchet.WorkerOption{
		hatchet.WithWorkflows(workflows...),
		hatchet.WithSlots(host.config.Slots),
	}

	if host.config.DurableSlots > 0 {
		workerOptions = append(workerOptions, hatchet.WithDurableSlots(host.config.DurableSlots))
	}

	// The registration record is a debug record; a global level above debug
	// drops it before any hook runs.
	if level := zerolog.GlobalLevel(); level > zerolog.DebugLevel {
		return nil, fmt.Errorf("start hatchet worker: %w: zerolog global level %q hides the registration record", ErrNotRegistered, level)
	}

	worker, err := client.NewWorker(host.config.WorkerName, workerOptions...)

	if err != nil {
		return nil, fmt.Errorf("create hatchet worker: %w", err)
	}

	lifetime, cancel := context.WithCancel(context.Background())
	worker.Use(host.trackActions(lifetime))

	release, err := worker.Start()

	if err != nil {
		cancel()

		return nil, fmt.Errorf("start hatchet worker: %w", err)
	}

	cleanup := func() error {
		host.mutex.Lock()
		cancel()
		host.mutex.Unlock()

		err := release()
		host.actions.Wait()

		return err
	}

	return cleanup, nil
}

// trackActions admits native callbacks under the worker's lifetime and supplies
// cancellation to their real SDK context. It does not launch replacement work or
// detach application bodies: cleanup waits for every admitted callback to return.
func (host *Host) trackActions(lifetime context.Context) hatchet.MiddlewareFunc {
	return func(native hatchet.Context, next func(hatchet.Context) error) error {
		host.mutex.Lock()

		if err := lifetime.Err(); err != nil {
			host.mutex.Unlock()

			return err
		}

		host.actions.Add(1)
		host.mutex.Unlock()
		defer host.actions.Done()

		parent := native.GetContext()
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(lifetime, cancel)
		defer func() {
			native.SetContext(parent)
			stop()
			cancel()
		}()

		native.SetContext(ctx)

		return next(native)
	}
}

// awaitRegistration waits until the engine has registered the worker. The
// SDK panics when its stop function cancels a registration in progress, so
// cleanup runs after the registration record or RegisterTimeout. Cancellation
// waits for that same handoff, then performs cleanup synchronously; it never
// leaves an adapter-owned cleanup goroutine behind. RegisterTimeout is only a
// registration-observation budget, not a bound on native startup or cleanup.
// The SDK's asynchronous panic risk remains even with this delayed cleanup.
func (host *Host) awaitRegistration(ctx context.Context, log *sdkLog, cleanup func() error) error {
	timer := time.NewTimer(host.config.RegisterTimeout)

	select {
	case <-log.registered:
		timer.Stop()

		return nil
	case <-timer.C:
		return errors.Join(
			fmt.Errorf("start hatchet worker: %w within %s", ErrNotRegistered, host.config.RegisterTimeout),
			cleanup(),
		)
	case <-ctx.Done():
		select {
		case <-log.registered:
			timer.Stop()
		case <-timer.C:
		}

		return errors.Join(fmt.Errorf("register hatchet worker: %w", ctx.Err()), cleanup())
	}
}

// newClient creates the Hatchet client and turns the panic of the SDK on a
// malformed token into an error.
func newClient(options []hatchet.ClientOpt) (client *hatchet.Client, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("client panicked: %v", recovered)
		}
	}()

	return hatchet.NewClient(options...)
}

// Stop invokes native worker cleanup and closes the owned client exactly once.
// Release runs synchronously even with an expired ctx; cancellation does not
// bound acquisition, SDK cleanup, or noncooperative handlers. Stop cancels and
// joins application callbacks admitted by the host middleware; it cannot join
// the SDK's opaque intake/reporting goroutines. Native Client.Close releases an
// embedded backend but is a no-op for ordinary SDK gRPC connections. Later calls
// return the first result. Construct a new host for another lifecycle.
// The pinned SDK's shutdown eviction path can race a returning durable wait:
// it reads mutable wait metadata without the record lock. Joining admitted
// callbacks does not make that native cleanup path race-free.
//
// Example:
//
//	if err := host.Stop(ctx); err != nil {
//		return err
//	}
func (host *Host) Stop(ctx context.Context) error {
	host.stopOnce.Do(func() {
		host.mutex.Lock()
		host.stopped = true
		starting := host.starting
		host.mutex.Unlock()

		if starting != nil {
			<-starting
		}

		host.mutex.Lock()
		cleanup := host.cleanup
		client := host.client
		host.client = nil
		host.mutex.Unlock()

		if cleanup != nil {
			host.stopErr = cleanup()
		}

		if client != nil {
			host.stopErr = errors.Join(host.stopErr, client.Close(context.WithoutCancel(ctx)))
		}

		if host.stopErr != nil {
			host.stopErr = fmt.Errorf("stop hatchet host: %w", host.stopErr)
		}
	})

	return host.stopErr
}
