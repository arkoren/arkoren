package mqttbroadcast

import (
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// SetClient attaches a staged borrowed manager after its native hooks have been
// configured. It transfers no ownership, installs no hooks, and does not reopen
// stopped drivers. Replacing a live manager or an owned manager is unsupported.
//
// Example:
//
//	driver.SetClient(manager)
func (driver *Driver) SetClient(client *autopaho.ConnectionManager) {
	driver.broker.SetClient(client)
}

// HandleConnectionUp schedules serialized restoration after each connection.
// It returns without waiting for broker acknowledgements, as autopaho requires.
// New installs it; NewFrom callers wire it before manager creation. Stop closes
// work admission and drains admitted restoration. Failures are logged once.
//
// Example:
//
//	config.OnConnectionUp = driver.HandleConnectionUp
func (driver *Driver) HandleConnectionUp(manager *autopaho.ConnectionManager, acknowledgement *paho.Connack) {
	driver.broker.HandleConnectionUp(manager, acknowledgement)
}
