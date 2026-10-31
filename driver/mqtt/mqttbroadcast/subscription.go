package mqttbroadcast

import "context"

// Subscribe registers a raw, read-only callback under a portable event pattern.
// Dots become slashes, asterisks become plus signs, and hashes remain hashes.
// Overlapping handlers fan out independently. Unsubscribe removes only this
// registration and releases the broker filter only after its final handler.
// Repeated cleanup returns its original result without affecting replacements.
// Cancellation before registration installs nothing; later cancellation does
// not remove the callback. Cleanup uses a detached ten-second acknowledgment
// timeout and serializes with reconnect, replacement, and Stop. Copy payload
// bytes before retaining them; native writes can outlive the timeout.
//
// Example:
//
//	unsubscribe, err := driver.Subscribe(ctx, "users.#", handleRaw)
//	if err != nil {
//		return err
//	}
//	return unsubscribe()
func (driver *Driver) Subscribe(ctx context.Context, event string, handler rawHandler) (func() error, error) {
	return driver.broker.Subscribe(ctx, event, handler)
}
