// Package scheduletest helps test code that defers jobs and checks drivers of
// the schedule package. It offers two tools.
//
// [Fake] is a [schedule.Driver] for unit tests. It records the entries given
// to Run and the jobs given to Defer, and fires nothing on its own: a test
// calls [Fake.Drain] to hand the deferred jobs that are due to a dispatch
// function. Used with a [schedule.Scheduler], Drain runs the real task
// handlers.
//
//	fake := scheduletest.New()
//	scheduler := schedule.New(fake)
//	_ = scheduler.Handle(Remind, remind)
//
//	_, _ = scheduler.After(ctx, Remind, Reminder{
//		UserID: 42,
//	}, 0)
//	fake.AssertDeferred(t, Remind, func(r Reminder) bool {
//		return r.UserID == 42
//	})
//
//	go scheduler.Run(ctx)
//	synctest.Wait() // inside a synctest bubble: until Run has reached the fake
//	err := fake.Drain(ctx)
//
// Drain returns [ErrNotRunning] until the scheduler's Run has called the
// fake's Run, so wait for it before draining.
//
// [Run] is the conformance suite that every driver must pass:
//
//	func TestConformance(t *testing.T) {
//		scheduletest.Run(t, func(t *testing.T) schedule.Driver {
//			return memoryschedule.New()
//		}, scheduletest.WithSynctest())
//	}
//
// Drivers backed by an engine run the suite from a test gated like their other
// integration tests.
package scheduletest
