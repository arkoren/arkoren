package hatchet

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestSDKLogSignalsRegistration verifies that only the native worker
// acknowledgement closes the startup registration channel.
func TestSDKLogSignalsRegistration(t *testing.T) {
	t.Parallel()

	log := newSDKLog(slog.New(slog.DiscardHandler))
	logger := log.zerolog()

	logger.Debug().Msg("starting action listener")

	select {
	case <-log.registered:
		t.Fatal("registered before the registration record")
	default:
	}

	logger.Debug().Msgf("%s: %s", registeredMessage, "worker-1")
	logger.Debug().Msgf("%s: %s", registeredMessage, "worker-1")

	select {
	case <-log.registered:
	default:
		t.Fatal("registration record did not signal")
	}
}

// TestSDKLogForwardsToSlog verifies that the SDK bridge preserves structured
// fields and respects the destination handler's level filter.
func TestSDKLogForwardsToSlog(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer

	log := newSDKLog(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	logger := log.zerolog()

	logger.Debug().Msg("hidden debug")
	logger.Error().Str("service", "worker").Msg("could not execute action")

	output := buffer.String()

	if strings.Contains(output, "hidden debug") {
		t.Fatalf("debug record forwarded below the handler level: %q", output)
	}

	for _, want := range []string{"level=ERROR", `msg="could not execute action"`, "service=worker"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q lacks %q", output, want)
		}
	}
}
