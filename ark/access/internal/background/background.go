// Package background runs the work that must not delay a response, such as
// sending an email, for the packages of arkoren.dev/ark/access. It is internal
// so the public packages never import each other.
package background

import (
	"context"
	"sync"
	"time"
)

// Timeout bounds one background job: the delivery of a message gets this long,
// whatever happens to the request that started it.
const Timeout = time.Minute

// Group tracks the jobs started with [Group.Go] so a shutdown, or a test, can
// wait for them. The zero value is ready to use and is safe for concurrent use.
type Group struct {
	jobs sync.WaitGroup
}

// Go runs job in a new goroutine with a context that keeps the values of ctx
// (such as a trace) but not its cancellation, limited to [Timeout]. The request
// that started the job has usually finished by the time it runs.
func (group *Group) Go(ctx context.Context, job func(ctx context.Context)) {
	ctx = context.WithoutCancel(ctx)
	group.jobs.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, Timeout)
		defer cancel()
		job(ctx)
	})
}

// Wait blocks until every job started so far has finished, or returns the error
// of ctx when it ends first. Jobs keep running after Wait gives up.
func (group *Group) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		group.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
