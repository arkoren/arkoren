package mqttbroadcast_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

// TestUnsubscribeSerializesReplacement holds an old UNSUBACK while a replacement
// tries to register. The replacement must not pass the outstanding transition.
func TestUnsubscribeSerializesReplacement(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		unsubscribe := peer.subscribe(t, driver, "sensor.changed", func([]byte) {
		})
		removed := make(chan error, 1)

		go func() {
			removed <- unsubscribe()
		}()

		request := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		replacement := make(chan error, 1)

		go func() {
			_, err := driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
			})
			replacement <- err
		}()

		synctest.Wait()

		select {
		case packet := <-peer.incoming:
			t.Fatalf("replacement passed pending unsubscribe: packet %d", packet.Type)
		default:
		}

		peer.acknowledgeUnsubscribe(t, request)

		if err := <-removed; err != nil {
			t.Fatal(err)
		}

		next := peer.receive(t, packets.SUBSCRIBE).Content.(*packets.Subscribe)
		peer.send(t, &packets.ControlPacket{
			Type:    packets.SUBACK,
			Content: &packets.Suback{PacketID: next.PacketID, Reasons: []byte{1}},
		})

		if err := <-replacement; err != nil {
			t.Fatal(err)
		}
	})
}

// TestRepeatedUnsubscribeLeavesReplacement ensures an old release token cannot
// send another UNSUBSCRIBE after a new registration has taken over the filter.
func TestRepeatedUnsubscribeLeavesReplacement(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		unsubscribe := peer.subscribe(t, driver, "sensor.changed", func([]byte) {
		})
		done := make(chan error, 1)

		go func() {
			done <- unsubscribe()
		}()

		request := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		peer.acknowledgeUnsubscribe(t, request)

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		calls := 0
		peer.subscribe(t, driver, "sensor.changed", func([]byte) {
			calls++
		})

		if err := unsubscribe(); err != nil {
			t.Fatal(err)
		}

		_, _ = driver.HandlePublish(paho.PublishReceived{Packet: &paho.Publish{Topic: "sensor/changed"}})

		if calls != 1 {
			t.Fatalf("replacement calls = %d, want 1", calls)
		}
	})
}

// TestCanceledSubscribeDoesNotJoinExistingFilter covers the local fast path,
// which must not install a handler simply because MQTT setup already succeeded.
func TestCanceledSubscribeDoesNotJoinExistingFilter(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		peer.subscribe(t, driver, "sensor.changed", func([]byte) {
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := driver.Subscribe(ctx, "sensor.changed", func([]byte) {
			t.Error("canceled registration received a publication")
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe error = %v, want context.Canceled", err)
		}

		_, _ = driver.HandlePublish(paho.PublishReceived{Packet: &paho.Publish{Topic: "sensor/changed"}})
	})
}

// TestSubscribeCanceledWhileQueuedDoesNotWaitForBroker ensures a queued caller
// can leave while another registration still waits for its broker acknowledgement.
func TestSubscribeCanceledWhileQueuedDoesNotWaitForBroker(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		first := make(chan error, 1)

		go func() {
			_, err := driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
			})
			first <- err
		}()

		request := peer.receive(t, packets.SUBSCRIBE).Content.(*packets.Subscribe)
		ctx, cancel := context.WithCancel(context.Background())
		queued := make(chan error, 1)

		go func() {
			_, err := driver.Subscribe(ctx, "other.changed", func([]byte) {
			})
			queued <- err
		}()

		synctest.Wait()
		cancel()

		if err := <-queued; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Subscribe error = %v, want context.Canceled", err)
		}

		peer.send(t, &packets.ControlPacket{
			Type:    packets.SUBACK,
			Content: &packets.Suback{PacketID: request.PacketID, Reasons: []byte{1}},
		})

		if err := <-first; err != nil {
			t.Fatal(err)
		}
	})
}

// TestStopExpiredContextReleasesBorrowedSubscriptions keeps a running callback
// blocked past Stop. Local admission closes and broker cleanup still runs; the
// native manager stays connected, and Go does not forcibly stop the callback.
func TestStopExpiredContextReleasesBorrowedSubscriptions(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		entered, release := make(chan struct{}), make(chan struct{})
		unsubscribe := peer.subscribe(t, driver, "sensor.changed", func([]byte) {
			close(entered)
			<-release
		})

		go func() {
			_, _ = driver.HandlePublish(paho.PublishReceived{Packet: &paho.Publish{Topic: "sensor/changed"}})
		}()

		<-entered

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)

		go func() {
			done <- driver.Stop(ctx)
		}()

		request := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		peer.acknowledgeUnsubscribe(t, request)

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop error = %v, want context.Canceled", err)
		}

		if err := driver.Client().AwaitConnection(context.Background()); err != nil {
			t.Fatalf("Stop closed borrowed manager: %v", err)
		}

		if err := unsubscribe(); err != nil {
			t.Fatalf("release after Stop: %v", err)
		}

		_, _ = driver.HandlePublish(paho.PublishReceived{Packet: &paho.Publish{Topic: "sensor/changed"}})
		close(release)
	})
}

// TestStopWaitsForPendingSubscribeCleanup acknowledges setup only after shutdown
// begins. Stop must remove that subscription before completing its cleanup.
func TestStopWaitsForPendingSubscribeCleanup(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		registered := make(chan error, 1)

		go func() {
			_, err := driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
			})
			registered <- err
		}()

		request := peer.receive(t, packets.SUBSCRIBE).Content.(*packets.Subscribe)
		stopped := make(chan error, 1)

		go func() {
			stopped <- driver.Stop(context.Background())
		}()

		synctest.Wait()
		peer.send(t, &packets.ControlPacket{
			Type:    packets.SUBACK,
			Content: &packets.Suback{PacketID: request.PacketID, Reasons: []byte{1}},
		})

		if err := <-registered; err != nil {
			t.Fatal(err)
		}

		remove := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		peer.acknowledgeUnsubscribe(t, remove)

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}

		if _, err := driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
		}); err == nil {
			t.Fatal("Subscribe succeeded after Stop")
		}
	})
}

// TestStopJoinsDrainAndUnsubscribeFailures proves cleanup errors are not hidden
// by an already expired drain context, and the borrowed manager stays open.
func TestStopJoinsDrainAndUnsubscribeFailures(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer)
		peer.subscribe(t, driver, "sensor.changed", func([]byte) {
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)

		go func() {
			done <- driver.Stop(ctx)
		}()

		request := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		peer.send(t, &packets.ControlPacket{
			Type: packets.UNSUBACK,
			Content: &packets.Unsuback{
				PacketID:   request.PacketID,
				Reasons:    []byte{0x87},
				Properties: &packets.Properties{ReasonString: "not authorized"},
			},
		})
		err := <-done

		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("Stop error = %v, want drain and broker failure", err)
		}

		if err := driver.Client().AwaitConnection(context.Background()); err != nil {
			t.Fatalf("borrowed manager closed: %v", err)
		}
	})
}

// TestOwnedStopExpiredContextCancelsManager observes New's real native manager
// terminate after an expired drain context. The driver remains terminal while
// Client still exposes the shared native pointer for ownership observation.
func TestOwnedStopExpiredContextCancelsManager(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)
	driver := newOwnedProtocolDriver(t, peer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := driver.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context.Canceled", err)
	}

	select {
	case <-driver.Client().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("owned manager was not canceled after expired Stop")
	}

	if _, err := driver.Subscribe(context.Background(), "sensor.changed", func([]byte) {
	}); err == nil {
		t.Fatal("owned driver reopened after Stop")
	}

	peer.receive(t, packets.DISCONNECT)
}
