package memorybroadcast_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/core/service/broadcast/memorybroadcast"
)

// Example limits concurrent deliveries in an in-process broker. Construction
// starts no consumers; this example only configures the broker.
func Example() {
	broker := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
		broker.MaxConcurrentDeliveries(8)
	})

	_ = broker
}

// ExampleDriver_Consume demonstrates that an already-canceled consumer does
// not subscribe, even when the broker has already been stopped.
func ExampleDriver_Consume() {
	driver := memorybroadcast.New()

	if err := driver.Stop(context.Background()); err != nil {
		fmt.Println(err)

		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := driver.Consume(ctx, func(context.Context, broadcast.Message) error {
		return nil
	})
	fmt.Println(errors.Is(err, context.Canceled))
	// Output: true
}

// ExampleDriver_Stop shows that closing admission permanently rejects later
// publications. No consumers or in-flight handlers are needed for this path.
func ExampleDriver_Stop() {
	driver := memorybroadcast.New()

	if err := driver.Stop(context.Background()); err != nil {
		fmt.Println(err)

		return
	}

	err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"})
	fmt.Println(errors.Is(err, memorybroadcast.ErrBrokerClosed))
	// Output: true
}

// TestConformance runs the broadcast conformance suite against the memory driver.
func TestConformance(t *testing.T) {
	t.Parallel()

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		driver := memorybroadcast.New()
		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return broadcasttest.Driver{
			Publisher: driver,
			Consumer:  driver,
		}
	}, broadcasttest.WithSynctest())
}

// admissionContext provides a public context-protocol rendezvous after the
// test arms it. Its parent supplies ordinary context behavior; no broker
// fields or implementation hooks are exposed to the test.
type admissionContext struct {
	// Context supplies cancellation, deadlines, values, and the final error.
	context.Context

	// armed enables interception after Consume has established a subscription.
	armed chan struct{}

	// reached reports that admission is querying cancellation before launch.
	reached chan struct{}

	// resume lets the cancellation query finish after shutdown has returned.
	resume chan struct{}

	// once limits the rendezvous to one lookup and keeps Err concurrency-safe.
	once sync.Once
}

// Err pauses the first lookup after armed closes, then returns the parent's
// error unchanged. Waiting happens only at the context protocol boundary,
// so the test can order shutdown without inspecting broker state.
func (ctx *admissionContext) Err() error {
	select {
	case <-ctx.armed:
		ctx.once.Do(func() {
			close(ctx.reached)
			<-ctx.resume
		})
	default:
	}

	return ctx.Context.Err()
}

// TestEveryConsumerReceivesEachMessage checks independent subscriptions both
// receive publications instead of competing for a single delivery.
func TestEveryConsumerReceivesEachMessage(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		received := make(chan broadcast.Message, 2)
		consumers := make(chan error, 2)

		for range 2 {
			go func() {
				consumers <- driver.Consume(ctx, func(_ context.Context, message broadcast.Message) error {
					received <- message

					return nil
				})
			}()
		}

		synctest.Wait()

		if err := driver.Publish(context.Background(), broadcast.Message{
			Name: "users.created",
		}); err != nil {
			t.Fatal(err)
		}

		for range 2 {
			if message := <-received; message.Name != "users.created" {
				t.Errorf("delivered name = %q, want users.created", message.Name)
			}
		}

		cancel()

		for range 2 {
			if err := <-consumers; !errors.Is(err, context.Canceled) {
				t.Errorf("Consume = %v, want context.Canceled", err)
			}
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopStopsConsume verifies broker shutdown releases a consumer even when
// its own context has not been canceled.
func TestStopStopsConsume(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New()
		stopped := make(chan error, 1)

		go func() {
			stopped <- driver.Consume(context.Background(), func(context.Context, broadcast.Message) error {
				return nil
			})
		}()

		synctest.Wait()

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}

		if err := <-stopped; !errors.Is(err, memorybroadcast.ErrBrokerClosed) {
			t.Fatalf("Consume = %v, want ErrBrokerClosed", err)
		}
	})
}

