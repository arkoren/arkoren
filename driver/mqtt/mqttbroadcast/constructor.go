package mqttbroadcast

import (
	"slices"

	"github.com/eclipse/paho.golang/autopaho"

	"arkoren.dev/driver/mqtt/mqttbroadcast/internal/broker"
)

// New captures required broker URLs before callbacks, validates completed
// settings, and constructs an owned clean-session driver without network I/O.
// Callbacks run directly once in order; nil entries panic. URL failures never
// echo credentials. Supported native transports preserve paths and queries only
// for WebSockets. QoS 0 is explicit; positive keepalive must fit whole seconds.
// Start later acquires the native manager; Stop owns cancellation and disconnect.
//
// Example:
//
//	driver, err := mqttbroadcast.New([]string{"mqtt://localhost:1883"})
//	if err != nil {
//		return err
//	}
//	_ = driver.Client()
func New(urls []string, configure ...BuilderFunc) (*Driver, error) {
	urls = slices.Clone(urls)
	config := configureDriver(configure...)

	transport, err := broker.New(urls, broker.Config(config))

	if err != nil {
		return nil, err
	}

	return publishDriver(transport), nil
}

// NewFrom borrows a native manager without changing session, authentication,
// keepalive, TLS, loggers, or reconnect policy. Only QoS and Logger apply.
// Nil stages the native callback bridge: install HandlePublish and
// HandleConnectionUp before manager creation, then attach with SetClient.
// Callbacks run once in order; nil entries panic. Invalid QoS is reported at use.
// Stop releases only this driver's filters, never disconnecting the manager.
// Do not share its filters with native users; their owner disconnects afterward.
//
// Example:
//
//	driver := mqttbroadcast.NewFrom(nil)
//	_ = driver.HandlePublish
//	_ = driver.HandleConnectionUp
func NewFrom(client *autopaho.ConnectionManager, configure ...BuilderFunc) *Driver {
	config := configureDriver(configure...)

	return publishDriver(broker.NewFrom(client, broker.Config(config)))
}

// publishDriver retains immutable construction metadata around the sole broker
// owner. The native configuration is inspected only here, before startup.
func publishDriver(transport *broker.Driver) *Driver {
	config, owned, qos, logger := transport.Construction()

	return &Driver{broker: transport, clientConfig: config,
		owned: owned, qos: qos, logger: logger}
}
