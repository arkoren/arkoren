package queue

import (
	"fmt"
	"math"
	"time"

	"arkoren.dev/core/internal/name"
)

// DefaultQueue is the queue used by jobs created without [JobBuilder.OnQueue].
// It is a literal routing destination, not an instruction to a native broker.
//
//	job := queue.NewJob[string]("send")
//	fmt.Println(job.Queue() == queue.DefaultQueue)
const DefaultQueue = "default"

// Job names one kind of job and fixes the payload type T it carries, together
// with the queue, retry, and timeout settings that are application code
// rather than transport data. Create it with [NewJob] and configure it through
// a [JobBuilder] callback before publication.
//
// A Job is an immutable value: its builder is detached at publication, and the
// first error found is kept and reported by [Job.Err]. [Consumer.Handle] and
// [Dispatcher.Dispatch] reject a job whose Err is not nil with
// [ErrInvalidName]. Always create jobs with [NewJob]; the zero value has no
// name and no queue, and [Job.Err] reports it as invalid.
//
// Example:
//
//	var SendWelcome = queue.NewJob[Welcome]("send-welcome", func(job *queue.JobBuilder) {
//		job.OnQueue("emails")
//		job.Retry(5)
//		job.Backoff(time.Second, 2, time.Minute)
//		job.Timeout(30 * time.Second)
//	})
type Job[T any] struct {
	// jobSettings is copied from the completed construction draft.
	jobSettings
}

// JobBuilder configures retry policy and routing before an immutable Job is
// published. Every method returns this same draft. Retained drafts cannot
// change published jobs; custom backoff closures retain their own captures.
//
//	job := queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.OnQueue("emails").Retry(5)
//	})
type JobBuilder struct {
	// jobSettings holds the construction draft, including its first error.
	jobSettings
}

// jobSettings is the non-generic routing and worker policy shared by a job
// and its construction draft. Publication copies it, never the draft pointer.
type jobSettings struct {
	// name is the stable application identifier of the job.
	name string

	// queue is the queue the job is dispatched to.
	queue string

	// attempts is the total allowed attempts. Zero uses the driver default.
	attempts int

	// backoff returns the delay before the given retry attempt. Nil uses the
	// driver default.
	backoff func(attempt int) time.Duration

	// timeout bounds one attempt. Zero means no limit.
	timeout time.Duration

	// description is free text for humans.
	description string

	// err is the first error found while building the job.
	err error
}

// NewJob returns the job called jobName that carries payloads of type T and runs
// on [DefaultQueue]. The name is the stable identifier used to find the
// handler of a delivery; it must satisfy the shared identifier rule of 1-64
// lowercase letters, digits, or hyphens with no hyphen at either end. An
// invalid name is reported by [Job.Err] rather than by a panic.
// Callbacks run directly once, synchronously and in order on one fresh draft,
// even when the name is invalid. Nil entries panic; expanded nil or empty slices
// supply no callbacks. The first validation error is retained across callbacks.
// Retained drafts cannot change the published job; closure captures stay shared.
//
//	job := queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.Retry(3)
//	})
//	if err := job.Err(); err != nil {
//		return err
//	}
func NewJob[T any](jobName string, configure ...JobBuilderFunc) Job[T] {
	job := JobBuilder{
		jobSettings: jobSettings{
			name:  jobName,
			queue: DefaultQueue,
		},
	}

	if err := name.Identifier(jobName); err != nil {
		job.err = fmt.Errorf("job name: %w", err)
	}

	for _, configure := range configure {
		configure(&job)
	}

	return Job[T](job)
}

// OnQueue sets the queue the job is dispatched to. An empty queue is an
// error. The default is [DefaultQueue]. It returns the same draft and preserves
// any earlier validation error.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.OnQueue("emails")
//	})
func (job *JobBuilder) OnQueue(queue string) *JobBuilder {
	if job.err != nil {
		return job
	}

	if queue == "" {
		job.err = fmt.Errorf("job %s: empty queue", job.name)

		return job
	}

	job.queue = queue

	return job
}

// Retry sets the total number of attempts, including the first one, before an
// ordinary handler error becomes a permanent failure. Zero uses the driver
// default and a negative value is an error. The limit is enforced only when
// the driver reports [Message.Attempts]; otherwise the driver's own
// redelivery limit applies.
// The same draft is returned; any earlier validation error is preserved.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.Retry(3)
//	})
func (job *JobBuilder) Retry(attempts int) *JobBuilder {
	if job.err != nil {
		return job
	}

	if attempts < 0 {
		job.err = fmt.Errorf("job %s: negative attempts %d", job.name, attempts)

		return job
	}

	job.attempts = attempts

	return job
}

