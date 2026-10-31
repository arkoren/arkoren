package redisbroadcast_test

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/driver/redis/internal/redistest"
	"arkoren.dev/driver/redis/redisbroadcast"

	"github.com/redis/go-redis/v9"
)

// redisTestEndpoint selects infrastructure only for integration clients, without
// changing production defaults or dialing Redis. A nonempty
// ARKOREN_REDIS_TEST_ADDR is used verbatim; unset or empty preserves localhost:6379.
func redisTestEndpoint() string {
	return cmp.Or(os.Getenv("ARKOREN_REDIS_TEST_ADDR"), "localhost:6379")
}

// TestConformance runs the broadcast conformance suite against a Redis server.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		id := make([]byte, 6)

		if _, err := rand.Read(id); err != nil {
			t.Fatal(err)
		}

		driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
			broker.Addr(redisTestEndpoint())
			broker.Prefix("conformance" + hex.EncodeToString(id) + ".")
		})

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if err := driver.Start(ctx); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() {
			_ = driver.Stop(context.Background())
		})

		return broadcasttest.Driver{
			Publisher: driver,
			Consumer:  driver,
		}
	})
}

// TestPublishCancellationPrecedesInvalidName keeps a pre-canceled publish from
// being reported as a name-validation failure. No envelope is encoded or sent.
func TestPublishCancellationPrecedesInvalidName(t *testing.T) {
	t.Parallel()

	driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
		broker.Addr("127.0.0.1:1")
	})
	t.Cleanup(func() {
		if err := driver.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := driver.Publish(ctx, broadcast.Message{Name: "invalid name"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish error = %v, want context.Canceled", err)
	}
}

// TestOwnedClientHonorsPingDeadline checks readiness on the real native command
// path, without a broker or real-time sleeps. The RESP peer delays its reply.
func TestOwnedClientHonorsPingDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		driver := redisbroadcast.New(func(broker *redisbroadcast.Builder) {
			broker.Addr("127.0.0.1:1")
		})
		redistest.DelayPing(t, driver.Client())
		t.Cleanup(func() {
			if err := driver.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		if err := driver.Ping(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Ping error = %v, want context.DeadlineExceeded", err)
		}
	})
}

// TestNewFromStopPreservesBorrowedClient observes client ownership through a
// successful native operation after the driver's Stop. The RESP peer and waits
// stay in-process and under virtual time; no shared Redis service is contacted.
func TestNewFromStopPreservesBorrowedClient(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", ContextTimeoutEnabled: true})
		redistest.DelayPing(t, client)
		t.Cleanup(func() {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		})

		driver := redisbroadcast.NewFrom(client)

		if err := driver.Start(t.Context()); err != nil {
			t.Fatal(err)
		}

		if err := driver.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}

		if err := client.Ping(t.Context()).Err(); err != nil {
			t.Fatalf("borrowed client after Stop: %v", err)
		}
	})
}

// redisIsolationTimeout bounds network operations and cleanup without a quiet
// period or sleeps. Successful assertions use ordered publications as barriers.
const redisIsolationTimeout = 10 * time.Second

