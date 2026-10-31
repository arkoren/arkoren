// Package memoryqueue provides a non-durable, in-process queue driver for local
// development. It implements [queue.DispatcherDriver] and
// [queue.ConsumerDriver] with competing consumers, delayed retries, and
// exactly-once settlement per delivery. Messages are lost when the process
// exits. For tests, prefer the recording fake in package queuetest.
//
// Delayed retries are promoted lazily by consumers. Construction starts no
// goroutines or retry scheduler; timers work inside a testing/synctest bubble.
// Cancellation is checked before admitting each delivery, including while a
// queue has a backlog. An admitted handler still owns settlement; a detached
// settlement context can acknowledge or retry its delivery after cancellation.
// Undelivered messages and accepted retries remain queued for live consumers.
// Stop instead rejects new admissions, discards queued work, and waits for
// admitted handlers until its context expires.
//
// During application composition, register your printText handler
// (func(context.Context, string) error) before registering the runner:
//
//	driver := memoryqueue.New()
//	consumer := queue.NewConsumer(driver)
//	job := queue.NewJob[string]("print")
//	compose.Require(consumer.Handle(job, printText))
//	compose.Cleanup(driver.Stop)
//	compose.Run(consumer.Run)
//
// New takes Builder policies for RetryDelay and Logger. They run directly once
// in order on a fresh draft; nil entries panic and expanded nil/empty slices
// supply no edits. Publication detaches settings, not the logger. RetryDelay
// retains its existing meaning for a retry requesting zero delay. Construction
// starts no consumer or goroutine and retained drafts cannot alter scheduling.
package memoryqueue
