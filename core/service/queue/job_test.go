package queue_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// TestJobCallbacksShareDraftInOrder verifies exact invocation order and fluent
// receiver identity while final values combine settings from both callbacks.
func TestJobCallbacksShareDraftInOrder(t *testing.T) {
	t.Parallel()

	var draft *queue.JobBuilder
	var calls []int
	definition := queue.NewJob[email]("email", func(job *queue.JobBuilder) {
		draft = job
		calls = append(calls, 1)

		if job.OnQueue("emails").Retry(2).Backoff(time.Second, 2, time.Minute) != job {
			t.Fatal("setting returned a different receiver")
		}
	}, func(job *queue.JobBuilder) {
		if job != draft {
			t.Fatal("callbacks received different drafts")
		}

		calls = append(calls, 2)

		if job.Timeout(time.Minute).Description("email").BackoffFunc(nil) != job {
			t.Fatal("setting returned a different receiver")
		}
	})

	if !slices.Equal(calls, []int{1, 2}) {
		t.Fatalf("calls = %v", calls)
	}

	if definition.Queue() != "emails" || definition.Attempts() != 2 {
		t.Fatalf("definition = %+v", definition)
	}

	if definition.TimeoutValue() != time.Minute || definition.DescriptionValue() != "email" {
		t.Fatalf("definition = %+v", definition)
	}
}

// TestJobEmptyCallbackExpansions checks both expanded empty forms retain the
// normal constructor defaults rather than invoking a nil function.
func TestJobEmptyCallbackExpansions(t *testing.T) {
	t.Parallel()

	var omitted []func(*queue.JobBuilder)
	first := queue.NewJob[email]("email", omitted...)
	second := queue.NewJob[email]("email", []func(*queue.JobBuilder){}...)

	if first.Err() != nil || second.Err() != nil {
		t.Fatal("empty expansions produced an error")
	}

	if first.Queue() != queue.DefaultQueue || second.Queue() != queue.DefaultQueue {
		t.Fatal("empty expansions changed defaults")
	}
}

// TestJobPolicyReusesAcrossPayloadTypes checks the non-generic draft shares
// policy without coupling independent definitions or their payload types.
func TestJobPolicyReusesAcrossPayloadTypes(t *testing.T) {
	t.Parallel()

	first := queue.NewJob[string]("first", emailPolicy)
	second := queue.NewJob[email]("second", emailPolicy, func(job *queue.JobBuilder) {
		job.Retry(5)
	})

	if first.Attempts() != 3 || second.Attempts() != 5 {
		t.Fatalf("attempts = %d, %d", first.Attempts(), second.Attempts())
	}

	if first.Queue() != "emails" || second.Queue() != "emails" {
		t.Fatal("shared policy did not apply routing")
	}
}

// TestJobNilCallbackPanics checks nil is caller misuse, distinct from the
// valid BackoffFunc(nil) setting and ordinary invalid-definition errors.
func TestJobNilCallbackPanics(t *testing.T) {
	t.Parallel()

	checkConstructionPanic(t, func() {
		queue.NewJob[email]("email", nil)
	})
}

// TestJobCallbackPanicPropagates ensures constructors do not recover user
// callbacks; runtime handler recovery remains a separate consumer concern.
func TestJobCallbackPanicPropagates(t *testing.T) {
	t.Parallel()

	checkConstructionPanic(t, func() {
		queue.NewJob[email]("email", func(job *queue.JobBuilder) {
			panic("application callback")
		})
	})
}

// TestJobStickyErrorAcrossCallbacks ensures later valid settings and unchecked
// metadata/backoff edits cannot erase the earliest invalid-setting diagnostic.
func TestJobStickyErrorAcrossCallbacks(t *testing.T) {
	t.Parallel()

	definition := queue.NewJob[email]("email", func(job *queue.JobBuilder) {
		job.Retry(-1)
	}, func(job *queue.JobBuilder) {
		job.Retry(3).OnQueue("emails").Timeout(-time.Second).Description("kept").BackoffFunc(nil)
	})

	if err := definition.Err(); err == nil || !strings.Contains(err.Error(), "negative attempts") {
		t.Fatalf("Err = %v", err)
	}

	if definition.DescriptionValue() != "kept" {
		t.Fatal("unchecked description edit was lost")
	}
}

// TestJobRetainedDraftCannotChangePublishedPolicy verifies root detachment and
// independent reuse of the same policy callback for fresh definitions.
func TestJobRetainedDraftCannotChangePublishedPolicy(t *testing.T) {
	t.Parallel()

	var drafts []*queue.JobBuilder
	policy := func(job *queue.JobBuilder) {
		emailPolicy(job)

		drafts = append(drafts, job)
	}

	first := queue.NewJob[email]("email", policy)
	second := queue.NewJob[email]("email", policy)
	drafts[0].OnQueue("changed").Retry(1).Description("changed")

	*drafts[1] = queue.JobBuilder{}

	if drafts[0] == drafts[1] {
		t.Fatal("constructors shared drafts")
	}

	if first.Queue() != "emails" || second.Queue() != "emails" {
		t.Fatal("retained draft changed published routing")
	}

	if first.Attempts() != 3 || second.Attempts() != 3 {
		t.Fatal("retained draft changed published retry policy")
	}
}

