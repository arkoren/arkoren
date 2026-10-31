package queue_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"arkoren.dev/core/service/queue"
	"arkoren.dev/core/service/queue/queuetest"
)

// TestDispatcherDispatchesSerializedPayload checks dispatch assigns an ID and
// passes the job's route and JSON payload to the exposed driver.
func TestDispatcherDispatchesSerializedPayload(t *testing.T) {
	t.Parallel()

	driver := queuetest.New()
	dispatcher := queue.NewDispatcher(driver)

	if err := dispatcher.Dispatch(context.Background(), emailJob, email{
		ID: "email-1",
	}); err != nil {
		t.Fatal(err)
	}

	dispatched := driver.Dispatched()

	if len(dispatched) != 1 || dispatched[0].ID == "" {
		t.Fatalf("dispatched = %#v, want one message with an ID", dispatched)
	}

	want := queue.Message{
		ID:      dispatched[0].ID,
		Name:    "emails-send",
		Queue:   "emails",
		Payload: []byte(`{"id":"email-1"}`),
	}

	if !reflect.DeepEqual(dispatched[0], want) {
		t.Fatalf("message = %#v, want %#v", dispatched[0], want)
	}

	if dispatcher.Driver() != driver {
		t.Fatal("dispatcher did not expose its driver")
	}
}

// TestZeroJobDispatchRejectsBeforeEffects checks rejection before payload
// encoding and before the raw driver is called.
func TestZeroJobDispatchRejectsBeforeEffects(t *testing.T) {
	t.Parallel()

	var job queue.Job[marshalProbe]
	calls := 0
	driver := queuetest.New()
	dispatcher := queue.NewDispatcher(driver)

	err := dispatcher.Dispatch(context.Background(), job, marshalProbe{
		calls: &calls,
	})

	if !errors.Is(err, queue.ErrInvalidName) {
		t.Fatalf("Dispatch error = %v, want ErrInvalidName", err)
	}

	if calls != 0 {
		t.Fatalf("payload encoded %d times", calls)
	}

	driver.AssertNothingDispatched(t)
}
