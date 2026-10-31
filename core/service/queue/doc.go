// Package queue provides transport-neutral asynchronous queue dispatch and
// execution.
//
// A [Job] is an immutable definition: its name, queue, retry
// attempts, backoff, and per-attempt timeout. A [Dispatcher] sends a payload
// for a job, and a [Consumer] registers one handler per job and runs every
// queue with a handled job through [Consumer.Run]:
//
//	var SendWelcome = queue.NewJob[Welcome]("send-welcome", func(job *queue.JobBuilder) {
//		job.OnQueue("emails")
//		job.Retry(5)
//		job.Backoff(time.Second, 2, time.Minute)
//	})
//
// Dispatch from a request or service operation, using an existing dispatcher:
//
//	if err := dispatcher.Dispatch(ctx, SendWelcome, Welcome{UserID: 1}); err != nil {
//		return err
//	}
//
// Register the worker handler and runner during application composition:
//
//	compose.Require(consumer.Handle(SendWelcome, sendMail))
//	compose.Run(consumer.Run)
//
// Always construct definitions with [NewJob]. A zero [Job] is invalid:
// [Job.Err] reports it, and [Consumer.Handle] and [Dispatcher.Dispatch] reject
// it with [ErrInvalidName] before registration or payload encoding.
//
// Configuration is fixed at construction through role-specific [JobBuilder],
// [ConsumerBuilder], and [DispatcherBuilder] callbacks. Callbacks run directly
// once, synchronously in order on one fresh draft; supplied nil callbacks panic,
// while expanded nil or empty slices supply no callbacks. Constructors detach
// completed drafts. [ConsumerBuilder.Queues] also copies its input slice.
// Reusing a callback does not share mutable restrictions between consumers.
// Each Queues call replaces the restriction; an empty list restores
// consumption of all handled queues. Telemetry and event-bus pointers remain
// shared services, and nil telemetry follows [telemetry.Default] per operation.
//
// A handler settles a delivery by returning: nil acknowledges, an ordinary
// error retries within the job's limits, and [RetryAfter], [Reject], and
// [Permanent] choose the outcome explicitly.
// Consumer cancellation requests retry without applying the immediate attempt
// cap, but does not reset or discount the driver's reported delivery count.
// Settlement uses a fresh 30-second context after execution, retaining context
// values independently of consumer cancellation. Drivers must honor that budget;
// synchronous event listeners must also cooperate with their context.
//
// [Message.Metadata] travels beside each payload; when telemetry is enabled
// [Dispatcher.Dispatch] injects the trace context into it and [Consumer]
// continues that trace in a consumer span per delivery attempt. Both component
// builders expose Telemetry and System settings; dispatchers have no consumer-
// only Queues, UnknownNameAttempts, or Events settings.
//
// With [ConsumerBuilder.Events], a [Consumer] publishes, for every driver, [JobFailed]
// ("queue.job.failed") when it fails a delivery for good, [JobRetried]
// ("queue.job.retried") when it schedules another attempt, [JobRejected]
// ("queue.job.rejected") when a job rejects its message, and [JobTimedOut]
// ("queue.job.timed-out") when an attempt errors after its timeout elapsed.
// A timeout is followed by the event for its settlement, usually
// [JobRetried].
package queue
