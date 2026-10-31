package vaultsecret

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// TestConfigRedactsToken checks every diagnostic formatting path for token
// leaks and verifies an empty token does not produce a misleading redaction.
func TestConfigRedactsToken(t *testing.T) {
	t.Parallel()

	config := Config{
		Address: "https://vault.example",
		Token:   "s.supersecret",
	}

	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).InfoContext(t.Context(), "config", "config", config)

	for _, out := range []string{fmt.Sprintf("%v %+v %#v", config, config, config), logged.String()} {
		if strings.Contains(out, "supersecret") {
			t.Fatalf("token leaked: %s", out)
		}

		if !strings.Contains(out, "vault.example") {
			t.Fatalf("non-secret field missing: %s", out)
		}
	}

	if out := fmt.Sprintf("%+v", Config{}); strings.Contains(out, redacted) {
		t.Fatalf("empty token redacted: %s", out)
	}
}