// TestConsumerSeparatesNestedPrefixes exercises ordinary typed publications in
// both scopes. A broad transport subscription must not reroute the nested
// application's envelope into the parent application's exact topic listener.
func TestConsumerSeparatesNestedPrefixes(t *testing.T) {
	t.Parallel()

	prefix := "sec004." + strings.ToLower(rand.Text()) + ".tenant."
	parent := newIsolationDriver(t, prefix)
	nested := newIsolationDriver(t, prefix+"eu.")
	parentConsumer := broadcast.NewConsumer(parent)
	nestedConsumer := broadcast.NewConsumer(nested)
	topic := broadcast.NewTopic[int]("users.created")
	parentReceived := make(chan int, 2)
	nestedReceived := make(chan int, 2)

	if _, err := parentConsumer.Listen(topic, func(_ context.Context, value int) error {
		parentReceived <- value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := nestedConsumer.Listen(topic, func(_ context.Context, value int) error {
		nestedReceived <- value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runIsolationConsumer(t, parent, parentConsumer)
	runIsolationConsumer(t, nested, nestedConsumer)

	if err := broadcast.NewPublisher(nested).Publish(t.Context(), topic, 2); err != nil {
		t.Fatal(err)
	}

	if err := broadcast.NewPublisher(parent).Publish(t.Context(), topic, 1); err != nil {
		t.Fatal(err)
	}

	if got := receiveIsolationMessage(t, parentReceived); got != 1 {
		t.Fatalf("parent listener received nested application's payload: %d", got)
	}

	if got := receiveIsolationMessage(t, nestedReceived); got != 2 {
		t.Fatalf("nested listener received payload %d, want 2", got)
	}
}

// TestConsumerTreatsBracketPrefixLiterally checks both sides of the glob bug:
// a lookalike channel is rejected and the literal bracket channel still works.
func TestConsumerTreatsBracketPrefixLiterally(t *testing.T) {
	t.Parallel()

	prefix := "sec004." + strings.ToLower(rand.Text()) + ".tenant"
	driver := newIsolationDriver(t, prefix+"[1].")
	lookalike := newIsolationDriver(t, prefix+"1.")
	consumer := broadcast.NewConsumer(driver)
	topic := broadcast.NewTopic[int]("users.created")
	received := make(chan int, 2)

	if _, err := consumer.Listen(topic, func(_ context.Context, value int) error {
		received <- value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runIsolationConsumer(t, driver, consumer)

	if err := broadcast.NewPublisher(lookalike).Publish(t.Context(), topic, 2); err != nil {
		t.Fatal(err)
	}

	if err := broadcast.NewPublisher(driver).Publish(t.Context(), topic, 1); err != nil {
		t.Fatal(err)
	}

	if got := receiveIsolationMessage(t, received); got != 1 {
		t.Fatalf("literal prefix listener received lookalike payload: %d", got)
	}
}

// TestConsumerRejectsEnvelopeChannelMismatch verifies rejection before either
// exact or wildcard core listeners run. A following valid envelope is an ordered
// barrier and must retain its identifier, metadata, and typed payload.
func TestConsumerRejectsEnvelopeChannelMismatch(t *testing.T) {
	t.Parallel()

	prefix := "sec004." + strings.ToLower(rand.Text()) + "."
	driver := newIsolationDriver(t, prefix)
	consumer := broadcast.NewConsumer(driver)
	topic := broadcast.NewTopic[int]("users.created")
	typed := make(chan int, 2)
	raw := make(chan broadcast.Message, 2)

	if _, err := consumer.Listen(topic, func(_ context.Context, value int) error {
		typed <- value

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := consumer.ListenPattern("#", func(_ context.Context, message broadcast.Message) error {
		raw <- message

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runIsolationConsumer(t, driver, consumer)

	// The envelope names users.created but arrives on orders.created. The wire
	// payload is base64 because broadcast.Message.Payload is a byte slice.
	if err := driver.Client().Publish(t.Context(), prefix+"orders.created",
		`{"name":"users.created","payload":"Mg=="}`).Err(); err != nil {
		t.Fatal(err)
	}

	if err := driver.Client().Publish(t.Context(), prefix+"users.created", `{`).Err(); err != nil {
		t.Fatal(err)
	}

	want := broadcast.Message{
		ID:       "local-message",
		Name:     topic.Name(),
		Payload:  []byte("1"),
		Metadata: map[string]string{"tenant": "local", "traceparent": "test-context"},
	}

	if err := driver.Publish(t.Context(), want); err != nil {
		t.Fatal(err)
	}

	if got := receiveIsolationMessage(t, typed); got != 1 {
		t.Fatalf("exact listener received mismatched envelope payload: %d", got)
	}

	if got := receiveIsolationMessage(t, raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("wildcard listener received %#v, want %#v", got, want)
	}
}

// TestSubscribeLiteralGlobPrefixAndWildcards exercises raw subscriptions with
// every Redis glob metacharacter in the prefix. Portable * and final # matching
// must retain their one-token and zero-or-more-token behavior respectively.
func TestSubscribeLiteralGlobPrefixAndWildcards(t *testing.T) {
	t.Parallel()

	prefix := "sec004." + strings.ToLower(rand.Text()) + `.[*?]\.`
	driver := newIsolationDriver(t, prefix)
	single := make(chan string, 5)
	tail := make(chan string, 5)

	unsubscribeSingle, err := driver.Subscribe(t.Context(), "users.*", func(payload []byte) {
		single <- string(payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unsubscribeSingle(); err != nil {
			t.Error(err)
		}
	})

	unsubscribeTail, err := driver.Subscribe(t.Context(), "users.#", func(payload []byte) {
		tail <- string(payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unsubscribeTail(); err != nil {
			t.Error(err)
		}
	})

	for _, name := range []string{"users", "users.created", "users.profile.updated", "orders.created", "users.finished"} {
		if err := driver.Client().Publish(t.Context(), prefix+name, name).Err(); err != nil {
			t.Fatal(err)
		}
	}

	for _, want := range []string{"users.created", "users.finished"} {
		if got := receiveIsolationMessage(t, single); got != want {
			t.Fatalf("single-token subscription received %q, want %q", got, want)
		}
	}

	for _, want := range []string{"users", "users.created", "users.profile.updated", "users.finished"} {
		if got := receiveIsolationMessage(t, tail); got != want {
			t.Fatalf("tail subscription received %q, want %q", got, want)
		}
	}
}

// TestSubscribeRecoversPanicAndUnsubscribes verifies the unchanged raw callback
// shape with a real Pub/Sub connection. A panic cannot lose the next delivery,
// and closing the subscription lets Stop drain before any later publication.
func TestSubscribeRecoversPanicAndUnsubscribes(t *testing.T) {
	t.Parallel()

	prefix := "sec004." + strings.ToLower(rand.Text()) + "."
	driver := newIsolationDriver(t, prefix)
	received := make(chan string, 2)

	unsubscribe, err := driver.Subscribe(t.Context(), "#", func(payload []byte) {
		if string(payload) == "panic" {
			panic("listener failed")
		}

		received <- string(payload)
	})

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := unsubscribe(); err != nil && !errors.Is(err, redis.ErrClosed) {
			t.Error(err)
		}
	})

	for _, payload := range []string{"panic", "after"} {
		if err := driver.Client().Publish(t.Context(), prefix+"users.created", payload).Err(); err != nil {
			t.Fatal(err)
		}
	}

	if got := receiveIsolationMessage(t, received); got != "after" {
		t.Fatalf("received %q after panic", got)
	}

	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), redisIsolationTimeout)
	t.Cleanup(cancel)

	if err := driver.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := driver.Client().Publish(ctx, prefix+"users.created", "unsubscribed").Err(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		t.Fatalf("received %q after unsubscribe drained", got)
	default:
	}
}

// newIsolationDriver creates a named, caller-owned Redis client so tests can
// observe their own subscription becoming active without publishing retries.
// redisTestEndpoint selects the integration address, including the test-only
// environment override. Cleanup releases only this client's subscriptions and
// never deletes data.
func newIsolationDriver(t *testing.T, prefix string) *redisbroadcast.Driver {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	client := redis.NewClient(&redis.Options{
		Addr:       redisTestEndpoint(),
		ClientName: "sec004-" + rand.Text(),
	})
	driver := redisbroadcast.NewFrom(client,
		func(broker *redisbroadcast.Builder) {
			broker.Prefix(prefix)
			broker.Logger(slog.New(slog.DiscardHandler))
		},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), redisIsolationTimeout)
		defer cancel()

		if err := driver.Stop(ctx); err != nil {
			t.Error(err)
		}

		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	return driver
}

// runIsolationConsumer starts the real core consumer and waits for Redis to
// report its named pattern subscription. CLIENT LIST is a server-side readiness
// barrier, so the first test publication cannot race subscription startup.
func runIsolationConsumer(t *testing.T, driver *redisbroadcast.Driver, consumer *broadcast.Consumer) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), redisIsolationTimeout)
	done := make(chan struct{})
	var err error

	go func() {
		err = consumer.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done

		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("consumer cancellation: %v", err)
		}
	})

	for {
		select {
		case <-done:
			t.Fatalf("consumer stopped before subscription became active: %v", err)
		default:
		}

		clients, err := driver.Client().ClientList(ctx).Result()

		if err != nil {
			t.Fatal(err)
		}

		for line := range strings.SplitSeq(clients, "\n") {
			if strings.Contains(line, " name="+driver.Client().Options().ClientName+" ") &&
				strings.Contains(line, " psub=1 ") {
				return
			}
		}
	}
}

// receiveIsolationMessage waits for the next ordered delivery, failing on a
// bounded timeout rather than guessing a quiet interval for absent messages.
func receiveIsolationMessage[T any](t *testing.T, received <-chan T) T {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), redisIsolationTimeout)
	defer cancel()

	select {
	case message := <-received:
		return message
	case <-ctx.Done():
		t.Fatal("timed out waiting for Redis delivery")
		var zero T

		return zero
	}
}
