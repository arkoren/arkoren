package queuetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// Example dispatches a job through a Fake and runs it with Drain.
func Example() {
	ctx := context.Background()
	job := queue.NewJob[string]("greet")

	fake := queuetest.New()
	dispatcher := queue.NewDispatcher(fake)
	consumer := queue.NewConsumer(fake)

	if err := consumer.Handle(job, func(_ context.Context, name string) error {
		fmt.Println("hello", name)

		return nil
	}); err != nil {
		fmt.Println(err)

		return
	}

	if err := dispatcher.Dispatch(ctx, job, "world"); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("dispatched:", len(fake.Dispatched()))

	if err := fake.Drain(ctx, consumer); err != nil {
		fmt.Println(err)

		return
	}
	// Output:
	// dispatched: 1
	// hello world
}

// ExampleFake_AssertDispatched asserts on a decoded payload.
func ExampleFake_AssertDispatched() {
	t := &exampleT{}
	job := queue.NewJob[int]("count")
	fake := queuetest.New()

	if err := queue.NewDispatcher(fake).Dispatch(context.Background(), job, 3); err != nil {
		fmt.Println(err)

		return
	}

	fake.AssertDispatched(t, job, func(count int) bool {
		return count == 3
	})
	fake.AssertNotDispatched(t, job, func(count int) bool {
		return count == 4
	})
	fake.AssertDispatchedCount(t, job, 1)
	fmt.Println("failures:", t.failures)
	// Output: failures: 0
}

// exampleT is a minimal testing.TB for examples, counting failures.
type exampleT struct {
	// recorder supplies testing.TB methods not exercised by these examples.
	recorder

	// failures counts assertion errors without requiring a real testing.T.
	failures int
}

// Errorf counts the failure.
func (example *exampleT) Errorf(string, ...any) {
	example.failures++
}

// recorder is a testing.TB that records Errorf calls instead of failing.
type recorder struct {
	// TB supplies testing methods other than the recording overrides below.
	testing.TB

	// failures holds the formatted Errorf messages.
	failures []string
}

// Helper does nothing.
func (recorder *recorder) Helper() {
}

// Errorf records the formatted failure.
func (recorder *recorder) Errorf(format string, args ...any) {
	recorder.failures = append(recorder.failures, fmt.Sprintf(format, args...))
}

// email is a job payload used by the tests.
type email struct {
	// To is the recipient.
	To string `json:"to"`
}

// emailJob is the job of the test jobs.
var emailJob = queue.NewJob[email]("email-send", func(job *queue.JobBuilder) {
	job.OnQueue("mail")
})

// newPair returns a fake with a dispatcher and a consumer on it.
func newPair() (*queuetest.Fake, *queue.Dispatcher, *queue.Consumer) {
	fake := queuetest.New()

	return fake, queue.NewDispatcher(fake), queue.NewConsumer(fake)
}

// TestAssertions verifies the typed assertions pass and fail correctly.
func TestAssertions(t *testing.T) {
	t.Parallel()

	fake, dispatcher, _ := newPair()
	ctx := context.Background()

	fake.AssertNothingDispatched(t)

	if err := dispatcher.Dispatch(ctx, emailJob, email{
		To: "a@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	fake.AssertDispatched(t, emailJob, func(payload email) bool {
		return payload.To == "a@example.com"
	})
	fake.AssertDispatched(t, emailJob)
	fake.AssertNotDispatched(t, emailJob, func(payload email) bool {
		return payload.To == "b@example.com"
	})
	fake.AssertDispatchedCount(t, emailJob, 1)

	failing := &recorder{
		TB: t,
	}

	fake.AssertNothingDispatched(failing)
	fake.AssertDispatched(failing, emailJob, func(payload email) bool {
		return payload.To == "b@example.com"
	})
	fake.AssertNotDispatched(failing, emailJob)
	fake.AssertDispatchedCount(failing, emailJob, 2)

	if len(failing.failures) != 4 {
		t.Errorf("failures = %q, want four", failing.failures)
	}
}

// TestFailureInjection verifies FailWith, FailNext, and Reset.
func TestFailureInjection(t *testing.T) {
	t.Parallel()

	fake, dispatcher, _ := newPair()
	ctx := context.Background()
	boom := errors.New("boom")

	fake.FailNext(boom)

	if err := dispatcher.Dispatch(ctx, emailJob, email{}); !errors.Is(err, boom) {
		t.Fatalf("first Dispatch = %v, want boom", err)
	}

	if err := dispatcher.Dispatch(ctx, emailJob, email{}); err != nil {
		t.Fatalf("second Dispatch = %v, want success", err)
	}

	fake.FailWith(boom)

	for range 2 {
		if err := dispatcher.Dispatch(ctx, emailJob, email{}); !errors.Is(err, boom) {
			t.Fatalf("Dispatch = %v, want boom", err)
		}
	}

	fake.Reset()

	if err := dispatcher.Dispatch(ctx, emailJob, email{}); err != nil {
		t.Fatalf("Dispatch after Reset = %v, want success", err)
	}

	fake.AssertDispatchedCount(t, emailJob, 1)
}

// TestFakeAssertionsRequireEveryPredicate verifies all predicates must match.
func TestFakeAssertionsRequireEveryPredicate(t *testing.T) {
	fake, dispatcher, _ := newPair()

	if err := dispatcher.Dispatch(context.Background(), emailJob, email{
		To: "a@example.com",
	}); err != nil {
		t.Fatal(err)
	}

	isA := func(payload email) bool {
		return payload.To == "a@example.com"
	}

	isB := func(payload email) bool {
		return payload.To == "b@example.com"
	}

	fake.AssertDispatched(t, emailJob, isA, isA)
	fake.AssertNotDispatched(t, emailJob, isA, isB)

	failing := &recorder{
		TB: t,
	}

	fake.AssertDispatched(failing, emailJob, isA, isB)
	fake.AssertNotDispatched(failing, emailJob, isA, isA)

	if len(failing.failures) != 2 {
		t.Errorf("failures = %q, want two", failing.failures)
	}
}
