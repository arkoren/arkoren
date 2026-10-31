package mqttbroadcast_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"

	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// TestNativeConnectionHookRestoresSubscriptions proves external callers can
// install the public hook and restore filters after real Paho reconnection.
func TestNativeConnectionHookRestoresSubscriptions(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		peer := newProtocolBroker(t)
		driver := mqttbroadcast.NewFrom(nil)
		manager := newProtocolManager(t, peer, autopaho.ClientConfig{
			OnConnectionUp:   driver.HandleConnectionUp,
			ReconnectBackoff: autopaho.NewConstantBackoff(0),
		})
		driver.SetClient(manager)
		synctest.Wait()

		peer.subscribe(t, driver, "sensor.changed", func([]byte) {
		})

		manager.TerminateConnectionForTest()
		request := peer.receive(t, packets.SUBSCRIBE).Content.(*packets.Subscribe)

		if len(request.Subscriptions) != 1 || request.Subscriptions[0].Topic != "sensor/changed" {
			t.Fatalf("restored subscriptions = %+v", request.Subscriptions)
		}

		peer.send(t, &packets.ControlPacket{
			Type:    packets.SUBACK,
			Content: &packets.Suback{PacketID: request.PacketID, Reasons: []byte{1}},
		})
		synctest.Wait()

		stopped := make(chan error, 1)

		go func() {
			stopped <- driver.Stop(context.Background())
		}()

		remove := peer.receive(t, packets.UNSUBSCRIBE).Content.(*packets.Unsubscribe)
		peer.acknowledgeUnsubscribe(t, remove)

		if err := <-stopped; err != nil {
			t.Fatal(err)
		}

		driver.HandleConnectionUp(manager, nil)
		synctest.Wait()

		select {
		case packet := <-peer.incoming:
			t.Fatalf("stopped driver restored packet %d", packet.Type)
		default:
		}
	})
}
