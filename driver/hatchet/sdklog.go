package hatchet

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"sync"

	"github.com/rs/zerolog"
)

// registeredMessage starts the debug record the SDK writes once the engine
// has registered the worker. The SDK starts the worker in a goroutine that
// panics when registration fails, also when its stop function cancels it
// mid-registration, so [Host.Start] waits for this record.
const registeredMessage = "Registered worker with id"

// sdkLog forwards the zerolog records of the SDK to a slog logger and
// reports when the worker is registered.
type sdkLog struct {
	// logger receives operational records without changing the engine's execution
	// state.
	logger *slog.Logger
	// registered closes after the SDK emits its worker-registration
	// acknowledgement.
	registered chan struct{}
	// once ensures repeated registration messages close registered only once.
	once sync.Once
}

// newSDKLog bridges native SDK records to the supplied logger and creates an
// acknowledgement channel for startup registration.
func newSDKLog(logger *slog.Logger) *sdkLog {
	return &sdkLog{
		logger:     logger,
		registered: make(chan struct{}),
	}
}

// zerolog returns the logger to pass to the SDK. It enables every level so
// the hook sees the registration record; the slog handler filters.
func (log *sdkLog) zerolog() *zerolog.Logger {
	logger := zerolog.New(log).Level(zerolog.DebugLevel).Hook(log)

	return &logger
}

// Run implements [zerolog.Hook].
func (log *sdkLog) Run(_ *zerolog.Event, _ zerolog.Level, message string) {
	if strings.HasPrefix(message, registeredMessage) {
		log.once.Do(func() {
			close(log.registered)
		})
	}
}

// Write implements [io.Writer] for one JSON record of the SDK.
func (log *sdkLog) Write(data []byte) (int, error) {
	var record map[string]any

	if err := json.Unmarshal(data, &record); err != nil {
		log.logger.InfoContext(context.Background(), "hatchet sdk", "record", strings.TrimSpace(string(data)))

		return len(data), nil
	}

	level := slogLevel(record[zerolog.LevelFieldName])

	if !log.logger.Enabled(context.Background(), level) {
		return len(data), nil
	}

	message, _ := record[zerolog.MessageFieldName].(string)

	delete(record, zerolog.LevelFieldName)
	delete(record, zerolog.MessageFieldName)

	attrs := make([]slog.Attr, 0, len(record))

	for key, value := range record {
		attrs = append(attrs, slog.Any(key, value))
	}

	log.logger.LogAttrs(context.Background(), level, message, attrs...)

	return len(data), nil
}

// slogLevel maps a zerolog level name to a slog level.
func slogLevel(name any) slog.Level {
	switch name {
	case zerolog.LevelTraceValue, zerolog.LevelDebugValue:
		return slog.LevelDebug
	case zerolog.LevelWarnValue:
		return slog.LevelWarn
	case zerolog.LevelErrorValue, zerolog.LevelFatalValue, zerolog.LevelPanicValue:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
