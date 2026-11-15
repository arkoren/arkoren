// Package background runs the work that must not delay a response, such as
// sending an email, for the packages of arkoren.dev/ark/access. It is internal
// so the public packages never import each other.
package background

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// Timeout bounds one background job: the delivery of a message gets this
	// long, whatever happens to the request that started it.
	Timeout = time.Minute

	// Limit is how many jobs may run at once. Beyond it a job is logged and
	// dropped: an attacker who floods a throttled route must not be able to
	// grow the process without bound, and the request must not block on a slow
	// mail server either.
	Limit = 16
)

// Group tracks the jobs started with [Group.Go] so a shutdown, or a test, can
// wait for them. The zero value is ready to use and is safe for concurrent use.
type Group struct {
	mu      sync.Mutex
	running int
	idle    chan struct{}
}

// Go runs job in a new goroutine and reports whether it started. The job gets a
// fresh context limited to [Timeout]: it carries nothing from the request that
// started it (no cancellation, no transaction, no session), so the request's
// end cannot cancel it and a finished transaction is never reused. A panic in
// the job is recovered and logged. When [Limit] jobs are already running the
// job is logged and dropped, and Go returns false.
func (group *Group) Go(job func(ctx context.Context)) bool {
	group.mu.Lock()
	if group.running >= Limit {
		group.mu.Unlock()
		slog.ErrorContext(context.Background(), "access: background queue is full, a job was dropped")

		return false
	}
	if group.running == 0 {
		group.idle = make(chan struct{})
	}
	group.running++
	group.mu.Unlock()

	go func() {
		defer group.finish()
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(context.Background(), "access: background job panicked", "panic", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		job(ctx)
	}()

	return true
}

// finish records the end of one job and releases the waiters when it was the
// last.
func (group *Group) finish() {
	group.mu.Lock()
	defer group.mu.Unlock()

	group.running--
	if group.running == 0 {
		close(group.idle)
	}
}

// Wait blocks until no job is running, or returns the error of ctx when it ends
// first; it leaves nothing behind either way. Jobs started while Wait is
// blocked may or may not be waited for: call it after the last request, as a
// shutdown does.
func (group *Group) Wait(ctx context.Context) error {
	group.mu.Lock()
	if group.running == 0 {
		group.mu.Unlock()

		return nil
	}
	idle := group.idle
	group.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
