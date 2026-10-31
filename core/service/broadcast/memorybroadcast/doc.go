// Package memorybroadcast provides an asynchronous, in-process broadcast driver for
// local development. It implements [broadcast.PublisherDriver] and
// [broadcast.ConsumerDriver]: every running Consume call receives its own copy
// of each message published after it started. For tests, prefer the recording
// fake in package broadcasttest.
//
// Construction starts no consumers. Run a consumer in a supervised goroutine,
// then join it when canceled. Stop separately bounds the wait for handlers:
//
//	driver := memorybroadcast.New()
//	consumer := broadcast.NewConsumer(driver)
//	ctx, cancel := context.WithCancel(context.Background())
//	result := make(chan error, 1)
//	go func() {
//		result <- consumer.Run(ctx)
//	}()
//	cancel()
//	if err := <-result; !errors.Is(err, context.Canceled) {
//		log.Fatal(err)
//	}
//	if err := driver.Stop(context.Background()); err != nil {
//		log.Fatal(err)
//	}
//
// New takes Builder policies for MaxConcurrentDeliveries and Logger. Policies
// run directly once in order on a fresh draft; nil entries panic and expanded
// nil/empty slices supply no edits. Nonpositive limits normalize to the default
// after all policies. Retained drafts cannot reconfigure a running broker;
// logger dependencies remain shared and construction starts no runners.
//
// # Admission and shutdown
//
// Delivery admission waits for a shared handler slot. Canceling Consume ends
// that wait with the context error; an already-canceled context is rejected
// before handler validation or broker-closed checks. Stop closes admission
// before waiting for admitted handlers. Buffered messages and messages waiting
// for a slot are not drained into handlers after cancellation or shutdown.
//
// Admitted handlers keep the consumer context's values, without its deadline
// or cancellation. They can finish after Consume returns. Stop waits for these
// handlers only until its own context ends; even an expired Stop context
// closes admission and signals consumers. Stop does not wait for Consume calls
// to return. An admitted handler may start or finish after Stop returns, but
// no new delivery is admitted. Repeated Stop calls can wait for the same
// handlers again. This in-process broker provides no durable retention.
package memorybroadcast
