package temporalschedule

import (
	"context"

	"arkoren.dev/core/service/schedule"
)

// backend resolves the Temporal API only when a native client is available.
// A supplied NewFrom client is already usable; a dialing host must start first.
func (driver *Driver) backend() (backend, error) {
	if driver.api != nil {
		return driver.api, nil
	}

	conn := driver.host.Client()

	if conn == nil {
		return nil, ErrNotStarted
	}

	return temporalBackend{
		client: conn,
	}, nil
}

// Run stores dispatch, reconciles Temporal schedules with entries, and blocks
// until ctx is done. It returns nil on cancellation, including cancellation
// before reconciliation. A missing native connection returns [ErrNotStarted].
// Only one local Run may be active ([schedule.ErrRunning]); dispatch must be
// non-nil. Exiting removes dispatch and cancels active dispatch contexts, but
// does not wait for application callbacks to return. Early native activities
// wait for Run within their activity timeout rather than losing a delivery.
//
// Example:
//
//	err := driver.Run(ctx, entries, dispatch)
//	if err != nil {
//		return err
//	}
func (driver *Driver) Run(ctx context.Context, entries []schedule.Entry, dispatch schedule.Dispatch) error {
	if ctx.Err() != nil {
		return nil
	}

	api, err := driver.backend()

	if err != nil {
		return err
	}

	release, err := driver.bindDispatch(ctx, dispatch)

	if err != nil {
		return err
	}

	defer release()

	if err := driver.reconcile(ctx, api, entries); err != nil {
		if ctx.Err() != nil {
			return nil
		}

		return err
	}

	<-ctx.Done()

	return nil
}

// Defer starts a delayed workflow for job and returns its workflow ID. It does
// not require Run in this process, but needs a connected host; otherwise it
// returns [ErrNotStarted]. A worker using the same prefix and queue dispatches it.
//
// Example:
//
//	id, err := driver.Defer(ctx, schedule.Job{Name: "reminder", At: time.Now().Add(time.Hour)})
//	if err != nil {
//		return err
//	}
//	_ = id // Retain for cancellation.
func (driver *Driver) Defer(ctx context.Context, job schedule.Job) (string, error) {
	api, err := driver.backend()

	if err != nil {
		return "", err
	}

	return driver.deferJob(ctx, api, job)
}

// Cancel terminates the deferred workflow with the given ID. An unknown or
// finished workflow is not an error. It returns [ErrNotStarted] when the host
// has no native connection; no Run in this process is required.
//
// Example:
//
//	if err := driver.Cancel(ctx, id); err != nil {
//		return err
//	}
func (driver *Driver) Cancel(ctx context.Context, id string) error {
	api, err := driver.backend()

	if err != nil {
		return err
	}

	return driver.cancel(ctx, api, id)
}
