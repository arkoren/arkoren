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

	// Limit is how many jobs run at once.
	Limit = 16

	// QueueSize is how many jobs wait for a free worker. A burst of requests
	// (a mail server that slows down for a minute) is absorbed here; only when
	// the queue is full too is a job dropped and logged, so a flood of throttled
	// requests can never grow the process without bound and a request never
	// blocks on a slow mail server.
	QueueSize = 256
)

// Group tracks the jobs started with [Group.Go] so a shutdown, or a test, can
// wait for them. The zero value is ready to use and is safe for concurrent use.
type Group struct {
	mu      sync.Mutex
	running int
	queue   []func(ctx context.Context)
	idle    chan struct{}
}

// Go runs job on one of [Limit] workers, or queues it behind them, and reports
// whether it was accepted. The job gets a fresh context limited to [Timeout]: it
// carries nothing from the request that started it (no cancellation, no
// transaction, no session), so the request's end cannot cancel it and a finished
// transaction is never reused. A panic in the job is recovered and logged. When
// all workers are busy and [QueueSize] jobs already wait, the job is dropped,
// logged at error level, and Go returns false.
func (group *Group) Go(job func(ctx context.Context)) bool {
	group.mu.Lock()
	if group.running >= Limit {
		if len(group.queue) >= QueueSize {
			group.mu.Unlock()
			slog.ErrorContext(context.Background(), "access: background queue is full, a job was dropped")

			return false
		}
		group.queue = append(group.queue, job)
		group.mu.Unlock()

		return true
	}
	if group.running == 0 {
		group.idle = make(chan struct{})
	}
	group.running++
	group.mu.Unlock()

	go group.work(job)

	return true
}

// work runs job and then the queued jobs, until none is left; the last worker
// to finish releases the waiters.
func (group *Group) work(job func(ctx context.Context)) {
	for job != nil {
		group.run(job)

		group.mu.Lock()
		if len(group.queue) > 0 {
			job = group.queue[0]
			group.queue[0] = nil
			group.queue = group.queue[1:]
		} else {
			job = nil
			group.running--
			if group.running == 0 {
				close(group.idle)
			}
		}
		group.mu.Unlock()
	}
}

// run executes one job with a fresh context, recovering a panic.
func (group *Group) run(job func(ctx context.Context)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(context.Background(), "access: background job panicked", "panic", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	job(ctx)
}

// Wait blocks until no job is running or queued, or returns the error of ctx
// when it ends first; it leaves nothing behind either way. Jobs started while
// Wait is blocked may or may not be waited for: call it after the last request,
// as a shutdown does.
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
