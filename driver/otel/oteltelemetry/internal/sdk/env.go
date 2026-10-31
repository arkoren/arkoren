package sdk

import (
	"cmp"
	"fmt"
	"os"
	"strings"
)

// Environment variables the driver reads itself. The OTLP exporters and the
// SDK read the remaining OTEL_EXPORTER_OTLP_*, OTEL_SERVICE_NAME,
// OTEL_RESOURCE_ATTRIBUTES, and OTEL_TRACES_SAMPLER* variables.
const (
	// protocolGRPC selects the native OTLP/gRPC transport.
	protocolGRPC = "grpc"
	// protocolHTTP selects the native OTLP/HTTP protobuf transport.
	protocolHTTP = "http/protobuf"

	// envSDKDisabled turns every signal into a no-op when "true".
	envSDKDisabled = "OTEL_SDK_DISABLED"

	// envProtocol selects the OTLP transport of every signal.
	envProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"

	// envTracesProtocol, envMetricsProtocol, and envLogsProtocol select the
	// transport of one signal.
	envTracesProtocol  = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	envMetricsProtocol = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
	envLogsProtocol    = "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"

	// envTracesExporter, envMetricsExporter, and envLogsExporter pick the
	// exporter of one signal: "otlp" or "none".
	envTracesExporter  = "OTEL_TRACES_EXPORTER"
	envMetricsExporter = "OTEL_METRICS_EXPORTER"
	envLogsExporter    = "OTEL_LOGS_EXPORTER"
)

const (
	// exporterOTLP selects the supported native OTLP exporter for a signal.
	exporterOTLP = "otlp"
	// exporterNone explicitly disables that signal's driver-owned exporter.
	exporterNone = "none"
)

// sdkDisabled reports whether OTEL_SDK_DISABLED asks for no telemetry.
func sdkDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(envSDKDisabled)), "true")
}

// exporterEnabled reports whether the exporter variable envName selects the
// OTLP exporter. Only "otlp" (the default) and "none" are supported.
func exporterEnabled(envName string) (bool, error) {
	switch value := strings.ToLower(strings.TrimSpace(os.Getenv(envName))); value {
	case "", exporterOTLP:
		return true, nil
	case exporterNone:
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q is not supported; use %q or %q", envName, value, exporterOTLP, exporterNone)
	}
}

// ResolveProtocol returns the transport for a signal: the configured one,
// then the signal's own variable, then OTEL_EXPORTER_OTLP_PROTOCOL, then
// http/protobuf.
func ResolveProtocol(config Config, signalEnv string) (string, error) {
	value := cmp.Or(
		config.Protocol,
		strings.TrimSpace(os.Getenv(signalEnv)),
		strings.TrimSpace(os.Getenv(envProtocol)),
		protocolHTTP,
	)

	switch value {
	case protocolGRPC, protocolHTTP:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported OTLP protocol %q; use %q or %q", value, protocolGRPC, protocolHTTP)
	}
}
