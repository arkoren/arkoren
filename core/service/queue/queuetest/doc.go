// Package queuetest tests code that dispatches or consumes jobs. It offers a
// recording [Fake] with typed assertions and a synchronous [Fake.Drain], and
// [Run], a conformance suite that every [queue.DispatcherDriver] and
// [queue.ConsumerDriver] pair must pass.
//
// A test that exercises code under test dispatching jobs:
//
//	fake := queuetest.New()
//	dispatcher := queue.NewDispatcher(fake)
//	consumer := queue.NewConsumer(fake)
//	_ = consumer.Handle(job, handler)
//
//	_ = dispatcher.Dispatch(ctx, job, payload)
//	fake.AssertDispatched(t, job, func(p Payload) bool {
//		return p.ID == 1
//	})
//	_ = fake.Drain(ctx, consumer)
//
// Drain runs the real [queue.Consumer], so handler registration, decoding,
// retry, rejection, and failure handling behave as in production.
//
// A driver author proves a driver with the suite:
//
//	func TestConformance(t *testing.T) {
//		queuetest.Run(t, func(t *testing.T) queuetest.Driver {
//			driver := memoryqueue.New()
//			t.Cleanup(func() {
//				_ = driver.Stop(context.Background())
//			})
//
//			return queuetest.Driver{Dispatcher: driver, Consumer: driver}
//		})
//	}
package queuetest
