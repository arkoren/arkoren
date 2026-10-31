package restate_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/driver/restate"
)

// TestConfigDoesNotPrintSecrets verifies fmt and slog never show secret fields.
func TestConfigDoesNotPrintSecrets(t *testing.T) {
	t.Parallel()

	config := restate.Config{
		AuthKey:      "secret-authkey-value",
		AdminAuthKey: "secret-adminauthkey-value",
	}

	var logs strings.Builder

	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	for _, secret := range []string{"secret-authkey-value", "secret-adminauthkey-value"} {
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

	if strings.Contains(fmt.Sprintf("%+v", restate.Config{}), "[REDACTED]") {
		t.Fatal("empty secret was redacted")
	}
}