// TestPublishedJobAndComponentsIgnoreConcurrentDraftEdits exercises publication
// under the race detector without sharing mutable state with the fake driver.
func TestPublishedJobAndComponentsIgnoreConcurrentDraftEdits(t *testing.T) {
	t.Parallel()

	var jobDraft *queue.JobBuilder
	definition := queue.NewJob[email]("email", func(job *queue.JobBuilder) {
		jobDraft = job
	})

	var dispatcherDraft *queue.DispatcherBuilder
	dispatcher := queue.NewDispatcher(discardDriver{}, func(dispatcher *queue.DispatcherBuilder) {
		dispatcherDraft = dispatcher
	})

	var consumerDraft *queue.ConsumerBuilder
	driver := &recordingDriver{
		failQueue: "default",
	}
	consumer := queue.NewConsumer(driver, func(consumer *queue.ConsumerBuilder) {
		consumerDraft = consumer
	})

	if err := consumer.Handle(definition, failWith(nil)); err != nil {
		t.Fatal(err)
	}

	var mutations sync.WaitGroup
	mutations.Go(func() {
		for range 100 {
			jobDraft.OnQueue("changed").Retry(1)
			dispatcherDraft.System("changed")
			consumerDraft.Queues("changed")
		}
	})
	t.Cleanup(mutations.Wait)

	for range 100 {
		if err := dispatcher.Dispatch(t.Context(), definition, email{}); err != nil {
			t.Fatal(err)
		}
	}

	if err := consumer.Run(t.Context()); err == nil {
		t.Fatal("expected driver failure")
	}
}

// ExampleNewJob shows the immutable published routing and attempt settings.
// Definition errors are checked before the value is registered or dispatched.
func ExampleNewJob() {
	job := queue.NewJob[string]("send", func(job *queue.JobBuilder) {
		job.OnQueue("work")
		job.Retry(3)
		job.Timeout(time.Minute)
	})

	if err := job.Err(); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(job.Name(), job.Queue(), job.Attempts(), job.TimeoutValue())
	// Output: send work 3 1m0s
}

// TestJobDefaults verifies an omitted callback publishes the normal routing
// and driver-selected retry policy without a construction error.
func TestJobDefaults(t *testing.T) {
	t.Parallel()

	plain := queue.NewJob[email]("plain")

	if plain.Name() != "plain" || plain.Queue() != queue.DefaultQueue {
		t.Fatalf("plain job = %#v", plain)
	}

	if plain.Attempts() != 0 || plain.Err() != nil {
		t.Fatalf("plain job = %#v", plain)
	}
}

// TestJobSharedEmailPolicy checks the reusable fixture policy still publishes
// its routing and total attempt limit through the non-generic draft.
func TestJobSharedEmailPolicy(t *testing.T) {
	t.Parallel()

	if emailJob.Queue() != "emails" || emailJob.Attempts() != 3 {
		t.Fatalf("email job = %#v", emailJob)
	}
}

// TestJobBuilderMetadataVariant checks a fresh definition can add metadata and
// a timeout without mutating another published definition with the same name.
func TestJobBuilderMetadataVariant(t *testing.T) {
	t.Parallel()

	plain := queue.NewJob[email]("plain")
	described := queue.NewJob[email]("plain", func(job *queue.JobBuilder) {
		job.Description("sends mail").Timeout(time.Second)
	})

	if described.DescriptionValue() != "sends mail" || described.TimeoutValue() != time.Second {
		t.Fatalf("described job = %#v", described)
	}

	if plain.DescriptionValue() != "" || plain.TimeoutValue() != 0 {
		t.Fatal("setters modified the original job")
	}
}

// TestJobErrInvalidName checks an invalid identity is retained as a definition
// error instead of panicking during construction.
func TestJobErrInvalidName(t *testing.T) {
	t.Parallel()

	checkJobHasError(t, queue.NewJob[email]("Bad.Name"))
}

// TestJobErrInvalidQueue verifies an empty queue becomes a definition error
// rather than replacing the default with an unusable route.
func TestJobErrInvalidQueue(t *testing.T) {
	t.Parallel()

	checkJobHasError(t, queue.NewJob[email]("ok", func(job *queue.JobBuilder) {
		job.OnQueue("")
	}))
}

// TestJobErrInvalidAttempts checks negative attempt limits are rejected in the
// published definition.
func TestJobErrInvalidAttempts(t *testing.T) {
	t.Parallel()

	checkJobHasError(t, queue.NewJob[email]("ok", func(job *queue.JobBuilder) {
		job.Retry(-1)
	}))
}

// TestJobErrInvalidBackoff verifies a negative backoff duration is retained as
// a construction error for later registration or dispatch.
func TestJobErrInvalidBackoff(t *testing.T) {
	t.Parallel()

	checkJobHasError(t, queue.NewJob[email]("ok", func(job *queue.JobBuilder) {
		job.Backoff(-time.Second, 2, 0)
	}))
}