// TestConsumeAlreadyCanceledPrecedesNilHandler checks cancellation is the
// first result even when no handler was supplied. No subscription may start.
func TestConsumeAlreadyCanceledPrecedesNilHandler(t *testing.T) {
	t.Parallel()

	driver := memorybroadcast.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := driver.Consume(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want context.Canceled", err)
	}
}

// TestConsumeAlreadyCanceledPrecedesClosedBroker checks a stopped broker
// still honors the contract's already-canceled context result.
func TestConsumeAlreadyCanceledPrecedesClosedBroker(t *testing.T) {
	t.Parallel()

	driver := memorybroadcast.New()

	if err := driver.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := driver.Consume(ctx, func(context.Context, broadcast.Message) error {
		return nil
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume = %v, want context.Canceled", err)
	}
}

// TestConsumeCancellationInterruptsDeliveryAdmission fills the sole handler
// slot, then cancels a consumer waiting to admit its next message. Cancellation
// must release Consume without waiting for the detached, in-flight handler.
func TestConsumeCancellationInterruptsDeliveryAdmission(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
			broker.MaxConcurrentDeliveries(1)
		})

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		started := make(chan context.Context, 2)
		release := make(chan struct{})
		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(ctx, func(ctx context.Context, _ broadcast.Message) error {
				started <- ctx
				<-release

				return nil
			})
		}()

		synctest.Wait()

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"}); err != nil {
			t.Fatal(err)
		}

		inflight := <-started

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.updated"}); err != nil {
			t.Error(err)
		}

		synctest.Wait()

		cancel()
		synctest.Wait()

		select {
		case err := <-consumer:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Consume = %v, want context.Canceled", err)
			}
		default:
			t.Error("Consume is still waiting for a handler slot after cancellation")
		}

		if err := inflight.Err(); err != nil {
			t.Errorf("in-flight handler context = %v, want detached context", err)
		}

		close(release)
		synctest.Wait()

		select {
		case <-started:
			t.Error("pending delivery started after consumer cancellation")
		default:
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopInterruptsDeliveryAdmissionBeforeDraining distinguishes admission
// shutdown from draining. Consume must stop while Stop still waits for the
// admitted handler, and the pending delivery must never start.
func TestStopInterruptsDeliveryAdmissionBeforeDraining(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
			broker.MaxConcurrentDeliveries(1)
		})

		started := make(chan broadcast.Message, 2)
		release := make(chan struct{})
		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(context.Background(), func(_ context.Context, message broadcast.Message) error {
				started <- message
				<-release

				return nil
			})
		}()

		synctest.Wait()

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"}); err != nil {
			t.Fatal(err)
		}

		<-started

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.updated"}); err != nil {
			t.Error(err)
		}

		synctest.Wait()

		stopped := make(chan error, 1)

		go func() {
			stopped <- driver.Stop(context.Background())
			close(stopped)
		}()

		synctest.Wait()

		select {
		case err := <-consumer:
			if !errors.Is(err, memorybroadcast.ErrBrokerClosed) {
				t.Errorf("Consume = %v, want ErrBrokerClosed", err)
			}
		default:
			t.Error("Consume is still waiting for a handler slot after shutdown")
		}

		select {
		case err := <-stopped:
			t.Errorf("Stop returned %v before the in-flight handler finished", err)
		default:
		}

		close(release)
		synctest.Wait()

		select {
		case message := <-started:
			t.Errorf("pending delivery %q started during shutdown", message.Name)
		default:
		}

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	})
}

