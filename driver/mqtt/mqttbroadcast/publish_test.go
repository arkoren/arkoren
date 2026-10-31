package mqttbroadcast_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/eclipse/paho.golang/packets"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// TestPublishRejectsQoS2PubrecFailure covers Paho v0.23.0's nil-error failure
// response without a fake driver client or access to driver internals.
func TestPublishRejectsQoS2PubrecFailure(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)
	driver := newProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
		broker.QoS(2)
	})
	done := make(chan error, 1)

	go func() {
		done <- driver.Publish(context.Background(), broadcast.Message{Name: "sensor.changed"})
	}()

	request := peer.receive(t, packets.PUBLISH).Content.(*packets.Publish)
	peer.send(t, (&packets.Pubrec{
		PacketID:   request.PacketID,
		ReasonCode: 0x87,
	}).ToControlPacket())

	if err := <-done; err == nil || !strings.Contains(err.Error(), "0x87") {
		t.Fatalf("Publish error = %v, want failed PUBREC code", err)
	}
}

// TestPublishRejectsQoS2PubcompFailure exercises the other nil-error QoS 2
// response after the successful PUBREC/PUBREL exchange.
func TestPublishRejectsQoS2PubcompFailure(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)
	driver := newProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
		broker.QoS(2)
	})
	done := make(chan error, 1)

	go func() {
		done <- driver.Publish(context.Background(), broadcast.Message{Name: "sensor.changed"})
	}()

	request := peer.receive(t, packets.PUBLISH).Content.(*packets.Publish)
	peer.send(t, (&packets.Pubrec{PacketID: request.PacketID}).ToControlPacket())
	peer.receive(t, packets.PUBREL)
	peer.send(t, &packets.ControlPacket{
		Type:    packets.PUBCOMP,
		Content: &packets.Pubcomp{PacketID: request.PacketID, ReasonCode: 0x92},
	})

	if err := <-done; err == nil || !strings.Contains(err.Error(), "0x92") {
		t.Fatalf("Publish error = %v, want failed PUBCOMP code", err)
	}
}

// TestPublishAcceptsNoMatchingSubscribers keeps successful MQTT informational
// codes distinct from failures; acceptance does not require a live subscriber.
func TestPublishAcceptsNoMatchingSubscribers(t *testing.T) {
	t.Parallel()

	peer := newProtocolBroker(t)
	driver := newProtocolDriver(t, peer)
	done := make(chan error, 1)

	go func() {
		done <- driver.Publish(context.Background(), broadcast.Message{Name: "sensor.changed"})
	}()

	request := peer.receive(t, packets.PUBLISH).Content.(*packets.Publish)
	peer.send(t, &packets.ControlPacket{
		Type:    packets.PUBACK,
		Content: &packets.Puback{PacketID: request.PacketID, ReasonCode: 0x10},
	})

	if err := <-done; err != nil {
		t.Fatalf("informational PUBACK rejected: %v", err)
	}
}

// TestPublishCanceledQoS0DoesNotWrite uses a connected native client to exercise
// the Paho path that would write QoS 0 bytes despite an already canceled context.
func TestPublishCanceledQoS0DoesNotWrite(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := newProtocolDriver(t, peer, func(broker *mqttbroadcast.Builder) {
			broker.QoS(0)
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := driver.Publish(ctx, broadcast.Message{Name: "sensor.changed"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Publish error = %v, want context.Canceled", err)
		}

		synctest.Wait()

		select {
		case packet := <-peer.incoming:
			t.Fatalf("canceled Publish wrote packet %d", packet.Type)
		default:
		}
	})
}

// TestPublishCanceledContextWins validates cancellation before names, encoding,
// or connection lookup, including the QoS 0 path Paho writes without checking ctx.
func TestPublishCanceledContextWins(t *testing.T) {
	t.Parallel()

	driver := mqttbroadcast.NewFrom(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := driver.Publish(ctx, broadcast.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish error = %v, want context.Canceled", err)
	}
}

// TestConsumeCanceledContextWins verifies an ended consume never registers a
// subscription, even when no native connection or handler has been supplied.
func TestConsumeCanceledContextWins(t *testing.T) {
	t.Parallel()

	driver := mqttbroadcast.NewFrom(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := driver.Consume(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Consume error = %v, want context.Canceled", err)
	}
}

// TestSubscribeCanceledContextWins verifies cancellation precedes subscription
// validation and local registration as well as the first broker request.
func TestSubscribeCanceledContextWins(t *testing.T) {
	t.Parallel()

	driver := mqttbroadcast.NewFrom(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := driver.Subscribe(ctx, "", func([]byte) {
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe error = %v, want context.Canceled", err)
	}
}
