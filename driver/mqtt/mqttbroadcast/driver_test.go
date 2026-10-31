package mqttbroadcast_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"

	"arkoren.dev/core/service/broadcast"
	"arkoren.dev/core/service/broadcast/broadcasttest"
	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// protocolBroker is an isolated MQTT peer, not a service on the shared broker
// port. Tests control acknowledgements through Paho's public packet API.
type protocolBroker struct {
	// incoming carries requests after the automatic CONNECT/PING exchange.
	incoming chan *packets.ControlPacket
	// connected captures each CONNECT for wire-level configuration assertions.
	connected chan *packets.Connect
	// connections tracks accepted sockets so cleanup interrupts every reader.
	connections []net.Conn
	// mu protects connections during acceptance and cleanup.
	mu sync.Mutex
	// work tracks each active packet reader.
	work sync.WaitGroup
	// done interrupts delivery to incoming during cleanup.
	done chan struct{}
}

// newProtocolBroker owns the controlled MQTT peer and every attached stream.
// Native-client tests use net.Pipe; owned-client tests attach an isolated
// loopback listener. Cleanup interrupts readers even after test failure.
func newProtocolBroker(t *testing.T) *protocolBroker {
	t.Helper()

	peer := &protocolBroker{
		incoming:  make(chan *packets.ControlPacket, 16),
		connected: make(chan *packets.Connect, 16),
		done:      make(chan struct{}),
	}
	t.Cleanup(func() {
		close(peer.done)
		peer.mu.Lock()

		for _, conn := range peer.connections {
			_ = conn.Close()
		}

		peer.mu.Unlock()
		peer.work.Wait()
	})

	return peer
}

// connect replaces native dialing with net.Pipe, a public Paho transport seam.
// Each reconnect receives a new stream; the URL is never resolved or contacted.
func (peer *protocolBroker) connect(context.Context, autopaho.ClientConfig, *url.URL) (net.Conn, error) {
	client, server := net.Pipe()

	peer.mu.Lock()
	peer.connections = append(peer.connections, server)
	peer.mu.Unlock()
	peer.work.Go(func() { peer.read(server) })

	return packets.NewThreadSafeConn(client), nil
}

// read acknowledges connection establishment and keepalive, leaving business
// protocol packets to the test. The concrete packet is read-only after delivery.
func (peer *protocolBroker) read(conn net.Conn) {
	for {
		packet, err := packets.ReadPacket(conn)

		if err != nil {
			return
		}

		if peer.respond(conn, packet) {
			continue
		}

		select {
		case peer.incoming <- packet:
		case <-peer.done:
			return
		}
	}
}

// respond handles transport housekeeping only. Tests inspect every other packet
// and explicitly choose success, failure, or delayed acknowledgements.
func (peer *protocolBroker) respond(conn net.Conn, packet *packets.ControlPacket) bool {
	switch packet.Type {
	case packets.CONNECT:
		peer.connected <- packet.Content.(*packets.Connect)
		// Reconnection and cleanup close sockets; bounded native waits observe it.
		_, _ = (&packets.Connack{Properties: &packets.Properties{}}).WriteTo(conn)

		return true
	case packets.PINGREQ:
		// Closing sockets during cleanup can interrupt a keepalive reply.
		_, _ = (&packets.Pingresp{}).WriteTo(conn)

		return true
	default:
		return false
	}
}

// receive waits for a request with a finite failure deadline. It does not retry
// operations, so a missing or reordered MQTT packet fails the regression test.
func (peer *protocolBroker) receive(t *testing.T, kind byte) *packets.ControlPacket {
	t.Helper()

	select {
	case packet := <-peer.incoming:
		if packet.Type != kind {
			t.Fatalf("received packet %d, want %d", packet.Type, kind)
		}

		return packet
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MQTT request")

		return nil
	}
}

// send writes a response on the newest native connection. The peer never closes
// another test's socket; reconnection tests can deliberately replace this one.
func (peer *protocolBroker) send(t *testing.T, packet *packets.ControlPacket) {
	t.Helper()

	peer.mu.Lock()
	conn := peer.connections[len(peer.connections)-1]
	peer.mu.Unlock()

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := packet.WriteTo(conn); err != nil {
		t.Fatal(err)
	}
}

// newOwnedProtocolDriver uses one ephemeral loopback endpoint to observe New's
// actual CONNECT packet. Only this test owns the listener, stream, and manager;
// no Compose service or shared broker is contacted or started.
func newOwnedProtocolDriver(t *testing.T, peer *protocolBroker, configure ...mqttbroadcast.BuilderFunc) *mqttbroadcast.Driver {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })
	peer.work.Go(func() {
		conn, err := listener.Accept()

		if err != nil {
			return // Cleanup closes the listener if construction or startup fails.
		}

		peer.mu.Lock()
		peer.connections = append(peer.connections, conn)
		peer.mu.Unlock()
		peer.read(conn)
	})

	driver, err := mqttbroadcast.New([]string{"mqtt://" + listener.Addr().String()}, configure...)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := driver.Stop(ctx); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := driver.Start(ctx); err != nil {
		t.Fatal(err)
	}

	return driver
}

