package redisbroadcast_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"arkoren.dev/driver/redis/redisbroadcast"
)

// TestConfigDoesNotPrintPassword verifies fmt and slog never show the password.
func TestConfigDoesNotPrintPassword(t *testing.T) {
	t.Parallel()

	config := redisbroadcast.Config{
		Password: "hunter2",
	}
	var logs strings.Builder

	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "config", "config", config)

	output := fmt.Sprintf("%v %+v %#v %s", config, config, config, logs.String())

	if strings.Contains(output, "hunter2") || !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("unexpected output: %s", output)
	}
}

// TestConfigPrintsEmptyPassword verifies an unset password stays empty.
func TestConfigPrintsEmptyPassword(t *testing.T) {
	t.Parallel()

	if strings.Contains(fmt.Sprintf("%+v", redisbroadcast.Config{}), "[REDACTED]") {
		t.Fatal("empty password was redacted")
	}
}
