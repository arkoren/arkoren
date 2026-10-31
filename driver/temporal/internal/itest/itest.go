// Package itest holds the shared setup of the Temporal integration tests that
// run against the temporal service of driver/temporal/compose.yaml.
package itest

import (
	"os"
	"testing"
)

// defaultHostPort is the port of the compose service.
// ARKOREN_TEMPORAL_HOST_PORT overrides it.
const defaultHostPort = "127.0.0.1:17233"

// HostPort returns the address of the Temporal frontend. It skips the test in
// short mode before reading configuration. ARKOREN_TEMPORAL_HOST_PORT selects an
// isolated service supplied by the test coordinator; omission uses the module's
// Compose endpoint. This helper neither starts services nor validates reachability.
//
// Example:
//
//	func TestEngine(t *testing.T) {
//		address := itest.HostPort(t)
//		_ = address // Pass to the native client's connection settings.
//	}
func HostPort(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("integration test")
	}

	if value := os.Getenv("ARKOREN_TEMPORAL_HOST_PORT"); value != "" {
		return value
	}

	return defaultHostPort
}
