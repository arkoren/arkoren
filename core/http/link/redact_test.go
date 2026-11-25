package link_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/core/event"
	"arkoren.dev/core/http/link"
)

// TestConfigOmitsServices verifies fmt and slog show the origin and ignored
// names but never the live event bus.
func TestConfigOmitsServices(t *testing.T) {
	t.Parallel()

	config := link.Config{
		Origin:      "https://example.test",
		IgnoreQuery: []string{"utm_source"},
		Events:      event.New(),
	}
	var logs strings.Builder
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	if !strings.Contains(output, "https://example.test") || !strings.Contains(output, "utm_source") {
		t.Fatalf("settings missing: %s", output)
	}

	if strings.Contains(output, "Events") || strings.Contains(output, "0x") {
		t.Fatalf("service printed: %s", output)
	}
}
