package storage

import (
	"strings"

	"arkoren.dev/core/service/queue"
)

// Queue is the name of the queue every job of the module runs on. A consumer
// that restricts its queues with queue.ConsumerBuilder.Queues must include it.
const Queue = "iam"

// NewJob returns the queue job named "<name>" for the default instance and
// "<name>-<instance>" for a named one, on [Queue]. Job names allow only
// [a-z0-9-], so the underscores of an instance become hyphens; the mapping is
// injective because an instance never contains a hyphen. An instance ending in
// an underscore therefore yields a name with a trailing hyphen, which the queue
// rejects: callers check [queue.Job.Err] when they build the job.
//
// Jobs are built per instance, not declared once per package, so two instances
// of the module can share one queue and one consumer without handing each
// other's work to the wrong handler.
func NewJob[T any](name, instance string) queue.Job[T] {
	if instance != "" {
		name += "-" + strings.ReplaceAll(instance, "_", "-")
	}

	return queue.NewJob[T](name, func(job *queue.JobBuilder) {
		job.OnQueue(Queue)
	})
}