// newProtocolDriver wires only public native callbacks, matching caller-owned
// Paho setup. The native manager's lifecycle belongs to this test, not the driver.
func newProtocolDriver(t *testing.T, peer *protocolBroker, configure ...mqttbroadcast.BuilderFunc) *mqttbroadcast.Driver {
	t.Helper()

	driver := mqttbroadcast.NewFrom(nil, configure...)
	manager := newProtocolManager(t, peer, autopaho.ClientConfig{
		ClientConfig: paho.ClientConfig{
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){driver.HandlePublish},
		},
	})
	driver.SetClient(manager)

	return driver
}

// newProtocolManager preserves supplied native hooks and session implementations
// while replacing only the transport. The test owns disconnect and cancellation.
func newProtocolManager(t *testing.T, peer *protocolBroker, config autopaho.ClientConfig) *autopaho.ConnectionManager {
	t.Helper()

	config.ServerUrls = []*url.URL{{Scheme: "mqtt", Host: "isolated.invalid:1883"}}
	config.AttemptConnection = peer.connect

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	manager, err := autopaho.NewConnection(ctx, config)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := manager.Disconnect(ctx); err != nil {
			t.Error(fmt.Errorf("disconnect protocol client: %w", err))
		}
	})

	ready, release := context.WithTimeout(ctx, 5*time.Second)
	defer release()

	if err := manager.AwaitConnection(ready); err != nil {
		t.Fatal(err)
	}

	return manager
}

// subscribe completes one driver's registration against this controlled peer.
// It returns the public unsubscribe function only after a successful SUBACK.
func (peer *protocolBroker) subscribe(t *testing.T, driver *mqttbroadcast.Driver, event string, handler func([]byte)) func() error {
	t.Helper()

	var unsubscribe func() error
	done := make(chan error, 1)

	go func() {
		var err error
		unsubscribe, err = driver.Subscribe(context.Background(), event, handler)
		done <- err
	}()

	request := peer.receive(t, packets.SUBSCRIBE).Content.(*packets.Subscribe)
	peer.send(t, &packets.ControlPacket{
		Type:    packets.SUBACK,
		Content: &packets.Suback{PacketID: request.PacketID, Reasons: []byte{1}},
	})

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	return unsubscribe
}

// acknowledgeUnsubscribe accepts one completed release. The returned reason
// indicates success and is deliberately independent of the registration context.
func (peer *protocolBroker) acknowledgeUnsubscribe(t *testing.T, request *packets.Unsubscribe) {
	t.Helper()

	peer.send(t, &packets.ControlPacket{
		Type:    packets.UNSUBACK,
		Content: &packets.Unsuback{PacketID: request.PacketID, Reasons: []byte{0}},
	})
}

// Example checks package-level lifecycle wiring without contacting a broker.
// Cleanup is registered before startup and joins errors with the named result.
func Example() {
	run := func(ctx context.Context) (err error) {
		driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})

		if err != nil {
			return err
		}

		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err = errors.Join(err, driver.Stop(cleanup))
		}()

		if err := driver.Start(ctx); err != nil {
			return err
		}

		consumer := broadcast.NewConsumer(driver)

		// Register typed listeners on consumer before starting the runner.
		return consumer.Run(ctx)
	}
	_ = run
	// Output:
}

// ExampleBuilder_KeepAlive uses a duration at the public construction boundary.
// Construction validates policy but performs no native lifecycle I/O.
func ExampleBuilder_KeepAlive() {
	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"}, func(broker *mqttbroadcast.Builder) {
		broker.KeepAlive(45 * time.Second)
	})

	if err != nil {
		fmt.Println(err)

		return
	}

	_ = driver
	// Output:
}

// ExampleNewFrom type-checks the inline ownership example without executing its
// network flow. Protocol tests separately exercise these same public hooks.
func ExampleNewFrom() {
	run := func(ctx context.Context) error {
		driver := mqttbroadcast.NewFrom(nil)
		config := autopaho.ClientConfig{
			ServerUrls:     []*url.URL{{Scheme: "mqtt", Host: "localhost:1883"}},
			OnConnectionUp: driver.HandleConnectionUp,
			ClientConfig: paho.ClientConfig{
				OnPublishReceived: []func(paho.PublishReceived) (bool, error){driver.HandlePublish},
			},
		}
		manager, err := autopaho.NewConnection(ctx, config)

		if err != nil {
			return err
		}

		driver.SetClient(manager)

		runErr := broadcast.NewConsumer(driver).Run(ctx)

		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return errors.Join(runErr, driver.Stop(cleanup), manager.Disconnect(cleanup))
	}
	_ = run
	// Output:
}

// mqttTestURL is the address of the MQTT broker started by the compose file in the driver directory.
const mqttTestURL = "tcp://127.0.0.1:1883"

// TestConformance runs the broadcast conformance suite against an MQTT broker.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	broadcasttest.Run(t, func(t *testing.T) broadcasttest.Driver {
		driver, err := mqttbroadcast.New([]string{mqttTestURL})

		if err != nil {
			t.Fatal(err)
		}

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
