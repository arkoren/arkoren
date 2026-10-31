package sdk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"arkoren.dev/core/service/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// NewResource describes the process for the exporters. It starts from an
// "unknown_service" name, lets OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES
// override it, and finally applies the non-empty fields of the telemetry
// resource, which win over the environment.
func NewResource(ctx context.Context, described telemetry.Resource) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("unknown_service:"+filepath.Base(os.Args[0]))),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(resourceAttributes(described)...),
	)

	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	return res, nil
}

// resourceAttributes maps the non-empty fields of described onto OpenTelemetry
// attributes.
func resourceAttributes(described telemetry.Resource) []attribute.KeyValue {
	var attrs []attribute.KeyValue

	if described.Service != "" {
		attrs = append(attrs, semconv.ServiceName(described.Service))
	}

	if described.Version != "" {
		attrs = append(attrs, semconv.ServiceVersion(described.Version))
	}

	if described.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentName(described.Environment))
	}

	return append(attrs, Attrs(described.Attrs)...)
}