// TestJobErrInvalidTimeout checks negative execution budgets produce a
// definition error rather than an immediately expired job.
func TestJobErrInvalidTimeout(t *testing.T) {
	t.Parallel()

	checkJobHasError(t, queue.NewJob[email]("ok", func(job *queue.JobBuilder) {
		job.Timeout(-time.Second)
	}))
}

// TestJobKeepsFirstError verifies a later invalid setting does not replace the
// first diagnostic retained by the job draft.
func TestJobKeepsFirstError(t *testing.T) {
	t.Parallel()

	first := queue.NewJob[email]("Bad", func(job *queue.JobBuilder) {
		job.Retry(-1)
	})

	if got := first.Err(); got == nil || !strings.Contains(got.Error(), "job name") {
		t.Fatalf("Err = %v, want the name error kept", got)
	}
}

// TestJobBackoffExponential checks successive attempts multiply the initial
// retry delay by the configured factor.
func TestJobBackoffExponential(t *testing.T) {
	t.Parallel()

	checkBackoffDelays(t, queue.NewJob[email]("a", func(job *queue.JobBuilder) {
		job.Backoff(time.Second, 2, 0)
	}),
		[]int{2, 3, 4},
		[]time.Duration{time.Second, 2 * time.Second, 4 * time.Second})
}

// TestJobBackoffCapped verifies exponential growth stops at the configured
// maximum retry delay.
func TestJobBackoffCapped(t *testing.T) {
	t.Parallel()

	checkBackoffDelays(t, queue.NewJob[email]("a", func(job *queue.JobBuilder) {
		job.Backoff(time.Second, 2, 3*time.Second)
	}),
		[]int{2, 3, 4},
		[]time.Duration{time.Second, 2 * time.Second, 3 * time.Second})
}

// TestJobBackoffConstant checks a growth factor below one is normalized to
// preserve the initial delay across retry attempts.
func TestJobBackoffConstant(t *testing.T) {
	t.Parallel()

	checkBackoffDelays(t, queue.NewJob[email]("a", func(job *queue.JobBuilder) {
		job.Backoff(time.Second, 0.5, 0)
	}),
		[]int{2, 3, 4},
		[]time.Duration{time.Second, time.Second, time.Second})
}

// TestJobBackoffOverflow checks extreme exponential growth saturates safely
// instead of wrapping into a negative retry delay.
func TestJobBackoffOverflow(t *testing.T) {
	t.Parallel()

	checkBackoffDelays(t, queue.NewJob[email]("a", func(job *queue.JobBuilder) {
		job.Backoff(time.Hour, 1000, 0)
	}),
		[]int{2, 3, 30},
		[]time.Duration{time.Hour, 1000 * time.Hour, math.MaxInt64})
}

// TestJobNilBackoffFuncRestoresDriverDefault checks that nil remains a valid
// runtime policy setting, not an invalid callback declaration.
func TestJobNilBackoffFuncRestoresDriverDefault(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[email]("emails-send", emailPolicy, func(job *queue.JobBuilder) {
		job.Backoff(time.Minute, 2, time.Hour).BackoffFunc(nil)
	})

	if err := job.Err(); err != nil {
		t.Fatal(err)
	}

	if delay := backoffDelay(t, job, 2); delay != 0 {
		t.Fatalf("retry delay = %s, want driver default request 0", delay)
	}
}

// TestZeroJobErr rejects an unconstructed definition independently of use.
func TestZeroJobErr(t *testing.T) {
	t.Parallel()

	var job queue.Job[email]

	if err := job.Err(); err == nil {
		t.Fatal("zero Job.Err() = nil")
	}
}

// TestJobBuilderSettingsCannotSupplyIdentity checks that an empty constructor
// identity stays invalid after builder settings are applied.
func TestJobBuilderSettingsCannotSupplyIdentity(t *testing.T) {
	t.Parallel()

	job := queue.NewJob[email]("", func(job *queue.JobBuilder) {
		job.OnQueue("emails").Retry(3)
	})

	if err := job.Err(); err == nil {
		t.Fatal("zero job with settings became valid")
	}
}

// TestInvalidJobIsRejected verifies both dispatch and handler registration
// reject an invalid definition with ErrInvalidName.
func TestInvalidJobIsRejected(t *testing.T) {
	t.Parallel()

	invalid := queue.NewJob[email]("Bad Name")
	consumer := queue.NewConsumer(jobConsumerDriver{})

	if err := consumer.Handle(invalid, failWith(nil)); !errors.Is(err, queue.ErrInvalidName) {
		t.Fatalf("Handle error = %v, want ErrInvalidName", err)
	}

	dispatcher := queue.NewDispatcher(queuetest.New())

	if err := dispatcher.Dispatch(context.Background(), invalid, email{}); !errors.Is(err, queue.ErrInvalidName) {
		t.Fatalf("Dispatch error = %v, want ErrInvalidName", err)
	}
}
