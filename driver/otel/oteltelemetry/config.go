package oteltelemetry

import (
	"maps"

	"arkoren.dev/driver/otel/oteltelemetry/internal/sdk"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// BuilderFunc edits one fresh construction draft during New, synchronously in
// order. Nil entries panic; expanded nil or empty slices supply no edits.
// Native hooks are retained rather than invoked by these construction policies.
//
//	var policy oteltelemetry.BuilderFunc = func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.RuntimeMetrics(false)
//	}
//	driver := oteltelemetry.New(policy)
//	_ = driver
type BuilderFunc = func(*Builder)

// Protocol is the OTLP transport used by driver-owned exporters. Empty resolves
// the signal-specific environment variable, then the generic variable, then
// ProtocolHTTP. Unsupported values fail at Start for a driver-owned signal.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Protocol(oteltelemetry.ProtocolHTTP)
//	})
//	_ = driver
type Protocol string

const (
	// ProtocolGRPC exports over OTLP/gRPC, which defaults to port 4317.
	//
	//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	//		instrumentation.Protocol(oteltelemetry.ProtocolGRPC)
	//	})
	//	_ = driver
	ProtocolGRPC Protocol = "grpc"

	// ProtocolHTTP exports over OTLP/HTTP with protobuf bodies, which defaults
	// to port 4318. It is the default protocol, as in the OpenTelemetry
	// specification.
	//
	//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
	//		instrumentation.Protocol(oteltelemetry.ProtocolHTTP)
	//	})
	//	_ = driver
	ProtocolHTTP Protocol = "http/protobuf"
)

// settings configures a Driver without owning its native dependencies. Empty
// connection settings retain the native environment chain. Runtime metrics and
// global error handling start enabled before callbacks; explicit false wins.
type settings struct {
	// Endpoint is the collector address of every signal, either host:port or
	// a full URL such as https://collector:4318 (http disables TLS, and HTTP
	// exports append /v1/traces, /v1/metrics, /v1/logs). Empty uses
	// OTEL_EXPORTER_OTLP_ENDPOINT.
	Endpoint string

	// Protocol is the OTLP transport. Empty uses OTEL_EXPORTER_OTLP_PROTOCOL
	// and then [ProtocolHTTP].
	Protocol Protocol

	// Insecure disables TLS towards the collector. False defers to
	// OTEL_EXPORTER_OTLP_INSECURE and the endpoint scheme.
	Insecure bool

	// Headers are extra request headers or gRPC metadata sent with every
	// export, such as an API key. Empty uses OTEL_EXPORTER_OTLP_HEADERS.
	Headers map[string]string

	// Sampler decides which traces are recorded. Nil uses the SDK default,
	// which reads OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG.
	Sampler sdktrace.Sampler

	// TracerProvider replaces the provider the driver would build. The caller
	// owns it: the driver flushes it on shutdown but never shuts it down, and
	// ignores Endpoint, Protocol, Insecure, Headers, and Sampler for traces.
	TracerProvider trace.TracerProvider

	// MeterProvider replaces the provider the driver would build. The caller
	// owns it, as with TracerProvider.
	MeterProvider metric.MeterProvider

	// LoggerProvider replaces the provider the driver would build. The caller
	// owns it, as with TracerProvider.
	LoggerProvider log.LoggerProvider

	// Propagator reads and writes trace context. Nil uses W3C Trace Context
	// together with W3C Baggage.
	Propagator propagation.TextMapPropagator

	// RuntimeMetrics reports Go runtime metrics (memory, GC, goroutines)
	// through the meter provider. [New] enables it.
	RuntimeMetrics bool

	// GlobalErrorHandler routes OpenTelemetry SDK errors, which the SDK
	// reports through a process-wide handler, to the application's logger.
	// It calls [go.opentelemetry.io/otel.SetErrorHandler], the only way to
	// observe errors of asynchronous exporters. [New] enables it.
	GlobalErrorHandler bool
}

// Builder holds optional construction settings for [New]. Retained drafts
// cannot change a driver; native providers and hooks remain shared handles.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Endpoint("collector:4318")
//	})
//	_ = driver
type Builder struct {
	// config stores the draft independently from the published driver.
	config settings
}

// newConfig applies construction policies once to one fresh draft and detaches
// the completed root and headers. Recommended booleans precede caller edits;
// SDK validation remains deferred to Start. Nil supplied callbacks panic.
func newConfig(configure ...BuilderFunc) settings {
	telemetry := &Builder{
		config: settings{
			RuntimeMetrics:     true,
			GlobalErrorHandler: true,
		},
	}

	for _, configure := range configure {
		configure(telemetry)
	}

	config := telemetry.config
	config.Headers = maps.Clone(config.Headers)

	return config
}

// native converts completed driver settings without replaying construction
// policies. Protocol retains its public defined identity in this package; only
// its wire value crosses the private pipeline boundary. Headers are already
// detached at publication, while native handles intentionally remain shared.
func (settings settings) native() sdk.Config {
	return sdk.Config{
		Endpoint:           settings.Endpoint,
		Protocol:           string(settings.Protocol),
		Insecure:           settings.Insecure,
		Headers:            settings.Headers,
		Sampler:            settings.Sampler,
		TracerProvider:     settings.TracerProvider,
		MeterProvider:      settings.MeterProvider,
		LoggerProvider:     settings.LoggerProvider,
		Propagator:         settings.Propagator,
		RuntimeMetrics:     settings.RuntimeMetrics,
		GlobalErrorHandler: settings.GlobalErrorHandler,
	}
}

// Endpoint sets the collector address; empty retains the SDK environment chain.
// Validation and exporter creation remain deferred until Start. Explicit URLs
// must use HTTP(S) without credentials, nonempty query, or fragment; use Headers
// for exporter authentication. URL errors do not echo the supplied address.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Endpoint("collector:4318")
//	})
//	_ = driver
func (instrumentation *Builder) Endpoint(endpoint string) *Builder {
	instrumentation.config.Endpoint = endpoint

	return instrumentation
}

// Protocol selects the transport. Empty uses environment then ProtocolHTTP;
// unsupported values fail at Start, not during construction.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Protocol(oteltelemetry.ProtocolGRPC)
//	})
//	_ = driver
func (instrumentation *Builder) Protocol(protocol Protocol) *Builder {
	instrumentation.config.Protocol = protocol

	return instrumentation
}

// Insecure disables TLS when true. False retains the existing SDK environment
// and endpoint-scheme behavior; it does not force TLS over an HTTP endpoint.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Insecure(true)
//	})
//	_ = driver
func (instrumentation *Builder) Insecure(insecure bool) *Builder {
	instrumentation.config.Insecure = insecure

	return instrumentation
}

// Headers replaces exporter headers with a copy of headers. Empty retains the
// SDK environment fallback. Publication takes another independent map copy.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Headers(map[string]string{"X-Tenant": "operations"})
//	})
//	_ = driver
func (instrumentation *Builder) Headers(headers map[string]string) *Builder {
	instrumentation.config.Headers = maps.Clone(headers)

	return instrumentation
}

// Sampler retains the SDK sampler without invoking it. Nil uses SDK defaults;
// an externally supplied tracer provider ignores this setting.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Sampler(sdktrace.AlwaysSample())
//	})
//	_ = driver
func (instrumentation *Builder) Sampler(sampler sdktrace.Sampler) *Builder {
	instrumentation.config.Sampler = sampler

	return instrumentation
}

// TracerProvider retains a caller-owned provider. Stop flushes but never shuts
// it down. Nil restores driver-owned provider creation at Start.
//
//	provider := sdktrace.NewTracerProvider()
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.TracerProvider(provider)
//	})
//	_ = driver
func (instrumentation *Builder) TracerProvider(provider trace.TracerProvider) *Builder {
	instrumentation.config.TracerProvider = provider

	return instrumentation
}

// MeterProvider retains a caller-owned provider. Stop flushes but never shuts
// it down. Driver-installed runtime callbacks are unregistered on Stop or failed
// Start. Nil restores driver-owned provider creation at Start; typed-nil pointers
// are invalid. The caller owns final native provider shutdown.
//
//	provider := sdkmetric.NewMeterProvider()
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.MeterProvider(provider)
//	})
//	_ = driver
func (instrumentation *Builder) MeterProvider(provider metric.MeterProvider) *Builder {
	instrumentation.config.MeterProvider = provider

	return instrumentation
}

// LoggerProvider retains a caller-owned provider. Stop flushes but never shuts
// it down. Nil restores driver-owned provider creation at Start.
//
//	provider := sdklog.NewLoggerProvider()
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.LoggerProvider(provider)
//	})
//	_ = driver
func (instrumentation *Builder) LoggerProvider(provider log.LoggerProvider) *Builder {
	instrumentation.config.LoggerProvider = provider

	return instrumentation
}

// Propagator retains the native context propagator without invoking it. Nil
// restores W3C Trace Context together with W3C Baggage at Start.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.Propagator(propagation.TraceContext{})
//	})
//	_ = driver
func (instrumentation *Builder) Propagator(propagator propagation.TextMapPropagator) *Builder {
	instrumentation.config.Propagator = propagator

	return instrumentation
}

// RuntimeMetrics controls Go runtime instrumentation. New starts with it enabled;
// false explicitly disables it and is not overwritten by defaults. Callback
// registrations belong to the driver even when MeterProvider is borrowed; Stop
// releases them without shutting down that provider.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.RuntimeMetrics(false)
//	})
//	_ = driver
func (instrumentation *Builder) RuntimeMetrics(enabled bool) *Builder {
	instrumentation.config.RuntimeMetrics = enabled

	return instrumentation
}

// GlobalErrorHandler controls installation of the process-wide SDK error handler
// at Start. New enables it initially; false explicitly leaves the handler alone.
// The installed handler is not restored on Stop or startup failure; applications
// with a process-wide SDK logging policy should set false and own that policy.
//
//	driver := oteltelemetry.New(func(instrumentation *oteltelemetry.Builder) {
//		instrumentation.GlobalErrorHandler(false)
//	})
//	_ = driver
func (instrumentation *Builder) GlobalErrorHandler(enabled bool) *Builder {
	instrumentation.config.GlobalErrorHandler = enabled

	return instrumentation
}
