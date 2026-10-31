package broadcasttest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
)

// Example publishes through a Fake and delivers to a listener with Drain.
func Example() {
	ctx := context.Background()
	topic := broadcast.NewTopic[string]("greetings.sent")

	fake := broadcasttest.New()
	publisher := broadcast.NewPublisher(fake)
	consumer := broadcast.NewConsumer(fake)

	unsubscribe, err := consumer.Listen(topic, func(_ context.Context, name string) error {
		fmt.Println("hello", name)

		return nil
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	defer unsubscribe()

	if err := publisher.Publish(ctx, topic, "world"); err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("published:", len(fake.Published()))

	if err := fake.Drain(ctx, consumer); err != nil {
		fmt.Println(err)

		return
	}
	// Output:
	// published: 1
	// hello world
}

// ExampleFake_AssertPublished asserts on a decoded payload.
func ExampleFake_AssertPublished() {
	t := &exampleT{}
	topic := broadcast.NewTopic[int]("counter.changed")
	fake := broadcasttest.New()

	if err := broadcast.NewPublisher(fake).Publish(context.Background(), topic, 3); err != nil {
		fmt.Println(err)

		return
	}

	fake.AssertPublished(t, topic, func(count int) bool {
		return count == 3
	})
	fake.AssertNotPublished(t, topic, func(count int) bool {
		return count == 4
	})
	fake.AssertPublishedCount(t, topic, 1)
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

// signup is a broadcast payload used by the tests.
type signup struct {
	// User is the user name.
	User string `json:"user"`
}

// signupTopic is the topic of the test broadcasts.
var signupTopic = broadcast.NewTopic[signup]("users.signed-up")

// newPair returns a fake with a publisher and a consumer on it.
func newPair() (*broadcasttest.Fake, *broadcast.Publisher, *broadcast.Consumer) {
	fake := broadcasttest.New()

	return fake, broadcast.NewPublisher(fake), broadcast.NewConsumer(fake)
}

// TestAssertions verifies the typed assertions pass and fail correctly.
func TestAssertions(t *testing.T) {
	t.Parallel()

	fake, publisher, _ := newPair()

	fake.AssertNothingPublished(t)

	if err := publisher.Publish(context.Background(), signupTopic, signup{
		User: "ann",
	}); err != nil {
		t.Fatal(err)
	}

	fake.AssertPublished(t, signupTopic, func(payload signup) bool {
		return payload.User == "ann"
	})
	fake.AssertPublished(t, signupTopic)
	fake.AssertNotPublished(t, signupTopic, func(payload signup) bool {
		return payload.User == "bob"
	})
	fake.AssertPublishedCount(t, signupTopic, 1)

	failing := &recorder{
		TB: t,
	}

	fake.AssertNothingPublished(failing)
	fake.AssertPublished(failing, signupTopic, func(payload signup) bool {
		return payload.User == "bob"
	})
	fake.AssertNotPublished(failing, signupTopic)
	fake.AssertPublishedCount(failing, signupTopic, 2)

	if len(failing.failures) != 4 {
		t.Errorf("failures = %q, want four", failing.failures)
	}
}

// TestFailureInjection verifies FailWith, FailNext, and Reset.
func TestFailureInjection(t *testing.T) {
	t.Parallel()

	fake, publisher, _ := newPair()
	ctx := context.Background()
	boom := errors.New("boom")

	fake.FailNext(boom)

	if err := publisher.Publish(ctx, signupTopic, signup{}); !errors.Is(err, boom) {
		t.Fatalf("first Publish = %v, want boom", err)
	}

	if err := publisher.Publish(ctx, signupTopic, signup{}); err != nil {
		t.Fatalf("second Publish = %v, want success", err)
	}

	fake.FailWith(boom)

	if err := publisher.Publish(ctx, signupTopic, signup{}); !errors.Is(err, boom) {
		t.Fatalf("Publish = %v, want boom", err)
	}

	fake.Reset()

	fake.AssertNothingPublished(t)

	if err := publisher.Publish(ctx, signupTopic, signup{}); err != nil {
		t.Fatalf("Publish after Reset = %v, want success", err)
	}
}

// TestDrainRoutesThroughListeners verifies Drain runs the real consumer once per message.
func TestDrainRoutesThroughListeners(t *testing.T) {
	t.Parallel()

	fake, publisher, consumer := newPair()
	ctx := context.Background()
	var users []string

	_, err := consumer.Listen(signupTopic, func(_ context.Context, payload signup) error {
		users = append(users, payload.User)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	var names []string

	_, err = consumer.ListenPattern("users.#", func(_ context.Context, message broadcast.Message) error {
		names = append(names, message.Name)

		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	for _, user := range []string{"ann", "bob"} {
		if err := publisher.Publish(ctx, signupTopic, signup{
			User: user,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if len(fake.Undelivered()) != 2 {
		t.Fatalf("undelivered = %d, want 2", len(fake.Undelivered()))
	}

	for range 2 {
		if err := fake.Drain(ctx, consumer); err != nil {
			t.Fatal(err)
		}
	}

	if fmt.Sprint(users) != "[ann bob]" || len(names) != 2 || len(fake.Undelivered()) != 0 {
		t.Errorf("users = %v, names = %v, undelivered = %d", users, names, len(fake.Undelivered()))
	}
}

// TestDrainReportsListenerErrorsOnce verifies a failing listener is reported and not redelivered.
func TestDrainReportsListenerErrorsOnce(t *testing.T) {
	t.Parallel()

	fake, publisher, consumer := newPair()
	ctx := context.Background()
	boom := errors.New("boom")

	_, err := consumer.Listen(signupTopic, func(context.Context, signup) error {
		return boom
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := publisher.Publish(ctx, signupTopic, signup{}); err != nil {
		t.Fatal(err)
	}

	if err := fake.Drain(ctx, consumer); !errors.Is(err, boom) {
		t.Fatalf("Drain = %v, want boom", err)
	}

	if err := fake.Drain(ctx, consumer); err != nil {
		t.Fatalf("second Drain = %v, want nil", err)
	}
}

// TestDrainGuardsAgainstEndlessPublishing verifies the delivery limit.
func TestDrainGuardsAgainstEndlessPublishing(t *testing.T) {
	t.Parallel()

	fake, publisher, consumer := newPair()
	ctx := context.Background()

	_, err := consumer.Listen(signupTopic, func(ctx context.Context, payload signup) error {
		return publisher.Publish(ctx, signupTopic, payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := publisher.Publish(ctx, signupTopic, signup{}); err != nil {
		t.Fatal(err)
	}

	if err := fake.Drain(ctx, consumer); !errors.Is(err, broadcasttest.ErrDrainLimit) {
		t.Fatalf("Drain = %v, want ErrDrainLimit", err)
	}
}

// TestDrainRejectsForeignConsumer verifies Drain refuses other consumers.
func TestDrainRejectsForeignConsumer(t *testing.T) {
	t.Parallel()

	consumer := broadcast.NewConsumer(broadcasttest.New())

	if err := broadcasttest.New().Drain(context.Background(), consumer); !errors.Is(err, broadcasttest.ErrForeignConsumer) {
		t.Fatalf("Drain = %v, want ErrForeignConsumer", err)
	}
}

// TestFakeAssertionsRequireEveryPredicate verifies all predicates must match.
func TestFakeAssertionsRequireEveryPredicate(t *testing.T) {
	fake := broadcasttest.New()

	if err := broadcast.NewPublisher(fake).Publish(t.Context(), signupTopic, signup{
		User: "ann",
	}); err != nil {
		t.Fatal(err)
	}

	isAnn := func(payload signup) bool {
		return payload.User == "ann"
	}

	isBob := func(payload signup) bool {
		return payload.User == "bob"
	}

	fake.AssertPublished(t, signupTopic, isAnn, isAnn)
	fake.AssertNotPublished(t, signupTopic, isAnn, isBob)

	failing := &recorder{
		TB: t,
	}

	fake.AssertPublished(failing, signupTopic, isAnn, isBob)
	fake.AssertNotPublished(failing, signupTopic, isAnn, isAnn)

	if len(failing.failures) != 2 {
		t.Errorf("failures = %q, want two", failing.failures)
	}
}
