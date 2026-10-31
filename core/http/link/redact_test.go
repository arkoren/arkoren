package link_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/http/link"
	"arkoren.dev/foundation/key"
)

// TestConfigDoesNotPrintPreviousSecrets verifies fmt and slog never show the
// rotation secrets.
func TestConfigDoesNotPrintPreviousSecrets(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", link.MinSecretLength)
	config := link.Config{
		Origin:          "https://example.test",
		PreviousSecrets: []key.Shared{fixtureSecret([]byte(secret))},
	}
	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	if strings.Contains(output, secret) || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("unexpected output: %s", output)
	}

	if !strings.Contains(output, "https://example.test") {
		t.Fatalf("origin missing: %s", output)
	}
}

// TestConfigPrintsNoSecretsEmpty verifies a config without previous secrets
// is not redacted.
func TestConfigPrintsNoSecretsEmpty(t *testing.T) {
	t.Parallel()

	if strings.Contains(fmt.Sprintf("%+v", link.Config{}), "[REDACTED]") {
		t.Fatal("empty secrets were redacted")
	}
}
