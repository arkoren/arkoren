package sdk

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// errInvalidEndpoint avoids reflecting collector URLs into startup errors.
// User info and query strings can contain credentials even in malformed input.
var errInvalidEndpoint = errors.New("invalid collector endpoint: use host[:port] or an HTTP(S) URL without credentials, query, or fragment")

// endpointFuncs are the option constructors of one OTLP exporter package.
// WithURLPath is nil for gRPC exporters, which have no URL path.
type endpointFuncs[O any] struct {
	// WithEndpoint sets the host:port.
	WithEndpoint func(string) O

	// WithInsecure disables TLS.
	WithInsecure func() O

	// WithHeaders sets request headers or metadata.
	WithHeaders func(map[string]string) O

	// WithURLPath sets the HTTP request path.
	WithURLPath func(string) O
}

// exporterOptions translates the connection settings of config into the
// option type O of one OTLP exporter package. Empty settings produce no
// option, which leaves the exporter to read its OTEL_EXPORTER_OTLP_*
// variables.
//
// An endpoint with a scheme, such as https://collector:4318/prefix, is split
// into host, TLS mode (http disables TLS), and, for HTTP exporters, a path
// prefix to which signalPath (such as /v1/traces) is appended, as the
// OpenTelemetry specification defines for OTEL_EXPORTER_OTLP_ENDPOINT.
func exporterOptions[O any](config Config, signalPath string, funcs endpointFuncs[O]) ([]O, error) {
	var opts []O

	insecure := config.Insecure

	switch {
	case strings.Contains(config.Endpoint, "://"):
		endpoint, err := collectorURL(config.Endpoint)

		if err != nil {
			return nil, err
		}

		opts = append(opts, funcs.WithEndpoint(endpoint.Host))
		insecure = insecure || endpoint.Scheme == "http"

		if funcs.WithURLPath != nil {
			opts = append(opts, funcs.WithURLPath(strings.TrimSuffix(endpoint.Path, "/")+signalPath))
		}
	case config.Endpoint != "":
		endpoint, err := collectorURL("https://" + config.Endpoint)

		if err != nil || endpoint.Path != "" {
			return nil, errInvalidEndpoint
		}

		opts = append(opts, funcs.WithEndpoint(config.Endpoint))
	}

	if insecure {
		opts = append(opts, funcs.WithInsecure())
	}

	if len(config.Headers) > 0 {
		opts = append(opts, funcs.WithHeaders(config.Headers))
	}

	return opts, nil
}

// collectorURL validates the supported URL form without performing I/O or
// retaining its input. Unsupported URL parts are rejected instead of silently
// discarded. Errors never wrap url.Parse diagnostics, which can contain secrets.
func collectorURL(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)

	if err != nil {
		return nil, errInvalidEndpoint
	}

	if parsed.Hostname() == "" {
		return nil, errInvalidEndpoint
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errInvalidEndpoint
	}

	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errInvalidEndpoint
	}

	return parsed, nil
}

// newSpanExporter creates the OTLP span exporter for protocol.
func newSpanExporter(ctx context.Context, config Config, protocol string) (sdktrace.SpanExporter, error) {
	if protocol == protocolGRPC {
		opts, err := exporterOptions(config, "", endpointFuncs[otlptracegrpc.Option]{
			WithEndpoint: otlptracegrpc.WithEndpoint,
			WithInsecure: otlptracegrpc.WithInsecure,
			WithHeaders:  otlptracegrpc.WithHeaders,
		})

		if err != nil {
			return nil, err
		}

		exporter, err := otlptracegrpc.New(ctx, opts...)

		if err != nil {
			return nil, fmt.Errorf("create OTLP/gRPC span exporter: %w", err)
		}

		return exporter, nil
	}

	opts, err := exporterOptions(config, "/v1/traces", endpointFuncs[otlptracehttp.Option]{
		WithEndpoint: otlptracehttp.WithEndpoint,
		WithInsecure: otlptracehttp.WithInsecure,
		WithHeaders:  otlptracehttp.WithHeaders,
		WithURLPath:  otlptracehttp.WithURLPath,
	})

	if err != nil {
		return nil, err
	}

	exporter, err := otlptracehttp.New(ctx, opts...)

	if err != nil {
		return nil, fmt.Errorf("create OTLP/HTTP span exporter: %w", err)
	}

	return exporter, nil
}

// newMetricExporter creates the OTLP metric exporter for protocol.
func newMetricExporter(ctx context.Context, config Config, protocol string) (sdkmetric.Exporter, error) {
	if protocol == protocolGRPC {
		opts, err := exporterOptions(config, "", endpointFuncs[otlpmetricgrpc.Option]{
			WithEndpoint: otlpmetricgrpc.WithEndpoint,
			WithInsecure: otlpmetricgrpc.WithInsecure,
			WithHeaders:  otlpmetricgrpc.WithHeaders,
		})

		if err != nil {
			return nil, err
		}

		exporter, err := otlpmetricgrpc.New(ctx, opts...)

		if err != nil {
			return nil, fmt.Errorf("create OTLP/gRPC metric exporter: %w", err)
		}

		return exporter, nil
	}

	opts, err := exporterOptions(config, "/v1/metrics", endpointFuncs[otlpmetrichttp.Option]{
		WithEndpoint: otlpmetrichttp.WithEndpoint,
		WithInsecure: otlpmetrichttp.WithInsecure,
		WithHeaders:  otlpmetrichttp.WithHeaders,
		WithURLPath:  otlpmetrichttp.WithURLPath,
	})

	if err != nil {
		return nil, err
	}

	exporter, err := otlpmetrichttp.New(ctx, opts...)

	if err != nil {
		return nil, fmt.Errorf("create OTLP/HTTP metric exporter: %w", err)
	}

	return exporter, nil
}

// newLogExporter creates the OTLP log exporter for protocol.
func newLogExporter(ctx context.Context, config Config, protocol string) (sdklog.Exporter, error) {
	if protocol == protocolGRPC {
		opts, err := exporterOptions(config, "", endpointFuncs[otlploggrpc.Option]{
			WithEndpoint: otlploggrpc.WithEndpoint,
			WithInsecure: otlploggrpc.WithInsecure,
			WithHeaders:  otlploggrpc.WithHeaders,
		})

		if err != nil {
			return nil, err
		}

		exporter, err := otlploggrpc.New(ctx, opts...)

		if err != nil {
			return nil, fmt.Errorf("create OTLP/gRPC log exporter: %w", err)
		}

		return exporter, nil
	}

	opts, err := exporterOptions(config, "/v1/logs", endpointFuncs[otlploghttp.Option]{
		WithEndpoint: otlploghttp.WithEndpoint,
		WithInsecure: otlploghttp.WithInsecure,
		WithHeaders:  otlploghttp.WithHeaders,
		WithURLPath:  otlploghttp.WithURLPath,
	})

	if err != nil {
		return nil, err
	}

	exporter, err := otlploghttp.New(ctx, opts...)

	if err != nil {
		return nil, fmt.Errorf("create OTLP/HTTP log exporter: %w", err)
	}

	return exporter, nil
}
