package semconv

// SecretOperation is the bounded operation label; it never contains a name
// or version. Example: telemetry.String(semconv.SecretOperation, "get").
const SecretOperation = "arkoren.secret.operation"

// SecretOperations counts facade provider operations, not typed decodes.
// Example: meter.Counter(semconv.SecretOperations).
const SecretOperations = "arkoren.secret.operations"

// SecretOperationDuration measures provider-operation latency in seconds.
// Example: meter.Histogram(semconv.SecretOperationDuration, telemetry.Unit("s")).
const SecretOperationDuration = "arkoren.secret.operation.duration"