// TestExpiredStopPreventsPendingDelivery returns from Stop while a handler
// owns the sole slot. Releasing that slot later must not launch queued work,
// and publishing must remain closed even though the handler was not drained.
func TestExpiredStopPreventsPendingDelivery(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New(func(broker *memorybroadcast.Builder) {
			broker.MaxConcurrentDeliveries(1)
		})

		started := make(chan broadcast.Message, 2)
		release := make(chan struct{})
		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(context.Background(), func(_ context.Context, message broadcast.Message) error {
				started <- message
				<-release

				return nil
			})
		}()

		synctest.Wait()

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"}); err != nil {
			t.Fatal(err)
		}

		<-started

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.updated"}); err != nil {
			t.Error(err)
		}

		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := driver.Stop(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Stop = %v, want context.Canceled", err)
		}

		err := driver.Publish(context.Background(), broadcast.Message{Name: "users.deleted"})

		if !errors.Is(err, memorybroadcast.ErrBrokerClosed) {
			t.Errorf("Publish after Stop = %v, want ErrBrokerClosed", err)
		}

		close(release)
		synctest.Wait()

		select {
		case message := <-started:
			t.Errorf("pending delivery %q started after Stop returned", message.Name)
		default:
		}

		if err := <-consumer; !errors.Is(err, memorybroadcast.ErrBrokerClosed) {
			t.Errorf("Consume = %v, want ErrBrokerClosed", err)
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopClosesAdmissionBeforeFirstHandlerRegistration pauses the public
// cancellation protocol before the first delivery launches. Stop may finish
// with no admitted work; resuming that delivery must not register late work.
func TestStopClosesAdmissionBeforeFirstHandlerRegistration(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New()
		ctx := &admissionContext{
			Context: context.Background(),
			armed:   make(chan struct{}),
			reached: make(chan struct{}),
			resume:  make(chan struct{}),
		}
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(ctx, func(context.Context, broadcast.Message) error {
				started <- struct{}{}
				<-release

				return nil
			})
		}()

		synctest.Wait()
		close(ctx.armed)

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"}); err != nil {
			t.Fatal(err)
		}

		select {
		case <-ctx.reached:
		case <-started:
			t.Error("delivery launched without checking cancellation at admission")
			close(release)

			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}

			return
		}

		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}

		close(ctx.resume)
		close(release)

		if err := <-consumer; !errors.Is(err, memorybroadcast.ErrBrokerClosed) {
			t.Errorf("Consume = %v, want ErrBrokerClosed", err)
		}

		synctest.Wait()

		select {
		case <-started:
			t.Error("delivery registered after Stop returned with no in-flight handlers")
		default:
		}
	})
}

// TestStopDeadlineKeepsInFlightContextDetached bounds the drain with virtual
// time while preserving handler values and detaching cancellation/deadlines.
// A later Stop must still wait for that handler, not abandon its accounting.
func TestStopDeadlineKeepsInFlightContextDetached(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := memorybroadcast.New()
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), t, "consumer"), time.Hour)
		t.Cleanup(cancel)

		started := make(chan context.Context, 1)
		release := make(chan struct{})
		consumer := make(chan error, 1)

		go func() {
			consumer <- driver.Consume(ctx, func(ctx context.Context, _ broadcast.Message) error {
				started <- ctx
				<-release

				return nil
			})
		}()

		synctest.Wait()

		if err := driver.Publish(context.Background(), broadcast.Message{Name: "users.created"}); err != nil {
			t.Fatal(err)
		}

		inflight := <-started
		cancel()

		if err := <-consumer; !errors.Is(err, context.Canceled) {
			t.Errorf("Consume = %v, want context.Canceled", err)
		}

		drain, cancelDrain := context.WithTimeout(context.Background(), time.Second)
		t.Cleanup(cancelDrain)

		if err := driver.Stop(drain); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop = %v, want context.DeadlineExceeded", err)
		}

		if err := inflight.Err(); err != nil {
			t.Errorf("in-flight handler context = %v, want no error", err)
		}

		if inflight.Done() != nil {
			t.Error("in-flight handler context has a cancellation channel")
		}

		if _, deadline := inflight.Deadline(); deadline {
			t.Error("in-flight handler context has a deadline")
		}

		if value := inflight.Value(t); value != "consumer" {
			t.Errorf("in-flight context value = %v, want consumer", value)
		}

		stopped := make(chan error, 1)

		go func() {
			stopped <- driver.Stop(context.Background())
			close(stopped)
		}()

		synctest.Wait()

		select {
		case err := <-stopped:
			t.Errorf("second Stop returned %v before the in-flight handler finished", err)
		default:
		}

		close(release)

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	})
}
