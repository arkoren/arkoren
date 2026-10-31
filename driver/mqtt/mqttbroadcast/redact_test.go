package mqttbroadcast_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/driver/mqtt/mqttbroadcast"
)

// TestConfigDoesNotPrintSecrets verifies fmt and slog never show secret fields.
func TestConfigDoesNotPrintSecrets(t *testing.T) {
	t.Parallel()

	config := mqttbroadcast.Config{
		Password: "secret-password-value",
	}
	var logs strings.Builder

	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	for _, secret := range []string{"secret-password-value"} {
		if strings.Contains(output, secret) {
			t.Fatalf("output contains %q: %s", secret, output)
		}
	}

	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("output has no placeholder: %s", output)
	}
}

// TestConfigPrintsEmptySecretsEmpty verifies unset secrets are not redacted.
func TestConfigPrintsEmptySecretsEmpty(t *testing.T) {
	t.Parallel()

	if strings.Contains(fmt.Sprintf("%+v", mqttbroadcast.Config{}), "[REDACTED]") {
		t.Fatal("empty secret was redacted")
	}
}
