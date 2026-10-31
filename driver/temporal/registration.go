package temporal

import (
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Register queues fn to run against the worker before it starts. It returns
// ErrStarted after successful Start and rejects nil functions. Callbacks run
// synchronously during startup; panics propagate after owned client cleanup.
// Registration does not run callbacks or perform lifecycle I/O.
//
// Example:
//
//	err := host.Register(func(registry worker.Registry) {
//		registry.RegisterActivity(sendReceipt)
//	})
//	if err != nil {
//		return err
//	}
func (host *Host) Register(fn func(worker.Registry)) error {
	if fn == nil {
		return errors.New("register: nil function")
	}

	host.mu.Lock()
	defer host.mu.Unlock()

	if host.started {
		return fmt.Errorf("register: %w", ErrStarted)
	}

	host.registrations = append(host.registrations, fn)

	return nil
}

// startWorker registers and starts the permitted worker, or returns nil when
// none is needed. The instance is returned even on SDK failure so Start can
// release partially started native resources before closing its client.
func (host *Host) startWorker(conn client.Client) (worker.Worker, error) {
	if len(host.registrations) == 0 || host.config.NoWorker {
		return nil, nil
	}

	instance := worker.New(conn, host.config.TaskQueue, host.workerOptions())

	for _, register := range host.registrations {
		register(instance)
	}

	if err := instance.Start(); err != nil {
		return instance, fmt.Errorf("start temporal worker: %w", err)
	}

	return instance, nil
}