// Backoff sets the delay before the first retry, the factor that multiplies
// it after each retry, and the cap on the delay. A zero maximum means no cap
// and a factor below one counts as one (constant delay). A negative initial
// or maximum is an error. It replaces any earlier [JobBuilder.BackoffFunc].
// NaN also normalizes to one; overflow saturates at the largest duration.
// It returns the same draft, preserving any earlier validation error.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.Backoff(time.Second, 2, time.Minute)
//	})
func (job *JobBuilder) Backoff(initial time.Duration, factor float64, maximum time.Duration) *JobBuilder {
	if job.err != nil {
		return job
	}

	if initial < 0 || maximum < 0 {
		job.err = fmt.Errorf("job %s: negative backoff", job.name)

		return job
	}

	if factor < 1 || math.IsNaN(factor) {
		factor = 1
	}

	job.backoff = func(attempt int) time.Duration {
		delay := float64(initial) * math.Pow(factor, float64(max(attempt-2, 0)))

		if maximum > 0 {
			delay = min(delay, float64(maximum))
		}

		if delay >= math.MaxInt64 {
			return math.MaxInt64
		}

		return time.Duration(delay)
	}

	return job
}

// BackoffFunc sets a custom delay before a retry. fn receives the number of
// the attempt about to run (always at least 2). Negative delays count as
// zero. A nil fn restores the driver default. It replaces any earlier
// [JobBuilder.Backoff]. It returns the same draft and does not clear an earlier
// error. The function and its captured state remain application-owned.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.BackoffFunc(nil)
//	})
func (job *JobBuilder) BackoffFunc(fn func(attempt int) time.Duration) *JobBuilder {
	job.backoff = fn

	return job
}

// Timeout bounds each attempt of the handler. An attempt that exceeds it has
// its context canceled and counts as a failed attempt; an attempt interrupted
// by consumer shutdown is retried without applying the attempt cap to that
// interruption. Reported delivery counts still advance and can exhaust the cap
// on a later ordinary failure. Zero means no limit and a
// negative value is an error. It returns the same draft and preserves earlier
// validation errors.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.Timeout(time.Minute)
//	})
func (job *JobBuilder) Timeout(duration time.Duration) *JobBuilder {
	if job.err != nil {
		return job
	}

	if duration < 0 {
		job.err = fmt.Errorf("job %s: negative timeout %s", job.name, duration)

		return job
	}

	job.timeout = duration

	return job
}

// Description sets free text for people and returns the same draft. It does
// not affect delivery or clear an earlier validation error.
//
//	queue.NewJob[string]("email", func(job *queue.JobBuilder) {
//		job.Description("Sends welcome mail")
//	})
func (job *JobBuilder) Description(text string) *JobBuilder {
	job.description = text

	return job
}

// Name returns the job name.
// It is empty for the invalid zero Job; NewJob preserves the declared identity.
//
//	job := queue.NewJob[string]("send")
//	fmt.Println(job.Name()) // send
func (job Job[T]) Name() string {
	return job.name
}

// Queue returns the queue the job is dispatched to.
// NewJob defaults to DefaultQueue; a zero Job has no queue and must not be used.
//
//	job := queue.NewJob[string]("send")
//	fmt.Println(job.Queue()) // default
func (job Job[T]) Queue() string {
	return job.queue
}

// Attempts returns the total allowed attempts, or zero for the driver
// default.
// It counts the first delivery as an attempt when drivers report delivery counts.
//
//	job := queue.NewJob[string]("send", func(job *queue.JobBuilder) {
//		job.Retry(3)
//	})
//	fmt.Println(job.Attempts()) // 3
func (job Job[T]) Attempts() int {
	return job.attempts
}

// TimeoutValue returns the bound on one attempt, or zero for no limit.
// Execution must cooperate with its canceled context; this is not preemption.
//
//	job := queue.NewJob[string]("send")
//	fmt.Println(job.TimeoutValue() == 0) // true
func (job Job[T]) TimeoutValue() time.Duration {
	return job.timeout
}

// DescriptionValue returns the free text set with [JobBuilder.Description].
// An omitted description is empty and does not affect routing or execution.
//
//	job := queue.NewJob[string]("send", func(job *queue.JobBuilder) {
//		job.Description("Sends a reminder")
//	})
//	fmt.Println(job.DescriptionValue())
func (job Job[T]) DescriptionValue() string {
	return job.description
}

// Err returns the first error found while building the job, or nil for a
// valid definition. The zero Job is invalid; only [NewJob] supplies its identity.
// [Consumer.Handle] and [Dispatcher.Dispatch] wrap this error with
// [ErrInvalidName] before registering a handler or encoding a payload.
//
//	job := queue.NewJob[string]("send-welcome")
//	if err := job.Err(); err != nil {
//		return err
//	}
func (job Job[T]) Err() error {
	if job.err != nil {
		return job.err
	}

	if job.name == "" {
		return fmt.Errorf("job name: %w", name.Identifier(job.name))
	}

	return nil
}

// JobBuilderFunc edits one fresh JobBuilder during NewJob. Policies run once
// in order even for invalid declarations; nil entries panic. Retaining the
// draft cannot mutate a published Job, but captured application state is shared.
//
//	policy := queue.JobBuilderFunc(func(job *queue.JobBuilder) {
//		job.Retry(3)
//	})
//	job := queue.NewJob[string]("send", policy)
//	if err := job.Err(); err != nil {
//		return err
//	}
type JobBuilderFunc = func(*JobBuilder)
