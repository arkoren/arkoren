package hatchetschedule

import (
	"context"
	"errors"
	"fmt"

	"arkoren.dev/core/service/schedule"
)

// dispatchReady waits for initial Run readiness, then admits one callback under
// the lifecycle lock. Run teardown removes the callback before waiting for all
// admitted calls; stopped or failed runs never retain callable application work.
func (driver *Driver) dispatchReady(ctx context.Context) (schedule.Dispatch, error) {
	driver.mutex.Lock()
	dispatch := driver.dispatch
	driver.mutex.Unlock()

	if dispatch == nil {
		select {
		case <-driver.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	driver.mutex.Lock()
	dispatch = driver.dispatch

	if dispatch == nil {
		driver.mutex.Unlock()

		return nil, fmt.Errorf("driver is not running: %w", context.Canceled)
	}

	driver.calls.Add(1)
	driver.mutex.Unlock()

	return dispatch, nil
}

// Run installs dispatch once, reconciles namespace-qualified cron inventory,
// and waits for ctx. The host must be started. Earlier native assignments wait
// for readiness rather than failing before dispatch exists. On any return, Run
// removes dispatch, cancels admitted call contexts, and joins their bodies; a
// callback ignoring cancellation can delay return. Later Run calls return
// schedule.ErrRunning. Reconciliation failures are errors, not empty inventory.
//
// Example:
//
//	// Prefer schedule.New(driver).Run for normal entry/task dispatch.
//	if err := driver.Run(ctx, entries, dispatch); err != nil {
//		return err
//	}
func (driver *Driver) Run(ctx context.Context, entries []schedule.Entry, dispatch schedule.Dispatch) error {
	if dispatch == nil {
		return errors.New("run hatchet schedule driver: nil dispatch")
	}

	finish, err := driver.beginRun(ctx, dispatch)

	if err != nil {
		return err
	}

	defer finish()

	crons, _, err := driver.clients()

	if err != nil {
		return fmt.Errorf("run hatchet schedule driver: %w", err)
	}

	if err := driver.reconcile(ctx, crons, entries); err != nil {
		return fmt.Errorf("reconcile hatchet crons: %w", err)
	}

	<-ctx.Done()

	return nil
}

// beginRun binds dispatch to one cancelable lifetime and returns its synchronous
// teardown. Closing readiness is permanent: like the scheduler facade, a driver
// runs once. Teardown cancels callback contexts and joins their actual bodies;
// a body that ignores cancellation can therefore delay Run's return.
func (driver *Driver) beginRun(ctx context.Context, dispatch schedule.Dispatch) (func(), error) {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()

	select {
	case <-driver.ready:
		return nil, schedule.ErrRunning
	default:
	}

	lifetime, cancel := context.WithCancel(ctx)
	driver.dispatch = func(native context.Context, name string, payload []byte) error {
		callCtx, release := context.WithCancel(native)
		stop := context.AfterFunc(lifetime, release)
		defer func() {
			stop()
			release()
		}()

		if err := lifetime.Err(); err != nil {
			return err
		}

		return dispatch(callCtx, name, payload)
	}

	close(driver.ready)

	return func() {
		driver.mutex.Lock()
		driver.dispatch = nil
		cancel()
		driver.mutex.Unlock()

		driver.calls.Wait()
	}, nil
}
